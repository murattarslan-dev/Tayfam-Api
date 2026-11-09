package jobs

import (
	"context"
	"fmt"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AlertOverdueHikes raises the overdue alarm (v14 "Yürüyüşte beklenen
// dönüş") and returns how many hikers it was raised for.
//
// A hiker is overdue when they started (they have a check-in row), did not
// return, and the hike's expected_return_at + return_grace_minutes has
// passed. Each hiker gets one hike.overdue event and alert_sent_at is set
// in the same transaction, so the alarm is never sent twice. The alarm goes
// out even when the event was cancelled or deleted: the person may still be
// out.
//
// The event carries IDs only: chat (#64) writes the high-priority
// hike_overdue notification for recipient_ids, which muting cannot block.
// Recipients are the organizer, if still an active member, and the tayfa's
// active admins and moderators, never the hiker.
func (s *Service) AlertOverdueHikes(ctx context.Context) (int, error) {
	now := s.now()
	return s.drain(ctx, JobHikeAlert, hikeBatch, func(q *store.Queries) (int, error) {
		rows, err := q.LockOverdueHikers(ctx, store.LockOverdueHikersParams{Now: ts(now), N: hikeBatch})
		if err != nil {
			return 0, fmt.Errorf("gecikenler: %w", err)
		}
		type alert struct {
			tayfa, event id.ID
			payload      map[string]any
		}
		alerts := make([]alert, 0, len(rows))
		for _, r := range rows {
			n, err := q.MarkHikeAlertSent(ctx, store.MarkHikeAlertSentParams{Now: ts(now), EventID: r.EventID, UserID: r.UserID})
			if err != nil {
				return 0, fmt.Errorf("alarm zamanı: %w", err)
			}
			if n != 1 {
				continue
			}
			recipients, err := q.ListHikeAlertRecipients(ctx, store.ListHikeAlertRecipientsParams{
				TayfaID: r.TayfaID, HikerID: r.UserID, OrganizerID: r.CreatedBy, N: maxRecipients,
			})
			if err != nil {
				return 0, fmt.Errorf("alıcılar: %w", err)
			}
			tf, ev := id.ID(r.TayfaID.Bytes), id.ID(r.EventID.Bytes)
			p := map[string]any{
				"tayfa_id":      tf.String(),
				"event_id":      ev.String(),
				"user_id":       id.ID(r.UserID.Bytes).String(),
				"recipient_ids": ids(recipients),
			}
			if r.CreatedBy.Valid {
				p["organizer_id"] = id.ID(r.CreatedBy.Bytes).String()
			}
			alerts = append(alerts, alert{tayfa: tf, event: ev, payload: p})
		}
		// Events last (outbox.Write).
		for _, a := range alerts {
			if err := txn.Event(ctx, q, EventHikeOverdue, a.tayfa, id.ID{}, txn.Target{Type: "event", ID: a.event}, a.payload); err != nil {
				return 0, err
			}
		}
		return len(alerts), nil
	})
}

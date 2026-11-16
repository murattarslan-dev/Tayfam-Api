package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// JobSafetyAlert raises the safety contact alarm (issue #210, ADR-0033).
const JobSafetyAlert = "safety-alert"

// DefaultSafetyAlertInterval: every minute; the contact is told soon after
// the grace time runs out.
const DefaultSafetyAlertInterval = time.Minute

// Retention of the Keşfet safety net (ADR-0033 §13).
const (
	// SafetyContactRetention: safety contacts go this long after the
	// event's end.
	SafetyContactRetention = 30 * 24 * time.Hour
	// FeedbackRetention: "Was everything all right?" answers go after a
	// year.
	FeedbackRetention = 365 * 24 * time.Hour
	// DiscoverJoinRetention: a Keşfet join mark goes this long after the
	// membership ended (discover.FormerMemberWindow).
	DiscoverJoinRetention = 30 * 24 * time.Hour
)

const safetyBatch = 100

// AlertMissedReturns tells each chosen safety contact whose participant did
// not mark "back" by the event's end plus their grace time, and returns
// how many alerts it raised.
//
// Only the chosen contact gets it, and only while the two are still
// friends with no block between them (LockDueSafetyAlerts). alert_sent_at
// is set in the same transaction, so an alert never goes out twice. An
// event cancelled or deleted before the start alerts only if the
// participant checked in. The notification carries the participant's ID
// only: no tayfa, no event content, no location (the contact reads the
// shared event from ListSafetyShares). Nothing is logged but the count.
func (s *Service) AlertMissedReturns(ctx context.Context) (int, error) {
	now := s.now()
	return s.drain(ctx, JobSafetyAlert, safetyBatch, func(q *store.Queries) (int, error) {
		rows, err := q.LockDueSafetyAlerts(ctx, store.LockDueSafetyAlertsParams{Now: ts(now), N: safetyBatch})
		if err != nil {
			return 0, fmt.Errorf("güvenlik alarmları: %w", err)
		}
		type alert struct{ participant, contact id.ID }
		alerts := make([]alert, 0, len(rows))
		for _, r := range rows {
			n, err := q.MarkSafetyAlertSent(ctx, store.MarkSafetyAlertSentParams{Now: ts(now), EventID: r.EventID, UserID: r.UserID})
			if err != nil {
				return 0, fmt.Errorf("alarm zamanı: %w", err)
			}
			if n == 1 {
				alerts = append(alerts, alert{participant: id.ID(r.UserID.Bytes), contact: id.ID(r.ContactUserID.Bytes)})
			}
		}
		// Events last (outbox.Write).
		for _, a := range alerts {
			if err := notify.Write(ctx, q, notify.Request{
				Type: notify.TypeSafetyAlert, TargetType: notify.TargetUser, Target: a.participant,
			}, []id.ID{a.contact}); err != nil {
				return 0, err
			}
		}
		return len(alerts), nil
	})
}

// expireDiscover deletes old safety contacts, feedback and Keşfet join
// marks.
func (s *Service) expireDiscover(ctx context.Context, q *store.Queries) (int, error) {
	nc, err := q.DeleteOldSafetyContacts(ctx, ts(s.now().Add(-SafetyContactRetention)))
	if err != nil {
		return 0, fmt.Errorf("güvenlik kişisi saklama: %w", err)
	}
	nf, err := q.DeleteOldEventFeedback(ctx, ts(s.now().Add(-FeedbackRetention)))
	if err != nil {
		return int(nc), fmt.Errorf("geri bildirim saklama: %w", err) //nolint:gosec // row count
	}
	nj, err := q.DeleteOldDiscoverJoins(ctx, ts(s.now().Add(-DiscoverJoinRetention)))
	if err != nil {
		return int(nc + nf), fmt.Errorf("keşfet işareti saklama: %w", err) //nolint:gosec // row counts
	}
	return int(nc + nf + nj), nil //nolint:gosec // row counts
}

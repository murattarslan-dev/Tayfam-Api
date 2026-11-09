package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// CleanupTayfas runs one pass of the inactive tayfa cleanup (project plan
// §7, v14 "Tayfa yönetimi") and returns how many tayfas changed state:
//
//  1. A warned tayfa with activity after the warning (last_activity_at >
//     warned_at) becomes active again: tayfa.reactivated.
//  2. A warned tayfa without activity for WarningPeriod is archived
//     (read-only): tayfa.archived.
//  3. An active tayfa is warned (tayfa.warned) when its last activity is
//     older than InactivityPeriod (reason inactive), or when it is at least
//     NewTayfaPeriod old and has fewer than MinActiveMembers active members
//     (reason few_members; once per tayfa, see WarnTayfas).
//
// Suspended tayfas and tayfas waiting for deletion are neither warned nor
// archived. Archiving is not undone by the job.
func (s *Service) CleanupTayfas(ctx context.Context) (int, error) {
	now := s.now()
	total := 0

	n, err := s.drain(ctx, JobTayfaCleanup, tayfaBatch, func(q *store.Queries) (int, error) {
		rows, err := q.ReactivateWarnedTayfas(ctx, tayfaBatch)
		if err != nil {
			return 0, fmt.Errorf("yeniden etkinleştirme: %w", err)
		}
		return len(rows), s.recordTayfas(ctx, q, rows, EventTayfaReactivated, "", func(id.ID) (map[string]any, []byte, []id.ID, error) {
			return map[string]any{}, nil, nil, nil
		})
	})
	total += n
	if err != nil {
		return total, err
	}

	n, err = s.drain(ctx, JobTayfaCleanup, tayfaBatch, func(q *store.Queries) (int, error) {
		rows, err := q.ArchiveWarnedTayfas(ctx, store.ArchiveWarnedTayfasParams{
			Now: ts(now), WarnedBefore: ts(now.Add(-WarningPeriod)), N: tayfaBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("arşivleme: %w", err)
		}
		return len(rows), s.recordTayfas(ctx, q, rows, EventTayfaArchived, notify.TypeTayfaArchived, func(tf id.ID) (map[string]any, []byte, []id.ID, error) {
			staff, err := staffOf(ctx, q, tf)
			if err != nil {
				return nil, nil, nil, err
			}
			return map[string]any{}, nil, staff, nil
		})
	})
	total += n
	if err != nil {
		return total, err
	}

	n, err = s.drain(ctx, JobTayfaCleanup, tayfaBatch, func(q *store.Queries) (int, error) {
		rows, err := q.WarnTayfas(ctx, store.WarnTayfasParams{
			Now:            ts(now),
			InactiveBefore: ts(now.Add(-InactivityPeriod)),
			CreatedBefore:  ts(now.Add(-NewTayfaPeriod)),
			MinMembers:     MinActiveMembers,
			N:              tayfaBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("uyarı: %w", err)
		}
		reason := make(map[id.ID]string, len(rows))
		tayfas := make([]pgtype.UUID, 0, len(rows))
		for _, r := range rows {
			tf := id.ID(r.ID.Bytes)
			reason[tf] = ReasonFewMembers
			if r.Inactive {
				reason[tf] = ReasonInactive
			}
			tayfas = append(tayfas, r.ID)
		}
		return len(rows), s.recordTayfas(ctx, q, tayfas, EventTayfaWarned, notify.TypeTayfaWarned, func(tf id.ID) (map[string]any, []byte, []id.ID, error) {
			staff, err := staffOf(ctx, q, tf)
			if err != nil {
				return nil, nil, nil, err
			}
			// The audit reason is what WarnTayfas reads to warn for
			// few_members only once.
			meta, err := json.Marshal(map[string]string{"reason": reason[tf]})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("json: %w", err)
			}
			return map[string]any{"reason": reason[tf]}, meta, staff, nil
		})
	})
	total += n
	return total, err
}

// DeleteDueTayfas soft deletes the tayfas whose deletion wait (72 hours,
// v14 "Tayfa silmede bekleme") is over and returns how many. A cancelled
// request has no deletion_scheduled_for and is not touched. The tayfa is
// purged RetentionPeriod later (Purge).
func (s *Service) DeleteDueTayfas(ctx context.Context) (int, error) {
	now := s.now()
	return s.drain(ctx, JobTayfaDeletion, tayfaBatch, func(q *store.Queries) (int, error) {
		rows, err := q.SoftDeleteDueTayfas(ctx, store.SoftDeleteDueTayfasParams{Now: ts(now), N: tayfaBatch})
		if err != nil {
			return 0, fmt.Errorf("tayfa silme: %w", err)
		}
		return len(rows), s.recordTayfas(ctx, q, rows, EventTayfaDeleted, "", func(id.ID) (map[string]any, []byte, []id.ID, error) {
			return map[string]any{}, nil, nil, nil
		})
	})
}

// recordTayfas writes, for each tayfa, a system audit record (action =
// event type) and then the outbox events: events last, as outbox.Write
// asks. payload returns the event payload (tayfa_id is added), the audit
// metadata (nil means {}) and, when notice is a notification type, the
// recipients of that notification: they go in notification.requested
// events after the tayfa events (ADR-0009, issue #64).
func (s *Service) recordTayfas(ctx context.Context, q *store.Queries, rows []pgtype.UUID, event, notice string, payload func(id.ID) (map[string]any, []byte, []id.ID, error)) error {
	payloads := make([]map[string]any, len(rows))
	recipients := make([][]id.ID, len(rows))
	for i, r := range rows {
		tf := id.ID(r.Bytes)
		p, meta, to, err := payload(tf)
		if err != nil {
			return err
		}
		p["tayfa_id"] = tf.String()
		payloads[i], recipients[i] = p, to
		if err := systemAudit(ctx, q, tf, event, txn.Target{Type: "tayfa", ID: tf}, meta); err != nil {
			return err
		}
	}
	for i, r := range rows {
		tf := id.ID(r.Bytes)
		if err := txn.Event(ctx, q, event, tf, id.ID{}, txn.Target{Type: "tayfa", ID: tf}, payloads[i]); err != nil {
			return err
		}
	}
	if notice == "" {
		return nil
	}
	for i, r := range rows {
		tf := id.ID(r.Bytes)
		req := notify.Request{Type: notice, Tayfa: tf, TargetType: notify.TargetTayfa, Target: tf}
		if reason, ok := payloads[i]["reason"].(string); ok {
			req.Extra = map[string]string{notify.ExtraReason: reason}
		}
		if err := notify.Write(ctx, q, req, recipients[i]); err != nil {
			return err
		}
	}
	return nil
}

// staffOf returns the tayfa's active admins and moderators, admins first:
// the recipients of warning and archive notifications.
func staffOf(ctx context.Context, q *store.Queries, tf id.ID) ([]id.ID, error) {
	us, err := q.ListTayfaStaffIDs(ctx, store.ListTayfaStaffIDsParams{TayfaID: tf.PG(), N: maxRecipients})
	if err != nil {
		return nil, fmt.Errorf("yöneticiler: %w", err)
	}
	return idList(us), nil
}

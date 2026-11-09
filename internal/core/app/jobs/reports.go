package jobs

import (
	"context"
	"fmt"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EscalateReports sends reports that stayed open for report.EscalationAfter
// to the platform (v14 "Platforma yükselen şikâyetler": "Tayfa içinde 7 gün
// boyunca çözülmeyen şikâyetler") and returns how many it escalated. Each
// gets a report.escalated event without an actor (escalation_reason
// unresolved), the platform panel's hook. The tayfa can still resolve the
// report afterwards.
func (s *Service) EscalateReports(ctx context.Context) (int, error) {
	now := s.now()
	return s.drain(ctx, JobReportEscalation, reportBatch, func(q *store.Queries) (int, error) {
		rows, err := q.EscalateUnresolvedReports(ctx, store.EscalateUnresolvedReportsParams{
			Now: ts(now), CreatedBefore: ts(now.Add(-report.EscalationAfter)), N: reportBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("şikâyet yükseltme: %w", err)
		}
		for _, r := range rows {
			tf, rid := id.ID(r.TayfaID.Bytes), id.ID(r.ID.Bytes)
			if err := txn.Event(ctx, q, report.EventEscalated, tf, id.ID{}, txn.Target{Type: "report", ID: rid}, map[string]any{
				"tayfa_id": tf.String(), "report_id": rid.String(),
				"escalation_reason": report.EscalationUnresolved, "priority": r.Priority,
			}); err != nil {
				return 0, err
			}
		}
		return len(rows), nil
	})
}

// ExpireRecords deletes what v14 "Saklama süreleri" keeps for a year:
// reports resolved report.Retention ago (unless the platform still has them
// open) and audit records older than that. It returns how many rows it
// deleted. audit_logs only accept the deletion in a transaction that set
// app.retention_job and only for records older than 365 days (migration
// 00011).
func (s *Service) ExpireRecords(ctx context.Context) (int, error) {
	before := ts(s.now().Add(-report.Retention))
	reports, err := s.drain(ctx, JobRetention, retentionBatch, func(q *store.Queries) (int, error) {
		n, err := q.DeleteExpiredReports(ctx, store.DeleteExpiredReportsParams{HandledBefore: before, N: retentionBatch})
		if err != nil {
			return 0, fmt.Errorf("şikâyet saklama: %w", err)
		}
		return int(n), nil //nolint:gosec // at most one batch
	})
	if err != nil {
		return reports, err
	}
	audits, err := s.drain(ctx, JobRetention, retentionBatch, func(q *store.Queries) (int, error) {
		if err := q.SetAuditRetentionJob(ctx); err != nil {
			return 0, fmt.Errorf("saklama işi: %w", err)
		}
		n, err := q.DeleteExpiredAuditLogs(ctx, store.DeleteExpiredAuditLogsParams{CreatedBefore: before, N: retentionBatch})
		if err != nil {
			return 0, fmt.Errorf("denetim kaydı saklama: %w", err)
		}
		return int(n), nil //nolint:gosec // at most one batch
	})
	return reports + audits, err
}

package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// Stats are the panel dashboard's tayfa counts and background job runs
// (issue #110; Notion "Platform Yönetim Paneli → Pano"). Counts and times
// only: no tayfa ID, name or member.
type Stats struct {
	Total, Active, Suspended, Warned, Archived int64
	Jobs                                       []JobRun
}

// JobRun is the last finished run of a background job (job_runs).
type JobRun struct {
	Job        string
	FinishedAt time.Time
	Succeeded  bool
	Changed    int64
	// LastSuccessAt is zero if no run succeeded.
	LastSuccessAt time.Time
}

// GetStats returns the dashboard counts. The caller must still be a
// platform administrator.
func (s *Service) GetStats(ctx context.Context, c auth.AdminClaims) (Stats, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return Stats{}, err
	}
	t, err := s.q.AdminTayfaStats(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("tayfa sayıları: %w", err)
	}
	runs, err := s.q.AdminListJobRuns(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("iş çalışmaları: %w", err)
	}
	out := Stats{Total: t.Total, Active: t.Active, Suspended: t.Suspended, Warned: t.Warned, Archived: t.Archived,
		Jobs: make([]JobRun, 0, len(runs))}
	for _, r := range runs {
		out.Jobs = append(out.Jobs, JobRun{
			Job: r.Job, FinishedAt: timeOf(r.FinishedAt), Succeeded: r.Succeeded, Changed: r.Changed,
			LastSuccessAt: timeOf(r.LastSucceededAt),
		})
	}
	return out, nil
}

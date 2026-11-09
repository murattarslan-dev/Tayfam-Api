package admin

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
)

func TestGetStats(t *testing.T) {
	e := setup(t)
	_, suspended, warned, archived, deleted := e.crew(t), e.crew(t), e.crew(t), e.crew(t), e.crew(t)
	must(t, e.svc.SuspendTayfa(ctx, e.claims, suspended.tayfa, reason))
	_, err := e.pool.Exec(ctx, `UPDATE tayfas SET status = 'warned', warned_at = now() WHERE id = $1`, warned.tayfa.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `UPDATE tayfas SET status = 'archived', warned_at = now(), archived_at = now() WHERE id = $1`, archived.tayfa.PG())
	must(t, err)
	must(t, e.svc.DeleteTayfa(ctx, e.claims, deleted.tayfa, reason))
	finished := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	_, err = e.pool.Exec(ctx, `INSERT INTO job_runs (job, finished_at, succeeded, changed, last_succeeded_at)
		VALUES ('tayfa-cleanup', $1, true, 4, $1), ('purge', $1, false, 0, NULL)`, finished)
	must(t, err)

	st, err := e.svc.GetStats(ctx, e.claims)
	must(t, err)
	if st.Total != 4 || st.Active != 1 || st.Suspended != 1 || st.Warned != 1 || st.Archived != 1 {
		t.Fatalf("sayılar = %+v", st)
	}
	want := []JobRun{
		{Job: "purge", FinishedAt: finished},
		{Job: "tayfa-cleanup", FinishedAt: finished, Succeeded: true, Changed: 4, LastSuccessAt: finished},
	}
	if !slices.Equal(st.Jobs, want) {
		t.Fatalf("işler = %+v", st.Jobs)
	}

	e.checker.err = apperr.ErrNotPlatformAdmin
	_, err = e.svc.GetStats(ctx, e.claims)
	is(t, "yönetici değil", err, apperr.ErrNotPlatformAdmin)
}

// TestStatsAreCountsOnly: the dashboard gets numbers and job runs, never a
// tayfa ID, name or member. A new field must be added here on purpose.
func TestStatsAreCountsOnly(t *testing.T) {
	var fields []string
	for _, typ := range []reflect.Type{reflect.TypeFor[Stats](), reflect.TypeFor[JobRun]()} {
		for f := range typ.Fields() {
			fields = append(fields, typ.Name()+"."+f.Name)
		}
	}
	want := []string{
		"Stats.Total", "Stats.Active", "Stats.Suspended", "Stats.Warned", "Stats.Archived", "Stats.Jobs",
		"JobRun.Job", "JobRun.FinishedAt", "JobRun.Succeeded", "JobRun.Changed", "JobRun.LastSuccessAt",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("pano alanları değişti: %v", fields)
	}
}

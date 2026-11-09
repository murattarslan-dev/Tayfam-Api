package jobs

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// addReport writes a report created at created; resolved reports were
// handled at handled. escalated marks it as already on the platform.
func (e *env) addReport(t *testing.T, tf id.ID, created time.Time, handled *time.Time, platformOpen bool) id.ID {
	t.Helper()
	r := id.New()
	status, resolution, handledAt := "open", any(nil), any(nil)
	if handled != nil {
		status, resolution, handledAt = "resolved", "dismissed", *handled
	}
	esc, reason, pstatus := any(nil), any(nil), any(nil)
	if platformOpen {
		esc, reason, pstatus = created, "child_safety", "open"
	}
	e.exec(t, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason, status, resolution,
		handled_at, created_at, escalated_to_platform_at, escalation_reason, platform_status)
		VALUES ($1, $2, $3, 'user', $4, $4, 'spam', $5, $6, $7, $8, $9, $10, $11)`,
		r.PG(), tf.PG(), id.New().PG(), id.New().PG(), status, resolution, handledAt, created, esc, reason, pstatus)
	return r
}

func (e *env) escalated(t *testing.T, r id.ID) (bool, string) {
	t.Helper()
	var reason *string
	must(t, e.pool.QueryRow(ctx, `SELECT escalation_reason FROM reports WHERE id = $1`, r.PG()).Scan(&reason))
	if reason == nil {
		return false, ""
	}
	return true, *reason
}

// TestEscalateReports: reports open for seven days go to the platform once,
// with a report.escalated event; resolved, younger and already escalated
// reports stay as they are.
func TestEscalateReports(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	old := e.base.Add(-report.EscalationAfter - time.Minute)
	due := e.addReport(t, tf, old, nil, false)
	young := e.addReport(t, tf, e.base.Add(-report.EscalationAfter+time.Hour), nil, false)
	handled := e.base.Add(-time.Hour)
	resolved := e.addReport(t, tf, old, &handled, false)
	already := e.addReport(t, tf, old, nil, true)

	run(t, "yükseltme", e.at(t, 0).EscalateReports, 1)
	if ok, reason := e.escalated(t, due); !ok || reason != report.EscalationUnresolved {
		t.Fatalf("7 günlük şikâyet: %v %q", ok, reason)
	}
	for name, r := range map[string]id.ID{"yeni": young, "çözülmüş": resolved} {
		if ok, _ := e.escalated(t, r); ok {
			t.Errorf("%s şikâyet yükseltildi", name)
		}
	}
	if _, reason := e.escalated(t, already); reason != "child_safety" {
		t.Errorf("zaten yükselmiş şikâyetin sebebi değişti: %q", reason)
	}
	evs := e.events(t, report.EventEscalated)
	if len(evs) != 1 || evs[0].subject != due || evs[0].tayfa != tf || evs[0].payload["escalation_reason"] != report.EscalationUnresolved {
		t.Fatalf("report.escalated = %+v", evs)
	}
	if _, ok := evs[0].payload["reporter_id"]; ok {
		t.Error("olayda şikâyet eden var")
	}
	run(t, "tekrar", e.at(t, time.Minute).EscalateReports, 0)
}

// TestExpireRecords: resolved reports and audit records go after a year;
// open reports, reports the platform still has open and younger records
// stay. Outside the retention job the audit log stays append-only.
func TestExpireRecords(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	longAgo := e.base.Add(-report.Retention - 24*time.Hour)
	recent := e.base.Add(-24 * time.Hour)
	expired := e.addReport(t, tf, longAgo, &longAgo, false)
	kept := e.addReport(t, tf, recent, &recent, false)
	open := e.addReport(t, tf, longAgo, nil, false)
	onPlatform := e.addReport(t, tf, longAgo, &longAgo, true)

	oldAudit, newAudit := id.New(), id.New()
	e.exec(t, `INSERT INTO audit_logs (id, tayfa_id, actor_id, action, created_at) VALUES ($1, $2, $3, 'tayfa.updated', $4)`,
		oldAudit.PG(), tf.PG(), admin.PG(), longAgo)
	e.exec(t, `INSERT INTO audit_logs (id, tayfa_id, actor_id, action, created_at) VALUES ($1, $2, $3, 'tayfa.updated', $4)`,
		newAudit.PG(), tf.PG(), admin.PG(), recent)

	// Without the retention job's setting the trigger refuses.
	if _, err := e.pool.Exec(ctx, `DELETE FROM audit_logs WHERE id = $1`, oldAudit.PG()); err == nil {
		t.Fatal("saklama işi dışında denetim kaydı silindi")
	}

	run(t, "saklama", e.at(t, 0).ExpireRecords, 2)
	for name, tc := range map[string]struct {
		r    id.ID
		want int
	}{"süresi dolan": {expired, 0}, "yeni çözülen": {kept, 1}, "açık": {open, 1}, "platformda açık": {onPlatform, 1}} {
		if n := e.count(t, `SELECT count(*) FROM reports WHERE id = $1`, tc.r.PG()); n != tc.want {
			t.Errorf("%s şikâyet: %d", name, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE id = $1`, oldAudit.PG()); n != 0 {
		t.Error("1 yıllık denetim kaydı kaldı")
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE id = $1`, newAudit.PG()); n != 1 {
		t.Error("yeni denetim kaydı silindi")
	}
	// Even with the setting, a young record cannot be deleted.
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('app.retention_job', 'audit_logs', true)`)
	must(t, err)
	if _, err := tx.Exec(ctx, `DELETE FROM audit_logs WHERE id = $1`, newAudit.PG()); err == nil {
		t.Fatal("saklama işi yeni denetim kaydını silebildi")
	}
}

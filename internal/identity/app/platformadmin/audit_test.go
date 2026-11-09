package platformadmin

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// coreEntry records an entry as core's relay would, at the given time.
func (e *env) coreEntry(t *testing.T, at time.Time, action, targetType string, target id.ID, meta map[string]string) id.ID {
	t.Helper()
	ev := id.New()
	err := e.svc.RecordFromCore(context.Background(), CoreEntry{
		EventID: ev, OccurredAt: at, Actor: e.admin,
		Entry: Entry{Action: action, TargetType: targetType, TargetID: target, Reason: reason, Metadata: meta},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func actions(p AuditPage) []string {
	out := make([]string, len(p.Entries))
	for i, en := range p.Entries {
		out[i] = en.Action
	}
	return out
}

func TestListAuditLogs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t) // writes admin.sign_in at the database's now()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tayfa, member, rep := id.New(), id.New(), id.New()
	e.coreEntry(t, base, "tayfa.suspend", "tayfa", tayfa, nil)
	e.coreEntry(t, base.Add(time.Hour), "membership.set_role", "tayfa", tayfa, map[string]string{"tayfa_id": tayfa.String(), "user_id": member.String()})
	e.coreEntry(t, base.Add(2*time.Hour), "report.decide", "report", rep, map[string]string{"tayfa_id": tayfa.String()})
	e.coreEntry(t, base.Add(3*time.Hour), "tayfa.unsuspend", "tayfa", id.New(), nil)

	all, err := e.svc.ListAuditLogs(ctx, c, AuditFilter{}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(all); !slices.Equal(got, []string{"admin.sign_in", "tayfa.unsuspend", "report.decide", "membership.set_role", "tayfa.suspend"}) || all.Next != "" {
		t.Fatalf("hepsi = %v next=%q", got, all.Next)
	}
	first := all.Entries[0]
	if first.Actor != e.admin || first.ActorName != "Yönetici" || first.Reason != signInReason || first.TargetType != "user" {
		t.Fatalf("giriş kaydı = %+v", first)
	}
	if d := all.Entries[3]; d.User != member || d.Tayfa != tayfa {
		t.Fatalf("metadata kimlikleri = %+v", d)
	}

	// Filters.
	for _, tc := range []struct {
		name string
		f    AuditFilter
		want []string
	}{
		{"işlem", AuditFilter{Action: "tayfa.suspend"}, []string{"tayfa.suspend"}},
		{"hedef türü", AuditFilter{TargetType: "report"}, []string{"report.decide"}},
		{"ilgili tayfa: hedef ve metadata", AuditFilter{Related: tayfa}, []string{"report.decide", "membership.set_role", "tayfa.suspend"}},
		{"ilgili kullanıcı: metadata", AuditFilter{Related: member}, []string{"membership.set_role"}},
		{"tarih aralığı", AuditFilter{Since: base.Add(time.Hour), Until: base.Add(3 * time.Hour)}, []string{"report.decide", "membership.set_role"}},
		{"eşleşme yok", AuditFilter{Action: "user.ban"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := e.svc.ListAuditLogs(ctx, c, tc.f, 0, "")
			if err != nil || !slices.Equal(actions(p), tc.want) {
				t.Fatalf("= %v %v", actions(p), err)
			}
		})
	}

	// Paging: newest first, the cursor continues the same filter only.
	f := AuditFilter{Related: tayfa}
	p1, err := e.svc.ListAuditLogs(ctx, c, f, 2, "")
	if err != nil || !slices.Equal(actions(p1), []string{"report.decide", "membership.set_role"}) || p1.Next == "" {
		t.Fatalf("sayfa 1 = %v %q %v", actions(p1), p1.Next, err)
	}
	p2, err := e.svc.ListAuditLogs(ctx, c, f, 2, p1.Next)
	if err != nil || !slices.Equal(actions(p2), []string{"tayfa.suspend"}) || p2.Next != "" {
		t.Fatalf("sayfa 2 = %v %q %v", actions(p2), p2.Next, err)
	}
	if _, err := e.svc.ListAuditLogs(ctx, c, AuditFilter{}, 2, p1.Next); !errors.Is(err, ErrValidation) {
		t.Fatalf("başka filtrenin imleci: %v", err)
	}
	for _, bad := range []string{"x", "!!!", "eyJmIjoiIn0"} {
		if _, err := e.svc.ListAuditLogs(ctx, c, f, 2, bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("bozuk imleç %q: %v", bad, err)
		}
	}

	// Validation.
	for _, bad := range []AuditFilter{
		{Action: "DROP TABLE"}, {TargetType: "message"},
		{Since: base, Until: base}, {Since: base.Add(time.Hour), Until: base},
	} {
		if _, err := e.svc.ListAuditLogs(ctx, c, bad, 0, ""); !errors.Is(err, ErrValidation) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if _, err := e.svc.ListAuditLogs(ctx, c, AuditFilter{}, -1, ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("negatif sayfa: %v", err)
	}
	big, err := e.svc.ListAuditLogs(ctx, c, AuditFilter{}, 1000, "")
	if err != nil || len(big.Entries) != 5 {
		t.Fatalf("büyük sayfa: %d %v", len(big.Entries), err)
	}
}

// TestAuditAndStatsNeedAdmin: a token whose user is not (or no longer) in
// platform_admins reads nothing.
func TestAuditAndStatsNeedAdmin(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)
	other := e.user(t, "g-other")
	for _, claims := range []auth.AdminClaims{
		{Subject: other.String(), SessionID: id.New().String()},
		{Subject: "kötü", SessionID: id.New().String()},
	} {
		if _, err := e.svc.ListAuditLogs(ctx, claims, AuditFilter{}, 0, ""); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("denetim: %v", err)
		}
		if _, err := e.svc.UserStats(ctx, claims); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("sayılar: %v", err)
		}
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM platform_admins`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ListAuditLogs(ctx, c, AuditFilter{}, 0, ""); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("satırı silinen yönetici: %v", err)
	}
	if _, err := e.svc.UserStats(ctx, c); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("satırı silinen yönetici: %v", err)
	}
}

// TestAuditEntryHasNoPersonalData: the entry the panel gets has IDs, the
// action, the reason and the administrator's display name only. A new field
// must be added here on purpose.
func TestAuditEntryHasNoPersonalData(t *testing.T) {
	var fields []string
	for f := range reflect.TypeFor[AuditEntry]().Fields() {
		fields = append(fields, f.Name)
	}
	want := []string{"ID", "CreatedAt", "Actor", "ActorName", "Action", "TargetType", "TargetID", "Reason", "Tayfa", "User", "Report"}
	if !slices.Equal(fields, want) {
		t.Fatalf("denetim kaydı alanları değişti: %v", fields)
	}
	var stats []string
	for f := range reflect.TypeFor[UserStats]().Fields() {
		if f.Type.Kind() != reflect.Int64 {
			t.Fatalf("UserStats.%s sayı değil", f.Name)
		}
		stats = append(stats, f.Name)
	}
	if len(stats) != 6 {
		t.Fatalf("UserStats alanları: %v", stats)
	}
}

func TestUserStats(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.signedIn(t)

	active := e.user(t, "g-active")
	e.session(t, active)
	e.session(t, active) // counted once
	revoked := e.user(t, "g-revoked")
	if _, err := e.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now(), revoked_reason = 'logout' WHERE id = $1`, e.session(t, revoked).PG()); err != nil {
		t.Fatal(err)
	}
	stale := e.user(t, "g-stale")
	if _, err := e.pool.Exec(ctx, `UPDATE sessions SET last_refreshed_at = now() - interval '40 days' WHERE id = $1`, e.session(t, stale).PG()); err != nil {
		t.Fatal(err)
	}
	suspended := e.user(t, "g-suspended")
	e.session(t, suspended) // ended by the sanction
	if _, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: suspended, Type: SanctionSuspend, Reason: reason, EndsAt: e.now.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	banned := e.user(t, "g-banned")
	if _, err := e.svc.SanctionUser(ctx, c, SanctionInput{User: banned, Type: SanctionBan, Reason: reason}); err != nil {
		t.Fatal(err)
	}
	// Not registered: not counted.
	if err := e.q.CreateUser(ctx, store.CreateUserParams{ID: id.New().PG(), DisplayName: pgtype.Text{String: "Bekleyen", Valid: true}, Locale: "tr"}); err != nil {
		t.Fatal(err)
	}
	// Deleted: one recently, one past the retention period.
	recent, old := e.user(t, "g-deleted-1"), e.user(t, "g-deleted-2")
	for _, u := range []id.ID{recent, old} {
		if n, err := e.q.SoftDeleteUser(ctx, u.PG()); err != nil || n != 1 {
			t.Fatalf("silme: %d %v", n, err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET deleted_at = now() - interval '32 days' WHERE id = $1`, old.PG()); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.UserStats(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	// Registered and live: admin, g-plain (setup), active, revoked, stale,
	// suspended, banned.
	want := UserStats{Total: 7, Active: 1, Suspended: 1, Banned: 1, DeletionsPending: 2, DeletionsOverdue: 1}
	if got != want {
		t.Fatalf("sayılar = %+v, beklenen %+v", got, want)
	}
}

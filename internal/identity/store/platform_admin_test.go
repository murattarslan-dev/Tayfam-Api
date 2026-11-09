package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// setupWithOwner is setup plus a superuser pool for what only the platform
// owner does (adding administrators, docs/runbooks/platform-yoneticisi.md)
// and for trigger tests that model a caller with more privileges than
// identity_app.
func setupWithOwner(t *testing.T) (app, owner *pgxpool.Pool, q *Queries) {
	t.Helper()
	d := dbtest.Open(t, "identity", identitydb.Migrations())
	return d.App, d.Admin, New(d.App)
}

// TestNoPlatformAdminWrites: the application never adds, changes or removes
// platform administrators (Notion "Platform Yönetim Paneli", layer 4). Only
// the platform owner does, with one SQL command
// (docs/runbooks/platform-yoneticisi.md). No query may write to the table,
// and audit log rows are only ever inserted (and purged by retention).
func TestNoPlatformAdminWrites(t *testing.T) {
	files, err := filepath.Glob("../../../db/identity/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("sorgu dosyaları bulunamadı: %v", err)
	}
	adminWrite := regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from|truncate|merge\s+into)\s+(only\s+)?platform_admins\b`)
	auditChange := regexp.MustCompile(`(?is)\b(update|truncate|merge\s+into)\s+(only\s+)?platform_audit_logs\b`)
	auditDelete := regexp.MustCompile(`(?is)\bdelete\s+from\s+platform_audit_logs\b`)
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- test reads the repo's own query files
		if err != nil {
			t.Fatal(err)
		}
		sql := stripSQLComments(string(b))
		if adminWrite.MatchString(sql) {
			t.Errorf("%s platform_admins'e yazıyor", filepath.Base(f))
		}
		if auditChange.MatchString(sql) {
			t.Errorf("%s platform_audit_logs'u değiştiriyor", filepath.Base(f))
		}
		if auditDelete.MatchString(sql) && filepath.Base(f) != "retention.sql" {
			t.Errorf("%s platform_audit_logs'tan siliyor (yalnızca saklama işi silebilir)", filepath.Base(f))
		}
	}
}

func stripSQLComments(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func registeredUser(t *testing.T, q *Queries) id.ID {
	t.Helper()
	u := newUser(t, q)
	if n, err := q.CompleteRegistration(context.Background(), CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: fakeCiphertext}); err != nil || n != 1 {
		t.Fatalf("kayıt: n=%d err=%v", n, err)
	}
	return u
}

func TestGetPlatformAdmin(t *testing.T) {
	_, owner, q := setupWithOwner(t)
	ctx := context.Background()
	admin := registeredUser(t, q)
	plain := registeredUser(t, q)
	if err := q.CreateAuthIdentity(ctx, CreateAuthIdentityParams{ID: id.New().PG(), UserID: admin.PG(), Provider: "google", Subject: "g-admin"}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateAuthIdentity(ctx, CreateAuthIdentityParams{ID: id.New().PG(), UserID: plain.PG(), Provider: "google", Subject: "g-plain"}); err != nil {
		t.Fatal(err)
	}
	// What the runbook's command does, outside the service.
	if _, err := owner.Exec(ctx, `INSERT INTO platform_admins (user_id) VALUES ($1)`, admin.PG()); err != nil {
		t.Fatal(err)
	}

	got, err := q.GetPlatformAdminByIdentity(ctx, GetPlatformAdminByIdentityParams{Provider: "google", Subject: "g-admin"})
	if err != nil || got.ID != admin.PG() {
		t.Fatalf("yönetici bulunamadı: %v", err)
	}
	if _, err := q.GetPlatformAdminByIdentity(ctx, GetPlatformAdminByIdentityParams{Provider: "google", Subject: "g-plain"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("sıradan kullanıcı yönetici sayıldı: %v", err)
	}
	if _, err := q.GetPlatformAdmin(ctx, admin.PG()); err != nil {
		t.Fatal(err)
	}

	// A deleted account is no longer an administrator, even before the row
	// goes with the users row 30 days later.
	if n, err := q.SoftDeleteUser(ctx, admin.PG()); err != nil || n != 1 {
		t.Fatalf("silme: n=%d err=%v", n, err)
	}
	if _, err := q.GetPlatformAdmin(ctx, admin.PG()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("silinmiş hesap yönetici sayıldı: %v", err)
	}

	if _, err := owner.Exec(ctx, `INSERT INTO platform_admins (user_id, granted_by) VALUES ($1, $1)`, plain.PG()); constraintOf(err) != "platform_admins_not_self_granted" {
		t.Fatalf("kendine yetki: %v", err)
	}
}

func auditParams(actor id.ID) InsertPlatformAuditLogParams {
	return InsertPlatformAuditLogParams{
		ID: id.New().PG(), ActorID: actor.PG(), Action: "admin.sign_in",
		TargetType: text("user"), TargetID: actor.PG(), Reason: "Panel girişi", Metadata: []byte(`{}`),
	}
}

func TestPlatformAuditLogIsAppendOnly(t *testing.T) {
	pool, owner, q := setupWithOwner(t)
	ctx := context.Background()
	actor := id.New()
	if err := q.InsertPlatformAuditLog(ctx, auditParams(actor)); err != nil {
		t.Fatal(err)
	}
	// The trigger holds even for a caller with every table privilege;
	// identity_app has no UPDATE or TRUNCATE at all (TestAppRoleCannotBypassGuards)
	// and its DELETE reaches the trigger.
	for name, c := range map[string]struct {
		pool *pgxpool.Pool
		sql  string
	}{
		"gerekçe değiştirme": {owner, `UPDATE platform_audit_logs SET reason = 'başka bir gerekçe'`},
		"silme":              {owner, `DELETE FROM platform_audit_logs`},
		"boşaltma":           {owner, `TRUNCATE platform_audit_logs`},
		"servisin silmesi":   {pool, `DELETE FROM platform_audit_logs`},
	} {
		_, err := c.pool.Exec(ctx, c.sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "TI003" {
			t.Errorf("%s reddedilmedi: %v", name, err)
		}
	}

	// A year-old row may go (retention), and only through the purge.
	if _, err := owner.Exec(ctx, `ALTER TABLE platform_audit_logs DISABLE TRIGGER platform_audit_logs_append_only`); err != nil {
		t.Fatal(err)
	}
	old := auditParams(actor)
	if err := q.InsertPlatformAuditLog(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE platform_audit_logs SET created_at = now() - interval '366 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE platform_audit_logs ENABLE TRIGGER platform_audit_logs_append_only`); err != nil {
		t.Fatal(err)
	}
	future := pgtype.Timestamptz{Time: time.Now().UTC().AddDate(1, 0, 0), Valid: true}
	n, err := q.PurgePlatformAuditLogs(ctx, PurgePlatformAuditLogsParams{Cutoff: future, Batch: 100})
	if err != nil || n != 1 {
		t.Fatalf("saklama: n=%d err=%v", n, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform_audit_logs`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("kalan = %d, %v", left, err)
	}
}

func TestPlatformAuditLogChecks(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	actor := id.New()
	for name, tc := range map[string]struct {
		f    func(*InsertPlatformAuditLogParams)
		want string
	}{
		"gerekçe yok":      {func(p *InsertPlatformAuditLogParams) { p.Reason = "" }, "platform_audit_logs_reason_len"},
		"kısa gerekçe":     {func(p *InsertPlatformAuditLogParams) { p.Reason = "   kısa    " }, "platform_audit_logs_reason_len"},
		"uzun gerekçe":     {func(p *InsertPlatformAuditLogParams) { p.Reason = strings.Repeat("a", 1001) }, "platform_audit_logs_reason_len"},
		"işlem biçimi":     {func(p *InsertPlatformAuditLogParams) { p.Action = "Ban" }, "platform_audit_logs_action_format"},
		"bilinmeyen hedef": {func(p *InsertPlatformAuditLogParams) { p.TargetType = text("dm") }, "platform_audit_logs_target_type"},
		"hedef kimliksiz":  {func(p *InsertPlatformAuditLogParams) { p.TargetID = pgtype.UUID{} }, "platform_audit_logs_target"},
		"metadata dizi":    {func(p *InsertPlatformAuditLogParams) { p.Metadata = []byte(`[]`) }, "platform_audit_logs_metadata"},
		"metadata çok büyük": {func(p *InsertPlatformAuditLogParams) {
			p.Metadata = []byte(`{"a":"` + strings.Repeat("x", 4100) + `"}`)
		}, "platform_audit_logs_metadata"},
	} {
		p := auditParams(actor)
		tc.f(&p)
		if got := constraintOf(q.InsertPlatformAuditLog(ctx, p)); got != tc.want {
			t.Errorf("%s: kısıt = %q, beklenen %q", name, got, tc.want)
		}
	}
}

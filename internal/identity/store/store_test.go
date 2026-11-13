package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
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

func setup(t *testing.T) (*pgxpool.Pool, *Queries) {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	return pool, New(pool)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// fakeCiphertext stands in for an encrypted birth date; the store never sees
// plaintext.
var fakeCiphertext = bytes.Repeat([]byte{0xAB}, 40)

func newUser(t *testing.T, q *Queries) id.ID {
	t.Helper()
	u := id.New()
	if err := q.CreateUser(context.Background(), CreateUserParams{ID: u.PG(), DisplayName: text("Ayşe Işık"), Locale: "tr"}); err != nil {
		t.Fatal(err)
	}
	return u
}

func constraintOf(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func TestMigrationsRevokePublicAccess(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	var canConnect, canCreate bool
	err := pool.QueryRow(ctx, `
		SELECT has_database_privilege('public', current_database(), 'CONNECT'),
		       has_schema_privilege('public', 'public', 'CREATE')`).Scan(&canConnect, &canCreate)
	if err != nil {
		t.Fatal(err)
	}
	if canConnect || canCreate {
		t.Fatalf("PUBLIC yetkisi kalmış: connect=%v create=%v", canConnect, canCreate)
	}
}

func TestRegistrationHappensOnce(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)

	n, err := q.CompleteRegistration(ctx, CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: fakeCiphertext})
	if err != nil || n != 1 {
		t.Fatalf("ilk kayıt: n=%d err=%v", n, err)
	}
	n, err = q.CompleteRegistration(ctx, CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: fakeCiphertext})
	if err != nil || n != 0 {
		t.Fatalf("ikinci kayıt kabul edildi: n=%d err=%v", n, err)
	}
	if n, _ := q.BlockRegistrationForAge(ctx, u.PG()); n != 0 {
		t.Fatal("kayıtlı hesap yaş nedeniyle engellendi")
	}
	got, err := q.GetUser(ctx, u.PG())
	if err != nil || !got.RegisteredAt.Valid || !bytes.Equal(got.BirthDateEnc, fakeCiphertext) {
		t.Fatalf("kullanıcı = %+v, err=%v", got, err)
	}
}

func TestAgeBlockedAccountCannotRegisterLater(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	if n, err := q.BlockRegistrationForAge(ctx, u.PG()); err != nil || n != 1 {
		t.Fatalf("engelleme: n=%d err=%v", n, err)
	}
	n, err := q.CompleteRegistration(ctx, CompleteRegistrationParams{ID: u.PG(), BirthDateEnc: fakeCiphertext})
	if err != nil || n != 0 {
		t.Fatalf("engellenmiş hesap başka tarihle kayıt oldu: n=%d err=%v", n, err)
	}
	got, _ := q.GetUser(ctx, u.PG())
	if got.BirthDateEnc != nil {
		t.Fatal("engellenen hesapta doğum tarihi tutuluyor")
	}
}

func TestUserConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	for name, tc := range map[string]struct {
		sql, constraint string
	}{
		"41 karakter ad":            {`UPDATE users SET display_name = repeat('ş', 41) WHERE id = $1`, "users_display_name_len"},
		"boş ad":                    {`UPDATE users SET display_name = '' WHERE id = $1`, "users_display_name_len"},
		"silinmemiş hesapta ad yok": {`UPDATE users SET display_name = NULL WHERE id = $1`, "users_display_name_present"},
		"doğum tarihsiz kayıt":      {`UPDATE users SET registered_at = now() WHERE id = $1`, "users_registered_has_birth_date"},
		"engelli hesapta tarih":     {`UPDATE users SET age_blocked_at = now(), birth_date_enc = repeat('x', 40)::bytea WHERE id = $1`, "users_age_blocked"},
		"bilinmeyen görünürlük":     {`UPDATE users SET birth_date_visibility = 'everyone' WHERE id = $1`, "users_birth_date_visibility"},
		"referans kodu biçimi":      {`UPDATE users SET ref_code = 'TYF-0O1I2L' WHERE id = $1`, "users_ref_code_format"},
		"kendini referans":          {`UPDATE users SET referred_by_user_id = id WHERE id = $1`, "users_no_self_referral"},
		"kısa şifreli tarih":        {`UPDATE users SET birth_date_enc = '\x01'::bytea WHERE id = $1`, "users_birth_date_enc_len"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, u.PG())
			if got := constraintOf(err); got != tc.constraint {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.constraint)
			}
		})
	}
	// 40 karakterlik Türkçe ad kabul edilir (bayt değil karakter sayılır).
	if _, err := pool.Exec(ctx, `UPDATE users SET display_name = repeat('ğ', 40) WHERE id = $1`, u.PG()); err != nil {
		t.Fatalf("40 karakter reddedildi: %v", err)
	}
}

func TestAuthIdentityIsUniquePerProvider(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u1, u2 := newUser(t, q), newUser(t, q)
	p := CreateAuthIdentityParams{ID: id.New().PG(), UserID: u1.PG(), Provider: "google", Subject: "1234567890", Email: text("ornek@example.com")}
	if err := q.CreateAuthIdentity(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ID, p.UserID = id.New().PG(), u2.PG()
	if err := q.CreateAuthIdentity(ctx, p); constraintOf(err) != "auth_identities_provider_subject_key" {
		t.Fatalf("aynı Google hesabı iki kullanıcıya bağlandı: %v", err)
	}
	p.Provider = "apple"
	if err := q.CreateAuthIdentity(ctx, p); err != nil {
		t.Fatalf("farklı sağlayıcıda aynı subject reddedildi: %v", err)
	}
	p.ID, p.Provider = id.New().PG(), "facebook"
	if err := q.CreateAuthIdentity(ctx, p); constraintOf(err) != "auth_identities_provider" {
		t.Fatalf("bilinmeyen sağlayıcı kabul edildi: %v", err)
	}
	got, err := q.GetAuthIdentity(ctx, GetAuthIdentityParams{Provider: "google", Subject: "1234567890"})
	if err != nil || got.UserID != u1.PG() {
		t.Fatalf("GetAuthIdentity = %+v, %v", got, err)
	}
}

// Demo hesaplarının giriş kimliği (issue #179, ADR-0023) yalnızca demo-NN
// biçiminde ve kendi işaret e-postasıyla yazılabilir.
func TestDemoIdentityIsMarked(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	ok := CreateAuthIdentityParams{ID: id.New().PG(), UserID: u.PG(), Provider: "demo", Subject: "demo-07", Email: text("demo-07@demo.tayfam.app")}
	if err := q.CreateAuthIdentity(ctx, ok); err != nil {
		t.Fatalf("demo kimliği reddedildi: %v", err)
	}
	for _, bad := range []CreateAuthIdentityParams{
		{Provider: "demo", Subject: "demo-08", Email: text("biri@example.com")},
		{Provider: "demo", Subject: "demo-08", Email: pgtype.Text{}},
		{Provider: "demo", Subject: "1234567890", Email: text("1234567890@demo.tayfam.app")},
		{Provider: "demo", Subject: "demo-09", Email: text("demo-08@demo.tayfam.app")},
	} {
		bad.ID, bad.UserID = id.New().PG(), u.PG()
		if err := q.CreateAuthIdentity(ctx, bad); constraintOf(err) != "auth_identities_demo" {
			t.Errorf("işaretsiz demo kimliği kabul edildi (%s, %v): %v", bad.Subject, bad.Email, err)
		}
	}
}

// session creates a device, a session and one refresh token.
func session(t *testing.T, q *Queries, user id.ID) (sessionID id.ID, hash []byte) {
	t.Helper()
	ctx := context.Background()
	dev, sess := id.New(), id.New()
	if err := q.CreateDevice(ctx, CreateDeviceParams{ID: dev.PG(), UserID: user.PG(), Platform: "android"}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateSession(ctx, CreateSessionParams{ID: sess.PG(), UserID: user.PG(), DeviceID: dev.PG(), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(90 * 24 * time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("test-refresh-" + sess.String()))
	if err := q.CreateRefreshToken(ctx, CreateRefreshTokenParams{ID: id.New().PG(), SessionID: sess.PG(), TokenHash: h[:]}); err != nil {
		t.Fatal(err)
	}
	return sess, h[:]
}

func TestRefreshTokenUsedOnce(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	sess, hash := session(t, q, u)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := q.WithTx(tx)
	row, err := qtx.LockRefreshToken(ctx, hash)
	if err != nil || row.SessionID != sess.PG() || row.UsedAt.Valid || row.RevokedAt.Valid {
		t.Fatalf("LockRefreshToken = %+v, %v", row, err)
	}
	if n, err := qtx.MarkRefreshTokenUsed(ctx, row.ID); err != nil || n != 1 {
		t.Fatalf("ilk kullanım: n=%d err=%v", n, err)
	}
	if n, _ := qtx.MarkRefreshTokenUsed(ctx, row.ID); n != 0 {
		t.Fatal("token iki kez kullanıldı")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	again, err := q.LockRefreshToken(ctx, hash)
	if err != nil || !again.UsedAt.Valid {
		t.Fatalf("kullanılmış token işaretsiz: %+v %v", again, err)
	}
	if _, err := q.LockRefreshToken(ctx, bytes.Repeat([]byte{1}, 32)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("bilinmeyen token: %v", err)
	}
}

func TestRefreshTokenConstraints(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	sess, hash := session(t, q, u)
	if err := q.CreateRefreshToken(ctx, CreateRefreshTokenParams{ID: id.New().PG(), SessionID: sess.PG(), TokenHash: hash}); constraintOf(err) != "refresh_tokens_token_hash_key" {
		t.Fatalf("aynı özet iki kez kabul edildi: %v", err)
	}
	if err := q.CreateRefreshToken(ctx, CreateRefreshTokenParams{ID: id.New().PG(), SessionID: sess.PG(), TokenHash: []byte("kisa")}); constraintOf(err) != "refresh_tokens_hash_len" {
		t.Fatalf("SHA-256 olmayan özet kabul edildi: %v", err)
	}
}

func TestRevokeSessions(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	s1, _ := session(t, q, u)
	s2, _ := session(t, q, u)

	// The revoke queries return what they ended: session.revoked needs it.
	if users, err := q.RevokeSession(ctx, RevokeSessionParams{ID: s1.PG(), Reason: text("logout")}); err != nil || len(users) != 1 || users[0] != u.PG() {
		t.Fatalf("RevokeSession: %v err=%v", users, err)
	}
	if _, err := q.GetActiveSession(ctx, s1.PG()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("iptal edilen oturum aktif görünüyor: %v", err)
	}
	if ids, _ := q.RevokeUserSessions(ctx, RevokeUserSessionsParams{UserID: u.PG(), Reason: text("token_reuse")}); len(ids) != 1 || ids[0] != s2.PG() {
		t.Fatalf("kalan oturumlar = %v", ids)
	}
	if users, err := q.RevokeSession(ctx, RevokeSessionParams{ID: s1.PG(), Reason: text("uydurma")}); err != nil || len(users) != 0 {
		// already revoked: no row matches, so the CHECK is not reached
		t.Fatalf("%v %v", users, err)
	}
}

func TestDeletingUserRemovesCredentials(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	u := newUser(t, q)
	session(t, q, u)
	if err := q.CreateAuthIdentity(ctx, CreateAuthIdentityParams{ID: id.New().PG(), UserID: u.PG(), Provider: "google", Subject: "sub-" + u.String()}); err != nil {
		t.Fatal(err)
	}
	// Kalıcı silme (retention) identity_app ile çalışır; bağlı satırlar
	// cascade ile tablo sahibinin yetkisiyle gider.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.PG()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"auth_identities", "devices", "sessions", "refresh_tokens"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: kullanıcı silindikten sonra %d satır kaldı", table, n)
		}
	}
}

func TestTurkishCollation(t *testing.T) {
	pool, _ := setup(t)
	var lower string
	if err := pool.QueryRow(context.Background(), `SELECT lower('IŞIK İSTANBUL')`).Scan(&lower); err != nil {
		t.Fatal(err)
	}
	if lower != "ışık istanbul" {
		t.Fatalf("Türkçe küçük harf dönüşümü yanlış: %q (veritabanı tr-TR ile oluşturulmalı)", lower)
	}
}

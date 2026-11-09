package retention

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func (f fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixture) exists(table string, row id.ID) bool {
	f.t.Helper()
	var ok bool
	if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, row.PG()).Scan(&ok); err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f fixture) user() id.ID {
	u := id.New()
	f.exec(`INSERT INTO users (id, display_name, birth_date_enc, registered_at) VALUES ($1, 'Ad', $2, now())`, u.PG(), bytes.Repeat([]byte{1}, 40))
	return u
}

// device adds a device seen seenDaysAgo days ago.
func (f fixture) device(user id.ID, seenDaysAgo int) id.ID {
	d := id.New()
	f.exec(`INSERT INTO devices (id, user_id, platform, created_at, last_seen_at) VALUES ($1, $2, 'android', now() - make_interval(days => $3), now() - make_interval(days => $3))`, d.PG(), user.PG(), seenDaysAgo)
	return d
}

// session adds a session last refreshed refreshedDaysAgo days ago, revoked
// revokedDaysAgo days ago (negative: open).
func (f fixture) session(user, device id.ID, refreshedDaysAgo, revokedDaysAgo int) id.ID {
	s := id.New()
	f.exec(`INSERT INTO sessions (id, user_id, device_id, created_at, last_refreshed_at, expires_at)
		VALUES ($1, $2, $3, now() - make_interval(days => $4 + 1), now() - make_interval(days => $4), now() - make_interval(days => $4) + interval '90 days')`,
		s.PG(), user.PG(), device.PG(), refreshedDaysAgo)
	if revokedDaysAgo >= 0 {
		f.exec(`UPDATE sessions SET revoked_at = now() - make_interval(days => $2), revoked_reason = 'logout' WHERE id = $1`, s.PG(), revokedDaysAgo)
	}
	return s
}

// token adds a refresh token used usedDaysAgo days ago (negative: unused).
func (f fixture) token(session id.ID, usedDaysAgo int) id.ID {
	rt := id.New()
	hash := bytes.Repeat([]byte{0}, 16)
	hash = append(hash, rt[:]...)
	f.exec(`INSERT INTO refresh_tokens (id, session_id, token_hash) VALUES ($1, $2, $3)`, rt.PG(), session.PG(), hash)
	if usedDaysAgo >= 0 {
		f.exec(`UPDATE refresh_tokens SET used_at = now() - make_interval(days => $2) WHERE id = $1`, rt.PG(), usedDaysAgo)
	}
	return rt
}

func TestRetention(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	f := fixture{t, pool}
	u := f.user()

	live := f.device(u, 1)
	liveSession := f.session(u, live, 1, -1)
	unused := f.token(liveSession, -1)
	usedRecently := f.token(liveSession, 29)
	usedLongAgo := f.token(liveSession, 31)

	// Ended 40 days ago: its tokens go after 30 days, the session after 90.
	ended := f.session(u, live, 45, 40)
	endedToken := f.token(ended, -1)
	// Ended 100 days ago.
	endedLongAgo := f.session(u, live, 100, 95)
	// Not refreshed for 95 days (expired).
	stale := f.session(u, live, 95, -1)

	oldDevice := f.device(u, 91)
	oldDeviceSession := f.session(u, oldDevice, 1, -1)
	recentDevice := f.device(u, 89)

	deletedOld, deletedRecent := f.user(), f.user()
	f.exec(`UPDATE users SET deleted_at = now() - interval '31 days', display_name = NULL, birth_date_enc = NULL WHERE id = $1`, deletedOld.PG())
	f.exec(`UPDATE users SET deleted_at = now() - interval '29 days', display_name = NULL, birth_date_enc = NULL WHERE id = $1`, deletedRecent.PG())
	// Someone referred by the deleted user keeps the account; the link goes.
	referred := f.user()
	f.exec(`UPDATE users SET referred_by_user_id = $2, referred_at = now() WHERE id = $1`, referred.PG(), deletedOld.PG())
	f.exec(`INSERT INTO consent_records (id, user_id, document_type, document_version) VALUES ($1, $2, 'terms', '2026-11-08')`, id.New().PG(), deletedOld.PG())

	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		table string
		row   id.ID
		kept  bool
	}{
		"kullanılmamış token":            {"refresh_tokens", unused, true},
		"29 gün önce kullanılan token":   {"refresh_tokens", usedRecently, true},
		"31 gün önce kullanılan token":   {"refresh_tokens", usedLongAgo, false},
		"40 gün önce biten oturum token": {"refresh_tokens", endedToken, false},
		"açık oturum":                    {"sessions", liveSession, true},
		"40 gün önce biten oturum":       {"sessions", ended, true},
		"95 gün önce biten oturum":       {"sessions", endedLongAgo, false},
		"95 gündür yenilenmeyen oturum":  {"sessions", stale, false},
		"görülen cihaz":                  {"devices", live, true},
		"89 gündür görünmeyen cihaz":     {"devices", recentDevice, true},
		"91 gündür görünmeyen cihaz":     {"devices", oldDevice, false},
		"eski cihazın oturumu":           {"sessions", oldDeviceSession, false},
		"31 gün önce silinen hesap":      {"users", deletedOld, false},
		"29 gün önce silinen hesap":      {"users", deletedRecent, true},
		"aktif hesap":                    {"users", u, true},
		"referansla gelen hesap":         {"users", referred, true},
	} {
		if got := f.exists(tc.table, tc.row); got != tc.kept {
			t.Errorf("%s: kaldı = %v, %v bekleniyordu", name, got, tc.kept)
		}
	}
	if r.Users != 1 || r.Devices != 1 || r.Sessions != 2 || r.RefreshTokens != 2 {
		t.Errorf("sayılar = %+v", r)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM consent_records WHERE user_id = $1`, deletedOld.PG()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("onay kaydı silindi: %d %v", n, err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE id = $1 AND referred_by_user_id IS NULL AND referred_at IS NOT NULL`, referred.PG()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("referans bağlantısı: %d %v", n, err)
	}

	// A second run has nothing left to do.
	if r, err := svc.RunOnce(context.Background()); err != nil || r != (Result{}) {
		t.Fatalf("ikinci çalışma = %+v, %v", r, err)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run durmadı")
	}
}

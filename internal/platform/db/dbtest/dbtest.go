// Package dbtest gives store tests a fresh, migrated Postgres database.
//
// Tests run against a real Postgres (CLAUDE.md). The server is given by
// TEST_DATABASE_URL, a superuser connection used only to create and drop
// one throwaway database per test. In CI this is a Postgres service
// container with the same image as the clusters; locally any Postgres works
// (`make test-db`). Without TEST_DATABASE_URL the tests are skipped, unless
// REQUIRE_TEST_DATABASE is set: CI sets it, so the tests cannot silently stop
// running there.
//
// The database mirrors the clusters' roles (ADR-0015): it is owned by the
// service's owner role (<service>), which runs the migrations, and tests get
// a pool acting as the runtime role (<service>_app), so a query that needs a
// privilege the migrations did not grant fails here as it would in the
// cluster. Both roles are NOLOGIN in the test server; the superuser
// connection takes them on with the `role` startup parameter, which makes
// them the current user for every privilege check.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
)

// DB is a fresh, migrated test database.
type DB struct {
	// App acts as <service>_app, the role the service uses at runtime.
	App *pgxpool.Pool
	// Admin is a superuser connection to the same database. It is only for
	// fixtures the service itself must not be able to create (bypassing
	// triggers, hard deletes done by future background jobs); tests of the
	// service's behaviour use App.
	Admin *pgxpool.Pool
}

// New creates a database for service, applies migrations as the owner role
// and returns a pool acting as the runtime role. The database is dropped
// when the test ends.
func New(t testing.TB, service string, migrations fs.FS) *pgxpool.Pool {
	t.Helper()
	return Open(t, service, migrations).App
}

var serviceName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// Open is New with an additional superuser pool for fixtures.
func Open(t testing.TB, service string, migrations fs.FS) DB {
	t.Helper()
	if !serviceName.MatchString(service) {
		t.Fatalf("geçersiz servis adı: %q", service)
	}
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		if os.Getenv("REQUIRE_TEST_DATABASE") != "" {
			t.Fatal("REQUIRE_TEST_DATABASE ayarlı ama TEST_DATABASE_URL yok; store testleri atlanamaz")
		}
		t.Skip("TEST_DATABASE_URL yok; gerçek Postgres gerektiren test atlandı")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	owner, app := service, service+"_app"

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "test_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("test Postgres'ine bağlanılamadı: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, role := range []string{owner, app} {
		ensureRole(ctx, t, conn, role)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+
		" OWNER "+pgx.Identifier{owner}.Sanitize()+
		" TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER icu ICU_LOCALE 'tr-TR' LOCALE 'C.UTF-8'"); err != nil {
		t.Fatalf("test veritabanı oluşturulamadı: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Logf("test veritabanı silinemedi: %v", err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Logf("test veritabanı silinemedi: %v", err)
		}
	})

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL çözümlenemedi")
	}
	u.Path = "/" + name
	asRole := func(role string) string {
		v := *u
		q := v.Query()
		q.Set("options", "-c role="+role)
		// libpq reads "+" literally; spaces must be %20.
		v.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
		return v.String()
	}

	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), asRole(owner), migrations); err != nil {
		t.Fatalf("migration: %v", err)
	}
	appPool, err := db.Open(ctx, asRole(app))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	adminPool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	// The runtime role must really be the runtime role: a misconfigured
	// server where it has extra powers would make every test meaningless.
	var current string
	if err := appPool.QueryRow(ctx, "SELECT current_user").Scan(&current); err != nil || current != app {
		t.Fatalf("test bağlantısı %s rolüyle çalışmıyor (%q): %v", app, current, err)
	}
	return DB{App: appPool, Admin: adminPool}
}

// ensureRole creates role as NOLOGIN if it does not exist. Roles are shared
// by every test database on the server, and test packages run in parallel,
// so a concurrent creation is not an error. An existing role with powers
// beyond an ordinary role fails the test.
func ensureRole(ctx context.Context, t testing.TB, conn *pgx.Conn, role string) {
	t.Helper()
	_, err := conn.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" NOLOGIN")
	var pgErr *pgconn.PgError
	if err != nil && (!errors.As(err, &pgErr) || (pgErr.Code != "42710" && pgErr.Code != "23505")) {
		t.Fatalf("test rolü oluşturulamadı (%s): %v", role, err)
	}
	var privileged bool
	if err := conn.QueryRow(ctx, `
		SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls
		    OR EXISTS (SELECT 1 FROM pg_auth_members WHERE member = r.oid)
		FROM pg_roles r WHERE rolname = $1`, role).Scan(&privileged); err != nil {
		t.Fatalf("test rolü okunamadı (%s): %v", role, err)
	}
	if privileged {
		t.Fatalf("test sunucusundaki %s rolünün fazladan yetkisi ya da üyeliği var; testler anlamsız olur", role)
	}
}

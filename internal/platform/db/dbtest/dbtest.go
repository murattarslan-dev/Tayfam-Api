// Package dbtest gives store tests a fresh, migrated Postgres database.
//
// Tests run against a real Postgres (CLAUDE.md). The server is given by
// TEST_DATABASE_URL, a superuser connection used only to create and drop
// one throwaway database per test. In CI this is a Postgres service
// container with the same image as the clusters; locally any Postgres works
// (`make test-db`). Without TEST_DATABASE_URL the tests are skipped, unless
// REQUIRE_TEST_DATABASE is set: CI sets it, so the tests cannot silently stop
// running there.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
)

// New creates a database, applies migrations and returns a pool connected
// to it. The database is dropped when the test ends.
func New(t testing.TB, migrations fs.FS) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		if os.Getenv("REQUIRE_TEST_DATABASE") != "" {
			t.Fatal("REQUIRE_TEST_DATABASE ayarlı ama TEST_DATABASE_URL yok; store testleri atlanamaz")
		}
		t.Skip("TEST_DATABASE_URL yok; gerçek Postgres gerektiren test atlandı")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "test_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("test Postgres'ine bağlanılamadı: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+
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
	connString := u.String()

	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), connString, migrations); err != nil {
		t.Fatalf("migration: %v", err)
	}
	pool, err := db.Open(ctx, connString)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

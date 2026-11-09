package dbtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TablePrivileges returns the table privileges the pool's current role
// holds directly, as "table" → "INSERT,SELECT,…" (sorted). Tests compare it
// with the exact set the service needs, so a widened grant fails a test.
func TablePrivileges(t testing.TB, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `
		SELECT table_name::text, string_agg(privilege_type::text, ',' ORDER BY privilege_type)
		FROM information_schema.table_privileges
		WHERE grantee = current_user AND table_schema = 'public'
		GROUP BY table_name`)
	if err != nil {
		t.Fatalf("tablo yetkileri okunamadı: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var table, privs string
		if err := rows.Scan(&table, &privs); err != nil {
			t.Fatal(err)
		}
		got[table] = privs
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// WantDenied runs sql in a transaction that is always rolled back and fails
// the test unless Postgres refuses it for lack of privilege (42501: missing
// grant, not the owner, or a superuser-only setting).
func WantDenied(t testing.TB, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, sql, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("yetki hatası bekleniyordu: %s\n  sonuç: %v", sql, err)
	}
}

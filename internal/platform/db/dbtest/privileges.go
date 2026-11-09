package dbtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TablePrivileges returns the table privileges the pool's current role
// holds directly, as "table" → "INSERT,SELECT,UPDATE(actor_id)" (sorted;
// column-level grants appear as PRIVILEGE(col,…)). Tests compare it with the
// exact set the service needs, so a widened grant fails a test.
func TablePrivileges(t testing.TB, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Table-level grants from pg_class.relacl, column-level grants from
	// pg_attribute.attacl (information_schema.table_privileges does not list
	// the latter).
	rows, err := pool.Query(ctx, `
		SELECT c.relname::text, p.privilege_type::text, NULL::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		CROSS JOIN LATERAL aclexplode(c.relacl) p
		WHERE c.relkind IN ('r', 'p', 'v') AND p.grantee = (SELECT oid FROM pg_roles WHERE rolname = current_user)
		UNION ALL
		SELECT c.relname::text, p.privilege_type::text, a.attname::text
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		CROSS JOIN LATERAL aclexplode(a.attacl) p
		WHERE a.attnum > 0 AND NOT a.attisdropped
		  AND p.grantee = (SELECT oid FROM pg_roles WHERE rolname = current_user)`)
	if err != nil {
		t.Fatalf("tablo yetkileri okunamadı: %v", err)
	}
	defer rows.Close()
	tables := map[string]map[string][]string{}
	for rows.Next() {
		var table, priv string
		var column *string
		if err := rows.Scan(&table, &priv, &column); err != nil {
			t.Fatal(err)
		}
		if tables[table] == nil {
			tables[table] = map[string][]string{}
		}
		cols := tables[table][priv]
		if column != nil {
			cols = append(cols, *column)
		} else if cols == nil {
			cols = []string{}
		}
		tables[table][priv] = cols
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for table, privs := range tables {
		var parts []string
		for priv, cols := range privs {
			if len(cols) == 0 {
				parts = append(parts, priv)
				continue
			}
			slices.Sort(cols)
			parts = append(parts, priv+"("+strings.Join(cols, ",")+")")
		}
		slices.Sort(parts)
		got[table] = strings.Join(parts, ",")
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

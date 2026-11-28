package dbtest

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunScript runs a SQL script that the platform owner feeds to psql by hand
// (docs/runbooks: no psql meta-commands, settings through PGOPTIONS) on a new
// superuser connection to the test database, as psql in the Postgres pod
// does. settings are the session settings PGOPTIONS would set (for example
// "tayfam.mod" → "kuru"). It returns the NOTICE messages the script raised
// and the script's error.
func RunScript(t testing.TB, d DB, path string, settings map[string]string) ([]string, error) {
	t.Helper()
	script, err := os.ReadFile(path) // #nosec G304 -- tests read the repo's own scripts
	if err != nil {
		t.Fatalf("betik okunamadı: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := d.Admin.Config().ConnConfig
	var (
		mu      sync.Mutex
		notices []string
	)
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, n.Message)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("betik bağlantısı: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT set_config('statement_timeout', '0', false)"); err != nil {
		t.Fatal(err)
	}
	for k, v := range settings {
		if _, err := conn.Exec(ctx, "SELECT set_config($1, $2, false)", k, v); err != nil {
			t.Fatalf("ayar %s: %v", k, err)
		}
	}
	_, err = conn.Exec(ctx, string(script))
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), notices...), err
}

// TableCounts returns the row count of every table in the public schema,
// tables of extensions (PostGIS's spatial_ref_sys) excluded.
func TableCounts(t testing.TB, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `
		SELECT c.relname::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64, len(names))
	for _, name := range names {
		var n int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{name}.Sanitize()).Scan(&n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		counts[name] = n
	}
	return counts
}

// SQLState returns the SQLSTATE of a Postgres error, or "".
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

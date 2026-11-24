// Package pilotsifirla tests scripts/pilot-sifirla.sh against the three
// service databases with every migration applied
// (docs/runbooks/pilot-oncesi-sifirlama.md).
package pilotsifirla

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

const (
	admin = "00000000-0000-7000-8000-00000000000a"
	other = "00000000-0000-7000-8000-00000000000b"
)

type env struct {
	dbs     map[string]*pgxpool.Pool
	wrapper string
}

// setup opens identity, core and chat, writes testdata/<db>.sql into each
// and returns a psql wrapper that maps "-d <db>" to the test database.
func setup(t *testing.T) env {
	t.Helper()
	if _, err := exec.LookPath("psql"); err != nil {
		if os.Getenv("REQUIRE_TEST_DATABASE") != "" {
			t.Fatal("psql bulunamadı")
		}
		t.Skip("psql yok; sıfırlama betiği testi atlandı")
	}
	migrations := map[string]fs.FS{
		"identity": identitydb.Migrations(),
		"core":     coredb.Migrations(),
		"chat":     chatdb.Migrations(),
	}
	e := env{dbs: map[string]*pgxpool.Pool{}}
	var cases strings.Builder
	for name, m := range migrations {
		d := dbtest.Open(t, name, m)
		e.dbs[name] = d.Admin
		seed, err := os.ReadFile(filepath.Join("testdata", name+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(context.Background(), string(seed)); err != nil {
			t.Fatalf("%s test verisi: %v", name, err)
		}
		cases.WriteString("      " + name + ") a+=(-d '" + d.Admin.Config().ConnString() + "') ;;\n")
	}
	e.wrapper = filepath.Join(t.TempDir(), "psql")
	script := "#!/usr/bin/env bash\na=()\nwhile [[ $# -gt 0 ]]; do\n  if [[ $1 == -d ]]; then\n    case $2 in\n" +
		cases.String() + "      *) exit 9 ;;\n    esac\n    shift 2\n  else\n    a+=(\"$1\"); shift\n  fi\ndone\nexec psql \"${a[@]}\"\n"
	if err := os.WriteFile(e.wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e env) run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"../pilot-sifirla.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "PILOT_SIFIRLA_PSQL="+e.wrapper)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e env) count(t *testing.T, db, query string) int {
	t.Helper()
	var n int
	if err := e.dbs[db].QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %s: %v", db, query, err)
	}
	return n
}

type check struct {
	db, query    string
	before, want int
}

var checks = []check{
	{"identity", "SELECT count(*) FROM users", 3, 1},
	{"identity", "SELECT count(*) FROM users WHERE id = '" + admin + "' AND referred_by_user_id IS NULL", 0, 1},
	{"identity", "SELECT count(*) FROM sessions WHERE user_id = '" + admin + "'", 1, 1},
	{"identity", "SELECT count(*) FROM refresh_tokens", 2, 1},
	{"identity", "SELECT count(*) FROM platform_admins", 1, 1},
	{"identity", "SELECT count(*) FROM domain_events WHERE type = 'user.profile_updated'", 2, 1},
	{"identity", "SELECT count(*) FROM domain_events WHERE actor_id = '" + other + "'", 1, 0},
	{"core", "SELECT count(*) FROM tayfas", 1, 0},
	{"core", "SELECT count(*) FROM memberships", 2, 0},
	{"core", "SELECT count(*) FROM events", 1, 0},
	{"core", "SELECT count(*) FROM event_rsvps", 1, 0},
	{"core", "SELECT count(*) FROM audit_logs", 1, 0},
	{"core", "SELECT count(*) FROM user_profiles", 2, 1},
	{"core", "SELECT count(*) FROM social_settings", 2, 1},
	{"core", "SELECT count(*) FROM garage_vehicles WHERE owner_id = '" + admin + "'", 1, 1},
	{"core", "SELECT count(*) FROM garage_vehicles", 2, 1},
	{"core", "SELECT count(*) FROM uploads WHERE status = 'ready'", 3, 1},
	{"core", "SELECT count(*) FROM uploads WHERE status = 'ready' AND user_id = '" + admin + "' AND purpose = 'avatar'", 1, 1},
	{"core", "SELECT count(*) FROM uploads", 3, 3},
	{"core", "SELECT count(*) FROM catalog_event_types", -1, -1},
	{"core", "SELECT count(*) FROM domain_events", 2, 2},
	{"core", "SELECT count(*) FROM domain_events WHERE actor_id = '" + other + "'", 1, 0},
	{"chat", "SELECT count(*) FROM user_profiles", 2, 1},
	{"chat", "SELECT count(*) FROM channels", 1, 0},
	{"chat", "SELECT count(*) FROM channel_messages", 1, 0},
	{"chat", "SELECT count(*) FROM dm_conversations", 1, 0},
	{"chat", "SELECT count(*) FROM notifications", 1, 0},
	{"chat", "SELECT count(*) FROM device_keys WHERE user_id = '" + admin + "'", 1, 1},
	{"chat", "SELECT count(*) FROM device_keys", 2, 1},
	{"chat", "SELECT count(*) FROM push_tokens", 2, 1},
}

// assertState checks every row of checks; after=false expects the seeded
// state, true the reset state. Catalog counts (-1) must stay as migrated.
func assertState(t *testing.T, e env, catalog int, after bool) {
	t.Helper()
	for _, c := range checks {
		want := c.before
		if after {
			want = c.want
		}
		if want == -1 {
			want = catalog
		}
		if got := e.count(t, c.db, c.query); got != want {
			t.Errorf("%s: %s = %d, beklenen %d", c.db, c.query, got, want)
		}
	}
}

func TestPilotSifirla(t *testing.T) {
	e := setup(t)
	catalog := e.count(t, "core", "SELECT count(*) FROM catalog_event_types")
	if catalog == 0 {
		t.Fatal("migration'lar katalog yazmamış; test anlamsız")
	}
	assertState(t, e, catalog, false)

	// Bayraksız çalıştırma yalnızca sayar.
	out, err := e.run(t, "")
	if err != nil {
		t.Fatalf("deneme: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Bitti (ROLLBACK)") {
		t.Fatalf("deneme çıktısı beklenmedik:\n%s", out)
	}
	assertState(t, e, catalog, false)

	// Yanlış onay hiçbir şeyi silmez.
	if out, err := e.run(t, "tayfam-prod\n", "--uygula"); err == nil {
		t.Fatalf("yanlış onay kabul edildi:\n%s", out)
	}
	assertState(t, e, catalog, false)

	out, err = e.run(t, "yerel\n", "--uygula")
	if err != nil {
		t.Fatalf("uygulama: %v\n%s", err, out)
	}
	assertState(t, e, catalog, true)

	// Tekrar çalıştırılabilir.
	if out, err := e.run(t, "yerel\n", "--uygula"); err != nil {
		t.Fatalf("ikinci uygulama: %v\n%s", err, out)
	}
	assertState(t, e, catalog, true)
}

func TestPilotSifirlaYoneticisiz(t *testing.T) {
	e := setup(t)
	if _, err := e.dbs["identity"].Exec(context.Background(), "DELETE FROM platform_admins"); err != nil {
		t.Fatal(err)
	}
	out, err := e.run(t, "yerel\n", "--uygula")
	if err == nil || !strings.Contains(out, "platform_admins boş") {
		t.Fatalf("yöneticisiz çalıştırma durmadı: %v\n%s", err, out)
	}
	for db, q := range map[string]string{
		"identity": "SELECT count(*) FROM users",
		"core":     "SELECT count(*) FROM tayfas",
		"chat":     "SELECT count(*) FROM channels",
	} {
		if e.count(t, db, q) == 0 {
			t.Errorf("%s: yöneticisiz çalıştırma veri sildi", db)
		}
	}
}

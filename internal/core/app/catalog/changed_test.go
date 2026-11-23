package catalog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// changedEvents returns the payloads of the catalog.changed events in ID
// order, checking that they have no actor and no subject.
func changedEvents(t *testing.T, pool *pgxpool.Pool) []Changed {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT payload, actor_id IS NULL AND subject_id IS NULL AND tayfa_id IS NULL
		FROM domain_events WHERE type = $1 ORDER BY id`, EventChanged)
	if err != nil {
		t.Fatal(err)
	}
	var out []Changed
	for rows.Next() {
		var raw []byte
		var bare bool
		if err := rows.Scan(&raw, &bare); err != nil {
			t.Fatal(err)
		}
		if !bare {
			t.Error("katalog olayında aktör, konu ya da tayfa var")
		}
		var c Changed
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// publishDue runs PublishDue in a transaction under core_app.
func publishDue(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var published bool
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		published, err = PublishDue(ctx, store.New(tx))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return published
}

// TestPublishDue: the first run publishes the migrations' catalog (every
// interest, retired ones included, keys and statuses only); a run without a
// new version publishes nothing; a migration that raises the version is
// published once (issue #280, ADR-0042 §5).
func TestPublishDue(t *testing.T) {
	_, app, admin := setup(t)
	if !publishDue(t, app) {
		t.Fatal("ilk çalışma yayınlamadı")
	}
	if publishDue(t, app) {
		t.Fatal("sürüm değişmeden yeniden yayınlandı")
	}
	got := changedEvents(t, app)
	if len(got) != 1 || got[0].CatalogVersion != seedVersion {
		t.Fatalf("olaylar = %+v", got)
	}
	var keys []string
	for _, i := range got[0].Interests {
		if i.Status != StatusActive {
			t.Errorf("%s: durum %q", i.Key, i.Status)
		}
		keys = append(keys, i.Key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"animals", "arts", "automotive", "books", "education", "environment", "faith", "food",
		"games", "general", "learning", "nature", "sports", "travel", "water"}) {
		t.Fatalf("ilgi alanları = %v", keys)
	}
	// Only key and status leave core.
	var raw map[string]any
	if err := app.QueryRow(ctx, `SELECT payload FROM domain_events WHERE type = $1`, EventChanged).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	first, _ := raw["interests"].([]any)
	if len(raw) != 2 || len(first) == 0 {
		t.Fatalf("yük = %v", raw)
	}
	if m, _ := first[0].(map[string]any); len(m) != 2 {
		t.Fatalf("ilgi alanı alanları = %v", m)
	}

	// A catalog migration: a new interest, a retired one, the version up.
	exec(t, admin, `INSERT INTO catalog_interests (key, position, labels, icon) VALUES ('dance', 90, '{"tr":"Dans"}', 'music_note')`)
	exec(t, admin, `UPDATE catalog_interests SET status = 'retired' WHERE key = 'water'`)
	exec(t, admin, `UPDATE catalog_meta SET catalog_version = catalog_version + 1`)
	if !publishDue(t, app) {
		t.Fatal("yeni sürüm yayınlanmadı")
	}
	if publishDue(t, app) {
		t.Fatal("yeni sürüm iki kez yayınlandı")
	}
	got = changedEvents(t, app)
	if len(got) != 2 || got[1].CatalogVersion != seedVersion+1 {
		t.Fatalf("olaylar = %+v", got)
	}
	status := map[string]string{}
	for _, i := range got[1].Interests {
		status[i.Key] = i.Status
	}
	if len(status) != 16 || status["dance"] != StatusActive || status["water"] != StatusRetired {
		t.Fatalf("yeni katalog = %v", status)
	}
}

// TestPublishChanged: a write path publishes in its own transaction (the
// panel, #262); the job then has nothing to publish for that version.
func TestPublishChanged(t *testing.T) {
	_, app, _ := setup(t)
	if err := pgx.BeginFunc(ctx, app, func(tx pgx.Tx) error {
		return PublishChanged(ctx, store.New(tx))
	}); err != nil {
		t.Fatal(err)
	}
	if publishDue(t, app) {
		t.Fatal("yazma yolunun yayınladığı sürüm yeniden yayınlandı")
	}
	if got := changedEvents(t, app); len(got) != 1 || got[0].CatalogVersion != seedVersion || len(got[0].Interests) != 15 {
		t.Fatalf("olaylar = %+v", got)
	}
	// Outside a transaction there is no event.
	if err := PublishChanged(ctx, store.New(app)); err == nil {
		t.Fatal("transaction dışında olay yazıldı")
	}
}

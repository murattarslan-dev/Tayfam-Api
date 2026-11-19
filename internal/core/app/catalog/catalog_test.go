package catalog

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

var ctx = context.Background()

func setup(t *testing.T) (*Service, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	svc, err := New(Config{Pool: d.App})
	if err != nil {
		t.Fatal(err)
	}
	return svc, d.App, d.Admin
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestNewNeedsPool(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("havuzsuz servis kuruldu")
	}
}

// seedVersion is the catalog version the migrations leave: 1 from the
// seed (00030), raised by every migration that changes a type in place
// (00036: the camp's fields, issue #241).
const seedVersion = 2

// The seed is the seven Faz 1 types with today's behaviour (ADR-0025 §10
// step 1): every definition passes the validator, the keys did not change.
func TestSeedCatalog(t *testing.T) {
	svc, _, _ := setup(t)
	c, notModified, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notModified || c.Version != seedVersion {
		t.Fatalf("sürüm = %d, değişmedi = %v", c.Version, notModified)
	}
	var interests []string
	for _, in := range c.Interests {
		interests = append(interests, in.Key)
		if in.Status != StatusActive || in.Labels["tr"] == "" || in.Icon == "" {
			t.Errorf("ilgi alanı = %+v", in)
		}
	}
	if want := []string{"automotive", "nature", "water", "sports", "games", "general"}; !slices.Equal(interests, want) {
		t.Fatalf("ilgi alanları = %v", interests)
	}

	wantInterest := map[string]string{
		"convoy": "automotive", "hike": "nature", "camp": "nature", "fishing": "water",
		"sports": "sports", "gaming": "games", "general": "general",
	}
	var types []string
	for _, et := range c.Types {
		types = append(types, et.Key)
		if et.Version != 1 || et.Status != StatusActive || !slices.Equal(et.InterestKeys, []string{wantInterest[et.Key]}) ||
			!slices.Equal(et.Modes, []string{ModeInPerson, ModeOnline}) || et.Parsed == nil || len(et.Definition) == 0 {
			t.Errorf("tür = %+v", et)
		}
		if !slices.ContainsFunc(c.Interests, func(in Interest) bool { return in.Key == et.InterestKeys[0] }) {
			t.Errorf("%s: ilgi alanı katalogda yok", et.Key)
		}
	}
	if want := []string{"convoy", "hike", "camp", "fishing", "sports", "gaming", "general"}; !slices.Equal(types, want) {
		t.Fatalf("türler = %v", types)
	}

	byKey := map[string]EventType{}
	for _, et := range c.Types {
		byKey[et.Key] = et
	}
	if byKey["sports"].Labels["tr"] != "Spor (diğer)" || byKey["gaming"].Labels["tr"] != "Video oyunu" {
		t.Fatalf("adlar: %q, %q", byKey["sports"].Labels["tr"], byKey["gaming"].Labels["tr"])
	}
	// Today's type-specific tools are components of the seed types.
	for typ, kind := range map[string]string{
		"convoy": definition.CompRoute, "hike": definition.CompRollCall,
		"camp": definition.CompChecklist, "fishing": definition.CompLog,
	} {
		if !slices.ContainsFunc(byKey[typ].Parsed.Components, func(c definition.Component) bool { return c.Kind == kind }) {
			t.Errorf("%s türünde %s bileşeni yok", typ, kind)
		}
	}
	// The camp's Faz 1 site details are fields (issue #241).
	for key, kind := range map[string]string{
		"site_name": definition.KindText, "has_water": definition.KindBoolean,
		"has_toilet": definition.KindBoolean, "notes": definition.KindLongText,
	} {
		if f, ok := byKey["camp"].Parsed.Field(key); !ok || f.Kind != kind || f.Required {
			t.Errorf("camp.%s = %+v", key, f)
		}
	}
	// Gaming and sports keep their Faz 1 fields.
	if f, ok := byKey["gaming"].Parsed.Field("platform"); !ok || !f.Required || len(f.Options) != 6 {
		t.Fatalf("gaming.platform = %+v", f)
	}
	if f, ok := byKey["sports"].Parsed.Field("sport"); !ok || !f.Required || len(f.Options) != 7 {
		t.Fatalf("sports.sport = %+v", f)
	}
	if d := byKey["general"].Parsed; len(d.Fields) != 0 || len(d.Components) != 0 {
		t.Fatalf("general = %+v", d)
	}
}

func TestGetNotModified(t *testing.T) {
	svc, _, admin := setup(t)
	c, notModified, err := svc.Get(ctx, seedVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !notModified || c.Version != seedVersion || c.Interests != nil || c.Types != nil {
		t.Fatalf("katalog = %+v, değişmedi = %v", c, notModified)
	}
	// A change to the catalog raises the version; the old one is stale.
	exec(t, admin, `UPDATE catalog_meta SET catalog_version = catalog_version + 1`)
	c, notModified, err = svc.Get(ctx, seedVersion)
	if err != nil {
		t.Fatal(err)
	}
	if notModified || c.Version != seedVersion+1 || len(c.Types) != 7 {
		t.Fatalf("sürüm = %d, değişmedi = %v, tür = %d", c.Version, notModified, len(c.Types))
	}
	// A version from the future is not "current" either.
	if _, notModified, err = svc.Get(ctx, 99); err != nil || notModified {
		t.Fatalf("gelecek sürüm: %v %v", notModified, err)
	}
}

func TestTypeVersion(t *testing.T) {
	svc, _, admin := setup(t)
	et, err := svc.TypeVersion(ctx, "hike", 1)
	if err != nil {
		t.Fatal(err)
	}
	if et.Key != "hike" || et.Version != 1 || et.Labels["tr"] != "Doğa yürüyüşü" || et.Status != StatusActive ||
		!slices.Equal(et.InterestKeys, []string{"nature"}) || et.Parsed == nil {
		t.Fatalf("tür = %+v", et)
	}
	for name, c := range map[string]struct {
		key     string
		version int32
	}{
		"bilinmeyen tür":   {"paragliding", 1},
		"bilinmeyen sürüm": {"hike", 2},
	} {
		if _, err := svc.TypeVersion(ctx, c.key, c.version); !errors.Is(err, apperr.ErrCatalogTypeNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		key     string
		version int32
		field   string
	}{
		"bozuk anahtar": {"Hike", 1, "key"},
		"boş anahtar":   {"", 1, "key"},
		"sql":           {"hike' OR '1'='1", 1, "key"},
		"sıfır sürüm":   {"hike", 0, "version"},
		"eksi sürüm":    {"hike", -1, "version"},
	} {
		var v *apperr.ValidationError
		if _, err := svc.TypeVersion(ctx, c.key, c.version); !errors.As(err, &v) || v.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A new version of the type: the old one stays as it was, the catalog
	// serves the new one.
	exec(t, admin, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ('hike', 2, '{"tr": "Yürüyüş"}', 'hiking', ARRAY['in_person'], '{"components": [{"kind": "route"}]}')`)
	exec(t, admin, `UPDATE catalog_event_types SET version = 2, labels = '{"tr": "Yürüyüş"}', modes = ARRAY['in_person'],
		definition = '{"components": [{"kind": "route"}]}' WHERE key = 'hike'`)
	exec(t, admin, `UPDATE catalog_meta SET catalog_version = catalog_version + 1`)
	old, err := svc.TypeVersion(ctx, "hike", 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Labels["tr"] != "Doğa yürüyüşü" || len(old.Modes) != 2 || len(old.Parsed.Components) != 1 || old.Parsed.Components[0].Kind != definition.CompRollCall {
		t.Fatalf("eski sürüm değişti: %+v", old)
	}
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(c.Types, func(et EventType) bool { return et.Key == "hike" })
	if i < 0 || c.Types[i].Version != 2 || c.Types[i].Labels["tr"] != "Yürüyüş" || string(c.Types[i].Definition) != `{"components":[{"kind":"route"}]}` {
		t.Fatalf("güncel sürüm: %+v", c.Types)
	}

	// A retired type leaves the catalog but its versions are still served.
	exec(t, admin, `UPDATE catalog_event_types SET status = 'retired' WHERE key = 'fishing'`)
	c, _, err = svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(c.Types, func(et EventType) bool { return et.Key == "fishing" }) || len(c.Types) != 6 {
		t.Fatalf("emekli tür katalogda: %d tür", len(c.Types))
	}
	if et, err := svc.TypeVersion(ctx, "fishing", 1); err != nil || et.Status != StatusRetired {
		t.Fatalf("emekli türün sürümü: %+v %v", et, err)
	}
}

// What the database holds is checked again before it reaches a client
// (ADR-0011): a definition outside the closed schema fails the request.
func TestTamperedCatalogIsNotServed(t *testing.T) {
	for name, sql := range map[string]string{
		"bilinmeyen anahtar":  `UPDATE catalog_event_types SET definition = '{"script": "x"}' WHERE key = 'general'`,
		"bilinmeyen bileşen":  `UPDATE catalog_event_types SET definition = '{"components": [{"kind": "webview"}]}' WHERE key = 'general'`,
		"para alanı":          `UPDATE catalog_event_types SET definition = '{"fields": [{"key": "stake", "kind": "money", "labels": {"tr": "Ortaya"}}]}' WHERE key = 'general'`,
		"ad satır sonlu":      `UPDATE catalog_event_types SET labels = '{"tr": "Ge\nnel"}' WHERE key = 'general'`,
		"ilgi alanı adı uzun": `UPDATE catalog_interests SET labels = jsonb_build_object('tr', repeat('a', 61)) WHERE key = 'general'`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, admin := setup(t)
			exec(t, admin, sql)
			if _, _, err := svc.Get(ctx, 0); err == nil {
				t.Fatal("bozuk katalog sunuldu")
			}
		})
	}
	svc, _, admin := setup(t)
	exec(t, admin, `UPDATE catalog_event_type_versions SET definition = '{"components": [{"kind": "webview"}]}' WHERE type_key = 'general'`)
	if _, err := svc.TypeVersion(ctx, "general", 1); err == nil || errors.Is(err, apperr.ErrCatalogTypeNotFound) {
		t.Fatalf("bozuk sürüm: %v", err)
	}
}

// The service's database user only reads the catalog (ADR-0015).
func TestAppRoleCannotChangeCatalog(t *testing.T) {
	_, pool, _ := setup(t)
	for name, sql := range map[string]string{
		"sürümü artırma":     `UPDATE catalog_meta SET catalog_version = catalog_version + 1`,
		"ilgi alanı ekleme":  `INSERT INTO catalog_interests (key, position, labels, icon) VALUES ('money', 1, '{"tr": "Para"}', 'event')`,
		"tür ekleme":         `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition) VALUES ('bet', ARRAY['general'], 1, '{"tr": "Bahis"}', 'event', ARRAY['online'], 1, '{}')`,
		"tanımı değiştirme":  `UPDATE catalog_event_types SET definition = '{}' WHERE key = 'hike'`,
		"sürümü değiştirme":  `UPDATE catalog_event_type_versions SET definition = '{}' WHERE type_key = 'hike'`,
		"sürüm ekleme":       `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition) VALUES ('hike', 2, '{"tr": "x"}', 'event', ARRAY['online'], '{}')`,
		"türü silme":         `DELETE FROM catalog_event_types WHERE key = 'hike'`,
		"ilgi alanını silme": `DELETE FROM catalog_interests WHERE key = 'nature'`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}

// The database's own rules: the second line of defence behind the
// validator.
func TestCatalogConstraints(t *testing.T) {
	_, _, admin := setup(t)
	for name, sql := range map[string]string{
		"ikinci meta satırı":      `INSERT INTO catalog_meta (id, catalog_version) VALUES (false, 1)`,
		"bozuk tür anahtarı":      `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition) VALUES ('Bad Key', ARRAY['general'], 1, '{"tr": "x"}', 'event', ARRAY['online'], 1, '{}')`,
		"bilinmeyen ilgi alanı":   `UPDATE catalog_event_types SET interest_keys = ARRAY['nature', 'gambling'] WHERE key = 'hike'`,
		"ilgi alanı yok":          `UPDATE catalog_event_types SET interest_keys = '{}' WHERE key = 'hike'`,
		"dört ilgi alanı":         `UPDATE catalog_event_types SET interest_keys = ARRAY['nature', 'water', 'sports', 'games'] WHERE key = 'hike'`,
		"tekrar eden ilgi alanı":  `UPDATE catalog_event_types SET interest_keys = ARRAY['nature', 'nature'] WHERE key = 'hike'`,
		"bilinmeyen mod":          `UPDATE catalog_event_types SET modes = ARRAY['hybrid'] WHERE key = 'hike'`,
		"mod yok":                 `UPDATE catalog_event_types SET modes = '{}' WHERE key = 'hike'`,
		"bilinmeyen durum":        `UPDATE catalog_event_types SET status = 'deleted' WHERE key = 'hike'`,
		"tanım dizi":              `UPDATE catalog_event_types SET definition = '[]' WHERE key = 'hike'`,
		"Türkçe adsız":            `UPDATE catalog_event_types SET labels = '{"en": "Hike"}' WHERE key = 'hike'`,
		"ilgi alanı Türkçe adsız": `UPDATE catalog_interests SET labels = '{}' WHERE key = 'nature'`,
		"sürümü olmayan tür":      `UPDATE catalog_event_types SET version = 7 WHERE key = 'hike'`,
		"sürümü silinen tür":      `DELETE FROM catalog_event_type_versions WHERE type_key = 'hike'`,
		"kullanılan türü silme":   `DELETE FROM catalog_event_types WHERE key = 'hike'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, sql); err == nil {
				t.Fatal("kabul edildi")
			}
		})
	}
}

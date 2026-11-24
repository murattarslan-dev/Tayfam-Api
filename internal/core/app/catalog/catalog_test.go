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
// (00036: the camp's fields, issue #241; 00037: the hike's fields, issue
// #242; 00038: the fishing log's fields, issue #243; 00040: the hike's
// measures, issue #245; 00066: the full catalog, issue #260; 00068: the
// Eğitim types' study tag fields, issue #333; 00070: the board game night's
// pair presets, issue #343).
const seedVersion = 8

// faz1Types are the seven Faz 1 types; their version 1 is today's
// behaviour of Faz 1 (ADR-0025 §10 step 1).
var faz1Types = []string{"convoy", "hike", "camp", "fishing", "sports", "gaming", "general"}

// The Faz 1 types' version 1 keeps today's behaviour (ADR-0025 §10 step
// 1): every definition passes the validator, the keys did not change. The
// full catalog of #260 is TestSeedCatalogData's.
func TestSeedCatalog(t *testing.T) {
	svc, _, _ := setup(t)
	wantInterest := map[string]string{
		"convoy": "automotive", "hike": "nature", "camp": "nature", "fishing": "water",
		"sports": "sports", "gaming": "games", "general": "general",
	}
	byKey := map[string]EventType{}
	for _, key := range faz1Types {
		et, err := svc.TypeVersion(ctx, key, 1)
		if err != nil {
			t.Fatal(err)
		}
		if et.Version != 1 || et.Status != StatusActive || !slices.Equal(et.InterestKeys, []string{wantInterest[et.Key]}) ||
			!slices.Equal(et.Modes, []string{ModeInPerson, ModeOnline}) || et.Parsed == nil || len(et.Definition) == 0 {
			t.Errorf("tür = %+v", et)
		}
		byKey[key] = et
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
	// The hike's Faz 1 details are fields (issue #242); its roll call has
	// the alarm on.
	for key, kind := range map[string]string{
		"difficulty": definition.KindInteger, "planned_distance": definition.KindDecimal,
		"planned_elevation": definition.KindInteger,
	} {
		if f, ok := byKey["hike"].Parsed.Field(key); !ok || f.Kind != kind || f.Required {
			t.Errorf("hike.%s = %+v", key, f)
		}
	}
	if f, _ := byKey["hike"].Parsed.Field("planned_distance"); f.Unit != "km" || f.Min != 0.01 || f.Max != 1000 {
		t.Errorf("hike.planned_distance = %+v", f)
	}
	if c, ok := byKey["hike"].Parsed.Component(definition.CompRollCall); !ok || !c.Params["alarm"].Bool() {
		t.Errorf("hike.roll_call = %+v", c)
	}
	// The hike's measures follow the Notion catalog (issue #245): totals the
	// organizer enters, suggested from the planned values.
	for key, want := range map[string][2]string{
		"distance": {"km", "planned_distance"}, "elevation_gain": {"m", "planned_elevation"},
	} {
		c, ok := byKey["hike"].Parsed.Component(key)
		if !ok || c.Kind != definition.CompMeasure || c.Params["unit"].String() != want[0] || c.Params["scope"].String() != "total" ||
			c.Params["entered_by"].String() != "organizer" || c.Params["prefill_from"].String() != want[1] || !c.Params["showcase"].Bool() {
			t.Errorf("hike.%s = %+v", key, c)
		}
	}
	// The fishing catch log follows the Notion catalog (issue #243): the
	// species in the fish_species vocabulary, count, weight and the Faz 1
	// length, the location kept to oneself by default, two statistics.
	catches, ok := byKey["fishing"].Parsed.Component("catches")
	if !ok || catches.Kind != definition.CompLog || !catches.Params["location"].Bool() ||
		catches.Params["location_default"].String() != "self" {
		t.Fatalf("fishing.catches = %+v", catches)
	}
	var keys []string
	for _, f := range catches.LogFields {
		keys = append(keys, f.Key)
	}
	if !slices.Equal(keys, []string{"species", "count", "weight", "length_cm"}) ||
		catches.LogFields[0].Vocabulary != "fish_species" || !catches.LogFields[0].Required ||
		catches.LogFields[2].Unit != "kg" || catches.LogFields[3].Required {
		t.Fatalf("fishing.catches alanları = %+v", catches.LogFields)
	}
	if st, _ := catches.Params["stat"].Value.([]string); !slices.Equal(st, []string{"distinct:species", "sum:weight"}) {
		t.Fatalf("fishing.catches stat = %v", st)
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
	if notModified || c.Version != seedVersion+1 || len(c.Types) != seedTypes {
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
		"bilinmeyen sürüm": {"hike", 9},
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

	// A new version of the type (the seed's is 2): the old ones stay as
	// they were, the catalog serves the new one.
	exec(t, admin, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ('hike', 3, '{"tr": "Yürüyüş"}', 'hiking', ARRAY['in_person'], '{"components": [{"kind": "route"}]}')`)
	exec(t, admin, `UPDATE catalog_event_types SET version = 3, labels = '{"tr": "Yürüyüş"}', modes = ARRAY['in_person'],
		definition = '{"components": [{"kind": "route"}]}' WHERE key = 'hike'`)
	exec(t, admin, `UPDATE catalog_meta SET catalog_version = catalog_version + 1`)
	old, err := svc.TypeVersion(ctx, "hike", 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Labels["tr"] != "Doğa yürüyüşü" || len(old.Modes) != 2 || len(old.Parsed.Components) != 3 || old.Parsed.Components[0].Kind != definition.CompRollCall {
		t.Fatalf("eski sürüm değişti: %+v", old)
	}
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(c.Types, func(et EventType) bool { return et.Key == "hike" })
	if i < 0 || c.Types[i].Version != 3 || c.Types[i].Labels["tr"] != "Yürüyüş" || string(c.Types[i].Definition) != `{"components":[{"kind":"route"}]}` {
		t.Fatalf("güncel sürüm: %+v", c.Types)
	}

	// A retired type leaves the catalog but its versions are still served.
	exec(t, admin, `UPDATE catalog_event_types SET status = 'retired' WHERE key = 'fishing'`)
	c, _, err = svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(c.Types, func(et EventType) bool { return et.Key == "fishing" }) || len(c.Types) != seedTypes-1 {
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

// The service's database user writes the catalog only as the platform
// panel needs (issue #262, 00067; ADR-0015): no key changes, no deletes,
// and a type version row never changes.
func TestAppRoleCatalogPrivileges(t *testing.T) {
	_, pool, _ := setup(t)
	for name, sql := range map[string]string{
		"sürüm satırını değiştirme":     `UPDATE catalog_event_type_versions SET definition = '{}' WHERE type_key = 'hike'`,
		"sürüm satırını silme":          `DELETE FROM catalog_event_type_versions WHERE type_key = 'hike'`,
		"türü silme":                    `DELETE FROM catalog_event_types WHERE key = 'hike'`,
		"tür anahtarını değiştirme":     `UPDATE catalog_event_types SET key = 'walk' WHERE key = 'hike'`,
		"ilgi alanını silme":            `DELETE FROM catalog_interests WHERE key = 'nature'`,
		"ilgi alanı anahtarı":           `UPDATE catalog_interests SET key = 'outdoors' WHERE key = 'nature'`,
		"rozeti silme":                  `DELETE FROM catalog_badges`,
		"rozet kapsamı":                 `UPDATE catalog_badges SET scope = 'tayfa'`,
		"etiketi silme":                 `DELETE FROM catalog_study_tags`,
		"etiketin üstü":                 `UPDATE catalog_study_tags SET parent_key = NULL`,
		"ön ayarı silme":                `DELETE FROM catalog_score_presets WHERE key = 'batak'`,
		"engelli alan adını değiştirme": `UPDATE blocked_domains SET domain = 'example.com' WHERE domain = 'bit.ly'`,
		"meta satırı ekleme":            `INSERT INTO catalog_meta (id, catalog_version) VALUES (false, 1)`,
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

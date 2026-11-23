package catalog

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// testBadges is the smallest set that exercises the catalog's rules. It is
// test data only: the real list is the seed's (issue #260;
// TestSeedCatalogData), which it replaces.
const testBadges = `DELETE FROM catalog_badges;
INSERT INTO catalog_badges (key, scope, position, labels, icon, rule, status) VALUES
	('first_event', 'user', 1, '{"tr": "İlk etkinlik", "en": "First event"}', 'flag', '{"kind": "first"}', 'active'),
	('ten_events', 'user', 2, '{"tr": "On etkinlik"}', 'medal', '{"kind": "threshold", "metric": "attended", "value": 10, "period": "all_time"}', 'active'),
	('yearly_km', 'tayfa', 3, '{"tr": "Yılın yürüyüşçüleri"}', 'trophy', '{"kind": "threshold", "metric": "hike.distance", "value": 100, "period": "year"}', 'active'),
	('old_champion', 'user', 4, '{"tr": "Eski şampiyon"}', 'crown', '{"kind": "champion"}', 'retired')`

// Test types: a personal measure, a sensitive measure, and a type whose
// attendance became sensitive in its second version.
const (
	readType  = "test_badge_read"
	readV1    = `{"components":[{"kind":"measure","key":"pages","labels":{"tr":"Sayfa"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2}}]}`
	examType  = "test_badge_exam"
	examV1    = `{"components":[{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true}}]}`
	bloodType = "test_badge_blood"
	bloodV1   = `{"components":[{"kind":"celebration"}]}`
	bloodV2   = `{"components":[{"kind":"celebration"}],"flags":{"sensitive_attendance":true}}`
)

// seedType writes a type and its versions as the platform would, with
// triggers off.
func seedType(t *testing.T, admin *pgxpool.Pool, key string, defs ...string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
		VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person'], $2, $3::jsonb)`, key, len(defs), defs[len(defs)-1]); err != nil {
		t.Fatal(err)
	}
	for i, d := range defs {
		if _, err := tx.Exec(ctx, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
			VALUES ($1, $2, '{"tr": "Test türü"}', 'event', ARRAY['in_person'], $3::jsonb)`, key, i+1, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestParseBadgeRule(t *testing.T) {
	for raw, want := range map[string]BadgeRule{
		`{"kind": "first"}`:                       {Kind: RuleFirst},
		`{"kind": "first", "event_type": "hike"}`: {Kind: RuleFirst, EventType: "hike"},
		`{"kind": "champion"}`:                    {Kind: RuleChampion},
		`{"kind": "threshold", "metric": "attended", "value": 10, "period": "all_time"}`:    {Kind: RuleThreshold, Metric: "attended", Value: 10, Period: PeriodAllTime},
		`{"kind": "threshold", "metric": "hike.distance", "value": 12.5, "period": "year"}`: {Kind: RuleThreshold, Metric: "hike.distance", Value: 12.5, Period: PeriodYear},
	} {
		got, err := ParseBadgeRule([]byte(raw))
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", raw, got, err)
		}
	}
	for name, raw := range map[string]string{
		"boş":                ``,
		"dizi":               `[]`,
		"türsüz":             `{}`,
		"bilinmeyen tür":     `{"kind": "streak"}`,
		"bilinmeyen alan":    `{"kind": "first", "note": "x"}`,
		"fazladan içerik":    `{"kind": "first"} {}`,
		"ilkte metrik":       `{"kind": "first", "metric": "attended"}`,
		"şampiyonda eşik":    `{"kind": "champion", "value": 1}`,
		"eşikte tür":         `{"kind": "threshold", "metric": "attended", "value": 1, "period": "year", "event_type": "hike"}`,
		"eşiksiz":            `{"kind": "threshold", "metric": "attended", "period": "year"}`,
		"sıfır eşik":         `{"kind": "threshold", "metric": "attended", "value": 0, "period": "year"}`,
		"eksi eşik":          `{"kind": "threshold", "metric": "attended", "value": -1, "period": "year"}`,
		"dev eşik":           `{"kind": "threshold", "metric": "attended", "value": 1e12, "period": "year"}`,
		"bilinmeyen dönem":   `{"kind": "threshold", "metric": "attended", "value": 1, "period": "week"}`,
		"bilinmeyen ortak":   `{"kind": "threshold", "metric": "friends", "value": 1, "period": "year"}`,
		"bozuk metrik":       `{"kind": "threshold", "metric": "Hike.distance", "value": 1, "period": "year"}`,
		"üç parçalı metrik":  `{"kind": "threshold", "metric": "hike.distance.km", "value": 1, "period": "year"}`,
		"bozuk tür anahtarı": `{"kind": "first", "event_type": "Hike"}`,
		"metin eşik":         `{"kind": "threshold", "metric": "attended", "value": "10", "period": "year"}`,
	} {
		if _, err := ParseBadgeRule([]byte(raw)); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

// A catalog without badges serves an empty list.
func TestEmptyBadges(t *testing.T) {
	svc, _, admin := setup(t)
	exec(t, admin, `DELETE FROM catalog_badges`)
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Badges == nil || len(c.Badges) != 0 {
		t.Fatalf("rozetler = %+v", c.Badges)
	}
}

func TestBadgesInCatalog(t *testing.T) {
	svc, _, admin := setup(t)
	exec(t, admin, testBadges)
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Badges) != 4 {
		t.Fatalf("rozetler = %+v", c.Badges)
	}
	first, yearly, retired := c.Badges[0], c.Badges[2], c.Badges[3]
	if first.Key != "first_event" || first.Scope != BadgeScopeUser || first.Icon != "flag" || first.Labels["en"] != "First event" ||
		first.Rule != (BadgeRule{Kind: RuleFirst}) || first.Status != StatusActive {
		t.Fatalf("ilk = %+v", first)
	}
	if yearly.Scope != BadgeScopeTayfa || !yearly.Rule.Year() || yearly.Rule.Metric != "hike.distance" || yearly.Rule.Value != 100 {
		t.Fatalf("yıllık = %+v", yearly)
	}
	// A retired badge is served: earned ones keep its name.
	if retired.Key != "old_champion" || retired.Status != StatusRetired {
		t.Fatalf("emekli = %+v", retired)
	}
}

// What the database holds is checked again before it reaches a client
// (ADR-0011).
func TestTamperedBadgesAreNotServed(t *testing.T) {
	for name, sql := range map[string]string{
		"ilkte metrik":         `UPDATE catalog_badges SET rule = '{"kind": "first", "metric": "attended"}' WHERE key = 'first_event'`,
		"bilinmeyen ad":        `UPDATE catalog_badges SET labels = '{"tr": "İlk\netkinlik"}' WHERE key = 'first_event'`,
		"kişide tayfa metriği": `UPDATE catalog_badges SET rule = '{"kind": "threshold", "metric": "events", "value": 10, "period": "all_time"}' WHERE key = 'ten_events'`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, admin := setup(t)
			exec(t, admin, testBadges)
			exec(t, admin, sql)
			if _, _, err := svc.Get(ctx, 0); err == nil {
				t.Fatal("bozuk rozet sunuldu")
			}
		})
	}
}

func TestCheckBadge(t *testing.T) {
	_, pool, admin := setup(t)
	seedType(t, admin, readType, readV1)
	seedType(t, admin, examType, examV1)
	seedType(t, admin, bloodType, bloodV1, bloodV2)
	q := store.New(pool)
	badge := func(scope, rule string) Badge {
		r, err := ParseBadgeRule([]byte(rule))
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		return Badge{Key: "test_badge", Scope: scope, Position: 1, Labels: map[string]string{"tr": "Rozet"}, Icon: "medal", Status: StatusActive, Rule: r}
	}
	ok := map[string]Badge{
		"genel ilk":             badge(BadgeScopeUser, `{"kind": "first"}`),
		"türde ilk":             badge(BadgeScopeUser, `{"kind": "first", "event_type": "hike"}`),
		"şampiyon":              badge(BadgeScopeUser, `{"kind": "champion", "event_type": "hike"}`),
		"kişisel katılım":       badge(BadgeScopeUser, `{"kind": "threshold", "metric": "attended", "value": 10, "period": "all_time"}`),
		"türde kişisel süre":    badge(BadgeScopeUser, `{"kind": "threshold", "metric": "hike.duration", "value": 600, "period": "year"}`),
		"tayfa etkinlik":        badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "events", "value": 50, "period": "year"}`),
		"tayfa toplam ölçüm":    badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "hike.distance", "value": 100, "period": "all_time"}`),
		"kişisel ölçüm":         badge(BadgeScopeUser, `{"kind": "threshold", "metric": "test_badge_read.pages", "value": 1000, "period": "all_time"}`),
		"tayfada kişisel ölçüm": badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "test_badge_read.pages", "value": 1000, "period": "year"}`),
	}
	for name, b := range ok {
		if err := CheckBadge(ctx, q, b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]Badge{
		"tayfada ilk":               badge(BadgeScopeTayfa, `{"kind": "first"}`),
		"tayfada şampiyon":          badge(BadgeScopeTayfa, `{"kind": "champion"}`),
		"kişide tayfa metriği":      badge(BadgeScopeUser, `{"kind": "threshold", "metric": "participants", "value": 10, "period": "year"}`),
		"kişide toplam ölçüm":       badge(BadgeScopeUser, `{"kind": "threshold", "metric": "hike.distance", "value": 100, "period": "year"}`),
		"bilinmeyen tür":            badge(BadgeScopeUser, `{"kind": "first", "event_type": "test_badge_none"}`),
		"bilinmeyen ölçüm":          badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "hike.depth", "value": 1, "period": "year"}`),
		"hassas ölçüm":              badge(BadgeScopeUser, `{"kind": "threshold", "metric": "test_badge_exam.net", "value": 100, "period": "all_time"}`),
		"hassas ölçüm tayfada":      badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "test_badge_exam.net", "value": 100, "period": "all_time"}`),
		"hassas katılım":            badge(BadgeScopeUser, `{"kind": "threshold", "metric": "test_badge_blood.attended", "value": 1, "period": "all_time"}`),
		"hassas katılımda tayfa":    badge(BadgeScopeTayfa, `{"kind": "threshold", "metric": "test_badge_blood.events", "value": 1, "period": "all_time"}`),
		"hassas katılımda ilk":      badge(BadgeScopeUser, `{"kind": "first", "event_type": "test_badge_blood"}`),
		"hassas katılımda şampiyon": badge(BadgeScopeUser, `{"kind": "champion", "event_type": "test_badge_blood"}`),
	}
	for name, b := range bad {
		if err := CheckBadge(ctx, q, b); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	b := ok["genel ilk"]
	b.Labels = map[string]string{"en": "First"}
	if err := CheckBadge(ctx, q, b); err == nil {
		t.Error("Türkçe adsız rozet kabul edildi")
	}
}

// The service's database user only reads the definitions and never
// changes or takes back an earned badge (ADR-0039 §2).
func TestAppRoleCannotChangeBadges(t *testing.T) {
	_, pool, admin := setup(t)
	exec(t, admin, testBadges)
	u, tf := id.New(), id.New()
	exec(t, admin, `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ($1, $2, 'first_event', 'all', now())`, id.New().PG(), u.PG())
	for name, sql := range map[string]string{
		"tanım ekleme":            `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'user', 1, '{"tr": "x"}', 'medal', '{"kind": "first"}')`,
		"tanım değiştirme":        `UPDATE catalog_badges SET labels = '{"tr": "x"}' WHERE key = 'first_event'`,
		"tanım silme":             `DELETE FROM catalog_badges WHERE key = 'ten_events'`,
		"rozet değiştirme":        `UPDATE user_badges SET earned_at = now() WHERE badge_key = 'first_event'`,
		"tayfa rozeti silme":      `DELETE FROM tayfa_badges WHERE tayfa_id = '` + tf.String() + `'`,
		"tayfa rozeti değiştirme": `UPDATE tayfa_badges SET earned_at = now()`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}

// The database's own rules: the second line of defence behind the
// validator and the app.
func TestBadgeConstraints(t *testing.T) {
	_, _, admin := setup(t)
	exec(t, admin, testBadges)
	u := id.New()
	exec(t, admin, `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ($1, $2, 'first_event', 'all', now())`, id.New().PG(), u.PG())
	for name, sql := range map[string]string{
		"bozuk anahtar":        `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('Bad Key', 'user', 1, '{"tr": "x"}', 'medal', '{"kind": "first"}')`,
		"bilinmeyen kapsam":    `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'team', 1, '{"tr": "x"}', 'medal', '{"kind": "first"}')`,
		"Türkçe adsız":         `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'user', 1, '{"en": "x"}', 'medal', '{"kind": "first"}')`,
		"türsüz kural":         `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'user', 1, '{"tr": "x"}', 'medal', '{}')`,
		"bilinmeyen kural":     `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'user', 1, '{"tr": "x"}', 'medal', '{"kind": "streak"}')`,
		"tayfada şampiyon":     `INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES ('x_badge', 'tayfa', 1, '{"tr": "x"}', 'medal', '{"kind": "champion"}')`,
		"bilinmeyen durum":     `UPDATE catalog_badges SET status = 'deleted' WHERE key = 'first_event'`,
		"anahtar değişmez":     `UPDATE catalog_badges SET key = 'first_ever' WHERE key = 'ten_events'`,
		"kapsam değişmez":      `UPDATE catalog_badges SET scope = 'tayfa' WHERE key = 'ten_events'`,
		"kural türü değişmez":  `UPDATE catalog_badges SET rule = '{"kind": "first"}' WHERE key = 'ten_events'`,
		"dönem değişmez":       `UPDATE catalog_badges SET rule = '{"kind": "threshold", "metric": "attended", "value": 10, "period": "year"}' WHERE key = 'ten_events'`,
		"kazanılmış silinmez":  `DELETE FROM catalog_badges WHERE key = 'first_event'`,
		"bilinmeyen rozet":     `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'nope', 'all', now())`,
		"kişiye tayfa rozeti":  `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'yearly_km', '2026', now())`,
		"tayfaya kişi rozeti":  `INSERT INTO tayfa_badges (id, tayfa_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + id.New().String() + `', 'ten_events', 'all', now())`,
		"tekrar":               `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'first_event', 'all', now())`,
		"yıllık olmayanda yıl": `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'ten_events', '2026', now())`,
		"bozuk dönem":          `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'ten_events', 'ever', now())`,
		"etkinliksiz şampiyon": `INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'old_champion', 'all', now())`,
		"etkinlikli ilk":       `INSERT INTO user_badges (id, user_id, badge_key, period_key, tayfa_id, event_id, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'ten_events', 'all', '` + id.New().String() + `', '` + id.New().String() + `', now())`,
		"tayfasız etkinlik":    `INSERT INTO user_badges (id, user_id, badge_key, period_key, event_id, earned_at) VALUES ('` + id.New().String() + `', '` + u.String() + `', 'old_champion', 'all', '` + id.New().String() + `', now())`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, sql); err == nil {
				t.Fatal("kabul edildi")
			}
		})
	}
	// What may change: names, icon, order, the threshold's value and
	// status.
	exec(t, admin, `UPDATE catalog_badges SET labels = '{"tr": "Onluk"}', icon = 'star', position = 7, status = 'retired',
		rule = '{"kind": "threshold", "metric": "attended", "value": 12, "period": "all_time"}' WHERE key = 'ten_events'`)
	// A champion badge is earned once per event.
	ev, tf := id.New(), id.New()
	for range 2 {
		exec(t, admin, `INSERT INTO user_badges (id, user_id, badge_key, period_key, tayfa_id, event_id, earned_at) VALUES ($1, $2, 'old_champion', 'all', $3, $4, now())
			ON CONFLICT DO NOTHING`, id.New().PG(), u.PG(), tf.PG(), ev.PG())
	}
	exec(t, admin, `INSERT INTO user_badges (id, user_id, badge_key, period_key, tayfa_id, event_id, earned_at) VALUES ($1, $2, 'old_champion', 'all', $3, $4, now())`,
		id.New().PG(), u.PG(), tf.PG(), id.New().PG())
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM user_badges WHERE user_id = $1 AND badge_key = 'old_champion'`, u.PG()).Scan(&n); err != nil || n != 2 {
		t.Fatalf("şampiyonluk rozetleri %d %v", n, err)
	}
}

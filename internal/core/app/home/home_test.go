package home_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/home"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/localday"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

const (
	cupType   = "test_home_cup"
	cupDef    = `{"components":[{"kind":"celebration"}]}`
	bloodType = "test_home_blood"
	bloodDef  = `{"components":[{"kind":"celebration"}],"flags":{"sensitive_attendance":true}}`
)

type env struct {
	svc    *home.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fw, err := component.New(component.Config{Pool: d.App, Log: log})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	e := &env{svc: home.New(fw), tayfas: tayfas, pool: d.App, admin: d.Admin}
	for key, def := range map[string]string{cupType: cupDef, bloodType: bloodDef} {
		e.exec(t, `INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition)
			VALUES ($1, ARRAY['general'], 9000, '{"tr": "Test türü"}', 'event', ARRAY['in_person', 'online'], 1, $2::jsonb)`, key, def)
		e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
			SELECT key, version, labels, icon, modes, definition FROM catalog_event_types WHERE key = $1`, key)
	}
	return e
}

// exec runs a fixture statement as a superuser with triggers off.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) newTayfa(t *testing.T, interests ...string) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Ana Sayfa Tayfası", Interests: interests, ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

func (e *env) member(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: role.String(), Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

func keys(items []home.Item, enabledOnly bool) []string {
	var out []string
	for _, it := range items {
		if !enabledOnly || it.Enabled {
			out = append(out, it.Key)
		}
	}
	return out
}

// inputs is the whole list in the layout's order, with on set for keys.
func inputs(items []home.Item, on ...string) []home.Input {
	out := make([]home.Input, 0, len(items))
	for _, it := range items {
		out = append(out, home.Input{Key: it.Key, Enabled: slices.Contains(on, it.Key), Config: it.Config})
	}
	return out
}

func wantValidation(t *testing.T, name string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Errorf("%s: err = %v, %s bekleniyordu", name, err, field)
	}
}

func allKeys() []string {
	var out []string
	for _, w := range home.Widgets() {
		out = append(out, w.Key)
	}
	return out
}

// A new tayfa starts with today's fixed sections on; the interests add
// their defaults; a widget the admin switched off stays off when an
// interest is added again (ADR-0027 §3).
func TestDefaults(t *testing.T) {
	e := setup(t)
	e.exec(t, `UPDATE catalog_interests SET default_widgets = ARRAY['stats', 'goal', 'champions', 'not_a_widget'] WHERE key = 'nature'`)
	e.exec(t, `UPDATE catalog_interests SET default_widgets = ARRAY['champions'] WHERE key = 'games'`)

	tf, admin := e.newTayfa(t)
	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	if got := keys(items, true); !slices.Equal(got, home.Sections()) {
		t.Fatalf("açık bölümler = %v", got)
	}
	if got := keys(items, false); len(got) != len(home.Widgets()) || !slices.Equal(got[:4], []string{"upcoming", "announcements", "polls", "questions"}) {
		t.Fatalf("düzen = %v", got)
	}
	for i, it := range items {
		if int(it.Position) != i {
			t.Fatalf("sıra: %+v", items)
		}
	}

	// An interest added: its defaults, those that need no settings, after
	// the sections.
	_, err = e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldInterests}, Interests: []string{"nature"}})
	must(t, err)
	items, err = e.svc.Layout(ctx, admin, tf)
	must(t, err)
	if got := keys(items, true); !slices.Equal(got, append(home.Sections(), "stats", "champions")) {
		t.Fatalf("ilgi alanıyla = %v", got)
	}

	// The admin switches stats off; the interest goes and comes back, and
	// another one with an overlapping default is added: stats stays off.
	_, err = e.svc.Set(ctx, admin, tf, inputs(items, "upcoming", "champions"))
	must(t, err)
	for _, in := range [][]string{{}, {"nature", "games"}} {
		_, err = e.tayfas.Update(ctx, admin, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldInterests}, Interests: in})
		must(t, err)
	}
	items, err = e.svc.Layout(ctx, admin, tf)
	must(t, err)
	if got := keys(items, true); !slices.Equal(got, []string{"upcoming", "champions"}) {
		t.Fatalf("kapatılan yeniden açıldı: %v", got)
	}

	// Created with interests: sections first, then the defaults in catalog
	// order, once each.
	tf2, admin2 := e.newTayfa(t, "games", "nature")
	items, err = e.svc.Layout(ctx, admin2, tf2)
	must(t, err)
	if got := keys(items, true); !slices.Equal(got, append(home.Sections(), "stats", "champions")) {
		t.Fatalf("ilgi alanlarıyla oluşturma = %v", got)
	}
}

func TestSetPermissions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	in := inputs(items, "stats", "upcoming")

	mod := e.member(t, tf, authz.Moderator)
	mem := e.member(t, tf, authz.Member)
	for name, u := range map[string]id.ID{"moderatör": mod, "üye": mem} {
		if _, err := e.svc.Set(ctx, u, tf, in); !errors.Is(err, apperr.ErrPermissionDenied) {
			t.Errorf("%s: %v", name, err)
		}
		// Every member reads the layout.
		if _, err := e.svc.Layout(ctx, u, tf); err != nil {
			t.Errorf("%s okuyamadı: %v", name, err)
		}
	}
	if _, err := e.svc.Set(ctx, id.New(), tf, in); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("üye olmayan: %v", err)
	}
	if _, err := e.svc.Layout(ctx, id.New(), tf); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("üye olmayan okudu: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = $2`, tf.PG(), home.AuditUpdated); n != 0 {
		t.Fatalf("reddedilen değişiklik kaydedildi: %d", n)
	}

	got, err := e.svc.Set(ctx, admin, tf, in)
	must(t, err)
	if k := keys(got, true); !slices.Equal(k, []string{"upcoming", "stats"}) {
		t.Fatalf("yeni düzen = %v", k)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = $2`, tf.PG(), home.AuditUpdated); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM tayfa_home_widgets WHERE tayfa_id = $1`, tf.PG()); n != len(home.Widgets()) {
		t.Fatalf("satır sayısı = %d", n)
	}
}

func TestSetValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	base := inputs(items, "upcoming")
	with := func(key string, enabled bool, c home.Config) []home.Input {
		out := slices.Clone(base)
		for i := range out {
			if out[i].Key == key {
				out[i] = home.Input{Key: key, Enabled: enabled, Config: c}
			}
		}
		return out
	}
	future := time.Now().Add(48 * time.Hour)
	for name, tc := range map[string]struct {
		in    []home.Input
		field string
	}{
		"eksik":                 {base[1:], home.FieldWidgets},
		"fazla":                 {append(slices.Clone(base), home.Input{Key: "weather"}), home.FieldWidgets},
		"tekrar":                {append(slices.Clone(base[1:]), base[1]), home.FieldWidgets},
		"bilinmeyen":            {append(slices.Clone(base[1:]), home.Input{Key: "weather"}), home.FieldWidgets},
		"ayarsız hedef":         {with(home.KeyGoal, true, home.Config{}), home.FieldConfig},
		"ayarsız geri sayım":    {with(home.KeyCountdown, true, home.Config{}), home.FieldConfig},
		"bölümde ayar":          {with(home.KeyUpcoming, true, home.Config{Stats: &home.StatsConfig{Metrics: []string{"events"}, Period: home.PeriodAllTime}}), home.FieldConfig},
		"başka bileşenin ayarı": {with(home.KeyStats, true, home.Config{Goal: &home.GoalConfig{Metric: "events", Target: 1}}), home.FieldConfig},
		"beş metrik": {with(home.KeyStats, true, home.Config{Stats: &home.StatsConfig{
			Metrics: []string{"events", "attended", "participants", "duration", "hike.distance"}, Period: home.PeriodThisYear,
		}}), home.FieldConfig},
		"bilinmeyen ortak metrik": {with(home.KeyStats, true, home.Config{Stats: &home.StatsConfig{Metrics: []string{"weight"}, Period: home.PeriodThisYear}}), home.FieldConfig},
		"istatistikte 30 gün":     {with(home.KeyStats, true, home.Config{Stats: &home.StatsConfig{Metrics: []string{"events"}, Period: home.PeriodLast30Days}}), home.FieldConfig},
		"sıfır hedef":             {with(home.KeyGoal, true, home.Config{Goal: &home.GoalConfig{Metric: "events"}}), home.FieldConfig},
		"eski yıl":                {with(home.KeyGoal, true, home.Config{Goal: &home.GoalConfig{Metric: "events", Target: 3, Year: 1999}}), home.FieldConfig},
		"uzun başlık":             {with(home.KeyCountdown, true, home.Config{Countdown: &home.CountdownConfig{Title: strings.Repeat("a", 61), At: future}}), home.FieldConfig},
		"boş başlık":              {with(home.KeyCountdown, true, home.Config{Countdown: &home.CountdownConfig{Title: "  ", At: future}}), home.FieldConfig},
		"denetim karakteri":       {with(home.KeyCountdown, true, home.Config{Countdown: &home.CountdownConfig{Title: "a\u0007", At: future}}), home.FieldConfig},
		"tarihsiz":                {with(home.KeyCountdown, true, home.Config{Countdown: &home.CountdownConfig{Title: "Bayram"}}), home.FieldConfig},
		"günlük olmayan metrik":   {with(home.KeyLogSummary, true, home.Config{LogSummary: &home.LogSummaryConfig{Metric: "hike.distance", Period: home.PeriodThisYear}}), home.FieldConfig},
		"bozuk tür":               {with(home.KeyChampions, true, home.Config{Champions: &home.ChampionsConfig{EventTypes: []string{"Satranç"}, Period: home.PeriodThisYear}}), home.FieldConfig},
	} {
		_, err := e.svc.Set(ctx, admin, tf, tc.in)
		wantValidation(t, name, err, tc.field)
	}

	// A widget off may keep settings it does not need yet; one that needs
	// them may be off without them.
	ok := with(home.KeyCountdown, false, home.Config{Countdown: &home.CountdownConfig{Title: "  Bayram ", At: future}})
	got, err := e.svc.Set(ctx, admin, tf, ok)
	must(t, err)
	for _, it := range got {
		if it.Key == home.KeyCountdown && (it.Enabled || it.Config.Countdown == nil || it.Config.Countdown.Title != "Bayram") {
			t.Fatalf("geri sayım: %+v", it)
		}
	}
	if !slices.Equal(allKeys(), keys(got, false)) {
		t.Fatalf("sıra: %v", keys(got, false))
	}
}

// The home page (GET /v1/tayfas/{id}) shows only the widgets that are on,
// with their module on and a countdown not past its moment.
func TestShown(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	in := inputs(items, "upcoming", "polls", "stats", "countdown", "champions")
	for i := range in {
		if in[i].Key == home.KeyCountdown {
			in[i].Config = home.Config{Countdown: &home.CountdownConfig{Title: "Kamp", At: time.Now().Add(time.Hour)}}
		}
	}
	// Reverse the order: the page follows the admin's order.
	slices.Reverse(in)
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)

	v, _, err := e.tayfas.Get(ctx, admin, tf)
	must(t, err)
	if got := keys(v.HomeWidgets, false); !slices.Equal(got, []string{"champions", "countdown", "stats", "polls", "upcoming"}) {
		t.Fatalf("ana sayfa = %v", got)
	}
	for i, it := range v.HomeWidgets {
		if int(it.Position) != i || !it.Enabled {
			t.Fatalf("ana sayfa: %+v", v.HomeWidgets)
		}
	}

	// The events module off hides its section and the champions; polls off
	// hides its section.
	must(t, e.tayfas.SetModule(ctx, admin, tf, home.ModuleEvents, false))
	must(t, e.tayfas.SetModule(ctx, admin, tf, home.ModulePolls, false))
	v, _, err = e.tayfas.Get(ctx, admin, tf)
	must(t, err)
	if got := keys(v.HomeWidgets, false); !slices.Equal(got, []string{"countdown", "stats"}) {
		t.Fatalf("modüller kapalı = %v", got)
	}

	// A countdown past its moment is not shown.
	e.exec(t, `UPDATE tayfa_home_widgets SET config = jsonb_set(config, '{at}', to_jsonb($2::text))
		WHERE tayfa_id = $1 AND widget_key = 'countdown'`, tf.PG(), time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	shown, err := home.Shown(ctx, store.New(e.pool), tf, time.Now())
	must(t, err)
	if got := keys(shown, false); !slices.Equal(got, []string{"stats"}) {
		t.Fatalf("geçmiş geri sayım = %v", got)
	}

	// Rows the code does not know, and settings that no longer pass, are
	// skipped.
	e.exec(t, `INSERT INTO tayfa_home_widgets (tayfa_id, widget_key, enabled, position, updated_at) VALUES ($1, 'weather', true, 0, now())`, tf.PG())
	e.exec(t, `UPDATE tayfa_home_widgets SET config = '{"metric_keys": [], "period": "this_year"}' WHERE tayfa_id = $1 AND widget_key = 'stats'`, tf.PG())
	items, err = e.svc.Layout(ctx, admin, tf)
	must(t, err)
	if slices.Contains(keys(items, false), "weather") || len(items) != len(home.Widgets()) {
		t.Fatalf("bilinmeyen satır: %v", keys(items, false))
	}
	for _, it := range items {
		if it.Key == home.KeyStats && (it.Config.Stats == nil || len(it.Config.Stats.Metrics) != 4) {
			t.Fatalf("bozuk ayar varsayılana dönmedi: %+v", it)
		}
	}
}

func (e *env) stat(t *testing.T, tf id.ID, year int32, metric string, v float64, events int32) {
	t.Helper()
	e.exec(t, `INSERT INTO tayfa_stats (tayfa_id, year, metric_key, source, value, event_count, computed_at)
		VALUES ($1, $2, $3, 'common', $4, $5, now())`, tf.PG(), year, metric, v, events)
}

func TestData(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	year := int32(localday.Today(time.Now()).Year()) //nolint:gosec // a year fits
	e.stat(t, tf, year, "events", 6, 6)
	e.stat(t, tf, year, "trash_pickup.weight", 120.5, 3)
	e.stat(t, tf, year, "birding.sightings.distinct_species", 84, 9)
	e.stat(t, tf, 0, "events", 40, 40)
	e.stat(t, tf, year-1, "trash_pickup.weight", 900, 9)

	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	at := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	in := inputs(items, "upcoming", "stats", "goal", "countdown", "log_summary")
	for i := range in {
		switch in[i].Key {
		case home.KeyGoal:
			in[i].Config = home.Config{Goal: &home.GoalConfig{Metric: "trash_pickup.weight", Target: 100}}
		case home.KeyCountdown:
			in[i].Config = home.Config{Countdown: &home.CountdownConfig{Title: "Şenlik", At: at}}
		case home.KeyLogSummary:
			in[i].Config = home.Config{LogSummary: &home.LogSummaryConfig{Metric: "birding.sightings.distinct_species", Period: home.PeriodThisYear}}
		}
	}
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)
	mem := e.member(t, tf, authz.Member)

	// The statistics card without settings: the four common metrics of
	// this year; one nothing counted is 0.
	d, err := e.svc.Data(ctx, mem, tf, home.KeyStats)
	must(t, err)
	if d.Stats == nil || d.Stats.Year != year || d.Stats.Period != home.PeriodThisYear || len(d.Stats.Values) != 4 ||
		d.Stats.Values[0] != (home.MetricValue{Metric: "events", Value: 6, Events: 6}) || d.Stats.Values[1].Value != 0 || d.Stats.ComputedAt.IsZero() {
		t.Fatalf("istatistik: %+v", d.Stats)
	}

	d, err = e.svc.Data(ctx, mem, tf, home.KeyGoal)
	must(t, err)
	if g := d.Goal; g == nil || g.Year != year || g.Value != 120.5 || g.Events != 3 || !g.Reached || g.Target != 100 {
		t.Fatalf("hedef: %+v", d.Goal)
	}

	d, err = e.svc.Data(ctx, mem, tf, home.KeyCountdown)
	must(t, err)
	if d.Countdown == nil || d.Countdown.Title != "Şenlik" || !d.Countdown.At.Equal(at) {
		t.Fatalf("geri sayım: %+v", d.Countdown)
	}

	d, err = e.svc.Data(ctx, mem, tf, home.KeyLogSummary)
	must(t, err)
	if l := d.LogSummary; l == nil || l.Value != 84 || l.Events != 9 || l.Year != year {
		t.Fatalf("günlük özeti: %+v", d.LogSummary)
	}

	// A goal of last year, statistics of all time.
	for i := range in {
		switch in[i].Key {
		case home.KeyGoal:
			in[i].Config.Goal.Year = year - 1
			in[i].Config.Goal.Target = 1000
		case home.KeyStats:
			in[i].Config = home.Config{Stats: &home.StatsConfig{Metrics: []string{"events"}, Period: home.PeriodAllTime}}
		}
	}
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)
	d, err = e.svc.Data(ctx, mem, tf, home.KeyGoal)
	must(t, err)
	if g := d.Goal; g == nil || g.Year != year-1 || g.Value != 900 || g.Reached {
		t.Fatalf("geçen yılın hedefi: %+v", d.Goal)
	}
	d, err = e.svc.Data(ctx, mem, tf, home.KeyStats)
	must(t, err)
	if s := d.Stats; s == nil || s.Year != 0 || len(s.Values) != 1 || s.Values[0].Value != 40 {
		t.Fatalf("tüm zamanlar: %+v", d.Stats)
	}

	// Not here: a section, a widget off, an unknown key; and no one but a
	// member.
	for _, k := range []string{home.KeyUpcoming, home.KeyChampions, "weather"} {
		if _, err := e.svc.Data(ctx, mem, tf, k); !errors.Is(err, apperr.ErrHomeWidgetNotFound) {
			t.Errorf("%s: %v", k, err)
		}
	}
	if _, err := e.svc.Data(ctx, id.New(), tf, home.KeyStats); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Errorf("üye olmayan: %v", err)
	}
}

// celebrated writes an event of typ with an announced celebration naming
// who.
func (e *env) celebrated(t *testing.T, tf id.ID, typ, title string, start time.Time, who ...id.ID) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url)
		VALUES ($1, $2, $3, 1, 'online', $4, $5, $6, 'https://example.test/oda')`, ev.PG(), tf.PG(), typ, title, start, start.Add(time.Hour))
	e.exec(t, `INSERT INTO event_celebrations (event_id, component_key, announced_by, announced_at, updated_at)
		VALUES ($1, 'celebration', NULL, $2, $2)`, ev.PG(), start.Add(2*time.Hour))
	for i, u := range who {
		e.exec(t, `INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position)
			VALUES ($1, $2, 'celebration', $3, $4)`, id.New().PG(), ev.PG(), u.PG(), i+1)
	}
	return ev
}

func TestChampions(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	other, _ := e.newTayfa(t)
	now := time.Now().UTC()
	u1, u2 := id.New(), id.New()

	recent := e.celebrated(t, tf, cupType, "Kupa", now.Add(-48*time.Hour), u1, u2)
	older := e.celebrated(t, tf, cupType, "Eski kupa", now.Add(-40*24*time.Hour), u1)
	e.celebrated(t, tf, bloodType, "Bağış", now.Add(-24*time.Hour), u1)        // sensitive: never shown
	e.celebrated(t, tf, cupType, "Kazanansız", now.Add(-24*time.Hour))         // no winner
	e.celebrated(t, other, cupType, "Başka tayfa", now.Add(-24*time.Hour), u1) // another tayfa
	e.celebrated(t, tf, cupType, "Gelecek", now.Add(24*time.Hour), u1)         // not started
	cancelled := e.celebrated(t, tf, cupType, "İptal", now.Add(-24*time.Hour), u1)
	e.exec(t, `UPDATE events SET cancelled_at = now() WHERE id = $1`, cancelled.PG())

	items, err := e.svc.Layout(ctx, admin, tf)
	must(t, err)
	in := inputs(items, "champions")
	for i := range in {
		if in[i].Key == home.KeyChampions {
			in[i].Config = home.Config{Champions: &home.ChampionsConfig{Period: home.PeriodLast30Days}}
		}
	}
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)

	d, err := e.svc.Data(ctx, admin, tf, home.KeyChampions)
	must(t, err)
	if d.Champions == nil || len(d.Champions.Champions) != 1 {
		t.Fatalf("son 30 gün: %+v", d.Champions)
	}
	c := d.Champions.Champions[0]
	if c.Event != recent || c.Title != "Kupa" || c.Type != cupType || c.Component != "celebration" ||
		len(c.Winners) != 2 || c.Winners[0].User != u1 || c.Winners[1].User != u2 {
		t.Fatalf("şampiyon: %+v", c)
	}

	// This year, filtered to another type: nothing.
	for i := range in {
		if in[i].Key == home.KeyChampions {
			in[i].Config = home.Config{Champions: &home.ChampionsConfig{EventTypes: []string{"chess"}, Period: home.PeriodThisYear}}
		}
	}
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)
	d, err = e.svc.Data(ctx, admin, tf, home.KeyChampions)
	must(t, err)
	if len(d.Champions.Champions) != 0 {
		t.Fatalf("başka tür: %+v", d.Champions)
	}

	// The 40-day-old celebration is this year's only when it is in this
	// Europe/Istanbul year.
	for i := range in {
		if in[i].Key == home.KeyChampions {
			in[i].Config = home.Config{}
		}
	}
	_, err = e.svc.Set(ctx, admin, tf, in)
	must(t, err)
	d, err = e.svc.Data(ctx, admin, tf, home.KeyChampions)
	must(t, err)
	want := []id.ID{recent}
	if localday.Today(now.Add(-40*24*time.Hour)).Year() == localday.Today(now).Year() {
		want = append(want, older)
	}
	var got []id.ID
	for _, c := range d.Champions.Champions {
		got = append(got, c.Event)
	}
	if d.Champions.Period != home.PeriodThisYear || !slices.Equal(got, want) {
		t.Fatalf("bu yıl = %v, %v bekleniyordu", got, want)
	}

	// The events module off: no champions.
	must(t, e.tayfas.SetModule(ctx, admin, tf, home.ModuleEvents, false))
	if _, err := e.svc.Data(ctx, admin, tf, home.KeyChampions); !errors.Is(err, apperr.ErrHomeWidgetNotFound) {
		t.Fatalf("modül kapalı: %v", err)
	}
}

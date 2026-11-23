package admin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
)

// Catalog management (issue #262).

const testType = "t_panel"

// wantProblem fails unless err is a catalog check failure on field.
func wantProblem(t *testing.T, what string, err error, field string) {
	t.Helper()
	var p *ProblemError
	if !errors.As(err, &p) || p.Field != field {
		t.Fatalf("%s: %v, beklenen %s alanında sorun", what, err, field)
	}
	if p.Problem == "" {
		t.Fatalf("%s: sorun metni boş", what)
	}
}

func (e *env) catalogVersion(t *testing.T) int64 {
	t.Helper()
	var v int64
	must(t, e.pool.QueryRow(ctx, `SELECT catalog_version FROM catalog_meta`).Scan(&v))
	return v
}

// catalogAudits returns the catalog audit entries, oldest first.
func (e *env) catalogAudits(t *testing.T) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, a := range e.audits(t) {
		if strings.HasPrefix(a["action"], "catalog.") {
			out = append(out, a)
		}
	}
	return out
}

// changedEvents counts catalog.changed events.
func (e *env) changedEvents(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'catalog.changed'`)
}

func typeInput(def string) TypeInput {
	return TypeInput{
		Key: testType, InterestKeys: []string{"general"}, Position: 9000, Labels: map[string]string{"tr": "Panel türü"},
		Icon: "event", Modes: []string{catalog.ModeInPerson}, Status: catalog.StatusActive, Definition: []byte(def),
	}
}

// TestCatalogChangeNeedsReasonAndRecentSignIn: a save needs a reason and
// a recent sign-in; a preview needs neither; nobody but a platform
// administrator gets anywhere.
func TestCatalogChangeNeedsReasonAndRecentSignIn(t *testing.T) {
	e := setup(t)
	in := catalog.Interest{Key: "dance", Position: 500, Labels: map[string]string{"tr": "Dans"}, Icon: "music_note", Status: catalog.StatusActive}
	_, err := e.svc.SaveInterest(ctx, e.claims, in, true, false, "kısa")
	is(t, "gerekçesiz", err, apperr.ErrReasonRequired)
	old := e.claims
	old.AuthTime = e.now.Add(-time.Hour)
	_, err = e.svc.SaveInterest(ctx, old, in, true, false, reason)
	is(t, "eski giriş", err, apperr.ErrReauthenticationRequired)
	err = e.svc.BlockDomain(ctx, old, "example.com", reason)
	is(t, "eski girişle alan adı", err, apperr.ErrReauthenticationRequired)
	// A preview needs neither and writes nothing.
	v := e.catalogVersion(t)
	res, err := e.svc.SaveInterest(ctx, old, in, true, true, "")
	must(t, err)
	if res.Check.Problem != "" || e.catalogVersion(t) != v || e.count(t, `SELECT count(*) FROM catalog_interests WHERE key = 'dance'`) != 0 {
		t.Fatalf("önizleme yazdı ya da reddetti: %+v", res)
	}
	e.checker.err = apperr.ErrNotPlatformAdmin
	_, err = e.svc.SaveInterest(ctx, e.claims, in, true, true, "")
	is(t, "yönetici değil", err, apperr.ErrNotPlatformAdmin)
	_, err = e.svc.GetCatalog(ctx, e.claims)
	is(t, "yönetici değil okuma", err, apperr.ErrNotPlatformAdmin)
	if len(e.catalogAudits(t)) != 0 {
		t.Fatal("denetim kaydı yazıldı")
	}
}

func TestSaveInterest(t *testing.T) {
	e := setup(t)
	v0, ev0 := e.catalogVersion(t), e.changedEvents(t)
	in := catalog.Interest{Key: "dance", Position: 500, Labels: map[string]string{"tr": "Dans", "en": "Dance"}, Icon: "music_note",
		Status: catalog.StatusActive, DefaultWidgets: []string{"stats", "upcoming"}}
	res, err := e.svc.SaveInterest(ctx, e.claims, in, true, false, reason)
	must(t, err)
	if res.CatalogVersion != v0+1 || e.catalogVersion(t) != v0+1 {
		t.Fatalf("katalog sürümü: %d", res.CatalogVersion)
	}
	// catalog.changed carries the new interest (ADR-0042).
	if e.changedEvents(t) != ev0+1 {
		t.Fatal("catalog.changed yayınlanmadı")
	}
	var payload []byte
	must(t, e.pool.QueryRow(ctx, `SELECT payload FROM domain_events WHERE type = 'catalog.changed' ORDER BY id DESC LIMIT 1`).Scan(&payload))
	var ch catalog.Changed
	must(t, json.Unmarshal(payload, &ch))
	if ch.CatalogVersion != v0+1 || !strings.Contains(string(payload), `"dance"`) {
		t.Fatalf("olay: %s", payload)
	}
	a := e.catalogAudits(t)
	if len(a) != 1 || a[0]["action"] != ActionInterestCreate || a[0]["catalog_key"] != "dance" || a[0]["reason"] != reason ||
		a[0]["target_id"] != "" || a[0]["tayfa_id"] != "" {
		t.Fatalf("denetim: %v", a)
	}

	// The same save again changes nothing: no version, no record.
	in2 := in
	_, err = e.svc.SaveInterest(ctx, e.claims, in2, false, false, reason)
	must(t, err)
	if e.catalogVersion(t) != v0+1 || len(e.catalogAudits(t)) != 1 {
		t.Fatal("değişmeyen kayıt yazıldı")
	}
	// Retiring it is a change.
	in2.Status = catalog.StatusRetired
	_, err = e.svc.SaveInterest(ctx, e.claims, in2, false, false, reason)
	must(t, err)
	if a := e.catalogAudits(t); len(a) != 2 || a[1]["action"] != ActionInterestUpdate {
		t.Fatalf("denetim: %v", a)
	}

	for name, c := range map[string]struct {
		mod    func(*catalog.Interest)
		create bool
		field  string
	}{
		"var olan anahtar":     {func(*catalog.Interest) {}, true, "key"},
		"olmayan anahtar":      {func(i *catalog.Interest) { i.Key = "nothere" }, false, "key"},
		"bozuk anahtar":        {func(i *catalog.Interest) { i.Key = "Bad Key" }, true, "key"},
		"Türkçe adsız":         {func(i *catalog.Interest) { i.Labels = map[string]string{"en": "Dance"} }, false, "labels"},
		"iki satırlı ad":       {func(i *catalog.Interest) { i.Labels = map[string]string{"tr": "Da\nns"} }, false, "labels"},
		"bozuk ikon":           {func(i *catalog.Interest) { i.Icon = "<svg>" }, false, "icon"},
		"bilinmeyen durum":     {func(i *catalog.Interest) { i.Status = "deleted" }, false, "status"},
		"sıra sınır dışı":      {func(i *catalog.Interest) { i.Position = 10000 }, false, "position"},
		"bilinmeyen bileşen":   {func(i *catalog.Interest) { i.DefaultWidgets = []string{"webview"} }, false, "default_widgets"},
		"ayar isteyen bileşen": {func(i *catalog.Interest) { i.DefaultWidgets = []string{"goal"} }, false, "default_widgets"},
		"tekrar eden bileşen":  {func(i *catalog.Interest) { i.DefaultWidgets = []string{"stats", "stats"} }, false, "default_widgets"},
	} {
		t.Run(name, func(t *testing.T) {
			x := in
			c.mod(&x)
			_, err := e.svc.SaveInterest(ctx, e.claims, x, c.create, false, reason)
			wantProblem(t, name, err, c.field)
			// The preview reports the same problem instead of failing.
			res, err := e.svc.SaveInterest(ctx, e.claims, x, c.create, true, "")
			if err != nil || res.Check.Field != c.field || res.Check.Problem == "" {
				t.Fatalf("önizleme: %+v %v", res.Check, err)
			}
		})
	}
	if len(e.catalogAudits(t)) != 2 {
		t.Fatal("reddedilen kayıt denetime yazıldı")
	}
}

func TestSaveEventType(t *testing.T) {
	e := setup(t)
	def := `{"components": [{"kind": "route"}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"},
	  "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "showcase": true}}]}`

	// Preview: the check describes the type, nothing is written.
	res, err := e.svc.SaveEventType(ctx, e.claims, typeInput(def), true, "")
	must(t, err)
	c := res.Check
	if c.Problem != "" || len(c.Components) != 2 || c.Components[1].Key != "distance" || c.MinClientVersion != "0.3.0" ||
		c.Version != 1 || !c.NewVersion {
		t.Fatalf("önizleme: %+v", c)
	}
	if e.count(t, `SELECT count(*) FROM catalog_event_types WHERE key = $1`, testType) != 0 {
		t.Fatal("önizleme yazdı")
	}

	v0 := e.catalogVersion(t)
	_, err = e.svc.SaveEventType(ctx, e.claims, typeInput(def), false, reason)
	must(t, err)
	if e.count(t, `SELECT count(*) FROM catalog_event_type_versions WHERE type_key = $1`, testType) != 1 || e.catalogVersion(t) != v0+1 {
		t.Fatal("tür ya da sürümü yazılmadı")
	}
	// The new type is served like any other.
	ty, err := catalog.CurrentType(ctx, e.svc.q, testType)
	must(t, err)
	if ty.Version != 1 || len(ty.Parsed.Components) != 2 {
		t.Fatalf("tür: %+v", ty)
	}

	// Position only: no new version.
	in := typeInput(def)
	in.BaseVersion, in.Position = 1, 9001
	res, err = e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	must(t, err)
	if res.Check.NewVersion || res.Check.Version != 1 || e.count(t, `SELECT count(*) FROM catalog_event_type_versions WHERE type_key = $1`, testType) != 1 {
		t.Fatalf("sıra değişikliği sürüm yaptı: %+v", res.Check)
	}
	// A new name: version 2; version 1 stays as it was.
	in.Labels = map[string]string{"tr": "Yeni ad"}
	res, err = e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	must(t, err)
	if !res.Check.NewVersion || res.Check.Version != 2 {
		t.Fatalf("yeni sürüm: %+v", res.Check)
	}
	old, err := catalog.TypeVersion(ctx, e.svc.q, testType, 1)
	must(t, err)
	if old.Labels["tr"] != "Panel türü" {
		t.Fatal("eski sürüm değişti")
	}
	// The same definition with other spacing is not a change.
	same := in
	same.BaseVersion = 2
	same.Definition = []byte(strings.ReplaceAll(def, " ", ""))
	res, err = e.svc.SaveEventType(ctx, e.claims, same, false, reason)
	must(t, err)
	if res.Check.NewVersion {
		t.Fatal("aynı tanım yeni sürüm yaptı")
	}
	// Made on version 1 while it is 2.
	_, err = e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	wantProblem(t, "eski sürüm üzerinde", err, "version")
	actions := []string{}
	for _, a := range e.catalogAudits(t) {
		actions = append(actions, a["action"]+":"+a["catalog_key"])
	}
	if strings.Join(actions, ",") != "catalog.type_create:t_panel,catalog.type_update:t_panel,catalog.type_update:t_panel" {
		t.Fatalf("denetim: %v", actions)
	}

	// The database keeps a type's version from going back (00067).
	if _, err := e.super.Exec(ctx, `UPDATE catalog_event_types SET version = 1 WHERE key = $1`, testType); err == nil {
		t.Fatal("tür sürümü geri gitti")
	}
	if _, err := e.super.Exec(ctx, `UPDATE catalog_meta SET catalog_version = catalog_version - 1`); err == nil {
		t.Fatal("katalog sürümü geri gitti")
	}

	_, err = e.svc.SaveInterest(ctx, e.claims, catalog.Interest{Key: "old_interest", Position: 1, Labels: map[string]string{"tr": "Eski"},
		Icon: "event", Status: catalog.StatusRetired}, true, false, reason)
	must(t, err)
	for name, c := range map[string]struct {
		mod   func(*TypeInput)
		field string
	}{
		"bilinmeyen anahtar tanımda": {func(i *TypeInput) { i.Definition = []byte(`{"script": "x"}`) }, "definition"},
		"para alanı": {func(i *TypeInput) {
			i.Definition = []byte(`{"fields": [{"key": "stake", "kind": "money", "labels": {"tr": "x"}}]}`)
		}, "definition"},
		"JSON değil": {func(i *TypeInput) { i.Definition = []byte(`{`) }, "definition"},
		"çok büyük":  {func(i *TypeInput) { i.Definition = []byte(`{"x": "` + strings.Repeat("a", 17<<10) + `"}`) }, "definition"},
		"katalogda olmayan ön ayar": {func(i *TypeInput) {
			i.Definition = []byte(`{"components": [{"kind": "scoreboard", "params": {"presets": ["poker"]}}]}`)
		}, "definition"},
		"katalogda olmayan ilgi alanı": {func(i *TypeInput) { i.InterestKeys = []string{"gambling"} }, "interest_keys"},
		"emekli ilgi alanı":            {func(i *TypeInput) { i.InterestKeys = []string{"general", "old_interest"} }, "interest_keys"},
		"dört ilgi alanı":              {func(i *TypeInput) { i.InterestKeys = []string{"general", "nature", "water", "sports"} }, "interest_keys"},
		"tekrar eden ilgi alanı":       {func(i *TypeInput) { i.InterestKeys = []string{"general", "general"} }, "interest_keys"},
		"mod yok":                      {func(i *TypeInput) { i.Modes = nil }, "modes"},
		"bilinmeyen mod":               {func(i *TypeInput) { i.Modes = []string{"hybrid"} }, "modes"},
		"var olan tür":                 {func(i *TypeInput) { i.Key = "hike" }, "key"},
	} {
		t.Run(name, func(t *testing.T) {
			x := typeInput(def)
			x.Key = "t_panel_new"
			c.mod(&x)
			_, err := e.svc.SaveEventType(ctx, e.claims, x, false, reason)
			wantProblem(t, name, err, c.field)
		})
	}
	// An offered preset that is in the catalog passes.
	ok := typeInput(`{"components": [{"kind": "scoreboard", "params": {"presets": ["batak", "okey"]}}]}`)
	ok.Key = "t_panel_board"
	res, err = e.svc.SaveEventType(ctx, e.claims, ok, true, "")
	if err != nil || res.Check.Problem != "" {
		t.Fatalf("ön ayarlı tür: %+v %v", res.Check, err)
	}
}

// TestTypeChangeKeepsBadgesValid: a type change that would make a badge
// count something sensitive is refused (ADR-0039 §2).
func TestTypeChangeKeepsBadgesValid(t *testing.T) {
	e := setup(t)
	_, err := e.svc.SaveEventType(ctx, e.claims, typeInput(`{}`), false, reason)
	must(t, err)
	b := catalog.Badge{Key: "t_panel_first", Scope: catalog.BadgeScopeUser, Position: 9000, Labels: map[string]string{"tr": "İlk panel"},
		Icon: "stars", Status: catalog.StatusActive, Rule: catalog.BadgeRule{Kind: catalog.RuleFirst, EventType: testType}}
	_, err = e.svc.SaveBadge(ctx, e.claims, b, true, false, reason)
	must(t, err)
	in := typeInput(`{"flags": {"sensitive_attendance": true}}`)
	in.BaseVersion = 1
	_, err = e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	wantProblem(t, "hassas katılım", err, "definition")
	if e.count(t, `SELECT count(*) FROM catalog_event_type_versions WHERE type_key = $1`, testType) != 1 {
		t.Fatal("reddedilen sürüm kaldı")
	}
	// Once the badge is retired the change goes through.
	b.Status = catalog.StatusRetired
	_, err = e.svc.SaveBadge(ctx, e.claims, b, false, false, reason)
	must(t, err)
	_, err = e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	must(t, err)
}

func TestSaveBadge(t *testing.T) {
	e := setup(t)
	b := catalog.Badge{Key: "t_panel_ten", Scope: catalog.BadgeScopeUser, Position: 9000, Labels: map[string]string{"tr": "On etkinlik"},
		Icon: "stars", Status: catalog.StatusActive,
		Rule: catalog.BadgeRule{Kind: catalog.RuleThreshold, Metric: "attended", Value: 10, Period: catalog.PeriodAllTime}}
	v0 := e.catalogVersion(t)
	_, err := e.svc.SaveBadge(ctx, e.claims, b, true, false, reason)
	must(t, err)
	var rule []byte
	must(t, e.pool.QueryRow(ctx, `SELECT rule FROM catalog_badges WHERE key = $1`, b.Key).Scan(&rule))
	r, err := catalog.ParseBadgeRule(compactJSON(rule))
	must(t, err)
	if r != b.Rule || e.catalogVersion(t) != v0+1 {
		t.Fatalf("kural: %+v", r)
	}
	// The threshold may change; the scope, kind and period may not.
	b2 := b
	b2.Rule.Value = 20
	_, err = e.svc.SaveBadge(ctx, e.claims, b2, false, false, reason)
	must(t, err)
	for name, c := range map[string]struct {
		mod    func(*catalog.Badge)
		create bool
		field  string
	}{
		"kapsam değişir":     {func(x *catalog.Badge) { x.Scope = catalog.BadgeScopeTayfa }, false, "rule"},
		"dönem değişir":      {func(x *catalog.Badge) { x.Rule.Period = catalog.PeriodYear }, false, "rule"},
		"kural türü değişir": {func(x *catalog.Badge) { x.Rule = catalog.BadgeRule{Kind: catalog.RuleFirst} }, false, "rule"},
		"tayfa ilk katılım": {func(x *catalog.Badge) {
			x.Key, x.Scope, x.Rule = "t_panel_x", catalog.BadgeScopeTayfa, catalog.BadgeRule{Kind: catalog.RuleFirst}
		}, true, "rule"},
		"bilinmeyen metrik": {func(x *catalog.Badge) { x.Key, x.Rule.Metric = "t_panel_y", "money" }, true, "rule"},
		"olmayan tür": {func(x *catalog.Badge) {
			x.Key, x.Rule = "t_panel_z", catalog.BadgeRule{Kind: catalog.RuleChampion, EventType: "poker"}
		}, true, "rule"},
		"bilinmeyen kural":  {func(x *catalog.Badge) { x.Key, x.Rule = "t_panel_w", catalog.BadgeRule{Kind: "lucky"} }, true, "rule"},
		"bilinmeyen kapsam": {func(x *catalog.Badge) { x.Key, x.Scope = "t_panel_v", "world" }, true, "scope"},
		"var olan rozet":    {func(*catalog.Badge) {}, true, "key"},
		"eşik sıfır":        {func(x *catalog.Badge) { x.Key, x.Rule.Value = "t_panel_u", 0 }, true, "rule"},
	} {
		t.Run(name, func(t *testing.T) {
			x := b2
			c.mod(&x)
			_, err := e.svc.SaveBadge(ctx, e.claims, x, c.create, false, reason)
			wantProblem(t, name, err, c.field)
		})
	}
}

func TestSaveStudyTag(t *testing.T) {
	e := setup(t)
	tag := func(key, parent string) catalog.StudyTag {
		return catalog.StudyTag{Key: key, ParentKey: parent, Position: 50, Labels: map[string]string{"tr": "Etiket " + key}, Status: catalog.StatusActive}
	}
	v0 := e.catalogVersion(t)
	// A level and a subject get their "Diğer"; a topic does not.
	for _, x := range []catalog.StudyTag{tag("kpss", ""), tag("kpss_tarih", "kpss"), tag("kpss_tarih_osmanli", "kpss_tarih")} {
		_, err := e.svc.SaveStudyTag(ctx, e.claims, x, true, false, reason)
		must(t, err)
	}
	if e.count(t, `SELECT count(*) FROM catalog_study_tags WHERE key IN ('kpss_other', 'kpss_tarih_other')`) != 2 ||
		e.count(t, `SELECT count(*) FROM catalog_study_tags WHERE key = 'kpss_tarih_osmanli_other'`) != 0 {
		t.Fatal("Diğer etiketleri")
	}
	if e.catalogVersion(t) != v0+3 {
		t.Fatal("katalog sürümü")
	}
	tags, err := catalog.StudyTags(ctx, e.svc.q)
	must(t, err)
	must(t, catalog.CheckStudyTags(tags))

	for name, c := range map[string]struct {
		t      catalog.StudyTag
		create bool
		field  string
	}{
		"dördüncü seviye": {tag("kpss_x", "kpss_tarih_osmanli"), true, "parent_key"},
		"olmayan üst":     {tag("kpss_y", "nothere"), true, "parent_key"},
		"üst değişir":     {tag("kpss_tarih", "lgs"), false, "parent_key"},
		"aktif üstün Diğeri emekli": {func() catalog.StudyTag {
			x := tag("kpss_other", "kpss")
			x.Status = catalog.StatusRetired
			return x
		}(), false, "key"},
		"çok uzun anahtar": {tag("k"+strings.Repeat("a", 35), ""), true, "key"},
		"var olan":         {tag("kpss", ""), true, "key"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.SaveStudyTag(ctx, e.claims, c.t, c.create, false, reason)
			wantProblem(t, name, err, c.field)
		})
	}
	// Retiring the level is allowed; its "Diğer" may stay.
	x := tag("kpss", "")
	x.Status = catalog.StatusRetired
	_, err = e.svc.SaveStudyTag(ctx, e.claims, x, false, false, reason)
	must(t, err)
}

func TestSaveScorePreset(t *testing.T) {
	e := setup(t)
	v0, ev0 := e.catalogVersion(t), e.changedEvents(t)
	p := ScorePreset{Key: "king", Position: 20, Labels: map[string]string{"tr": "King"},
		Params: []byte(`{"players_min": 4, "players_max": 4, "side": "player", "rounds": "round", "winner": "high", "result": "points"}`),
		Status: catalog.StatusActive}
	_, err := e.svc.SaveScorePreset(ctx, e.claims, p, true, false, reason)
	must(t, err)
	// Presets are not part of GET /v1/catalog.
	if e.catalogVersion(t) != v0 || e.changedEvents(t) != ev0 {
		t.Fatal("ön ayar katalog sürümünü değiştirdi")
	}
	if a := e.catalogAudits(t); len(a) != 1 || a[0]["action"] != ActionPresetCreate || a[0]["catalog_key"] != "king" {
		t.Fatalf("denetim: %v", a)
	}
	for name, params := range map[string]string{
		"bilinmeyen kural": `{"stake": 10}`,
		"bilinmeyen değer": `{"side": "everyone"}`,
		"best_of":          `{"best_of": 4}`,
		"oyuncu sayısı":    `{"players_min": 5, "players_max": 2}`,
		"dizi":             `[]`,
		"JSON değil":       `{`,
	} {
		t.Run(name, func(t *testing.T) {
			x := p
			x.Params = []byte(params)
			_, err := e.svc.SaveScorePreset(ctx, e.claims, x, false, false, reason)
			wantProblem(t, name, err, "params")
		})
	}
}

func TestBlockDomain(t *testing.T) {
	e := setup(t)
	must(t, e.svc.BlockDomain(ctx, e.claims, " Example.COM. ", reason))
	if e.count(t, `SELECT count(*) FROM blocked_domains WHERE domain = 'example.com' AND created_by = $1`, e.admin.PG()) != 1 {
		t.Fatal("alan adı eklenmedi")
	}
	// The panel tells the starting list from the panel's entries by the
	// initial column (00069), not by created_by (cleared on account
	// deletion, #334).
	got, err := e.svc.GetCatalog(ctx, e.claims)
	must(t, err)
	initial := map[string]bool{}
	for _, d := range got.Domains {
		initial[d.Domain] = d.Initial
	}
	if v, ok := initial["example.com"]; !ok || v {
		t.Fatalf("panelden eklenen: %v", got.Domains)
	}
	if v, ok := initial["bit.ly"]; !ok || !v {
		t.Fatalf("başlangıç listesi: %v", got.Domains)
	}
	wantProblem(t, "tekrar", e.svc.BlockDomain(ctx, e.claims, "example.com", reason), "domain")
	wantProblem(t, "IP", e.svc.BlockDomain(ctx, e.claims, "192.168.1.1", reason), "domain")
	wantProblem(t, "tek etiket", e.svc.BlockDomain(ctx, e.claims, "localhost", reason), "domain")
	must(t, e.svc.UnblockDomain(ctx, e.claims, "example.com", reason))
	wantProblem(t, "listede yok", e.svc.UnblockDomain(ctx, e.claims, "example.com", reason), "domain")
	// The list the migration started with can be changed too.
	must(t, e.svc.UnblockDomain(ctx, e.claims, "bit.ly", reason))
	a := e.catalogAudits(t)
	if len(a) != 3 || a[0]["action"] != ActionDomainBlock || a[0]["catalog_key"] != "example.com" ||
		a[1]["action"] != ActionDomainUnblock || a[2]["catalog_key"] != "bit.ly" {
		t.Fatalf("denetim: %v", a)
	}
	v, err := e.svc.GetCatalog(ctx, e.claims)
	must(t, err)
	for _, d := range v.Domains {
		if d.Domain == "bit.ly" || d.Domain == "example.com" {
			t.Fatalf("liste: %v", v.Domains)
		}
	}
}

// TestGetCatalog: retired types too, with presets and blocked domains.
func TestGetCatalog(t *testing.T) {
	e := setup(t)
	in := typeInput(`{}`)
	in.Status = catalog.StatusRetired
	_, err := e.svc.SaveEventType(ctx, e.claims, in, false, reason)
	must(t, err)
	v, err := e.svc.GetCatalog(ctx, e.claims)
	must(t, err)
	found := false
	for _, ty := range v.Types {
		if ty.Key == testType && ty.Status == catalog.StatusRetired && ty.Position == 9000 {
			found = true
		}
	}
	if !found || len(v.Interests) == 0 || len(v.Presets) == 0 || len(v.Domains) == 0 || len(v.StudyTags) == 0 || v.Version < 1 {
		t.Fatalf("katalog: tür %v, %d ilgi alanı, %d ön ayar, %d alan adı", found, len(v.Interests), len(v.Presets), len(v.Domains))
	}
	for _, d := range v.Domains {
		if !d.Initial || d.CreatedAt.IsZero() {
			t.Fatalf("başlangıç listesi: %+v", d)
		}
	}
}

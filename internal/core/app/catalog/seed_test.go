package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// studyTypes are the Eğitim types that have a study tag field since their
// second version (00068, issue #333; ADR-0043 §6).
var studyTypes = []string{"study_group", "problem_session", "peer_teaching", "mock_exam"}

// pairPresets are the scoreboard's pair presets; the board game night
// offers them since its second version (00070, issue #343; ADR-0044).
var pairPresets = []string{"batak_pairs", "okey_101_pairs", "okey_pairs", "pisti_pairs"}

// seedTypes is the number of active types the migrations leave (00066,
// issue #260).
const seedTypes = 65

// seedTypeOrder is the catalog's type order (Notion "Faz 3 — Katalog
// ayrıntıları ve şema ekleri" §B): by interest, "Genel" last.
var seedTypeOrder = []string{
	"convoy", "moto_tour", "car_meet", "auto_show", "maintenance_workshop", "hike", "camp", "summit",
	"climbing", "birding", "stargazing", "fishing", "diving", "paddling", "sailing", "team_sport",
	"running", "cycling", "racket", "swimming", "training", "yoga", "sports", "board_game_night",
	"quiz_night", "chess", "esports", "gaming", "games_other", "trash_pickup", "tree_planting",
	"blood_drive", "aid_drive", "visit", "shelter_volunteering", "dog_walk", "stray_feeding",
	"book_club", "reading_marathon", "book_swap", "writing_workshop", "art_workshop", "photo_walk",
	"museum_visit", "music_rehearsal", "choir", "show_outing", "talk", "language_exchange",
	"hackathon", "city_tour", "day_trip", "heritage_site", "festival", "potluck", "cooking_class",
	"food_tour", "study_circle", "group_reading", "group_visit", "study_group", "problem_session",
	"peer_teaching", "mock_exam", "general",
}

// clientIcons are the keys of the client's fixed icon map (Tayfam-App
// lib/core/katalog/ikonlar.dart, ADR-0024 §2): a catalog icon must be one
// of them.
var clientIcons = []string{
	"account_balance", "agriculture", "anchor", "auto_awesome", "auto_stories", "bakery_dining", "bloodtype",
	"brush", "build", "cabin", "calculate", "calendar_month", "camping", "casino", "celebration",
	"cleaning_services", "code", "cruelty_free", "directions_bike", "directions_car", "directions_run",
	"diversity_3", "downhill_skiing", "eco", "edit", "emoji_events", "event", "extension", "favorite",
	"festival", "fitness_center", "flag", "forest", "garage", "grass", "groups", "handshake", "hiking",
	"history_edu", "kayaking", "landscape", "lightbulb", "local_dining", "local_fire_department",
	"local_florist", "map", "menu_book", "mic", "military_tech", "museum", "music_note", "nightlight",
	"palette", "park", "pets", "phishing", "photo_camera", "pool", "quiz", "recycling", "restaurant",
	"sailing", "school", "science", "scuba_diving", "self_improvement", "snowboarding", "soup_kitchen",
	"spa", "sports", "sports_basketball", "sports_esports", "sports_soccer", "sports_tennis",
	"sports_volleyball", "star", "stars", "surfing", "terrain", "theater_comedy", "timer", "tour",
	"translate", "two_wheeler", "videogame_asset", "volunteer_activism", "water", "workspace_premium",
}

// TestSeedCatalogData checks the full catalog (issue #260) beyond the
// validator, which Get already applies to every definition.
func TestSeedCatalogData(t *testing.T) {
	svc, pool, admin := setup(t)
	c, notModified, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notModified || c.Version != seedVersion {
		t.Fatalf("sürüm = %d, değişmedi = %v", c.Version, notModified)
	}

	// 15 interests; each starts a tayfa with the basic sections only
	// (platform owner, 2026-11-23).
	var interests []string
	for _, in := range c.Interests {
		interests = append(interests, in.Key)
		if in.Status != StatusActive || in.Labels["tr"] == "" || !slices.Contains(clientIcons, in.Icon) ||
			!slices.Equal(in.DefaultWidgets, []string{"upcoming", "announcements", "polls", "questions"}) {
			t.Errorf("ilgi alanı = %+v", in)
		}
	}
	if want := []string{"automotive", "nature", "water", "sports", "games", "environment", "animals", "books",
		"arts", "learning", "travel", "food", "faith", "education", "general"}; !slices.Equal(interests, want) {
		t.Fatalf("ilgi alanları = %v", interests)
	}

	// The types, in order; the Faz 1 types but fishing, the Eğitim types
	// (00068, issue #333) and the board game night (00070, issue #343) are
	// at version 2, the others at 1.
	var types []string
	byKey := map[string]EventType{}
	for _, et := range c.Types {
		types = append(types, et.Key)
		byKey[et.Key] = et
		want := int32(1)
		if (slices.Contains(faz1Types, et.Key) && et.Key != "fishing") || slices.Contains(studyTypes, et.Key) || et.Key == "board_game_night" {
			want = 2
		}
		if et.Version != want || et.Status != StatusActive || et.Parsed == nil || !slices.Contains(clientIcons, et.Icon) {
			t.Errorf("tür = %+v", et)
		}
		for _, k := range et.InterestKeys {
			if !slices.Contains(interests, k) {
				t.Errorf("%s: ilgi alanı %q katalogda yok", et.Key, k)
			}
		}
		// A component's key is a metric of the type (ADR-0039): it is
		// never a common metric's name.
		for _, ec := range et.Parsed.Components {
			if isCommon(ec.Key) {
				t.Errorf("%s: bileşen anahtarı %q ortak metrikle çakışıyor", et.Key, ec.Key)
			}
		}
	}
	if !slices.Equal(types, seedTypeOrder) || len(types) != seedTypes {
		t.Fatalf("türler = %v", types)
	}
	// Notion marks the types that may be online; the Faz 1 types keep both
	// modes.
	both := []string{"training", "yoga", "quiz_night", "chess", "talk", "language_exchange", "study_group",
		"problem_session", "peer_teaching"}
	for _, et := range c.Types {
		want := []string{ModeInPerson}
		if slices.Contains(both, et.Key) || slices.Contains(faz1Types, et.Key) {
			want = []string{ModeInPerson, ModeOnline}
		}
		if !slices.Equal(et.Modes, want) {
			t.Errorf("%s: modlar = %v", et.Key, et.Modes)
		}
	}
	// Types of more than one interest: the first is the main one.
	for key, want := range map[string][]string{
		"dog_walk": {"animals", "nature"}, "writing_workshop": {"books", "arts"}, "photo_walk": {"arts", "travel"},
		"museum_visit": {"arts", "travel"}, "show_outing": {"arts", "travel"}, "summit": {"nature"},
	} {
		if !slices.Equal(byKey[key].InterestKeys, want) {
			t.Errorf("%s: ilgi alanları = %v", key, byKey[key].InterestKeys)
		}
	}

	comp := func(typ, key string) definition.Component {
		t.Helper()
		found, ok := byKey[typ].Parsed.Component(key)
		if !ok {
			t.Fatalf("%s: %s bileşeni yok", typ, key)
		}
		return found
	}
	// Type flags (ADR-0037 §1).
	if f := byKey["blood_drive"].Parsed.Flags; !f.SensitiveAttendance || !f.PersonalScopeForbidden || f.AlbumNotice {
		t.Errorf("kan bağışı bayrakları = %+v", f)
	}
	if f := byKey["visit"].Parsed.Flags; !f.AlbumNotice || f.SensitiveAttendance {
		t.Errorf("ziyaret bayrakları = %+v", f)
	}
	for _, et := range c.Types {
		if et.Key != "blood_drive" && et.Key != "visit" && et.Parsed.Flags != (definition.Flags{}) {
			t.Errorf("%s: bayraklar = %+v", et.Key, et.Parsed.Flags)
		}
	}
	// The mock exam's net is personal and sensitive, never in the
	// showcase, and nothing ranks it.
	if net := comp("mock_exam", "net"); net.Params["scope"].String() != "personal" || !net.Params["sensitive"].Bool() || net.Params["showcase"].Bool() {
		t.Errorf("deneme neti = %+v", net)
	}
	if slices.ContainsFunc(byKey["mock_exam"].Parsed.Components, func(c definition.Component) bool { return c.Kind == definition.CompRanking }) {
		t.Error("deneme sınavında sıralama var")
	}
	// Votes: the gastronomy tour's "route stops" is a free vote (route_stops
	// was dropped on 2026-11-23, #254); the others as Notion says.
	for typ, want := range map[string][2]string{
		"food_tour": {"best_stop", "free"}, "car_meet": {"best_vehicle", "vehicles"}, "photo_walk": {"best_photo", "album"},
		"book_club": {"next_book", "free"}, "hackathon": {"vote", "free"}, "general": {"vote", "free"},
	} {
		if v := comp(typ, want[0]); v.Kind != definition.CompVote || v.Params["source"].String() != want[1] {
			t.Errorf("%s oylaması = %+v", typ, v)
		}
	}
	// The organizer's toolbox of "Genel" is off by default.
	for _, gc := range byKey["general"].Parsed.Components {
		if gc.Optional == nil || *gc.Optional {
			t.Errorf("genel: %s varsayılan kapalı değil", gc.Key)
		}
	}
	// "Spor (diğer)" has a free-text branch in its new version; the old
	// version keeps its choice (its events stay there).
	if f, ok := byKey["sports"].Parsed.Field("sport"); !ok || f.Kind != definition.KindText || f.Vocabulary != "sport" || !f.Required {
		t.Errorf("spor dalı = %+v", f)
	}

	// The Eğitim types' study tag fields (ADR-0043 §6); no other type has
	// one.
	for typ, want := range map[string]definition.Field{
		"study_group":     {Key: "subjects", MinDepth: definition.StudyDepthSubject, MaxDepth: definition.StudyDepthTopic, MaxSelected: 5},
		"problem_session": {Key: "subjects", MinDepth: definition.StudyDepthSubject, MaxDepth: definition.StudyDepthTopic, MaxSelected: 5},
		"peer_teaching":   {Key: "subject", Required: true, MinDepth: definition.StudyDepthSubject, MaxDepth: definition.StudyDepthTopic, MaxSelected: 1},
		"mock_exam":       {Key: "exam", MinDepth: definition.StudyDepthLevel, MaxDepth: definition.StudyDepthSubject, MaxSelected: 1},
	} {
		fs := byKey[typ].Parsed.Fields
		if len(fs) != 1 {
			t.Errorf("%s: alanlar = %+v", typ, fs)
			continue
		}
		f := fs[0]
		if f.Kind != definition.KindStudyTags || f.Key != want.Key || f.Required != want.Required || f.MinDepth != want.MinDepth ||
			f.MaxDepth != want.MaxDepth || f.MaxSelected != want.MaxSelected || f.Labels["tr"] == "" {
			t.Errorf("%s: ders/konu alanı = %+v", typ, f)
		}
	}
	for _, et := range c.Types {
		for _, f := range et.Parsed.Fields {
			if f.Kind == definition.KindStudyTags && !slices.Contains(studyTypes, et.Key) {
				t.Errorf("%s: beklenmeyen ders/konu alanı %s", et.Key, f.Key)
			}
		}
	}
	// Version 1 keeps the definition the types were seeded with.
	for _, typ := range studyTypes {
		v1, err := svc.TypeVersion(ctx, typ, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(v1.Parsed.Fields) != 0 || len(v1.Parsed.Components) != len(byKey[typ].Parsed.Components) {
			t.Errorf("%s sürüm 1 = %+v", typ, v1.Parsed)
		}
	}

	// Every scoreboard preset a type offers is in the catalog.
	presets := map[string]bool{}
	rows, err := admin.Query(ctx, `SELECT key FROM catalog_score_presets WHERE status = 'active'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		presets[k] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(presets) != 12 {
		t.Fatalf("ön ayarlar = %v", presets)
	}
	for _, et := range c.Types {
		for _, pc := range et.Parsed.Components {
			ps, _ := pc.Params["presets"].Value.([]string)
			for _, p := range ps {
				if !presets[p] {
					t.Errorf("%s: bilinmeyen ön ayar %q", et.Key, p)
				}
			}
		}
	}
	if ps, _ := comp("board_game_night", "scoreboard").Params["presets"].Value.([]string); len(ps) != 11 || slices.Contains(ps, "team_rounds") {
		t.Errorf("masa oyunu ön ayarları = %v", ps)
	} else {
		for _, p := range pairPresets {
			if !slices.Contains(ps, p) {
				t.Errorf("masa oyunu gecesinde %s yok", p)
			}
		}
	}

	// The badges pass the panel's check (ADR-0039).
	var badges []string
	q := store.New(pool)
	for _, b := range c.Badges {
		badges = append(badges, b.Key)
		if err := CheckBadge(ctx, q, b); err != nil || b.Status != StatusActive || !slices.Contains(clientIcons, b.Icon) {
			t.Errorf("rozet %s: %v", b.Key, err)
		}
	}
	if want := []string{"first_attendance", "attended_10", "attended_50", "attended_100", "championship"}; !slices.Equal(badges, want) {
		t.Fatalf("rozetler = %v", badges)
	}

	// The study tag list follows its content rules: a "Diğer" under every
	// level and subject.
	if err := CheckStudyTags(c.StudyTags); err != nil {
		t.Fatal(err)
	}
	var levels []string
	tags := map[string]StudyTag{}
	for _, tag := range c.StudyTags {
		tags[tag.Key] = tag
		if tag.ParentKey == "" {
			levels = append(levels, tag.Key)
		}
		if tag.Status != StatusActive || (strings.HasSuffix(tag.Key, OtherSuffix) && tag.Labels["tr"] != "Diğer") {
			t.Errorf("etiket = %+v", tag)
		}
	}
	if want := []string{"lgs", "tyt", "ayt", "high_school", "yds", "other_exams"}; !slices.Equal(levels, want) || len(c.StudyTags) != 294 {
		t.Fatalf("düzeyler = %v, etiket = %d", levels, len(c.StudyTags))
	}
	for key, want := range map[string][2]string{
		"ayt_math":               {"ayt", "Matematik"},
		"ayt_math_derivative":    {"ayt_math", "Türev"},
		"lgs_hist_after_ataturk": {"lgs_history", "Atatürk'ün ölümü ve sonrası"},
		"hs_physics_11":          {"hs_physics", "11. sınıf"},
		"yds_en_cloze_test":      {"yds_english", "Cloze test"},
		"tyt_history_other":      {"tyt_history", "Diğer"},
		"other_exams_other":      {"other_exams", "Diğer"},
	} {
		if tag := tags[key]; tag.ParentKey != want[0] || tag.Labels["tr"] != want[1] {
			t.Errorf("%s = %+v", key, tag)
		}
	}
	// A subject can be chosen; a level cannot.
	if err := CheckStudySelection(ctx, q, "tyt_math", "tyt_math_functions"); err != nil {
		t.Fatal(err)
	}
	if err := CheckStudySelection(ctx, q, "lgs", ""); err == nil {
		t.Fatal("düzey ders olarak seçildi")
	}
}

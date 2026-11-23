package event

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// values builds field values from key, JSON text pairs.
func values(kv ...string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

func wantValues(t *testing.T, what string, got map[string]json.RawMessage, kv ...string) {
	t.Helper()
	want := values(kv...)
	if len(got) != len(want) {
		t.Errorf("%s: alanlar = %v", what, got)
		return
	}
	for k, w := range want {
		if string(got[k]) != string(w) {
			t.Errorf("%s: %s = %s, %s bekleniyordu", what, k, got[k], w)
		}
	}
}

func gaming() Input {
	in := input()
	in.Type = TypeGaming
	in.Fields = values("game", `"  Tavla "`, "platform", `"mobile"`)
	return in
}

// An event's type-specific data is the values of the fields its type
// version defines (issue #237, ADR-0025 §3).
func TestCreateWithFields(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)

	v := e.create(t, tf, admin, gaming())
	// Video oyunu is at its second version since issue #260.
	if v.Type != TypeGaming || v.TypeVersion != 2 {
		t.Fatalf("etkinlik = %+v", v)
	}
	// Stored in their clean form: the text is trimmed.
	wantValues(t, "oluşturma", v.Fields, "game", `"Tavla"`, "platform", `"mobile"`)
	// Every member who sees the event sees its fields.
	got, err := e.svc.Get(ctx, member, tf, v.ID)
	must(t, err)
	wantValues(t, "üye", got.Fields, "game", `"Tavla"`, "platform", `"mobile"`)
	if n := e.count(t, `SELECT count(*) FROM event_field_values WHERE event_id = $1`, v.ID.PG()); n != 2 {
		t.Fatalf("satır = %d", n)
	}

	// An optional field may be left out; a general event has no fields.
	in := input()
	in.Type = TypeSports
	in.Fields = values("sport", `"tennis"`)
	sp := e.create(t, tf, admin, in)
	wantValues(t, "isteğe bağlı alan", sp.Fields, "sport", `"tennis"`)
	gen := e.create(t, tf, admin, input())
	if gen.Fields == nil || len(gen.Fields) != 0 {
		t.Fatalf("genel etkinliğin alanları = %v", gen.Fields)
	}

	// Field values stay out of the audit record and the outbox event.
	for _, table := range []string{"audit_logs", "domain_events"} {
		col := "metadata"
		if table == "domain_events" {
			col = "payload"
		}
		if n := e.count(t, `SELECT count(*) FROM `+table+` WHERE `+col+`::text LIKE '%Tavla%'`); n != 0 {
			t.Errorf("%s alan değerini taşıyor", table)
		}
	}

	for name, tc := range map[string]struct {
		fields map[string]json.RawMessage
		field  string
	}{
		"zorunlu alan yok":   {values("platform", `"pc"`), "fields.game"},
		"null değer":         {values("game", `null`, "platform", `"pc"`), "fields.game"},
		"sayı metin yerine":  {values("game", `7`, "platform", `"pc"`), "fields.game"},
		"uzun metin":         {values("game", `"`+strings.Repeat("a", 61)+`"`, "platform", `"pc"`), "fields.game"},
		"bozuk json":         {values("game", `"CS2`, "platform", `"pc"`), "fields.game"},
		"seçenekte olmayan":  {values("game", `"CS2"`, "platform", `"amiga"`), "fields.platform"},
		"türde olmayan alan": {values("game", `"CS2"`, "platform", `"pc"`, "stake", `100`), "fields"},
	} {
		in := gaming()
		in.Fields = tc.fields
		_, err := e.svc.Create(ctx, admin, tf, in)
		wantField(t, name, err, tc.field)
	}
	// Nothing was written for the refused events.
	if n := e.count(t, `SELECT count(*) FROM events WHERE tayfa_id = $1`, tf.PG()); n != 3 {
		t.Fatalf("etkinlik = %d", n)
	}
}

func TestUpdateFields(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, admin, gaming())

	// The new set replaces the old one.
	v, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldFields}, Input{Fields: values("game", `"Okey"`, "platform", `"pc"`)})
	must(t, err)
	wantValues(t, "güncelleme", v.Fields, "game", `"Okey"`, "platform", `"pc"`)
	in := input()
	in.Type = TypeSports
	in.Fields = values("sport", `"tennis"`, "venue_name", `"Kordon kortları"`)
	sp := e.create(t, tf, admin, in)
	v, err = e.svc.Update(ctx, admin, tf, sp.ID, []string{FieldFields}, Input{Fields: values("sport", `"volleyball"`)})
	must(t, err)
	wantValues(t, "alan kaldırma", v.Fields, "sport", `"volleyball"`)

	// The set is complete: a required field cannot be dropped.
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldFields}, Input{Fields: values("platform", `"xbox"`)})
	wantField(t, "zorunlu alansız güncelleme", err, "fields.game")
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldFields}, Input{Fields: values("game", `"x"`, "platform", `"amiga"`)})
	wantField(t, "bozuk değer", err, "fields.platform")
	// The type's data is not in details any more (issue #243).
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{"details"}, Input{})
	wantField(t, "alanlı türde detay", err, "update_mask")
	// A type without fields takes only the empty set.
	gen := e.create(t, tf, admin, input())
	_, err = e.svc.Update(ctx, admin, tf, gen.ID, []string{FieldFields}, Input{Fields: values("game", `"x"`)})
	wantField(t, "alansız türde değer", err, "fields")
	_, err = e.svc.Update(ctx, admin, tf, gen.ID, []string{FieldFields}, Input{})
	must(t, err)

	// Like every other edit: the organizer and the tayfa's managers only.
	_, err = e.svc.Update(ctx, member, tf, ev.ID, []string{FieldFields}, Input{Fields: values("game", `"x"`, "platform", `"pc"`)})
	wantErr(t, "üyenin güncellemesi", err, apperr.ErrPermissionDenied)
	got, err := e.svc.Get(ctx, admin, tf, ev.ID)
	must(t, err)
	wantValues(t, "reddedilen güncellemelerden sonra", got.Fields, "game", `"Okey"`, "platform", `"pc"`)
}

// An event keeps the type version it was created with (ADR-0025 §2).
func TestEventKeepsItsTypeVersion(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	old := e.create(t, tf, admin, gaming())

	// The catalog gets a new version of the type (the seed's is 2, issue
	// #260): the platform field is gone and a new required field came.
	const v2 = `{"fields": [
	  {"key": "game", "kind": "text", "required": true, "max_length": 60, "labels": {"tr": "Oyun"}},
	  {"key": "players", "kind": "integer", "required": true, "min": 2, "max": 64, "labels": {"tr": "Oyuncu"}}
	]}`
	e.exec(t, `INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
		VALUES ('gaming', 3, '{"tr": "Video oyunu"}', 'sports_esports', ARRAY['online'], $1::jsonb)`, v2)
	e.exec(t, `UPDATE catalog_event_types SET version = 3, modes = ARRAY['online'], definition = $1::jsonb WHERE key = 'gaming'`, v2)

	// A new event is created with, and checked against, the new version.
	in := gaming()
	in.Location = Location{Mode: ModeOnline, OnlineURL: "https://example.com/oda"}
	_, err := e.svc.Create(ctx, admin, tf, in)
	wantField(t, "yeni sürümde eski alanlar", err, "fields.players")
	in.Fields = values("game", `"Okey"`, "players", `4`)
	in.ClientRequestID = id.New().String()
	fresh := e.create(t, tf, admin, in)
	if fresh.TypeVersion != 3 {
		t.Fatalf("yeni etkinliğin sürümü = %d", fresh.TypeVersion)
	}
	wantValues(t, "yeni sürüm", fresh.Fields, "game", `"Okey"`, "players", `4`)
	// The new version allows online events only.
	inPersonAgain := gaming()
	inPersonAgain.Fields = values("game", `"Okey"`, "players", `4`)
	_, err = e.svc.Create(ctx, admin, tf, inPersonAgain)
	wantField(t, "türün izin vermediği biçim", err, "mode")

	// ... and cannot be moved to a mode its version does not allow.
	_, err = e.svc.Update(ctx, admin, tf, fresh.ID, []string{FieldLocation}, Input{Location: inPerson()})
	wantField(t, "türün izin vermediği biçime geçiş", err, "mode")

	// The old event stays at version 2 and is edited against version 2.
	got, err := e.svc.Get(ctx, admin, tf, old.ID)
	must(t, err)
	if got.TypeVersion != 2 {
		t.Fatalf("eski etkinliğin sürümü = %d", got.TypeVersion)
	}
	wantValues(t, "eski etkinlik", got.Fields, "game", `"Tavla"`, "platform", `"mobile"`)
	v, err := e.svc.Update(ctx, admin, tf, old.ID, []string{FieldFields}, Input{Fields: values("game", `"Tavla"`, "platform", `"pc"`)})
	must(t, err)
	wantValues(t, "eski sürümle düzenleme", v.Fields, "game", `"Tavla"`, "platform", `"pc"`)
	_, err = e.svc.Update(ctx, admin, tf, old.ID, []string{FieldFields}, Input{Fields: values("game", `"Tavla"`, "players", `4`)})
	wantField(t, "eski etkinlikte yeni sürümün alanı", err, "fields.platform")

	// A retired type takes no new events; its events live on.
	e.exec(t, `UPDATE catalog_event_types SET status = 'retired' WHERE key = 'gaming'`)
	in.ClientRequestID = id.New().String()
	_, err = e.svc.Create(ctx, admin, tf, in)
	wantField(t, "emekli tür", err, "type")
	if _, err := e.svc.Get(ctx, admin, tf, fresh.ID); err != nil {
		t.Fatalf("emekli türün etkinliği: %v", err)
	}
}

// A series' occurrences get the template's type version and field values;
// a change to the series reaches all of them.
func TestSeriesCopiesFields(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := weekly(gaming(), "FREQ=WEEKLY")
	first := e.create(t, tf, admin, in)
	evs := e.seriesEvents(t, first.SeriesID)
	if len(evs) < 3 {
		t.Fatalf("seri örnekleri = %d", len(evs))
	}
	for _, r := range evs {
		got, err := e.svc.Get(ctx, admin, tf, id.ID(r.ID.Bytes))
		must(t, err)
		if got.TypeVersion != 2 {
			t.Errorf("örneğin sürümü = %d", got.TypeVersion)
		}
		wantValues(t, "seri örneği", got.Fields, "game", `"Tavla"`, "platform", `"mobile"`)
	}

	// One event alone ...
	alone := id.ID(evs[1].ID.Bytes)
	_, err := e.svc.Update(ctx, admin, tf, alone, []string{FieldFields}, Input{Fields: values("game", `"Pişti"`, "platform", `"mobile"`)})
	must(t, err)
	// ... then the whole series: the event edited alone keeps its own.
	_, err = e.svc.Update(ctx, admin, tf, first.ID, []string{FieldFields}, Input{Series: true, Fields: values("game", `"Okey"`, "platform", `"pc"`)})
	must(t, err)
	for _, r := range e.seriesEvents(t, first.SeriesID) {
		got, err := e.svc.Get(ctx, admin, tf, id.ID(r.ID.Bytes))
		must(t, err)
		if id.ID(r.ID.Bytes) == alone {
			wantValues(t, "tek başına düzenlenen", got.Fields, "game", `"Pişti"`, "platform", `"mobile"`)
			continue
		}
		wantValues(t, "seri güncellemesi", got.Fields, "game", `"Okey"`, "platform", `"pc"`)
	}
}

// The database refuses a value for a field the event's type version does
// not define, whatever the application let through.
func TestFieldValueGuard(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, gaming())
	gen := e.create(t, tf, admin, input())
	for name, tc := range map[string]struct {
		event id.ID
		key   string
		value string
	}{
		"türde olmayan alan":      {ev.ID, "stake", `100`},
		"alansız türde alan":      {gen.ID, "game", `"x"`},
		"nesne değer":             {ev.ID, "game", `{"a": 1}`},
		"null değer":              {ev.ID, "game", `null`},
		"bozuk anahtar":           {ev.ID, "Game", `"x"`},
		"olmayan etkinliğe değer": {id.New(), "game", `"x"`},
	} {
		_, err := e.pool.Exec(ctx, `INSERT INTO event_field_values (event_id, field_key, value) VALUES ($1, $2, $3::jsonb)
			ON CONFLICT (event_id, field_key) DO UPDATE SET value = EXCLUDED.value`, tc.event.PG(), tc.key, tc.value)
		if err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	// The key of a value cannot be changed (column privileges).
	if _, err := e.pool.Exec(ctx, `UPDATE event_field_values SET field_key = 'platform' WHERE event_id = $1 AND field_key = 'game'`, ev.ID.PG()); err == nil {
		t.Error("alan anahtarı değiştirildi")
	}
	// The refused writes left the event's values as they were.
	if n := e.count(t, `SELECT count(*) FROM event_field_values WHERE event_id = $1`, ev.ID.PG()); n != 2 {
		t.Fatalf("alan değerleri = %d", n)
	}
}

// The study tag fields of the Eğitim types (issue #333; ADR-0043): a tag
// of the list at a depth the field allows, selectable when chosen; a
// retired tag the event already holds stays.
func TestStudyTagFields(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	typed := func(typ string, fields map[string]json.RawMessage) Input {
		in := input()
		in.Type = typ
		in.Fields = fields
		in.ClientRequestID = id.New().String()
		return in
	}

	// Peer teaching: one subject or topic, required.
	teach := e.create(t, tf, admin, typed("peer_teaching", values("subject", `["ayt_phy_vectors"]`)))
	if teach.TypeVersion != 2 {
		t.Fatalf("akran konu anlatımının sürümü = %d", teach.TypeVersion)
	}
	wantValues(t, "konu", teach.Fields, "subject", `["ayt_phy_vectors"]`)
	// A study group: up to five, in the order given.
	group := e.create(t, tf, admin, typed("study_group", values("subjects", `["tyt_math","ayt_physics_other","hs_math"]`)))
	wantValues(t, "dersler", group.Fields, "subjects", `["tyt_math","ayt_physics_other","hs_math"]`)
	// Optional: a problem session without subjects.
	if ps := e.create(t, tf, admin, typed("problem_session", nil)); len(ps.Fields) != 0 {
		t.Fatalf("soru çözüm oturumunun alanları = %v", ps.Fields)
	}
	// A mock exam: a level or a subject.
	for _, exam := range []string{`["tyt"]`, `["ayt_math"]`, `["other_exams_other"]`} {
		got := e.create(t, tf, admin, typed("mock_exam", values("exam", exam)))
		wantValues(t, "deneme "+exam, got.Fields, "exam", exam)
	}

	for name, tc := range map[string]struct {
		typ    string
		fields map[string]json.RawMessage
		field  string
	}{
		"zorunlu alan yok":       {"peer_teaching", nil, "fields.subject"},
		"düzey ders alanında":    {"peer_teaching", values("subject", `["ayt"]`), "fields.subject"},
		"tek seçimde iki":        {"peer_teaching", values("subject", `["ayt_math","ayt_physics"]`), "fields.subject"},
		"bilinmeyen etiket":      {"peer_teaching", values("subject", `["ayt_astrology"]`), "fields.subject"},
		"liste değil":            {"peer_teaching", values("subject", `"ayt_math"`), "fields.subject"},
		"boş liste":              {"peer_teaching", values("subject", `[]`), "fields.subject"},
		"bozuk anahtar":          {"peer_teaching", values("subject", `["AYT"]`), "fields.subject"},
		"tekrar":                 {"study_group", values("subjects", `["tyt_math","tyt_math"]`), "fields.subjects"},
		"altıdan fazla":          {"study_group", values("subjects", `["tyt_math","tyt_physics","tyt_chemistry","tyt_biology","tyt_history","tyt_geography"]`), "fields.subjects"},
		"konu sınav alanında":    {"mock_exam", values("exam", `["ayt_phy_vectors"]`), "fields.exam"},
		"Diğer konu sınav alanı": {"mock_exam", values("exam", `["ayt_physics_other"]`), "fields.exam"},
	} {
		_, err := e.svc.Create(ctx, admin, tf, typed(tc.typ, tc.fields))
		wantField(t, name, err, tc.field)
	}

	// The topic is retired: no new event chooses it ...
	e.exec(t, `UPDATE catalog_study_tags SET status = 'retired' WHERE key = 'ayt_phy_vectors'`)
	_, err := e.svc.Create(ctx, admin, tf, typed("peer_teaching", values("subject", `["ayt_phy_vectors"]`)))
	wantField(t, "emekli konu", err, "fields.subject")
	// ... nor a topic under a retired subject ...
	e.exec(t, `UPDATE catalog_study_tags SET status = 'retired' WHERE key = 'ayt_math'`)
	_, err = e.svc.Create(ctx, admin, tf, typed("study_group", values("subjects", `["ayt_math_other"]`)))
	wantField(t, "emekli dersin konusu", err, "fields.subjects")
	// ... but the event that holds it keeps it through an edit.
	v, err := e.svc.Update(ctx, admin, tf, teach.ID, []string{FieldFields}, Input{Fields: values("subject", `["ayt_phy_vectors"]`)})
	must(t, err)
	wantValues(t, "emekli konuyu tutan düzenleme", v.Fields, "subject", `["ayt_phy_vectors"]`)
	// Another field's retired tag is not kept: the study group never held it.
	_, err = e.svc.Update(ctx, admin, tf, group.ID, []string{FieldFields}, Input{Fields: values("subjects", `["tyt_math","ayt_phy_vectors"]`)})
	wantField(t, "başka etkinliğin emekli konusu", err, "fields.subjects")
	// Once changed, the retired topic cannot come back.
	_, err = e.svc.Update(ctx, admin, tf, teach.ID, []string{FieldFields}, Input{Fields: values("subject", `["ayt_physics"]`)})
	must(t, err)
	_, err = e.svc.Update(ctx, admin, tf, teach.ID, []string{FieldFields}, Input{Fields: values("subject", `["ayt_phy_vectors"]`)})
	wantField(t, "geri eklenen emekli konu", err, "fields.subject")
	got, err := e.svc.Get(ctx, admin, tf, teach.ID)
	must(t, err)
	wantValues(t, "reddedilen düzenlemeden sonra", got.Fields, "subject", `["ayt_physics"]`)
}

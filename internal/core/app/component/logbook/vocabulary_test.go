package logbook_test

import (
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/logbook"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The fishing catch log's species field is bound to the tayfa vocabulary
// (fish_species; issue #244, ADR-0037 §4): a text joins the vocabulary or
// reuses a known value, an ID must be the tayfa's value of the namespace,
// the entry shows its value's merge target, and nothing is written outside
// the write window or for a field the type version does not bind.
func TestLogVocabulary(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.newEvent(t, tf, admin, event.TypeFishing)
	const key = "catches"
	angler := e.attended(t, tf, ev.ID, authz.Member)
	r := ref(angler, tf, ev.ID, key)
	add := func(in logbook.Input) (logbook.Entry, error) {
		if in.ClientRequestID == "" {
			in.ClientRequestID = id.New().String()
		}
		return e.logs.Add(ctx, r, in)
	}
	species := func(tayfa id.ID) int {
		return e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE tayfa_id = $1 AND namespace = 'fish_species'`, tayfa.PG())
	}

	// A new text joins the tayfa's vocabulary; field_values keeps no copy.
	first, err := add(logbook.Input{Values: values("species", `"  Kırlangıç   balığı "`, "count", `2`)})
	must(t, err)
	kirlangic := first.VocabularyIDs["species"]
	if string(first.Values["species"]) != `"Kırlangıç balığı"` || kirlangic == (id.ID{}) || string(first.Values["count"]) != `2` {
		t.Fatalf("ilk kayıt: %+v", first)
	}
	if species(tf) != 1 || e.count(t, `SELECT count(*) FROM event_log_entries WHERE field_values ? 'species'`) != 0 {
		t.Fatal("değer sözlüğe taşınmadı")
	}
	if e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE id = $1 AND created_by = $2`, kirlangic.PG(), angler.PG()) != 1 {
		t.Fatal("ekleyen yazılmadı")
	}
	// The same value written another way is the same value.
	again, err := add(logbook.Input{Values: values("species", `"KIRLANGIÇ BALIĞI"`)})
	must(t, err)
	if again.VocabularyIDs["species"] != kirlangic || string(again.Values["species"]) != `"Kırlangıç balığı"` {
		t.Fatalf("aynı değer: %+v", again)
	}
	// By its ID.
	withID, err := add(logbook.Input{VocabularyIDs: map[string]id.ID{"species": kirlangic}, Values: values("weight", `1.5`)})
	must(t, err)
	if withID.VocabularyIDs["species"] != kirlangic || string(withID.Values["species"]) != `"Kırlangıç balığı"` || species(tf) != 1 {
		t.Fatalf("kimlikle kayıt: %+v", withID)
	}

	// Another tayfa's value (IDOR), another namespace's, an unknown one; a
	// field given twice, a field not bound, a bad text, a missing field.
	tf2, admin2 := e.newTayfa(t)
	ev2 := e.newEvent(t, tf2, admin2, event.TypeFishing)
	other := e.attended(t, tf2, ev2.ID, authz.Member)
	theirs, err := e.logs.Add(ctx, ref(other, tf2, ev2.ID, key), logbook.Input{Values: values("species", `"Levrek"`), ClientRequestID: id.New().String()})
	must(t, err)
	book := id.New()
	e.exec(t, `INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized) VALUES ($1, $2, 'book', 'Levrek', 'levrek')`,
		book.PG(), tf.PG())
	for name, tc := range map[string]struct {
		in    logbook.Input
		field string
	}{
		"başka tayfanın değeri": {logbook.Input{VocabularyIDs: map[string]id.ID{"species": theirs.VocabularyIDs["species"]}}, "vocabulary_ids.species"},
		"başka ad alanı":        {logbook.Input{VocabularyIDs: map[string]id.ID{"species": book}}, "vocabulary_ids.species"},
		"bilinmeyen değer":      {logbook.Input{VocabularyIDs: map[string]id.ID{"species": id.New()}}, "vocabulary_ids.species"},
		"iki kez":               {logbook.Input{Values: values("species", `"Levrek"`), VocabularyIDs: map[string]id.ID{"species": kirlangic}}, "vocabulary_ids.species"},
		"bağlı olmayan alan":    {logbook.Input{Values: values("species", `"Levrek"`), VocabularyIDs: map[string]id.ID{"weight": kirlangic}}, "vocabulary_ids"},
		"uzun metin":            {logbook.Input{Values: values("species", `"`+strings.Repeat("a", 61)+`"`)}, "values.species"},
		"metin olmayan":         {logbook.Input{Values: values("species", `12`)}, "values.species"},
		"türsüz":                {logbook.Input{Values: values("count", `1`)}, "values.species"},
	} {
		_, err := add(tc.in)
		wantField(t, name, err, tc.field)
	}
	if species(tf) != 1 || e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE tayfa_id = $1 AND value = 'Levrek' AND namespace = 'fish_species'`, tf.PG()) != 0 {
		t.Fatal("reddedilen kayıt sözlüğe yazdı")
	}

	// A repeat returns the vocabulary field too.
	rep := logbook.Input{Values: values("species", `"Lüfer"`), ClientRequestID: id.New().String()}
	a1, err := e.logs.Add(ctx, r, rep)
	must(t, err)
	a2, err := e.logs.Add(ctx, r, rep)
	must(t, err)
	lufer := a1.VocabularyIDs["species"]
	if a2.ID != a1.ID || a2.VocabularyIDs["species"] != lufer || string(a2.Values["species"]) != `"Lüfer"` {
		t.Fatalf("tekrar gönderim: %+v", a2)
	}

	// A merge shows the target; the entry keeps the value it chose.
	e.exec(t, `UPDATE tayfa_vocabulary SET merged_into = $2 WHERE id = $1`, lufer.PG(), kirlangic.PG())
	page, err := e.logs.List(ctx, r, 0, "")
	must(t, err)
	got := byID(page)[a1.ID]
	if got.VocabularyIDs["species"] != kirlangic || string(got.Values["species"]) != `"Kırlangıç balığı"` {
		t.Fatalf("birleştirilmiş değer: %+v", got)
	}
	if e.count(t, `SELECT count(*) FROM event_log_entry_vocabulary WHERE entry_id = $1 AND vocabulary_id = $2`, a1.ID.PG(), lufer.PG()) != 1 {
		t.Fatal("kayıt seçtiği değeri kaybetti")
	}

	// A log whose type version binds no vocabulary takes no ID, and its
	// texts stay in the entry.
	books := e.newEvent(t, tf, admin, booksType)
	reader := e.attended(t, tf, books.ID, authz.Member)
	_, err = e.logs.Add(ctx, ref(reader, tf, books.ID, "books"), logbook.Input{
		Values: values("book", `"Tutunamayanlar"`), VocabularyIDs: map[string]id.ID{"book": book}, ClientRequestID: id.New().String(),
	})
	wantField(t, "sözlüksüz günlük", err, "vocabulary_ids")
	plain, err := e.logs.Add(ctx, ref(reader, tf, books.ID, "books"), logbook.Input{
		Values: values("book", `"Tutunamayanlar"`), ClientRequestID: id.New().String(),
	})
	must(t, err)
	if plain.VocabularyIDs != nil || string(plain.Values["book"]) != `"Tutunamayanlar"` ||
		e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE namespace = 'book'`) != 1 {
		t.Fatalf("sözlüksüz kayıt: %+v", plain)
	}

	// Outside the write window nothing joins the vocabulary.
	e.clock.set(e.clock.now().Add(9 * 24 * time.Hour))
	_, err = add(logbook.Input{Values: values("species", `"Palamut"`)})
	wantErr(t, "donmuş günlüğe yeni değer", err, apperr.ErrComponentFrozen)
	_, err = add(logbook.Input{VocabularyIDs: map[string]id.ID{"species": kirlangic}})
	wantErr(t, "donmuş günlüğe değer", err, apperr.ErrComponentFrozen)
	if e.count(t, `SELECT count(*) FROM tayfa_vocabulary WHERE value = 'Palamut'`) != 0 {
		t.Fatal("donmuş günlük sözlüğe yazdı")
	}
}

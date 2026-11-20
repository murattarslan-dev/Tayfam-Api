package stats

import (
	"encoding/json"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// computeType has a total measure, a personal one the organizer enters, a
// sensitive one, an optional measure that is off by default, and a log
// with a vocabulary field, a free text field and a number.
const computeType = `{"components":[
 {"kind":"measure","key":"distance","labels":{"tr":"Mesafe"},"params":{"unit":"km","scope":"total","entered_by":"organizer","decimals":1}},
 {"kind":"measure","key":"goals","labels":{"tr":"Gol"},"params":{"unit":"goal","scope":"personal","entered_by":"organizer","decimals":0}},
 {"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","min":-40,"max":120,"decimals":2,"sensitive":true,"showcase":false}},
 {"kind":"measure","key":"bags","labels":{"tr":"Torba"},"optional":{"default":false},"params":{"unit":"bag","scope":"total","entered_by":"organizer"}},
 {"kind":"log","key":"catches","labels":{"tr":"Av"},"params":{"fields":[
  {"key":"species","kind":"text","required":true,"max_length":60,"vocabulary":{"namespace":"fish_species"},"labels":{"tr":"Tür"}},
  {"key":"bait","kind":"text","max_length":60,"labels":{"tr":"Yem"}},
  {"key":"weight","kind":"decimal","min":0.01,"max":1000,"decimals":2,"unit":"kg","labels":{"tr":"Ağırlık"}}],
  "stat":["distinct:species","distinct:bait","sum:weight"]}}]}`

// sensitiveType is a type whose attendance is sensitive (blood drive).
const sensitiveType = `{"flags":{"sensitive_attendance":true},"components":[
 {"kind":"measure","key":"units","labels":{"tr":"Ünite"},"params":{"unit":"piece","scope":"total","entered_by":"organizer"}}]}`

func parse(t *testing.T, s string) *definition.Definition {
	t.Helper()
	d, err := definition.Parse([]byte(s))
	if err != nil {
		t.Fatalf("tanım: %v", err)
	}
	return d
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// value finds a row's value; ok is false when there is no row.
func value(rows []Row, user id.ID, year int32, metric string) (float64, int32, bool) {
	for _, r := range rows {
		if r.User == user && r.Year == year && r.Metric == metric {
			return r.Value, r.Events, true
		}
	}
	return 0, 0, false
}

func TestCompute(t *testing.T) {
	def, blood := parse(t, computeType), parse(t, sensitiveType)
	a, b, c := id.New(), id.New(), id.New()
	e1, e2, e3 := id.New(), id.New(), id.New()
	v1, v2 := id.New(), id.New()
	in := Input{
		Events: []Event{
			{ID: e1, Type: "t", Year: 2026, Minutes: 120, Def: def, Attended: []id.ID{a, b}},
			{ID: e2, Type: "t", Year: 2027, Minutes: 60, Def: def, Attended: []id.ID{a}, Choices: map[string]bool{"bags": true}},
			{ID: e3, Type: "blood", Year: 2026, Minutes: 90, Def: blood, Sensitive: true, Attended: []id.ID{c}},
		},
		Measurements: []Measurement{
			{Event: e1, Key: "distance", Value: 12.5},
			{Event: e1, Key: "goals", User: a, Value: 2},
			{Event: e1, Key: "goals", User: b, Value: 1},
			{Event: e1, Key: "net", User: a, Value: 50},      // sensitive: nowhere
			{Event: e1, Key: "bags", Value: 3},               // switched off: nowhere
			{Event: e1, Key: "distance", User: a, Value: 99}, // a personal row of a total measure
			{Event: e1, Key: "unknown", Value: 7},            // not in the definition
			{Event: e2, Key: "bags", Value: 4},               // switched on
			{Event: e2, Key: "distance", Value: 3},           //
			{Event: e3, Key: "units", Value: 5},              // a total of a sensitive event
			{Event: id.New(), Key: "distance", Value: 1000},  // not a frozen event of the tayfa
		},
		Logs: []LogEntry{
			{Event: e1, Key: "catches", User: a, Values: map[string]json.RawMessage{"bait": raw(t, "Solucan"), "weight": raw(t, 1.5)}, Vocabulary: map[string]id.ID{"species": v1}},
			{Event: e1, Key: "catches", User: a, Values: map[string]json.RawMessage{"bait": raw(t, "  SOLUCAN "), "weight": raw(t, 2)}, Vocabulary: map[string]id.ID{"species": v2}},
			{Event: e1, Key: "catches", User: b, Values: map[string]json.RawMessage{"weight": raw(t, 0.25)}, Vocabulary: map[string]id.ID{"species": v1}},
		},
	}
	res := Compute(in)
	var tf id.ID
	for _, tc := range []struct {
		year   int32
		metric string
		want   float64
		events int32
	}{
		{2026, "events", 2, 2},
		{2026, "attended", 3, 2},
		{2026, "participants", 2, 1},
		{2026, "duration", 120, 1},
		{2026, "t.events", 1, 1},
		{2026, "t.attended", 2, 1},
		{2026, "blood.events", 1, 1},
		{2026, "blood.attended", 1, 1},
		{2026, "t.distance", 12.5, 1},
		{2026, "t.goals", 3, 1},
		{2026, "t.catches.count", 3, 1},
		{2026, "t.catches.distinct_species", 2, 1},
		{2026, "t.catches.distinct_bait", 1, 1},
		{2026, "t.catches.sum_weight", 3.75, 1},
		{2026, "blood.units", 5, 1},
		{2027, "t.bags", 4, 1},
		{AllTime, "events", 3, 3},
		{AllTime, "attended", 4, 3},
		{AllTime, "participants", 2, 2},
		{AllTime, "duration", 180, 2},
		{AllTime, "t.distance", 15.5, 2},
		{AllTime, "t.bags", 4, 1},
	} {
		got, events, ok := value(res.Tayfa, tf, tc.year, tc.metric)
		if !ok || got != tc.want || events != tc.events {
			t.Errorf("tayfa %d %s = %v (%d etkinlik, %v), %v (%d) bekleniyordu", tc.year, tc.metric, got, events, ok, tc.want, tc.events)
		}
	}
	// Nothing sensitive, switched off or unknown.
	for _, m := range []string{"t.net", "blood.participants", "blood.duration", "t.unknown"} {
		if _, _, ok := value(res.Tayfa, tf, AllTime, m); ok {
			t.Errorf("tayfada %s var", m)
		}
	}
	if _, _, ok := value(res.Tayfa, tf, 2026, "t.bags"); ok {
		t.Error("kapalı bileşen sayıldı")
	}

	for _, tc := range []struct {
		user   id.ID
		year   int32
		metric string
		want   float64
	}{
		{a, 2026, "attended", 1},
		{a, 2026, "duration", 120},
		{a, 2026, "t.attended", 1},
		{a, 2026, "t.goals", 2},
		{a, 2026, "t.catches.count", 2},
		{a, 2026, "t.catches.distinct_species", 2},
		{a, 2026, "t.catches.distinct_bait", 1},
		{a, 2026, "t.catches.sum_weight", 3.5},
		{a, AllTime, "attended", 2},
		{a, AllTime, "duration", 180},
		{b, 2026, "t.goals", 1},
		{b, 2026, "t.catches.distinct_species", 1},
		{b, 2026, "t.catches.sum_weight", 0.25},
	} {
		if got, _, ok := value(res.Users, tc.user, tc.year, tc.metric); !ok || got != tc.want {
			t.Errorf("kişi %d %s = %v (%v), %v bekleniyordu", tc.year, tc.metric, got, ok, tc.want)
		}
	}
	for _, r := range res.Users {
		switch {
		case r.User == c:
			t.Errorf("hassas katılımın kişisel satırı: %+v", r)
		case r.User == tf:
			t.Errorf("kişisiz satır: %+v", r)
		case r.Metric == "t.net" || r.Metric == "t.distance":
			t.Errorf("hassas ya da toplam ölçüm kişiye yazıldı: %+v", r)
		}
	}
	if _, _, ok := value(res.Users, b, 2026, "t.catches.distinct_bait"); ok {
		t.Error("değeri olmayan alan kişiye yazıldı")
	}

	// Badge totals leave the sensitive event out and alias the log.
	all := res.Badge[AllTime]
	if all["events"] != 2 || all["attended"] != 3 || all["blood.events"] != 0 || all["blood.units"] != 0 || all["t.catches"] != 3 {
		t.Errorf("rozet toplamları: %v", all)
	}
	if res.Badge[2026]["events"] != 1 || res.Badge[2027]["t.bags"] != 4 {
		t.Errorf("yıllık rozet toplamları: %v", res.Badge)
	}

	// The same input gives the same rows.
	again := Compute(in)
	if len(again.Tayfa) != len(res.Tayfa) || len(again.Users) != len(res.Users) {
		t.Fatal("hesap tekrarında farklı satırlar")
	}
	for i := range res.Tayfa {
		if again.Tayfa[i] != res.Tayfa[i] {
			t.Fatalf("hesap tekrarında farklı: %+v %+v", again.Tayfa[i], res.Tayfa[i])
		}
	}
}

// An empty tayfa has no rows; an event without a definition (an unknown
// type version) still counts in the common metrics.
func TestComputeEmptyAndUnknownType(t *testing.T) {
	if res := Compute(Input{}); len(res.Tayfa) != 0 || len(res.Users) != 0 || len(res.Badge) != 0 {
		t.Fatalf("boş: %+v", res)
	}
	u := id.New()
	res := Compute(Input{Events: []Event{{ID: id.New(), Type: "old", Year: 2025, Minutes: 30, Attended: []id.ID{u}}}})
	if v, _, ok := value(res.Tayfa, id.ID{}, AllTime, "old.duration"); !ok || v != 30 {
		t.Fatalf("tanımsız tür: %+v", res.Tayfa)
	}
	if v, _, ok := value(res.Users, u, 2025, "attended"); !ok || v != 1 {
		t.Fatalf("tanımsız türde katılım: %+v", res.Users)
	}
}

func TestTotalsAliasAndValidYear(t *testing.T) {
	tot := Totals([]Row{
		{Year: 2026, Metric: "t.catches.count", Value: 4},
		{Year: 2026, Metric: "t.catches.sum_weight", Value: 2},
		{Year: 2026, Metric: "count", Value: 9},
	})
	if tot[2026]["t.catches"] != 4 || tot[2026]["t.catches.sum_weight"] != 2 || len(tot[2026]) != 4 {
		t.Fatalf("toplamlar: %v", tot)
	}
	for y, want := range map[int32]bool{0: true, 2000: true, 2999: true, 1999: false, 3000: false, -1: false} {
		if ValidYear(y) != want {
			t.Errorf("ValidYear(%d) = %v", y, !want)
		}
	}
}

package home

import (
	"slices"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
)

// The widget list: valid, distinct keys; the sections first, in today's
// order; only display widgets need settings.
func TestWidgets(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range Widgets() {
		if !definition.ValidKey(w.Key) || seen[w.Key] {
			t.Errorf("anahtar: %+v", w)
		}
		seen[w.Key] = true
		if !slices.Contains([]Kind{KindSection, KindDisplay, KindModule}, w.Kind) {
			t.Errorf("tür: %+v", w)
		}
		if w.NeedsConfig && w.Kind != KindDisplay {
			t.Errorf("ayar isteyen: %+v", w)
		}
		if got, ok := Lookup(w.Key); !ok || got != w {
			t.Errorf("Lookup(%q) = %+v", w.Key, got)
		}
	}
	if !slices.Equal(Sections(), []string{KeyUpcoming, KeyAnnouncements, KeyPolls, KeyQuestions}) {
		t.Fatalf("bölümler = %v", Sections())
	}
	if !slices.Equal(Widgets()[:4], []Widget{
		{Key: KeyUpcoming, Kind: KindSection, Module: ModuleEvents},
		{Key: KeyAnnouncements, Kind: KindSection, Module: ModuleAnnouncements},
		{Key: KeyPolls, Kind: KindSection, Module: ModulePolls},
		{Key: KeyQuestions, Kind: KindSection, Module: ModuleQA},
	}) {
		t.Fatalf("bölümler önce gelmeli: %+v", Widgets()[:4])
	}
	if _, ok := Lookup("weather"); ok {
		t.Fatal("bilinmeyen anahtar bulundu")
	}
}

// Stored settings are read back as written, and a stored row that no
// longer passes reads as none.
func TestConfigRoundTrip(t *testing.T) {
	for key, c := range map[string]Config{
		KeyStats:      {Stats: &StatsConfig{Metrics: []string{"events", "hike.distance"}, Period: PeriodAllTime}},
		KeyGoal:       {Goal: &GoalConfig{Metric: "trash_pickup.weight", Target: 250.5, Year: 2027}},
		KeyChampions:  {Champions: &ChampionsConfig{EventTypes: []string{"chess"}, Period: PeriodLast30Days}},
		KeyLogSummary: {LogSummary: &LogSummaryConfig{Metric: "birding.sightings.distinct_species", Period: PeriodThisYear}},
	} {
		w, _ := Lookup(key)
		raw, err := encodeConfig(key, c)
		if err != nil || raw == nil {
			t.Fatalf("%s: %v", key, err)
		}
		got, ok := decodeConfig(w, raw)
		if !ok || !configEqual(got, c) {
			t.Errorf("%s: %s → %+v", key, raw, got)
		}
	}
	w, _ := Lookup(KeyGoal)
	for _, raw := range []string{`{"metric_key":"events"}`, `{"metric_key":"events","target":5,"x":1}`, `[]`, `{"metric_key":"events","target":5}{}`} {
		if _, ok := decodeConfig(w, []byte(raw)); ok {
			t.Errorf("geçersiz kayıt okundu: %s", raw)
		}
	}
	if raw, err := encodeConfig(KeyUpcoming, Config{}); raw != nil || err != nil {
		t.Fatalf("ayarsız bölüm: %s %v", raw, err)
	}
}

func configEqual(a, b Config) bool {
	switch {
	case a.Stats != nil || b.Stats != nil:
		return a.Stats != nil && b.Stats != nil && slices.Equal(a.Stats.Metrics, b.Stats.Metrics) && a.Stats.Period == b.Stats.Period
	case a.Goal != nil || b.Goal != nil:
		return a.Goal != nil && b.Goal != nil && *a.Goal == *b.Goal
	case a.Champions != nil || b.Champions != nil:
		return a.Champions != nil && b.Champions != nil && slices.Equal(a.Champions.EventTypes, b.Champions.EventTypes) && a.Champions.Period == b.Champions.Period
	case a.LogSummary != nil || b.LogSummary != nil:
		return a.LogSummary != nil && b.LogSummary != nil && *a.LogSummary == *b.LogSummary
	}
	return true
}

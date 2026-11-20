package stats

import (
	"slices"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
)

func parseDef(t *testing.T, raw string) *definition.Definition {
	t.Helper()
	d, err := definition.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("tanım: %v", err)
	}
	return d
}

const (
	hikeV1 = `{"components":[` +
		`{"kind":"measure","key":"distance","labels":{"tr":"Mesafe"},"params":{"unit":"km","scope":"total","entered_by":"organizer","showcase":true}},` +
		`{"kind":"measure","key":"elevation","labels":{"tr":"İrtifa"},"params":{"unit":"m","scope":"total","entered_by":"organizer","showcase":true}},` +
		`{"kind":"measure","key":"secret","labels":{"tr":"Gizli"},"params":{"unit":"km","scope":"total","entered_by":"organizer"}},` +
		`{"kind":"measure","key":"net","labels":{"tr":"Net"},"params":{"unit":"net","scope":"personal","entered_by":"self","sensitive":true,"showcase":false}}]}`
	hikeV2 = `{"components":[` +
		`{"kind":"measure","key":"distance","labels":{"tr":"Mesafe"},"params":{"unit":"km","scope":"total","entered_by":"organizer","showcase":true}},` +
		`{"kind":"measure","key":"elevation","labels":{"tr":"İrtifa"},"params":{"unit":"m","scope":"total","entered_by":"organizer","showcase":false}}]}`
	bloodV1 = `{"components":[` +
		`{"kind":"measure","key":"units","labels":{"tr":"Ünite"},"params":{"unit":"piece","scope":"total","entered_by":"organizer"}}]}`
	bloodV2 = `{"flags":{"sensitive_attendance":true},"components":[` +
		`{"kind":"measure","key":"units","labels":{"tr":"Ünite"},"params":{"unit":"piece","scope":"total","entered_by":"organizer"}}]}`
)

// TestShowcaseStats (issue #267, ADR-0026 §3): the showcase keeps the
// overall common metrics, a type's common metrics and the measures that
// are showcase (and not sensitive) in every version; never a log metric,
// a measure some version hides, anything of a type whose attendance is
// sensitive in some version, or anything of an unknown type.
func TestShowcaseStats(t *testing.T) {
	policies := map[string]typePolicy{
		"hike":  policyOf([]*definition.Definition{parseDef(t, hikeV1), parseDef(t, hikeV2)}),
		"blood": policyOf([]*definition.Definition{parseDef(t, bloodV1), parseDef(t, bloodV2)}),
	}
	if !policies["blood"].sensitive || policies["hike"].sensitive {
		t.Fatalf("hassas bayrak: %+v", policies)
	}
	totals := []Stat{
		{Metric: "events", Source: SourceCommon, Value: 5},
		{Metric: "attended", Source: SourceCommon, Value: 20},
		{Metric: "participants", Source: SourceCommon, Value: 7},
		{Metric: "duration", Source: SourceCommon, Value: 600},
		{Metric: "hike.events", Source: SourceCommon, Value: 3},
		{Metric: "hike.participants", Source: SourceCommon, Value: 6},
		{Metric: "hike.distance", Source: SourceMeasure, Value: 42.5},
		{Metric: "hike.elevation", Source: SourceMeasure, Value: 900},
		{Metric: "hike.secret", Source: SourceMeasure, Value: 1},
		{Metric: "hike.net", Source: SourceMeasure, Value: 50},
		{Metric: "hike.walks.count", Source: SourceLog, Value: 4},
		{Metric: "hike.walks.distinct_trail", Source: SourceLog, Value: 2},
		{Metric: "blood.events", Source: SourceCommon, Value: 2},
		{Metric: "blood.attended", Source: SourceCommon, Value: 3},
		{Metric: "blood.units", Source: SourceMeasure, Value: 3},
		{Metric: "gone.events", Source: SourceCommon, Value: 1},
		{Metric: "unknown", Source: SourceCommon, Value: 1},
		{Metric: "events", Source: SourceMeasure, Value: 1},
	}
	var got []string
	for _, s := range showcaseStats(totals, policies) {
		got = append(got, s.Metric+"/"+s.Source)
	}
	want := []string{
		"events/common", "attended/common", "participants/common", "duration/common",
		"hike.events/common", "hike.participants/common", "hike.distance/measure",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("vitrin metrikleri:\n  %v\nbeklenen:\n  %v", got, want)
	}
	if out := showcaseStats(totals, nil); len(out) != 4 {
		t.Fatalf("türsüz vitrin yalnızca genel ortak metrikleri taşır: %v", out)
	}
}

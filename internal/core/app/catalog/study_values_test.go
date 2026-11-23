package catalog

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// fieldValues builds field values from key, JSON text pairs.
func fieldValues(kv ...string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

// Study tag field values (issue #333; ADR-0043 §2): every tag is in the
// list, at a depth the field allows and selectable; a retired tag the
// field already held may stay.
func TestCheckStudyValues(t *testing.T) {
	_, pool, admin := setup(t)
	exec(t, admin, testStudyTags)
	q := store.New(pool)
	d, err := definition.Parse([]byte(`{"fields":[
	  {"key":"subjects","kind":"study_tags","max_selected":3,"labels":{"tr":"Dersler"}},
	  {"key":"exam","kind":"study_tags","min_depth":"level","max_depth":"subject","labels":{"tr":"Sınav"}},
	  {"key":"notes","kind":"text","labels":{"tr":"Not"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string][2]map[string]json.RawMessage{
		"ders":                      {fieldValues("subjects", `["subject_a"]`), nil},
		"konu ve Diğer":             {fieldValues("subjects", `["topic_a","subject_a_other","level_a_other"]`), nil},
		"düzey":                     {fieldValues("exam", `["level_a"]`), nil},
		"sınavda ders":              {fieldValues("exam", `["subject_a"]`), nil},
		"ders/konu alanı yok":       {fieldValues("notes", `"x"`), nil},
		"değer yok":                 {fieldValues(), nil},
		"tutulan emekli konu":       {fieldValues("subjects", `["topic_old","subject_a"]`), fieldValues("subjects", `["topic_old"]`)},
		"tutulan emekli düzey altı": {fieldValues("subjects", `["subject_c"]`), fieldValues("subjects", `["subject_c","topic_a"]`)},
		"tutulan emekli düzey":      {fieldValues("exam", `["level_b"]`), fieldValues("exam", `["level_b"]`)},
	} {
		if err := CheckStudyValues(ctx, q, d, c[0], c[1]); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	for name, c := range map[string]struct {
		values, previous map[string]json.RawMessage
		field            string
	}{
		"düzey ders alanında":         {fieldValues("subjects", `["level_a"]`), nil, "subjects"},
		"konu sınav alanında":         {fieldValues("exam", `["topic_a"]`), nil, "exam"},
		"emekli konu":                 {fieldValues("subjects", `["topic_old"]`), nil, "subjects"},
		"emekli ders":                 {fieldValues("subjects", `["subject_old"]`), nil, "subjects"},
		"emekli dersin konusu":        {fieldValues("subjects", `["topic_b"]`), nil, "subjects"},
		"emekli düzeyin dersi":        {fieldValues("subjects", `["subject_c"]`), nil, "subjects"},
		"emekli düzey":                {fieldValues("exam", `["level_b"]`), nil, "exam"},
		"bilinmeyen etiket":           {fieldValues("subjects", `["subject_x"]`), nil, "subjects"},
		"liste değil":                 {fieldValues("subjects", `"subject_a"`), nil, "subjects"},
		"ikincisi emekli":             {fieldValues("subjects", `["subject_a","topic_old"]`), nil, "subjects"},
		"önceki değerde yok":          {fieldValues("subjects", `["topic_old"]`), fieldValues("subjects", `["topic_a"]`), "subjects"},
		"başka alanda tutulan emekli": {fieldValues("subjects", `["subject_old"]`), fieldValues("exam", `["subject_old"]`), "subjects"},
	} {
		var ve *definition.ValueError
		if err := CheckStudyValues(ctx, q, d, c.values, c.previous); !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
}

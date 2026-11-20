package definition

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const trLabel = `"labels":{"tr":"Ad"}`

// field wraps one field's JSON in a definition.
func fieldDef(body string) string { return `{"fields":[` + body + `]}` }

// comps wraps components in a definition.
func comps(body string) string { return `{"components":[` + body + `]}` }

// measure is a valid total measure with the key "distance".
// personalMeasure is a personal measure a ranking may be computed from.
const personalMeasure = `{"kind":"measure","key":"laps",` + trLabel + `,"params":{"unit":"piece","scope":"personal","entered_by":"organizer"}}`

const measure = `{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer"}}`

func TestParseADRExample(t *testing.T) {
	// ADR-0025 §3, with the labels the schema asks for.
	d, err := Parse([]byte(`{
	  "fields": [
	    {"key": "difficulty", "kind": "choice", "required": false,
	     "options": ["1", "2", "3", "4", "5"], "labels": {"tr": "Zorluk"}}
	  ],
	  "components": [
	    {"kind": "route"},
	    {"kind": "roll_call", "params": {"alarm": true}},
	    {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {
	      "unit": "km", "scope": "total", "entered_by": "organizer",
	      "min": 0, "max": 1000, "decimals": 2, "showcase": true}}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Fields) != 1 || len(d.Components) != 3 {
		t.Fatalf("tanım = %+v", d)
	}
	if c, ok := d.Component("roll_call"); !ok || !c.Params["alarm"].Bool() || c.Optional != nil {
		t.Fatalf("roll_call = %+v", c)
	}
	m, ok := d.Component("distance")
	if !ok || m.Kind != CompMeasure || m.Params["unit"].String() != "km" || m.Params["max"].Value != float64(1000) ||
		m.Params["decimals"].Value != int64(2) || m.Labels["tr"] != "Mesafe" {
		t.Fatalf("ölçüm = %+v", m)
	}
	if f, ok := d.Field("difficulty"); !ok || len(f.Options) != 5 || f.Required {
		t.Fatalf("alan = %+v", f)
	}
}

func TestParseEmptyAndShape(t *testing.T) {
	for _, ok := range []string{`{}`, `{"fields":[],"components":[]}`, `{"flags":{}}`, ` {"components":[{"kind":"draw"}]} `} {
		if _, err := Parse([]byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"boş":                 ``,
		"dizi":                `[]`,
		"null":                `null`,
		"metin":               `"x"`,
		"fazladan veri":       `{} {}`,
		"bozuk":               `{"fields":`,
		"bilinmeyen anahtar":  `{"script":"alert(1)"}`,
		"fields nesne":        `{"fields":{}}`,
		"components metin":    `{"components":"route"}`,
		"flags dizi":          `{"flags":[]}`,
		"bilinmeyen bayrak":   `{"flags":{"paid":true}}`,
		"bayrak metin":        `{"flags":{"album_notice":"yes"}}`,
		"geçersiz utf-8":      "{\"fields\":[],\"x\xff\":1}",
		"bileşen nesne değil": `{"components":["route"]}`,
		"alan nesne değil":    `{"fields":["difficulty"]}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

func TestParseSizeLimit(t *testing.T) {
	// Padding with spaces keeps the JSON valid; only the size decides.
	ok := []byte(`{}` + strings.Repeat(" ", MaxBytes-2))
	if _, err := Parse(ok); err != nil {
		t.Fatalf("tam sınırda: %v", err)
	}
	if _, err := Parse(append(ok, ' ')); err == nil {
		t.Fatal("16 KiB üstü kabul edildi")
	}
}

func TestParseFlags(t *testing.T) {
	d, err := Parse([]byte(`{"flags":{"sensitive_attendance":true,"album_notice":true,"personal_scope_forbidden":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Flags.SensitiveAttendance || !d.Flags.AlbumNotice || !d.Flags.PersonalScopeForbidden {
		t.Fatalf("bayraklar = %+v", d.Flags)
	}
}

func TestParseFieldKinds(t *testing.T) {
	type tc struct {
		name string
		body string
		ok   bool
	}
	cases := []tc{
		// text
		{"text", `{"key":"game","kind":"text",` + trLabel + `}`, true},
		{"text sınırlı", `{"key":"game","kind":"text","required":true,"max_length":60,` + trLabel + `}`, true},
		{"text sözlük", `{"key":"game","kind":"text","vocabulary":{"namespace":"games"},` + trLabel + `}`, true},
		{"text çok uzun", `{"key":"game","kind":"text","max_length":121,` + trLabel + `}`, false},
		{"text sıfır", `{"key":"game","kind":"text","max_length":0,` + trLabel + `}`, false},
		{"text seçenekli", `{"key":"game","kind":"text","options":["a"],` + trLabel + `}`, false},
		{"text sözlük fazla anahtar", `{"key":"game","kind":"text","vocabulary":{"namespace":"games","url":"x"},` + trLabel + `}`, false},
		{"text sözlük adı bozuk", `{"key":"game","kind":"text","vocabulary":{"namespace":"Games"},` + trLabel + `}`, false},
		// long_text
		{"long_text", `{"key":"notes","kind":"long_text","max_length":1000,` + trLabel + `}`, true},
		{"long_text çok uzun", `{"key":"notes","kind":"long_text","max_length":1001,` + trLabel + `}`, false},
		{"long_text sözlük", `{"key":"notes","kind":"long_text","vocabulary":{"namespace":"games"},` + trLabel + `}`, false},
		// integer
		{"integer", `{"key":"elevation","kind":"integer","min":0,"max":9000,"unit":"m",` + trLabel + `}`, true},
		{"integer sınırsız", `{"key":"elevation","kind":"integer",` + trLabel + `}`, true},
		{"integer kesirli sınır", `{"key":"elevation","kind":"integer","max":1.5,` + trLabel + `}`, false},
		{"integer ters aralık", `{"key":"elevation","kind":"integer","min":5,"max":1,` + trLabel + `}`, false},
		{"integer bilinmeyen birim", `{"key":"elevation","kind":"integer","unit":"try",` + trLabel + `}`, false},
		{"integer ondalık", `{"key":"elevation","kind":"integer","decimals":2,` + trLabel + `}`, false},
		// decimal
		{"decimal", `{"key":"distance","kind":"decimal","min":0,"max":1000,"decimals":2,"unit":"km",` + trLabel + `}`, true},
		{"decimal kesirli sınır", `{"key":"distance","kind":"decimal","min":0.5,"max":99.5,` + trLabel + `}`, true},
		{"decimal çok basamak", `{"key":"distance","kind":"decimal","decimals":4,` + trLabel + `}`, false},
		{"decimal sıfır basamak", `{"key":"distance","kind":"decimal","decimals":0,` + trLabel + `}`, false},
		{"decimal sınır metin", `{"key":"distance","kind":"decimal","max":"1000",` + trLabel + `}`, false},
		{"decimal dev sınır", `{"key":"distance","kind":"decimal","max":1e12,` + trLabel + `}`, false},
		// choice
		{"choice", `{"key":"platform","kind":"choice","options":["pc","other"],"option_labels":{"pc":{"tr":"PC"},"other":{"tr":"Diğer"}},` + trLabel + `}`, true},
		{"choice rakam", `{"key":"difficulty","kind":"choice","options":["1","2"],` + trLabel + `}`, true},
		{"choice seçeneksiz", `{"key":"platform","kind":"choice",` + trLabel + `}`, false},
		{"choice boş", `{"key":"platform","kind":"choice","options":[],` + trLabel + `}`, false},
		{"choice tekrar", `{"key":"platform","kind":"choice","options":["pc","pc"],` + trLabel + `}`, false},
		{"choice bozuk seçenek", `{"key":"platform","kind":"choice","options":["PC"],` + trLabel + `}`, false},
		{"choice sayı seçenek", `{"key":"platform","kind":"choice","options":[1],` + trLabel + `}`, false},
		{"choice yabancı etiket", `{"key":"platform","kind":"choice","options":["pc"],"option_labels":{"xbox":{"tr":"Xbox"}},` + trLabel + `}`, false},
		{"choice etiket tr yok", `{"key":"platform","kind":"choice","options":["pc"],"option_labels":{"pc":{"en":"PC"}},` + trLabel + `}`, false},
		{"choice max_selected", `{"key":"platform","kind":"choice","options":["pc"],"max_selected":1,` + trLabel + `}`, false},
		// multi_choice
		{"multi_choice", `{"key":"languages","kind":"multi_choice","options":["tr","en","de"],"max_selected":2,` + trLabel + `}`, true},
		{"multi_choice fazla seçim", `{"key":"languages","kind":"multi_choice","options":["tr","en"],"max_selected":3,` + trLabel + `}`, false},
		{"multi_choice seçeneksiz", `{"key":"languages","kind":"multi_choice",` + trLabel + `}`, false},
		// boolean
		{"boolean", `{"key":"has_water","kind":"boolean",` + trLabel + `}`, true},
		{"boolean sınırlı", `{"key":"has_water","kind":"boolean","min":0,` + trLabel + `}`, false},
		// date
		{"date", `{"key":"deadline","kind":"date",` + trLabel + `}`, true},
		{"date sınırlı", `{"key":"deadline","kind":"date","max":1,` + trLabel + `}`, false},
		// duration
		{"duration", `{"key":"planned","kind":"duration","min":5,"max":600,` + trLabel + `}`, true},
		{"duration sıfır", `{"key":"planned","kind":"duration","min":0,` + trLabel + `}`, false},
		{"duration birimli", `{"key":"planned","kind":"duration","unit":"hour",` + trLabel + `}`, false},
		// closed list (ADR-0025 §3): no money, URL, file, location, person
		{"para", `{"key":"stake","kind":"money",` + trLabel + `}`, false},
		{"url", `{"key":"link","kind":"url",` + trLabel + `}`, false},
		{"dosya", `{"key":"doc","kind":"file",` + trLabel + `}`, false},
		{"konum", `{"key":"spot","kind":"location",` + trLabel + `}`, false},
		{"kişi", `{"key":"owner","kind":"person",` + trLabel + `}`, false},
		// common rules
		{"tür yok", `{"key":"game",` + trLabel + `}`, false},
		{"anahtar yok", `{"kind":"text",` + trLabel + `}`, false},
		{"anahtar bozuk", `{"key":"Game","kind":"text",` + trLabel + `}`, false},
		{"anahtar tek harf", `{"key":"g","kind":"text",` + trLabel + `}`, false},
		{"etiket yok", `{"key":"game","kind":"text"}`, false},
		{"etiket tr yok", `{"key":"game","kind":"text","labels":{"en":"Game"}}`, false},
		{"etiket boş", `{"key":"game","kind":"text","labels":{"tr":""}}`, false},
		{"etiket boşluklu", `{"key":"game","kind":"text","labels":{"tr":" Oyun"}}`, false},
		{"etiket satır sonu", `{"key":"game","kind":"text","labels":{"tr":"Oy\nun"}}`, false},
		{"etiket yön kontrolü", `{"key":"game","kind":"text","labels":{"tr":"Oy\u202eun"}}`, false},
		{"etiket uzun", `{"key":"game","kind":"text","labels":{"tr":"` + strings.Repeat("a", 61) + `"}}`, false},
		{"etiket dil bozuk", `{"key":"game","kind":"text","labels":{"tr":"Oyun","TR":"Oyun"}}`, false},
		{"required metin", `{"key":"game","kind":"text","required":"yes",` + trLabel + `}`, false},
		{"bilinmeyen anahtar", `{"key":"game","kind":"text","pattern":".*",` + trLabel + `}`, false},
	}
	for _, c := range cases {
		_, err := Parse([]byte(fieldDef(c.body)))
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: kabul edildi", c.name)
		}
	}
	// Every field kind has a positive case above.
	for _, k := range FieldKinds {
		if !slices.ContainsFunc(cases, func(c tc) bool { return c.ok && strings.Contains(c.body, `"kind":"`+k+`"`) }) {
			t.Errorf("%s için olumlu örnek yok", k)
		}
	}
}

func TestParseFieldLimitsAndDuplicates(t *testing.T) {
	one := func(i int) string {
		return `{"key":"f` + strings.Repeat("x", i+1) + `","kind":"boolean",` + trLabel + `}`
	}
	var parts []string
	for i := range MaxFields {
		parts = append(parts, one(i))
	}
	if _, err := Parse([]byte(fieldDef(strings.Join(parts, ",")))); err != nil {
		t.Fatalf("%d alan: %v", MaxFields, err)
	}
	parts = append(parts, one(MaxFields))
	if _, err := Parse([]byte(fieldDef(strings.Join(parts, ",")))); err == nil {
		t.Fatalf("%d alan kabul edildi", MaxFields+1)
	}
	if _, err := Parse([]byte(fieldDef(one(1) + "," + one(1)))); err == nil {
		t.Fatal("tekrar eden alan anahtarı kabul edildi")
	}
}

func TestParseComponents(t *testing.T) {
	logFields := `"fields":[{"key":"species","kind":"text","vocabulary":{"namespace":"fish_species"},` + trLabel + `},{"key":"weight","kind":"decimal","unit":"kg",` + trLabel + `},{"key":"style","kind":"choice","options":["a","b"],` + trLabel + `},{"key":"seen","kind":"boolean",` + trLabel + `}]`
	type tc struct {
		kind string
		name string
		def  string
		ok   bool
	}
	cases := []tc{
		// route, meeting_point, draw: no parameters
		{"route", "yalın", comps(`{"kind":"route"}`), true},
		{"route", "boş params", comps(`{"kind":"route","params":{}}`), true},
		{"route", "isteğe bağlı", comps(`{"kind":"route","optional":{"default":false}}`), true},
		{"route", "parametreli", comps(`{"kind":"route","params":{"max_points":500}}`), false},
		{"route", "iki kez", comps(`{"kind":"route"},{"kind":"route","key":"second"}`), false},
		{"meeting_point", "yalın", comps(`{"kind":"meeting_point"}`), true},
		{"meeting_point", "parametreli", comps(`{"kind":"meeting_point","params":{"radius":300}}`), false},
		{"draw", "yalın", comps(`{"kind":"draw"}`), true},
		{"draw", "parametreli", comps(`{"kind":"draw","params":{"prize":100}}`), false},
		// schedule
		{"schedule", "yalın", comps(`{"kind":"schedule"}`), true},
		{"schedule", "sınırlı", comps(`{"kind":"schedule","params":{"max_items":30}}`), true},
		{"schedule", "sıfır", comps(`{"kind":"schedule","params":{"max_items":0}}`), false},
		{"schedule", "çok", comps(`{"kind":"schedule","params":{"max_items":101}}`), false},
		{"schedule", "kesirli", comps(`{"kind":"schedule","params":{"max_items":1.5}}`), false},
		// checklist
		{"checklist", "malzeme", comps(`{"kind":"checklist","key":"gear","params":{"label":"gear","assignment":true,"quantity":true}}`), true},
		{"checklist", "iki liste", comps(`{"kind":"checklist","key":"gear"},{"kind":"checklist","key":"duty","params":{"label":"duty"}}`), true},
		{"checklist", "anahtarsız iki liste", comps(`{"kind":"checklist"},{"kind":"checklist"}`), false},
		{"checklist", "bilinmeyen etiket", comps(`{"kind":"checklist","params":{"label":"money"}}`), false},
		{"checklist", "atama metin", comps(`{"kind":"checklist","params":{"assignment":"yes"}}`), false},
		// roll_call
		{"roll_call", "alarmlı", comps(`{"kind":"roll_call","params":{"alarm":true,"return_grace_minutes":30}}`), true},
		{"roll_call", "alarmsız", comps(`{"kind":"roll_call","params":{"alarm":false}}`), true},
		{"roll_call", "tolerans çok", comps(`{"kind":"roll_call","params":{"return_grace_minutes":1441}}`), false},
		{"roll_call", "alarm seçimli", comps(`{"kind":"roll_call","params":{"alarm":{"organizer_choice":[true,false],"default":true}}}`), false},
		// vehicles
		{"vehicles", "plaka ve koltuk", comps(`{"kind":"vehicles","params":{"plate":true,"seats":true}}`), true},
		{"vehicles", "masraf", comps(`{"kind":"vehicles","params":{"cost_share":true}}`), false},
		{"vehicles", "koltuk sayı", comps(`{"kind":"vehicles","params":{"seats":4}}`), false},
		// teams
		{"teams", "dengeli", comps(`{"kind":"teams","params":{"mode":"balanced","team_count":2}}`), true},
		{"teams", "ikili", comps(`{"kind":"teams","params":{"mode":"manual","team_size":2}}`), true},
		{"teams", "seçimli", comps(`{"kind":"teams","params":{"mode":{"organizer_choice":["manual","random"],"default":"manual"}}}`), true},
		{"teams", "bilinmeyen mod", comps(`{"kind":"teams","params":{"mode":"auction"}}`), false},
		{"teams", "tek takım", comps(`{"kind":"teams","params":{"team_count":1}}`), false},
		{"teams", "büyüklük seçimli", comps(`{"kind":"teams","params":{"team_size":{"organizer_choice":[2,3],"default":2}}}`), false},
		// bracket
		{"bracket", "eleme", comps(`{"kind":"bracket","params":{"format":"elimination","best_of":3}}`), true},
		{"bracket", "seçimli", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["elimination","league","swiss"],"default":"swiss"},"best_of":{"organizer_choice":[1,3,5],"default":1}}}`), true},
		{"bracket", "bilinmeyen biçim", comps(`{"kind":"bracket","params":{"format":"elo"}}`), false},
		{"bracket", "çift maç", comps(`{"kind":"bracket","params":{"best_of":2}}`), false},
		{"bracket", "tek seçenek", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["swiss"],"default":"swiss"}}}`), false},
		{"bracket", "varsayılan listede yok", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["league","swiss"],"default":"elimination"}}}`), false},
		{"bracket", "varsayılan yok", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["league","swiss"]}}}`), false},
		{"bracket", "seçenek tekrar", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["swiss","swiss"],"default":"swiss"}}}`), false},
		{"bracket", "seçimde fazla anahtar", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["league","swiss"],"default":"swiss","when":"x"}}}`), false},
		{"bracket", "seçimde bilinmeyen değer", comps(`{"kind":"bracket","params":{"format":{"organizer_choice":["league","elo"],"default":"league"}}}`), false},
		// scoreboard
		{"scoreboard", "masalı", comps(`{"kind":"scoreboard","params":{"side":"player","tables":true,"presets":["batak","okey_101","free"],"guests":true}}`), true},
		{"scoreboard", "takım tek maç", comps(`{"kind":"scoreboard","params":{"side":"team","rounds":"single","winner":"high","result":"points","target":21}}`), true},
		{"scoreboard", "wdl ve seçim", comps(`{"kind":"scoreboard","params":{"result":"wdl","best_of":{"organizer_choice":[1,3,5],"default":3}}}`), true},
		{"scoreboard", "bahis", comps(`{"kind":"scoreboard","params":{"stake":50}}`), false},
		{"scoreboard", "ön ayar tekrar", comps(`{"kind":"scoreboard","params":{"presets":["batak","batak"]}}`), false},
		{"scoreboard", "ön ayar boş", comps(`{"kind":"scoreboard","params":{"presets":[]}}`), false},
		{"scoreboard", "ön ayar bozuk", comps(`{"kind":"scoreboard","params":{"presets":["101 Okey"]}}`), false},
		{"scoreboard", "bilinmeyen sonuç", comps(`{"kind":"scoreboard","params":{"result":"elo"}}`), false},
		{"scoreboard", "hedef sıfır", comps(`{"kind":"scoreboard","params":{"target":0}}`), false},
		// ranking
		{"ranking", "elle", comps(`{"kind":"ranking","params":{"metric":"duration","direction":"asc"}}`), true},
		{"ranking", "ölçümden", comps(personalMeasure + `,{"kind":"ranking","params":{"source":"laps","direction":"desc"}}`), true},
		{"ranking", "kaynak yok", comps(`{"kind":"ranking","params":{"source":"distance"}}`), false},
		{"ranking", "kaynak ölçüm değil", comps(`{"kind":"route"},{"kind":"ranking","params":{"source":"route"}}`), false},
		{"ranking", "kaynak toplam ölçüm", comps(measure + `,{"kind":"ranking","params":{"source":"distance","direction":"desc"}}`), false},
		{"ranking", "kaynak ve ölçüt birlikte", comps(personalMeasure + `,{"kind":"ranking","params":{"source":"laps","metric":"rank"}}`), false},
		{"ranking", "ne kaynak ne ölçüt", comps(`{"kind":"ranking","params":{"direction":"asc"}}`), false},
		{"ranking", "bilinmeyen yön", comps(`{"kind":"ranking","params":{"metric":"rank","direction":"up"}}`), false},
		// vote
		{"vote", "serbest", comps(`{"kind":"vote","params":{"source":"free"}}`), true},
		{"vote", "araçlar", comps(`{"kind":"vehicles"},{"kind":"vote","params":{"source":"vehicles"}}`), true},
		{"vote", "duraklar", comps(`{"kind":"route"},{"kind":"vote","params":{"source":"route_stops"}}`), true},
		{"vote", "albüm ve katılımcı", comps(`{"kind":"vote","key":"photo","params":{"source":"album"}},{"kind":"vote","key":"mvp","params":{"source":"participants"}}`), true},
		{"vote", "kaynaksız", comps(`{"kind":"vote"}`), false},
		{"vote", "araçsız araç oyu", comps(`{"kind":"vote","params":{"source":"vehicles"}}`), false},
		{"vote", "rotasız durak oyu", comps(`{"kind":"vote","params":{"source":"route_stops"}}`), false},
		{"vote", "bilinmeyen kaynak", comps(`{"kind":"vote","params":{"source":"url"}}`), false},
		// celebration
		{"celebration", "yalın", comps(`{"kind":"celebration"}`), true},
		{"celebration", "yazbozdan", comps(`{"kind":"scoreboard"},{"kind":"celebration","params":{"source":"scoreboard"}}`), true},
		{"celebration", "kaynak yok", comps(`{"kind":"celebration","params":{"source":"scoreboard"}}`), false},
		{"celebration", "kaynak rota", comps(`{"kind":"route"},{"kind":"celebration","params":{"source":"route"}}`), false},
		// measure
		{"measure", "toplam", comps(measure), true},
		{"measure", "kişisel, kendi", comps(`{"kind":"measure","key":"pages",` + trLabel + `,"params":{"unit":"page","scope":"personal","entered_by":"self","showcase":true}}`), true},
		{"measure", "kişisel, organizatör (gol)", comps(`{"kind":"measure","key":"goals",` + trLabel + `,"params":{"unit":"goal","scope":"personal","entered_by":"organizer"}}`), true},
		{"measure", "hassas", comps(`{"kind":"measure","key":"net",` + trLabel + `,"params":{"unit":"net","scope":"personal","entered_by":"self","sensitive":true,"showcase":false,"min":-50,"max":200,"decimals":2}}`), true},
		{"measure", "iki ölçüm", comps(measure + `,{"kind":"measure","key":"elevation_gain",` + trLabel + `,"params":{"unit":"m","scope":"total","entered_by":"organizer"}}`), true},
		{"measure", "plandan", `{"fields":[{"key":"planned_distance","kind":"decimal","unit":"km",` + trLabel + `}],"components":[{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","prefill_from":"planned_distance"}}]}`, true},
		{"measure", "yeni birimler", comps(`{"kind":"measure","key":"nights",` + trLabel + `,"params":{"unit":"night","scope":"total","entered_by":"organizer"}},{"kind":"measure","key":"sea",` + trLabel + `,"params":{"unit":"nm","scope":"total","entered_by":"organizer"}},{"kind":"measure","key":"boxes",` + trLabel + `,"params":{"unit":"box","scope":"total","entered_by":"organizer"}},{"kind":"measure","key":"sections",` + trLabel + `,"params":{"unit":"section","scope":"total","entered_by":"organizer"}}`), true},
		{"measure", "anahtarsız", comps(`{"kind":"measure",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer"}}`), false},
		{"measure", "etiketsiz", comps(`{"kind":"measure","key":"distance","params":{"unit":"km","scope":"total","entered_by":"organizer"}}`), false},
		{"measure", "parametresiz", comps(`{"kind":"measure","key":"distance",` + trLabel + `}`), false},
		{"measure", "birimsiz", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"scope":"total","entered_by":"organizer"}}`), false},
		{"measure", "para birimi", comps(`{"kind":"measure","key":"pot",` + trLabel + `,"params":{"unit":"try","scope":"total","entered_by":"organizer"}}`), false},
		{"measure", "toplamı katılımcı girer", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"self"}}`), false},
		{"measure", "hassas toplam", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","sensitive":true}}`), false},
		{"measure", "hassas vitrinde", comps(`{"kind":"measure","key":"net",` + trLabel + `,"params":{"unit":"net","scope":"personal","entered_by":"self","sensitive":true,"showcase":true}}`), false},
		{"measure", "ters aralık", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","min":10,"max":1}}`), false},
		{"measure", "çok basamak", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","decimals":4}}`), false},
		{"measure", "aynı anahtar", comps(measure + `,` + measure), false},
		{"measure", "plan alanı yok", comps(`{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","prefill_from":"planned_distance"}}`), false},
		{"measure", "plan başka birimde", `{"fields":[{"key":"planned_distance","kind":"decimal","unit":"m",` + trLabel + `}],"components":[{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","prefill_from":"planned_distance"}}]}`, false},
		{"measure", "plan metin alanı", `{"fields":[{"key":"planned_distance","kind":"text",` + trLabel + `}],"components":[{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"km","scope":"total","entered_by":"organizer","prefill_from":"planned_distance"}}]}`, false},
		{"measure", "kişisel yasakken", `{"flags":{"personal_scope_forbidden":true},"components":[{"kind":"measure","key":"units",` + trLabel + `,"params":{"unit":"unit","scope":"personal","entered_by":"organizer"}}]}`, false},
		// goal
		{"goal", "ölçüme bağlı", comps(measure + `,{"kind":"goal","params":{"measure":"distance","default_target":100}}`), true},
		{"goal", "ölçümsüz", comps(`{"kind":"goal"}`), false},
		{"goal", "ölçüm yok", comps(`{"kind":"goal","params":{"measure":"distance"}}`), false},
		{"goal", "ölçüm değil", comps(`{"kind":"route"},{"kind":"goal","params":{"measure":"route"}}`), false},
		{"goal", "eksi hedef", comps(measure + `,{"kind":"goal","params":{"measure":"distance","default_target":-1}}`), false},
		// log
		{"log", "av günlüğü", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"location":true,"location_default":"self","stat":["count","sum:weight","distinct:species"],"max_entries":100}}`), true},
		{"log", "konumsuz", comps(`{"kind":"log","key":"read",` + trLabel + `,"params":{` + logFields + `,"stat":["distinct:style"]}}`), true},
		{"log", "alansız", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{"location":true}}`), false},
		{"log", "boş alan listesi", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{"fields":[]}}`), false},
		{"log", "anahtarsız", comps(`{"kind":"log",` + trLabel + `,"params":{` + logFields + `}}`), false},
		{"log", "etiketsiz", comps(`{"kind":"log","key":"catches","params":{` + logFields + `}}`), false},
		{"log", "dört istatistik", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["count","sum:weight","distinct:species","distinct:style"]}}`), false},
		{"log", "istatistik tekrar", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["count","count"]}}`), false},
		{"log", "bilinmeyen istatistik", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["avg:weight"]}}`), false},
		{"log", "olmayan alanın toplamı", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["sum:length"]}}`), false},
		{"log", "metnin toplamı", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["sum:species"]}}`), false},
		{"log", "sayının farklısı", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"stat":["distinct:weight"]}}`), false},
		{"log", "konum kapalıyken gizlilik", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"location_default":"tayfa"}}`), false},
		{"log", "bilinmeyen gizlilik", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{` + logFields + `,"location":true,"location_default":"public"}}`), false},
		{"log", "kayıt alanı tekrar", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{"fields":[{"key":"species","kind":"text",` + trLabel + `},{"key":"species","kind":"text",` + trLabel + `}]}}`), false},
		{"log", "kayıt alanı para", comps(`{"kind":"log","key":"catches",` + trLabel + `,"params":{"fields":[{"key":"price","kind":"money",` + trLabel + `}]}}`), false},
		{"log", "kişisel yasakken", `{"flags":{"personal_scope_forbidden":true},"components":[{"kind":"log","key":"donors",` + trLabel + `,"params":{` + logFields + `}}]}`, false},
		// questions
		{"questions", "anonim", comps(`{"kind":"questions","params":{"anonymous":true}}`), true},
		{"questions", "isteğe bağlı", comps(`{"kind":"questions","optional":{"default":false}}`), true},
		{"questions", "anonim metin", comps(`{"kind":"questions","params":{"anonymous":"yes"}}`), false},
		// timer
		{"timer", "geri sayım", comps(`{"kind":"timer","params":{"mode":"countdown","presets":[60,300,1500]}}`), true},
		{"timer", "seçimli", comps(`{"kind":"timer","params":{"mode":{"organizer_choice":["countdown","stopwatch"],"default":"countdown"}}}`), true},
		{"timer", "satranç saati", comps(`{"kind":"timer","params":{"mode":"chess_clock"}}`), true},
		{"timer", "yedi ön ayar", comps(`{"kind":"timer","params":{"presets":[1,2,3,4,5,6,7]}}`), false},
		{"timer", "ön ayar tekrar", comps(`{"kind":"timer","params":{"presets":[60,60]}}`), false},
		{"timer", "ön ayar sıfır", comps(`{"kind":"timer","params":{"presets":[0]}}`), false},
		{"timer", "bilinmeyen mod", comps(`{"kind":"timer","params":{"mode":"sync"}}`), false},
		// pool_link
		{"pool_link", "yalın", comps(`{"kind":"pool_link"}`), true},
		{"pool_link", "sınırlı", comps(`{"kind":"pool_link","params":{"max_links":50}}`), true},
		{"pool_link", "çok", comps(`{"kind":"pool_link","params":{"max_links":51}}`), false},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.def))
		if c.ok && err != nil {
			t.Errorf("%s/%s: %v", c.kind, c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s/%s: kabul edildi", c.kind, c.name)
		}
	}
	// Every component kind has a schema, a positive and a negative case.
	for _, k := range ComponentKinds {
		if _, ok := kinds[k]; !ok {
			t.Errorf("%s için parametre şeması yok", k)
		}
		if _, ok := MinClientVersions[k]; !ok {
			t.Errorf("%s için en düşük istemci sürümü yok", k)
		}
		for _, want := range []bool{true, false} {
			if !slices.ContainsFunc(cases, func(c tc) bool { return c.kind == k && c.ok == want }) {
				t.Errorf("%s için örnek yok (olumlu=%v)", k, want)
			}
		}
	}
	if len(kinds) != len(ComponentKinds) || len(MinClientVersions) != len(ComponentKinds) {
		t.Fatalf("bileşen listeleri uyuşmuyor: %d şema, %d sürüm, %d tür", len(kinds), len(MinClientVersions), len(ComponentKinds))
	}
}

func TestParseComponentCommonRules(t *testing.T) {
	for name, def := range map[string]string{
		"bilinmeyen tür":         comps(`{"kind":"webview"}`),
		"tür yok":                comps(`{"key":"route"}`),
		"bilinmeyen anahtar":     comps(`{"kind":"route","script":"x"}`),
		"anahtar bozuk":          comps(`{"kind":"checklist","key":"Gear"}`),
		"optional bool":          comps(`{"kind":"route","optional":true}`),
		"optional boş":           comps(`{"kind":"route","optional":{}}`),
		"optional fazla":         comps(`{"kind":"route","optional":{"default":true,"when":"open_water"}}`),
		"params dizi":            comps(`{"kind":"schedule","params":[]}`),
		"etiket tr yok":          comps(`{"kind":"route","labels":{"en":"Route"}}`),
		"anahtar başka türle":    comps(`{"kind":"route"},{"kind":"checklist","key":"route"}`),
		"değer yerine nesne":     comps(`{"kind":"schedule","params":{"max_items":{"value":3}}}`),
		"değer yerine boş liste": comps(`{"kind":"timer","params":{"presets":[]}}`),
	} {
		if _, err := Parse([]byte(def)); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	var parts []string
	for i := range MaxComponents + 1 {
		parts = append(parts, `{"kind":"checklist","key":"c`+strings.Repeat("x", i+1)+`"}`)
	}
	if _, err := Parse([]byte(comps(strings.Join(parts[:MaxComponents], ",")))); err != nil {
		t.Fatalf("%d bileşen: %v", MaxComponents, err)
	}
	if _, err := Parse([]byte(comps(strings.Join(parts, ",")))); err == nil {
		t.Fatalf("%d bileşen kabul edildi", MaxComponents+1)
	}
}

func TestParseOptionalAndChoice(t *testing.T) {
	d, err := Parse([]byte(comps(`{"kind":"roll_call","optional":{"default":true}},{"kind":"bracket","params":{"format":{"organizer_choice":["elimination","league","swiss"],"default":"swiss"}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := d.Component("roll_call")
	if rc.Optional == nil || !*rc.Optional {
		t.Fatalf("optional = %v", rc.Optional)
	}
	br, _ := d.Component("bracket")
	f := br.Params["format"]
	if f.String() != "swiss" || len(f.Choices) != 3 || f.Choices[0] != "elimination" {
		t.Fatalf("format = %+v", f)
	}
}

func TestParseErrorPath(t *testing.T) {
	_, err := Parse([]byte(comps(`{"kind":"route"},{"kind":"measure","key":"distance",` + trLabel + `,"params":{"unit":"furlong","scope":"total","entered_by":"organizer"}}`)))
	var de *Error
	if !errors.As(err, &de) || de.Path != "components[1].params.unit" {
		t.Fatalf("hata = %v", err)
	}
	// The rejected value is not echoed.
	if strings.Contains(err.Error(), "furlong") {
		t.Fatalf("hata değeri taşıyor: %v", err)
	}
	_, err = Parse([]byte(`{"components":[{"kind":"route","<script>":1}]}`))
	if err == nil || strings.Contains(err.Error(), "script") {
		t.Fatalf("bilinmeyen anahtar hatası: %v", err)
	}
}

func TestMinClientVersion(t *testing.T) {
	d, err := Parse([]byte(comps(`{"kind":"route"},{"kind":"timer"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.MinClientVersion(); got != "0.3.0" {
		t.Fatalf("sürüm = %q", got)
	}
	if got := (&Definition{}).MinClientVersion(); got != "0.3.0" {
		t.Fatalf("boş tanım = %q", got)
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.3.0", "0.3.1", true}, {"0.3.1", "0.3.0", false}, {"0.9.0", "0.10.0", true}, {"1.0.0", "0.10.0", false}, {"0.3.0", "0.3.0", false}} {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func valuesDef(t *testing.T) *Definition {
	t.Helper()
	d, err := Parse([]byte(`{"fields":[
	  {"key":"game","kind":"text","required":true,"max_length":10,"labels":{"tr":"Oyun"}},
	  {"key":"notes","kind":"long_text","max_length":20,"labels":{"tr":"Not"}},
	  {"key":"elevation","kind":"integer","min":0,"max":9000,"labels":{"tr":"İrtifa"}},
	  {"key":"distance","kind":"decimal","min":0,"max":1000,"decimals":2,"labels":{"tr":"Mesafe"}},
	  {"key":"platform","kind":"choice","options":["pc","xbox"],"labels":{"tr":"Platform"}},
	  {"key":"langs","kind":"multi_choice","options":["tr","en","de"],"max_selected":2,"labels":{"tr":"Diller"}},
	  {"key":"water","kind":"boolean","labels":{"tr":"Su"}},
	  {"key":"deadline","kind":"date","labels":{"tr":"Son gün"}},
	  {"key":"planned","kind":"duration","min":5,"max":600,"labels":{"tr":"Süre"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCleanValues(t *testing.T) {
	d := valuesDef(t)
	in := map[string]json.RawMessage{
		"game":      json.RawMessage(`"  Tavla "`),
		"notes":     json.RawMessage(`"satır 1\nsatır 2"`),
		"elevation": json.RawMessage(`1200`),
		"distance":  json.RawMessage(`12.345`),
		"platform":  json.RawMessage(`"xbox"`),
		"langs":     json.RawMessage(`["en","tr"]`),
		"water":     json.RawMessage(`false`),
		"deadline":  json.RawMessage(`"2026-12-31"`),
		"planned":   json.RawMessage(`90`),
	}
	out, err := d.CleanValues(in, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"game": `"Tavla"`, "notes": `"satır 1\nsatır 2"`, "elevation": `1200`, "distance": `12.35`,
		"platform": `"xbox"`, "langs": `["tr","en"]`, "water": `false`, "deadline": `"2026-12-31"`, "planned": `90`,
	}
	if len(out) != len(want) {
		t.Fatalf("çıktı = %v", out)
	}
	for k, w := range want {
		if string(out[k]) != w {
			t.Errorf("%s = %s, beklenen %s", k, out[k], w)
		}
	}
	// Only the required field is needed.
	if out, err := d.CleanValues(map[string]json.RawMessage{"game": json.RawMessage(`"Okey"`)}, true); err != nil || len(out) != 1 {
		t.Fatalf("yalnızca zorunlu: %v %v", out, err)
	}
	// A partial set (an update) does not ask for the required field.
	if _, err := d.CleanValues(map[string]json.RawMessage{"water": json.RawMessage(`true`)}, false); err != nil {
		t.Fatalf("kısmi: %v", err)
	}
}

func TestCleanValuesRefuses(t *testing.T) {
	d := valuesDef(t)
	game := json.RawMessage(`"Okey"`)
	for name, c := range map[string]struct {
		key, val, field string
	}{
		"zorunlu yok":          {"water", `true`, "game"},
		"bilinmeyen alan":      {"stake", `100`, ""},
		"metin sayı":           {"game", `5`, "game"},
		"metin boş":            {"game", `"   "`, "game"},
		"metin uzun":           {"game", `"01234567890"`, "game"},
		"metin satır sonu":     {"game", `"a\nb"`, "game"},
		"metin null":           {"game", `null`, "game"},
		"uzun metin kontrol":   {"notes", `"a\u0000b"`, "notes"},
		"uzun metin uzun":      {"notes", `"` + strings.Repeat("a", 21) + `"`, "notes"},
		"tam sayı kesirli":     {"elevation", `12.5`, "elevation"},
		"tam sayı üstel":       {"elevation", `1e3`, "elevation"},
		"tam sayı metin":       {"elevation", `"1200"`, "elevation"},
		"tam sayı aralık dışı": {"elevation", `9001`, "elevation"},
		"tam sayı eksi":        {"elevation", `-1`, "elevation"},
		"ondalık metin":        {"distance", `"12"`, "distance"},
		"ondalık aralık dışı":  {"distance", `1000.01`, "distance"},
		"ondalık dev":          {"distance", `1e400`, "distance"},
		"seçim yabancı":        {"platform", `"switch"`, "platform"},
		"seçim liste":          {"platform", `["pc"]`, "platform"},
		"çoklu seçim metin":    {"langs", `"tr"`, "langs"},
		"çoklu seçim boş":      {"langs", `[]`, "langs"},
		"çoklu seçim fazla":    {"langs", `["tr","en","de"]`, "langs"},
		"çoklu seçim tekrar":   {"langs", `["tr","tr"]`, "langs"},
		"çoklu seçim yabancı":  {"langs", `["fr"]`, "langs"},
		"çoklu seçim sayı":     {"langs", `[1]`, "langs"},
		"mantıksal metin":      {"water", `"true"`, "water"},
		"mantıksal sayı":       {"water", `1`, "water"},
		"tarih biçimi":         {"deadline", `"31.12.2026"`, "deadline"},
		"tarih yok":            {"deadline", `"2026-02-30"`, "deadline"},
		"tarih saatli":         {"deadline", `"2026-12-31T10:00:00Z"`, "deadline"},
		"tarih sayı":           {"deadline", `20261231`, "deadline"},
		"süre kısa":            {"planned", `4`, "planned"},
		"süre uzun":            {"planned", `601`, "planned"},
		"süre kesirli":         {"planned", `7.5`, "planned"},
		"bozuk json":           {"water", `tru`, "water"},
		"fazladan veri":        {"water", `true false`, "water"},
		"boş değer":            {"water", ``, "water"},
	} {
		in := map[string]json.RawMessage{"game": game}
		if name == "zorunlu yok" {
			delete(in, "game")
		}
		in[c.key] = json.RawMessage(c.val)
		_, err := d.CleanValues(in, true)
		var ve *ValueError
		if !errors.As(err, &ve) {
			t.Errorf("%s: hata = %v", name, err)
			continue
		}
		if ve.Field != c.field {
			t.Errorf("%s: alan = %q, beklenen %q", name, ve.Field, c.field)
		}
	}
	// More values than the type has fields.
	many := map[string]json.RawMessage{}
	for i := range len(d.Fields) + 1 {
		many["k"+strings.Repeat("x", i+1)] = json.RawMessage(`1`)
	}
	if _, err := d.CleanValues(many, false); err == nil {
		t.Fatal("alan sayısından çok değer kabul edildi")
	}
	// A value far larger than any field allows is refused unread.
	if _, err := d.CleanValues(map[string]json.RawMessage{"game": json.RawMessage(`"` + strings.Repeat("a", maxValueBytes) + `"`)}, false); err == nil {
		t.Fatal("dev değer kabul edildi")
	}
}

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"hike", "board_game_night", "a1", "x" + strings.Repeat("y", 39)} {
		if !ValidKey(ok) {
			t.Errorf("%q reddedildi", ok)
		}
	}
	for _, bad := range []string{"", "a", "Hike", "1hike", "hike-night", "hike night", "x" + strings.Repeat("y", 40), "hıke"} {
		if ValidKey(bad) {
			t.Errorf("%q kabul edildi", bad)
		}
	}
}

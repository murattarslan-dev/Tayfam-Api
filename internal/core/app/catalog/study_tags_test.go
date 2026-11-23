package catalog

import (
	"errors"
	"slices"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
)

// testStudyTags is the smallest list that exercises every rule. It is test
// data only: the real list is the seed's (issue #260; TestSeedCatalogData).
// Its keys are not the seed's; a test that reads the whole list removes the
// seed first (clearStudyTags).
const testStudyTags = `INSERT INTO catalog_study_tags (key, parent_key, position, labels, status) VALUES
	('level_a', NULL, 1, '{"tr": "Düzey A"}', 'active'),
	('level_a_other', 'level_a', 99, '{"tr": "Diğer"}', 'active'),
	('subject_a', 'level_a', 1, '{"tr": "Ders A", "en": "Subject A"}', 'active'),
	('subject_a_other', 'subject_a', 99, '{"tr": "Diğer"}', 'active'),
	('topic_a', 'subject_a', 1, '{"tr": "Konu A"}', 'active'),
	('topic_old', 'subject_a', 2, '{"tr": "Eski konu"}', 'retired'),
	('subject_old', 'level_a', 2, '{"tr": "Eski ders"}', 'retired'),
	('topic_b', 'subject_old', 1, '{"tr": "Konu B"}', 'active'),
	('level_b', NULL, 2, '{"tr": "Düzey B"}', 'retired'),
	('subject_c', 'level_b', 1, '{"tr": "Ders C"}', 'active'),
	('subject_c_other', 'subject_c', 99, '{"tr": "Diğer"}', 'active')`

// clearStudyTags removes the seed's list (00066), for a test that reads the
// whole list.
const clearStudyTags = `DELETE FROM catalog_study_tags`

// An empty list is served as an empty list.
func TestEmptyStudyTags(t *testing.T) {
	svc, _, admin := setup(t)
	exec(t, admin, clearStudyTags)
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.StudyTags == nil || len(c.StudyTags) != 0 {
		t.Fatalf("etiketler = %+v", c.StudyTags)
	}
	if err := CheckStudyTags(c.StudyTags); err != nil {
		t.Fatal(err)
	}
}

func TestStudyTagsInCatalog(t *testing.T) {
	svc, _, admin := setup(t)
	exec(t, admin, clearStudyTags)
	exec(t, admin, testStudyTags)
	c, _, err := svc.Get(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, tag := range c.StudyTags {
		keys = append(keys, tag.Key)
	}
	want := []string{
		"level_a", "subject_a", "subject_c", "topic_a", "topic_b",
		"level_b", "subject_old", "topic_old",
		"level_a_other", "subject_a_other", "subject_c_other",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("sıra = %v", keys)
	}
	byKey := map[string]StudyTag{}
	for _, tag := range c.StudyTags {
		byKey[tag.Key] = tag
	}
	if s := byKey["subject_a"]; s.ParentKey != "level_a" || s.Position != 1 || s.Labels["tr"] != "Ders A" ||
		s.Labels["en"] != "Subject A" || s.Status != StatusActive {
		t.Fatalf("ders = %+v", s)
	}
	if l := byKey["level_b"]; l.ParentKey != "" || l.Status != StatusRetired {
		t.Fatalf("emekli düzey = %+v", l)
	}
	if err := CheckStudyTags(c.StudyTags); err != nil {
		t.Fatalf("örnek liste kurallara uymuyor: %v", err)
	}
}

func TestCheckStudySelection(t *testing.T) {
	_, pool, admin := setup(t)
	exec(t, admin, testStudyTags)
	q := store.New(pool)

	for name, c := range map[string][2]string{
		"ders":                {"subject_a", ""},
		"ders ve konu":        {"subject_a", "topic_a"},
		"Diğer konu":          {"subject_a", "subject_a_other"},
		"düzeyin Diğer dersi": {"level_a_other", ""},
	} {
		if err := CheckStudySelection(ctx, q, c[0], c[1]); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	for name, c := range map[string]struct {
		subject, topic, field string
	}{
		"ders yok":               {"", "", FieldSubjectKey},
		"düzey ders değil":       {"level_a", "", FieldSubjectKey},
		"konu ders değil":        {"topic_a", "", FieldSubjectKey},
		"emekli ders":            {"subject_old", "", FieldSubjectKey},
		"emekli düzeyin dersi":   {"subject_c", "", FieldSubjectKey},
		"bilinmeyen ders":        {"subject_x", "", FieldSubjectKey},
		"bozuk anahtar":          {"Subject A", "", FieldSubjectKey},
		"sql":                    {"subject_a' OR '1'='1", "", FieldSubjectKey},
		"emekli konu":            {"subject_a", "topic_old", FieldTopicKey},
		"başka dersin konusu":    {"subject_a", "topic_b", FieldTopicKey},
		"ders konu değil":        {"subject_a", "subject_a", FieldTopicKey},
		"düzey konu değil":       {"subject_a", "level_a", FieldTopicKey},
		"bilinmeyen konu":        {"subject_a", "topic_x", FieldTopicKey},
		"bozuk konu anahtarı":    {"subject_a", "Topic", FieldTopicKey},
		"emekli dersin konusu":   {"subject_old", "topic_b", FieldSubjectKey},
		"ders yokken konu":       {"", "topic_a", FieldSubjectKey},
		"Diğer dersin dışındaki": {"level_a_other", "subject_a_other", FieldTopicKey},
	} {
		var v *apperr.ValidationError
		if err := CheckStudySelection(ctx, q, c.subject, c.topic); !errors.As(err, &v) || v.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckStudyTags(t *testing.T) {
	tag := func(key, parent, status string) StudyTag {
		return StudyTag{Key: key, ParentKey: parent, Labels: map[string]string{"tr": key}, Status: status}
	}
	valid := []StudyTag{
		tag("level", "", StatusActive),
		tag("level_other", "level", StatusActive),
		tag("subject", "level", StatusActive),
		tag("subject_other", "subject", StatusActive),
		tag("topic", "subject", StatusActive),
		// A retired subject needs no "Diğer".
		tag("old", "level", StatusRetired),
	}
	if err := CheckStudyTags(valid); err != nil {
		t.Fatal(err)
	}
	without := func(key string) []StudyTag {
		return slices.DeleteFunc(slices.Clone(valid), func(t StudyTag) bool { return t.Key == key })
	}
	with := func(tags ...StudyTag) []StudyTag { return append(slices.Clone(valid), tags...) }
	for name, tags := range map[string][]StudyTag{
		"düzeyin Diğer'i yok":      without("level_other"),
		"dersin Diğer'i yok":       without("subject_other"),
		"üst etiket yok":           without("level"),
		"Diğer emekli":             append(without("subject_other"), tag("subject_other", "subject", StatusRetired)),
		"Diğer başka yerde":        append(without("subject_other"), tag("subject_other", "level", StatusActive)),
		"Diğer'in alt etiketi var": with(tag("deep", "level_other", StatusActive)),
		"dört seviye":              with(tag("deep", "topic", StatusActive)),
		"tekrar eden anahtar":      with(tag("topic", "subject", StatusActive)),
		"kendi kendinin üstü":      with(tag("loop", "loop", StatusActive)),
		"üst etiketsiz Diğer":      with(tag("free_other", "", StatusActive)),
	} {
		if err := CheckStudyTags(tags); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

// What the database holds is checked again before it reaches a client
// (ADR-0011).
func TestTamperedStudyTagsAreNotServed(t *testing.T) {
	svc, _, admin := setup(t)
	exec(t, admin, testStudyTags)
	exec(t, admin, `UPDATE catalog_study_tags SET labels = '{"tr": "Ders\nA"}' WHERE key = 'subject_a'`)
	if _, _, err := svc.Get(ctx, 0); err == nil {
		t.Fatal("bozuk etiket sunuldu")
	}
}

// The service's database user only reads the list (ADR-0015).
func TestAppRoleCannotChangeStudyTags(t *testing.T) {
	_, pool, admin := setup(t)
	exec(t, admin, testStudyTags)
	for name, sql := range map[string]string{
		"ekleme":          `INSERT INTO catalog_study_tags (key, position, labels) VALUES ('level_x', 1, '{"tr": "x"}')`,
		"ad değiştirme":   `UPDATE catalog_study_tags SET labels = '{"tr": "x"}' WHERE key = 'subject_a'`,
		"emekliye ayırma": `UPDATE catalog_study_tags SET status = 'retired' WHERE key = 'subject_a'`,
		"silme":           `DELETE FROM catalog_study_tags WHERE key = 'topic_a'`,
	} {
		t.Run(name, func(t *testing.T) { dbtest.WantDenied(t, pool, sql) })
	}
}

// The database's own rules: the second line of defence behind the
// validator.
func TestStudyTagConstraints(t *testing.T) {
	_, _, admin := setup(t)
	exec(t, admin, testStudyTags)
	for name, sql := range map[string]string{
		"bozuk anahtar":        `INSERT INTO catalog_study_tags (key, position, labels) VALUES ('Bad Key', 1, '{"tr": "x"}')`,
		"Türkçe adsız":         `INSERT INTO catalog_study_tags (key, position, labels) VALUES ('level_x', 1, '{"en": "x"}')`,
		"ad nesne değil":       `INSERT INTO catalog_study_tags (key, position, labels) VALUES ('level_x', 1, '"x"')`,
		"bilinmeyen durum":     `UPDATE catalog_study_tags SET status = 'deleted' WHERE key = 'topic_a'`,
		"eksi sıra":            `UPDATE catalog_study_tags SET position = -1 WHERE key = 'topic_a'`,
		"bilinmeyen üst":       `INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES ('topic_x', 'subject_x', 1, '{"tr": "x"}')`,
		"kendi kendinin üstü":  `INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES ('topic_x', 'topic_x', 1, '{"tr": "x"}')`,
		"dört seviye":          `INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES ('topic_x', 'topic_a', 1, '{"tr": "x"}')`,
		"anahtar değişmez":     `UPDATE catalog_study_tags SET key = 'topic_x' WHERE key = 'topic_a'`,
		"üst etiket değişmez":  `UPDATE catalog_study_tags SET parent_key = 'subject_old' WHERE key = 'topic_a'`,
		"düzey ders olamaz":    `UPDATE catalog_study_tags SET parent_key = 'level_b' WHERE key = 'level_a'`,
		"kullanılan üst silme": `DELETE FROM catalog_study_tags WHERE key = 'subject_a'`,
		"tekrar eden anahtar":  `INSERT INTO catalog_study_tags (key, position, labels) VALUES ('level_a', 3, '{"tr": "x"}')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, sql); err == nil {
				t.Fatal("kabul edildi")
			}
		})
	}
	// What may change: names, order and status.
	exec(t, admin, `UPDATE catalog_study_tags SET labels = '{"tr": "Konu A2"}', position = 5, status = 'retired' WHERE key = 'topic_a'`)
}

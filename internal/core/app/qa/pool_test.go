package qa

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// poolStudyTags is test data only: the real list is the platform owner's
// (issue #260). subject_old is retired, and so is topic_old.
const poolStudyTags = `INSERT INTO catalog_study_tags (key, parent_key, position, labels, status) VALUES
	('level_a', NULL, 1, '{"tr": "Düzey A"}', 'active'),
	('level_a_other', 'level_a', 99, '{"tr": "Diğer"}', 'active'),
	('subject_a', 'level_a', 1, '{"tr": "Ders A"}', 'active'),
	('subject_a_other', 'subject_a', 99, '{"tr": "Diğer"}', 'active'),
	('topic_a', 'subject_a', 1, '{"tr": "Konu A"}', 'active'),
	('topic_old', 'subject_a', 2, '{"tr": "Eski konu"}', 'retired'),
	('subject_b', 'level_a', 2, '{"tr": "Ders B"}', 'active'),
	('topic_b', 'subject_b', 1, '{"tr": "Konu B"}', 'active'),
	('subject_old', 'level_a', 3, '{"tr": "Eski ders"}', 'retired')`

// setupPool is setup with the study tag list written as the platform would
// (the catalog is read-only for the service).
func setupPool(t *testing.T) *env {
	t.Helper()
	e, _ := setupPoolAdmin(t)
	return e
}

// setupPoolAdmin also returns the superuser pool for catalog changes.
func setupPoolAdmin(t *testing.T) (*env, *pgxpool.Pool) {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	e := &env{pool: d.App}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	if e.svc, err = New(Config{Pool: d.App, Now: now}); err != nil {
		t.Fatal(err)
	}
	if e.tayfas, err = tayfa.New(tayfa.Config{Pool: d.App, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, poolStudyTags); err != nil {
		t.Fatal(err)
	}
	return e, d.Admin
}

func (e *env) photo(t *testing.T, owner, tf id.ID) id.ID {
	t.Helper()
	return mediatest.Ready(t, e.pool, owner, media.PurposeQuestionPool, tf)
}

func (e *env) poolAsk(t *testing.T, c crew, author id.ID, subject, topic, body string, photos ...id.ID) Question {
	t.Helper()
	v, err := e.svc.CreateQuestion(ctx, author, c.tayfa, CreateQuestionInput{
		Pool: PoolQuestionPool, SubjectKey: subject, TopicKey: topic, Body: body, Photos: idStrs(photos),
		ClientRequestID: id.New().String(),
	})
	must(t, err)
	return v
}

func idStrs(ids []id.ID) []string {
	out := make([]string, 0, len(ids))
	for _, x := range ids {
		out = append(out, x.String())
	}
	return out
}

// A pool question: subject required, topic optional, text and/or up to two
// photos; no title, no tags; Soru-Cevap and the pool never mix.
func TestPoolQuestionCreate(t *testing.T) {
	e := setupPool(t)
	c := e.crew(t)
	p1, p2 := e.photo(t, c.member, c.tayfa), e.photo(t, c.member, c.tayfa)
	q := e.poolAsk(t, c, c.member, "subject_a", "topic_a", " Şu integrali çözemedim ", p1, p2)
	if q.Pool != PoolQuestionPool || q.Title != "" || q.Body != "Şu integrali çözemedim" || q.SubjectKey != "subject_a" ||
		q.TopicKey != "topic_a" || !slices.Equal(q.Photos, []id.ID{p1, p2}) || len(q.Tags) != 0 {
		t.Fatalf("havuz sorusu: %+v", q)
	}
	// A photo alone, without a topic.
	p3 := e.photo(t, c.member2, c.tayfa)
	onlyPhoto := e.poolAsk(t, c, c.member2, "subject_b", "", "", p3)
	if onlyPhoto.Body != "" || onlyPhoto.TopicKey != "" || len(onlyPhoto.Photos) != 1 {
		t.Fatalf("yalnızca fotoğraf: %+v", onlyPhoto)
	}

	in := func(mod func(*CreateQuestionInput)) error {
		x := CreateQuestionInput{Pool: PoolQuestionPool, SubjectKey: "subject_a", Body: "Soru", ClientRequestID: id.New().String()}
		mod(&x)
		_, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, x)
		return err
	}
	for name, tc := range map[string]struct {
		mod   func(*CreateQuestionInput)
		field string
	}{
		"başlık verilemez":     {func(x *CreateQuestionInput) { x.Title = "Başlıklı bir soru" }, FieldTitle},
		"etiket verilemez":     {func(x *CreateQuestionInput) { x.Tags = []string{"matematik"} }, FieldTags},
		"ders yok":             {func(x *CreateQuestionInput) { x.SubjectKey = "" }, FieldSubjectKey},
		"düzey ders değildir":  {func(x *CreateQuestionInput) { x.SubjectKey = "level_a" }, FieldSubjectKey},
		"emekli ders":          {func(x *CreateQuestionInput) { x.SubjectKey = "subject_old" }, FieldSubjectKey},
		"bilinmeyen ders":      {func(x *CreateQuestionInput) { x.SubjectKey = "yok_boyle" }, FieldSubjectKey},
		"başka dersin konusu":  {func(x *CreateQuestionInput) { x.TopicKey = "topic_b" }, FieldTopicKey},
		"emekli konu":          {func(x *CreateQuestionInput) { x.TopicKey = "topic_old" }, FieldTopicKey},
		"metin de foto da yok": {func(x *CreateQuestionInput) { x.Body = "  " }, FieldBody},
		"üç fotoğraf": {func(x *CreateQuestionInput) {
			x.Photos = []string{id.New().String(), id.New().String(), id.New().String()}
		}, FieldPhotoIDs},
		"aynı fotoğraf iki kez": {func(x *CreateQuestionInput) { x.Photos = []string{p1.String(), p1.String()} }, FieldPhotoIDs},
		"bozuk fotoğraf":        {func(x *CreateQuestionInput) { x.Photos = []string{"foto"} }, FieldPhotoIDs},
		"bilinmeyen havuz":      {func(x *CreateQuestionInput) { x.Pool = "pool" }, FieldPool},
		"kontrol karakteri":     {func(x *CreateQuestionInput) { x.Body = "a\x00b" }, FieldBody},
	} {
		field(t, name, in(tc.mod), tc.field)
	}
	// Uploads: someone else's, of another tayfa or purpose, or already used.
	other := e.crew(t)
	is(t, "başkasının fotoğrafı", in(func(x *CreateQuestionInput) { x.Photos = []string{e.photo(t, c.member2, c.tayfa).String()} }), apperr.ErrUploadNotFound)
	is(t, "başka tayfanın fotoğrafı", in(func(x *CreateQuestionInput) { x.Photos = []string{e.photo(t, c.member, other.tayfa).String()} }), apperr.ErrUploadNotFound)
	cover := mediatest.Ready(t, e.pool, c.member, media.PurposeCover, c.tayfa)
	is(t, "kapak soru fotoğrafı olamaz", in(func(x *CreateQuestionInput) { x.Photos = []string{cover.String()} }), apperr.ErrUploadNotFound)
	is(t, "kullanılmış fotoğraf", in(func(x *CreateQuestionInput) { x.Photos = []string{p1.String()} }), apperr.ErrUploadInUse)

	// Soru-Cevap keeps its rules and takes none of the pool's fields.
	qa := func(mod func(*CreateQuestionInput)) error {
		x := CreateQuestionInput{Title: "Kamp yeri neresi olsun?", ClientRequestID: id.New().String()}
		mod(&x)
		_, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, x)
		return err
	}
	field(t, "Soru-Cevap'ta ders", qa(func(x *CreateQuestionInput) { x.SubjectKey = "subject_a" }), FieldSubjectKey)
	field(t, "Soru-Cevap'ta konu", qa(func(x *CreateQuestionInput) { x.TopicKey = "topic_a" }), FieldTopicKey)
	field(t, "Soru-Cevap'ta fotoğraf", qa(func(x *CreateQuestionInput) { x.Photos = []string{e.photo(t, c.member, c.tayfa).String()} }), FieldPhotoIDs)
	field(t, "Soru-Cevap'ta başlık zorunlu", qa(func(x *CreateQuestionInput) { x.Title = "" }), FieldTitle)
	plain := e.ask(t, c, c.member, "Kamp yeri neresi olsun?", "")
	if plain.Pool != PoolQA || len(plain.Photos) != 0 || plain.SubjectKey != "" {
		t.Fatalf("Soru-Cevap sorusu: %+v", plain)
	}

	// Each pool lists its own questions; the filters are the pool's.
	list := func(in ListQuestionsInput) []id.ID {
		t.Helper()
		got, _, err := e.svc.ListQuestions(ctx, c.member2, c.tayfa, in)
		must(t, err)
		var out []id.ID
		for _, x := range got {
			out = append(out, x.ID)
		}
		return out
	}
	if got := list(ListQuestionsInput{}); !slices.Equal(got, []id.ID{plain.ID}) {
		t.Fatalf("Soru-Cevap listesi: %v", got)
	}
	if got := list(ListQuestionsInput{Pool: PoolQuestionPool}); !slices.Equal(got, []id.ID{onlyPhoto.ID, q.ID}) {
		t.Fatalf("havuz listesi: %v", got)
	}
	if got := list(ListQuestionsInput{Pool: PoolQuestionPool, SubjectKey: "subject_a"}); !slices.Equal(got, []id.ID{q.ID}) {
		t.Fatalf("ders süzgeci: %v", got)
	}
	if got := list(ListQuestionsInput{Pool: PoolQuestionPool, SubjectKey: "subject_a", TopicKey: "topic_old"}); len(got) != 0 {
		t.Fatalf("konu süzgeci: %v", got)
	}
	if got := list(ListQuestionsInput{Pool: PoolQuestionPool, Query: "integrali"}); !slices.Equal(got, []id.ID{q.ID}) {
		t.Fatalf("havuzda arama: %v", got)
	}
	if got := list(ListQuestionsInput{Query: "integrali"}); len(got) != 0 {
		t.Fatalf("Soru-Cevap araması havuzu buldu: %v", got)
	}
	_, _, err := e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{Pool: PoolQuestionPool, TagID: id.New().String()})
	field(t, "havuzda etiket süzgeci", err, "tag_id")
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{SubjectKey: "subject_a"})
	field(t, "Soru-Cevap'ta ders süzgeci", err, FieldSubjectKey)
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{Pool: PoolQuestionPool, TopicKey: "topic_a"})
	field(t, "dersiz konu süzgeci", err, FieldTopicKey)
	got, err := e.svc.GetQuestion(ctx, c.member2, c.tayfa, q.ID)
	if err != nil || got.Pool != PoolQuestionPool || !slices.Equal(got.Photos, []id.ID{p1, p2}) {
		t.Fatalf("havuz sorusunu okuma: %+v %v", got, err)
	}
	// The audit trail and the outbox never hold the text.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%integral%'`); n != 0 {
		t.Fatalf("metin olaya yazıldı: %d", n)
	}
}

// Answers in the pool: text and/or photos, helpful votes, no solved mark.
func TestPoolAnswers(t *testing.T) {
	e := setupPool(t)
	c := e.crew(t)
	q := e.poolAsk(t, c, c.member, "subject_a", "", "Bu soruyu nasıl çözerim?")
	ph := e.photo(t, c.member2, c.tayfa)
	a, err := e.svc.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, CreateAnswerInput{Photos: []string{ph.String()}, ClientRequestID: id.New().String()})
	if err != nil || a.Body != "" || a.Pool != PoolQuestionPool || !slices.Equal(a.Photos, []id.ID{ph}) {
		t.Fatalf("fotoğraflı cevap: %+v %v", a, err)
	}
	_, err = e.svc.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, CreateAnswerInput{Body: " ", ClientRequestID: id.New().String()})
	field(t, "boş havuz cevabı", err, FieldBody)
	// Helpful votes as in Soru-Cevap.
	v, err := e.svc.SetVote(ctx, c.member, c.tayfa, a.ID, true)
	if err != nil || v.VoteCount != 1 || !v.Voted {
		t.Fatalf("faydalı oy: %+v %v", v, err)
	}
	// Replacing the photo deletes the old file; text only is fine too.
	u, err := e.svc.UpdateAnswer(ctx, c.member2, c.tayfa, a.ID, "Önce paydayı eşitle.", nil)
	if err != nil || u.Body != "Önce paydayı eşitle." || len(u.Photos) != 0 || u.VoteCount != 1 {
		t.Fatalf("cevap düzenleme: %+v %v", u, err)
	}
	if s := mediatest.Status(t, e.pool, ph); s != "deleting" {
		t.Fatalf("bırakılan fotoğraf = %q", s)
	}
	_, err = e.svc.UpdateAnswer(ctx, c.member2, c.tayfa, a.ID, "", nil)
	field(t, "boşaltılan cevap", err, FieldBody)

	// A Soru-Cevap answer takes no photo and needs text.
	plain := e.ask(t, c, c.member, "Kamp yeri neresi olsun?", "")
	_, err = e.svc.CreateAnswer(ctx, c.member2, c.tayfa, plain.ID, CreateAnswerInput{Body: "Göl", Photos: []string{e.photo(t, c.member2, c.tayfa).String()}, ClientRequestID: id.New().String()})
	field(t, "Soru-Cevap cevabında fotoğraf", err, FieldPhotoIDs)
	_, err = e.svc.CreateAnswer(ctx, c.member2, c.tayfa, plain.ID, CreateAnswerInput{Body: "", ClientRequestID: id.New().String()})
	field(t, "Soru-Cevap cevabı boş", err, FieldBody)

	// The author's own deletion empties the answer and releases its photos.
	ph2 := e.photo(t, c.member2, c.tayfa)
	b, err := e.svc.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, CreateAnswerInput{Photos: []string{ph2.String()}, ClientRequestID: id.New().String()})
	must(t, err)
	must(t, e.svc.DeleteAnswer(ctx, c.member2, c.tayfa, b.ID))
	if s := mediatest.Status(t, e.pool, ph2); s != "deleting" {
		t.Fatalf("silinen cevabın fotoğrafı = %q", s)
	}
	_, err = e.svc.RestoreAnswer(ctx, c.admin, c.tayfa, b.ID)
	is(t, "boşaltılan cevap geri alındı", err, apperr.ErrAnswerNotFound)
}

// Editing a pool question: subject, photos, the other pool's paths; a
// retired subject stays on the question until it changes.
func TestPoolQuestionUpdate(t *testing.T) {
	d, admin := setupPoolAdmin(t)
	c := d.crew(t)
	p1 := d.photo(t, c.member, c.tayfa)
	q := d.poolAsk(t, c, c.member, "subject_a", "topic_a", "", p1)

	upd := func(user id.ID, in UpdateQuestionInput) (Question, error) {
		return d.svc.UpdateQuestion(ctx, user, c.tayfa, q.ID, in)
	}
	_, err := upd(c.member, UpdateQuestionInput{Fields: []string{FieldTitle}, Title: "Başlık eklemek istiyorum"})
	field(t, "havuzda başlık", err, "update_mask")
	_, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldTags}, Tags: []string{"x"}})
	field(t, "havuzda etiket", err, "update_mask")
	_, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldPhotos}})
	field(t, "metinsiz ve fotoğrafsız", err, FieldBody)
	_, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldSubject}, SubjectKey: "subject_old"})
	field(t, "emekli derse geçiş", err, FieldSubjectKey)

	p2 := d.photo(t, c.member, c.tayfa)
	v, err := upd(c.member, UpdateQuestionInput{Fields: []string{FieldBody, FieldPhotos}, Body: "Metin de ekledim", Photos: []string{p2.String(), p1.String()}})
	if err != nil || v.Body != "Metin de ekledim" || !slices.Equal(v.Photos, []id.ID{p2, p1}) || v.SubjectKey != "subject_a" {
		t.Fatalf("düzenleme: %+v %v", v, err)
	}
	v, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldSubject, FieldPhotos}, SubjectKey: "subject_b", Photos: []string{p2.String()}})
	if err != nil || v.SubjectKey != "subject_b" || v.TopicKey != "" || !slices.Equal(v.Photos, []id.ID{p2}) {
		t.Fatalf("ders değişimi: %+v %v", v, err)
	}
	if s := mediatest.Status(t, d.pool, p1); s != "deleting" {
		t.Fatalf("çıkarılan fotoğraf = %q", s)
	}
	// A moderator may drop the author's photo but not add someone's.
	v, err = upd(c.mod, UpdateQuestionInput{Fields: []string{FieldPhotos}})
	if err != nil || len(v.Photos) != 0 {
		t.Fatalf("moderatör fotoğrafı kaldırdı: %+v %v", v, err)
	}
	_, err = upd(c.mod, UpdateQuestionInput{Fields: []string{FieldPhotos}, Photos: []string{d.photo(t, c.member, c.tayfa).String()}})
	is(t, "moderatör başkasının yüklemesini ekledi", err, apperr.ErrUploadNotFound)

	// The same values record nothing.
	before := d.count(t, "SELECT count(*) FROM domain_events WHERE type = $1", EventQuestionUpdated)
	_, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldSubject, FieldBody}, SubjectKey: "subject_b", Body: "Metin de ekledim"})
	must(t, err)
	if n := d.count(t, "SELECT count(*) FROM domain_events WHERE type = $1", EventQuestionUpdated); n != before {
		t.Fatalf("aynı değerler kaydedildi: %d → %d", before, n)
	}

	// A retired subject stays on the question: other edits and the same
	// subject again are fine.
	if _, err := admin.Exec(ctx, "UPDATE catalog_study_tags SET status = 'retired' WHERE key = 'subject_b'"); err != nil {
		t.Fatal(err)
	}
	v, err = upd(c.member, UpdateQuestionInput{Fields: []string{FieldBody, FieldSubject}, Body: "Ders emekli oldu", SubjectKey: "subject_b"})
	if err != nil || v.SubjectKey != "subject_b" || v.Body != "Ders emekli oldu" {
		t.Fatalf("emekli ders soruda kalmadı: %+v %v", v, err)
	}

	// Soru-Cevap refuses the pool's paths.
	plain := d.ask(t, c, c.member, "Kamp yeri neresi olsun?", "")
	_, err = d.svc.UpdateQuestion(ctx, c.member, c.tayfa, plain.ID, UpdateQuestionInput{Fields: []string{FieldSubject}, SubjectKey: "subject_a"})
	field(t, "Soru-Cevap'ta ders", err, "update_mask")
}

// The author's own deletion empties the pool question and releases its
// photos; a moderator's deletion hides it and an admin restores it.
func TestPoolQuestionDelete(t *testing.T) {
	e := setupPool(t)
	c := e.crew(t)
	ph := e.photo(t, c.member, c.tayfa)
	own := e.poolAsk(t, c, c.member, "subject_a", "", "", ph)
	must(t, e.svc.DeleteQuestion(ctx, c.member, c.tayfa, own.ID))
	if s := mediatest.Status(t, e.pool, ph); s != "deleting" {
		t.Fatalf("silinen sorunun fotoğrafı = %q", s)
	}
	_, err := e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, own.ID)
	is(t, "yazarın silmesi geri alındı", err, apperr.ErrQuestionNotFound)

	ph2 := e.photo(t, c.member, c.tayfa)
	hidden := e.poolAsk(t, c, c.member, "subject_a", "", "", ph2)
	must(t, e.svc.DeleteQuestion(ctx, c.mod, c.tayfa, hidden.ID))
	if s := mediatest.Status(t, e.pool, ph2); s != "ready" {
		t.Fatalf("gizlenen sorunun fotoğrafı = %q", s)
	}
	v, err := e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, hidden.ID)
	if err != nil || !slices.Equal(v.Photos, []id.ID{ph2}) {
		t.Fatalf("geri alma: %+v %v", v, err)
	}
}

// Each pool answers to its own module: the question pool off closes its
// questions and answers, reads included, and leaves Soru-Cevap open; and
// the other way round. Tags are Soru-Cevap's.
func TestPoolModules(t *testing.T) {
	e := setupPool(t)
	c := e.crew(t)
	pq := e.poolAsk(t, c, c.member, "subject_a", "", "Havuz sorusu")
	pa := e.answer(t, c, c.member2, pq, "Havuz cevabı")
	plain := e.ask(t, c, c.member, "Kamp yeri neresi olsun?", "", "kamp")
	pla := e.answer(t, c, c.member2, plain, "Göl kenarı")

	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "question_pool", false))
	for method, err := range e.callAll(c.member, c.tayfa, pq.ID, pa.ID, plain.Tags[0].ID) {
		switch method {
		case "ListTags", "DeleteTag", "CreateQuestion", "ListQuestions":
			// Soru-Cevap's.
		default:
			is(t, "havuz kapalı "+method, err, apperr.ErrModuleDisabled)
		}
	}
	_, _, err := e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{Pool: PoolQuestionPool})
	is(t, "havuz kapalı liste", err, apperr.ErrModuleDisabled)
	_, err = e.svc.CreateQuestion(ctx, c.member, c.tayfa, CreateQuestionInput{Pool: PoolQuestionPool, SubjectKey: "subject_a", Body: "x", ClientRequestID: id.New().String()})
	is(t, "havuz kapalı soru", err, apperr.ErrModuleDisabled)
	if _, err := e.svc.GetQuestion(ctx, c.member, c.tayfa, plain.ID); err != nil {
		t.Fatalf("havuz kapalıyken Soru-Cevap: %v", err)
	}

	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "question_pool", true))
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "qa", false))
	if _, err := e.svc.GetQuestion(ctx, c.member, c.tayfa, pq.ID); err != nil {
		t.Fatalf("Soru-Cevap kapalıyken havuz: %v", err)
	}
	if _, err := e.svc.SetVote(ctx, c.member, c.tayfa, pa.ID, true); err != nil {
		t.Fatalf("Soru-Cevap kapalıyken havuz oyu: %v", err)
	}
	_, err = e.svc.SetVote(ctx, c.member, c.tayfa, pla.ID, true)
	is(t, "Soru-Cevap kapalı oy", err, apperr.ErrModuleDisabled)
}

// The database keeps the pools apart too.
func TestPoolConstraints(t *testing.T) {
	e := setupPool(t)
	c := e.crew(t)
	pq := e.poolAsk(t, c, c.member, "subject_a", "", "Havuz sorusu")
	plain := e.ask(t, c, c.member, "Kamp yeri neresi olsun?", "")
	e.photo(t, c.member, c.tayfa)
	for name, sql := range map[string]string{
		"havuz sorusunda başlık":   `UPDATE questions SET title = 'On karakterlik başlık' WHERE id = $1`,
		"havuz sorusu derssiz":     `UPDATE questions SET subject_key = NULL WHERE id = $1`,
		"havuzun değişmesi":        `UPDATE questions SET pool = 'qa', subject_key = NULL WHERE id = $1`,
		"bilinmeyen ders anahtarı": `UPDATE questions SET subject_key = 'yok_boyle' WHERE id = $1`,
		"başka havuzun cevabı":     `INSERT INTO answers (id, question_id, tayfa_id, pool, body, client_request_id) SELECT gen_random_uuid(), id, tayfa_id, 'qa', 'x', gen_random_uuid() FROM questions WHERE id = $1`,
		"bağlanmamış fotoğraf":     `UPDATE questions SET photo1_upload_id = (SELECT id FROM uploads LIMIT 1) WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, pq.ID.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	for name, sql := range map[string]string{
		"Soru-Cevap'ta ders":       `UPDATE questions SET subject_key = 'subject_a' WHERE id = $1`,
		"Soru-Cevap'ta boş başlık": `UPDATE questions SET title = '' WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, plain.ID.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

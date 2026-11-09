package store

import (
	"context"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func newQuestion(t *testing.T, q *Queries, tayfa, author id.ID, title string) id.ID {
	t.Helper()
	qid := id.New()
	if err := q.CreateQuestion(context.Background(), CreateQuestionParams{
		ID: qid.PG(), TayfaID: tayfa.PG(), AuthorID: author.PG(), Title: title, Body: "Gövde", ClientRequestID: id.New().PG(),
	}); err != nil {
		t.Fatalf("soru: %v", err)
	}
	return qid
}

func TestQAConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	other := newTayfa(t, pool, admin, tayfaOpts{})

	for name, tc := range map[string]struct {
		title, body string
		constraint  string
	}{
		"kısa başlık": {"Dokuz kr.", "", "questions_title_len"},
		"uzun başlık": {strings.Repeat("ş", 151), "", "questions_title_len"},
		"uzun gövde":  {"On karakterlik", strings.Repeat("a", 10001), "questions_body_len"},
	} {
		err := q.CreateQuestion(ctx, CreateQuestionParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: tc.title, Body: tc.body, ClientRequestID: id.New().PG(),
		})
		if constraintOf(err) != tc.constraint {
			t.Errorf("%s: %v", name, err)
		}
	}
	crid := id.New()
	if err := q.CreateQuestion(ctx, CreateQuestionParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: strings.Repeat("ş", 150), Body: strings.Repeat("ğ", 10000), ClientRequestID: crid.PG(),
	}); err != nil {
		t.Fatal(err)
	}
	err := q.CreateQuestion(ctx, CreateQuestionParams{ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: "Tekrar gelen soru", ClientRequestID: crid.PG()})
	if constraintOf(err) != "questions_client_request_key" {
		t.Fatalf("tekrar gönderim: %v", err)
	}

	qid := newQuestion(t, q, tayfa, admin, "Bir soru başlığı")
	// An answer belongs to its question's tayfa.
	err = q.CreateAnswer(ctx, CreateAnswerParams{ID: id.New().PG(), QuestionID: qid.PG(), TayfaID: other.PG(), AuthorID: admin.PG(), Body: "x", ClientRequestID: id.New().PG()})
	if constraintOf(err) != "answers_question_fkey" {
		t.Fatalf("başka tayfadan cevap: %v", err)
	}
	for name, body := range map[string]string{"boş": "", "uzun": strings.Repeat("a", 10001)} {
		err := q.CreateAnswer(ctx, CreateAnswerParams{ID: id.New().PG(), QuestionID: qid.PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Body: body, ClientRequestID: id.New().PG()})
		if constraintOf(err) != "answers_body_len" {
			t.Errorf("%s cevap: %v", name, err)
		}
	}
	aid := id.New()
	if err := q.CreateAnswer(ctx, CreateAnswerParams{ID: aid.PG(), QuestionID: qid.PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Body: "Cevap", ClientRequestID: id.New().PG()}); err != nil {
		t.Fatal(err)
	}
	_, err = q.AdjustAnswerVoteCount(ctx, AdjustAnswerVoteCountParams{TayfaID: tayfa.PG(), ID: aid.PG(), Delta: -1})
	if constraintOf(err) != "answers_vote_count" {
		t.Fatalf("negatif oy: %v", err)
	}
	// One vote per person.
	voter := id.New()
	for i, want := range []int64{1, 0} {
		n, err := q.AddAnswerVote(ctx, AddAnswerVoteParams{AnswerID: aid.PG(), UserID: voter.PG()})
		if err != nil || n != want {
			t.Fatalf("oy %d: %d %v", i, n, err)
		}
	}
	// Votes do not move the answer's update time.
	var before, after string
	if err := pool.QueryRow(ctx, "SELECT updated_at::text FROM answers WHERE id = $1", aid.PG()).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AdjustAnswerVoteCount(ctx, AdjustAnswerVoteCountParams{TayfaID: tayfa.PG(), ID: aid.PG(), Delta: 1}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT updated_at::text FROM answers WHERE id = $1", aid.PG()).Scan(&after); err != nil || after != before {
		t.Fatalf("oy updated_at'i değiştirdi: %s %s %v", before, after, err)
	}

	// Tags: unique per tayfa by the normalized name; a tag of another tayfa
	// cannot be linked.
	tag := id.New()
	if err := q.CreateTag(ctx, CreateTagParams{ID: tag.PG(), TayfaID: tayfa.PG(), Name: "Çadır", NameNormalized: "çadır"}); err != nil {
		t.Fatal(err)
	}
	err = q.CreateTag(ctx, CreateTagParams{ID: id.New().PG(), TayfaID: tayfa.PG(), Name: "ÇADIR", NameNormalized: "çadır"})
	if constraintOf(err) != "tags_tayfa_name_key" {
		t.Fatalf("aynı etiket: %v", err)
	}
	if err := q.CreateTag(ctx, CreateTagParams{ID: id.New().PG(), TayfaID: other.PG(), Name: "Çadır", NameNormalized: "çadır"}); err != nil {
		t.Fatalf("başka tayfada aynı ad: %v", err)
	}
	err = q.CreateTag(ctx, CreateTagParams{ID: id.New().PG(), TayfaID: tayfa.PG(), Name: strings.Repeat("a", 31), NameNormalized: "x"})
	if constraintOf(err) != "tags_name_len" {
		t.Fatalf("uzun etiket: %v", err)
	}
	otherQ := newQuestion(t, q, other, admin, "Başka tayfanın sorusu")
	err = q.AddQuestionTag(ctx, AddQuestionTagParams{QuestionID: otherQ.PG(), TagID: tag.PG(), TayfaID: other.PG()})
	if constraintOf(err) != "question_tags_tag_fkey" {
		t.Fatalf("başka tayfanın etiketi: %v", err)
	}
	err = q.AddQuestionTag(ctx, AddQuestionTagParams{QuestionID: otherQ.PG(), TagID: tag.PG(), TayfaID: tayfa.PG()})
	if constraintOf(err) != "question_tags_question_fkey" {
		t.Fatalf("başka tayfanın sorusu: %v", err)
	}
}

// TestTrLowerIgnoresLocale: the search text is folded by a fixed Turkish
// table, so "IŞIK" and "ışık", "İzmir" and "izmir" give the same lexemes
// whatever the database's locale, collation or provider is.
func TestTrLowerIgnoresLocale(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	for _, tc := range [][2]string{
		{"IŞIK", "ışık"},
		{"İZMİR", "izmir"},
		{"İzmir", "izmir"},
		{"ÇAĞDAŞ ÖĞÜT", "çağdaş öğüt"},
		{"ISPARTA", "ısparta"},
		{"Âşık Veysel", "âşık veysel"},
	} {
		var lower string
		var same bool
		if err := pool.QueryRow(ctx, `SELECT tr_lower($1), to_tsvector('turkish'::regconfig, tr_lower($1)) = to_tsvector('turkish'::regconfig, tr_lower($2))`, tc[0], tc[1]).Scan(&lower, &same); err != nil {
			t.Fatal(err)
		}
		if lower != tc[1] || !same {
			t.Errorf("%q: %q %v", tc[0], lower, same)
		}
	}
	// The generated column is IMMUTABLE and recomputed on every edit.
	var volatility string
	if err := pool.QueryRow(ctx, `SELECT provolatile::text FROM pg_proc WHERE proname = 'tr_lower'`).Scan(&volatility); err != nil || volatility != "i" {
		t.Fatalf("tr_lower IMMUTABLE değil: %q %v", volatility, err)
	}
}

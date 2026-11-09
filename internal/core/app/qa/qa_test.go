package qa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	svc    *Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	shift  time.Duration
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "core", coredb.Migrations())
	e := &env{pool: pool}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	if e.svc, err = New(Config{Pool: pool, Now: now}); err != nil {
		t.Fatal(err)
	}
	if e.tayfas, err = tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil {
		t.Fatal(err)
	}
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func field(t *testing.T, what string, err error, want string) {
	t.Helper()
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) || ve.Field != want {
		t.Errorf("%s: %v, %s alanı bekleniyordu", what, err, want)
	}
}

// crew is a tayfa with one member of each kind.
type crew struct {
	tayfa                                     id.ID
	admin, admin2, mod, mod2, member, member2 id.ID
	muted, pending, banned, nonMember         id.ID
}

func (e *env) crew(t *testing.T) crew {
	t.Helper()
	c := crew{admin: id.New(), nonMember: id.New()}
	v, err := e.tayfas.Create(ctx, c.admin, tayfa.CreateInput{Name: "Kamp Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	c.tayfa = v.ID
	c.admin2 = e.member(t, c, authz.Admin)
	c.mod = e.member(t, c, authz.Moderator)
	c.mod2 = e.member(t, c, authz.Moderator)
	c.member = e.member(t, c, authz.Member)
	c.member2 = e.member(t, c, authz.Member)
	c.muted = e.member(t, c, authz.Member)
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.muted.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	c.pending = id.New()
	e.join(t, c.tayfa, c.pending, authz.StatusPending)
	c.banned = e.member(t, c, authz.Member)
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.banned.PG()})
		return err
	})
	return c
}

func (e *env) as(t *testing.T, actor id.ID, fn func(q *store.Queries) error) {
	t.Helper()
	must(t, txn.WithActor(ctx, e.pool, actor, func(_ pgx.Tx, q *store.Queries) error { return fn(q) }))
}

func (e *env) join(t *testing.T, tf, user id.ID, st authz.Status) {
	t.Helper()
	joined := pgtype.Timestamptz{}
	if st == authz.StatusActive {
		joined = ts(time.Now())
	}
	e.as(t, user, func(q *store.Queries) error {
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tf.PG(), UserID: user.PG(), Role: "member", Status: string(st), JoinedVia: "code", JoinedAt: joined,
		})
	})
}

func (e *env) member(t *testing.T, c crew, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, c.tayfa, u, authz.StatusActive)
	if role != authz.Member {
		e.setRole(t, c, u, role)
	}
	return u
}

func (e *env) setRole(t *testing.T, c crew, u id.ID, role authz.Role) {
	t.Helper()
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), Role: role.String()})
		return err
	})
}

func (e *env) ask(t *testing.T, c crew, author id.ID, title, body string, tags ...string) Question {
	t.Helper()
	v, err := e.svc.CreateQuestion(ctx, author, c.tayfa, CreateQuestionInput{Title: title, Body: body, Tags: tags, ClientRequestID: id.New().String()})
	must(t, err)
	return v
}

func (e *env) answer(t *testing.T, c crew, author id.ID, q Question, body string) Answer {
	t.Helper()
	v, err := e.svc.CreateAnswer(ctx, author, c.tayfa, q.ID, CreateAnswerInput{Body: body, ClientRequestID: id.New().String()})
	must(t, err)
	return v
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func tagNames(q Question) string {
	var out []string
	for _, t := range q.Tags {
		out = append(out, t.Name)
	}
	return strings.Join(out, ",")
}

func TestCreateQuestionWhoMayAsk(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"yönetici", c.admin, nil},
		{"moderatör", c.mod, nil},
		{"üye", c.member, nil},
		{"susturulmuş üye", c.muted, apperr.ErrMuted},
		{"onay bekleyen", c.pending, apperr.ErrTayfaNotFound},
		{"banlı", c.banned, apperr.ErrTayfaNotFound},
		{"üye değil", c.nonMember, apperr.ErrTayfaNotFound},
	} {
		_, err := e.svc.CreateQuestion(ctx, tc.user, c.tayfa, CreateQuestionInput{Title: "Kamp ocağı önerisi var mı?", ClientRequestID: id.New().String()})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	if n := e.count(t, "SELECT count(*) FROM questions"); n != 3 {
		t.Fatalf("soru sayısı = %d", n)
	}
}

func TestCreateQuestionRecordsWithoutContent(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	in := CreateQuestionInput{Title: "  Gizli başlık sorusu  ", Body: "\n Gizli gövde \n", Tags: []string{" Gizli   Etiket ", "ÇADIR", "çadır"}, ClientRequestID: id.New().String()}
	v, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, in)
	must(t, err)
	if v.Title != "Gizli başlık sorusu" || v.Body != "Gizli gövde" || v.AuthorID != c.member || v.TayfaID != c.tayfa || v.AnswerCount != 0 {
		t.Fatalf("görünüm = %+v", v)
	}
	// Names that fold the same are one tag; the first spelling is kept.
	if got := tagNames(v); got != "ÇADIR,Gizli Etiket" {
		t.Fatalf("etiketler = %q", got)
	}
	again, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, in)
	if err != nil || again.ID != v.ID {
		t.Fatalf("tekrar gönderim: %v %v", again.ID, err)
	}
	if n := e.count(t, "SELECT count(*) FROM questions"); n != 1 {
		t.Fatalf("tekrar gönderim yeni soru açtı: %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM tags"); n != 2 {
		t.Fatalf("etiket sayısı = %d", n)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'question.created' AND tayfa_id = $1 AND subject_id = $2", c.tayfa.PG(), v.ID.PG()).Scan(&payload))
	if !strings.Contains(payload, v.ID.String()) || strings.Contains(payload, "Gizli") || strings.Contains(payload, "ÇADIR") {
		t.Fatalf("olay = %s", payload)
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'question.created'"); n != 1 {
		t.Fatalf("olay sayısı = %d", n)
	}
	// The same request ID in another tayfa is refused.
	other := e.crew(t)
	e.join(t, other.tayfa, c.member, authz.StatusActive)
	_, err = e.svc.CreateQuestion(ctx, c.member, other.tayfa, in)
	field(t, "başka tayfada aynı istek", err, "client_request_id")
}

func TestCreateQuestionValidation(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	crid := id.New().String()
	ok := "On karakterlik başlık"
	for name, tc := range map[string]struct {
		in    CreateQuestionInput
		field string
	}{
		"kısa başlık":        {CreateQuestionInput{Title: "Kısa?", ClientRequestID: crid}, "title"},
		"uzun başlık":        {CreateQuestionInput{Title: strings.Repeat("ş", 151), ClientRequestID: crid}, "title"},
		"çok satırlı başlık": {CreateQuestionInput{Title: "Birinci satır\nikinci", ClientRequestID: crid}, "title"},
		"bozuk utf8":         {CreateQuestionInput{Title: "Başlık \xff burada", ClientRequestID: crid}, "title"},
		"kontrol karakter":   {CreateQuestionInput{Title: ok, Body: "a\x00b", ClientRequestID: crid}, "body"},
		"uzun gövde":         {CreateQuestionInput{Title: ok, Body: strings.Repeat("ğ", 10001), ClientRequestID: crid}, "body"},
		"çok etiket":         {CreateQuestionInput{Title: ok, Tags: []string{"a", "b", "c", "d", "e", "f"}, ClientRequestID: crid}, "tags"},
		"boş etiket":         {CreateQuestionInput{Title: ok, Tags: []string{"   "}, ClientRequestID: crid}, "tags"},
		"uzun etiket":        {CreateQuestionInput{Title: ok, Tags: []string{strings.Repeat("ı", 31)}, ClientRequestID: crid}, "tags"},
		"satırlı etiket":     {CreateQuestionInput{Title: ok, Tags: []string{"a\nb"}, ClientRequestID: crid}, "tags"},
		"istek kimliği":      {CreateQuestionInput{Title: ok}, "client_request_id"},
	} {
		_, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, tc.in)
		field(t, name, err, tc.field)
	}
	// Limits are inclusive.
	v, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, CreateQuestionInput{
		Title: strings.Repeat("ş", 150), Body: strings.Repeat("ğ", 10000),
		Tags: []string{strings.Repeat("ı", 30), "b", "c", "d", "e"}, ClientRequestID: crid,
	})
	if err != nil || len(v.Tags) != 5 {
		t.Fatalf("sınırlar: %v", err)
	}
	_, err = e.svc.CreateQuestion(ctx, c.member, c.tayfa, CreateQuestionInput{Title: strings.Repeat("ş", 10), ClientRequestID: id.New().String()})
	must(t, err)
}

func TestTagLimit(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	e.as(t, c.admin, func(q *store.Queries) error {
		for i := range MaxTayfaTags - 1 {
			name := fmt.Sprintf("etiket %d", i)
			if err := q.CreateTag(ctx, store.CreateTagParams{ID: id.New().PG(), TayfaID: c.tayfa.PG(), Name: name, NameNormalized: name}); err != nil {
				return err
			}
		}
		return nil
	})
	// An existing tag and one new one fit; two new ones do not.
	_, err := e.svc.CreateQuestion(ctx, c.member, c.tayfa, CreateQuestionInput{Title: "Sınırı deneyen soru", Tags: []string{"yeni bir", "yeni iki"}, ClientRequestID: id.New().String()})
	is(t, "sınır aşıldı", err, apperr.ErrTagLimit)
	q := e.ask(t, c, c.member, "Sınırı deneyen soru", "", "ETİKET 1", "yeni bir")
	if got := tagNames(q); got != "etiket 1,yeni bir" {
		t.Fatalf("etiketler = %q", got)
	}
	if n := e.count(t, "SELECT count(*) FROM tags WHERE tayfa_id = $1", c.tayfa.PG()); n != MaxTayfaTags {
		t.Fatalf("etiket sayısı = %d", n)
	}
	_, err = e.svc.UpdateQuestion(ctx, c.member, c.tayfa, q.ID, UpdateQuestionInput{Fields: []string{FieldTags}, Tags: []string{"bir yenisi"}})
	is(t, "düzenlemede sınır", err, apperr.ErrTagLimit)
}

func TestEditHierarchy(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	byAdmin := e.ask(t, c, c.admin, "Yöneticinin sorusu burada", "")
	byMod := e.ask(t, c, c.mod, "Moderatörün sorusu burada", "")
	byMember := e.ask(t, c, c.member, "Üyenin sorusu burada", "")
	edits := 0
	edit := func(user id.ID, q Question) error {
		edits++
		_, err := e.svc.UpdateQuestion(ctx, user, c.tayfa, q.ID, UpdateQuestionInput{Fields: []string{FieldTitle}, Title: fmt.Sprintf("Değişen başlık %d", edits)})
		return err
	}
	for _, tc := range []struct {
		name string
		user id.ID
		q    Question
		want error
	}{
		{"üye kendi", c.member, byMember, nil},
		{"üye başkasının", c.member2, byMember, apperr.ErrPermissionDenied},
		{"moderatör üyenin", c.mod, byMember, nil},
		{"moderatör başka moderatörün", c.mod2, byMod, apperr.ErrRoleHierarchy},
		{"moderatör yöneticinin", c.mod, byAdmin, apperr.ErrRoleHierarchy},
		{"yönetici moderatörün", c.admin, byMod, nil},
		{"yönetici başka yöneticinin", c.admin2, byAdmin, apperr.ErrRoleHierarchy},
		{"susturulmuş", c.muted, byMember, apperr.ErrPermissionDenied},
		{"üye değil", c.nonMember, byMember, apperr.ErrTayfaNotFound},
		{"bilinmeyen", c.admin, Question{ID: id.New()}, apperr.ErrQuestionNotFound},
	} {
		if err := edit(tc.user, tc.q); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	// A muted author cannot edit their own question.
	mq := e.ask(t, c, c.member2, "Susturulacak kişinin sorusu", "")
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: c.member2.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	is(t, "susturulmuş yazar", edit(c.member2, mq), apperr.ErrMuted)

	// Edits by someone else are audited; one's own are not.
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'question.edited'"); n != 2 {
		t.Fatalf("denetim = %d", n)
	}
	// Tags: replaced; the same set records nothing.
	v, err := e.svc.UpdateQuestion(ctx, c.member, c.tayfa, byMember.ID, UpdateQuestionInput{Fields: []string{FieldTags, FieldBody}, Tags: []string{"Kamp", "Ocak"}, Body: "Yeni gövde"})
	if err != nil || tagNames(v) != "Kamp,Ocak" || v.Body != "Yeni gövde" {
		t.Fatalf("etiket düzenleme: %+v %v", v, err)
	}
	before := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'question.updated'")
	v, err = e.svc.UpdateQuestion(ctx, c.member, c.tayfa, byMember.ID, UpdateQuestionInput{Fields: []string{FieldTags, FieldBody}, Tags: []string{"OCAK", "kamp"}, Body: " Yeni gövde "})
	if err != nil || tagNames(v) != "Kamp,Ocak" {
		t.Fatalf("aynı etiketler: %+v %v", v, err)
	}
	if after := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'question.updated'"); after != before {
		t.Fatal("değişmeyen düzenleme kaydedildi")
	}
	v, err = e.svc.UpdateQuestion(ctx, c.member, c.tayfa, byMember.ID, UpdateQuestionInput{Fields: []string{FieldTags}})
	if err != nil || len(v.Tags) != 0 {
		t.Fatalf("etiketleri boşaltma: %+v %v", v, err)
	}
	for _, in := range []UpdateQuestionInput{{}, {Fields: []string{"author"}}, {Fields: []string{FieldTitle}, Title: "kısa"}} {
		_, err := e.svc.UpdateQuestion(ctx, c.admin, c.tayfa, byAdmin.ID, in)
		var ve *apperr.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestDeleteAndRestoreQuestion(t *testing.T) {
	e := setup(t)
	c := e.crew(t)

	// The author's own deletion empties the content and drops the tags.
	own := e.ask(t, c, c.member, "Kendi silinecek sorum", "Gövde", "kamp")
	e.answer(t, c, c.member2, own, "Bir cevap")
	must(t, e.svc.DeleteQuestion(ctx, c.member, c.tayfa, own.ID))
	var title, body string
	must(t, e.pool.QueryRow(ctx, "SELECT title, body FROM questions WHERE id = $1", own.ID.PG()).Scan(&title, &body))
	if title != "" || body != "" || e.count(t, "SELECT count(*) FROM question_tags WHERE question_id = $1", own.ID.PG()) != 0 {
		t.Fatalf("içerik boşaltılmadı: %q %q", title, body)
	}
	_, err := e.svc.GetQuestion(ctx, c.member2, c.tayfa, own.ID)
	is(t, "silinen okundu", err, apperr.ErrQuestionNotFound)
	_, _, err = e.svc.ListAnswers(ctx, c.member2, c.tayfa, own.ID, 0, "")
	is(t, "silinen sorunun cevapları", err, apperr.ErrQuestionNotFound)
	_, err = e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, own.ID)
	is(t, "yazarın silmesi geri alındı", err, apperr.ErrQuestionNotFound)
	is(t, "iki kez silme", e.svc.DeleteQuestion(ctx, c.member, c.tayfa, own.ID), apperr.ErrQuestionNotFound)

	// A muted author can still delete their own question.
	later := e.member(t, c, authz.Member)
	mq := e.ask(t, c, later, "Susturulanın sorusu burada", "")
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: later.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
	must(t, e.svc.DeleteQuestion(ctx, later, c.tayfa, mq.ID))

	// Moderators hide; the hierarchy applies.
	byAdmin := e.ask(t, c, c.admin, "Yöneticinin sorusu burada", "")
	is(t, "moderatör yöneticininkini sildi", e.svc.DeleteQuestion(ctx, c.mod, c.tayfa, byAdmin.ID), apperr.ErrRoleHierarchy)
	is(t, "üye başkasınınkini sildi", e.svc.DeleteQuestion(ctx, c.member2, c.tayfa, byAdmin.ID), apperr.ErrPermissionDenied)
	byMember := e.ask(t, c, c.member, "Üyenin gizlenecek sorusu", "Gövde", "kamp")
	a := e.answer(t, c, c.member2, byMember, "Cevap duruyor")
	must(t, e.svc.DeleteQuestion(ctx, c.mod, c.tayfa, byMember.ID))
	_, err = e.svc.UpdateAnswer(ctx, c.member2, c.tayfa, a.ID, "Değişti")
	is(t, "gizli sorunun cevabı", err, apperr.ErrAnswerNotFound)
	_, err = e.svc.RestoreQuestion(ctx, c.mod, c.tayfa, byMember.ID)
	is(t, "moderatör geri aldı", err, apperr.ErrPermissionDenied)
	v, err := e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, byMember.ID)
	if err != nil || v.Title != "Üyenin gizlenecek sorusu" || tagNames(v) != "kamp" || v.AnswerCount != 1 {
		t.Fatalf("geri alma: %+v %v", v, err)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action IN ('question.deleted', 'question.restored') AND target_id = $1", byMember.ID.PG()); n != 2 {
		t.Fatalf("denetim = %d", n)
	}
	// Restoring a visible question changes nothing.
	_, err = e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, byMember.ID)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'question.restored'"); n != 1 {
		t.Fatalf("görünen sorunun geri alınması kaydedildi: %d", n)
	}

	// The window.
	late := e.ask(t, c, c.member, "Geç kalınan soru burada", "")
	inTime := e.ask(t, c, c.member, "Zamanında soru burada", "")
	must(t, e.svc.DeleteQuestion(ctx, c.admin, c.tayfa, late.ID))
	must(t, e.svc.DeleteQuestion(ctx, c.admin, c.tayfa, inTime.ID))
	e.shift = RestoreWindow + time.Minute
	_, err = e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, late.ID)
	is(t, "süre geçti", err, apperr.ErrQuestionNotFound)
	e.shift = RestoreWindow - time.Minute
	_, err = e.svc.RestoreQuestion(ctx, c.admin, c.tayfa, inTime.ID)
	must(t, err)
}

func TestAnswers(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q := e.ask(t, c, c.member, "Hangi çadırı alalım?", "")
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"üye", c.member2, nil},
		{"soru sahibi", c.member, nil},
		{"susturulmuş", c.muted, apperr.ErrMuted},
		{"üye değil", c.nonMember, apperr.ErrTayfaNotFound},
	} {
		_, err := e.svc.CreateAnswer(ctx, tc.user, c.tayfa, q.ID, CreateAnswerInput{Body: "Cevabım", ClientRequestID: id.New().String()})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	_, err := e.svc.CreateAnswer(ctx, c.member, c.tayfa, id.New(), CreateAnswerInput{Body: "x", ClientRequestID: id.New().String()})
	is(t, "bilinmeyen soru", err, apperr.ErrQuestionNotFound)
	for name, in := range map[string]CreateAnswerInput{
		"boş":        {Body: "  ", ClientRequestID: id.New().String()},
		"uzun":       {Body: strings.Repeat("ç", 10001), ClientRequestID: id.New().String()},
		"istek yok":  {Body: "x"},
		"kontrol kr": {Body: "a\x07", ClientRequestID: id.New().String()},
	} {
		_, err := e.svc.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, in)
		var ve *apperr.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The event names the question's author, IDs only.
	in := CreateAnswerInput{Body: "Gizli cevap metni", ClientRequestID: id.New().String()}
	a, err := e.svc.CreateAnswer(ctx, c.mod, c.tayfa, q.ID, in)
	must(t, err)
	var payload string
	must(t, e.pool.QueryRow(ctx, "SELECT payload::text FROM domain_events WHERE type = 'answer.created' AND subject_id = $1", a.ID.PG()).Scan(&payload))
	if !strings.Contains(payload, `"question_author_id": "`+c.member.String()) || !strings.Contains(payload, q.ID.String()) || strings.Contains(payload, "Gizli") {
		t.Fatalf("olay = %s", payload)
	}
	again, err := e.svc.CreateAnswer(ctx, c.mod, c.tayfa, q.ID, in)
	if err != nil || again.ID != a.ID {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	q2 := e.ask(t, c, c.member, "Başka bir soru burada", "")
	_, err = e.svc.CreateAnswer(ctx, c.mod, c.tayfa, q2.ID, in)
	field(t, "başka soruda aynı istek", err, "client_request_id")

	// Edit and delete: author or moderator above the author.
	_, err = e.svc.UpdateAnswer(ctx, c.member2, c.tayfa, a.ID, "Değişti")
	is(t, "üye başkasının cevabı", err, apperr.ErrPermissionDenied)
	_, err = e.svc.UpdateAnswer(ctx, c.mod2, c.tayfa, a.ID, "Değişti")
	is(t, "moderatör moderatörün cevabı", err, apperr.ErrRoleHierarchy)
	v, err := e.svc.UpdateAnswer(ctx, c.admin, c.tayfa, a.ID, " Yönetici düzeltti ")
	if err != nil || v.Body != "Yönetici düzeltti" {
		t.Fatalf("düzenleme: %+v %v", v, err)
	}
	g, err := e.svc.GetQuestion(ctx, c.member, c.tayfa, q.ID)
	if err != nil || g.AnswerCount != 3 {
		t.Fatalf("cevap sayısı: %+v %v", g, err)
	}
	must(t, e.svc.DeleteAnswer(ctx, c.admin, c.tayfa, a.ID))
	v, err = e.svc.RestoreAnswer(ctx, c.admin, c.tayfa, a.ID)
	if err != nil || v.Body != "Yönetici düzeltti" {
		t.Fatalf("geri alma: %+v %v", v, err)
	}
	is(t, "moderatör moderatörün cevabını sildi", e.svc.DeleteAnswer(ctx, c.mod2, c.tayfa, a.ID), apperr.ErrRoleHierarchy)
	must(t, e.svc.DeleteAnswer(ctx, c.admin2, c.tayfa, a.ID))
	_, err = e.svc.RestoreAnswer(ctx, c.mod, c.tayfa, a.ID)
	is(t, "moderatör geri aldı", err, apperr.ErrPermissionDenied)
	_, err = e.svc.RestoreAnswer(ctx, c.admin, c.tayfa, a.ID)
	must(t, err)
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action IN ('answer.edited', 'answer.deleted', 'answer.restored')", a.ID.PG()); n != 5 {
		t.Fatalf("denetim = %d", n)
	}
	// Own deletion: erased, not restorable.
	must(t, e.svc.DeleteAnswer(ctx, c.mod, c.tayfa, a.ID))
	_, err = e.svc.RestoreAnswer(ctx, c.admin, c.tayfa, a.ID)
	is(t, "yazarın silmesi", err, apperr.ErrAnswerNotFound)
	var body string
	must(t, e.pool.QueryRow(ctx, "SELECT body FROM answers WHERE id = $1", a.ID.PG()).Scan(&body))
	if body != "" {
		t.Fatalf("cevap boşaltılmadı: %q", body)
	}
}

func TestVotes(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q := e.ask(t, c, c.member, "Oylanacak bir soru burada", "")
	first := e.answer(t, c, c.member2, q, "İlk cevap")
	second := e.answer(t, c, c.mod, q, "İkinci cevap")
	third := e.answer(t, c, c.admin, q, "Üçüncü cevap")

	_, err := e.svc.SetVote(ctx, c.member2, c.tayfa, first.ID, true)
	is(t, "kendi cevabına oy", err, apperr.ErrOwnAnswerVote)
	_, err = e.svc.SetVote(ctx, c.muted, c.tayfa, first.ID, true)
	is(t, "susturulmuş oy", err, apperr.ErrMuted)
	v, err := e.svc.SetVote(ctx, c.member, c.tayfa, third.ID, true)
	if err != nil || v.VoteCount != 1 || !v.Voted {
		t.Fatalf("oy: %+v %v", v, err)
	}
	// The same vote again changes nothing.
	v, err = e.svc.SetVote(ctx, c.member, c.tayfa, third.ID, true)
	if err != nil || v.VoteCount != 1 {
		t.Fatalf("ikinci oy: %+v %v", v, err)
	}
	for _, u := range []id.ID{c.member, c.admin2} {
		_, err = e.svc.SetVote(ctx, u, c.tayfa, second.ID, true)
		must(t, err)
	}
	// Taking a vote back; taking back a missing vote changes nothing.
	v, err = e.svc.SetVote(ctx, c.member, c.tayfa, third.ID, false)
	if err != nil || v.VoteCount != 0 || v.Voted {
		t.Fatalf("geri alma: %+v %v", v, err)
	}
	v, err = e.svc.SetVote(ctx, c.member, c.tayfa, third.ID, false)
	if err != nil || v.VoteCount != 0 {
		t.Fatalf("ikinci geri alma: %+v %v", v, err)
	}
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE type = 'answer.voted'"); n != 4 {
		t.Fatalf("oy olayları = %d", n)
	}
	// Order: most votes first, then oldest; Voted is the reader's own.
	list, _, err := e.svc.ListAnswers(ctx, c.member, c.tayfa, q.ID, 0, "")
	must(t, err)
	if len(list) != 3 || list[0].ID != second.ID || list[1].ID != first.ID || list[2].ID != third.ID {
		t.Fatalf("sıra = %v", list)
	}
	if !list[0].Voted || list[1].Voted || list[0].VoteCount != 2 {
		t.Fatalf("oy görünümü = %+v", list[0])
	}
	// Paging by one.
	var got []id.ID
	token := ""
	for range 5 {
		page, next, err := e.svc.ListAnswers(ctx, c.mod2, c.tayfa, q.ID, 1, token)
		must(t, err)
		for _, a := range page {
			got = append(got, a.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if len(got) != 3 || got[0] != second.ID || got[2] != third.ID {
		t.Fatalf("sayfalı = %v", got)
	}
	_, _, err = e.svc.ListAnswers(ctx, c.mod2, c.tayfa, first.ID, 1, token)
	field(t, "başka sorunun token'ı", err, "page_token")

	// The author's own deletion removes the answer's votes; a hidden
	// answer keeps them and cannot be voted on.
	_, err = e.svc.SetVote(ctx, c.member, c.tayfa, first.ID, true)
	must(t, err)
	must(t, e.svc.DeleteAnswer(ctx, c.admin, c.tayfa, first.ID))
	_, err = e.svc.SetVote(ctx, c.mod2, c.tayfa, first.ID, true)
	is(t, "gizli cevaba oy", err, apperr.ErrAnswerNotFound)
	must(t, e.svc.DeleteAnswer(ctx, c.mod, c.tayfa, second.ID))
	if n := e.count(t, "SELECT count(*) FROM answer_votes WHERE answer_id = $1", second.ID.PG()); n != 0 {
		t.Fatalf("silinen cevabın oyları kaldı: %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM answer_votes WHERE answer_id = $1", first.ID.PG()); n != 1 {
		t.Fatalf("gizlenen cevabın oyu = %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM answers a WHERE a.vote_count <> (SELECT count(*) FROM answer_votes v WHERE v.answer_id = a.id)"); n != 0 {
		t.Fatalf("oy sayısı tutarsız: %d", n)
	}
}

// TestConcurrentVotes: many members vote and take back at once; the count
// always equals the votes.
func TestConcurrentVotes(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q := e.ask(t, c, c.member, "Eşzamanlı oy sorusu burada", "")
	a := e.answer(t, c, c.admin, q, "Cevap")
	voters := make([]id.ID, 12)
	for i := range voters {
		voters[i] = e.member(t, c, authz.Member)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(voters)*3)
	for i, u := range voters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every voter votes twice at once; odd ones then take it back.
			var inner sync.WaitGroup
			for range 2 {
				inner.Add(1)
				go func() {
					defer inner.Done()
					if _, err := e.svc.SetVote(ctx, u, c.tayfa, a.ID, true); err != nil {
						errs <- err
					}
				}()
			}
			inner.Wait()
			if i%2 == 1 {
				if _, err := e.svc.SetVote(ctx, u, c.tayfa, a.ID, false); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var count, votes int
	must(t, e.pool.QueryRow(ctx, "SELECT vote_count, (SELECT count(*) FROM answer_votes WHERE answer_id = $1) FROM answers WHERE id = $1", a.ID.PG()).Scan(&count, &votes))
	if count != len(voters)/2 || votes != count {
		t.Fatalf("vote_count = %d, oylar = %d, %d bekleniyordu", count, votes, len(voters)/2)
	}
}

// TestTurkishSearch: full-text search folds Turkish case correctly (İ/ı)
// and does not depend on the database's locale or collation.
func TestTurkishSearch(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	izmir := e.ask(t, c, c.member, "İzmir'de kamp yeri önerisi", "Körfeze yakın olsun", "Ege")
	isik := e.ask(t, c, c.member, "Gece için ışık lazım mı?", "Fener mi kafa lambası mı", "Ekipman")
	upper := e.ask(t, c, c.member, "IŞIK KAYNAĞI VE ÇADIR", "Pil ne kadar dayanır")
	other := e.ask(t, c, c.member, "Balık tutmak için yem", "Hangi saatte gidelim")
	_ = other

	search := func(query, tag string) []id.ID {
		t.Helper()
		list, _, err := e.svc.ListQuestions(ctx, c.member2, c.tayfa, ListQuestionsInput{Query: query, TagID: tag})
		must(t, err)
		out := make([]id.ID, 0, len(list))
		for _, q := range list {
			out = append(out, q.ID)
		}
		return out
	}
	has := func(got []id.ID, want ...id.ID) bool {
		if len(got) != len(want) {
			return false
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || g == w
			}
			if !found {
				return false
			}
		}
		return true
	}
	for _, tc := range []struct {
		query string
		want  []id.ID
	}{
		{"izmir", []id.ID{izmir.ID}},
		{"İZMİR", []id.ID{izmir.ID}},
		{"İzmir", []id.ID{izmir.ID}},
		{"ışık", []id.ID{isik.ID, upper.ID}},
		{"IŞIK", []id.ID{isik.ID, upper.ID}},
		{"Işık", []id.ID{isik.ID, upper.ID}},
		{"çadır", []id.ID{upper.ID}},
		{"kafa lambası", []id.ID{isik.ID}},
		{"körfez", []id.ID{izmir.ID}},
		{"uzay gemisi", nil},
		// websearch_to_tsquery accepts any text.
		{`"' OR 1=1 -- & | ! ( )`, nil},
	} {
		if got := search(tc.query, ""); !has(got, tc.want...) {
			t.Errorf("%q: %v, %v bekleniyordu", tc.query, got, tc.want)
		}
	}
	// Tag filter, alone and with a query; tags of another tayfa are unknown.
	tags, err := e.svc.ListTags(ctx, c.member, c.tayfa)
	must(t, err)
	var ekipman id.ID
	for _, tg := range tags {
		if tg.Name == "Ekipman" {
			ekipman = tg.ID
			if tg.QuestionCount != 1 {
				t.Fatalf("etiket sayısı = %d", tg.QuestionCount)
			}
		}
	}
	if got := search("", ekipman.String()); !has(got, isik.ID) {
		t.Fatalf("etiket filtresi: %v", got)
	}
	if got := search("ışık", ekipman.String()); !has(got, isik.ID) {
		t.Fatalf("arama ve etiket: %v", got)
	}
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{TagID: id.New().String()})
	is(t, "bilinmeyen etiket", err, apperr.ErrTagNotFound)
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{TagID: "x"})
	field(t, "bozuk etiket", err, "tag_id")
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{Query: strings.Repeat("a", 101)})
	field(t, "uzun sorgu", err, "query")
	_, _, err = e.svc.ListQuestions(ctx, c.member, c.tayfa, ListQuestionsInput{Query: "a\nb"})
	field(t, "satırlı sorgu", err, "query")

	// An edit moves the question in the index; a hidden one leaves it.
	_, err = e.svc.UpdateQuestion(ctx, c.member, c.tayfa, izmir.ID, UpdateQuestionInput{Fields: []string{FieldTitle}, Title: "Ayvalık'ta kamp yeri önerisi"})
	must(t, err)
	if got := search("izmir", ""); len(got) != 0 {
		t.Fatalf("eski metin bulundu: %v", got)
	}
	if got := search("AYVALIK", ""); !has(got, izmir.ID) {
		t.Fatalf("yeni metin: %v", got)
	}
	must(t, e.svc.DeleteQuestion(ctx, c.mod, c.tayfa, isik.ID))
	if got := search("ışık", ""); !has(got, upper.ID) {
		t.Fatalf("gizlenen bulundu: %v", got)
	}

	// The search text never reaches events or audit records.
	if n := e.count(t, "SELECT count(*) FROM domain_events WHERE payload::text ILIKE '%ışık%' OR payload::text ILIKE '%izmir%'"); n != 0 {
		t.Fatal("içerik olaya girdi")
	}
}

func TestListAndSearchPaging(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	var all []Question
	for i := range 5 {
		all = append(all, e.ask(t, c, c.member, fmt.Sprintf("Kamp sorusu numara %d", i), strings.Repeat("kamp ", i+1)))
	}
	collect := func(in ListQuestionsInput) []id.ID {
		t.Helper()
		var out []id.ID
		for range 10 {
			page, next, err := e.svc.ListQuestions(ctx, c.member2, c.tayfa, in)
			must(t, err)
			for _, q := range page {
				out = append(out, q.ID)
			}
			if next == "" {
				break
			}
			in.PageToken = next
		}
		return out
	}
	got := collect(ListQuestionsInput{PageSize: 2})
	if len(got) != 5 || got[0] != all[4].ID || got[4] != all[0].ID {
		t.Fatalf("liste = %v", got)
	}
	got = collect(ListQuestionsInput{Query: "kamp", PageSize: 2})
	seen := map[id.ID]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if len(got) != 5 || len(seen) != 5 {
		t.Fatalf("arama sayfaları = %v", got)
	}
	// A token only works for the request it was made for.
	_, next, err := e.svc.ListQuestions(ctx, c.member2, c.tayfa, ListQuestionsInput{Query: "kamp", PageSize: 1})
	must(t, err)
	for name, in := range map[string]ListQuestionsInput{
		"başka sorgu":   {Query: "çadır", PageToken: next},
		"sorgusuz":      {PageToken: next},
		"bozuk":         {Query: "kamp", PageToken: next[:len(next)-2]},
		"çok uzun":      {Query: "kamp", PageToken: strings.Repeat("a", 600)},
		"negatif sayfa": {PageSize: -1},
	} {
		_, _, err := e.svc.ListQuestions(ctx, c.member2, c.tayfa, in)
		var ve *apperr.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if strings.Contains(next, "kamp") {
		t.Fatal("token sorgu metnini taşıyor")
	}
	other := e.crew(t)
	_, _, err = e.svc.ListQuestions(ctx, other.admin, other.tayfa, ListQuestionsInput{Query: "kamp", PageToken: next})
	field(t, "başka tayfanın token'ı", err, "page_token")
}

func TestTags(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q := e.ask(t, c, c.member, "Etiketli bir soru burada", "", "Kamp", "Ocak")
	tags, err := e.svc.ListTags(ctx, c.muted, c.tayfa)
	if err != nil || len(tags) != 2 || tags[0].Name != "Kamp" {
		t.Fatalf("etiketler: %+v %v", tags, err)
	}
	is(t, "üye etiket sildi", e.svc.DeleteTag(ctx, c.member, c.tayfa, tags[0].ID), apperr.ErrPermissionDenied)
	must(t, e.svc.DeleteTag(ctx, c.mod, c.tayfa, tags[0].ID))
	is(t, "iki kez silme", e.svc.DeleteTag(ctx, c.mod, c.tayfa, tags[0].ID), apperr.ErrTagNotFound)
	v, err := e.svc.GetQuestion(ctx, c.member, c.tayfa, q.ID)
	if err != nil || tagNames(v) != "Ocak" {
		t.Fatalf("soru etiketleri: %+v %v", v, err)
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE action = 'tag.deleted' AND target_id = $1", tags[0].ID.PG()); n != 1 {
		t.Fatalf("denetim = %d", n)
	}
	// Another tayfa's tag cannot be removed through one's own tayfa.
	other := e.crew(t)
	is(t, "başka tayfanın etiketi", e.svc.DeleteTag(ctx, other.admin, other.tayfa, tags[1].ID), apperr.ErrTagNotFound)
}

// callAll calls every method as user with question qid, answer aid and tag
// tid of tayfaID.
func (e *env) callAll(user, tayfaID, qid, aid, tid id.ID) map[string]error {
	out := map[string]error{}
	_, out["CreateQuestion"] = e.svc.CreateQuestion(ctx, user, tayfaID, CreateQuestionInput{Title: "Yeni bir soru burada", ClientRequestID: id.New().String()})
	_, out["GetQuestion"] = e.svc.GetQuestion(ctx, user, tayfaID, qid)
	_, _, out["ListQuestions"] = e.svc.ListQuestions(ctx, user, tayfaID, ListQuestionsInput{Query: "soru"})
	_, out["UpdateQuestion"] = e.svc.UpdateQuestion(ctx, user, tayfaID, qid, UpdateQuestionInput{Fields: []string{FieldTitle}, Title: "Değişen soru başlığı"})
	_, out["RestoreQuestion"] = e.svc.RestoreQuestion(ctx, user, tayfaID, qid)
	_, out["CreateAnswer"] = e.svc.CreateAnswer(ctx, user, tayfaID, qid, CreateAnswerInput{Body: "Cevap", ClientRequestID: id.New().String()})
	_, _, out["ListAnswers"] = e.svc.ListAnswers(ctx, user, tayfaID, qid, 0, "")
	_, out["UpdateAnswer"] = e.svc.UpdateAnswer(ctx, user, tayfaID, aid, "Değişti")
	_, out["SetVote"] = e.svc.SetVote(ctx, user, tayfaID, aid, true)
	_, out["RestoreAnswer"] = e.svc.RestoreAnswer(ctx, user, tayfaID, aid)
	_, out["ListTags"] = e.svc.ListTags(ctx, user, tayfaID)
	out["DeleteTag"] = e.svc.DeleteTag(ctx, user, tayfaID, tid)
	out["DeleteAnswer"] = e.svc.DeleteAnswer(ctx, user, tayfaID, aid)
	out["DeleteQuestion"] = e.svc.DeleteQuestion(ctx, user, tayfaID, qid)
	return out
}

func (e *env) fixture(t *testing.T, c crew) (Question, Answer, id.ID) {
	t.Helper()
	q := e.ask(t, c, c.member, "Hedef soru burada duruyor", "", "hedef")
	a := e.answer(t, c, c.member2, q, "Hedef cevap")
	return q, a, q.Tags[0].ID
}

func TestModuleDisabled(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q, a, tg := e.fixture(t, c)
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "qa", false))
	for _, u := range []id.ID{c.admin, c.member} {
		for method, err := range e.callAll(u, c.tayfa, q.ID, a.ID, tg) {
			is(t, method, err, apperr.ErrModuleDisabled)
		}
	}
	for method, err := range e.callAll(c.nonMember, c.tayfa, q.ID, a.ID, tg) {
		is(t, "üye olmayan "+method, err, apperr.ErrTayfaNotFound)
	}
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "qa", true))
	v, err := e.svc.GetQuestion(ctx, c.member, c.tayfa, q.ID)
	if err != nil || v.Title != "Hedef soru burada duruyor" || v.AnswerCount != 1 {
		t.Fatalf("kapalı modülde değişti: %+v %v", v, err)
	}
}

func TestReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q, a, tg := e.fixture(t, c)
	_, err := e.pool.Exec(ctx, "UPDATE tayfas SET suspended_at = now(), suspended_reason = 'kural ihlali' WHERE id = $1", c.tayfa.PG())
	must(t, err)
	for method, err := range e.callAll(c.admin, c.tayfa, q.ID, a.ID, tg) {
		switch method {
		case "GetQuestion", "ListQuestions", "ListAnswers", "ListTags":
			must(t, err)
		default:
			is(t, method, err, apperr.ErrTayfaReadOnly)
		}
	}
}

// TestOtherTayfasContent: an admin of one tayfa cannot reach another
// tayfa's questions, answers or tags through their own tayfa's ID (IDOR).
func TestOtherTayfasContent(t *testing.T) {
	e := setup(t)
	victim, attacker := e.crew(t), e.crew(t)
	q, a, tg := e.fixture(t, victim)
	for method, err := range e.callAll(attacker.admin, attacker.tayfa, q.ID, a.ID, tg) {
		switch method {
		case "CreateQuestion", "ListQuestions", "ListTags":
			must(t, err)
		case "UpdateAnswer", "SetVote", "RestoreAnswer", "DeleteAnswer":
			is(t, method, err, apperr.ErrAnswerNotFound)
		case "DeleteTag":
			is(t, method, err, apperr.ErrTagNotFound)
		default:
			is(t, method, err, apperr.ErrQuestionNotFound)
		}
	}
	for method, err := range e.callAll(attacker.admin, victim.tayfa, q.ID, a.ID, tg) {
		is(t, method, err, apperr.ErrTayfaNotFound)
	}
	v, err := e.svc.GetQuestion(ctx, victim.member, victim.tayfa, q.ID)
	if err != nil || v.Title != "Hedef soru burada duruyor" || tagNames(v) != "hedef" {
		t.Fatalf("hedef değişti: %+v %v", v, err)
	}
	list, _, err := e.svc.ListAnswers(ctx, victim.member, victim.tayfa, q.ID, 0, "")
	if err != nil || len(list) != 1 || list[0].VoteCount != 0 || list[0].Body != "Hedef cevap" {
		t.Fatalf("hedef cevap değişti: %+v %v", list, err)
	}
	// The database refuses cross-tayfa links too.
	_, err = e.pool.Exec(ctx, "INSERT INTO question_tags (question_id, tag_id, tayfa_id) VALUES ($1, $2, $3)", q.ID.PG(), tg.PG(), attacker.tayfa.PG())
	if err == nil {
		t.Fatal("başka tayfanın sorusuna etiket bağlandı")
	}
}

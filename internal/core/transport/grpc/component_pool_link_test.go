package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/poollink"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakePoolLinks struct {
	err   error
	ref   component.Ref
	ids   []id.ID
	out   poollink.Links
	calls int
}

func (f *fakePoolLinks) Get(_ context.Context, ref component.Ref) (poollink.Links, error) {
	f.ref, f.calls = ref, f.calls+1
	return f.out, f.err
}

func (f *fakePoolLinks) Set(_ context.Context, ref component.Ref, questions []id.ID) (poollink.Links, error) {
	f.ref, f.ids, f.calls = ref, questions, f.calls+1
	return f.out, f.err
}

func TestPoolLinkComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	q, ph := id.New(), id.New()
	f := &fakePoolLinks{out: poollink.Links{
		Questions: []poollink.Question{{ID: q, Body: "Soru", SubjectKey: "subject_a", Photos: []id.ID{ph}, AnswerCount: 2, CreatedAt: at}},
		MaxLinks:  50, CallerCanEdit: true,
	}}
	s := NewPoolLinkComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "pool"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "pool"}

	got, err := s.GetPoolLinks(ctx, &corev1.GetPoolLinksRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	l := got.GetLinks()
	if f.ref != want || len(l.GetQuestions()) != 1 || l.GetMaxLinks() != 50 || !l.GetCallerCanEdit() {
		t.Fatalf("okuma: %v", l)
	}
	x := l.GetQuestions()[0]
	if x.GetQuestionId() != q.String() || x.GetAuthorUserId() != "" || x.GetSubjectKey() != "subject_a" || x.GetTopicKey() != "" ||
		!slices.Equal(x.GetPhotoUploadIds(), []string{ph.String()}) || x.GetAnswerCount() != 2 || !x.GetCreateTime().AsTime().Equal(at) {
		t.Fatalf("soru: %v", x)
	}

	q2 := id.New()
	if _, err := s.SetPoolLinks(ctx, &corev1.SetPoolLinksRequest{Ref: ref, QuestionIds: []string{q2.String(), q.String()}}); err != nil {
		t.Fatal(err)
	}
	if f.ref != want || !slices.Equal(f.ids, []id.ID{q2, q}) {
		t.Fatalf("yazma: %+v %v", f.ref, f.ids)
	}
	f.err = apperr.ErrModuleDisabled
	if _, err := s.GetPoolLinks(ctx, &corev1.GetPoolLinksRequest{Ref: ref}); codeOf(t, err) != codes.FailedPrecondition {
		t.Fatalf("kapalı modül: %v", err)
	}
	f.err = apperr.ErrQuestionNotFound
	if _, err := s.SetPoolLinks(ctx, &corev1.SetPoolLinksRequest{Ref: ref}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("olmayan soru: %v", err)
	}
}

// Junk is refused before the app layer is asked.
func TestPoolLinkComponentValidation(t *testing.T) {
	f := &fakePoolLinks{}
	s := NewPoolLinkComponentServer(f, discard)
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: id.New().String(), EventId: id.New().String(), ComponentKey: "pool_link"}
	many := make([]string, poollink.MaxLinks+1)
	for i := range many {
		many[i] = id.New().String()
	}
	for name, req := range map[string]*corev1.SetPoolLinksRequest{
		"başvurusuz": {QuestionIds: []string{id.New().String()}},
		"bozuk soru": {Ref: ref, QuestionIds: []string{"soru"}},
		"51 soru":    {Ref: ref, QuestionIds: many},
	} {
		if _, err := s.SetPoolLinks(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
}

// The registry serves the pool_link component.
func TestPoolLinkComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.PoolLinkComponentService"]
	if !ok || len(info.Methods) != 2 {
		t.Fatalf("soru havuzu bağlantısı servisi: %v", info)
	}
}

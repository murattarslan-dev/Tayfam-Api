package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/draw"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDraws struct {
	err    error
	method string
	ref    component.Ref
	out    draw.Draw
	calls  int
}

func (f *fakeDraws) Get(_ context.Context, ref component.Ref) (draw.Draw, error) {
	f.method, f.ref, f.calls = "Get", ref, f.calls+1
	return f.out, f.err
}

func (f *fakeDraws) Make(_ context.Context, ref component.Ref) (draw.Draw, error) {
	f.method, f.ref, f.calls = "Make", ref, f.calls+1
	return f.out, f.err
}

func TestDrawComponentMethods(t *testing.T) {
	a, team := id.New(), id.New()
	at := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	f := &fakeDraws{out: draw.Draw{
		Drawn: true,
		Result: draw.Result{
			Subject: draw.SubjectParticipants, DrawnAt: at,
			Entries: []draw.Entry{{Position: 1, User: a}, {Position: 2}},
		},
	}}
	s := NewDrawComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "draw"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "draw"}
	check := func(method string, got *corev1.Draw, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want {
			t.Fatalf("%s: app'e giden %s %+v", method, f.method, f.ref)
		}
		en := got.GetEntries()
		if !got.GetDrawn() || got.GetSubject() != "participants" || got.GetTeamsKey() != "" || !got.GetDrawnTime().AsTime().Equal(at) ||
			got.GetCallerCanDraw() || len(en) != 2 || en[0].GetPosition() != 1 || en[0].GetUserId() != a.String() || en[0].GetTeamId() != "" ||
			en[1].GetPosition() != 2 || en[1].GetUserId() != "" || en[1].GetTeamId() != "" {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}
	got, err := s.GetDraw(ctx, &corev1.GetDrawRequest{Ref: ref})
	check("Get", got.GetDraw(), err)
	made, err := s.MakeDraw(ctx, &corev1.MakeDrawRequest{Ref: ref})
	check("Make", made.GetDraw(), err)

	// Before the draw: no time, no places; a team draw carries its key.
	f.out = draw.Draw{Result: draw.Result{Subject: draw.SubjectTeams, TeamsKey: "teams", Entries: []draw.Entry{}}, CallerCanDraw: true}
	got, err = s.GetDraw(ctx, &corev1.GetDrawRequest{Ref: ref})
	if d := got.GetDraw(); err != nil || d.GetDrawn() || d.GetDrawnTime() != nil || len(d.GetEntries()) != 0 ||
		d.GetSubject() != "teams" || d.GetTeamsKey() != "teams" || !d.GetCallerCanDraw() {
		t.Fatalf("çekilmemiş: %v %v", d, err)
	}
	f.out = draw.Draw{Drawn: true, Result: draw.Result{Subject: draw.SubjectTeams, TeamsKey: "teams", DrawnAt: at, Entries: []draw.Entry{{Position: 1, Team: team}}}}
	got, err = s.GetDraw(ctx, &corev1.GetDrawRequest{Ref: ref})
	if en := got.GetDraw().GetEntries(); err != nil || len(en) != 1 || en[0].GetTeamId() != team.String() || en[0].GetUserId() != "" {
		t.Fatalf("takım kurası: %v %v", got, err)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrDrawAlreadyMade:   {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DRAW_ALREADY_MADE},
		apperr.ErrDrawTooFew:        {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_DRAW_TOO_FEW},
		apperr.ErrEventLimit:        {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrComponentFrozen:   {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN},
		apperr.ErrPermissionDenied:  {codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
		apperr.ErrComponentNotFound: {codes.NotFound, corev1.ErrorReason_ERROR_REASON_COMPONENT_NOT_FOUND},
	} {
		f.err = appErr
		_, err := s.MakeDraw(ctx, &corev1.MakeDrawRequest{Ref: ref})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestDrawComponentValidation(t *testing.T) {
	f := &fakeDraws{}
	s := NewDrawComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	for name, bad := range map[string]*corev1.ComponentRef{
		"başvurusuz":     nil,
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "draw"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "draw"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Draw"},
	} {
		_, e1 := s.GetDraw(ctx, &corev1.GetDrawRequest{Ref: bad})
		_, e2 := s.MakeDraw(ctx, &corev1.MakeDrawRequest{Ref: bad})
		for i, err := range []error{e1, e2} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "draw"}
	if _, err := s.MakeDraw(context.Background(), &corev1.MakeDrawRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the draw component.
func TestDrawComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.DrawComponentService"]
	if !ok || len(info.Methods) != 2 {
		t.Fatalf("kura servisi: %v", info)
	}
}

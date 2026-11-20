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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeCelebrations struct {
	err     error
	ref     component.Ref
	winners []id.ID
	out     celebration.Celebration
	calls   int
	last    string
}

func (f *fakeCelebrations) Get(_ context.Context, ref component.Ref) (celebration.Celebration, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "get"
	return f.out, f.err
}

func (f *fakeCelebrations) Announce(_ context.Context, ref component.Ref, winners []id.ID) (celebration.Celebration, error) {
	f.ref, f.winners, f.calls, f.last = ref, winners, f.calls+1, "announce"
	return f.out, f.err
}

func TestCelebrationComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	a, team, guest, table := id.New(), id.New(), id.New(), id.New()
	f := &fakeCelebrations{out: celebration.Celebration{
		Result: celebration.Result{
			Source: "games", SourceKind: "scoreboard", Announced: true, AnnouncedAt: at,
			Winners: []celebration.Winner{{User: a, Table: table}, {Team: team}, {Guest: guest, Table: table}},
		},
		CallerCanAnnounce: true,
	}}
	s := NewCelebrationComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "celebration"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "celebration"}

	got, err := s.GetCelebration(ctx, &corev1.GetCelebrationRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	c := got.GetCelebration()
	if f.ref != want || c.GetSource() != "games" || c.GetSourceKind() != "scoreboard" || !c.GetAnnounced() || !c.GetCallerCanAnnounce() ||
		c.GetHidden() || !c.GetAnnounceTime().AsTime().Equal(at) || len(c.GetWinners()) != 3 {
		t.Fatalf("okuma: %v (%+v)", c, f.ref)
	}
	w := c.GetWinners()
	if w[0].GetUserId() != a.String() || w[0].GetTableId() != table.String() || w[0].GetTeamId() != "" || w[0].GetGuestId() != "" ||
		w[1].GetTeamId() != team.String() || w[1].GetUserId() != "" || w[1].GetTableId() != "" ||
		w[2].GetGuestId() != guest.String() || w[2].GetUserId() != "" {
		t.Fatalf("kazananlar: %v", w)
	}

	if _, err := s.AnnounceCelebration(ctx, &corev1.AnnounceCelebrationRequest{Ref: ref, WinnerIds: []string{a.String()}}); err != nil ||
		f.last != "announce" || len(f.winners) != 1 || f.winners[0] != a || f.ref != want {
		t.Fatalf("ilan: %v %+v", err, f)
	}
	if _, err := s.AnnounceCelebration(ctx, &corev1.AnnounceCelebrationRequest{Ref: ref}); err != nil || len(f.winners) != 0 {
		t.Fatalf("kaynaklı ilan: %v %+v", err, f)
	}

	// Not announced: no time.
	f.out = celebration.Celebration{Result: celebration.Result{Winners: []celebration.Winner{}, Hidden: true}}
	got, err = s.GetCelebration(ctx, &corev1.GetCelebrationRequest{Ref: ref})
	if c := got.GetCelebration(); err != nil || c.GetAnnounced() || c.GetAnnounceTime() != nil || !c.GetHidden() || len(c.GetWinners()) != 0 {
		t.Fatalf("ilansız: %v %v", c, err)
	}

	// The app's errors keep their reasons.
	for err, reason := range map[error]corev1.ErrorReason{
		apperr.ErrCelebrationNotStarted:     corev1.ErrorReason_ERROR_REASON_CELEBRATION_NOT_STARTED,
		apperr.ErrCelebrationNoWinner:       corev1.ErrorReason_ERROR_REASON_CELEBRATION_NO_WINNER,
		apperr.ErrCelebrationNotParticipant: corev1.ErrorReason_ERROR_REASON_CELEBRATION_NOT_PARTICIPANT,
		apperr.ErrComponentFrozen:           corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN,
	} {
		f.err = err
		_, got := s.AnnounceCelebration(ctx, &corev1.AnnounceCelebrationRequest{Ref: ref})
		if code, info := infoOf(t, got); code != codes.FailedPrecondition || info.GetReason() != reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestCelebrationComponentValidation(t *testing.T) {
	f := &fakeCelebrations{}
	s := NewCelebrationComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "celebration"}
	eleven := make([]string, 0, celebration.MaxWinners+1)
	for range celebration.MaxWinners + 1 {
		eleven = append(eleven, id.New().String())
	}

	for name, req := range map[string]*corev1.AnnounceCelebrationRequest{
		"başvurusuz":    {},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Celebration"}},
		"bozuk kazanan": {Ref: ref, WinnerIds: []string{"x"}},
		"boş kazanan":   {Ref: ref, WinnerIds: []string{""}},
		"fazla kazanan": {Ref: ref, WinnerIds: eleven},
	} {
		if _, err := s.AnnounceCelebration(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.GetCelebration(ctx, &corev1.GetCelebrationRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.AnnounceCelebration(context.Background(), &corev1.AnnounceCelebrationRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the celebration component.
func TestCelebrationComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.CelebrationComponentService"]
	if !ok || len(info.Methods) != 2 {
		t.Fatalf("kutlama servisi: %v", info)
	}
}

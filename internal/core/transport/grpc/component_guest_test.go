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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeGuests struct {
	err    error
	method string
	ref    component.Ref
	in     guest.Input
	guest  id.ID
	out    guest.Guests
	calls  int
}

func (f *fakeGuests) Get(_ context.Context, ref component.Ref) (guest.Guests, error) {
	f.method, f.ref, f.calls = "Get", ref, f.calls+1
	return f.out, f.err
}

func (f *fakeGuests) Add(_ context.Context, ref component.Ref, in guest.Input) (guest.Guests, error) {
	f.method, f.ref, f.in, f.calls = "Add", ref, in, f.calls+1
	return f.out, f.err
}

func (f *fakeGuests) Remove(_ context.Context, ref component.Ref, g id.ID) (guest.Guests, error) {
	f.method, f.ref, f.guest, f.calls = "Remove", ref, g, f.calls+1
	return f.out, f.err
}

func TestGuestPlayerMethods(t *testing.T) {
	g, table := id.New(), id.New()
	at := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	f := &fakeGuests{out: guest.Guests{
		Guests:    []guest.Guest{{ID: g, Nickname: "Ali", CreatedAt: at}, {ID: id.New(), CreatedAt: at}},
		MaxGuests: guest.MaxGuests, CallerCanAdd: true,
	}}
	s := NewGuestPlayerComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "scoreboard"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "scoreboard"}
	check := func(method string, got *corev1.GuestPlayers, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want {
			t.Fatalf("%s: app'e giden %s %+v", method, f.method, f.ref)
		}
		gs := got.GetGuests()
		if len(gs) != 2 || gs[0].GetId() != g.String() || gs[0].GetNickname() != "Ali" || !gs[0].GetCreateTime().AsTime().Equal(at) ||
			gs[1].GetNickname() != "" || got.GetMaxGuests() != 30 || !got.GetCallerCanAdd() || got.GetCallerCanRemove() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}
	list, err := s.ListGuestPlayers(ctx, &corev1.ListGuestPlayersRequest{Ref: ref})
	check("Get", list.GetGuests(), err)
	added, err := s.AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: " Ali ", TableId: table.String()})
	check("Add", added.GetGuests(), err)
	if f.in != (guest.Input{Nickname: " Ali ", Table: table}) {
		t.Fatalf("eklenen: %+v", f.in)
	}
	_, err = s.AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Veli"})
	if err != nil || f.in != (guest.Input{Nickname: "Veli"}) {
		t.Fatalf("masasız: %+v %v", f.in, err)
	}
	removed, err := s.RemoveGuestPlayer(ctx, &corev1.RemoveGuestPlayerRequest{Ref: ref, GuestId: g.String()})
	check("Remove", removed.GetGuests(), err)
	if f.guest != g {
		t.Fatalf("silinen: %v", f.guest)
	}

	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrGuestPlayersDisabled: {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYERS_DISABLED},
		apperr.ErrGuestPlayerNotFound:  {codes.NotFound, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYER_NOT_FOUND},
		apperr.ErrGuestPlayerInUse:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_GUEST_PLAYER_IN_USE},
		apperr.ErrScoreTableNotFound:   {codes.NotFound, corev1.ErrorReason_ERROR_REASON_SCORE_TABLE_NOT_FOUND},
		apperr.ErrEventLimit:           {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrPermissionDenied:     {codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
	} {
		f.err = appErr
		_, err := s.AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Ali"})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestGuestPlayerValidation(t *testing.T) {
	f := &fakeGuests{}
	s := NewGuestPlayerComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "scoreboard"}
	bad := &corev1.ComponentRef{TayfaId: "x", EventId: ev, ComponentKey: "scoreboard"}
	_, e1 := s.ListGuestPlayers(ctx, &corev1.ListGuestPlayersRequest{Ref: bad})
	_, e2 := s.AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: bad, Nickname: "Ali"})
	_, e3 := s.AddGuestPlayer(ctx, &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Ali", TableId: "x"})
	_, e4 := s.RemoveGuestPlayer(ctx, &corev1.RemoveGuestPlayerRequest{Ref: ref, GuestId: "x"})
	_, e5 := s.RemoveGuestPlayer(ctx, &corev1.RemoveGuestPlayerRequest{Ref: bad, GuestId: id.New().String()})
	for i, err := range []error{e1, e2, e3, e4, e5} {
		if codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%d: %v", i, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	if _, err := s.AddGuestPlayer(context.Background(), &corev1.AddGuestPlayerRequest{Ref: ref, Nickname: "Ali"}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// A guest's seat and draw place carry its ID; a seat input names one of
// person, team and guest.
func TestGuestPlayerInOtherComponents(t *testing.T) {
	g := id.New()
	sb := scoreboardPB(scoreboard.Scoreboard{Tables: []scoreboard.Table{{ID: id.New(), Players: []scoreboard.Player{{ID: id.New(), Position: 1, Guest: g}}}}})
	if p := sb.GetTables()[0].GetPlayers()[0]; p.GetGuestId() != g.String() || p.GetUserId() != "" || p.GetTeamId() != "" {
		t.Fatalf("masadaki misafir: %v", p)
	}
	d := drawPB(draw.Draw{Drawn: true, Result: draw.Result{Entries: []draw.Entry{{Position: 1, Guest: g}}}})
	if e := d.GetEntries()[0]; e.GetGuestId() != g.String() || e.GetUserId() != "" {
		t.Fatalf("kuradaki misafir: %v", e)
	}
	in, err := scorePlayers([]*corev1.ScorePlayerInput{{GuestId: g.String()}})
	if err != nil || len(in) != 1 || in[0] != (scoreboard.PlayerInput{Guest: g}) {
		t.Fatalf("misafir yeri: %+v %v", in, err)
	}
	for _, bad := range []*corev1.ScorePlayerInput{
		{GuestId: "x"},
		{GuestId: g.String(), UserId: id.New().String()},
		{GuestId: g.String(), TeamId: id.New().String()},
	} {
		if _, err := scorePlayers([]*corev1.ScorePlayerInput{bad}); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

// The registry serves the guest players.
func TestGuestPlayerRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.GuestPlayerComponentService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("misafir servisi: %v", info)
	}
}

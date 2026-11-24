package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/vote"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeVotes struct {
	err    error
	ref    component.Ref
	option id.ID
	label  string
	out    vote.Vote
	calls  int
	last   string
}

func (f *fakeVotes) Get(_ context.Context, ref component.Ref) (vote.Vote, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "get"
	return f.out, f.err
}

func (f *fakeVotes) Cast(_ context.Context, ref component.Ref, option id.ID) (vote.Vote, error) {
	f.ref, f.option, f.calls, f.last = ref, option, f.calls+1, "cast"
	return f.out, f.err
}

func (f *fakeVotes) Withdraw(_ context.Context, ref component.Ref) (vote.Vote, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "withdraw"
	return f.out, f.err
}

func (f *fakeVotes) AddOption(_ context.Context, ref component.Ref, label string) (vote.Vote, error) {
	f.ref, f.label, f.calls, f.last = ref, label, f.calls+1, "add"
	return f.out, f.err
}

func (f *fakeVotes) RemoveOption(_ context.Context, ref component.Ref, option id.ID) (vote.Vote, error) {
	f.ref, f.option, f.calls, f.last = ref, option, f.calls+1, "remove"
	return f.out, f.err
}

func (f *fakeVotes) Close(_ context.Context, ref component.Ref) (vote.Vote, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "close"
	return f.out, f.err
}

func TestVoteComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	car, owner, photo, upload, mine := id.New(), id.New(), id.New(), id.New(), id.New()
	f := &fakeVotes{out: vote.Vote{
		Source: vote.SourceVehicles, VoterCount: 3, CallerOption: mine, Closed: true, ClosedAt: at, ResultsVisible: true,
		CallerCanVote: true, CallerCanEditOptions: true, CallerCanClose: true, MaxOptions: vote.MaxOptions,
		Options: []vote.Option{
			{ID: car, User: owner, Vehicle: &vote.Vehicle{Make: "Ford", Model: "Mustang", ModelYear: 1967, Color: "Kırmızı"}, VoteCount: 2, Winner: true},
			{ID: photo, User: owner, Upload: upload, VoteCount: 1},
			{ID: mine, Label: "Serbest"},
		},
	}}
	s := NewVoteComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "vote"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "vote"}

	got, err := s.GetVote(ctx, &corev1.GetVoteRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	v := got.GetVote()
	if f.ref != want || v.GetSource() != "vehicles" || v.GetVoterCount() != 3 || v.GetCallerOptionId() != mine.String() || !v.GetClosed() ||
		!v.GetCloseTime().AsTime().Equal(at) || !v.GetResultsVisible() || !v.GetCallerCanVote() || !v.GetCallerCanEditOptions() ||
		!v.GetCallerCanClose() || v.GetMaxOptions() != vote.MaxOptions || len(v.GetOptions()) != 3 {
		t.Fatalf("okuma: %v (%+v)", v, f.ref)
	}
	o := v.GetOptions()
	if o[0].GetId() != car.String() || o[0].GetUserId() != owner.String() || o[0].GetVehicle().GetMake() != "Ford" ||
		o[0].GetVehicle().GetModelYear() != 1967 || o[0].GetVoteCount() != 2 || !o[0].GetWinner() ||
		o[1].GetUploadId() != upload.String() || o[1].GetVehicle() != nil || o[1].GetVoteCount() != 1 ||
		o[2].GetLabel() != "Serbest" || o[2].GetUserId() != "" || o[2].GetUploadId() != "" {
		t.Fatalf("seçenekler: %v", o)
	}

	// Counts never leave while hidden, whatever the app layer holds.
	f.out = vote.Vote{Source: vote.SourceFree, Options: []vote.Option{{ID: mine, Label: "Bir", VoteCount: 4, Winner: true}}}
	got, err = s.GetVote(ctx, &corev1.GetVoteRequest{Ref: ref})
	if v := got.GetVote(); err != nil || v.GetClosed() || v.GetCloseTime() != nil || v.GetCallerOptionId() != "" ||
		v.GetOptions()[0].GetVoteCount() != 0 || v.GetOptions()[0].GetWinner() {
		t.Fatalf("gizli: %v %v", v, err)
	}

	if _, err := s.CastVote(ctx, &corev1.CastVoteRequest{Ref: ref, OptionId: car.String()}); err != nil || f.last != "cast" || f.option != car || f.ref != want {
		t.Fatalf("oy: %v %+v", err, f)
	}
	if _, err := s.WithdrawVote(ctx, &corev1.WithdrawVoteRequest{Ref: ref}); err != nil || f.last != "withdraw" {
		t.Fatalf("geri alma: %v %+v", err, f)
	}
	if _, err := s.AddVoteOption(ctx, &corev1.AddVoteOptionRequest{Ref: ref, Label: "Suç ve Ceza"}); err != nil || f.last != "add" || f.label != "Suç ve Ceza" {
		t.Fatalf("seçenek: %v %+v", err, f)
	}
	if _, err := s.RemoveVoteOption(ctx, &corev1.RemoveVoteOptionRequest{Ref: ref, OptionId: mine.String()}); err != nil || f.last != "remove" || f.option != mine {
		t.Fatalf("seçenek kaldırma: %v %+v", err, f)
	}
	if _, err := s.CloseVote(ctx, &corev1.CloseVoteRequest{Ref: ref}); err != nil || f.last != "close" {
		t.Fatalf("kapatma: %v %+v", err, f)
	}

	// The app's errors keep their reasons.
	for err, exp := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrVoteClosed:        {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOTE_CLOSED},
		apperr.ErrVoteOwnOption:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOTE_OWN_OPTION},
		apperr.ErrVoteOptionsFixed:  {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOTE_OPTIONS_FIXED},
		apperr.ErrVoteOptionExists:  {codes.AlreadyExists, corev1.ErrorReason_ERROR_REASON_VOTE_OPTION_EXISTS},
		apperr.ErrEventNotGoing:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING},
		apperr.ErrEventItemNotFound: {codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
	} {
		f.err = err
		_, got := s.CastVote(ctx, &corev1.CastVoteRequest{Ref: ref, OptionId: car.String()})
		if code, info := infoOf(t, got); code != exp.code || info.GetReason() != exp.reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestVoteComponentValidation(t *testing.T) {
	f := &fakeVotes{}
	s := NewVoteComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "vote"}

	for name, req := range map[string]*corev1.CastVoteRequest{
		"başvurusuz":    {OptionId: id.New().String()},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Vote"}, OptionId: id.New().String()},
		"bozuk seçenek": {Ref: ref, OptionId: "x"},
		"boş seçenek":   {Ref: ref},
	} {
		if _, err := s.CastVote(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.RemoveVoteOption(ctx, &corev1.RemoveVoteOptionRequest{Ref: ref, OptionId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("bozuk seçenek kaldırma: %v", err)
	}
	if _, err := s.AddVoteOption(ctx, &corev1.AddVoteOptionRequest{Ref: ref, Label: strings.Repeat("a", maxVoteLabelBytes+1)}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("uzun seçenek: %v", err)
	}
	if _, err := s.GetVote(ctx, &corev1.GetVoteRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.CloseVote(context.Background(), &corev1.CloseVoteRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the vote component.
func TestVoteComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.VoteComponentService"]
	if !ok || len(info.Methods) != 6 {
		t.Fatalf("oylama servisi: %v", info)
	}
}

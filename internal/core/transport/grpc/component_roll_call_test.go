package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/rollcall"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeRollCalls struct {
	err    error
	ref    component.Ref
	at     time.Time
	method string
	out    rollcall.RollCall
	calls  int
}

func (f *fakeRollCalls) rec(ref component.Ref, at time.Time, method string) (rollcall.RollCall, error) {
	f.ref, f.at, f.method, f.calls = ref, at, method, f.calls+1
	return f.out, f.err
}

func (f *fakeRollCalls) Get(_ context.Context, ref component.Ref) (rollcall.RollCall, error) {
	return f.rec(ref, time.Time{}, "get")
}

func (f *fakeRollCalls) SetExpectedReturn(_ context.Context, ref component.Ref, at time.Time) (rollcall.RollCall, error) {
	return f.rec(ref, at, "plan")
}

func (f *fakeRollCalls) Start(_ context.Context, ref component.Ref, at time.Time) (rollcall.RollCall, error) {
	return f.rec(ref, at, "start")
}

func (f *fakeRollCalls) Return(_ context.Context, ref component.Ref, at time.Time) (rollcall.RollCall, error) {
	return f.rec(ref, at, "return")
}

func TestRollCallComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	walker, other := id.New(), id.New()
	f := &fakeRollCalls{out: rollcall.RollCall{
		Alarm: true, ReturnGraceMinutes: 30, ExpectedReturnAt: at.Add(6 * time.Hour),
		Mine:            &rollcall.Entry{User: walker, StartedAt: at},
		Entries:         []rollcall.Entry{{User: walker, StartedAt: at}, {User: other, StartedAt: at, ReturnedAt: at.Add(time.Hour)}},
		CallerCanManage: true, CallerCanEdit: true,
	}}
	s := NewRollCallComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "roll_call"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "roll_call"}

	got, err := s.GetRollCall(ctx, &corev1.GetRollCallRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	r := got.GetRollCall()
	if f.ref != want || !r.GetAlarm() || r.GetReturnGraceMinutes() != 30 || !r.GetExpectedReturnTime().AsTime().Equal(at.Add(6*time.Hour)) ||
		r.GetMine().GetUserId() != walker.String() || r.GetMine().GetReturnTime() != nil || len(r.GetEntries()) != 2 ||
		!r.GetEntries()[1].GetReturnTime().AsTime().Equal(at.Add(time.Hour)) || !r.GetCallerCanManage() || !r.GetCallerCanEdit() {
		t.Fatalf("okuma: %v (%+v)", r, f.ref)
	}

	// Times reach the app as given; unset is zero (now, or no plan).
	for name, call := range map[string]func(*timestamppb.Timestamp) error{
		"plan": func(ts *timestamppb.Timestamp) error {
			_, err := s.SetExpectedReturn(ctx, &corev1.SetExpectedReturnRequest{Ref: ref, ExpectedReturnTime: ts})
			return err
		},
		"start": func(ts *timestamppb.Timestamp) error {
			_, err := s.StartRollCall(ctx, &corev1.StartRollCallRequest{Ref: ref, StartTime: ts})
			return err
		},
		"return": func(ts *timestamppb.Timestamp) error {
			_, err := s.ReturnRollCall(ctx, &corev1.ReturnRollCallRequest{Ref: ref, ReturnTime: ts})
			return err
		},
	} {
		if err := call(timestamppb.New(at)); err != nil || f.method != name || f.ref != want || !f.at.Equal(at) {
			t.Fatalf("%s: %v %s %v", name, err, f.method, f.at)
		}
		if err := call(nil); err != nil || f.method != name || !f.at.IsZero() {
			t.Fatalf("%s zamansız: %v %v", name, err, f.at)
		}
	}
	// Without a plan there is no expected return; others' entries are empty.
	f.out = rollcall.RollCall{ReturnGraceMinutes: 30}
	got, err = s.GetRollCall(ctx, &corev1.GetRollCallRequest{Ref: ref})
	if err != nil || got.GetRollCall().GetExpectedReturnTime() != nil || got.GetRollCall().GetMine() != nil || got.GetRollCall().GetEntries() == nil {
		t.Fatalf("plansız: %v %v", got, err)
	}

	// The app's errors keep their reasons.
	f.err = apperr.ErrHikeNotStarted
	if _, err := s.ReturnRollCall(ctx, &corev1.ReturnRollCallRequest{Ref: ref}); codeOf(t, err) != codes.FailedPrecondition {
		t.Fatalf("çıkmadan dönüş: %v", err)
	}
	f.err = apperr.ErrComponentNotFound
	if _, err := s.GetRollCall(ctx, &corev1.GetRollCallRequest{Ref: ref}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("olmayan bileşen: %v", err)
	}
}

// Junk is refused before the app layer is asked.
func TestRollCallComponentValidation(t *testing.T) {
	f := &fakeRollCalls{}
	s := NewRollCallComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "roll_call"}
	bad := &timestamppb.Timestamp{Seconds: 1, Nanos: -1}

	for name, call := range map[string]func() error{
		"başvurusuz okuma": func() error { _, err := s.GetRollCall(ctx, &corev1.GetRollCallRequest{}); return err },
		"bozuk anahtar": func() error {
			_, err := s.GetRollCall(ctx, &corev1.GetRollCallRequest{Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Roll"}})
			return err
		},
		"bozuk etkinlik": func() error {
			_, err := s.StartRollCall(ctx, &corev1.StartRollCallRequest{Ref: &corev1.ComponentRef{TayfaId: tf, EventId: "x", ComponentKey: "roll_call"}})
			return err
		},
		"bozuk beklenen dönüş": func() error {
			_, err := s.SetExpectedReturn(ctx, &corev1.SetExpectedReturnRequest{Ref: ref, ExpectedReturnTime: bad})
			return err
		},
		"bozuk çıkış": func() error {
			_, err := s.StartRollCall(ctx, &corev1.StartRollCallRequest{Ref: ref, StartTime: bad})
			return err
		},
		"bozuk dönüş": func() error {
			_, err := s.ReturnRollCall(ctx, &corev1.ReturnRollCallRequest{Ref: ref, ReturnTime: bad})
			return err
		},
	} {
		if err := call(); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.ReturnRollCall(context.Background(), &corev1.ReturnRollCallRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the roll call component.
func TestRollCallComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.RollCallComponentService"]
	if !ok || len(info.Methods) != 4 {
		t.Fatalf("yoklama servisi: %v", info)
	}
}

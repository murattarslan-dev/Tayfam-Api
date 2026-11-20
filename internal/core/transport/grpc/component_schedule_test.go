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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/schedule"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeSchedule struct {
	err    error
	method string
	ref    component.Ref
	in     schedule.Input
	item   id.ID
	out    schedule.Schedule
	calls  int
}

func (f *fakeSchedule) rec(method string, ref component.Ref, item id.ID, in schedule.Input) (schedule.Schedule, error) {
	f.method, f.ref, f.item, f.in, f.calls = method, ref, item, in, f.calls+1
	return f.out, f.err
}

func (f *fakeSchedule) Get(_ context.Context, ref component.Ref) (schedule.Schedule, error) {
	return f.rec("Get", ref, id.ID{}, schedule.Input{})
}

func (f *fakeSchedule) Add(_ context.Context, ref component.Ref, in schedule.Input) (schedule.Schedule, error) {
	return f.rec("Add", ref, id.ID{}, in)
}

func (f *fakeSchedule) Update(_ context.Context, ref component.Ref, item id.ID, in schedule.Input) (schedule.Schedule, error) {
	return f.rec("Update", ref, item, in)
}

func (f *fakeSchedule) Remove(_ context.Context, ref component.Ref, item id.ID) (schedule.Schedule, error) {
	return f.rec("Remove", ref, item, schedule.Input{})
}

func TestScheduleComponentMethods(t *testing.T) {
	start := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	talk, lunch := id.New(), id.New()
	f := &fakeSchedule{out: schedule.Schedule{
		Items: []schedule.Item{
			{ID: talk, StartsAt: start, EndsAt: start.Add(time.Hour), Title: "Söyleşi", Note: "Salon B"},
			{ID: lunch, StartsAt: start.Add(2 * time.Hour), Title: "Öğle yemeği"},
		},
		MaxItems: 30, CallerCanEdit: true,
	}}
	s := NewScheduleComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "schedule"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "schedule"}
	check := func(method string, got *corev1.Schedule, err error, item id.ID) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want || f.item != item {
			t.Fatalf("%s: app'e giden %s %+v %s", method, f.method, f.ref, f.item)
		}
		it := got.GetItems()
		if len(it) != 2 || it[0].GetId() != talk.String() || !it[0].GetStartTime().AsTime().Equal(start) ||
			!it[0].GetEndTime().AsTime().Equal(start.Add(time.Hour)) || it[0].GetTitle() != "Söyleşi" || it[0].GetNote() != "Salon B" ||
			it[1].GetEndTime() != nil || it[1].GetNote() != "" || got.GetMaxItems() != 30 || !got.GetCallerCanEdit() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}
	input := &corev1.ScheduleItemInput{
		StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)), Title: " Söyleşi ", Note: "Salon B",
	}
	wantIn := schedule.Input{StartsAt: start, EndsAt: start.Add(time.Hour), Title: " Söyleşi ", Note: "Salon B"}

	got, err := s.GetSchedule(ctx, &corev1.GetScheduleRequest{Ref: ref})
	check("Get", got.GetSchedule(), err, id.ID{})
	added, err := s.AddScheduleItem(ctx, &corev1.AddScheduleItemRequest{Ref: ref, Item: input})
	check("Add", added.GetSchedule(), err, id.ID{})
	// The values reach the app layer as sent; it cleans them.
	if !f.in.StartsAt.Equal(wantIn.StartsAt) || !f.in.EndsAt.Equal(wantIn.EndsAt) || f.in.Title != wantIn.Title || f.in.Note != wantIn.Note {
		t.Fatalf("madde girdisi: %+v", f.in)
	}
	updated, err := s.UpdateScheduleItem(ctx, &corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: talk.String(),
		Item: &corev1.ScheduleItemInput{StartTime: timestamppb.New(start), Title: "Söyleşi"}})
	check("Update", updated.GetSchedule(), err, talk)
	if !f.in.EndsAt.IsZero() || f.in.Note != "" {
		t.Fatalf("bitişsiz madde: %+v", f.in)
	}
	removed, err := s.RemoveScheduleItem(ctx, &corev1.RemoveScheduleItemRequest{Ref: ref, ItemId: talk.String()})
	check("Remove", removed.GetSchedule(), err, talk)

	// An empty programme: an empty list, not nil fields that break a client.
	f.out = schedule.Schedule{Items: []schedule.Item{}, MaxItems: 100}
	if got, err := s.GetSchedule(ctx, &corev1.GetScheduleRequest{Ref: ref}); err != nil || len(got.GetSchedule().GetItems()) != 0 || got.GetSchedule().GetMaxItems() != 100 {
		t.Fatalf("boş program: %v %v", got, err)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrEventLimit:          {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrEventItemNotFound:   {codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
		apperr.ErrComponentFrozen:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN},
		apperr.Validation("end_time"): {codes.InvalidArgument, corev1.ErrorReason_ERROR_REASON_VALIDATION},
	} {
		f.err = appErr
		_, err := s.UpdateScheduleItem(ctx, &corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: talk.String(), Item: input})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestScheduleComponentValidation(t *testing.T) {
	f := &fakeSchedule{}
	s := NewScheduleComponentServer(f, discard)
	tf, ev, good := id.New().String(), id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "schedule"}
	in := &corev1.ScheduleItemInput{StartTime: timestamppb.Now(), Title: "Varış"}
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}

	for name, bad := range map[string]*corev1.ComponentRef{
		"başvurusuz":     nil,
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "schedule"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "schedule"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Schedule"},
	} {
		_, e1 := s.GetSchedule(ctx, &corev1.GetScheduleRequest{Ref: bad})
		_, e2 := s.AddScheduleItem(ctx, &corev1.AddScheduleItemRequest{Ref: bad, Item: in})
		_, e3 := s.UpdateScheduleItem(ctx, &corev1.UpdateScheduleItemRequest{Ref: bad, ItemId: good, Item: in})
		_, e4 := s.RemoveScheduleItem(ctx, &corev1.RemoveScheduleItemRequest{Ref: bad, ItemId: good})
		for i, err := range []error{e1, e2, e3, e4} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	for name, tc := range map[string]struct {
		in    *corev1.ScheduleItemInput
		field string
	}{
		"girdisiz":    {nil, "start_time"},
		"saatsiz":     {&corev1.ScheduleItemInput{Title: "Varış"}, "start_time"},
		"bozuk saat":  {&corev1.ScheduleItemInput{StartTime: &timestamppb.Timestamp{Nanos: -1}, Title: "Varış"}, "start_time"},
		"bozuk bitiş": {&corev1.ScheduleItemInput{StartTime: timestamppb.Now(), EndTime: &timestamppb.Timestamp{Seconds: -1 << 62}, Title: "Varış"}, "end_time"},
	} {
		_, err := s.AddScheduleItem(ctx, &corev1.AddScheduleItemRequest{Ref: ref, Item: tc.in})
		if got := field(err); got != tc.field {
			t.Errorf("%s: %q", name, got)
		}
	}
	_, err := s.UpdateScheduleItem(ctx, &corev1.UpdateScheduleItemRequest{Ref: ref, ItemId: "x", Item: in})
	if field(err) != "item_id" {
		t.Error("bozuk madde kimliği")
	}
	_, err = s.RemoveScheduleItem(ctx, &corev1.RemoveScheduleItemRequest{Ref: ref})
	if field(err) != "item_id" {
		t.Error("maddesiz silme")
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.AddScheduleItem(context.Background(), &corev1.AddScheduleItemRequest{Ref: ref, Item: in}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the schedule component.
func TestScheduleComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.ScheduleComponentService"]
	if !ok || len(info.Methods) != 4 {
		t.Fatalf("program servisi: %v", info)
	}
}

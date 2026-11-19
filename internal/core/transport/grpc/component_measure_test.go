package grpc

import (
	"context"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/measure"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeMeasures struct {
	err   error
	ref   component.Ref
	user  id.ID
	value float64
	out   measure.Measure
	calls int
	last  string
}

func (f *fakeMeasures) Get(_ context.Context, ref component.Ref) (measure.Measure, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "get"
	return f.out, f.err
}

func (f *fakeMeasures) SetTotal(_ context.Context, ref component.Ref, value float64) (measure.Measure, error) {
	f.ref, f.value, f.calls, f.last = ref, value, f.calls+1, "set-total"
	return f.out, f.err
}

func (f *fakeMeasures) RemoveTotal(_ context.Context, ref component.Ref) (measure.Measure, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "remove-total"
	return f.out, f.err
}

func (f *fakeMeasures) SetEntry(_ context.Context, ref component.Ref, user id.ID, value float64) (measure.Measure, error) {
	f.ref, f.user, f.value, f.calls, f.last = ref, user, value, f.calls+1, "set-entry"
	return f.out, f.err
}

func (f *fakeMeasures) RemoveEntry(_ context.Context, ref component.Ref, user id.ID) (measure.Measure, error) {
	f.ref, f.user, f.calls, f.last = ref, user, f.calls+1, "remove-entry"
	return f.out, f.err
}

func TestMeasureComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	total, suggested := 12.5, 13.0
	player := id.New()
	f := &fakeMeasures{out: measure.Measure{
		Unit: "km", Scope: measure.ScopeTotal, EnteredBy: measure.EnteredByOrganizer, Min: 0, Max: 1000, Decimals: 2,
		Total: &total, TotalUpdatedAt: at, Entries: []measure.Entry{}, Suggested: &suggested, CallerCanEdit: true,
	}}
	s := NewMeasureComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "distance"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "distance"}

	got, err := s.GetMeasure(ctx, &corev1.GetMeasureRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	m := got.GetMeasure()
	if f.ref != want || m.GetUnit() != "km" || m.GetScope() != corev1.MeasureScope_MEASURE_SCOPE_TOTAL ||
		m.GetEnteredBy() != corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_ORGANIZER || m.GetMax() != 1000 || m.GetDecimals() != 2 ||
		m.Total == nil || m.GetTotal() != 12.5 || !m.GetTotalUpdateTime().AsTime().Equal(at) || m.GetSuggested() != 13 || !m.GetCallerCanEdit() {
		t.Fatalf("okuma: %v (%+v)", m, f.ref)
	}

	if _, err := s.SetMeasureTotal(ctx, &corev1.SetMeasureTotalRequest{Ref: ref, Value: 7.25}); err != nil || f.last != "set-total" || f.value != 7.25 || f.ref != want {
		t.Fatalf("toplam yazma: %v %+v", err, f)
	}
	if _, err := s.RemoveMeasureTotal(ctx, &corev1.RemoveMeasureTotalRequest{Ref: ref}); err != nil || f.last != "remove-total" {
		t.Fatalf("toplam kaldırma: %v %+v", err, f)
	}

	// A personal measure: entries, no total, no suggestion.
	f.out = measure.Measure{
		Unit: "goal", Scope: measure.ScopePersonal, EnteredBy: measure.EnteredBySelf, Max: 50,
		Entries: []measure.Entry{{User: player, Value: 2, UpdatedAt: at}},
	}
	set, err := s.SetMeasureEntry(ctx, &corev1.SetMeasureEntryRequest{Ref: ref, UserId: player.String(), Value: 2})
	if err != nil || f.last != "set-entry" || f.user != player || f.value != 2 {
		t.Fatalf("kişisel yazma: %v %+v", err, f)
	}
	if m := set.GetMeasure(); m.Total != nil || m.Suggested != nil || m.TotalUpdateTime != nil || len(m.GetEntries()) != 1 ||
		m.GetEntries()[0].GetUserId() != player.String() || m.GetEntries()[0].GetValue() != 2 ||
		m.GetScope() != corev1.MeasureScope_MEASURE_SCOPE_PERSONAL || m.GetEnteredBy() != corev1.MeasureEnteredBy_MEASURE_ENTERED_BY_SELF {
		t.Fatalf("kişisel yanıt: %v", m)
	}
	if _, err := s.RemoveMeasureEntry(ctx, &corev1.RemoveMeasureEntryRequest{Ref: ref, UserId: player.String()}); err != nil || f.last != "remove-entry" || f.user != player {
		t.Fatalf("kişisel kaldırma: %v %+v", err, f)
	}

	// The app's errors keep their reasons.
	for err, reason := range map[error]corev1.ErrorReason{
		apperr.ErrMeasurementNotStarted:     corev1.ErrorReason_ERROR_REASON_MEASUREMENT_NOT_STARTED,
		apperr.ErrMeasurementNotParticipant: corev1.ErrorReason_ERROR_REASON_MEASUREMENT_NOT_PARTICIPANT,
		apperr.ErrMeasurementScope:          corev1.ErrorReason_ERROR_REASON_MEASUREMENT_SCOPE,
		apperr.ErrNotAttended:               corev1.ErrorReason_ERROR_REASON_NOT_ATTENDED,
		apperr.ErrComponentFrozen:           corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN,
	} {
		f.err = err
		_, got := s.SetMeasureEntry(ctx, &corev1.SetMeasureEntryRequest{Ref: ref, UserId: player.String(), Value: 1})
		if code, info := infoOf(t, got); code != codes.FailedPrecondition || info.GetReason() != reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestMeasureComponentValidation(t *testing.T) {
	f := &fakeMeasures{}
	s := NewMeasureComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "goals"}
	u := id.New().String()

	for name, req := range map[string]*corev1.SetMeasureEntryRequest{
		"başvurusuz":    {UserId: u, Value: 1},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Goals"}, UserId: u, Value: 1},
		"kişisiz":       {Ref: ref, Value: 1},
		"bozuk kişi":    {Ref: ref, UserId: "x", Value: 1},
		"NaN":           {Ref: ref, UserId: u, Value: math.NaN()},
		"sonsuz":        {Ref: ref, UserId: u, Value: math.Inf(1)},
	} {
		if _, err := s.SetMeasureEntry(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]*corev1.SetMeasureTotalRequest{
		"başvurusuz":   {Value: 1},
		"bozuk tayfa":  {Ref: &corev1.ComponentRef{TayfaId: "x", EventId: ev, ComponentKey: "goals"}, Value: 1},
		"eksi sonsuz":  {Ref: ref, Value: math.Inf(-1)},
		"NaN toplam":   {Ref: ref, Value: math.NaN()},
		"bozuk etkinl": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: "x", ComponentKey: "goals"}, Value: 1},
	} {
		if _, err := s.SetMeasureTotal(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.RemoveMeasureEntry(ctx, &corev1.RemoveMeasureEntryRequest{Ref: ref}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("kişisiz kaldırma: %v", err)
	}
	if _, err := s.GetMeasure(ctx, &corev1.GetMeasureRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.SetMeasureTotal(context.Background(), &corev1.SetMeasureTotalRequest{Ref: ref, Value: 1}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the measure component.
func TestMeasureComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.MeasureComponentService"]
	if !ok || len(info.Methods) != 5 {
		t.Fatalf("ölçüm servisi: %v", info)
	}
}

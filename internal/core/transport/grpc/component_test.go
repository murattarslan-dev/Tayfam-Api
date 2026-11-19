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
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeComponents struct {
	err                error
	user, tayfa, event id.ID
	sel                component.Selection
	in                 component.RefereeInput
	removed            id.ID
	calls              []string
}

func (f *fakeComponents) note(what string, user, tayfa, event id.ID) error {
	f.calls = append(f.calls, what)
	f.user, f.tayfa, f.event = user, tayfa, event
	return f.err
}

func (f *fakeComponents) states() component.States {
	return component.States{
		Locked: true, CallerCanManage: true,
		Components: []component.State{
			{Key: "draw", Kind: "draw", Enabled: true, Choices: []component.Choice{}},
			{Key: "bracket", Kind: "bracket", Optional: true, Choices: []component.Choice{
				{Param: "format", Value: "swiss", Options: []string{"elimination", "swiss"}},
			}},
		},
	}
}

func (f *fakeComponents) List(_ context.Context, user, tayfa, event id.ID) (component.States, error) {
	return f.states(), f.note("list", user, tayfa, event)
}

func (f *fakeComponents) Set(_ context.Context, user, tayfa, event id.ID, sel component.Selection) (component.States, error) {
	f.sel = sel
	return f.states(), f.note("set", user, tayfa, event)
}

var refereeTime = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func (f *fakeComponents) ListReferees(_ context.Context, user, tayfa, event id.ID) ([]component.Referee, error) {
	return []component.Referee{{ID: f.removed, User: user, CreatedAt: refereeTime}}, f.note("referees", user, tayfa, event)
}

func (f *fakeComponents) AssignReferee(_ context.Context, user, tayfa, event id.ID, in component.RefereeInput) (component.Referee, error) {
	f.in = in
	return component.Referee{ID: id.New(), User: in.User, ComponentKey: in.ComponentKey, Scope: in.Scope, CreatedAt: refereeTime},
		f.note("assign", user, tayfa, event)
}

func (f *fakeComponents) RemoveReferee(_ context.Context, user, tayfa, event, referee id.ID) error {
	f.removed = referee
	return f.note("remove", user, tayfa, event)
}

func (f *fakeComponents) CheckAccess(_ context.Context, user, tayfa, event id.ID) error {
	return f.note("access", user, tayfa, event)
}

func TestEventComponentMethods(t *testing.T) {
	f := &fakeComponents{}
	s := NewEventComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)

	list, err := s.ListEventComponents(ctx, &corev1.ListEventComponentsRequest{TayfaId: tf.String(), EventId: ev.String()})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != tf || f.event != ev || !list.GetLocked() || !list.GetCallerCanManage() || len(list.GetComponents()) != 2 {
		t.Fatalf("liste: %v (%v %v %v)", list, f.user, f.tayfa, f.event)
	}
	b := list.GetComponents()[1]
	if b.GetKey() != "bracket" || b.GetKind() != "bracket" || !b.GetOptional() || b.GetEnabled() || len(b.GetChoices()) != 1 ||
		b.GetChoices()[0].GetParam() != "format" || b.GetChoices()[0].GetValue() != "swiss" ||
		!slices.Equal(b.GetChoices()[0].GetOptions(), []string{"elimination", "swiss"}) {
		t.Fatalf("bileşen: %v", b)
	}

	off := false
	set, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf.String(), EventId: ev.String(),
		Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Enabled: &off, Choices: map[string]string{"format": "league", "best_of": "3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if f.sel.Key != "bracket" || f.sel.Enabled == nil || *f.sel.Enabled || len(f.sel.Choices) != 2 || f.sel.Choices["best_of"] != "3" || len(set.GetComponents()) != 2 {
		t.Fatalf("seçim: %+v", f.sel)
	}
	// An unset switch stays unset.
	if _, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf.String(), EventId: ev.String(),
		Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Choices: map[string]string{"format": "swiss"}}}); err != nil || f.sel.Enabled != nil {
		t.Fatalf("gönderilmeyen enabled: %+v %v", f.sel, err)
	}

	target, scopeID := id.New(), id.New()
	as, err := s.AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{TayfaId: tf.String(), EventId: ev.String(),
		UserId: target.String(), ComponentKey: "bracket", ScopeId: scopeID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if f.in != (component.RefereeInput{User: target, ComponentKey: "bracket", Scope: scopeID}) {
		t.Fatalf("atama girdisi: %+v", f.in)
	}
	if r := as.GetReferee(); r.GetUserId() != target.String() || r.GetComponentKey() != "bracket" || r.GetScopeId() != scopeID.String() ||
		!r.GetCreateTime().AsTime().Equal(refereeTime) || r.GetId() == "" {
		t.Fatalf("atama: %v", r)
	}
	// The whole event: no component, no scope.
	as, err = s.AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{TayfaId: tf.String(), EventId: ev.String(), UserId: target.String()})
	if err != nil || as.GetReferee().GetScopeId() != "" || as.GetReferee().GetComponentKey() != "" || f.in.Scope != (id.ID{}) {
		t.Fatalf("etkinlik hakemi: %v %v", as, err)
	}

	ref := id.New()
	if _, err := s.RemoveEventReferee(ctx, &corev1.RemoveEventRefereeRequest{TayfaId: tf.String(), EventId: ev.String(), RefereeId: ref.String()}); err != nil || f.removed != ref {
		t.Fatalf("kaldırma: %v %v", f.removed, err)
	}
	refs, err := s.ListEventReferees(ctx, &corev1.ListEventRefereesRequest{TayfaId: tf.String(), EventId: ev.String()})
	if err != nil || len(refs.GetReferees()) != 1 || refs.GetReferees()[0].GetId() != ref.String() || refs.GetReferees()[0].GetUserId() != user.String() {
		t.Fatalf("hakemler: %v %v", refs, err)
	}
	if _, err := s.CheckEventAccess(ctx, &corev1.CheckEventAccessRequest{TayfaId: tf.String(), EventId: ev.String()}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "set", "set", "assign", "assign", "remove", "referees", "access"}; !slices.Equal(f.calls, want) {
		t.Fatalf("çağrılar: %v", f.calls)
	}
}

func TestEventComponentValidation(t *testing.T) {
	f := &fakeComponents{}
	s := NewEventComponentServer(f, discard)
	ctx := callerCtx(id.New())
	tf, ev, u := id.New().String(), id.New().String(), id.New().String()
	long := make([]byte, 41)
	for i := range long {
		long[i] = 'a'
	}
	many := map[string]string{}
	for i := range 21 {
		many["param_"+string(rune('a'+i))] = "x"
	}
	for name, call := range map[string]func() error{
		"tayfa": func() error {
			_, err := s.ListEventComponents(ctx, &corev1.ListEventComponentsRequest{TayfaId: "x", EventId: ev})
			return err
		},
		"etkinlik": func() error {
			_, err := s.ListEventReferees(ctx, &corev1.ListEventRefereesRequest{TayfaId: tf, EventId: "x"})
			return err
		},
		"seçim yok": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev})
			return err
		},
		"bozuk anahtar": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev, Selection: &corev1.ComponentSelection{ComponentKey: "Bracket"}})
			return err
		},
		"bozuk parametre": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev,
				Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Choices: map[string]string{"Format": "swiss"}}})
			return err
		},
		"boş değer": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev,
				Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Choices: map[string]string{"format": ""}}})
			return err
		},
		"uzun değer": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev,
				Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Choices: map[string]string{"format": string(long)}}})
			return err
		},
		"çok seçim": func() error {
			_, err := s.SetEventComponent(ctx, &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev,
				Selection: &corev1.ComponentSelection{ComponentKey: "bracket", Choices: many}})
			return err
		},
		"hakem kullanıcısı": func() error {
			_, err := s.AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{TayfaId: tf, EventId: ev, UserId: "x"})
			return err
		},
		"hakem bileşeni": func() error {
			_, err := s.AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{TayfaId: tf, EventId: ev, UserId: u, ComponentKey: "X"})
			return err
		},
		"hakem kapsamı": func() error {
			_, err := s.AssignEventReferee(ctx, &corev1.AssignEventRefereeRequest{TayfaId: tf, EventId: ev, UserId: u, ComponentKey: "draw", ScopeId: "x"})
			return err
		},
		"hakem kimliği": func() error {
			_, err := s.RemoveEventReferee(ctx, &corev1.RemoveEventRefereeRequest{TayfaId: tf, EventId: ev, RefereeId: "x"})
			return err
		},
		"erişim etkinliği": func() error {
			_, err := s.CheckEventAccess(ctx, &corev1.CheckEventAccessRequest{TayfaId: tf, EventId: ""})
			return err
		},
	} {
		code, info := infoOf(t, call())
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Errorf("%s: %v %v", name, code, info)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %v", f.calls)
	}
}

func TestEventComponentErrorsAndCaller(t *testing.T) {
	tf, ev := id.New().String(), id.New().String()
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason corev1.ErrorReason
	}{
		{apperr.ErrComponentNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_COMPONENT_NOT_FOUND},
		{apperr.ErrComponentDisabled, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_DISABLED},
		{apperr.ErrComponentLocked, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_LOCKED},
		{apperr.ErrComponentFrozen, codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN},
		{apperr.ErrEventNotFound, codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND},
		{apperr.ErrMuted, codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_MUTED},
	} {
		s := NewEventComponentServer(&fakeComponents{err: tc.err}, discard)
		_, err := s.SetEventComponent(callerCtx(id.New()), &corev1.SetEventComponentRequest{TayfaId: tf, EventId: ev,
			Selection: &corev1.ComponentSelection{ComponentKey: "bracket"}})
		if code, info := infoOf(t, err); code != tc.code || info.GetReason() != tc.reason.String() {
			t.Errorf("%v: %v %v", tc.err, code, info)
		}
		_, err = s.CheckEventAccess(callerCtx(id.New()), &corev1.CheckEventAccessRequest{TayfaId: tf, EventId: ev})
		if code, _ := infoOf(t, err); code != tc.code {
			t.Errorf("erişim %v: %v", tc.err, code)
		}
	}
	// No verified caller: nothing reaches the app layer.
	f := &fakeComponents{}
	s := NewEventComponentServer(f, discard)
	bare := context.Background()
	for name, call := range map[string]func() error{
		"liste": func() error {
			_, err := s.ListEventComponents(bare, &corev1.ListEventComponentsRequest{TayfaId: tf, EventId: ev})
			return err
		},
		"erişim": func() error {
			_, err := s.CheckEventAccess(bare, &corev1.CheckEventAccessRequest{TayfaId: tf, EventId: ev})
			return err
		},
		"atama": func() error {
			_, err := s.AssignEventReferee(bare, &corev1.AssignEventRefereeRequest{TayfaId: tf, EventId: ev, UserId: id.New().String()})
			return err
		},
	} {
		if codeOf(t, call()) != codes.Unauthenticated {
			t.Errorf("%s: çağıransız kabul edildi", name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("çağıransız istek app katmanına ulaştı: %v", f.calls)
	}
}

// componentRef is the start of every component's server: the caller from
// the token, the IDs and the key from the request.
func TestComponentRef(t *testing.T) {
	user, tf, ev := id.New(), id.New(), id.New()
	ref, err := componentRef(callerCtx(user), &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "gear"})
	if err != nil || ref != (component.Ref{User: user, Tayfa: tf, Event: ev, Key: "gear"}) {
		t.Fatalf("ref = %+v %v", ref, err)
	}
	for name, in := range map[string]*corev1.ComponentRef{
		"boş":            nil,
		"tayfa":          {TayfaId: "x", EventId: ev.String(), ComponentKey: "gear"},
		"etkinlik":       {TayfaId: tf.String(), EventId: "x", ComponentKey: "gear"},
		"anahtar yok":    {TayfaId: tf.String(), EventId: ev.String()},
		"bozuk anahtar":  {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "Gear"},
		"tek harf":       {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "g"},
		"uzun anahtar":   {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "g1234567890123456789012345678901234567890"},
		"sql denemesi":   {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "gear'--"},
		"boşluklu":       {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "gear "},
		"rakamla başlar": {TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "1gear"},
	} {
		if _, err := componentRef(callerCtx(user), in); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := componentRef(context.Background(), &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "gear"}); codeOf(t, err) != codes.Unauthenticated {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry registers the framework's service; a component's registrar
// that fails stops the start.
func TestRegisterComponents(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info := srv.GetServiceInfo()
	if _, ok := info["tayfam.core.v1.EventComponentService"]; !ok {
		t.Fatalf("servisler: %v", info)
	}
	if got := len(info["tayfam.core.v1.EventComponentService"].Methods); got != 6 {
		t.Fatalf("metod sayısı = %d", got)
	}
}

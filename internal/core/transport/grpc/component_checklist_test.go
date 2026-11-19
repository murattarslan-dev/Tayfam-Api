package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/checklist"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeChecklist struct {
	err      error
	method   string
	ref      component.Ref
	in       checklist.Input
	item     id.ID
	assigned id.ID
	out      checklist.List
	calls    int
}

func (f *fakeChecklist) rec(method string, ref component.Ref, item, assigned id.ID) (checklist.List, error) {
	f.method, f.ref, f.item, f.assigned, f.calls = method, ref, item, assigned, f.calls+1
	return f.out, f.err
}

func (f *fakeChecklist) List(_ context.Context, ref component.Ref) (checklist.List, error) {
	return f.rec("List", ref, id.ID{}, id.ID{})
}

func (f *fakeChecklist) Add(_ context.Context, ref component.Ref, in checklist.Input) (checklist.List, error) {
	f.in = in
	return f.rec("Add", ref, id.ID{}, id.ID{})
}

func (f *fakeChecklist) Remove(_ context.Context, ref component.Ref, item id.ID) (checklist.List, error) {
	return f.rec("Remove", ref, item, id.ID{})
}

func (f *fakeChecklist) Assign(_ context.Context, ref component.Ref, item, assigned id.ID) (checklist.List, error) {
	return f.rec("Assign", ref, item, assigned)
}

func TestChecklistComponentMethods(t *testing.T) {
	member, tent, stove := id.New(), id.New(), id.New()
	f := &fakeChecklist{out: checklist.List{
		Items: []checklist.Item{
			{ID: tent, Name: "Çadır", Quantity: 2, AssignedUserID: member},
			{ID: stove, Name: "Ocak", Quantity: 1},
		},
		Label: "gear", AssignmentEnabled: true, QuantityEnabled: true, CallerCanManage: true, CallerCanTake: true,
	}}
	s := NewChecklistComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "gear"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "gear"}
	check := func(method string, got *corev1.Checklist, err error, item, assigned id.ID) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if f.method != method || f.ref != want || f.item != item || f.assigned != assigned {
			t.Fatalf("%s: app'e giden %s %+v %s %s", method, f.method, f.ref, f.item, f.assigned)
		}
		it := got.GetItems()
		if len(it) != 2 || it[0].GetId() != tent.String() || it[0].GetName() != "Çadır" || it[0].GetQuantity() != 2 ||
			it[0].GetAssignedUserId() != member.String() || it[1].GetAssignedUserId() != "" || got.GetLabel() != "gear" ||
			!got.GetAssignmentEnabled() || !got.GetQuantityEnabled() || !got.GetCallerCanManage() || !got.GetCallerCanTake() {
			t.Fatalf("%s: yanıt %v", method, got)
		}
	}

	list, err := s.ListChecklist(ctx, &corev1.ListChecklistRequest{Ref: ref})
	check("List", list.GetChecklist(), err, id.ID{}, id.ID{})
	added, err := s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: ref, Name: " Çadır ", Quantity: 2, AssignedUserId: member.String()})
	check("Add", added.GetChecklist(), err, id.ID{}, id.ID{})
	// The values reach the app layer as sent; it cleans them.
	if f.in != (checklist.Input{Name: " Çadır ", Quantity: 2, AssignedUserID: member}) {
		t.Fatalf("madde girdisi: %+v", f.in)
	}
	if _, err := s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Ocak"}); err != nil || f.in != (checklist.Input{Name: "Ocak"}) {
		t.Fatalf("atamasız madde: %+v %v", f.in, err)
	}
	removed, err := s.RemoveChecklistItem(ctx, &corev1.RemoveChecklistItemRequest{Ref: ref, ItemId: tent.String()})
	check("Remove", removed.GetChecklist(), err, tent, id.ID{})
	assigned, err := s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: tent.String(), AssignedUserId: member.String()})
	check("Assign", assigned.GetChecklist(), err, tent, member)
	cleared, err := s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: tent.String()})
	check("Assign", cleared.GetChecklist(), err, tent, id.ID{})

	// An empty list: an empty list, not nil fields that break a client.
	f.out = checklist.List{Items: []checklist.Item{}, Label: "item"}
	if list, err := s.ListChecklist(ctx, &corev1.ListChecklistRequest{Ref: ref}); err != nil || len(list.GetChecklist().GetItems()) != 0 || list.GetChecklist().GetLabel() != "item" {
		t.Fatalf("boş liste: %v %v", list, err)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrEventItemTaken:    {codes.Aborted, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_TAKEN},
		apperr.ErrEventLimit:        {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrEventNotGoing:     {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_NOT_GOING},
		apperr.ErrEventEnded:        {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_EVENT_ENDED},
		apperr.ErrEventItemNotFound: {codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
	} {
		f.err = appErr
		_, err := s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: tent.String(), AssignedUserId: member.String()})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestChecklistComponentValidation(t *testing.T) {
	f := &fakeChecklist{}
	s := NewChecklistComponentServer(f, discard)
	tf, ev, good := id.New().String(), id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "gear"}
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
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "gear"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "gear"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Gear"},
	} {
		_, e1 := s.ListChecklist(ctx, &corev1.ListChecklistRequest{Ref: bad})
		_, e2 := s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: bad, Name: "Çadır"})
		_, e3 := s.RemoveChecklistItem(ctx, &corev1.RemoveChecklistItemRequest{Ref: bad, ItemId: good})
		_, e4 := s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: bad, ItemId: good})
		for i, err := range []error{e1, e2, e3, e4} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	_, err := s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Çadır", Quantity: -1})
	if field(err) != "quantity" {
		t.Error("eksi adet")
	}
	_, err = s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Çadır", Quantity: checklist.QuantityMax + 1})
	if field(err) != "quantity" {
		t.Error("1000 adet")
	}
	_, err = s.AddChecklistItem(ctx, &corev1.AddChecklistItemRequest{Ref: ref, Name: "Çadır", AssignedUserId: "x"})
	if field(err) != "assigned_user_id" {
		t.Error("eklemede bozuk kişi")
	}
	_, err = s.RemoveChecklistItem(ctx, &corev1.RemoveChecklistItemRequest{Ref: ref, ItemId: "x"})
	if field(err) != "item_id" {
		t.Error("bozuk madde kimliği")
	}
	_, err = s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref})
	if field(err) != "item_id" {
		t.Error("maddesiz atama")
	}
	_, err = s.AssignChecklistItem(ctx, &corev1.AssignChecklistItemRequest{Ref: ref, ItemId: good, AssignedUserId: "x"})
	if field(err) != "assigned_user_id" {
		t.Error("atamada bozuk kişi")
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.AddChecklistItem(context.Background(), &corev1.AddChecklistItemRequest{Ref: ref, Name: "Çadır"}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the list component.
func TestChecklistComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.ChecklistComponentService"]
	if !ok || len(info.Methods) != 4 {
		t.Fatalf("liste servisi: %v", info)
	}
}

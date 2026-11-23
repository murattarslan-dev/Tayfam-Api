package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/daily"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDaily struct {
	err         error
	method      string
	user, tayfa id.ID
	item        id.ID
	in          daily.Input
	crid        string
	deleted     bool
	ids         []id.ID
	today       daily.Today
	list        daily.List
	saved       daily.Saved
	calls       int
}

func (f *fakeDaily) rec(method string, user, tayfa id.ID) {
	f.method, f.user, f.tayfa, f.calls = method, user, tayfa, f.calls+1
}

func (f *fakeDaily) Today(_ context.Context, user, tayfa id.ID) (daily.Today, error) {
	f.rec("Today", user, tayfa)
	return f.today, f.err
}

func (f *fakeDaily) List(_ context.Context, user, tayfa id.ID, deleted bool) (daily.List, error) {
	f.rec("List", user, tayfa)
	f.deleted = deleted
	return f.list, f.err
}

func (f *fakeDaily) Create(_ context.Context, user, tayfa id.ID, in daily.Input, crid string) (daily.Saved, error) {
	f.rec("Create", user, tayfa)
	f.in, f.crid = in, crid
	return f.saved, f.err
}

func (f *fakeDaily) Update(_ context.Context, user, tayfa, item id.ID, in daily.Input) (daily.Saved, error) {
	f.rec("Update", user, tayfa)
	f.item, f.in = item, in
	return f.saved, f.err
}

func (f *fakeDaily) Delete(_ context.Context, user, tayfa, item id.ID) error {
	f.rec("Delete", user, tayfa)
	f.item = item
	return f.err
}

func (f *fakeDaily) Restore(_ context.Context, user, tayfa, item id.ID) (daily.Saved, error) {
	f.rec("Restore", user, tayfa)
	f.item = item
	return f.saved, f.err
}

func (f *fakeDaily) Reorder(_ context.Context, user, tayfa id.ID, ids []id.ID) ([]daily.Item, error) {
	f.rec("Reorder", user, tayfa)
	f.ids = ids
	return f.list.Items, f.err
}

func TestTayfaDailyMethods(t *testing.T) {
	a, b := id.New(), id.New()
	author := id.New()
	t0 := time.Date(2026, time.November, 10, 5, 0, 0, 0, time.UTC)
	pinned := daily.Item{ID: a, Body: "Söz", Attribution: "Atatürk", Position: 0, Pin: &daily.Pin{Month: 11, Day: 10}, CreatedBy: author, CreatedAt: t0, UpdatedAt: t0}
	plain := daily.Item{ID: b, Body: "Dize", Position: 1, Pin: &daily.Pin{Month: 11, Day: 24, Year: 2026}, CreatedAt: t0, UpdatedAt: t0, DeletedAt: t0}
	f := &fakeDaily{
		today: daily.Today{Item: &pinned, Pinned: true, Date: time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)},
		list:  daily.List{Items: []daily.Item{pinned, plain}, CallerCanManage: true},
		saved: daily.Saved{Item: pinned, PinConflict: true},
	}
	s := NewTayfaDailyServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	today, err := s.GetDailyToday(ctx, &corev1.GetDailyTodayRequest{TayfaId: tf.String()})
	if err != nil || f.method != "Today" || f.user != user || f.tayfa != tf {
		t.Fatalf("bugün: %v %+v", err, f)
	}
	if !today.GetPinned() || today.GetDate() != "2026-11-10" || today.GetItem().GetBody() != "Söz" ||
		today.GetItem().GetAttribution() != "Atatürk" || today.GetItem().GetCreatedBy() != author.String() ||
		today.GetItem().GetPin().GetMonth() != 11 || today.GetItem().GetPin().Year != nil || today.GetItem().GetDeleteTime() != nil {
		t.Fatalf("bugün yanıtı: %v", today)
	}
	f.today = daily.Today{Date: time.Date(2026, time.November, 11, 0, 0, 0, 0, time.UTC)}
	if empty, err := s.GetDailyToday(ctx, &corev1.GetDailyTodayRequest{TayfaId: tf.String()}); err != nil || empty.GetItem() != nil || empty.GetPinned() {
		t.Fatalf("boş gün: %v %v", empty, err)
	}

	list, err := s.ListDailyItems(ctx, &corev1.ListDailyItemsRequest{TayfaId: tf.String(), Deleted: true})
	if err != nil || f.method != "List" || !f.deleted || !list.GetCallerCanManage() || len(list.GetItems()) != 2 {
		t.Fatalf("liste: %v %v", list, err)
	}
	if it := list.GetItems()[1]; it.GetPin().GetYear() != 2026 || it.GetDeleteTime() == nil || it.GetCreatedBy() != "" {
		t.Fatalf("silinen: %v", it)
	}

	year := int32(2027)
	crid := id.New().String()
	created, err := s.CreateDailyItem(ctx, &corev1.CreateDailyItemRequest{
		TayfaId: tf.String(), ClientRequestId: crid,
		Item: &corev1.DailyItemInput{Body: "Söz", Attribution: "Atatürk", Pin: &corev1.DailyPin{Month: 11, Day: 10, Year: &year}},
	})
	if err != nil || f.method != "Create" || f.crid != crid || f.in.Body != "Söz" || f.in.Attribution != "Atatürk" ||
		f.in.Pin == nil || *f.in.Pin != (daily.Pin{Month: 11, Day: 10, Year: 2027}) || !created.GetPinConflict() {
		t.Fatalf("ekleme: %v %v %+v", created, err, f.in)
	}
	// An explicit year 0 is not "every year": the app layer refuses it.
	zero := int32(0)
	if _, err := s.CreateDailyItem(ctx, &corev1.CreateDailyItemRequest{
		TayfaId: tf.String(), ClientRequestId: crid, Item: &corev1.DailyItemInput{Body: "x", Pin: &corev1.DailyPin{Month: 1, Day: 1, Year: &zero}},
	}); err != nil || daily.ValidPin(f.in.Pin) {
		t.Fatalf("yıl 0: %v %+v", err, f.in.Pin)
	}

	if _, err := s.UpdateDailyItem(ctx, &corev1.UpdateDailyItemRequest{TayfaId: tf.String(), ItemId: a.String(), Item: &corev1.DailyItemInput{Body: "Yeni"}}); err != nil ||
		f.method != "Update" || f.item != a || f.in.Body != "Yeni" || f.in.Pin != nil {
		t.Fatalf("düzenleme: %v %+v", err, f)
	}
	if _, err := s.DeleteDailyItem(ctx, &corev1.DeleteDailyItemRequest{TayfaId: tf.String(), ItemId: b.String()}); err != nil || f.method != "Delete" || f.item != b {
		t.Fatalf("silme: %v", err)
	}
	if _, err := s.RestoreDailyItem(ctx, &corev1.RestoreDailyItemRequest{TayfaId: tf.String(), ItemId: b.String()}); err != nil || f.method != "Restore" || f.item != b {
		t.Fatalf("geri alma: %v", err)
	}
	order, err := s.ReorderDailyItems(ctx, &corev1.ReorderDailyItemsRequest{TayfaId: tf.String(), ItemIds: []string{b.String(), a.String()}})
	if err != nil || f.method != "Reorder" || len(f.ids) != 2 || f.ids[0] != b || len(order.GetItems()) != 2 {
		t.Fatalf("sıra: %v %v", order, err)
	}
}

func TestTayfaDailyValidation(t *testing.T) {
	f := &fakeDaily{}
	s := NewTayfaDailyServer(f, discard)
	ctx := callerCtx(id.New())
	tf := id.New().String()
	for name, call := range map[string]func() error{
		"tayfa": func() error {
			_, err := s.GetDailyToday(ctx, &corev1.GetDailyTodayRequest{TayfaId: "x"})
			return err
		},
		"item_id": func() error {
			_, err := s.DeleteDailyItem(ctx, &corev1.DeleteDailyItemRequest{TayfaId: tf, ItemId: "x"})
			return err
		},
		"item_ids": func() error {
			_, err := s.ReorderDailyItems(ctx, &corev1.ReorderDailyItemsRequest{TayfaId: tf, ItemIds: []string{"x"}})
			return err
		},
	} {
		if err := call(); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek uygulamaya ulaştı: %d", f.calls)
	}
	f.err = apperr.ErrDailyItemNotFound
	if _, err := s.RestoreDailyItem(ctx, &corev1.RestoreDailyItemRequest{TayfaId: tf, ItemId: id.New().String()}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("bulunamadı: %v", err)
	}
}

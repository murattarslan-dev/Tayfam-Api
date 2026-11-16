package rest

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// --- fakeCore: AvailabilityService (issue #207) ---

func testAvailabilityDay() *corev1.AvailabilityDay {
	return &corev1.AvailabilityDay{
		Date: "2026-11-20", Slots: []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_MORNING, corev1.DaySlot_DAY_SLOT_EVENING},
		Note: "Kahve?", UpdateTime: timestamppb.New(time.Unix(3, 0)),
	}
}

func (f *fakeCore) ListMyAvailability(ctx context.Context, in *corev1.ListMyAvailabilityRequest, _ ...grpc.CallOption) (*corev1.ListMyAvailabilityResponse, error) {
	return &corev1.ListMyAvailabilityResponse{Days: []*corev1.AvailabilityDay{testAvailabilityDay()}}, f.record(ctx, in)
}
func (f *fakeCore) SetAvailability(ctx context.Context, in *corev1.SetAvailabilityRequest, _ ...grpc.CallOption) (*corev1.SetAvailabilityResponse, error) {
	return &corev1.SetAvailabilityResponse{Day: testAvailabilityDay()}, f.record(ctx, in)
}
func (f *fakeCore) DeleteAvailability(ctx context.Context, in *corev1.DeleteAvailabilityRequest, _ ...grpc.CallOption) (*corev1.DeleteAvailabilityResponse, error) {
	return &corev1.DeleteAvailabilityResponse{}, f.record(ctx, in)
}
func (f *fakeCore) ListUserAvailability(ctx context.Context, in *corev1.ListUserAvailabilityRequest, _ ...grpc.CallOption) (*corev1.ListUserAvailabilityResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.ListUserAvailabilityResponse{Days: []*corev1.AvailabilityDay{}}, nil
}

func TestAvailabilityRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "PUT", "/v1/me/availability/2026-11-20", `{"slots":["evening","morning"],"note":"Kahve?"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("yazma: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	in := got.(*corev1.SetAvailabilityRequest)
	if in.GetDate() != "2026-11-20" || in.GetNote() != "Kahve?" || tok != "USERTOKEN" ||
		!slices.Equal(in.GetSlots(), []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_EVENING, corev1.DaySlot_DAY_SLOT_MORNING}) {
		t.Fatalf("core'a giden: %v %q", in, tok)
	}
	if v := decodeBody[availabilityDayJSON](t, w); v.Date != "2026-11-20" || !slices.Equal(v.Slots, []string{"morning", "evening"}) || v.Note != "Kahve?" || v.UpdatedAt.IsZero() {
		t.Fatalf("cevap: %+v", v)
	}
	// The note is optional.
	if w := as(h, "PUT", "/v1/me/availability/2026-11-21", `{"slots":["all_day"]}`); w.Code != http.StatusOK {
		t.Fatalf("notsuz: %d %s", w.Code, w.Body)
	}

	w = as(h, "GET", "/v1/me/availability", "")
	if v := decodeBody[availabilityListJSON](t, w); w.Code != 200 || len(v.Days) != 1 {
		t.Fatalf("kendi listesi: %d %+v", w.Code, v)
	}
	w = as(h, "GET", "/v1/users/"+userID+"/availability", "")
	got, _, _ = f.last()
	if w.Code != 200 || got.(*corev1.ListUserAvailabilityRequest).GetUserId() != userID || strings.TrimSpace(w.Body.String()) != `{"days":[]}` {
		t.Fatalf("arkadaşın listesi: %d %s", w.Code, w.Body)
	}
	if w := as(h, "DELETE", "/v1/me/availability/2026-11-20", ""); w.Code != http.StatusNoContent {
		t.Fatalf("silme: %d", w.Code)
	}
	if got, _, _ = f.last(); got.(*corev1.DeleteAvailabilityRequest).GetDate() != "2026-11-20" {
		t.Fatalf("silme isteği: %v", got)
	}

	// Not a friend (or blocked, or unknown): the unknown user answer.
	f.err = coreErr(codes.NotFound, "ERROR_REASON_USER_NOT_FOUND")
	w = as(h, "GET", "/v1/users/"+userID+"/availability", "")
	if w.Code != http.StatusNotFound || problemType(t, w) != "user-not-found" {
		t.Fatalf("arkadaş değil: %d %s", w.Code, w.Body)
	}
}

func TestAvailabilityRoutesValidateInput(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/v1/me/availability/2026-11-31", `{"slots":["morning"]}`},
		{"PUT", "/v1/me/availability/20261120", `{"slots":["morning"]}`},
		{"PUT", "/v1/me/availability/2026-11-20", `{"slots":[]}`},
		{"PUT", "/v1/me/availability/2026-11-20", `{}`},
		{"PUT", "/v1/me/availability/2026-11-20", `{"slots":["night"]}`},
		{"PUT", "/v1/me/availability/2026-11-20", `{"slots":["morning","morning","evening","afternoon","all_day"]}`},
		{"PUT", "/v1/me/availability/2026-11-20", `{"slots":["morning"],"extra":1}`},
		{"DELETE", "/v1/me/availability/yarin", ""},
		{"GET", "/v1/users/x/availability", ""},
	} {
		if w := as(h, c.method, c.path, c.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: %d", c.method, c.path, c.body, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("core çağrıldı: %d", n)
	}
}

// The friend list carries the badge when core sends one, and only then.
func TestFriendListBadgeJSON(t *testing.T) {
	fr := testFriend()
	if v := friendFrom(fr); v.NextAvailable != nil {
		t.Fatalf("rozetsiz arkadaş: %+v", v)
	}
	fr.NextAvailable = &corev1.AvailabilityDay{Date: "2026-11-22", Slots: []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_AFTERNOON}}
	v := friendFrom(fr)
	if v.NextAvailable == nil || v.NextAvailable.Date != "2026-11-22" || !slices.Equal(v.NextAvailable.Slots, []string{"afternoon"}) {
		t.Fatalf("rozet: %+v", v.NextAvailable)
	}
}

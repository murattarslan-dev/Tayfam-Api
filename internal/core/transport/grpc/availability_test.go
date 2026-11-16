package grpc

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/availability"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeAvailability struct {
	err    error
	caller id.ID
	target id.ID
	date   string
	slots  []availability.Slot
	note   string
	called bool
}

func (f *fakeAvailability) day() availability.Day {
	return availability.Day{
		Date: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Slots: []availability.Slot{availability.SlotMorning, availability.SlotEvening},
		Note: "Kahve?", UpdatedAt: time.Unix(3, 0).UTC(),
	}
}
func (f *fakeAvailability) ListMine(_ context.Context, c id.ID) ([]availability.Day, error) {
	f.caller, f.called = c, true
	return []availability.Day{f.day()}, f.err
}
func (f *fakeAvailability) Set(_ context.Context, c id.ID, date string, slots []availability.Slot, note string) (availability.Day, error) {
	f.caller, f.date, f.slots, f.note, f.called = c, date, slots, note, true
	return f.day(), f.err
}
func (f *fakeAvailability) Delete(_ context.Context, c id.ID, date string) error {
	f.caller, f.date, f.called = c, date, true
	return f.err
}
func (f *fakeAvailability) ListUser(_ context.Context, c, target id.ID) ([]availability.Day, error) {
	f.caller, f.target, f.called = c, target, true
	if f.err != nil {
		return nil, f.err
	}
	return []availability.Day{f.day()}, nil
}

func TestAvailabilityMethods(t *testing.T) {
	f := &fakeAvailability{}
	var logs bytes.Buffer
	srv := NewAvailabilityServer(f, slog.New(slog.NewJSONHandler(&logs, nil)))
	u, other := id.New(), id.New()
	ctx := callerCtx(u)

	s, err := srv.SetAvailability(ctx, &corev1.SetAvailabilityRequest{
		Date: "2026-11-20", Slots: []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_EVENING, corev1.DaySlot_DAY_SLOT_MORNING}, Note: "Kahve?",
	})
	if err != nil || f.caller != u || f.date != "2026-11-20" || f.note != "Kahve?" ||
		!slices.Equal(f.slots, []availability.Slot{availability.SlotEvening, availability.SlotMorning}) {
		t.Fatalf("yazma: %v %+v", err, f)
	}
	d := s.GetDay()
	if d.GetDate() != "2026-11-20" || d.GetNote() != "Kahve?" || d.GetUpdateTime() == nil ||
		!slices.Equal(d.GetSlots(), []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_MORNING, corev1.DaySlot_DAY_SLOT_EVENING}) {
		t.Fatalf("gün: %v", d)
	}
	if l, err := srv.ListMyAvailability(ctx, &corev1.ListMyAvailabilityRequest{}); err != nil || len(l.GetDays()) != 1 {
		t.Fatalf("kendi listesi: %v %v", l, err)
	}
	if l, err := srv.ListUserAvailability(ctx, &corev1.ListUserAvailabilityRequest{UserId: other.String()}); err != nil || len(l.GetDays()) != 1 || f.target != other || f.caller != u {
		t.Fatalf("arkadaşın listesi: %v %v", l, err)
	}
	if _, err := srv.DeleteAvailability(ctx, &corev1.DeleteAvailabilityRequest{Date: "2026-11-20"}); err != nil || f.date != "2026-11-20" {
		t.Fatalf("silme: %v", err)
	}
	if strings.Contains(logs.String(), "Kahve") {
		t.Fatal("not log'a yazıldı")
	}
}

func TestAvailabilityValidationAndAuth(t *testing.T) {
	f := &fakeAvailability{}
	srv := NewAvailabilityServer(f, discard)
	ctx := callerCtx(id.New())
	for name, slots := range map[string][]corev1.DaySlot{
		"belirsiz":   {corev1.DaySlot_DAY_SLOT_UNSPECIFIED},
		"bilinmeyen": {corev1.DaySlot(99)},
	} {
		if _, err := srv.SetAvailability(ctx, &corev1.SetAvailabilityRequest{Date: "2026-11-20", Slots: slots}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s dilim: %v", name, err)
		}
	}
	if _, err := srv.ListUserAvailability(ctx, &corev1.ListUserAvailabilityRequest{UserId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("kimlik: %v", err)
	}
	bg := context.Background()
	if _, err := srv.ListMyAvailability(bg, &corev1.ListMyAvailabilityRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız liste: %v", err)
	}
	if _, err := srv.SetAvailability(bg, &corev1.SetAvailabilityRequest{Date: "2026-11-20"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız yazma: %v", err)
	}
	if _, err := srv.DeleteAvailability(bg, &corev1.DeleteAvailabilityRequest{Date: "2026-11-20"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız silme: %v", err)
	}
	if _, err := srv.ListUserAvailability(bg, &corev1.ListUserAvailabilityRequest{UserId: id.New().String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız arkadaş listesi: %v", err)
	}
	if f.called {
		t.Fatal("uygulama katmanı çağrıldı")
	}

	srv = NewAvailabilityServer(&fakeAvailability{err: apperr.ErrUserNotFound}, discard)
	_, err := srv.ListUserAvailability(ctx, &corev1.ListUserAvailabilityRequest{UserId: id.New().String()})
	if code, reason, _ := reasonOf(t, err); code != codes.NotFound || reason != "ERROR_REASON_USER_NOT_FOUND" {
		t.Fatalf("arkadaş değil: %v %q", code, reason)
	}
	srv = NewAvailabilityServer(&fakeAvailability{err: apperr.Validation("date")}, discard)
	_, err = srv.SetAvailability(ctx, &corev1.SetAvailabilityRequest{Date: "2020-01-01", Slots: []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_ALL_DAY}})
	if code, reason, _ := reasonOf(t, err); code != codes.InvalidArgument || reason != "ERROR_REASON_VALIDATION" {
		t.Fatalf("tarih: %v %q", code, reason)
	}
}

// The friend list badge carries the date and slots, never the note.
func TestFriendBadge(t *testing.T) {
	f := social.Friend{
		UserID: id.New(), DisplayName: "Ece", State: social.StateFriends, RequestedAt: time.Unix(1, 0).UTC(), AcceptedAt: time.Unix(2, 0).UTC(),
		NextAvailable: social.NextAvailable{Date: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), Slots: []string{"all_day"}},
	}
	pb := friendPB(f).GetNextAvailable()
	if pb.GetDate() != "2026-11-21" || !slices.Equal(pb.GetSlots(), []corev1.DaySlot{corev1.DaySlot_DAY_SLOT_ALL_DAY}) || pb.GetNote() != "" || pb.GetUpdateTime() != nil {
		t.Fatalf("rozet: %v", pb)
	}
	f.NextAvailable = social.NextAvailable{}
	if friendPB(f).GetNextAvailable() != nil {
		t.Fatal("boş vakti olmayan arkadaşta rozet")
	}
}

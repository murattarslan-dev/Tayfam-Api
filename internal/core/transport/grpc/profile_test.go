package grpc

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeProfiles struct {
	caller, subject id.ID
	items           []profile.Activity
}

func (f *fakeProfiles) GetProfile(_ context.Context, c, s id.ID) (profile.Profile, error) {
	f.caller, f.subject = c, s
	return profile.Profile{UserID: s, DisplayName: "Ece", Friend: social.StateFriends, ActivityVisible: true,
		MutualTayfas: []profile.Tayfa{{ID: id.New(), Name: "Kamp"}}}, nil
}
func (f *fakeProfiles) ListActivity(_ context.Context, c, s id.ID, _ int32, _ string) (profile.Page[profile.Activity], error) {
	f.caller, f.subject = c, s
	return profile.Page[profile.Activity]{Items: f.items}, nil
}

func TestProfileServer(t *testing.T) {
	tf, ev := id.New(), id.New()
	f := &fakeProfiles{items: []profile.Activity{
		{EventType: "hike", Mode: "in_person", City: "Bursa", StartedAt: time.Unix(1, 0).UTC()},
		{EventType: "gaming", Mode: "online", StartedAt: time.Unix(2, 0).UTC(), Member: true, TayfaID: tf, TayfaName: "Kamp", EventID: ev, EventTitle: "Turnuva", DurationMinutes: 90},
	}}
	var buf bytes.Buffer
	srv := NewProfileServer(f, slog.New(slog.NewJSONHandler(&buf, nil)))
	u, s := id.New(), id.New()

	p, err := srv.GetUserProfile(callerCtx(u), &corev1.GetUserProfileRequest{UserId: s.String()})
	if err != nil || f.caller != u || f.subject != s || p.GetProfile().GetFriendState() != corev1.FriendState_FRIEND_STATE_FRIENDS ||
		len(p.GetProfile().GetMutualTayfas()) != 1 || p.GetProfile().GetActivityVisibility() != corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED {
		t.Fatalf("profil: %v %v", p, err)
	}
	l, err := srv.ListUserActivity(callerCtx(u), &corev1.ListUserActivityRequest{UserId: s.String()})
	if err != nil {
		t.Fatal(err)
	}
	r0, r1 := l.GetRecords()[0], l.GetRecords()[1]
	if r0.GetEventType() != corev1.EventType_EVENT_TYPE_HIKE || r0.GetMode() != corev1.EventMode_EVENT_MODE_IN_PERSON || r0.GetTayfaMember() || r0.GetTayfaId() != "" || r0.GetEventId() != "" {
		t.Fatalf("üye olmayan kaydı: %v", r0)
	}
	if r1.GetMode() != corev1.EventMode_EVENT_MODE_ONLINE || !r1.GetTayfaMember() || r1.GetTayfaId() != tf.String() || r1.GetEventId() != ev.String() || r1.GetDurationMinutes() != 90 {
		t.Fatalf("üye kaydı: %v", r1)
	}
	if _, err := srv.GetUserProfile(callerCtx(u), &corev1.GetUserProfileRequest{UserId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("kimlik: %v", err)
	}
	if _, err := srv.ListUserActivity(context.Background(), &corev1.ListUserActivityRequest{UserId: s.String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
}

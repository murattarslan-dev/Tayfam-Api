package grpc

import (
	"bytes"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestAttendanceServerValidates(t *testing.T) {
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(id.New())
	good := id.New().String()
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	badTime := &timestamppb.Timestamp{Seconds: 1, Nanos: -1}
	for name, tc := range map[string]struct {
		call  func() error
		field string
	}{
		"NaN konum": {func() error {
			_, err := srv.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: good, EventId: good, Location: &corev1.LatLng{Lat: math.NaN()}})
			return err
		}, "location"},
		"sonsuz konum": {func() error {
			_, err := srv.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: good, EventId: good, Location: &corev1.LatLng{Lng: math.Inf(-1)}})
			return err
		}, "location"},
		"bozuk çıkış zamanı": {func() error {
			_, err := srv.StartHike(ctx, &corev1.StartHikeRequest{TayfaId: good, EventId: good, StartTime: badTime})
			return err
		}, "start_time"},
		"bozuk dönüş zamanı": {func() error {
			_, err := srv.FinishHike(ctx, &corev1.FinishHikeRequest{TayfaId: good, EventId: good, ReturnTime: badTime})
			return err
		}, "return_time"},
		"bozuk kişi": {func() error {
			_, err := srv.SetAttendance(ctx, &corev1.SetAttendanceRequest{TayfaId: good, EventId: good, UserId: "x"})
			return err
		}, "user_id"},
		"bozuk av konumu": {func() error {
			_, err := srv.AddFishingCatch(ctx, &corev1.AddFishingCatchRequest{TayfaId: good, EventId: good, Location: &corev1.LatLng{Lat: math.NaN()}})
			return err
		}, "location"},
		"bilinmeyen görünürlük": {func() error {
			_, err := srv.AddFishingCatch(ctx, &corev1.AddFishingCatchRequest{TayfaId: good, EventId: good, Visibility: 9})
			return err
		}, "visibility"},
		"bozuk av kimliği": {func() error {
			_, err := srv.RemoveFishingCatch(ctx, &corev1.RemoveFishingCatchRequest{TayfaId: good, EventId: good, CatchId: "x"})
			return err
		}, "catch_id"},
	} {
		if got := field(tc.call()); got != tc.field {
			t.Errorf("%s: %s", name, got)
		}
	}
	if f.calls != 0 {
		t.Fatal("geçersiz istek app katmanına ulaştı")
	}
}

func TestAttendanceServerConverts(t *testing.T) {
	user := id.New()
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(user)
	tid, eid := id.New().String(), id.New().String()

	resp, err := srv.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: tid, EventId: eid, Location: &corev1.LatLng{Lat: 38.1, Lng: 27.2}})
	if err != nil || f.at == nil || f.at.Lat != 38.1 || f.at.Lng != 27.2 ||
		resp.GetAttendance() != corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED || resp.GetMethod() != corev1.AttendanceMethod_ATTENDANCE_METHOD_LOCATION {
		t.Fatalf("check-in = %v %v %+v", resp, err, f.at)
	}
	if _, err := srv.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: tid, EventId: eid}); err != nil || f.at != nil {
		t.Fatalf("konumsuz check-in app'e %+v olarak gitti: %v", f.at, err)
	}

	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	h, err := srv.StartHike(ctx, &corev1.StartHikeRequest{TayfaId: tid, EventId: eid, StartTime: timestamppb.New(at)})
	if err != nil || !f.when.Equal(at) || h.GetCheckin().GetStartTime() == nil || h.GetCheckin().GetReturnTime() != nil {
		t.Fatalf("çıkış = %v %v %v", h, err, f.when)
	}
	if _, err := srv.StartHike(ctx, &corev1.StartHikeRequest{TayfaId: tid, EventId: eid}); err != nil || !f.when.IsZero() {
		t.Fatalf("zamansız çıkış = %v %v", f.when, err)
	}
	back, err := srv.FinishHike(ctx, &corev1.FinishHikeRequest{TayfaId: tid, EventId: eid})
	if err != nil || back.GetCheckin().GetReturnTime() == nil {
		t.Fatalf("dönüş = %v %v", back, err)
	}

	target := id.New()
	if _, err := srv.SetAttendance(ctx, &corev1.SetAttendanceRequest{TayfaId: tid, EventId: eid, UserId: target.String(), Attended: true}); err != nil || f.target != target || !f.attended {
		t.Fatalf("düzeltme = %v %v %v", f.target, f.attended, err)
	}

	c, err := srv.AddFishingCatch(ctx, &corev1.AddFishingCatchRequest{
		TayfaId: tid, EventId: eid, Species: "Levrek", LengthCm: 40, Location: &corev1.LatLng{Lat: 1, Lng: 2},
		Visibility: corev1.CatchVisibility_CATCH_VISIBILITY_TAYFA, ClientRequestId: "istek",
	})
	if err != nil || f.catch.Visibility != event.CatchTayfa || f.catch.Location == nil || f.catch.ClientRequestID != "istek" ||
		c.GetCatch().GetVisibility() != corev1.CatchVisibility_CATCH_VISIBILITY_TAYFA || c.GetCatch().GetLocation().GetLng() != 2 {
		t.Fatalf("av = %v %v %+v", c, err, f.catch)
	}
	if _, err := srv.AddFishingCatch(ctx, &corev1.AddFishingCatchRequest{TayfaId: tid, EventId: eid}); err != nil || f.catch.Visibility != "" {
		t.Fatalf("belirsiz görünürlük app'e %q olarak gitti: %v", f.catch.Visibility, err)
	}
	list, err := srv.ListFishingCatches(ctx, &corev1.ListFishingCatchesRequest{TayfaId: tid, EventId: eid})
	if err != nil || len(list.GetCatches()) != 1 || list.GetCatches()[0].GetLocation() != nil ||
		list.GetCatches()[0].GetVisibility() != corev1.CatchVisibility_CATCH_VISIBILITY_SELF {
		t.Fatalf("liste = %v %v", list, err)
	}

	f.view = event.Event{CallerAttendance: event.AttendanceNoShow}
	g, err := srv.GetEvent(ctx, &corev1.GetEventRequest{TayfaId: tid, EventId: eid})
	if err != nil || g.GetEvent().GetCallerAttendance() != corev1.AttendanceStatus_ATTENDANCE_STATUS_NO_SHOW {
		t.Fatalf("caller_attendance = %v %v", g, err)
	}
}

// TestCheckInLocationNotLogged runs the real app: the location sent with a
// check-in is neither logged nor in the status the client gets.
func TestCheckInLocationNotLogged(t *testing.T) {
	pool := dbtest.New(t, "core", coredb.Migrations())
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := event.New(event.Config{Pool: pool, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	owner := id.New()
	ctx := callerCtx(owner)
	created, err := NewTayfaServer(tayfas, log).CreateTayfa(ctx, &corev1.CreateTayfaRequest{Name: "Sahibin Tayfası", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	ev, err := NewEventServer(creator, log).CreateEvent(ctx, createReq(tid))
	if err != nil {
		t.Fatal(err)
	}
	eid := ev.GetEvent().GetId()
	if _, err := NewEventServer(creator, log).SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: tid, EventId: eid, Status: corev1.RsvpStatus_RSVP_STATUS_GOING}); err != nil {
		t.Fatal(err)
	}
	start := ev.GetEvent().GetStartTime().AsTime()
	events, err := event.New(event.Config{Pool: pool, Log: log, Now: func() time.Time { return start }})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewEventServer(events, log)

	// 38.42 / 27.14 is the meeting point; these are several km away.
	sent := &corev1.LatLng{Lat: 38.47391, Lng: 27.08623}
	var statuses []string
	for i := range 6 {
		_, err := srv.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: tid, EventId: eid, Location: sent})
		want := corev1.ErrorReason_ERROR_REASON_CHECK_IN_TOO_FAR
		if i == 5 {
			want = corev1.ErrorReason_ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED
		}
		if _, info := infoOf(t, err); info.GetReason() != want.String() {
			t.Fatalf("deneme %d: %v", i+1, err)
		}
		statuses = append(statuses, fmt.Sprint(err))
	}
	for _, s := range []string{"38.473", "27.086", "38.47391", "27.08623"} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("konum log'da: %s", s)
		}
		for _, st := range statuses {
			if strings.Contains(st, s) {
				t.Errorf("konum hata mesajında: %s", st)
			}
		}
	}
}

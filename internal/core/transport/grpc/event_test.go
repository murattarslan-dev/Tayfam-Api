package grpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"math"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeEvents records what transport passed on and returns err.
type fakeEvents struct {
	err      error
	calls    int
	user     id.ID
	tayfa    id.ID
	event    id.ID
	in       event.Input
	fields   []string
	status   string
	vehicle  event.VehicleInput
	assigned id.ID
	view     event.Event
}

func (f *fakeEvents) rec(u, t, e id.ID) { f.calls++; f.user, f.tayfa, f.event = u, t, e }

func (f *fakeEvents) Create(_ context.Context, u, t id.ID, in event.Input) (event.Event, error) {
	f.rec(u, t, id.ID{})
	f.in = in
	return f.view, f.err
}
func (f *fakeEvents) Update(_ context.Context, u, t, e id.ID, fields []string, in event.Input) (event.Event, error) {
	f.rec(u, t, e)
	f.fields, f.in = fields, in
	return f.view, f.err
}
func (f *fakeEvents) Cancel(_ context.Context, u, t, e id.ID, _ string) (event.Event, error) {
	f.rec(u, t, e)
	return f.view, f.err
}
func (f *fakeEvents) Get(_ context.Context, u, t, e id.ID) (event.Event, error) {
	f.rec(u, t, e)
	return f.view, f.err
}
func (f *fakeEvents) ListUpcoming(_ context.Context, u, t id.ID, _ int32, _ string) ([]event.Event, string, error) {
	f.rec(u, t, id.ID{})
	return []event.Event{f.view}, "sonraki", f.err
}
func (f *fakeEvents) SetRSVP(_ context.Context, u, t, e id.ID, st string) (string, error) {
	f.rec(u, t, e)
	f.status = st
	return event.RSVPWaitlist, f.err
}
func (f *fakeEvents) ListAttendees(_ context.Context, u, t, e id.ID, st string, _ int32, _ string) ([]event.Attendee, string, error) {
	f.rec(u, t, e)
	f.status = st
	return []event.Attendee{{UserID: u, Status: event.RSVPGoing, UpdatedAt: time.Now()}}, "", f.err
}
func (f *fakeEvents) AddCampItem(_ context.Context, u, t, e id.ID, name string, q int32, a id.ID) (event.CampItem, error) {
	f.rec(u, t, e)
	f.assigned = a
	return event.CampItem{ID: id.New(), Name: name, Quantity: q, AssignedUserID: a}, f.err
}
func (f *fakeEvents) RemoveCampItem(_ context.Context, u, t, e, _ id.ID) error {
	f.rec(u, t, e)
	return f.err
}
func (f *fakeEvents) SetVehicle(_ context.Context, u, t, e id.ID, in event.VehicleInput) (event.Vehicle, error) {
	f.rec(u, t, e)
	f.vehicle = in
	return event.Vehicle{ID: id.New(), UserID: u, Make: in.Make, Plate: in.Plate}, f.err
}
func (f *fakeEvents) RemoveVehicle(_ context.Context, u, t, e, _ id.ID) error {
	f.rec(u, t, e)
	return f.err
}

func future() (*timestamppb.Timestamp, *timestamppb.Timestamp) {
	s := time.Now().Add(24 * time.Hour)
	return timestamppb.New(s), timestamppb.New(s.Add(time.Hour))
}

func createReq(tayfaID string) *corev1.CreateEventRequest {
	s, e := future()
	return &corev1.CreateEventRequest{
		TayfaId: tayfaID, Type: corev1.EventType_EVENT_TYPE_GENERAL, Mode: corev1.EventMode_EVENT_MODE_IN_PERSON,
		Title: "Buluşma", StartTime: s, EndTime: e, MeetingPoint: &corev1.LatLng{Lat: 38.42, Lng: 27.14},
		City: "İzmir", District: "Konak", ClientRequestId: id.New().String(),
	}
}

// callAllEvents calls every EventService method and returns the errors by
// method name.
func callAllEvents(ctx context.Context, c corev1.EventServiceServer, tayfaID, eventID string) map[string]error {
	out := map[string]error{}
	_, out["CreateEvent"] = c.CreateEvent(ctx, createReq(tayfaID))
	_, out["UpdateEvent"] = c.UpdateEvent(ctx, &corev1.UpdateEventRequest{TayfaId: tayfaID, EventId: eventID, Title: "x", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}})
	_, out["CancelEvent"] = c.CancelEvent(ctx, &corev1.CancelEventRequest{TayfaId: tayfaID, EventId: eventID})
	_, out["GetEvent"] = c.GetEvent(ctx, &corev1.GetEventRequest{TayfaId: tayfaID, EventId: eventID})
	_, out["ListUpcomingEvents"] = c.ListUpcomingEvents(ctx, &corev1.ListUpcomingEventsRequest{TayfaId: tayfaID})
	_, out["SetRsvp"] = c.SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: tayfaID, EventId: eventID, Status: corev1.RsvpStatus_RSVP_STATUS_GOING})
	_, out["ListAttendees"] = c.ListAttendees(ctx, &corev1.ListAttendeesRequest{TayfaId: tayfaID, EventId: eventID})
	_, out["AddCampItem"] = c.AddCampItem(ctx, &corev1.AddCampItemRequest{TayfaId: tayfaID, EventId: eventID, Name: "Çadır"})
	_, out["RemoveCampItem"] = c.RemoveCampItem(ctx, &corev1.RemoveCampItemRequest{TayfaId: tayfaID, EventId: eventID, ItemId: id.New().String()})
	_, out["AddConvoyVehicle"] = c.AddConvoyVehicle(ctx, &corev1.AddConvoyVehicleRequest{TayfaId: tayfaID, EventId: eventID, Make: "Ford"})
	_, out["RemoveConvoyVehicle"] = c.RemoveConvoyVehicle(ctx, &corev1.RemoveConvoyVehicleRequest{TayfaId: tayfaID, EventId: eventID, VehicleId: id.New().String()})
	return out
}

func TestEventServerNeedsCaller(t *testing.T) {
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	for method, err := range callAllEvents(context.Background(), srv, id.New().String(), id.New().String()) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	if f.calls != 0 {
		t.Fatal("token'sız istek app katmanına ulaştı")
	}
}

func TestEventServerValidatesRequest(t *testing.T) {
	f := &fakeEvents{}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(id.New())
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	good := id.New().String()
	for _, bad := range []string{"", "abc", "' OR 1=1 --"} {
		for method, err := range callAllEvents(ctx, srv, bad, good) {
			if got := field(err); got != "tayfa_id" {
				t.Errorf("%s tayfa_id %q: %s", method, bad, got)
			}
		}
		for method, err := range callAllEvents(ctx, srv, good, bad) {
			if method == "CreateEvent" || method == "ListUpcomingEvents" {
				continue
			}
			if got := field(err); got != "event_id" {
				t.Errorf("%s event_id %q: %s", method, bad, got)
			}
		}
	}
	// Only CreateEvent and ListUpcomingEvents (no event_id) went through,
	// once per bad value.
	if f.calls != 6 {
		t.Fatalf("geçersiz kimlik app katmanına ulaştı: %d çağrı", f.calls)
	}
	f.calls = 0

	for name, tc := range map[string]struct {
		edit  func(*corev1.CreateEventRequest)
		field string
	}{
		"belirsiz tür":   {func(r *corev1.CreateEventRequest) { r.Type = 0 }, "type"},
		"bilinmeyen tür": {func(r *corev1.CreateEventRequest) { r.Type = 99 }, "type"},
		"belirsiz biçim": {func(r *corev1.CreateEventRequest) { r.Mode = 0 }, "mode"},
		"NaN enlem":      {func(r *corev1.CreateEventRequest) { r.MeetingPoint.Lat = math.NaN() }, "meeting_point"},
		"sonsuz boylam":  {func(r *corev1.CreateEventRequest) { r.MeetingPoint.Lng = math.Inf(1) }, "meeting_point"},
		"NaN rota noktası": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Convoy{Convoy: &corev1.ConvoyDetails{Route: []*corev1.LatLng{{Lat: math.NaN()}}}}
		}, "route"},
		"boş rota noktası": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Convoy{Convoy: &corev1.ConvoyDetails{Route: []*corev1.LatLng{nil, nil}}}
		}, "route"},
		"belirsiz platform": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Gaming{Gaming: &corev1.GamingDetails{Game: "x"}}
		}, "platform"},
		"belirsiz spor": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Sports{Sports: &corev1.SportsDetails{}}
		}, "sport"},
		"NaN mesafe": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Hike{Hike: &corev1.HikeDetails{DistanceKm: math.NaN()}}
		}, "distance_km"},
		"bozuk görünürlük": {func(r *corev1.CreateEventRequest) {
			r.Details = &corev1.CreateEventRequest_Fishing{Fishing: &corev1.FishingDetails{SpotVisibility: 9}}
		}, "spot_visibility"},
	} {
		r := createReq(good)
		tc.edit(r)
		_, err := srv.CreateEvent(ctx, r)
		if got := field(err); got != tc.field {
			t.Errorf("%s: %s", name, got)
		}
	}
	for _, st := range []corev1.RsvpStatus{0, corev1.RsvpStatus_RSVP_STATUS_WAITLIST, 42} {
		_, err := srv.SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: good, EventId: good, Status: st})
		if field(err) != "status" {
			t.Errorf("katılım %v", st)
		}
	}
	_, err := srv.ListAttendees(ctx, &corev1.ListAttendeesRequest{TayfaId: good, EventId: good, Status: 42})
	if field(err) != "status" {
		t.Error("bilinmeyen liste durumu")
	}
	_, err = srv.AddCampItem(ctx, &corev1.AddCampItemRequest{TayfaId: good, EventId: good, AssignedUserId: "x"})
	if field(err) != "assigned_user_id" {
		t.Error("bozuk atanan")
	}
	_, err = srv.RemoveCampItem(ctx, &corev1.RemoveCampItemRequest{TayfaId: good, EventId: good, ItemId: "x"})
	if field(err) != "item_id" {
		t.Error("bozuk malzeme kimliği")
	}
	_, err = srv.RemoveConvoyVehicle(ctx, &corev1.RemoveConvoyVehicleRequest{TayfaId: good, EventId: good, VehicleId: "x"})
	if field(err) != "vehicle_id" {
		t.Error("bozuk araç kimliği")
	}
	if f.calls != 0 {
		t.Fatal("geçersiz istek app katmanına ulaştı")
	}

	// Fields outside the mask are not converted: a bad mode only matters
	// with "location".
	if _, err := srv.UpdateEvent(ctx, &corev1.UpdateEventRequest{TayfaId: good, EventId: good, Mode: 9, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}}); err != nil {
		t.Fatal(err)
	}
	_, err = srv.UpdateEvent(ctx, &corev1.UpdateEventRequest{TayfaId: good, EventId: good, Mode: 9, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"location"}}})
	if field(err) != "mode" {
		t.Error("maskedeki bozuk biçim")
	}
}

func TestEventServerConverts(t *testing.T) {
	user := id.New()
	start := time.Now().Add(time.Hour).UTC()
	water := true
	view := event.Event{
		ID: id.New(), TayfaID: id.New(), Type: event.TypeCamp, Title: "Kamp", StartsAt: start, EndsAt: start.Add(time.Hour),
		Location: event.Location{Mode: event.ModeInPerson, MeetingPoint: &event.Point{Lat: 1, Lng: 2}, City: "Çanakkale", District: "Ayvacık"},
		Capacity: 4, Status: event.StatusCancelled, CancelledAt: start, CancelReason: "yağmur", CreatedBy: user,
		Going: 3, Waitlist: 1, CallerRSVP: event.RSVPWaitlist, CallerCanManage: true,
		Details: event.Details{Camp: &event.Camp{SiteName: "Assos", HasWater: &water, Items: []event.CampItem{{ID: id.New(), Name: "Çadır", Quantity: 2}}}},
	}
	f := &fakeEvents{view: view}
	srv := NewEventServer(f, discard)
	ctx := callerCtx(user)
	resp, err := srv.GetEvent(ctx, &corev1.GetEventRequest{TayfaId: view.TayfaID.String(), EventId: view.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != view.TayfaID || f.event != view.ID {
		t.Fatal("kimlikler aktarılmadı")
	}
	p := resp.GetEvent()
	if p.GetType() != corev1.EventType_EVENT_TYPE_CAMP || p.GetMode() != corev1.EventMode_EVENT_MODE_IN_PERSON ||
		p.GetStatus() != corev1.EventStatus_EVENT_STATUS_CANCELLED || p.GetCancelTime() == nil || p.GetMeetingPoint().GetLng() != 2 ||
		p.GetCallerRsvp() != corev1.RsvpStatus_RSVP_STATUS_WAITLIST || !p.GetCallerCanManage() || p.GetGoingCount() != 3 ||
		p.GetCreatedBy() != user.String() || p.GetCamp().GetSiteName() != "Assos" || !p.GetCamp().GetHasWater() ||
		p.GetCamp().HasToilet != nil || len(p.GetCamp().GetItems()) != 1 || p.GetCamp().GetItems()[0].GetAssignedUserId() != "" {
		t.Fatalf("dönüştürme = %v", p)
	}

	// Request details reach the app layer; output-only fields are dropped.
	r := createReq(view.TayfaID.String())
	r.Type = corev1.EventType_EVENT_TYPE_CONVOY
	r.Details = &corev1.CreateEventRequest_Convoy{Convoy: &corev1.ConvoyDetails{
		Route:    []*corev1.LatLng{{Lat: 1, Lng: 2}, {Lat: 3, Lng: 4}},
		Vehicles: []*corev1.ConvoyVehicle{{Make: "sızma"}},
	}}
	if _, err := srv.CreateEvent(ctx, r); err != nil {
		t.Fatal(err)
	}
	if f.in.Type != event.TypeConvoy || f.in.Details.Convoy == nil || len(f.in.Details.Convoy.Route) != 2 || len(f.in.Details.Convoy.Vehicles) != 0 ||
		f.in.Location.MeetingPoint == nil || !f.in.StartsAt.Equal(r.GetStartTime().AsTime()) {
		t.Fatalf("girdi = %+v", f.in)
	}
	for _, tc := range []struct {
		d     any
		check func(event.Details) bool
	}{
		{&corev1.CreateEventRequest_Gaming{Gaming: &corev1.GamingDetails{Game: "CS2", Platform: corev1.GamingPlatform_GAMING_PLATFORM_SWITCH}},
			func(d event.Details) bool { return d.Gaming != nil && d.Gaming.Platform == "switch" }},
		{&corev1.CreateEventRequest_Sports{Sports: &corev1.SportsDetails{Sport: corev1.Sport_SPORT_TABLE_FOOTBALL}},
			func(d event.Details) bool { return d.Sports != nil && d.Sports.Sport == "table_football" }},
		{&corev1.CreateEventRequest_Fishing{Fishing: &corev1.FishingDetails{SpotVisibility: corev1.SpotVisibility_SPOT_VISIBILITY_PARTICIPANTS}},
			func(d event.Details) bool {
				return d.Fishing != nil && d.Fishing.SpotVisibility == event.SpotParticipants
			}},
		{&corev1.CreateEventRequest_Hike{Hike: &corev1.HikeDetails{Difficulty: 4, ExpectedReturnTime: timestamppb.New(start)}},
			func(d event.Details) bool {
				return d.Hike != nil && d.Hike.Difficulty == 4 && d.Hike.ExpectedReturnAt.Equal(start)
			}},
	} {
		r := createReq(view.TayfaID.String())
		switch d := tc.d.(type) {
		case *corev1.CreateEventRequest_Gaming:
			r.Details = d
		case *corev1.CreateEventRequest_Sports:
			r.Details = d
		case *corev1.CreateEventRequest_Fishing:
			r.Details = d
		case *corev1.CreateEventRequest_Hike:
			r.Details = d
		}
		if _, err := srv.CreateEvent(ctx, r); err != nil {
			t.Fatal(err)
		}
		if !tc.check(f.in.Details) {
			t.Errorf("detay = %+v", f.in.Details)
		}
	}

	rs, err := srv.SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: view.TayfaID.String(), EventId: view.ID.String(), Status: corev1.RsvpStatus_RSVP_STATUS_NOT_GOING})
	if err != nil || f.status != event.RSVPNotGoing || rs.GetStatus() != corev1.RsvpStatus_RSVP_STATUS_WAITLIST {
		t.Fatalf("katılım = %v %v %q", rs, err, f.status)
	}
	if _, err := srv.ListAttendees(ctx, &corev1.ListAttendeesRequest{TayfaId: view.TayfaID.String(), EventId: view.ID.String()}); err != nil || f.status != "" {
		t.Fatalf("belirsiz durum app'e %q olarak gitti: %v", f.status, err)
	}
	v, err := srv.AddConvoyVehicle(ctx, &corev1.AddConvoyVehicleRequest{TayfaId: view.TayfaID.String(), EventId: view.ID.String(), Make: "Ford", Plate: "35 ABC 1"})
	if err != nil || f.vehicle.Plate != "35 ABC 1" || v.GetVehicle().GetUserId() != user.String() {
		t.Fatalf("araç = %v %v", v, err)
	}
	list, err := srv.ListUpcomingEvents(ctx, &corev1.ListUpcomingEventsRequest{TayfaId: view.TayfaID.String()})
	if err != nil || len(list.GetEvents()) != 1 || list.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste = %v %v", list, err)
	}
}

// TestEventServiceBehindGuard runs the real app behind auth.Guard.
func TestEventServiceBehindGuard(t *testing.T) {
	key, err := auth.ParseSigningKey("test", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-test", Audience: "tayfam-api", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{key.ID: key.Public().Key}, Issuer: "tayfam-test", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t, coredb.Migrations())
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	events, err := event.New(event.Config{Pool: pool, Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(s, NewTayfaServer(tayfas, discard))
	corev1.RegisterEventServiceServer(s, NewEventServer(events, discard))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	as := func(user id.ID) context.Context {
		tok, _, err := signer.Issue(user.String(), id.New().String())
		if err != nil {
			t.Fatal(err)
		}
		return auth.WithToken(context.Background(), tok)
	}
	tc, ec := corev1.NewTayfaServiceClient(conn), corev1.NewEventServiceClient(conn)

	owner, stranger := id.New(), id.New()
	created, err := tc.CreateTayfa(as(owner), &corev1.CreateTayfaRequest{Name: "Sahibin Tayfası", ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	ev, err := ec.CreateEvent(as(owner), createReq(tid))
	if err != nil {
		t.Fatal(err)
	}
	eid := ev.GetEvent().GetId()
	if ev.GetEvent().GetCity() != "İzmir" || !ev.GetEvent().GetCallerCanManage() {
		t.Fatalf("etkinlik = %v", ev)
	}

	// Without a token the request is refused before the app layer.
	if _, err := ec.GetEvent(context.Background(), &corev1.GetEventRequest{TayfaId: tid, EventId: eid}); codeOf(t, err) != codes.Unauthenticated {
		t.Errorf("token'sız: %v", err)
	}
	// A stranger learns nothing about the tayfa or the event.
	_, err = ec.GetEvent(as(stranger), &corev1.GetEventRequest{TayfaId: tid, EventId: eid})
	if _, info := infoOf(t, err); info.GetReason() != corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
		t.Errorf("yabancı: %v", err)
	}
	// Module off: MODULE_DISABLED for members.
	if _, err := tc.SetModule(as(owner), &corev1.SetModuleRequest{TayfaId: tid, Module: corev1.Module_MODULE_EVENTS}); err != nil {
		t.Fatal(err)
	}
	_, err = ec.GetEvent(as(owner), &corev1.GetEventRequest{TayfaId: tid, EventId: eid})
	if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED.String() {
		t.Errorf("modül kapalı: %v", err)
	}
	if _, err := tc.SetModule(as(owner), &corev1.SetModuleRequest{TayfaId: tid, Module: corev1.Module_MODULE_EVENTS, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err = ec.GetEvent(as(owner), &corev1.GetEventRequest{TayfaId: tid, EventId: id.New().String()})
	if code, info := infoOf(t, err); code != codes.NotFound || info.GetReason() != corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND.String() {
		t.Errorf("olmayan etkinlik: %v", err)
	}
	rs, err := ec.SetRsvp(as(owner), &corev1.SetRsvpRequest{TayfaId: tid, EventId: eid, Status: corev1.RsvpStatus_RSVP_STATUS_GOING})
	if err != nil || rs.GetStatus() != corev1.RsvpStatus_RSVP_STATUS_GOING {
		t.Fatalf("katılım = %v %v", rs, err)
	}
	if _, err := ec.CancelEvent(as(owner), &corev1.CancelEventRequest{TayfaId: tid, EventId: eid, Reason: "yağmur"}); err != nil {
		t.Fatal(err)
	}
	_, err = ec.SetRsvp(as(owner), &corev1.SetRsvpRequest{TayfaId: tid, EventId: eid, Status: corev1.RsvpStatus_RSVP_STATUS_MAYBE})
	if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_EVENT_CANCELLED.String() {
		t.Errorf("iptal sonrası katılım: %v", err)
	}
}

func codeOf(t *testing.T, err error) codes.Code {
	t.Helper()
	code, _ := infoOf(t, err)
	return code
}

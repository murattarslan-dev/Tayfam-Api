package rest

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

const (
	eventID   = "01965a3c-0000-7000-8000-000000000005"
	itemID    = "01965a3c-0000-7000-8000-000000000006"
	vehicleID = "01965a3c-0000-7000-8000-000000000007"
	entryID   = "01965a3c-0000-7000-8000-000000000008"
	seriesID  = "01965a3c-0000-7000-8000-000000000009"
	photoID   = "01965a3c-0000-7000-8000-0000000000a1"
	uploadID2 = "01965a3c-0000-7000-8000-0000000000a2"
)

// Coordinates that must never show up in a log line or a problem response.
// The digits are distinctive enough not to appear by chance.
const (
	secretLat = "40.9876543"
	secretLng = "29.1234567"
)

func testEvent() *corev1.Event {
	return &corev1.Event{
		Id: eventID, TayfaId: tayfaID, Type: corev1.EventType_EVENT_TYPE_CONVOY, Mode: corev1.EventMode_EVENT_MODE_IN_PERSON,
		Title: "Pazar konvoyu", Description: "Sahil yolu", StartTime: timestamppb.New(t0.Add(24 * time.Hour)),
		EndTime: timestamppb.New(t0.Add(30 * time.Hour)), MeetingPointName: "Otopark",
		MeetingPoint: &corev1.LatLng{Lat: 41.5, Lng: 29.5}, City: "İstanbul", District: "Sarıyer",
		Capacity: 20, Status: corev1.EventStatus_EVENT_STATUS_SCHEDULED, CreatedBy: userID,
		CreateTime: timestamppb.New(t0), UpdateTime: timestamppb.New(t0),
		GoingCount: 3, MaybeCount: 1, CallerRsvp: corev1.RsvpStatus_RSVP_STATUS_GOING, CallerCanManage: true,
		SeriesId: seriesID,
	}
}

func testSeries() *corev1.EventSeries {
	return &corev1.EventSeries{
		Id: seriesID, TayfaId: tayfaID, TemplateEventId: eventID, Rrule: "FREQ=WEEKLY;BYDAY=SA",
		TimeZone: "Europe/Istanbul", StartTime: timestamppb.New(t0), CreateTime: timestamppb.New(t0),
	}
}

func (f *fakeCore) CreateEvent(ctx context.Context, in *corev1.CreateEventRequest, _ ...grpc.CallOption) (*corev1.CreateEventResponse, error) {
	return &corev1.CreateEventResponse{Event: testEvent()}, f.record(ctx, in)
}
func (f *fakeCore) CreateInstantEvent(ctx context.Context, in *corev1.CreateInstantEventRequest, _ ...grpc.CallOption) (*corev1.CreateInstantEventResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	t := f.t()
	t.Kind, t.InstantEventId = corev1.TayfaKind_TAYFA_KIND_INSTANT, eventID
	return &corev1.CreateInstantEventResponse{Tayfa: t, Event: testEvent()}, nil
}
func (f *fakeCore) UpdateEvent(ctx context.Context, in *corev1.UpdateEventRequest, _ ...grpc.CallOption) (*corev1.UpdateEventResponse, error) {
	return &corev1.UpdateEventResponse{Event: testEvent()}, f.record(ctx, in)
}
func (f *fakeCore) CancelEvent(ctx context.Context, in *corev1.CancelEventRequest, _ ...grpc.CallOption) (*corev1.CancelEventResponse, error) {
	e := testEvent()
	e.Status, e.CancelTime, e.CancelReason = corev1.EventStatus_EVENT_STATUS_CANCELLED, timestamppb.New(t0), in.GetReason()
	return &corev1.CancelEventResponse{Event: e}, f.record(ctx, in)
}
func (f *fakeCore) GetEvent(ctx context.Context, in *corev1.GetEventRequest, _ ...grpc.CallOption) (*corev1.GetEventResponse, error) {
	if f.event != nil {
		return &corev1.GetEventResponse{Event: f.event}, f.record(ctx, in)
	}
	return &corev1.GetEventResponse{Event: testEvent()}, f.record(ctx, in)
}
func (f *fakeCore) ListUpcomingEvents(ctx context.Context, in *corev1.ListUpcomingEventsRequest, _ ...grpc.CallOption) (*corev1.ListUpcomingEventsResponse, error) {
	online := testEvent()
	online.Details, online.Mode, online.MeetingPoint, online.MeetingPointName, online.City, online.District =
		nil, corev1.EventMode_EVENT_MODE_ONLINE, nil, "", "", ""
	online.OnlineUrl, online.CallerRsvp, online.CreatedBy, online.SeriesId = "https://meet.example/x", 0, "", ""
	return &corev1.ListUpcomingEventsResponse{Events: []*corev1.Event{testEvent(), online}, NextPageToken: "SONRAKI"}, f.record(ctx, in)
}
func (f *fakeCore) SetRsvp(ctx context.Context, in *corev1.SetRsvpRequest, _ ...grpc.CallOption) (*corev1.SetRsvpResponse, error) {
	return &corev1.SetRsvpResponse{Status: corev1.RsvpStatus_RSVP_STATUS_WAITLIST}, f.record(ctx, in)
}
func (f *fakeCore) ListAttendees(ctx context.Context, in *corev1.ListAttendeesRequest, _ ...grpc.CallOption) (*corev1.ListAttendeesResponse, error) {
	return &corev1.ListAttendeesResponse{Attendees: []*corev1.Attendee{
		{UserId: userID, Status: corev1.RsvpStatus_RSVP_STATUS_GOING, UpdateTime: timestamppb.New(t0),
			Attendance: corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED, AttendanceMethod: corev1.AttendanceMethod_ATTENDANCE_METHOD_LOCATION},
		{UserId: tayfaID, Status: corev1.RsvpStatus_RSVP_STATUS_GOING, UpdateTime: timestamppb.New(t0)},
	}}, f.record(ctx, in)
}
func (f *fakeCore) CheckIn(ctx context.Context, in *corev1.CheckInRequest, _ ...grpc.CallOption) (*corev1.CheckInResponse, error) {
	return &corev1.CheckInResponse{Attendance: corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED, Method: corev1.AttendanceMethod_ATTENDANCE_METHOD_LOCATION}, f.record(ctx, in)
}
func (f *fakeCore) SetAttendance(ctx context.Context, in *corev1.SetAttendanceRequest, _ ...grpc.CallOption) (*corev1.SetAttendanceResponse, error) {
	return &corev1.SetAttendanceResponse{}, f.record(ctx, in)
}
func (f *fakeCore) AddEventPhoto(ctx context.Context, in *corev1.AddEventPhotoRequest, _ ...grpc.CallOption) (*corev1.AddEventPhotoResponse, error) {
	return &corev1.AddEventPhotoResponse{Photo: &corev1.EventPhoto{
		Id: photoID, EventId: in.GetEventId(), UploaderId: userID, UploadId: in.GetUploadId(), Caption: in.GetCaption(), CreateTime: timestamppb.New(t0),
	}}, f.record(ctx, in)
}
func (f *fakeCore) ListEventPhotos(ctx context.Context, in *corev1.ListEventPhotosRequest, _ ...grpc.CallOption) (*corev1.ListEventPhotosResponse, error) {
	return &corev1.ListEventPhotosResponse{Photos: []*corev1.EventPhoto{
		{Id: photoID, EventId: in.GetEventId(), UploaderId: userID, UploadId: uploadID2, CreateTime: timestamppb.New(t0)},
	}, NextPageToken: "N"}, f.record(ctx, in)
}
func (f *fakeCore) RemoveEventPhoto(ctx context.Context, in *corev1.RemoveEventPhotoRequest, _ ...grpc.CallOption) (*corev1.RemoveEventPhotoResponse, error) {
	return &corev1.RemoveEventPhotoResponse{}, f.record(ctx, in)
}
func (f *fakeCore) GetEventSeries(ctx context.Context, in *corev1.GetEventSeriesRequest, _ ...grpc.CallOption) (*corev1.GetEventSeriesResponse, error) {
	return &corev1.GetEventSeriesResponse{Series: testSeries()}, f.record(ctx, in)
}
func (f *fakeCore) EndEventSeries(ctx context.Context, in *corev1.EndEventSeriesRequest, _ ...grpc.CallOption) (*corev1.EndEventSeriesResponse, error) {
	s := testSeries()
	s.EndTime = timestamppb.New(t0)
	return &corev1.EndEventSeriesResponse{Series: s, CancelledCount: 3}, f.record(ctx, in)
}
func (f *fakeCore) ExportEvent(ctx context.Context, in *corev1.ExportEventRequest, _ ...grpc.CallOption) (*corev1.ExportEventResponse, error) {
	return &corev1.ExportEventResponse{Ics: []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"), ContentType: "text/html"}, f.record(ctx, in)
}

func TestEventRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/events"
	one := base + "/" + eventID
	series := "/v1/tayfas/" + tayfaID + "/event-series/" + seriesID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", base + "?page_size=10&page_token=P", "", 200, &corev1.ListUpcomingEventsRequest{TayfaId: tayfaID, PageSize: 10, PageToken: "P"}},
		{"GET", one, "", 200, &corev1.GetEventRequest{TayfaId: tayfaID, EventId: eventID}},
		{"POST", one + "/cancellation", `{"reason":"Yağmur"}`, 200, &corev1.CancelEventRequest{TayfaId: tayfaID, EventId: eventID, Reason: "Yağmur"}},
		{"POST", one + "/cancellation", `{}`, 200, &corev1.CancelEventRequest{TayfaId: tayfaID, EventId: eventID}},
		{"PUT", one + "/rsvp", `{"status":"going"}`, 200, &corev1.SetRsvpRequest{TayfaId: tayfaID, EventId: eventID, Status: corev1.RsvpStatus_RSVP_STATUS_GOING}},
		{"PUT", one + "/rsvp", `{"status":"not_going"}`, 200, &corev1.SetRsvpRequest{TayfaId: tayfaID, EventId: eventID, Status: corev1.RsvpStatus_RSVP_STATUS_NOT_GOING}},
		{"GET", one + "/attendees", "", 200, &corev1.ListAttendeesRequest{TayfaId: tayfaID, EventId: eventID, Status: corev1.RsvpStatus_RSVP_STATUS_GOING}},
		{"GET", one + "/attendees?status=waitlist&page_size=5", "", 200, &corev1.ListAttendeesRequest{TayfaId: tayfaID, EventId: eventID, Status: corev1.RsvpStatus_RSVP_STATUS_WAITLIST, PageSize: 5}},
		{"PUT", one + "/attendees/" + userID + "/attendance", "", 204, &corev1.SetAttendanceRequest{TayfaId: tayfaID, EventId: eventID, UserId: userID, Attended: true}},
		{"DELETE", one + "/attendees/" + userID + "/attendance", "", 204, &corev1.SetAttendanceRequest{TayfaId: tayfaID, EventId: eventID, UserId: userID}},
		{"POST", one + "/check-in", `{}`, 200, &corev1.CheckInRequest{TayfaId: tayfaID, EventId: eventID}},
		{"POST", one + "/check-in", `{"location":{"lat":41.01,"lng":28.97}}`, 200, &corev1.CheckInRequest{TayfaId: tayfaID, EventId: eventID, Location: &corev1.LatLng{Lat: 41.01, Lng: 28.97}}},
		{"GET", one + "/photos?page_size=20&page_token=P", "", 200, &corev1.ListEventPhotosRequest{TayfaId: tayfaID, EventId: eventID, PageSize: 20, PageToken: "P"}},
		{"POST", one + "/photos", `{"upload_id":"` + uploadID2 + `","caption":"Zirvede","client_request_id":"` + reqID + `"}`, 201,
			&corev1.AddEventPhotoRequest{TayfaId: tayfaID, EventId: eventID, UploadId: uploadID2, Caption: "Zirvede", ClientRequestId: reqID}},
		{"POST", one + "/photos", `{"upload_id":"` + uploadID2 + `","client_request_id":"` + reqID + `"}`, 201,
			&corev1.AddEventPhotoRequest{TayfaId: tayfaID, EventId: eventID, UploadId: uploadID2, ClientRequestId: reqID}},
		{"DELETE", one + "/photos/" + photoID, "", 204, &corev1.RemoveEventPhotoRequest{TayfaId: tayfaID, EventId: eventID, PhotoId: photoID}},
		{"GET", series, "", 200, &corev1.GetEventSeriesRequest{TayfaId: tayfaID, SeriesId: seriesID}},
		{"POST", series + "/ending", "", 200, &corev1.EndEventSeriesRequest{TayfaId: tayfaID, SeriesId: seriesID}},
		{"GET", one + "/calendar", "", 200, &corev1.ExportEventRequest{TayfaId: tayfaID, EventId: eventID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}
}

func TestEventResponses(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	one := "/v1/tayfas/" + tayfaID + "/events/" + eventID

	w := as(h, "GET", one, "")
	e := decodeBody[eventJSON](t, w)
	if e.ID != eventID || e.Type != "convoy" || e.Mode != "in_person" || e.Status != "scheduled" || e.CallerRSVP != "going" ||
		!e.CallerCanManage || e.CreatedByUserID != userID || e.SeriesID != seriesID || !e.StartsAt.Equal(t0.Add(24*time.Hour)) ||
		e.MeetingPoint == nil || e.MeetingPoint.Lat != 41.5 || e.GoingCount != 3 {
		t.Fatalf("etkinlik: %+v", e)
	}
	// No type has details: a convoy's route and vehicles are components
	// (#239, #240), a fishing event's catches too (#243).
	for _, absent := range []string{"caller_attendance", "cancelled_at", "cancel_reason", "online_url", `"camp"`, `"convoy":`, `"hike":`, `"fishing":`, "vehicles"} {
		if strings.Contains(w.Body.String(), absent) {
			t.Errorf("boş alan yazıldı: %s", absent)
		}
	}

	w = as(h, "GET", "/v1/tayfas/"+tayfaID+"/events", "")
	l := decodeBody[eventListJSON](t, w)
	if len(l.Events) != 2 || l.NextPageToken != "SONRAKI" || l.Events[1].Mode != "online" || l.Events[1].OnlineURL == "" ||
		l.Events[1].MeetingPoint != nil || l.Events[1].CallerRSVP != "" || l.Events[1].CreatedByUserID != "" {
		t.Fatalf("liste: %+v", l)
	}

	w = as(h, "POST", one+"/cancellation", `{"reason":"Yağmur"}`)
	if c := decodeBody[eventJSON](t, w); c.Status != "cancelled" || c.CancelReason != "Yağmur" || c.CancelledAt == nil {
		t.Fatalf("iptal: %+v", c)
	}
	w = as(h, "PUT", one+"/rsvp", `{"status":"going"}`)
	if r := decodeBody[rsvpJSON](t, w); r.Status != "waitlist" {
		t.Fatalf("rsvp: %+v", r)
	}
	w = as(h, "GET", one+"/attendees", "")
	al := decodeBody[attendeeListJSON](t, w)
	if len(al.Attendees) != 2 || al.Attendees[0].Attendance != "attended" || al.Attendees[0].AttendanceMethod != "location" || al.Attendees[1].Attendance != "" {
		t.Fatalf("katılımcılar: %+v", al)
	}
	w = as(h, "POST", one+"/check-in", `{}`)
	if c := decodeBody[checkInJSON](t, w); c.Attendance != "attended" || c.Method != "location" {
		t.Fatalf("check-in: %+v", c)
	}
	w = as(h, "GET", one+"/photos", "")
	if pl := decodeBody[eventPhotoListJSON](t, w); len(pl.Photos) != 1 || pl.Photos[0].UploadID != uploadID2 || pl.Photos[0].UploaderID != userID ||
		pl.NextPageToken != "N" || strings.Contains(w.Body.String(), "caption") {
		t.Fatalf("albüm: %s", w.Body)
	}
	w = as(h, "POST", one+"/photos", `{"upload_id":"`+uploadID2+`","caption":"Göl","client_request_id":"`+reqID+`"}`)
	if p := decodeBody[eventPhotoJSON](t, w); p.ID != photoID || p.Caption != "Göl" || !p.CreatedAt.Equal(t0) || p.EventID != eventID {
		t.Fatalf("albüm fotoğrafı: %s", w.Body)
	}
	w = as(h, "POST", "/v1/tayfas/"+tayfaID+"/event-series/"+seriesID+"/ending", "")
	if s := decodeBody[eventSeriesEndingJSON](t, w); s.CancelledCount != 3 || s.Series.EndedAt == nil || s.Series.RRule != "FREQ=WEEKLY;BYDAY=SA" || s.Series.TemplateEventID != eventID {
		t.Fatalf("seri bitirme: %s", w.Body)
	}
	w = as(h, "GET", "/v1/tayfas/"+tayfaID+"/event-series/"+seriesID, "")
	if strings.Contains(w.Body.String(), "ended_at") || strings.Contains(w.Body.String(), "created_by_user_id") {
		t.Fatalf("seri: boş alanlar yazıldı: %s", w.Body)
	}
}

func TestCreateEvent(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/events"
	start, end := t0.Add(48*time.Hour), t0.Add(50*time.Hour)
	body := `{"type":"hike","mode":"in_person","title":"Belgrad turu","description":"Kolay","starts_at":"` + start.Format(time.RFC3339) +
		`","ends_at":"` + end.Format(time.RFC3339) + `","meeting_point_name":"Bahçeköy girişi","meeting_point":{"lat":41.17,"lng":28.98},` +
		`"city":"İstanbul","district":"Sarıyer","capacity":15,"discoverable":true,` +
		`"fields":{"difficulty":2,"planned_distance":8.5,"planned_elevation":120},` +
		`"recurrence":{"rrule":"FREQ=WEEKLY;BYDAY=SA","time_zone":"Europe/Istanbul"},"client_request_id":"` + reqID + `"}`
	w := as(h, "POST", base, body)
	if w.Code != 201 || w.Header().Get("Location") != base+"/"+eventID {
		t.Fatalf("açma: %d %v %s", w.Code, w.Header(), w.Body)
	}
	want := &corev1.CreateEventRequest{
		TayfaId: tayfaID, TypeKey: "hike", Mode: corev1.EventMode_EVENT_MODE_IN_PERSON,
		Title: "Belgrad turu", Description: "Kolay", StartTime: timestamppb.New(start), EndTime: timestamppb.New(end),
		MeetingPointName: "Bahçeköy girişi", MeetingPoint: &corev1.LatLng{Lat: 41.17, Lng: 28.98},
		City: "İstanbul", District: "Sarıyer", Capacity: 15, Discoverable: true,
		Fields:          map[string]string{"difficulty": `2`, "planned_distance": `8.5`, "planned_elevation": `120`},
		ClientRequestId: reqID, Recurrence: &corev1.Recurrence{Rrule: "FREQ=WEEKLY;BYDAY=SA", TimeZone: "Europe/Istanbul"},
	}
	if got, _, _ := f.last(); !proto.Equal(got, want) {
		t.Fatalf("core'a giden = %v", got)
	}

	times := `"starts_at":"` + start.Format(time.RFC3339) + `","ends_at":"` + end.Format(time.RFC3339) + `"`
	for _, tc := range []struct {
		body string
		want *corev1.CreateEventRequest // only Fields
	}{
		// The type's own data travels as field values, as the JSON the
		// client wrote (issue #237).
		{`{"type":"gaming","mode":"online","title":"T",` + times + `,"online_url":"https://x","fields":{"game":"Satranç","platform":"pc"},"client_request_id":"` + reqID + `"}`,
			&corev1.CreateEventRequest{Fields: map[string]string{"game": `"Satranç"`, "platform": `"pc"`}}},
		{`{"type":"sports","mode":"in_person","title":"T",` + times + `,"fields":{"sport":"table_tennis","venue_name":"Salon"},"client_request_id":"` + reqID + `"}`,
			&corev1.CreateEventRequest{Fields: map[string]string{"sport": `"table_tennis"`, "venue_name": `"Salon"`}}},
		// Any key may be a type: the catalog decides, in core.
		{`{"type":"board_game_night","mode":"in_person","title":"T",` + times + `,"fields":{"players":4,"langs":["tr","en"],"rated":true,"avg":1.5},"client_request_id":"` + reqID + `"}`,
			&corev1.CreateEventRequest{Fields: map[string]string{"players": `4`, "langs": `["tr","en"]`, "rated": `true`, "avg": `1.5`}}},
		{`{"type":"general","mode":"in_person","title":"T",` + times + `,"fields":{},"client_request_id":"` + reqID + `"}`, &corev1.CreateEventRequest{}},
		// A fishing event has no details to send: its catches are the log
		// component's (#243).
		{`{"type":"fishing","mode":"in_person","title":"T",` + times + `,"client_request_id":"` + reqID + `"}`, &corev1.CreateEventRequest{}},
		// A camp's site details are fields (#241).
		{`{"type":"camp","mode":"in_person","title":"T",` + times + `,"fields":{"site_name":"Göl","has_water":false},"client_request_id":"` + reqID + `"}`,
			&corev1.CreateEventRequest{Fields: map[string]string{"site_name": `"Göl"`, "has_water": `false`}}},
		// A convoy has no details to send: its route is the route component's (#239).
		{`{"type":"convoy","mode":"in_person","title":"T",` + times + `,"client_request_id":"` + reqID + `"}`, &corev1.CreateEventRequest{}},
		{`{"type":"general","mode":"in_person","title":"T",` + times + `,"client_request_id":"` + reqID + `"}`, &corev1.CreateEventRequest{}},
	} {
		w := as(h, "POST", base, tc.body)
		got, _, _ := f.last()
		req := got.(*corev1.CreateEventRequest)
		if w.Code != 201 || !proto.Equal(req, &corev1.CreateEventRequest{
			TayfaId: req.GetTayfaId(), TypeKey: req.GetTypeKey(), Mode: req.GetMode(), Title: "T", StartTime: timestamppb.New(start),
			EndTime: timestamppb.New(end), OnlineUrl: req.GetOnlineUrl(), ClientRequestId: reqID, Fields: tc.want.Fields,
		}) || req.GetTypeKey() == "" || req.GetType() != corev1.EventType_EVENT_TYPE_UNSPECIFIED {
			t.Errorf("%s: %d; core'a giden %v", tc.body, w.Code, got)
		}
	}
}

func TestEventBadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/events"
	one := base + "/" + eventID
	times := `"starts_at":"2027-01-02T10:00:00Z","ends_at":"2027-01-02T12:00:00Z"`
	create := func(extra string) string {
		return `{"type":"general","mode":"in_person","title":"T",` + times + `,"client_request_id":"` + reqID + `"` + extra + `}`
	}
	for name, tc := range map[string]struct{ method, path, body string }{
		"bozuk tür anahtarı":     {"POST", base, strings.Replace(create(""), "general", "Golf", 1)},
		"boş tür":                {"POST", base, strings.Replace(create(""), "general", "", 1)},
		"null alan değeri":       {"POST", base, create(`,"fields":{"game":null}`)},
		"bozuk alan anahtarı":    {"POST", base, create(`,"fields":{"Game":"x"}`)},
		"alanlar dizi":           {"POST", base, create(`,"fields":["game"]`)},
		"dev alan değeri":        {"POST", base, create(`,"fields":{"game":"` + strings.Repeat("a", 9000) + `"}`)},
		"düzenlemede bozuk alan": {"PATCH", one, `{"fields":{"Game":"x"}}`},
		"bilinmeyen biçim":       {"POST", base, strings.Replace(create(""), "in_person", "hybrid", 1)},
		"boş başlık":             {"POST", base, strings.Replace(create(""), `"title":"T"`, `"title":" "`, 1)},
		"uzun başlık":            {"POST", base, strings.Replace(create(""), `"title":"T"`, `"title":"`+strings.Repeat("ş", 121)+`"`, 1)},
		"uzun açıklama":          {"POST", base, create(`,"description":"` + strings.Repeat("a", 5001) + `"`)},
		"başlangıç yok":          {"POST", base, `{"type":"general","mode":"online","title":"T","ends_at":"2027-01-02T12:00:00Z","client_request_id":"` + reqID + `"}`},
		"istek kimliği yok":      {"POST", base, `{"type":"general","mode":"online","title":"T",` + times + `}`},
		"enlem dışı":             {"POST", base, create(`,"meeting_point":{"lat":90.5,"lng":0}`)},
		"boylam dışı":            {"POST", base, create(`,"meeting_point":{"lat":0,"lng":-181}`)},
		"kapasite":               {"POST", base, create(`,"capacity":10001`)},
		"eksi kapasite":          {"POST", base, create(`,"capacity":-1`)},
		"tür ayrıntısı uymuyor":  {"POST", base, create(`,"camp":{}`)},
		"iki ayrıntı":            {"POST", base, strings.Replace(create(`,"camp":{},"hike":{}`), "general", "camp", 1)},
		// The Faz 1 hike object is gone too (#242): its data is in fields
		// and the roll call component.
		"eski yürüyüş ayrıntısı": {"POST", base, strings.Replace(create(`,"hike":{"difficulty":3}`), "general", "hike", 1)},
		// The Faz 1 gaming and sports objects are gone (ADR-0041).
		"eski oyun ayrıntısı": {"POST", base, strings.Replace(create(`,"gaming":{"game":"x","platform":"pc"}`), "general", "gaming", 1)},
		"eski spor ayrıntısı": {"POST", base, strings.Replace(create(`,"sports":{"sport":"football"}`), "general", "sports", 1)},
		// The fishing object is gone too (#243): the catches are the log
		// component's.
		"eski balık ayrıntısı": {"POST", base, strings.Replace(create(`,"fishing":{"spot_visibility":"participants"}`), "general", "fishing", 1)},
		// The convoy object is gone too: the route and the vehicles are
		// components (#239, #240).
		"eski konvoy rotası":        {"POST", base, strings.Replace(create(`,"convoy":{"route":[{"lat":0,"lng":0},{"lat":1,"lng":0}]}`), "general", "convoy", 1)},
		"eski konvoy ayrıntısı":     {"POST", base, strings.Replace(create(`,"convoy":{}`), "general", "convoy", 1)},
		"boş kural":                 {"POST", base, create(`,"recurrence":{"rrule":""}`)},
		"bilinmeyen alan":           {"POST", base, create(`,"status":"cancelled"`)},
		"organizatör verilemez":     {"POST", base, create(`,"created_by":"` + userID + `"`)},
		"boş düzenleme":             {"PATCH", one, `{}`},
		"null düzenleme":            {"PATCH", one, `{"title":null}`},
		"yalnızca seri":             {"PATCH", one, `{"apply_to_series":true}`},
		"yalnızca başlangıç":        {"PATCH", one, `{"starts_at":"2027-01-02T10:00:00Z"}`},
		"yer modsuz":                {"PATCH", one, `{"city":"İzmir"}`},
		"yer bozuk mod":             {"PATCH", one, `{"mode":"x","city":"İzmir"}`},
		"düzenleme kapasite":        {"PATCH", one, `{"capacity":-5}`},
		"uzun iptal sebebi":         {"POST", one + "/cancellation", `{"reason":"` + strings.Repeat("a", 501) + `"}`},
		"iptal gövdesiz":            {"POST", one + "/cancellation", ``},
		"bekleme listesi rsvp":      {"PUT", one + "/rsvp", `{"status":"waitlist"}`},
		"boş rsvp":                  {"PUT", one + "/rsvp", `{}`},
		"bozuk katılımcı durum":     {"GET", one + "/attendees?status=attended", ""},
		"bozuk kullanıcı":           {"PUT", one + "/attendees/kim/attendance", ""},
		"check-in enlem":            {"POST", one + "/check-in", `{"location":{"lat":-91,"lng":0}}`},
		"check-in bozuk":            {"POST", one + "/check-in", `{"location":{"lat":"kuzey","lng":0}}`},
		"check-in fazla alan":       {"POST", one + "/check-in", `{"location":{"lat":1,"lng":2,"accuracy":5}}`},
		"balık ayrıntısı düzenleme": {"PATCH", one, `{"fishing":{"spot_visibility":"participants"}}`},
		"albüm yüklemesi yok":       {"POST", one + "/photos", `{"client_request_id":"` + reqID + `"}`},
		"albüm istek kimliği":       {"POST", one + "/photos", `{"upload_id":"` + uploadID2 + `"}`},
		"albüm uzun açıklama":       {"POST", one + "/photos", `{"upload_id":"` + uploadID2 + `","caption":"` + strings.Repeat("ş", 201) + `","client_request_id":"` + reqID + `"}`},
		"albüm bilinmeyen alan":     {"POST", one + "/photos", `{"upload_id":"` + uploadID2 + `","url":"x","client_request_id":"` + reqID + `"}`},
		"bozuk fotoğraf id":         {"DELETE", one + "/photos/x", ""},
		"bozuk etkinlik id":         {"GET", base + "/x", ""},
		"bozuk tayfa id":            {"GET", "/v1/tayfas/x/events", ""},
		"bozuk sayfa":               {"GET", base + "?page_size=101", ""},
		"bozuk seri id":             {"GET", "/v1/tayfas/" + tayfaID + "/event-series/x", ""},
		"bozuk takvim id":           {"GET", base + "/x/calendar", ""},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		if (w.Code != 400 && w.Code != 415) || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("geçersiz istek core'a gitti (%d)", n)
	}
}

func TestUpdateEventMask(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	one := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	for _, tc := range []struct {
		body  string
		paths []string
		check func(*corev1.UpdateEventRequest) bool
	}{
		{`{"description":""}`, []string{"description"}, nil},
		{`{"title":"Yeni","capacity":0,"discoverable":false}`, []string{"title", "capacity", "discoverable"},
			func(r *corev1.UpdateEventRequest) bool { return r.GetTitle() == "Yeni" }},
		{`{"starts_at":"2027-01-02T10:00:00Z","ends_at":"2027-01-02T12:00:00Z"}`, []string{"time"},
			func(r *corev1.UpdateEventRequest) bool { return r.GetEndTime().AsTime().Hour() == 12 }},
		{`{"mode":"online","online_url":"https://meet.example/x"}`, []string{"location"},
			func(r *corev1.UpdateEventRequest) bool {
				return r.GetMode() == corev1.EventMode_EVENT_MODE_ONLINE && r.GetOnlineUrl() == "https://meet.example/x" && r.GetMeetingPoint() == nil
			}},
		{`{"mode":"in_person","meeting_point":{"lat":38.4,"lng":27.1},"city":"İzmir","district":"Konak"}`, []string{"location"},
			func(r *corev1.UpdateEventRequest) bool {
				return r.GetMeetingPoint().GetLat() == 38.4 && r.GetCity() == "İzmir"
			}},
		{`{"fields":{"game":"Okey","players":4}}`, []string{"fields"},
			func(r *corev1.UpdateEventRequest) bool {
				return len(r.GetFields()) == 2 && r.GetFields()["game"] == `"Okey"` && r.GetFields()["players"] == `4`
			}},
		// An empty object removes every value; null leaves them alone.
		{`{"fields":{}}`, []string{"fields"}, func(r *corev1.UpdateEventRequest) bool { return len(r.GetFields()) == 0 }},
		{`{"title":"Yeni","fields":null}`, []string{"title"}, nil},
	} {
		w := as(h, "PATCH", one, tc.body)
		got, _, _ := f.last()
		req, _ := got.(*corev1.UpdateEventRequest)
		if w.Code != 200 || req == nil || !slices.Equal(req.GetUpdateMask().GetPaths(), tc.paths) || req.GetEventId() != eventID ||
			(tc.check != nil && !tc.check(req)) {
			t.Errorf("%s: %d %v", tc.body, w.Code, got)
		}
	}
}

func TestEventWritesNeedClientHeader(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	one := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	for _, rt := range []struct{ method, path string }{
		{"POST", "/v1/tayfas/" + tayfaID + "/events"}, {"PATCH", one}, {"POST", one + "/cancellation"},
		{"PUT", one + "/rsvp"}, {"PUT", one + "/attendees/" + userID + "/attendance"}, {"DELETE", one + "/attendees/" + userID + "/attendance"},
		{"POST", one + "/check-in"},
		{"POST", "/v1/tayfas/" + tayfaID + "/event-series/" + seriesID + "/ending"},
	} {
		w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"})
		if w.Code != 400 || problemType(t, w) != "missing-client-header" {
			t.Errorf("%s %s: %d", rt.method, rt.path, w.Code)
		}
	}
	for _, path := range []string{one, one + "/calendar", one + "/attendees"} {
		if w := do(h, "GET", path, "", map[string]string{}); w.Code != http.StatusUnauthorized {
			t.Errorf("token'sız okuma %s: %d", path, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("istek core'a gitti")
	}
}

// TestLocationNeverLogged sends coordinates through every path a check-in
// can take (success, core refusals, a core error whose message
// carries them, bad bodies, the IP limit) and checks that no log line, from
// the handlers or the access log, and no problem response contains them.
func TestLocationNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := &fakeCore{}
	d := testCoreDeps(f, 100)
	d.Log = log
	// Exactly the check-in attempts below before "sınır".
	d.CheckInLimit = ratelimit.Every(time.Hour, 9)
	// The same middleware the service puts around every HTTP server.
	h := httpx.Chain(coreRouterWith(d), httpx.RequestID(), httpx.AccessLog(log), httpx.Recover(log))

	one := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	loc := `{"lat":` + secretLat + `,"lng":` + secretLng + `}`
	query := "?lat=" + secretLat + "&lng=" + secretLng
	leakyErr := status.Error(codes.Internal, "konum "+secretLat+","+secretLng+" uzak")
	for _, tc := range []struct {
		name, path, body string
		err              error
	}{
		{"başarılı", one + "/check-in", `{"location":` + loc + `}`, nil},
		{"uzak", one + "/check-in", `{"location":` + loc + `}`, coreErr(codes.FailedPrecondition, "ERROR_REASON_CHECK_IN_TOO_FAR")},
		{"deneme bitti", one + "/check-in", `{"location":` + loc + `}`, coreErr(codes.ResourceExhausted, "ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED")},
		{"doğrulama", one + "/check-in", `{"location":` + loc + `}`, coreErr(codes.InvalidArgument, "ERROR_REASON_VALIDATION")},
		{"iç hata", one + "/check-in", `{"location":` + loc + `}`, leakyErr},
		{"bozuk gövde", one + "/check-in", `{"location":{"lat":` + secretLat + `,"lng":"` + secretLng + `"}}`, nil},
		{"aralık dışı", one + "/check-in", `{"location":{"lat":1` + secretLat + `,"lng":` + secretLng + `}}`, nil},
		{"bilinmeyen alan", one + "/check-in", `{"location":` + loc + `,"` + secretLat + `":1}`, nil},
		{"sorguda konum", one + "/check-in" + query, `{}`, leakyErr},
		{"sınır", one + "/check-in", `{"location":` + loc + `}`, nil},
	} {
		f.mu.Lock()
		f.err = tc.err
		f.mu.Unlock()
		w := as(h, "POST", tc.path, tc.body, "CF-Connecting-IP", "203.0.113.7")
		if w.Code >= 400 && (strings.Contains(w.Body.String(), secretLat) || strings.Contains(w.Body.String(), secretLng)) {
			t.Errorf("%s: konum hata yanıtına girdi: %s", tc.name, w.Body)
		}
		if tc.name == "sınır" && w.Code != 429 {
			t.Errorf("IP sınırı uygulanmadı: %d", w.Code)
		}
	}
	// A location in the query string is never read.
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	d.CheckInLimit = ratelimit.New(60, 6)
	h2 := coreRouterWith(d)
	if w := as(h2, "POST", one+"/check-in"+query, `{}`); w.Code != 200 {
		t.Fatalf("sorgulu check-in: %d", w.Code)
	}
	if got, _, _ := f.last(); got.(*corev1.CheckInRequest).GetLocation() != nil {
		t.Fatal("sorgudaki konum core'a gitti")
	}

	out := logs.String()
	if !strings.Contains(out, `"route":"POST /v1/tayfas/{tayfa_id}/events/{event_id}/check-in"`) || !strings.Contains(out, "servis çağrısı başarısız") {
		t.Fatalf("log yakalanmadı: %s", out)
	}
	for _, s := range []string{secretLat, secretLng, "9876543", "1234567"} {
		if strings.Contains(out, s) {
			t.Fatalf("konum log'a girdi (%s): %s", s, out)
		}
	}
}

func TestCheckInLimitedPerIP(t *testing.T) {
	f := &fakeCore{}
	d := testCoreDeps(f, 100)
	d.CheckInLimit = ratelimit.New(60, 2)
	h := coreRouterWith(d)
	path := "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/check-in"
	for range 2 {
		if w := as(h, "POST", path, `{}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 200 {
			t.Fatalf("check-in: %d", w.Code)
		}
	}
	// Bozuk gövde de hakkı harcar; sınır gövde okunmadan uygulanır.
	if w := as(h, "POST", path, `{`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 429 || problemType(t, w) != "rate-limited" {
		t.Fatalf("sınır aşıldı ama kod %d", w.Code)
	}
	if w := as(h, "POST", path, `{}`, "CF-Connecting-IP", "198.51.100.9"); w.Code != 200 {
		t.Fatalf("farklı IP etkilendi: %d", w.Code)
	}
	// Diğer etkinlik uçları bu sınırı harcamaz.
	if w := as(h, "PUT", "/v1/tayfas/"+tayfaID+"/events/"+eventID+"/rsvp", `{"status":"going"}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 200 {
		t.Fatalf("rsvp: %d", w.Code)
	}
	if _, _, n := f.last(); n != 4 {
		t.Fatalf("core çağrısı %d", n)
	}
}

func TestExportEventCalendar(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	path := "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/calendar"
	w := as(h, "GET", path, "")
	hdr := w.Header()
	if w.Code != 200 || w.Body.String() != "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" {
		t.Fatalf("takvim: %d %q", w.Code, w.Body)
	}
	// core'un söylediği tür (text/html) değil, sabit tür kullanılır.
	if hdr.Get("Content-Type") != "text/calendar; charset=utf-8" || hdr.Get("Cache-Control") != "private, no-store" ||
		hdr.Get("Content-Disposition") != `attachment; filename="tayfam-`+eventID+`.ics"` || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("başlıklar: %v", hdr)
	}
	w = as(coreRouter(&fakeCore{err: coreErr(codes.NotFound, "ERROR_REASON_EVENT_NOT_FOUND")}), "GET", path, "")
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/problem+json" || problemType(t, w) != "event-not-found" {
		t.Fatalf("hata: %d %v", w.Code, w.Header())
	}
}

func TestEventProblems(t *testing.T) {
	one := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	for _, tc := range []struct {
		method, path, body, reason string
		code                       int
		typ                        string
	}{
		{"GET", one, "", "ERROR_REASON_MODULE_DISABLED", 409, "module-disabled"},
		{"GET", one, "", "ERROR_REASON_EVENT_NOT_FOUND", 404, "event-not-found"},
		{"PUT", one + "/rsvp", `{"status":"going"}`, "ERROR_REASON_EVENT_CANCELLED", 409, "event-cancelled"},
		{"POST", one + "/check-in", `{}`, "ERROR_REASON_CHECK_IN_CLOSED", 409, "check-in-closed"},
		{"POST", one + "/check-in", `{}`, "ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED", 429, "check-in-attempts-exhausted"},
		{"PUT", one + "/attendees/" + userID + "/attendance", "", "ERROR_REASON_CORRECTION_CLOSED", 409, "correction-closed"},
		{"POST", one + "/check-in", `{}`, "ERROR_REASON_NOT_ATTENDED", 409, "not-attended"},
		{"POST", "/v1/tayfas/" + tayfaID + "/event-series/" + seriesID + "/ending", "", "ERROR_REASON_EVENT_SERIES_ENDED", 409, "event-series-ended"},
		{"GET", "/v1/tayfas/" + tayfaID + "/event-series/" + seriesID, "", "ERROR_REASON_EVENT_SERIES_NOT_FOUND", 404, "event-series-not-found"},
	} {
		w := as(coreRouter(&fakeCore{err: coreErr(codes.Unknown, tc.reason)}), tc.method, tc.path, tc.body)
		if w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%s %s %s: %d %s", tc.method, tc.path, tc.reason, w.Code, problemType(t, w))
		}
	}
}

// Without an EventClient the routes are not served.
func TestEventRoutesOptional(t *testing.T) {
	d := testCoreDeps(&fakeCore{}, 100)
	d.Events = nil
	if w := as(coreRouterWith(d), "GET", "/v1/tayfas/"+tayfaID+"/events", ""); w.Code != 404 {
		t.Fatalf("kod %d", w.Code)
	}
}

func TestTayfaMembersCanCreateEvents(t *testing.T) {
	tf := testTayfa()
	tf.MembersCanCreateEvents = true
	f := &fakeCore{tayfa: tf}
	h := coreRouter(f)
	w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"members_can_create_events":true}`)
	got, _, _ := f.last()
	req := got.(*corev1.UpdateTayfaRequest)
	if w.Code != 200 || !slices.Equal(req.GetUpdateMask().GetPaths(), []string{"members_can_create_events"}) || !req.GetMembersCanCreateEvents() {
		t.Fatalf("güncelleme: %d %v", w.Code, req)
	}
	if !decodeBody[tayfaJSON](t, w).MembersCanCreateEvents {
		t.Fatalf("yanıt: %s", w.Body)
	}
	w = as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"members_can_create_events":false}`)
	if got, _, _ := f.last(); w.Code != 200 || got.(*corev1.UpdateTayfaRequest).GetMembersCanCreateEvents() {
		t.Fatalf("kapatma: %d %v", w.Code, got)
	}
	// Varsayılan (false) da yanıtta yazılır.
	w = as(coreRouter(&fakeCore{}), "GET", "/v1/tayfas/"+tayfaID, "")
	if !strings.Contains(w.Body.String(), `"members_can_create_events":false`) {
		t.Fatalf("alan yok: %s", w.Body)
	}
}

// An event answers with its catalog type, the version it was created with
// and the values of the type's fields (issue #237).
func TestEventTypeVersionAndFields(t *testing.T) {
	ev := testEvent()
	ev.Type, ev.TypeKey, ev.TypeVersion = corev1.EventType_EVENT_TYPE_UNSPECIFIED, "board_game_night", 3
	ev.Fields = map[string]string{"game": `"Tavla"`, "players": `4`, "langs": `["tr","en"]`, "rated": `true`, "bozuk": `{"a":`}
	h := coreRouter(&fakeCore{event: ev})
	w := as(h, "GET", "/v1/tayfas/"+tayfaID+"/events/"+eventID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("etkinlik: %d %s", w.Code, w.Body)
	}
	v := decodeBody[eventJSON](t, w)
	if v.Type != "board_game_night" || v.TypeVersion != 3 || len(v.Fields) != 4 ||
		string(v.Fields["game"]) != `"Tavla"` || string(v.Fields["players"]) != `4` || string(v.Fields["langs"]) != `["tr","en"]` || string(v.Fields["rated"]) != `true` {
		t.Fatalf("gövde: %s", w.Body)
	}
	// Values are JSON in the body, not JSON inside a string.
	if !strings.Contains(w.Body.String(), `"players":4`) || strings.Contains(w.Body.String(), "bozuk") {
		t.Fatalf("alan değerleri: %s", w.Body)
	}

	// A Faz 1 event: the key of its enum, version from core, no fields.
	w = as(coreRouter(&fakeCore{}), "GET", "/v1/tayfas/"+tayfaID+"/events/"+eventID, "")
	if v := decodeBody[eventJSON](t, w); v.Type != "convoy" || v.Fields != nil || strings.Contains(w.Body.String(), `"fields"`) {
		t.Fatalf("Faz 1 etkinliği: %s", w.Body)
	}
}

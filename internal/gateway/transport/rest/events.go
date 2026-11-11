package rest

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// EventClient is core's EventService as the gateway uses it.
type EventClient interface {
	CreateEvent(ctx context.Context, in *corev1.CreateEventRequest, opts ...grpc.CallOption) (*corev1.CreateEventResponse, error)
	UpdateEvent(ctx context.Context, in *corev1.UpdateEventRequest, opts ...grpc.CallOption) (*corev1.UpdateEventResponse, error)
	CancelEvent(ctx context.Context, in *corev1.CancelEventRequest, opts ...grpc.CallOption) (*corev1.CancelEventResponse, error)
	GetEvent(ctx context.Context, in *corev1.GetEventRequest, opts ...grpc.CallOption) (*corev1.GetEventResponse, error)
	ListUpcomingEvents(ctx context.Context, in *corev1.ListUpcomingEventsRequest, opts ...grpc.CallOption) (*corev1.ListUpcomingEventsResponse, error)
	SetRsvp(ctx context.Context, in *corev1.SetRsvpRequest, opts ...grpc.CallOption) (*corev1.SetRsvpResponse, error)
	ListAttendees(ctx context.Context, in *corev1.ListAttendeesRequest, opts ...grpc.CallOption) (*corev1.ListAttendeesResponse, error)
	AddCampItem(ctx context.Context, in *corev1.AddCampItemRequest, opts ...grpc.CallOption) (*corev1.AddCampItemResponse, error)
	RemoveCampItem(ctx context.Context, in *corev1.RemoveCampItemRequest, opts ...grpc.CallOption) (*corev1.RemoveCampItemResponse, error)
	AddConvoyVehicle(ctx context.Context, in *corev1.AddConvoyVehicleRequest, opts ...grpc.CallOption) (*corev1.AddConvoyVehicleResponse, error)
	RemoveConvoyVehicle(ctx context.Context, in *corev1.RemoveConvoyVehicleRequest, opts ...grpc.CallOption) (*corev1.RemoveConvoyVehicleResponse, error)
	CheckIn(ctx context.Context, in *corev1.CheckInRequest, opts ...grpc.CallOption) (*corev1.CheckInResponse, error)
	StartHike(ctx context.Context, in *corev1.StartHikeRequest, opts ...grpc.CallOption) (*corev1.StartHikeResponse, error)
	FinishHike(ctx context.Context, in *corev1.FinishHikeRequest, opts ...grpc.CallOption) (*corev1.FinishHikeResponse, error)
	SetAttendance(ctx context.Context, in *corev1.SetAttendanceRequest, opts ...grpc.CallOption) (*corev1.SetAttendanceResponse, error)
	AddFishingCatch(ctx context.Context, in *corev1.AddFishingCatchRequest, opts ...grpc.CallOption) (*corev1.AddFishingCatchResponse, error)
	ListFishingCatches(ctx context.Context, in *corev1.ListFishingCatchesRequest, opts ...grpc.CallOption) (*corev1.ListFishingCatchesResponse, error)
	RemoveFishingCatch(ctx context.Context, in *corev1.RemoveFishingCatchRequest, opts ...grpc.CallOption) (*corev1.RemoveFishingCatchResponse, error)
	GetEventSeries(ctx context.Context, in *corev1.GetEventSeriesRequest, opts ...grpc.CallOption) (*corev1.GetEventSeriesResponse, error)
	EndEventSeries(ctx context.Context, in *corev1.EndEventSeriesRequest, opts ...grpc.CallOption) (*corev1.EndEventSeriesResponse, error)
	ExportEvent(ctx context.Context, in *corev1.ExportEventRequest, opts ...grpc.CallOption) (*corev1.ExportEventResponse, error)
}

const (
	// maxEventBody fits 5000 characters of description in the worst JSON
	// escaping plus a 500-point convoy route.
	maxEventBody      = 160 << 10
	eventTitleMax     = 120
	eventDescMax      = 5000
	eventCapacityMax  = 10000
	convoyRouteMax    = 500
	rruleMax          = 256
	timeZoneMax       = 64
	cancelReasonMax   = 500
	campItemNameMax   = 60
	campQuantityMax   = 999
	vehicleMakeMax    = 40
	vehicleColorMax   = 30
	vehiclePlateMax   = 16
	catchSpeciesMax   = 60
	catchLengthMaxCm  = 1000
	calendarMediaType = "text/calendar; charset=utf-8"
)

func registerEvents(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	const ev = "/v1/tayfas/{tayfa_id}/events"
	const one = ev + "/{event_id}"
	mux.HandleFunc("GET "+ev, a(h.listEvents))
	mux.HandleFunc("POST "+ev, a(h.createEvent))
	mux.HandleFunc("GET "+one, a(h.getEvent))
	mux.HandleFunc("PATCH "+one, a(h.updateEvent))
	mux.HandleFunc("POST "+one+"/cancellation", a(h.cancelEvent))
	mux.HandleFunc("GET "+one+"/calendar", a(h.exportEvent))
	mux.HandleFunc("PUT "+one+"/rsvp", a(h.setRsvp))
	mux.HandleFunc("GET "+one+"/attendees", a(h.listAttendees))
	mux.HandleFunc("PUT "+one+"/attendees/{user_id}/attendance", a(h.setAttendance(true)))
	mux.HandleFunc("DELETE "+one+"/attendees/{user_id}/attendance", a(h.setAttendance(false)))
	mux.HandleFunc("POST "+one+"/check-in", a(h.checkIn))
	mux.HandleFunc("POST "+one+"/hike-start", a(h.startHike))
	mux.HandleFunc("POST "+one+"/hike-return", a(h.finishHike))
	mux.HandleFunc("POST "+one+"/camp-items", a(h.addCampItem))
	mux.HandleFunc("DELETE "+one+"/camp-items/{item_id}", a(h.removeCampItem))
	mux.HandleFunc("POST "+one+"/convoy-vehicles", a(h.addConvoyVehicle))
	mux.HandleFunc("DELETE "+one+"/convoy-vehicles/{vehicle_id}", a(h.removeConvoyVehicle))
	mux.HandleFunc("GET "+one+"/fishing-catches", a(h.listFishingCatches))
	mux.HandleFunc("POST "+one+"/fishing-catches", a(h.addFishingCatch))
	mux.HandleFunc("DELETE "+one+"/fishing-catches/{catch_id}", a(h.removeFishingCatch))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/event-series/{series_id}", a(h.getEventSeries))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/event-series/{series_id}/ending", a(h.endEventSeries))
}

// pathIDs reads the named UUID path parameters in order.
func pathIDs(w http.ResponseWriter, r *http.Request, names ...string) ([]string, bool) {
	out := make([]string, len(names))
	for i, n := range names {
		v, ok := pathUUID(w, r, n)
		if !ok {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

func pathTayfaEvent(w http.ResponseWriter, r *http.Request) (tayfaID, eventID string, ok bool) {
	ids, ok := pathIDs(w, r, "tayfa_id", "event_id")
	if !ok {
		return "", "", false
	}
	return ids[0], ids[1], true
}

// runes counts characters after trimming surrounding spaces, as core does.
func runes(s string) int { return utf8.RuneCountInString(strings.TrimSpace(s)) }

// --- Input: type details ---

type convoyInput struct {
	Route []latLngJSON `json:"route"`
}

type campInput struct {
	SiteName  string `json:"site_name"`
	HasWater  *bool  `json:"has_water"`
	HasToilet *bool  `json:"has_toilet"`
	Notes     string `json:"notes"`
}

// eventDetailsInput is the type details of a create or update body: at most
// one is set. Output-only fields (vehicles, camp items) are unknown fields
// here and fail decoding.
type eventDetailsInput struct {
	Convoy  *convoyInput `json:"convoy"`
	Camp    *campInput   `json:"camp"`
	Hike    *hikeJSON    `json:"hike"`
	Fishing *fishingJSON `json:"fishing"`
	Gaming  *gamingJSON  `json:"gaming"`
	Sports  *sportsJSON  `json:"sports"`
}

// eventDetails is the checked details: the type they belong to and the
// proto message, or the zero value when none was given.
type eventDetails struct {
	typ     corev1.EventType
	convoy  *corev1.ConvoyDetails
	camp    *corev1.CampDetails
	hike    *corev1.HikeDetails
	fishing *corev1.FishingDetails
	gaming  *corev1.GamingDetails
	sports  *corev1.SportsDetails
}

// details checks the cheap rules (enum names, coordinates, counts) and
// converts. core checks everything again.
func (in eventDetailsInput) details() (eventDetails, bool) {
	var d eventDetails
	n := 0
	if in.Convoy != nil {
		n++
		if len(in.Convoy.Route) > convoyRouteMax {
			return d, false
		}
		d.typ, d.convoy = corev1.EventType_EVENT_TYPE_CONVOY, &corev1.ConvoyDetails{}
		for _, p := range in.Convoy.Route {
			if !p.valid() {
				return d, false
			}
			d.convoy.Route = append(d.convoy.Route, &corev1.LatLng{Lat: p.Lat, Lng: p.Lng})
		}
	}
	if in.Camp != nil {
		n++
		d.typ, d.camp = corev1.EventType_EVENT_TYPE_CAMP, &corev1.CampDetails{
			SiteName: in.Camp.SiteName, HasWater: in.Camp.HasWater, HasToilet: in.Camp.HasToilet, Notes: in.Camp.Notes,
		}
	}
	if in.Hike != nil {
		n++
		d.typ, d.hike = corev1.EventType_EVENT_TYPE_HIKE, &corev1.HikeDetails{
			Difficulty: in.Hike.Difficulty, DistanceKm: in.Hike.DistanceKm, ElevationM: in.Hike.ElevationM,
			ExpectedReturnTime: optTimestamp(in.Hike.ExpectedReturnAt), ReturnGraceMinutes: in.Hike.ReturnGraceMinutes,
		}
	}
	if in.Fishing != nil {
		n++
		v, ok := spotVisibilities.valueOrUnspecified(in.Fishing.SpotVisibility)
		if !ok {
			return d, false
		}
		d.typ, d.fishing = corev1.EventType_EVENT_TYPE_FISHING, &corev1.FishingDetails{SpotVisibility: v}
	}
	if in.Gaming != nil {
		n++
		p, ok := gamingPlatforms.value(in.Gaming.Platform)
		if !ok {
			return d, false
		}
		d.typ, d.gaming = corev1.EventType_EVENT_TYPE_GAMING, &corev1.GamingDetails{Game: in.Gaming.Game, Platform: p}
	}
	if in.Sports != nil {
		n++
		s, ok := sports.value(in.Sports.Sport)
		if !ok {
			return d, false
		}
		d.typ, d.sports = corev1.EventType_EVENT_TYPE_SPORTS, &corev1.SportsDetails{Sport: s, VenueName: in.Sports.VenueName}
	}
	return d, n <= 1
}

// setCreate sets req's type details, if any.
func (d eventDetails) setCreate(req *corev1.CreateEventRequest) {
	switch {
	case d.convoy != nil:
		req.Details = &corev1.CreateEventRequest_Convoy{Convoy: d.convoy}
	case d.camp != nil:
		req.Details = &corev1.CreateEventRequest_Camp{Camp: d.camp}
	case d.hike != nil:
		req.Details = &corev1.CreateEventRequest_Hike{Hike: d.hike}
	case d.fishing != nil:
		req.Details = &corev1.CreateEventRequest_Fishing{Fishing: d.fishing}
	case d.gaming != nil:
		req.Details = &corev1.CreateEventRequest_Gaming{Gaming: d.gaming}
	case d.sports != nil:
		req.Details = &corev1.CreateEventRequest_Sports{Sports: d.sports}
	}
}

// setUpdate sets req's type details, if any.
func (d eventDetails) setUpdate(req *corev1.UpdateEventRequest) {
	switch {
	case d.convoy != nil:
		req.Details = &corev1.UpdateEventRequest_Convoy{Convoy: d.convoy}
	case d.camp != nil:
		req.Details = &corev1.UpdateEventRequest_Camp{Camp: d.camp}
	case d.hike != nil:
		req.Details = &corev1.UpdateEventRequest_Hike{Hike: d.hike}
	case d.fishing != nil:
		req.Details = &corev1.UpdateEventRequest_Fishing{Fishing: d.fishing}
	case d.gaming != nil:
		req.Details = &corev1.UpdateEventRequest_Gaming{Gaming: d.gaming}
	case d.sports != nil:
		req.Details = &corev1.UpdateEventRequest_Sports{Sports: d.sports}
	}
}

func eventTitleOK(s string) bool { n := runes(s); return n >= 1 && n <= eventTitleMax }

func eventDescOK(s string) bool { return runes(s) <= eventDescMax }

func capacityOK(n int32) bool { return n >= 0 && n <= eventCapacityMax }

func pointOK(p *latLngJSON) bool { return p == nil || p.valid() }

// --- Events ---

func (h coreHandlers) listEvents(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.ListUpcomingEvents(ctx, &corev1.ListUpcomingEventsRequest{TayfaId: id, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := eventListJSON{Events: make([]eventJSON, 0, len(resp.GetEvents())), NextPageToken: resp.GetNextPageToken()}
	for _, e := range resp.GetEvents() {
		out.Events = append(out.Events, eventFrom(e))
	}
	writeJSON(w, http.StatusOK, out)
}

type recurrenceJSON struct {
	RRule    string `json:"rrule"`
	TimeZone string `json:"time_zone"`
}

type eventCreateBody struct {
	Type             string      `json:"type"`
	Mode             string      `json:"mode"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	StartsAt         time.Time   `json:"starts_at"`
	EndsAt           time.Time   `json:"ends_at"`
	MeetingPointName string      `json:"meeting_point_name"`
	MeetingPoint     *latLngJSON `json:"meeting_point"`
	City             string      `json:"city"`
	District         string      `json:"district"`
	OnlineURL        string      `json:"online_url"`
	Capacity         int32       `json:"capacity"`
	Discoverable     bool        `json:"discoverable"`
	eventDetailsInput
	Recurrence      *recurrenceJSON `json:"recurrence"`
	ClientRequestID string          `json:"client_request_id"`
	CoverUploadID   string          `json:"cover_upload_id"`
}

func (h coreHandlers) createEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b eventCreateBody
	if !decodeLimit(w, r, &b, maxEventBody) {
		return
	}
	typ, okT := eventTypes.value(b.Type)
	mode, okM := eventModes.value(b.Mode)
	det, okD := b.details()
	if !okT || !okM || !okD || (det.typ != 0 && det.typ != typ) ||
		!eventTitleOK(b.Title) || !eventDescOK(b.Description) || b.StartsAt.IsZero() || b.EndsAt.IsZero() ||
		!pointOK(b.MeetingPoint) || !capacityOK(b.Capacity) || !validUUID(b.ClientRequestID) ||
		(b.CoverUploadID != "" && !validUUID(b.CoverUploadID)) {
		badRequest(w, r)
		return
	}
	req := &corev1.CreateEventRequest{
		TayfaId: id, Type: typ, Mode: mode, Title: b.Title, Description: b.Description,
		StartTime: timestamppb.New(b.StartsAt), EndTime: timestamppb.New(b.EndsAt),
		MeetingPointName: b.MeetingPointName, MeetingPoint: b.MeetingPoint.proto(),
		City: b.City, District: b.District, OnlineUrl: b.OnlineURL,
		Capacity: b.Capacity, Discoverable: b.Discoverable,
		ClientRequestId: b.ClientRequestID, CoverUploadId: b.CoverUploadID,
	}
	det.setCreate(req)
	if rc := b.Recurrence; rc != nil {
		if rc.RRule == "" || len(rc.RRule) > rruleMax || len(rc.TimeZone) > timeZoneMax {
			badRequest(w, r)
			return
		}
		req.Recurrence = &corev1.Recurrence{Rrule: rc.RRule, TimeZone: rc.TimeZone}
	}
	resp, err := h.d.Events.CreateEvent(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	e := eventFrom(resp.GetEvent())
	w.Header().Set("Location", "/v1/tayfas/"+e.TayfaID+"/events/"+e.ID)
	writeJSON(w, http.StatusCreated, e)
}

func (h coreHandlers) getEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.GetEvent(ctx, &corev1.GetEventRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventFrom(resp.GetEvent()))
}

// eventUpdateBody: a field present in the body is changed; null counts as
// absent. Fields change in core's groups (see updateEvent).
type eventUpdateBody struct {
	Title            *string     `json:"title"`
	Description      *string     `json:"description"`
	StartsAt         *time.Time  `json:"starts_at"`
	EndsAt           *time.Time  `json:"ends_at"`
	Mode             *string     `json:"mode"`
	MeetingPointName *string     `json:"meeting_point_name"`
	MeetingPoint     *latLngJSON `json:"meeting_point"`
	City             *string     `json:"city"`
	District         *string     `json:"district"`
	OnlineURL        *string     `json:"online_url"`
	Capacity         *int32      `json:"capacity"`
	Discoverable     *bool       `json:"discoverable"`
	eventDetailsInput
	ApplyToSeries bool `json:"apply_to_series"`
	// CoverUploadID: "" removes the cover (issue #135).
	CoverUploadID *string `json:"cover_upload_id"`
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (h coreHandlers) updateEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b eventUpdateBody
	if !decodeLimit(w, r, &b, maxEventBody) {
		return
	}
	req := &corev1.UpdateEventRequest{TayfaId: id, EventId: eid, UpdateMask: &fieldmaskpb.FieldMask{}, Series: b.ApplyToSeries}
	add := func(path string) { req.UpdateMask.Paths = append(req.UpdateMask.Paths, path) }
	if b.Title != nil {
		if !eventTitleOK(*b.Title) {
			badRequest(w, r)
			return
		}
		req.Title = *b.Title
		add("title")
	}
	if b.Description != nil {
		if !eventDescOK(*b.Description) {
			badRequest(w, r)
			return
		}
		req.Description = *b.Description
		add("description")
	}
	// "time": start and end together.
	if b.StartsAt != nil || b.EndsAt != nil {
		if b.StartsAt == nil || b.EndsAt == nil || b.StartsAt.IsZero() || b.EndsAt.IsZero() {
			badRequest(w, r)
			return
		}
		req.StartTime, req.EndTime = timestamppb.New(*b.StartsAt), timestamppb.New(*b.EndsAt)
		add("time")
	}
	// "location": every location field together, mode required; the ones
	// left out become empty.
	if b.Mode != nil || b.MeetingPointName != nil || b.MeetingPoint != nil || b.City != nil || b.District != nil || b.OnlineURL != nil {
		mode, ok := eventModes.value(deref(b.Mode))
		if !ok || !pointOK(b.MeetingPoint) {
			badRequest(w, r)
			return
		}
		req.Mode, req.MeetingPointName, req.MeetingPoint = mode, deref(b.MeetingPointName), b.MeetingPoint.proto()
		req.City, req.District, req.OnlineUrl = deref(b.City), deref(b.District), deref(b.OnlineURL)
		add("location")
	}
	if b.Capacity != nil {
		if !capacityOK(*b.Capacity) {
			badRequest(w, r)
			return
		}
		req.Capacity = *b.Capacity
		add("capacity")
	}
	if b.Discoverable != nil {
		req.Discoverable = *b.Discoverable
		add("discoverable")
	}
	det, ok := b.details()
	if !ok {
		badRequest(w, r)
		return
	}
	if det.typ != 0 {
		det.setUpdate(req)
		add("details")
	}
	if b.CoverUploadID != nil {
		if *b.CoverUploadID != "" && !validUUID(*b.CoverUploadID) {
			badRequest(w, r)
			return
		}
		req.CoverUploadId = *b.CoverUploadID
		add("cover_upload_id")
	}
	if len(req.UpdateMask.Paths) == 0 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.UpdateEvent(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventFrom(resp.GetEvent()))
}

type eventCancelBody struct {
	Reason string `json:"reason"`
}

func (h coreHandlers) cancelEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b eventCancelBody
	if !decode(w, r, &b) {
		return
	}
	if runes(b.Reason) > cancelReasonMax {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.CancelEvent(ctx, &corev1.CancelEventRequest{TayfaId: id, EventId: eid, Reason: b.Reason})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventFrom(resp.GetEvent()))
}

// exportEvent serves the event as an .ics file. It may carry the online
// link, so no cache keeps it; the media type is fixed here, not taken from
// core.
func (h coreHandlers) exportEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.ExportEvent(ctx, &corev1.ExportEventRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", calendarMediaType)
	// eid is a checked UUID, safe inside the quoted file name.
	hdr.Set("Content-Disposition", `attachment; filename="tayfam-`+eid+`.ics"`)
	hdr.Set("Cache-Control", "private, no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.GetIcs())
}

// --- RSVP and attendees ---

type rsvpBody struct {
	Status string `json:"status"`
}

func (h coreHandlers) setRsvp(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b rsvpBody
	if !decode(w, r, &b) {
		return
	}
	st, ok := rsvpStatuses.value(b.Status)
	if !ok || st == corev1.RsvpStatus_RSVP_STATUS_WAITLIST {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: id, EventId: eid, Status: st})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rsvpJSON{rsvpStatuses.name(resp.GetStatus())})
}

func (h coreHandlers) listAttendees(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	st := corev1.RsvpStatus_RSVP_STATUS_GOING
	if s := r.URL.Query().Get("status"); s != "" {
		if st, ok = rsvpStatuses.value(s); !ok {
			badRequest(w, r)
			return
		}
	}
	resp, err := h.d.Events.ListAttendees(ctx, &corev1.ListAttendeesRequest{TayfaId: id, EventId: eid, Status: st, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := attendeeListJSON{Attendees: make([]attendeeJSON, 0, len(resp.GetAttendees())), NextPageToken: resp.GetNextPageToken()}
	for _, a := range resp.GetAttendees() {
		out.Attendees = append(out.Attendees, attendeeJSON{
			UserID: a.GetUserId(), Status: rsvpStatuses.name(a.GetStatus()), UpdatedAt: a.GetUpdateTime().AsTime(),
			Attendance:       optName(attendanceStatuses, a.GetAttendance()),
			AttendanceMethod: optName(attendanceMethods, a.GetAttendanceMethod()),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// setAttendance is the organizer's correction: PUT adds, DELETE removes.
func (h coreHandlers) setAttendance(attended bool) coreHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		ids, ok := pathIDs(w, r, "tayfa_id", "event_id", "user_id")
		if !ok {
			return
		}
		if _, err := h.d.Events.SetAttendance(ctx, &corev1.SetAttendanceRequest{TayfaId: ids[0], EventId: ids[1], UserId: ids[2], Attended: attended}); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Attendance ---

type checkInBody struct {
	Location *latLngJSON `json:"location"`
}

// checkIn passes the caller's position to core in the body only. Nothing
// here logs it or puts it in an error: problems carry only their type.
func (h coreHandlers) checkIn(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Limited before the body is read, so malformed attempts count too; core
	// also counts five attempts per event and caller.
	if !h.d.CheckInLimit.Allow(clientIP(r, h.d.TrustCFConnectingIP)) {
		rateLimited(w, r)
		return
	}
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b checkInBody
	if !decode(w, r, &b) {
		return
	}
	if !pointOK(b.Location) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.CheckIn(ctx, &corev1.CheckInRequest{TayfaId: id, EventId: eid, Location: b.Location.proto()})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, checkInJSON{attendanceStatuses.name(resp.GetAttendance()), attendanceMethods.name(resp.GetMethod())})
}

type hikeStartBody struct {
	StartedAt *time.Time `json:"started_at"`
}

func (h coreHandlers) startHike(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b hikeStartBody
	if !decode(w, r, &b) {
		return
	}
	resp, err := h.d.Events.StartHike(ctx, &corev1.StartHikeRequest{TayfaId: id, EventId: eid, StartTime: optTimestamp(b.StartedAt)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hikeCheckinFrom(resp.GetCheckin()))
}

type hikeReturnBody struct {
	ReturnedAt *time.Time `json:"returned_at"`
}

func (h coreHandlers) finishHike(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b hikeReturnBody
	if !decode(w, r, &b) {
		return
	}
	resp, err := h.d.Events.FinishHike(ctx, &corev1.FinishHikeRequest{TayfaId: id, EventId: eid, ReturnTime: optTimestamp(b.ReturnedAt)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hikeCheckinFrom(resp.GetCheckin()))
}

// --- Camp items and convoy vehicles ---

type campItemBody struct {
	Name           string `json:"name"`
	Quantity       int32  `json:"quantity"`
	AssignedUserID string `json:"assigned_user_id"`
}

func (h coreHandlers) addCampItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b campItemBody
	if !decode(w, r, &b) {
		return
	}
	if n := runes(b.Name); n < 1 || n > campItemNameMax || b.Quantity < 0 || b.Quantity > campQuantityMax ||
		(b.AssignedUserID != "" && !validUUID(b.AssignedUserID)) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.AddCampItem(ctx, &corev1.AddCampItemRequest{
		TayfaId: id, EventId: eid, Name: b.Name, Quantity: b.Quantity, AssignedUserId: b.AssignedUserID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, campItemFrom(resp.GetItem()))
}

func (h coreHandlers) removeCampItem(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "event_id", "item_id")
	if !ok {
		return
	}
	if _, err := h.d.Events.RemoveCampItem(ctx, &corev1.RemoveCampItemRequest{TayfaId: ids[0], EventId: ids[1], ItemId: ids[2]}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type convoyVehicleBody struct {
	Make      string `json:"make"`
	Model     string `json:"model"`
	ModelYear int32  `json:"model_year"`
	Color     string `json:"color"`
	Plate     string `json:"plate"`
}

func (h coreHandlers) addConvoyVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b convoyVehicleBody
	if !decode(w, r, &b) {
		return
	}
	if n := runes(b.Make); n < 1 || n > vehicleMakeMax || runes(b.Model) > vehicleMakeMax ||
		runes(b.Color) > vehicleColorMax || utf8.RuneCountInString(b.Plate) > vehiclePlateMax {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.AddConvoyVehicle(ctx, &corev1.AddConvoyVehicleRequest{
		TayfaId: id, EventId: eid, Make: b.Make, Model: b.Model, ModelYear: b.ModelYear, Color: b.Color, Plate: b.Plate,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, convoyVehicleFrom(resp.GetVehicle()))
}

func (h coreHandlers) removeConvoyVehicle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "event_id", "vehicle_id")
	if !ok {
		return
	}
	if _, err := h.d.Events.RemoveConvoyVehicle(ctx, &corev1.RemoveConvoyVehicleRequest{TayfaId: ids[0], EventId: ids[1], VehicleId: ids[2]}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Fishing log ---

func (h coreHandlers) listFishingCatches(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.ListFishingCatches(ctx, &corev1.ListFishingCatchesRequest{TayfaId: id, EventId: eid, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := fishingCatchListJSON{Catches: make([]fishingCatchJSON, 0, len(resp.GetCatches())), NextPageToken: resp.GetNextPageToken()}
	for _, c := range resp.GetCatches() {
		out.Catches = append(out.Catches, fishingCatchFrom(c))
	}
	writeJSON(w, http.StatusOK, out)
}

type fishingCatchBody struct {
	Species         string      `json:"species"`
	LengthCm        float64     `json:"length_cm"`
	Location        *latLngJSON `json:"location"`
	Visibility      string      `json:"visibility"`
	ClientRequestID string      `json:"client_request_id"`
	PhotoUploadID   string      `json:"photo_upload_id"`
}

// addFishingCatch passes the catch's location to core in the body only,
// like checkIn.
func (h coreHandlers) addFishingCatch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b fishingCatchBody
	if !decode(w, r, &b) {
		return
	}
	vis, okV := catchVisibilities.valueOrUnspecified(b.Visibility)
	if n := runes(b.Species); !okV || n < 1 || n > catchSpeciesMax || !(b.LengthCm > 0 && b.LengthCm <= catchLengthMaxCm) ||
		!pointOK(b.Location) || !validUUID(b.ClientRequestID) || (b.PhotoUploadID != "" && !validUUID(b.PhotoUploadID)) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.AddFishingCatch(ctx, &corev1.AddFishingCatchRequest{
		TayfaId: id, EventId: eid, Species: b.Species, LengthCm: b.LengthCm,
		Location: b.Location.proto(), Visibility: vis, ClientRequestId: b.ClientRequestID, PhotoUploadId: b.PhotoUploadID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, fishingCatchFrom(resp.GetCatch()))
}

func (h coreHandlers) removeFishingCatch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "event_id", "catch_id")
	if !ok {
		return
	}
	if _, err := h.d.Events.RemoveFishingCatch(ctx, &corev1.RemoveFishingCatchRequest{TayfaId: ids[0], EventId: ids[1], CatchId: ids[2]}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Series ---

func (h coreHandlers) getEventSeries(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "series_id")
	if !ok {
		return
	}
	resp, err := h.d.Events.GetEventSeries(ctx, &corev1.GetEventSeriesRequest{TayfaId: ids[0], SeriesId: ids[1]})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventSeriesFrom(resp.GetSeries()))
}

func (h coreHandlers) endEventSeries(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "series_id")
	if !ok {
		return
	}
	resp, err := h.d.Events.EndEventSeries(ctx, &corev1.EndEventSeriesRequest{TayfaId: ids[0], SeriesId: ids[1]})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventSeriesEndingJSON{eventSeriesFrom(resp.GetSeries()), resp.GetCancelledCount()})
}

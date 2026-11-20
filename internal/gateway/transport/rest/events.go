package rest

import (
	"context"
	"encoding/json"
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
	CreateInstantEvent(ctx context.Context, in *corev1.CreateInstantEventRequest, opts ...grpc.CallOption) (*corev1.CreateInstantEventResponse, error)
	UpdateEvent(ctx context.Context, in *corev1.UpdateEventRequest, opts ...grpc.CallOption) (*corev1.UpdateEventResponse, error)
	CancelEvent(ctx context.Context, in *corev1.CancelEventRequest, opts ...grpc.CallOption) (*corev1.CancelEventResponse, error)
	FinishEvent(ctx context.Context, in *corev1.FinishEventRequest, opts ...grpc.CallOption) (*corev1.FinishEventResponse, error)
	CreateEventJoinQr(ctx context.Context, in *corev1.CreateEventJoinQrRequest, opts ...grpc.CallOption) (*corev1.CreateEventJoinQrResponse, error)
	JoinEventByQr(ctx context.Context, in *corev1.JoinEventByQrRequest, opts ...grpc.CallOption) (*corev1.JoinEventByQrResponse, error)
	GetEvent(ctx context.Context, in *corev1.GetEventRequest, opts ...grpc.CallOption) (*corev1.GetEventResponse, error)
	ListUpcomingEvents(ctx context.Context, in *corev1.ListUpcomingEventsRequest, opts ...grpc.CallOption) (*corev1.ListUpcomingEventsResponse, error)
	SetRsvp(ctx context.Context, in *corev1.SetRsvpRequest, opts ...grpc.CallOption) (*corev1.SetRsvpResponse, error)
	ListAttendees(ctx context.Context, in *corev1.ListAttendeesRequest, opts ...grpc.CallOption) (*corev1.ListAttendeesResponse, error)
	CheckIn(ctx context.Context, in *corev1.CheckInRequest, opts ...grpc.CallOption) (*corev1.CheckInResponse, error)
	SetAttendance(ctx context.Context, in *corev1.SetAttendanceRequest, opts ...grpc.CallOption) (*corev1.SetAttendanceResponse, error)
	AddEventPhoto(ctx context.Context, in *corev1.AddEventPhotoRequest, opts ...grpc.CallOption) (*corev1.AddEventPhotoResponse, error)
	ListEventPhotos(ctx context.Context, in *corev1.ListEventPhotosRequest, opts ...grpc.CallOption) (*corev1.ListEventPhotosResponse, error)
	RemoveEventPhoto(ctx context.Context, in *corev1.RemoveEventPhotoRequest, opts ...grpc.CallOption) (*corev1.RemoveEventPhotoResponse, error)
	GetEventSeries(ctx context.Context, in *corev1.GetEventSeriesRequest, opts ...grpc.CallOption) (*corev1.GetEventSeriesResponse, error)
	EndEventSeries(ctx context.Context, in *corev1.EndEventSeriesRequest, opts ...grpc.CallOption) (*corev1.EndEventSeriesResponse, error)
	ExportEvent(ctx context.Context, in *corev1.ExportEventRequest, opts ...grpc.CallOption) (*corev1.ExportEventResponse, error)
}

const (
	// maxEventBody fits 5000 characters of description in the worst JSON
	// escaping.
	maxEventBody     = 160 << 10
	eventTitleMax    = 120
	eventDescMax     = 5000
	eventCapacityMax = 10000
	rruleMax         = 256
	timeZoneMax      = 64
	cancelReasonMax  = 500
	// photoCaptionMax: album photo caption (core trims and checks again).
	photoCaptionMax   = 200
	calendarMediaType = "text/calendar; charset=utf-8"
)

func registerEvents(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	const ev = "/v1/tayfas/{tayfa_id}/events"
	const one = ev + "/{event_id}"
	mux.HandleFunc("GET "+ev, a(h.listEvents))
	mux.HandleFunc("POST "+ev, a(h.createEvent))
	mux.HandleFunc("POST /v1/instant-events", a(h.createInstantEvent))
	mux.HandleFunc("GET "+one, a(h.getEvent))
	mux.HandleFunc("PATCH "+one, a(h.updateEvent))
	mux.HandleFunc("POST "+one+"/cancellation", a(h.cancelEvent))
	mux.HandleFunc("POST "+one+"/finish", a(h.finishEvent))
	mux.HandleFunc("POST "+one+"/join-qr", a(h.createEventJoinQr))
	mux.HandleFunc("POST /v1/join-qr", a(h.joinEventByQr))
	mux.HandleFunc("GET "+one+"/calendar", a(h.exportEvent))
	mux.HandleFunc("PUT "+one+"/rsvp", a(h.setRsvp))
	mux.HandleFunc("GET "+one+"/attendees", a(h.listAttendees))
	mux.HandleFunc("PUT "+one+"/attendees/{user_id}/attendance", a(h.setAttendance(true)))
	mux.HandleFunc("DELETE "+one+"/attendees/{user_id}/attendance", a(h.setAttendance(false)))
	mux.HandleFunc("POST "+one+"/check-in", a(h.checkIn))
	mux.HandleFunc("GET "+one+"/photos", a(h.listEventPhotos))
	mux.HandleFunc("POST "+one+"/photos", a(h.addEventPhoto))
	mux.HandleFunc("DELETE "+one+"/photos/{photo_id}", a(h.removeEventPhoto))
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

// Limits of the field values of a body; core checks the values themselves
// against the event's type version.
const (
	maxEventFields     = 20
	maxFieldValueBytes = 8 << 10
)

// eventFields converts a body's field values (JSON by field key) to the
// JSON text core takes; ok is false for a bad key, a null or an oversized
// value, or too many values. nil stays nil.
func eventFields(in map[string]json.RawMessage) (map[string]string, bool) {
	if in == nil {
		return nil, true
	}
	if len(in) > maxEventFields {
		return nil, false
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if !catalogKeyPattern.MatchString(k) || len(v) == 0 || len(v) > maxFieldValueBytes || string(v) == "null" {
			return nil, false
		}
		out[k] = string(v)
	}
	return out, true
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
	// Fields holds the values of the type's fields by field key.
	Fields          map[string]json.RawMessage `json:"fields"`
	Recurrence      *recurrenceJSON            `json:"recurrence"`
	ClientRequestID string                     `json:"client_request_id"`
	CoverUploadID   string                     `json:"cover_upload_id"`
	// Components are the organizer's selections for the type's components
	// (issue #238, ADR-0037 §2).
	Components []componentSelectionJSON `json:"components"`
	// StartsNow: the event starts at core's now and is open-ended; the
	// times are not sent (ADR-0038 §1).
	StartsNow bool `json:"starts_now"`
}

// request checks the body and converts it for core; ok is false for a bad
// request.
func (b eventCreateBody) request(tayfaID string) (*corev1.CreateEventRequest, bool) {
	// The type is a key of the catalog; core knows which keys exist.
	okT := catalogKeyPattern.MatchString(b.Type)
	mode, okM := eventModes.value(b.Mode)
	fields, okF := eventFields(b.Fields)
	components, okC := componentSelections(b.Components)
	// With starts_now the times are core's; without it both are required.
	okTime := !b.StartsAt.IsZero() && !b.EndsAt.IsZero()
	if b.StartsNow {
		okTime = b.StartsAt.IsZero() && b.EndsAt.IsZero() && b.Recurrence == nil && !b.Discoverable
	}
	if !okT || !okM || !okF || !okC || !okTime ||
		!eventTitleOK(b.Title) || !eventDescOK(b.Description) ||
		!pointOK(b.MeetingPoint) || !capacityOK(b.Capacity) || !validUUID(b.ClientRequestID) ||
		(b.CoverUploadID != "" && !validUUID(b.CoverUploadID)) {
		return nil, false
	}
	req := &corev1.CreateEventRequest{
		TayfaId: tayfaID, TypeKey: b.Type, Fields: fields, Mode: mode, Title: b.Title, Description: b.Description,
		StartsNow:        b.StartsNow,
		MeetingPointName: b.MeetingPointName, MeetingPoint: b.MeetingPoint.proto(),
		City: b.City, District: b.District, OnlineUrl: b.OnlineURL,
		Capacity: b.Capacity, Discoverable: b.Discoverable,
		ClientRequestId: b.ClientRequestID, CoverUploadId: b.CoverUploadID,
		Components: components,
	}
	if !b.StartsNow {
		req.StartTime, req.EndTime = timestamppb.New(b.StartsAt), timestamppb.New(b.EndsAt)
	}
	if rc := b.Recurrence; rc != nil {
		if rc.RRule == "" || len(rc.RRule) > rruleMax || len(rc.TimeZone) > timeZoneMax {
			return nil, false
		}
		req.Recurrence = &corev1.Recurrence{Rrule: rc.RRule, TimeZone: rc.TimeZone}
	}
	return req, true
}

// maxInstantInvitees is core's limit on friends invited with an instant
// event (ADR-0032).
const maxInstantInvitees = 10

type instantEventBody struct {
	Event          *eventCreateBody `json:"event"`
	InviteeUserIDs []string         `json:"invitee_user_ids"`
}

type instantEventJSON struct {
	Tayfa tayfaJSON `json:"tayfa"`
	Event eventJSON `json:"event"`
}

// createInstantEvent opens an instant event: a hidden single-event tayfa
// and its event (ADR-0032). It is not a tayfa creation: the per-IP tayfa
// limit does not apply; core limits open instant events per user.
func (h coreHandlers) createInstantEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b instantEventBody
	if !decodeLimit(w, r, &b, maxEventBody) {
		return
	}
	if b.Event == nil || b.Event.Recurrence != nil || b.Event.CoverUploadID != "" || len(b.InviteeUserIDs) > maxInstantInvitees {
		badRequest(w, r)
		return
	}
	for _, u := range b.InviteeUserIDs {
		if !validUUID(u) {
			badRequest(w, r)
			return
		}
	}
	er, ok := b.Event.request("")
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.CreateInstantEvent(ctx, &corev1.CreateInstantEventRequest{Event: er, InviteeUserIds: b.InviteeUserIDs})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := instantEventJSON{Tayfa: tayfaFrom(resp.GetTayfa()), Event: eventFrom(resp.GetEvent())}
	w.Header().Set("Location", "/v1/tayfas/"+out.Tayfa.ID+"/events/"+out.Event.ID)
	writeJSON(w, http.StatusCreated, out)
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
	req, ok := b.request(id)
	if !ok {
		badRequest(w, r)
		return
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
	// Fields: the complete new set of field values; {} removes them all.
	Fields        map[string]json.RawMessage `json:"fields"`
	ApplyToSeries bool                       `json:"apply_to_series"`
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
	if b.Fields != nil {
		fields, ok := eventFields(b.Fields)
		if !ok {
			badRequest(w, r)
			return
		}
		req.Fields = fields
		add("fields")
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

// finishEvent is the organizer's "Bitir" on an open-ended event (ADR-0038
// §1); the end is core's now, so the request has no body.
func (h coreHandlers) finishEvent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.FinishEvent(ctx, &corev1.FinishEventRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventFrom(resp.GetEvent()))
}

// --- QR ile katılım (issue #264, ADR-0038 §3) ---

type joinQRTokenJSON struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// createEventJoinQr returns a new join token for the event's QR. The token
// is in the response body only; nothing here logs it.
func (h coreHandlers) createEventJoinQr(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !h.d.InviteLimit.Allow(clientIP(r, h.d.TrustCFConnectingIP)) {
		rateLimited(w, r)
		return
	}
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.CreateEventJoinQr(ctx, &corev1.CreateEventJoinQrRequest{TayfaId: id, EventId: eid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, joinQRTokenJSON{Token: resp.GetToken(), ExpiresAt: resp.GetExpireTime().AsTime().UTC()})
}

type joinQRResultJSON struct {
	TayfaID    string `json:"tayfa_id"`
	EventID    string `json:"event_id"`
	Pending    bool   `json:"pending"`
	RSVP       string `json:"rsvp,omitempty"`
	Attendance string `json:"attendance,omitempty"`
}

// joinEventByQr redeems a join token. The token travels in the body (it is
// in the QR link's fragment and never in a URL), and is limited per IP
// before the body is read; core also limits attempts per user.
func (h coreHandlers) joinEventByQr(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	token, ok := h.inviteToken(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{Token: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, joinQRResultJSON{
		TayfaID: resp.GetTayfaId(), EventID: resp.GetEventId(), Pending: resp.GetPending(),
		RSVP: optName(rsvpStatuses, resp.GetRsvp()), Attendance: optName(attendanceStatuses, resp.GetAttendance()),
	})
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

// --- Album (issue #206) ---

func (h coreHandlers) listEventPhotos(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Events.ListEventPhotos(ctx, &corev1.ListEventPhotosRequest{TayfaId: id, EventId: eid, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := eventPhotoListJSON{Photos: make([]eventPhotoJSON, 0, len(resp.GetPhotos())), NextPageToken: resp.GetNextPageToken()}
	for _, p := range resp.GetPhotos() {
		out.Photos = append(out.Photos, eventPhotoFrom(p))
	}
	writeJSON(w, http.StatusOK, out)
}

type eventPhotoBody struct {
	UploadID        string `json:"upload_id"`
	Caption         string `json:"caption"`
	ClientRequestID string `json:"client_request_id"`
	ConsentAck      bool   `json:"consent_ack"`
}

func (h coreHandlers) addEventPhoto(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b eventPhotoBody
	if !decode(w, r, &b) {
		return
	}
	if !validUUID(b.UploadID) || !validUUID(b.ClientRequestID) || runes(b.Caption) > photoCaptionMax {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Events.AddEventPhoto(ctx, &corev1.AddEventPhotoRequest{
		TayfaId: id, EventId: eid, UploadId: b.UploadID, Caption: b.Caption, ClientRequestId: b.ClientRequestID,
		ConsentAck: b.ConsentAck,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventPhotoFrom(resp.GetPhoto()))
}

func (h coreHandlers) removeEventPhoto(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "event_id", "photo_id")
	if !ok {
		return
	}
	if _, err := h.d.Events.RemoveEventPhoto(ctx, &corev1.RemoveEventPhotoRequest{TayfaId: ids[0], EventId: ids[1], PhotoId: ids[2]}); err != nil {
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

package grpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event/ical"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/instant"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Events is what EventServer needs from the app layer.
type Events interface {
	Create(ctx context.Context, user, t id.ID, in event.Input) (event.Event, error)
	Update(ctx context.Context, user, t, e id.ID, fields []string, in event.Input) (event.Event, error)
	Cancel(ctx context.Context, user, t, e id.ID, reason string) (event.Event, error)
	Finish(ctx context.Context, user, t, e id.ID) (event.Event, error)
	Get(ctx context.Context, user, t, e id.ID) (event.Event, error)
	ListUpcoming(ctx context.Context, user, t id.ID, pageSize int32, pageToken string) ([]event.Event, string, error)
	SetRSVP(ctx context.Context, user, t, e id.ID, status string) (string, error)
	ListAttendees(ctx context.Context, user, t, e id.ID, status string, pageSize int32, pageToken string) ([]event.Attendee, string, error)
	CheckIn(ctx context.Context, user, t, e id.ID, at *event.Point) (event.Attendance, error)
	SetAttendance(ctx context.Context, user, t, e, target id.ID, attended bool) error
	AddPhoto(ctx context.Context, user, t, e id.ID, in event.PhotoInput) (event.Photo, error)
	ListPhotos(ctx context.Context, user, t, e id.ID, pageSize int32, pageToken string) ([]event.Photo, string, error)
	RemovePhoto(ctx context.Context, user, t, e, photo id.ID) error
	GetSeries(ctx context.Context, user, t, series id.ID) (event.Series, error)
	EndSeries(ctx context.Context, user, t, series id.ID) (event.Series, int, error)
	Export(ctx context.Context, user, t, e id.ID) ([]byte, error)
}

// Instants is what EventServer needs to create instant events (ADR-0032).
type Instants interface {
	Create(ctx context.Context, user id.ID, in instant.Input) (instant.Result, error)
}

// EventServer implements corev1.EventServiceServer. The acting user always
// comes from the verified token (auth.Guard), never from the request.
// Locations and URLs are converted here and never logged.
type EventServer struct {
	corev1.UnimplementedEventServiceServer
	app     Events
	instant Instants
	log     *slog.Logger
}

// WithInstant enables CreateInstantEvent; without it the method is
// unimplemented.
func (s *EventServer) WithInstant(i Instants) *EventServer {
	s.instant = i
	return s
}

// NewEventServer returns an EventServer.
func NewEventServer(app Events, log *slog.Logger) *EventServer {
	return &EventServer{app: app, log: log}
}

// scope parses the caller, tayfa_id and, unless eventID is nil, event_id.
func scope(ctx context.Context, tayfa string, eventID *string) (user, t, e id.ID, err error) {
	if user, err = callerID(ctx); err != nil {
		return
	}
	if t, err = tayfaID(tayfa); err != nil {
		return
	}
	if eventID != nil {
		var ok bool
		if e, ok = id.Parse(*eventID); !ok {
			err = validation("event_id")
		}
	}
	return
}

func ptr(s string) *string { return &s }

// createInput converts a CreateEventRequest without its tayfa_id.
func createInput(req *corev1.CreateEventRequest) (event.Input, error) {
	// type_key is the catalog key; the Faz 1 enum is read only without it.
	typ := req.GetTypeKey()
	if typ == "" {
		var ok bool
		if typ, ok = interestFromProto(req.GetType()); !ok {
			return event.Input{}, validation("type")
		}
	}
	fields, err := fieldsFromProto(req.GetFields())
	if err != nil {
		return event.Input{}, err
	}
	loc, err := locationFromProto(req.GetMode(), req.GetMeetingPointName(), req.GetMeetingPoint(), req.GetCity(), req.GetDistrict(), req.GetOnlineUrl())
	if err != nil {
		return event.Input{}, err
	}
	if req.GetDetails() != nil {
		return event.Input{}, validation("details")
	}
	in := event.Input{
		Type: typ, Title: req.GetTitle(), Description: req.GetDescription(),
		StartsAt: timeOf(req.GetStartTime()), EndsAt: timeOf(req.GetEndTime()), Location: loc,
		Capacity: req.GetCapacity(), Discoverable: req.GetDiscoverable(), Fields: fields,
		ClientRequestID: req.GetClientRequestId(), StartsNow: req.GetStartsNow(),
	}
	if r := req.GetRecurrence(); r != nil {
		in.Recurrence = &event.Recurrence{RRule: r.GetRrule(), TimeZone: r.GetTimeZone()}
	}
	if in.CoverUploadID, err = optionalUploadID(req.GetCoverUploadId(), "cover_upload_id"); err != nil {
		return event.Input{}, err
	}
	if in.Components, err = selectionsFromProto(req.GetComponents()); err != nil {
		return event.Input{}, err
	}
	return in, nil
}

// CreateEvent implements corev1.EventServiceServer.
func (s *EventServer) CreateEvent(ctx context.Context, req *corev1.CreateEventRequest) (*corev1.CreateEventResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	in, err := createInput(req)
	if err != nil {
		return nil, err
	}
	v, err := s.app.Create(ctx, user, t, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateEventResponse{Event: eventToProto(v)}, nil
}

// CreateInstantEvent implements corev1.EventServiceServer (ADR-0032).
func (s *EventServer) CreateInstantEvent(ctx context.Context, req *corev1.CreateInstantEventRequest) (*corev1.CreateInstantEventResponse, error) {
	user, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if s.instant == nil {
		return nil, status.Error(codes.Unimplemented, msgRejected)
	}
	er := req.GetEvent()
	if er == nil {
		return nil, validation("event")
	}
	// The tayfa does not exist yet; a cover is set later with UpdateEvent.
	if er.GetTayfaId() != "" {
		return nil, validation("event.tayfa_id")
	}
	if er.GetRecurrence() != nil {
		return nil, validation("event.recurrence")
	}
	if er.GetCoverUploadId() != "" {
		return nil, validation("event.cover_upload_id")
	}
	in, err := createInput(er)
	if err != nil {
		return nil, err
	}
	if len(req.GetInviteeUserIds()) > instant.MaxInvitees {
		return nil, validation("invitee_user_ids")
	}
	invitees := make([]id.ID, 0, len(req.GetInviteeUserIds()))
	for _, u := range req.GetInviteeUserIds() {
		v, ok := id.Parse(u)
		if !ok {
			return nil, validation("invitee_user_ids")
		}
		invitees = append(invitees, v)
	}
	r, err := s.instant.Create(ctx, user, instant.Input{Event: in, Invitees: invitees})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateInstantEventResponse{Tayfa: tayfaToProto(r.Tayfa), Event: eventToProto(r.Event)}, nil
}

// UpdateEvent implements corev1.EventServiceServer.
func (s *EventServer) UpdateEvent(ctx context.Context, req *corev1.UpdateEventRequest) (*corev1.UpdateEventResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	fields := req.GetUpdateMask().GetPaths()
	in := event.Input{
		Title: req.GetTitle(), Description: req.GetDescription(),
		StartsAt: timeOf(req.GetStartTime()), EndsAt: timeOf(req.GetEndTime()),
		Capacity: req.GetCapacity(), Discoverable: req.GetDiscoverable(), Series: req.GetSeries(),
	}
	// Only the fields named in the mask are converted.
	for _, f := range fields {
		switch f {
		case event.FieldLocation:
			if in.Location, err = locationFromProto(req.GetMode(), req.GetMeetingPointName(), req.GetMeetingPoint(), req.GetCity(), req.GetDistrict(), req.GetOnlineUrl()); err != nil {
				return nil, err
			}
		case event.FieldFields:
			if in.Fields, err = fieldsFromProto(req.GetFields()); err != nil {
				return nil, err
			}
		case event.FieldCover:
			if in.CoverUploadID, err = optionalUploadID(req.GetCoverUploadId(), "cover_upload_id"); err != nil {
				return nil, err
			}
		}
	}
	v, err := s.app.Update(ctx, user, t, e, fields, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateEventResponse{Event: eventToProto(v)}, nil
}

// CancelEvent implements corev1.EventServiceServer.
func (s *EventServer) CancelEvent(ctx context.Context, req *corev1.CancelEventRequest) (*corev1.CancelEventResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	v, err := s.app.Cancel(ctx, user, t, e, req.GetReason())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CancelEventResponse{Event: eventToProto(v)}, nil
}

// FinishEvent implements corev1.EventServiceServer.
func (s *EventServer) FinishEvent(ctx context.Context, req *corev1.FinishEventRequest) (*corev1.FinishEventResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	v, err := s.app.Finish(ctx, user, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.FinishEventResponse{Event: eventToProto(v)}, nil
}

// GetEvent implements corev1.EventServiceServer.
func (s *EventServer) GetEvent(ctx context.Context, req *corev1.GetEventRequest) (*corev1.GetEventResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	v, err := s.app.Get(ctx, user, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetEventResponse{Event: eventToProto(v)}, nil
}

// ListUpcomingEvents implements corev1.EventServiceServer.
func (s *EventServer) ListUpcomingEvents(ctx context.Context, req *corev1.ListUpcomingEventsRequest) (*corev1.ListUpcomingEventsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListUpcoming(ctx, user, t, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListUpcomingEventsResponse{NextPageToken: next, Events: make([]*corev1.Event, 0, len(list))}
	for _, v := range list {
		resp.Events = append(resp.Events, eventToProto(v))
	}
	return resp, nil
}

// SetRsvp implements corev1.EventServiceServer.
func (s *EventServer) SetRsvp(ctx context.Context, req *corev1.SetRsvpRequest) (*corev1.SetRsvpResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	st := rsvpFromProto(req.GetStatus())
	if st == "" || st == event.RSVPWaitlist {
		return nil, validation("status")
	}
	got, err := s.app.SetRSVP(ctx, user, t, e, st)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetRsvpResponse{Status: rsvpToProto(got)}, nil
}

// ListAttendees implements corev1.EventServiceServer.
func (s *EventServer) ListAttendees(ctx context.Context, req *corev1.ListAttendeesRequest) (*corev1.ListAttendeesResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	st := ""
	if req.GetStatus() != corev1.RsvpStatus_RSVP_STATUS_UNSPECIFIED {
		if st = rsvpFromProto(req.GetStatus()); st == "" {
			return nil, validation("status")
		}
	}
	list, next, err := s.app.ListAttendees(ctx, user, t, e, st, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListAttendeesResponse{NextPageToken: next, Attendees: make([]*corev1.Attendee, 0, len(list))}
	for _, a := range list {
		resp.Attendees = append(resp.Attendees, &corev1.Attendee{
			UserId: a.UserID.String(), Status: rsvpToProto(a.Status), UpdateTime: timestamppb.New(a.UpdatedAt),
			Attendance: attendanceToProto(a.Attendance), AttendanceMethod: methodToProto(a.AttendanceMethod),
		})
	}
	return resp, nil
}

// GetEventSeries implements corev1.EventServiceServer.
func (s *EventServer) GetEventSeries(ctx context.Context, req *corev1.GetEventSeriesRequest) (*corev1.GetEventSeriesResponse, error) {
	user, t, series, err := seriesScope(ctx, req.GetTayfaId(), req.GetSeriesId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetSeries(ctx, user, t, series)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetEventSeriesResponse{Series: seriesToProto(v)}, nil
}

// EndEventSeries implements corev1.EventServiceServer.
func (s *EventServer) EndEventSeries(ctx context.Context, req *corev1.EndEventSeriesRequest) (*corev1.EndEventSeriesResponse, error) {
	user, t, series, err := seriesScope(ctx, req.GetTayfaId(), req.GetSeriesId())
	if err != nil {
		return nil, err
	}
	v, n, err := s.app.EndSeries(ctx, user, t, series)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.EndEventSeriesResponse{Series: seriesToProto(v), CancelledCount: int32(min(n, math.MaxInt32))}, nil //nolint:gosec // clamped
}

// ExportEvent implements corev1.EventServiceServer.
func (s *EventServer) ExportEvent(ctx context.Context, req *corev1.ExportEventRequest) (*corev1.ExportEventResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	b, err := s.app.Export(ctx, user, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ExportEventResponse{Ics: b, ContentType: ical.ContentType}, nil
}

func seriesScope(ctx context.Context, tayfa, series string) (user, t, sid id.ID, err error) {
	if user, t, _, err = scope(ctx, tayfa, nil); err != nil {
		return
	}
	var ok bool
	if sid, ok = id.Parse(series); !ok {
		err = validation("series_id")
	}
	return
}

// ---------------------------------------------------------------------------
// Conversions

func seriesToProto(v event.Series) *corev1.EventSeries {
	out := &corev1.EventSeries{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), Rrule: v.RRule, TimeZone: v.TimeZone,
		StartTime: timestamppb.New(v.StartsAt), CreateTime: timestamppb.New(v.CreatedAt),
	}
	if v.TemplateEventID != (id.ID{}) {
		out.TemplateEventId = v.TemplateEventID.String()
	}
	if !v.EndedAt.IsZero() {
		out.EndTime = timestamppb.New(v.EndedAt)
	}
	if v.CreatedBy != (id.ID{}) {
		out.CreatedBy = v.CreatedBy.String()
	}
	return out
}

// timeOf returns the zero time for an unset or invalid timestamp; the app
// layer refuses it.
func timeOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil || ts.CheckValid() != nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func pointFromProto(p *corev1.LatLng) (*event.Point, bool) {
	if p == nil {
		return nil, true
	}
	if math.IsNaN(p.GetLat()) || math.IsNaN(p.GetLng()) || math.IsInf(p.GetLat(), 0) || math.IsInf(p.GetLng(), 0) {
		return nil, false
	}
	return &event.Point{Lat: p.GetLat(), Lng: p.GetLng()}, true
}

func locationFromProto(mode corev1.EventMode, name string, point *corev1.LatLng, city, district, url string) (event.Location, error) {
	l := event.Location{MeetingPointName: name, City: city, District: district, OnlineURL: url}
	switch mode {
	case corev1.EventMode_EVENT_MODE_IN_PERSON:
		l.Mode = event.ModeInPerson
	case corev1.EventMode_EVENT_MODE_ONLINE:
		l.Mode = event.ModeOnline
	default:
		return event.Location{}, validation("mode")
	}
	p, ok := pointFromProto(point)
	if !ok {
		return event.Location{}, validation("meeting_point")
	}
	l.MeetingPoint = p
	return l, nil
}

// maxFieldValueBytes bounds one field value before the app layer reads it.
const maxFieldValueBytes = 8 << 10

// fieldsFromProto converts a request's field values (JSON text by field
// key). The app layer checks them against the event's type version; here
// only their number and size are bounded.
func fieldsFromProto(in map[string]string) (map[string]json.RawMessage, error) {
	if len(in) > definition.MaxFields {
		return nil, validation("fields")
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		if !definition.ValidKey(k) || v == "" || len(v) > maxFieldValueBytes {
			return nil, validation("fields")
		}
		out[k] = json.RawMessage(v)
	}
	return out, nil
}

func eventToProto(v event.Event) *corev1.Event {
	out := &corev1.Event{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), Type: interestToProto(v.Type), TypeKey: v.Type, TypeVersion: v.TypeVersion, Title: v.Title,
		Description: v.Description, StartTime: timestamppb.New(v.StartsAt), EndTime: timestamppb.New(v.EndsAt),
		MeetingPointName: v.MeetingPointName, City: v.City, District: v.District, OnlineUrl: v.OnlineURL,
		Capacity: v.Capacity, CancelReason: v.CancelReason, Discoverable: v.Discoverable,
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
		GoingCount: v.Going, MaybeCount: v.Maybe, WaitlistCount: v.Waitlist,
		CallerRsvp: rsvpToProto(v.CallerRSVP), CallerCanManage: v.CallerCanManage,
		CallerAttendance: attendanceToProto(v.CallerAttendance),
		CoverUploadId:    optionalID(v.CoverUploadID),
		OpenEnded:        v.OpenEnded,
	}
	switch v.Mode {
	case event.ModeInPerson:
		out.Mode = corev1.EventMode_EVENT_MODE_IN_PERSON
	case event.ModeOnline:
		out.Mode = corev1.EventMode_EVENT_MODE_ONLINE
	}
	switch v.Status {
	case event.StatusScheduled:
		out.Status = corev1.EventStatus_EVENT_STATUS_SCHEDULED
	case event.StatusCancelled:
		out.Status = corev1.EventStatus_EVENT_STATUS_CANCELLED
	}
	if v.MeetingPoint != nil {
		out.MeetingPoint = &corev1.LatLng{Lat: v.MeetingPoint.Lat, Lng: v.MeetingPoint.Lng}
	}
	if !v.CancelledAt.IsZero() {
		out.CancelTime = timestamppb.New(v.CancelledAt)
	}
	if v.CreatedBy != (id.ID{}) {
		out.CreatedBy = v.CreatedBy.String()
	}
	if v.SeriesID != (id.ID{}) {
		out.SeriesId, out.SeriesOverride = v.SeriesID.String(), v.SeriesOverride
	}
	if len(v.Fields) > 0 {
		out.Fields = make(map[string]string, len(v.Fields))
		for k, raw := range v.Fields {
			out.Fields[k] = string(raw)
		}
	}
	return out
}

var rsvpValues = map[corev1.RsvpStatus]string{
	corev1.RsvpStatus_RSVP_STATUS_GOING:     event.RSVPGoing,
	corev1.RsvpStatus_RSVP_STATUS_MAYBE:     event.RSVPMaybe,
	corev1.RsvpStatus_RSVP_STATUS_NOT_GOING: event.RSVPNotGoing,
	corev1.RsvpStatus_RSVP_STATUS_WAITLIST:  event.RSVPWaitlist,
}

func rsvpFromProto(s corev1.RsvpStatus) string { return rsvpValues[s] }

func rsvpToProto(s string) corev1.RsvpStatus {
	for k, v := range rsvpValues {
		if v == s {
			return k
		}
	}
	return corev1.RsvpStatus_RSVP_STATUS_UNSPECIFIED
}

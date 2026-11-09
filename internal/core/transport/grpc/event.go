package grpc

import (
	"context"
	"log/slog"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Events is what EventServer needs from the app layer.
type Events interface {
	Create(ctx context.Context, user, t id.ID, in event.Input) (event.Event, error)
	Update(ctx context.Context, user, t, e id.ID, fields []string, in event.Input) (event.Event, error)
	Cancel(ctx context.Context, user, t, e id.ID, reason string) (event.Event, error)
	Get(ctx context.Context, user, t, e id.ID) (event.Event, error)
	ListUpcoming(ctx context.Context, user, t id.ID, pageSize int32, pageToken string) ([]event.Event, string, error)
	SetRSVP(ctx context.Context, user, t, e id.ID, status string) (string, error)
	ListAttendees(ctx context.Context, user, t, e id.ID, status string, pageSize int32, pageToken string) ([]event.Attendee, string, error)
	AddCampItem(ctx context.Context, user, t, e id.ID, name string, quantity int32, assigned id.ID) (event.CampItem, error)
	RemoveCampItem(ctx context.Context, user, t, e, item id.ID) error
	SetVehicle(ctx context.Context, user, t, e id.ID, in event.VehicleInput) (event.Vehicle, error)
	RemoveVehicle(ctx context.Context, user, t, e, vehicle id.ID) error
	CheckIn(ctx context.Context, user, t, e id.ID, at *event.Point) (event.Attendance, error)
	StartHike(ctx context.Context, user, t, e id.ID, startedAt time.Time) (event.HikeCheckin, error)
	FinishHike(ctx context.Context, user, t, e id.ID, returnedAt time.Time) (event.HikeCheckin, error)
	SetAttendance(ctx context.Context, user, t, e, target id.ID, attended bool) error
	AddCatch(ctx context.Context, user, t, e id.ID, in event.CatchInput) (event.Catch, error)
	ListCatches(ctx context.Context, user, t, e id.ID, pageSize int32, pageToken string) ([]event.Catch, string, error)
	RemoveCatch(ctx context.Context, user, t, e, catch id.ID) error
}

// EventServer implements corev1.EventServiceServer. The acting user always
// comes from the verified token (auth.Guard), never from the request.
// Locations, URLs and plates are converted here and never logged.
type EventServer struct {
	corev1.UnimplementedEventServiceServer
	app Events
	log *slog.Logger
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

// CreateEvent implements corev1.EventServiceServer.
func (s *EventServer) CreateEvent(ctx context.Context, req *corev1.CreateEventRequest) (*corev1.CreateEventResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	typ, ok := interestFromProto(req.GetType())
	if !ok {
		return nil, validation("type")
	}
	loc, err := locationFromProto(req.GetMode(), req.GetMeetingPointName(), req.GetMeetingPoint(), req.GetCity(), req.GetDistrict(), req.GetOnlineUrl())
	if err != nil {
		return nil, err
	}
	d, err := detailsFromProto(req.GetConvoy(), req.GetCamp(), req.GetHike(), req.GetFishing(), req.GetGaming(), req.GetSports())
	if err != nil {
		return nil, err
	}
	in := event.Input{
		Type: typ, Title: req.GetTitle(), Description: req.GetDescription(),
		StartsAt: timeOf(req.GetStartTime()), EndsAt: timeOf(req.GetEndTime()), Location: loc,
		Capacity: req.GetCapacity(), Discoverable: req.GetDiscoverable(), Details: d,
		ClientRequestID: req.GetClientRequestId(),
	}
	v, err := s.app.Create(ctx, user, t, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateEventResponse{Event: eventToProto(v)}, nil
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
		Capacity: req.GetCapacity(), Discoverable: req.GetDiscoverable(),
	}
	// Only the fields named in the mask are converted.
	for _, f := range fields {
		switch f {
		case event.FieldLocation:
			if in.Location, err = locationFromProto(req.GetMode(), req.GetMeetingPointName(), req.GetMeetingPoint(), req.GetCity(), req.GetDistrict(), req.GetOnlineUrl()); err != nil {
				return nil, err
			}
		case event.FieldDetails:
			if in.Details, err = detailsFromProto(req.GetConvoy(), req.GetCamp(), req.GetHike(), req.GetFishing(), req.GetGaming(), req.GetSports()); err != nil {
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

// AddCampItem implements corev1.EventServiceServer.
func (s *EventServer) AddCampItem(ctx context.Context, req *corev1.AddCampItemRequest) (*corev1.AddCampItemResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	var assigned id.ID
	if a := req.GetAssignedUserId(); a != "" {
		var ok bool
		if assigned, ok = id.Parse(a); !ok {
			return nil, validation("assigned_user_id")
		}
	}
	item, err := s.app.AddCampItem(ctx, user, t, e, req.GetName(), req.GetQuantity(), assigned)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddCampItemResponse{Item: campItemToProto(item)}, nil
}

// RemoveCampItem implements corev1.EventServiceServer.
func (s *EventServer) RemoveCampItem(ctx context.Context, req *corev1.RemoveCampItemRequest) (*corev1.RemoveCampItemResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	item, ok := id.Parse(req.GetItemId())
	if !ok {
		return nil, validation("item_id")
	}
	if err := s.app.RemoveCampItem(ctx, user, t, e, item); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveCampItemResponse{}, nil
}

// AddConvoyVehicle implements corev1.EventServiceServer.
func (s *EventServer) AddConvoyVehicle(ctx context.Context, req *corev1.AddConvoyVehicleRequest) (*corev1.AddConvoyVehicleResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	v, err := s.app.SetVehicle(ctx, user, t, e, event.VehicleInput{
		Make: req.GetMake(), Model: req.GetModel(), ModelYear: req.GetModelYear(), Color: req.GetColor(), Plate: req.GetPlate(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddConvoyVehicleResponse{Vehicle: vehicleToProto(v)}, nil
}

// RemoveConvoyVehicle implements corev1.EventServiceServer.
func (s *EventServer) RemoveConvoyVehicle(ctx context.Context, req *corev1.RemoveConvoyVehicleRequest) (*corev1.RemoveConvoyVehicleResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	vehicle, ok := id.Parse(req.GetVehicleId())
	if !ok {
		return nil, validation("vehicle_id")
	}
	if err := s.app.RemoveVehicle(ctx, user, t, e, vehicle); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveConvoyVehicleResponse{}, nil
}

// ---------------------------------------------------------------------------
// Conversions

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

// detailsFromProto converts the request's details oneof; at most one of the
// arguments is set. Output-only fields (vehicles, items) are dropped.
func detailsFromProto(cv *corev1.ConvoyDetails, cp *corev1.CampDetails, h *corev1.HikeDetails, f *corev1.FishingDetails, g *corev1.GamingDetails, sp *corev1.SportsDetails) (event.Details, error) {
	var d event.Details
	switch {
	case cv != nil:
		if len(cv.GetRoute()) > 500 {
			return event.Details{}, validation("route")
		}
		c := &event.Convoy{}
		for _, p := range cv.GetRoute() {
			pt, ok := pointFromProto(p)
			if !ok || pt == nil {
				return event.Details{}, validation("route")
			}
			c.Route = append(c.Route, *pt)
		}
		d.Convoy = c
	case cp != nil:
		c := &event.Camp{SiteName: cp.GetSiteName(), Notes: cp.GetNotes()}
		if cp.HasWater != nil {
			w := cp.GetHasWater()
			c.HasWater = &w
		}
		if cp.HasToilet != nil {
			w := cp.GetHasToilet()
			c.HasToilet = &w
		}
		d.Camp = c
	case h != nil:
		if math.IsNaN(h.GetDistanceKm()) || math.IsInf(h.GetDistanceKm(), 0) {
			return event.Details{}, validation("distance_km")
		}
		d.Hike = &event.Hike{
			Difficulty: h.GetDifficulty(), DistanceKm: h.GetDistanceKm(), ElevationM: h.GetElevationM(),
			ExpectedReturnAt: timeOf(h.GetExpectedReturnTime()), ReturnGraceMinutes: h.GetReturnGraceMinutes(),
		}
		if h.GetExpectedReturnTime() != nil && d.Hike.ExpectedReturnAt.IsZero() {
			return event.Details{}, validation("expected_return_time")
		}
	case f != nil:
		fi := &event.Fishing{}
		switch f.GetSpotVisibility() {
		case corev1.SpotVisibility_SPOT_VISIBILITY_UNSPECIFIED:
		case corev1.SpotVisibility_SPOT_VISIBILITY_TAYFA:
			fi.SpotVisibility = event.SpotTayfa
		case corev1.SpotVisibility_SPOT_VISIBILITY_PARTICIPANTS:
			fi.SpotVisibility = event.SpotParticipants
		default:
			return event.Details{}, validation("spot_visibility")
		}
		d.Fishing = fi
	case g != nil:
		p, ok := platformFromProto(g.GetPlatform())
		if !ok {
			return event.Details{}, validation("platform")
		}
		d.Gaming = &event.Gaming{Game: g.GetGame(), Platform: p}
	case sp != nil:
		v, ok := sportFromProto(sp.GetSport())
		if !ok {
			return event.Details{}, validation("sport")
		}
		d.Sports = &event.Sports{Sport: v, VenueName: sp.GetVenueName()}
	}
	return d, nil
}

func eventToProto(v event.Event) *corev1.Event {
	out := &corev1.Event{
		Id: v.ID.String(), TayfaId: v.TayfaID.String(), Type: interestToProto(v.Type), Title: v.Title,
		Description: v.Description, StartTime: timestamppb.New(v.StartsAt), EndTime: timestamppb.New(v.EndsAt),
		MeetingPointName: v.MeetingPointName, City: v.City, District: v.District, OnlineUrl: v.OnlineURL,
		Capacity: v.Capacity, CancelReason: v.CancelReason, Discoverable: v.Discoverable,
		CreateTime: timestamppb.New(v.CreatedAt), UpdateTime: timestamppb.New(v.UpdatedAt),
		GoingCount: v.Going, MaybeCount: v.Maybe, WaitlistCount: v.Waitlist,
		CallerRsvp: rsvpToProto(v.CallerRSVP), CallerCanManage: v.CallerCanManage,
		CallerAttendance: attendanceToProto(v.CallerAttendance),
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
	d := v.Details
	switch {
	case d.Convoy != nil:
		c := &corev1.ConvoyDetails{}
		for _, p := range d.Convoy.Route {
			c.Route = append(c.Route, &corev1.LatLng{Lat: p.Lat, Lng: p.Lng})
		}
		for _, veh := range d.Convoy.Vehicles {
			c.Vehicles = append(c.Vehicles, vehicleToProto(veh))
		}
		out.Details = &corev1.Event_Convoy{Convoy: c}
	case d.Camp != nil:
		c := &corev1.CampDetails{SiteName: d.Camp.SiteName, HasWater: d.Camp.HasWater, HasToilet: d.Camp.HasToilet, Notes: d.Camp.Notes}
		for _, it := range d.Camp.Items {
			c.Items = append(c.Items, campItemToProto(it))
		}
		out.Details = &corev1.Event_Camp{Camp: c}
	case d.Hike != nil:
		h := &corev1.HikeDetails{
			Difficulty: d.Hike.Difficulty, DistanceKm: d.Hike.DistanceKm, ElevationM: d.Hike.ElevationM,
			ReturnGraceMinutes: d.Hike.ReturnGraceMinutes,
		}
		if !d.Hike.ExpectedReturnAt.IsZero() {
			h.ExpectedReturnTime = timestamppb.New(d.Hike.ExpectedReturnAt)
		}
		out.Details = &corev1.Event_Hike{Hike: h}
	case d.Fishing != nil:
		vis := corev1.SpotVisibility_SPOT_VISIBILITY_TAYFA
		if d.Fishing.SpotVisibility == event.SpotParticipants {
			vis = corev1.SpotVisibility_SPOT_VISIBILITY_PARTICIPANTS
		}
		out.Details = &corev1.Event_Fishing{Fishing: &corev1.FishingDetails{SpotVisibility: vis}}
	case d.Gaming != nil:
		out.Details = &corev1.Event_Gaming{Gaming: &corev1.GamingDetails{Game: d.Gaming.Game, Platform: platformToProto(d.Gaming.Platform)}}
	case d.Sports != nil:
		out.Details = &corev1.Event_Sports{Sports: &corev1.SportsDetails{Sport: sportToProto(d.Sports.Sport), VenueName: d.Sports.VenueName}}
	}
	return out
}

func vehicleToProto(v event.Vehicle) *corev1.ConvoyVehicle {
	return &corev1.ConvoyVehicle{
		Id: v.ID.String(), UserId: v.UserID.String(), Make: v.Make, Model: v.Model,
		ModelYear: v.ModelYear, Color: v.Color, Plate: v.Plate,
	}
}

func campItemToProto(it event.CampItem) *corev1.CampItem {
	out := &corev1.CampItem{Id: it.ID.String(), Name: it.Name, Quantity: it.Quantity}
	if it.AssignedUserID != (id.ID{}) {
		out.AssignedUserId = it.AssignedUserID.String()
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

var platformValues = map[corev1.GamingPlatform]string{
	corev1.GamingPlatform_GAMING_PLATFORM_PC:          "pc",
	corev1.GamingPlatform_GAMING_PLATFORM_PLAYSTATION: "playstation",
	corev1.GamingPlatform_GAMING_PLATFORM_XBOX:        "xbox",
	corev1.GamingPlatform_GAMING_PLATFORM_SWITCH:      "switch",
	corev1.GamingPlatform_GAMING_PLATFORM_MOBILE:      "mobile",
	corev1.GamingPlatform_GAMING_PLATFORM_OTHER:       "other",
}

func platformFromProto(p corev1.GamingPlatform) (string, bool) {
	v, ok := platformValues[p]
	return v, ok
}

func platformToProto(s string) corev1.GamingPlatform {
	for k, v := range platformValues {
		if v == s {
			return k
		}
	}
	return corev1.GamingPlatform_GAMING_PLATFORM_UNSPECIFIED
}

var sportValues = map[corev1.Sport]string{
	corev1.Sport_SPORT_FOOTBALL:       "football",
	corev1.Sport_SPORT_TABLE_FOOTBALL: "table_football",
	corev1.Sport_SPORT_BASKETBALL:     "basketball",
	corev1.Sport_SPORT_VOLLEYBALL:     "volleyball",
	corev1.Sport_SPORT_TENNIS:         "tennis",
	corev1.Sport_SPORT_TABLE_TENNIS:   "table_tennis",
	corev1.Sport_SPORT_OTHER:          "other",
}

func sportFromProto(s corev1.Sport) (string, bool) {
	v, ok := sportValues[s]
	return v, ok
}

func sportToProto(s string) corev1.Sport {
	for k, v := range sportValues {
		if v == s {
			return k
		}
	}
	return corev1.Sport_SPORT_UNSPECIFIED
}

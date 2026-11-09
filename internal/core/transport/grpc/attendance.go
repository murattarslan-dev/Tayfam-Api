package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// CheckIn implements corev1.EventServiceServer. The location is passed to
// the app layer for the distance check and dropped; it is never logged.
func (s *EventServer) CheckIn(ctx context.Context, req *corev1.CheckInRequest) (*corev1.CheckInResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	at, ok := pointFromProto(req.GetLocation())
	if !ok {
		return nil, validation("location")
	}
	a, err := s.app.CheckIn(ctx, user, t, e, at)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CheckInResponse{Attendance: attendanceToProto(a.Status), Method: methodToProto(a.Method)}, nil
}

// StartHike implements corev1.EventServiceServer.
func (s *EventServer) StartHike(ctx context.Context, req *corev1.StartHikeRequest) (*corev1.StartHikeResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	if req.GetStartTime() != nil && req.GetStartTime().CheckValid() != nil {
		return nil, validation("start_time")
	}
	h, err := s.app.StartHike(ctx, user, t, e, timeOf(req.GetStartTime()))
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.StartHikeResponse{Checkin: hikeToProto(h)}, nil
}

// FinishHike implements corev1.EventServiceServer.
func (s *EventServer) FinishHike(ctx context.Context, req *corev1.FinishHikeRequest) (*corev1.FinishHikeResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	if req.GetReturnTime() != nil && req.GetReturnTime().CheckValid() != nil {
		return nil, validation("return_time")
	}
	h, err := s.app.FinishHike(ctx, user, t, e, timeOf(req.GetReturnTime()))
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.FinishHikeResponse{Checkin: hikeToProto(h)}, nil
}

// SetAttendance implements corev1.EventServiceServer.
func (s *EventServer) SetAttendance(ctx context.Context, req *corev1.SetAttendanceRequest) (*corev1.SetAttendanceResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	target, ok := id.Parse(req.GetUserId())
	if !ok {
		return nil, validation("user_id")
	}
	if err := s.app.SetAttendance(ctx, user, t, e, target, req.GetAttended()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetAttendanceResponse{}, nil
}

// AddFishingCatch implements corev1.EventServiceServer.
func (s *EventServer) AddFishingCatch(ctx context.Context, req *corev1.AddFishingCatchRequest) (*corev1.AddFishingCatchResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	at, ok := pointFromProto(req.GetLocation())
	if !ok {
		return nil, validation("location")
	}
	vis, ok := catchVisibilityValues[req.GetVisibility()]
	if !ok {
		return nil, validation("visibility")
	}
	c, err := s.app.AddCatch(ctx, user, t, e, event.CatchInput{
		Species: req.GetSpecies(), LengthCm: req.GetLengthCm(), Location: at, Visibility: vis,
		ClientRequestID: req.GetClientRequestId(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddFishingCatchResponse{Catch: catchToProto(c)}, nil
}

// ListFishingCatches implements corev1.EventServiceServer.
func (s *EventServer) ListFishingCatches(ctx context.Context, req *corev1.ListFishingCatchesRequest) (*corev1.ListFishingCatchesResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.ListCatches(ctx, user, t, e, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	resp := &corev1.ListFishingCatchesResponse{NextPageToken: next, Catches: make([]*corev1.FishingCatch, 0, len(list))}
	for _, c := range list {
		resp.Catches = append(resp.Catches, catchToProto(c))
	}
	return resp, nil
}

// RemoveFishingCatch implements corev1.EventServiceServer.
func (s *EventServer) RemoveFishingCatch(ctx context.Context, req *corev1.RemoveFishingCatchRequest) (*corev1.RemoveFishingCatchResponse, error) {
	user, t, e, err := scope(ctx, req.GetTayfaId(), ptr(req.GetEventId()))
	if err != nil {
		return nil, err
	}
	c, ok := id.Parse(req.GetCatchId())
	if !ok {
		return nil, validation("catch_id")
	}
	if err := s.app.RemoveCatch(ctx, user, t, e, c); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveFishingCatchResponse{}, nil
}

// ---------------------------------------------------------------------------
// Conversions

func attendanceToProto(s string) corev1.AttendanceStatus {
	switch s {
	case event.AttendanceAttended:
		return corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED
	case event.AttendanceNoShow:
		return corev1.AttendanceStatus_ATTENDANCE_STATUS_NO_SHOW
	}
	return corev1.AttendanceStatus_ATTENDANCE_STATUS_UNSPECIFIED
}

func methodToProto(s string) corev1.AttendanceMethod {
	switch s {
	case event.MethodLocation:
		return corev1.AttendanceMethod_ATTENDANCE_METHOD_LOCATION
	case event.MethodOnline:
		return corev1.AttendanceMethod_ATTENDANCE_METHOD_ONLINE
	case event.MethodHikeStart:
		return corev1.AttendanceMethod_ATTENDANCE_METHOD_HIKE_START
	case event.MethodOrganizer:
		return corev1.AttendanceMethod_ATTENDANCE_METHOD_ORGANIZER
	}
	return corev1.AttendanceMethod_ATTENDANCE_METHOD_UNSPECIFIED
}

// catchVisibilityValues maps the request values; UNSPECIFIED is the
// default ("" → self in the app layer).
var catchVisibilityValues = map[corev1.CatchVisibility]string{
	corev1.CatchVisibility_CATCH_VISIBILITY_UNSPECIFIED: "",
	corev1.CatchVisibility_CATCH_VISIBILITY_SELF:        event.CatchSelf,
	corev1.CatchVisibility_CATCH_VISIBILITY_TAYFA:       event.CatchTayfa,
}

func hikeToProto(h event.HikeCheckin) *corev1.HikeCheckin {
	out := &corev1.HikeCheckin{StartTime: timestamppb.New(h.StartedAt)}
	if !h.ReturnedAt.IsZero() {
		out.ReturnTime = timestamppb.New(h.ReturnedAt)
	}
	return out
}

func catchToProto(c event.Catch) *corev1.FishingCatch {
	out := &corev1.FishingCatch{
		Id: c.ID.String(), UserId: c.UserID.String(), Species: c.Species, LengthCm: c.LengthCm,
		Visibility: corev1.CatchVisibility_CATCH_VISIBILITY_SELF, CreateTime: timestamppb.New(c.CreatedAt),
	}
	if c.Visibility == event.CatchTayfa {
		out.Visibility = corev1.CatchVisibility_CATCH_VISIBILITY_TAYFA
	}
	if c.Location != nil {
		out.Location = &corev1.LatLng{Lat: c.Location.Lat, Lng: c.Location.Lng}
	}
	return out
}

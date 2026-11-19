package grpc

import (
	"context"

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

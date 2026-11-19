package rest

import (
	"encoding/json"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// JSON shapes of the event routes (api/openapi/gateway.yaml). Coordinates in
// them are never logged: no handler logs a request or response body.

var (
	eventModes = newEnum(map[string]corev1.EventMode{
		"in_person": corev1.EventMode_EVENT_MODE_IN_PERSON, "online": corev1.EventMode_EVENT_MODE_ONLINE,
	})
	eventStatuses = newEnum(map[string]corev1.EventStatus{
		"scheduled": corev1.EventStatus_EVENT_STATUS_SCHEDULED, "cancelled": corev1.EventStatus_EVENT_STATUS_CANCELLED,
	})
	rsvpStatuses = newEnum(map[string]corev1.RsvpStatus{
		"going": corev1.RsvpStatus_RSVP_STATUS_GOING, "maybe": corev1.RsvpStatus_RSVP_STATUS_MAYBE,
		"not_going": corev1.RsvpStatus_RSVP_STATUS_NOT_GOING, "waitlist": corev1.RsvpStatus_RSVP_STATUS_WAITLIST,
	})
	attendanceStatuses = newEnum(map[string]corev1.AttendanceStatus{
		"attended": corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED, "no_show": corev1.AttendanceStatus_ATTENDANCE_STATUS_NO_SHOW,
	})
	attendanceMethods = newEnum(map[string]corev1.AttendanceMethod{
		"location": corev1.AttendanceMethod_ATTENDANCE_METHOD_LOCATION, "online": corev1.AttendanceMethod_ATTENDANCE_METHOD_ONLINE,
		"hike_start": corev1.AttendanceMethod_ATTENDANCE_METHOD_HIKE_START, "organizer": corev1.AttendanceMethod_ATTENDANCE_METHOD_ORGANIZER,
	})
	catchVisibilities = newEnum(map[string]corev1.CatchVisibility{
		"self": corev1.CatchVisibility_CATCH_VISIBILITY_SELF, "tayfa": corev1.CatchVisibility_CATCH_VISIBILITY_TAYFA,
	})
	spotVisibilities = newEnum(map[string]corev1.SpotVisibility{
		"tayfa": corev1.SpotVisibility_SPOT_VISIBILITY_TAYFA, "participants": corev1.SpotVisibility_SPOT_VISIBILITY_PARTICIPANTS,
	})
)

// optName is the enum's name, or "" (left out) for the zero value.
func optName[T ~int32](e enum[T], v T) string {
	if v == 0 {
		return ""
	}
	return e.name(v)
}

func optTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// --- Shared ---

type latLngJSON struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func (p latLngJSON) valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) && p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

func (p *latLngJSON) proto() *corev1.LatLng {
	if p == nil {
		return nil
	}
	return &corev1.LatLng{Lat: p.Lat, Lng: p.Lng}
}

func latLngFrom(p *corev1.LatLng) *latLngJSON {
	if p == nil {
		return nil
	}
	return &latLngJSON{p.GetLat(), p.GetLng()}
}

// --- Type details (output) ---

// hikeJSON is used for both input and output.
type hikeJSON struct {
	Difficulty         int32      `json:"difficulty"`
	DistanceKm         float64    `json:"distance_km"`
	ElevationM         int32      `json:"elevation_m"`
	ExpectedReturnAt   *time.Time `json:"expected_return_at,omitempty"`
	ReturnGraceMinutes int32      `json:"return_grace_minutes"`
}

// fishingJSON is used for both input and output; an empty input value means
// the default (tayfa).
type fishingJSON struct {
	SpotVisibility string `json:"spot_visibility,omitempty"`
}

type eventJSON struct {
	ID      string `json:"id"`
	TayfaID string `json:"tayfa_id"`
	// Type is the catalog key of the event's type; TypeVersion the version
	// it was created with (GET /v1/catalog/event-types/{key}/versions/{version}).
	Type             string      `json:"type"`
	TypeVersion      int32       `json:"type_version"`
	Mode             string      `json:"mode"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	StartsAt         time.Time   `json:"starts_at"`
	EndsAt           time.Time   `json:"ends_at"`
	MeetingPointName string      `json:"meeting_point_name,omitempty"`
	MeetingPoint     *latLngJSON `json:"meeting_point,omitempty"`
	City             string      `json:"city,omitempty"`
	District         string      `json:"district,omitempty"`
	OnlineURL        string      `json:"online_url,omitempty"`
	Capacity         int32       `json:"capacity"`
	Status           string      `json:"status"`
	CancelledAt      *time.Time  `json:"cancelled_at,omitempty"`
	CancelReason     string      `json:"cancel_reason,omitempty"`
	Discoverable     bool        `json:"discoverable"`
	// Absent when the organizer's account was deleted.
	CreatedByUserID  string    `json:"created_by_user_id,omitempty"`
	GoingCount       int32     `json:"going_count"`
	MaybeCount       int32     `json:"maybe_count"`
	WaitlistCount    int32     `json:"waitlist_count"`
	CallerRSVP       string    `json:"caller_rsvp,omitempty"`
	CallerCanManage  bool      `json:"caller_can_manage"`
	CallerAttendance string    `json:"caller_attendance,omitempty"`
	SeriesID         string    `json:"series_id,omitempty"`
	SeriesOverride   bool      `json:"series_override"`
	CoverUploadID    string    `json:"cover_upload_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	Hike    *hikeJSON    `json:"hike,omitempty"`
	Fishing *fishingJSON `json:"fishing,omitempty"`

	// Fields holds the values of the type's fields by field key; left out
	// when the event has none and in lists.
	Fields map[string]json.RawMessage `json:"fields,omitempty"`
}

// eventTypeKey is the event's catalog key. An older core sends only the
// Faz 1 enum.
func eventTypeKey(e *corev1.Event) string {
	if k := e.GetTypeKey(); k != "" {
		return k
	}
	return eventTypes.name(e.GetType())
}

// eventFieldsFrom passes core's field values (JSON text by field key) on
// as JSON. core wrote them itself; a value that is not valid JSON is left
// out rather than breaking the body.
func eventFieldsFrom(in map[string]string) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		if json.Valid([]byte(v)) {
			out[k] = json.RawMessage(v)
		}
	}
	return out
}

func eventFrom(e *corev1.Event) eventJSON {
	out := eventJSON{
		ID: e.GetId(), TayfaID: e.GetTayfaId(), Type: eventTypeKey(e), TypeVersion: e.GetTypeVersion(), Mode: eventModes.name(e.GetMode()),
		Title: e.GetTitle(), Description: e.GetDescription(), Fields: eventFieldsFrom(e.GetFields()),
		StartsAt: e.GetStartTime().AsTime(), EndsAt: e.GetEndTime().AsTime(),
		MeetingPointName: e.GetMeetingPointName(), MeetingPoint: latLngFrom(e.GetMeetingPoint()),
		City: e.GetCity(), District: e.GetDistrict(), OnlineURL: e.GetOnlineUrl(),
		Capacity: e.GetCapacity(), Status: eventStatuses.name(e.GetStatus()),
		CancelledAt: optTime(e.GetCancelTime()), CancelReason: e.GetCancelReason(),
		Discoverable: e.GetDiscoverable(), CreatedByUserID: e.GetCreatedBy(),
		GoingCount: e.GetGoingCount(), MaybeCount: e.GetMaybeCount(), WaitlistCount: e.GetWaitlistCount(),
		CallerRSVP: optName(rsvpStatuses, e.GetCallerRsvp()), CallerCanManage: e.GetCallerCanManage(),
		CallerAttendance: optName(attendanceStatuses, e.GetCallerAttendance()),
		SeriesID:         e.GetSeriesId(), SeriesOverride: e.GetSeriesOverride(), CoverUploadID: e.GetCoverUploadId(),
		CreatedAt: e.GetCreateTime().AsTime(), UpdatedAt: e.GetUpdateTime().AsTime(),
	}
	switch d := e.GetDetails().(type) {
	case *corev1.Event_Hike:
		out.Hike = &hikeJSON{
			Difficulty: d.Hike.GetDifficulty(), DistanceKm: d.Hike.GetDistanceKm(), ElevationM: d.Hike.GetElevationM(),
			ExpectedReturnAt: optTime(d.Hike.GetExpectedReturnTime()), ReturnGraceMinutes: d.Hike.GetReturnGraceMinutes(),
		}
	case *corev1.Event_Fishing:
		out.Fishing = &fishingJSON{SpotVisibility: spotVisibilities.name(d.Fishing.GetSpotVisibility())}
	}
	return out
}

type eventListJSON struct {
	Events        []eventJSON `json:"events"`
	NextPageToken string      `json:"next_page_token,omitempty"`
}

type rsvpJSON struct {
	Status string `json:"status"`
}

type attendeeJSON struct {
	UserID           string    `json:"user_id"`
	Status           string    `json:"status"`
	UpdatedAt        time.Time `json:"updated_at"`
	Attendance       string    `json:"attendance,omitempty"`
	AttendanceMethod string    `json:"attendance_method,omitempty"`
}

type attendeeListJSON struct {
	Attendees     []attendeeJSON `json:"attendees"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

type checkInJSON struct {
	Attendance string `json:"attendance"`
	Method     string `json:"method"`
}

type hikeCheckinJSON struct {
	StartedAt  time.Time  `json:"started_at"`
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
}

func hikeCheckinFrom(c *corev1.HikeCheckin) hikeCheckinJSON {
	return hikeCheckinJSON{StartedAt: c.GetStartTime().AsTime(), ReturnedAt: optTime(c.GetReturnTime())}
}

type fishingCatchJSON struct {
	ID       string  `json:"id"`
	UserID   string  `json:"user_id"`
	Species  string  `json:"species"`
	LengthCm float64 `json:"length_cm"`
	// Only core decides who gets it.
	Location   *latLngJSON `json:"location,omitempty"`
	Visibility string      `json:"visibility"`
	CreatedAt  time.Time   `json:"created_at"`
	// PhotoUploadID: links with POST /v1/media/urls (issue #135).
	PhotoUploadID string `json:"photo_upload_id,omitempty"`
}

func fishingCatchFrom(c *corev1.FishingCatch) fishingCatchJSON {
	return fishingCatchJSON{
		ID: c.GetId(), UserID: c.GetUserId(), Species: c.GetSpecies(), LengthCm: c.GetLengthCm(),
		Location: latLngFrom(c.GetLocation()), Visibility: catchVisibilities.name(c.GetVisibility()),
		CreatedAt: c.GetCreateTime().AsTime(), PhotoUploadID: c.GetPhotoUploadId(),
	}
}

type fishingCatchListJSON struct {
	Catches       []fishingCatchJSON `json:"catches"`
	NextPageToken string             `json:"next_page_token,omitempty"`
}

// eventPhotoJSON is an album photo (issue #206). Links come from
// POST /v1/media/urls.
type eventPhotoJSON struct {
	ID         string    `json:"id"`
	EventID    string    `json:"event_id"`
	UploaderID string    `json:"uploader_id"`
	UploadID   string    `json:"upload_id"`
	Caption    string    `json:"caption,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func eventPhotoFrom(p *corev1.EventPhoto) eventPhotoJSON {
	return eventPhotoJSON{
		ID: p.GetId(), EventID: p.GetEventId(), UploaderID: p.GetUploaderId(), UploadID: p.GetUploadId(),
		Caption: p.GetCaption(), CreatedAt: p.GetCreateTime().AsTime(),
	}
}

type eventPhotoListJSON struct {
	Photos        []eventPhotoJSON `json:"photos"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

type eventSeriesJSON struct {
	ID              string     `json:"id"`
	TayfaID         string     `json:"tayfa_id"`
	TemplateEventID string     `json:"template_event_id,omitempty"`
	RRule           string     `json:"rrule"`
	TimeZone        string     `json:"time_zone"`
	StartsAt        time.Time  `json:"starts_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	CreatedByUserID string     `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func eventSeriesFrom(s *corev1.EventSeries) eventSeriesJSON {
	return eventSeriesJSON{
		ID: s.GetId(), TayfaID: s.GetTayfaId(), TemplateEventID: s.GetTemplateEventId(),
		RRule: s.GetRrule(), TimeZone: s.GetTimeZone(), StartsAt: s.GetStartTime().AsTime(),
		EndedAt: optTime(s.GetEndTime()), CreatedByUserID: s.GetCreatedBy(), CreatedAt: s.GetCreateTime().AsTime(),
	}
}

type eventSeriesEndingJSON struct {
	Series         eventSeriesJSON `json:"series"`
	CancelledCount int32           `json:"cancelled_count"`
}

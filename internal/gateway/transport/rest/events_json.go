package rest

import (
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
	gamingPlatforms = newEnum(map[string]corev1.GamingPlatform{
		"pc": corev1.GamingPlatform_GAMING_PLATFORM_PC, "playstation": corev1.GamingPlatform_GAMING_PLATFORM_PLAYSTATION,
		"xbox": corev1.GamingPlatform_GAMING_PLATFORM_XBOX, "switch": corev1.GamingPlatform_GAMING_PLATFORM_SWITCH,
		"mobile": corev1.GamingPlatform_GAMING_PLATFORM_MOBILE, "other": corev1.GamingPlatform_GAMING_PLATFORM_OTHER,
	})
	sports = newEnum(map[string]corev1.Sport{
		"football": corev1.Sport_SPORT_FOOTBALL, "table_football": corev1.Sport_SPORT_TABLE_FOOTBALL,
		"basketball": corev1.Sport_SPORT_BASKETBALL, "volleyball": corev1.Sport_SPORT_VOLLEYBALL,
		"tennis": corev1.Sport_SPORT_TENNIS, "table_tennis": corev1.Sport_SPORT_TABLE_TENNIS,
		"other": corev1.Sport_SPORT_OTHER,
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

type convoyVehicleJSON struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Make      string `json:"make"`
	Model     string `json:"model,omitempty"`
	ModelYear int32  `json:"model_year,omitempty"`
	Color     string `json:"color,omitempty"`
	// Only core decides who gets it (participants and the organizer).
	Plate string `json:"plate,omitempty"`
}

func convoyVehicleFrom(v *corev1.ConvoyVehicle) convoyVehicleJSON {
	return convoyVehicleJSON{
		ID: v.GetId(), UserID: v.GetUserId(), Make: v.GetMake(), Model: v.GetModel(),
		ModelYear: v.GetModelYear(), Color: v.GetColor(), Plate: v.GetPlate(),
	}
}

type convoyJSON struct {
	Route    []latLngJSON        `json:"route"`
	Vehicles []convoyVehicleJSON `json:"vehicles"`
}

type campItemJSON struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Quantity       int32  `json:"quantity"`
	AssignedUserID string `json:"assigned_user_id,omitempty"`
}

func campItemFrom(i *corev1.CampItem) campItemJSON {
	return campItemJSON{ID: i.GetId(), Name: i.GetName(), Quantity: i.GetQuantity(), AssignedUserID: i.GetAssignedUserId()}
}

type campJSON struct {
	SiteName  string         `json:"site_name"`
	HasWater  *bool          `json:"has_water,omitempty"`
	HasToilet *bool          `json:"has_toilet,omitempty"`
	Notes     string         `json:"notes"`
	Items     []campItemJSON `json:"items"`
}

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

// gamingJSON is used for both input and output.
type gamingJSON struct {
	Game     string `json:"game"`
	Platform string `json:"platform"`
}

// sportsJSON is used for both input and output.
type sportsJSON struct {
	Sport     string `json:"sport"`
	VenueName string `json:"venue_name"`
}

type eventJSON struct {
	ID               string      `json:"id"`
	TayfaID          string      `json:"tayfa_id"`
	Type             string      `json:"type"`
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
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	Convoy  *convoyJSON  `json:"convoy,omitempty"`
	Camp    *campJSON    `json:"camp,omitempty"`
	Hike    *hikeJSON    `json:"hike,omitempty"`
	Fishing *fishingJSON `json:"fishing,omitempty"`
	Gaming  *gamingJSON  `json:"gaming,omitempty"`
	Sports  *sportsJSON  `json:"sports,omitempty"`
}

func eventFrom(e *corev1.Event) eventJSON {
	out := eventJSON{
		ID: e.GetId(), TayfaID: e.GetTayfaId(), Type: eventTypes.name(e.GetType()), Mode: eventModes.name(e.GetMode()),
		Title: e.GetTitle(), Description: e.GetDescription(),
		StartsAt: e.GetStartTime().AsTime(), EndsAt: e.GetEndTime().AsTime(),
		MeetingPointName: e.GetMeetingPointName(), MeetingPoint: latLngFrom(e.GetMeetingPoint()),
		City: e.GetCity(), District: e.GetDistrict(), OnlineURL: e.GetOnlineUrl(),
		Capacity: e.GetCapacity(), Status: eventStatuses.name(e.GetStatus()),
		CancelledAt: optTime(e.GetCancelTime()), CancelReason: e.GetCancelReason(),
		Discoverable: e.GetDiscoverable(), CreatedByUserID: e.GetCreatedBy(),
		GoingCount: e.GetGoingCount(), MaybeCount: e.GetMaybeCount(), WaitlistCount: e.GetWaitlistCount(),
		CallerRSVP: optName(rsvpStatuses, e.GetCallerRsvp()), CallerCanManage: e.GetCallerCanManage(),
		CallerAttendance: optName(attendanceStatuses, e.GetCallerAttendance()),
		SeriesID:         e.GetSeriesId(), SeriesOverride: e.GetSeriesOverride(),
		CreatedAt: e.GetCreateTime().AsTime(), UpdatedAt: e.GetUpdateTime().AsTime(),
	}
	switch d := e.GetDetails().(type) {
	case *corev1.Event_Convoy:
		c := &convoyJSON{Route: make([]latLngJSON, 0, len(d.Convoy.GetRoute())), Vehicles: make([]convoyVehicleJSON, 0, len(d.Convoy.GetVehicles()))}
		for _, p := range d.Convoy.GetRoute() {
			c.Route = append(c.Route, latLngJSON{p.GetLat(), p.GetLng()})
		}
		for _, v := range d.Convoy.GetVehicles() {
			c.Vehicles = append(c.Vehicles, convoyVehicleFrom(v))
		}
		out.Convoy = c
	case *corev1.Event_Camp:
		c := &campJSON{SiteName: d.Camp.GetSiteName(), HasWater: d.Camp.HasWater, HasToilet: d.Camp.HasToilet, Notes: d.Camp.GetNotes(), Items: make([]campItemJSON, 0, len(d.Camp.GetItems()))}
		for _, i := range d.Camp.GetItems() {
			c.Items = append(c.Items, campItemFrom(i))
		}
		out.Camp = c
	case *corev1.Event_Hike:
		out.Hike = &hikeJSON{
			Difficulty: d.Hike.GetDifficulty(), DistanceKm: d.Hike.GetDistanceKm(), ElevationM: d.Hike.GetElevationM(),
			ExpectedReturnAt: optTime(d.Hike.GetExpectedReturnTime()), ReturnGraceMinutes: d.Hike.GetReturnGraceMinutes(),
		}
	case *corev1.Event_Fishing:
		out.Fishing = &fishingJSON{SpotVisibility: spotVisibilities.name(d.Fishing.GetSpotVisibility())}
	case *corev1.Event_Gaming:
		out.Gaming = &gamingJSON{Game: d.Gaming.GetGame(), Platform: gamingPlatforms.name(d.Gaming.GetPlatform())}
	case *corev1.Event_Sports:
		out.Sports = &sportsJSON{Sport: sports.name(d.Sports.GetSport()), VenueName: d.Sports.GetVenueName()}
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
}

func fishingCatchFrom(c *corev1.FishingCatch) fishingCatchJSON {
	return fishingCatchJSON{
		ID: c.GetId(), UserID: c.GetUserId(), Species: c.GetSpecies(), LengthCm: c.GetLengthCm(),
		Location: latLngFrom(c.GetLocation()), Visibility: catchVisibilities.name(c.GetVisibility()),
		CreatedAt: c.GetCreateTime().AsTime(),
	}
}

type fishingCatchListJSON struct {
	Catches       []fishingCatchJSON `json:"catches"`
	NextPageToken string             `json:"next_page_token,omitempty"`
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

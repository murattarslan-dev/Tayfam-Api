package event

import (
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Stored values (events.type, events.mode, …; 00004_events.sql).
const (
	TypeConvoy  = "convoy"
	TypeCamp    = "camp"
	TypeHike    = "hike"
	TypeFishing = "fishing"
	TypeGaming  = "gaming"
	TypeSports  = "sports"
	TypeGeneral = "general"

	ModeInPerson = "in_person"
	ModeOnline   = "online"

	StatusScheduled = "scheduled"
	StatusCancelled = "cancelled"

	RSVPGoing    = "going"
	RSVPMaybe    = "maybe"
	RSVPNotGoing = "not_going"
	RSVPWaitlist = "waitlist"

	SpotTayfa        = "tayfa"
	SpotParticipants = "participants"
)

// Types are the event types, in EventType order.
var Types = []string{TypeConvoy, TypeCamp, TypeHike, TypeFishing, TypeGaming, TypeSports, TypeGeneral}

// Platforms and SportKinds are the allowed values of event_gaming.platform and
// event_sports.sport.
var (
	Platforms  = []string{"pc", "playstation", "xbox", "switch", "mobile", "other"}
	SportKinds = []string{"football", "table_football", "basketball", "volleyball", "tennis", "table_tennis", "other"}
)

// Limits (data model v14 and 00004_events.sql; the same CHECKs are in the
// database).
const (
	titleMax        = 120
	descriptionMax  = 5000
	placeNameMax    = 120 // meeting point, camp site, sports venue
	cityMax         = 50
	onlineURLMax    = 2048
	capacityMax     = 10000
	cancelReasonMax = 500
	campNotesMax    = 1000
	gameMax         = 60
	itemNameMax     = 60
	itemQuantityMax = 999
	makeMax         = 40
	modelMax        = 40
	colorMax        = 30
	routeMin        = 2
	routeMax        = 500
	distanceMax     = 1000
	elevationMax    = 9000
	graceDefault    = 30
	graceMax        = 1440
	modelYearMin    = 1900

	// startAhead bounds how far ahead an event may start; maxDuration how
	// long it may last.
	startAhead  = 2 * 366 * 24 * time.Hour
	maxDuration = 30 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// platePattern is the event_convoy_vehicles_plate CHECK; single spaces only
// (checked separately).
var platePattern = regexp.MustCompile(`^[0-9A-Z][0-9A-Z ]{0,14}[0-9A-Z]$`)

// Point is a WGS 84 coordinate in degrees.
type Point struct {
	Lat, Lng float64
}

func (p Point) valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

// Location is where an event happens: in person (meeting point, city,
// district) or online (URL).
type Location struct {
	Mode             string
	MeetingPointName string
	// MeetingPoint is nil for online events.
	MeetingPoint *Point
	City         string
	District     string
	OnlineURL    string
}

// Details are the type-specific fields; at most one is set and it matches
// the event's type.
type Details struct {
	Convoy  *Convoy
	Camp    *Camp
	Hike    *Hike
	Fishing *Fishing
	Gaming  *Gaming
	Sports  *Sports
}

// typ returns the event type the set field belongs to, "" when none is set,
// or "?" when more than one is.
func (d Details) typ() string {
	t := ""
	for _, c := range []struct {
		set bool
		typ string
	}{
		{d.Convoy != nil, TypeConvoy}, {d.Camp != nil, TypeCamp}, {d.Hike != nil, TypeHike},
		{d.Fishing != nil, TypeFishing}, {d.Gaming != nil, TypeGaming}, {d.Sports != nil, TypeSports},
	} {
		if c.set {
			if t != "" {
				return "?"
			}
			t = c.typ
		}
	}
	return t
}

// Convoy holds a convoy's route and, in views, its vehicles.
type Convoy struct {
	Route    []Point
	Vehicles []Vehicle
}

// Vehicle is a convoy participant's vehicle.
type Vehicle struct {
	ID        id.ID
	UserID    id.ID
	Make      string
	Model     string
	ModelYear int32
	Color     string
	// Plate is empty for callers who are not participants.
	Plate string
}

// Camp holds a camp's site details and, in views, its item list.
type Camp struct {
	SiteName  string
	HasWater  *bool
	HasToilet *bool
	Notes     string
	Items     []CampItem
}

// CampItem is one entry of a camp's list.
type CampItem struct {
	ID       id.ID
	Name     string
	Quantity int32
	// AssignedUserID is zero when nobody is assigned.
	AssignedUserID id.ID
}

// Hike holds a hike's details. Zero values mean "not given".
type Hike struct {
	Difficulty         int32
	DistanceKm         float64
	ElevationM         int32
	ExpectedReturnAt   time.Time
	ReturnGraceMinutes int32
}

// Fishing holds who sees the spot.
type Fishing struct {
	// SpotVisibility is SpotTayfa or SpotParticipants; "" means SpotTayfa.
	SpotVisibility string
}

// Gaming holds the game and platform.
type Gaming struct {
	Game     string
	Platform string
}

// Sports holds the sport and venue.
type Sports struct {
	Sport     string
	VenueName string
}

// cleanText trims s and checks its length in characters. Empty is allowed
// when lo is 0.
func cleanText(s, field string, lo, hi int, multiline bool) (string, error) {
	if len(s) > (hi+64)*maxRuneBytes {
		return "", apperr.Validation(field)
	}
	t := strings.TrimSpace(s)
	if !validText(t, multiline) {
		return "", apperr.Validation(field)
	}
	if n := utf8.RuneCountInString(t); n < lo || n > hi {
		return "", apperr.Validation(field)
	}
	return t, nil
}

// validText refuses invalid UTF-8 and control characters; multiline text
// may contain line breaks and tabs.
func validText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// cleanTime checks an event's start and end: the start is in the future
// and at most startAhead away, the end is after the start and at most
// maxDuration later.
func cleanTime(start, end, now time.Time) (time.Time, time.Time, error) {
	if start.IsZero() || !start.After(now) || start.Sub(now) > startAhead {
		return time.Time{}, time.Time{}, apperr.Validation("start_time")
	}
	if end.IsZero() || !end.After(start) || end.Sub(start) > maxDuration {
		return time.Time{}, time.Time{}, apperr.Validation("end_time")
	}
	return start.UTC().Truncate(time.Microsecond), end.UTC().Truncate(time.Microsecond), nil
}

// cleanLocation checks a location against its mode (events_mode_fields).
func cleanLocation(l Location) (Location, error) {
	var out Location
	switch l.Mode {
	case ModeInPerson:
		if l.OnlineURL != "" {
			return Location{}, apperr.Validation("online_url")
		}
		if l.MeetingPoint == nil || !l.MeetingPoint.valid() {
			return Location{}, apperr.Validation("meeting_point")
		}
		p := *l.MeetingPoint
		var err error
		if out.MeetingPointName, err = cleanText(l.MeetingPointName, "meeting_point_name", 0, placeNameMax, false); err != nil {
			return Location{}, err
		}
		if out.City, err = cleanText(l.City, "city", 1, cityMax, false); err != nil {
			return Location{}, err
		}
		if out.District, err = cleanText(l.District, "district", 1, cityMax, false); err != nil {
			return Location{}, err
		}
		out.Mode, out.MeetingPoint = ModeInPerson, &p
	case ModeOnline:
		switch {
		case l.MeetingPoint != nil:
			return Location{}, apperr.Validation("meeting_point")
		case l.MeetingPointName != "":
			return Location{}, apperr.Validation("meeting_point_name")
		case l.City != "":
			return Location{}, apperr.Validation("city")
		case l.District != "":
			return Location{}, apperr.Validation("district")
		}
		u, err := cleanURL(l.OnlineURL)
		if err != nil {
			return Location{}, err
		}
		out.Mode, out.OnlineURL = ModeOnline, u
	default:
		return Location{}, apperr.Validation("mode")
	}
	return out, nil
}

// cleanURL accepts only absolute https URLs with a host and without user
// info, spaces or control characters (javascript: and the like are
// refused).
func cleanURL(s string) (string, error) {
	bad := apperr.Validation("online_url")
	t := strings.TrimSpace(s)
	if t == "" || len(t) > onlineURLMax || !utf8.ValidString(t) {
		return "", bad
	}
	for _, r := range t {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", bad
		}
	}
	u, err := url.Parse(t)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", bad
	}
	// The stored form must start with "https://" (events_online_url).
	if !strings.HasPrefix(t, "https://") {
		return "", bad
	}
	return t, nil
}

func cleanCapacity(c int32) (int32, error) {
	if c < 0 || c > capacityMax {
		return 0, apperr.Validation("capacity")
	}
	return c, nil
}

// cleanDetails checks d for an event of type typ starting at start. create
// fills in the default details of types that have no required field.
func cleanDetails(typ string, d Details, start time.Time, create bool) (Details, error) {
	bad := apperr.Validation("details")
	dt := d.typ()
	switch {
	case dt == "?":
		return Details{}, bad
	case dt == "" && typ == TypeGeneral:
		return Details{}, nil
	case dt == "" && create:
		switch typ {
		case TypeConvoy:
			d.Convoy = &Convoy{}
		case TypeCamp:
			d.Camp = &Camp{}
		case TypeHike:
			d.Hike = &Hike{}
		case TypeFishing:
			d.Fishing = &Fishing{}
		default:
			// Gaming and sports have required fields.
			return Details{}, bad
		}
	case dt != typ:
		return Details{}, bad
	}

	var out Details
	var err error
	switch typ {
	case TypeConvoy:
		c := Convoy{}
		if n := len(d.Convoy.Route); n != 0 && (n < routeMin || n > routeMax) {
			return Details{}, apperr.Validation("route")
		}
		for _, p := range d.Convoy.Route {
			if !p.valid() {
				return Details{}, apperr.Validation("route")
			}
			c.Route = append(c.Route, p)
		}
		out.Convoy = &c
	case TypeCamp:
		c := Camp{HasWater: d.Camp.HasWater, HasToilet: d.Camp.HasToilet}
		if c.SiteName, err = cleanText(d.Camp.SiteName, "site_name", 0, placeNameMax, false); err != nil {
			return Details{}, err
		}
		if c.Notes, err = cleanText(d.Camp.Notes, "notes", 0, campNotesMax, true); err != nil {
			return Details{}, err
		}
		out.Camp = &c
	case TypeHike:
		h := *d.Hike
		if h.Difficulty < 0 || h.Difficulty > 5 {
			return Details{}, apperr.Validation("difficulty")
		}
		if math.IsNaN(h.DistanceKm) || h.DistanceKm < 0 || h.DistanceKm > distanceMax {
			return Details{}, apperr.Validation("distance_km")
		}
		h.DistanceKm = math.Round(h.DistanceKm*100) / 100
		if h.ElevationM < 0 || h.ElevationM > elevationMax {
			return Details{}, apperr.Validation("elevation_m")
		}
		if !h.ExpectedReturnAt.IsZero() {
			if !h.ExpectedReturnAt.After(start) {
				return Details{}, apperr.Validation("expected_return_time")
			}
			h.ExpectedReturnAt = h.ExpectedReturnAt.UTC().Truncate(time.Microsecond)
		}
		switch {
		case h.ReturnGraceMinutes == 0:
			h.ReturnGraceMinutes = graceDefault
		case h.ReturnGraceMinutes < 0 || h.ReturnGraceMinutes > graceMax:
			return Details{}, apperr.Validation("return_grace_minutes")
		}
		out.Hike = &h
	case TypeFishing:
		f := *d.Fishing
		switch f.SpotVisibility {
		case "":
			f.SpotVisibility = SpotTayfa
		case SpotTayfa, SpotParticipants:
		default:
			return Details{}, apperr.Validation("spot_visibility")
		}
		out.Fishing = &f
	case TypeGaming:
		g := Gaming{Platform: d.Gaming.Platform}
		if g.Game, err = cleanText(d.Gaming.Game, "game", 1, gameMax, false); err != nil {
			return Details{}, err
		}
		if !slices.Contains(Platforms, g.Platform) {
			return Details{}, apperr.Validation("platform")
		}
		out.Gaming = &g
	case TypeSports:
		sp := Sports{Sport: d.Sports.Sport}
		if !slices.Contains(SportKinds, sp.Sport) {
			return Details{}, apperr.Validation("sport")
		}
		if sp.VenueName, err = cleanText(d.Sports.VenueName, "venue_name", 0, placeNameMax, false); err != nil {
			return Details{}, err
		}
		out.Sports = &sp
	default:
		return Details{}, bad
	}
	return out, nil
}

// VehicleInput is a convoy vehicle to add.
type VehicleInput struct {
	Make      string
	Model     string
	ModelYear int32
	Color     string
	Plate     string
}

func cleanVehicle(v VehicleInput, now time.Time) (VehicleInput, error) {
	var (
		out VehicleInput
		err error
	)
	if out.Make, err = cleanText(v.Make, "make", 1, makeMax, false); err != nil {
		return VehicleInput{}, err
	}
	if out.Model, err = cleanText(v.Model, "model", 0, modelMax, false); err != nil {
		return VehicleInput{}, err
	}
	if out.Color, err = cleanText(v.Color, "color", 0, colorMax, false); err != nil {
		return VehicleInput{}, err
	}
	if v.ModelYear != 0 && (v.ModelYear < modelYearMin || int(v.ModelYear) > now.Year()+1) {
		return VehicleInput{}, apperr.Validation("model_year")
	}
	out.ModelYear = v.ModelYear
	if out.Plate, err = cleanPlate(v.Plate); err != nil {
		return VehicleInput{}, err
	}
	return out, nil
}

// cleanPlate upper-cases the plate and collapses runs of spaces; the
// result has only A–Z, digits and single spaces. Empty means no plate.
func cleanPlate(s string) (string, error) {
	if len(s) > 64 {
		return "", apperr.Validation("plate")
	}
	p := strings.Join(strings.Fields(strings.ToUpper(s)), " ")
	if p == "" {
		return "", nil
	}
	if !platePattern.MatchString(p) {
		return "", apperr.Validation("plate")
	}
	return p, nil
}

package event

import (
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
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

// Types are the seven Faz 1 event types, in EventType order. The type of
// an event is a catalog key (ADR-0025): any active type of the catalog can
// be created, not only these.
var Types = []string{TypeConvoy, TypeCamp, TypeHike, TypeFishing, TypeGaming, TypeSports, TypeGeneral}

// detailTypes are the types that still keep data in a Faz 1 detail table
// (event_fishing) until their components move it (issue #243). Every other
// type has only the fields of its catalog definition (event_field_values)
// and its components; the convoy's route and vehicles, the camp's list and
// the hike's roll call are components (issues #239–#242), the camp's site
// details and the hike's difficulty, distance and elevation are fields
// (issues #241, #242).
var detailTypes = []string{TypeFishing}

// Limits (data model v14 and 00004_events.sql; the same CHECKs are in the
// database).
const (
	titleMax        = 120
	descriptionMax  = 5000
	placeNameMax    = 120 // meeting point
	cityMax         = 50
	onlineURLMax    = 2048
	capacityMax     = 10000
	cancelReasonMax = 500

	// startAhead bounds how far ahead an event may start; maxDuration how
	// long it may last.
	startAhead  = 2 * 366 * 24 * time.Hour
	maxDuration = 30 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

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

// Details are the Faz 1 type details of the types in detailTypes; at most
// one is set and it matches the event's type.
type Details struct {
	Fishing *Fishing
}

// typ returns the event type the set field belongs to, "" when none is set,
// or "?" when more than one is.
func (d Details) typ() string {
	t := ""
	for _, c := range []struct {
		set bool
		typ string
	}{
		{d.Fishing != nil, TypeFishing},
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

// Fishing holds who sees the spot.
type Fishing struct {
	// SpotVisibility is SpotTayfa or SpotParticipants; "" means SpotTayfa.
	SpotVisibility string
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
	case !slices.Contains(detailTypes, typ):
		// No detail table: the type's data is in its fields.
		if dt != "" {
			return Details{}, bad
		}
		return Details{}, nil
	case dt == "" && create:
		if typ == TypeFishing {
			d.Fishing = &Fishing{}
		}
	case dt != typ:
		return Details{}, bad
	}

	var out Details
	switch typ {
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
	default:
		return Details{}, bad
	}
	return out, nil
}

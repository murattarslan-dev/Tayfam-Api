package event

import (
	"context"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event/ical"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Export returns the event as an iCalendar file ("Takvime ekleme", data
// model v14: the file holds only what the member may see). It is built
// from Get's view, so the same access rules apply: only active members,
// and a participants-only fishing spot stays hidden from others.
//
// Privacy choices: the location is the meeting point's name, district and
// city, never the coordinates (no GEO property), because the file leaves
// the app and is often synced to third-party calendars. No attendees,
// organizer or other members' data. An online event's link is included:
// only active members reach this method.
func (s *Service) Export(ctx context.Context, user, tayfa, event id.ID) ([]byte, error) {
	v, err := s.Get(ctx, user, tayfa, event)
	if err != nil {
		return nil, err
	}
	return ical.Write(icalEvent(v), s.now()), nil
}

func icalEvent(v Event) ical.Event {
	e := ical.Event{
		UID: v.ID.String(), Start: v.StartsAt, End: v.EndsAt, Created: v.CreatedAt, Modified: v.UpdatedAt,
		Summary: v.Title, Description: v.Description, Cancelled: v.Status == StatusCancelled,
	}
	switch v.Mode {
	case ModeOnline:
		e.Location, e.URL = v.OnlineURL, v.OnlineURL
	case ModeInPerson:
		var parts []string
		for _, p := range []string{v.MeetingPointName, v.District, v.City} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		e.Location = strings.Join(parts, ", ")
	}
	return e
}

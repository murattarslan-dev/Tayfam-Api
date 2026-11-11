package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Get returns one event with its type details.
func (s *Service) Get(ctx context.Context, user, tayfa, event id.ID) (Event, error) {
	c, err := s.load(ctx, s.q, user, tayfa, false)
	if err != nil {
		return Event{}, err
	}
	if err := c.authorize(authz.EventView); err != nil {
		return Event{}, err
	}
	ev, err := getEvent(ctx, s.q, tayfa, event)
	if err != nil {
		return Event{}, err
	}
	out, err := s.views(ctx, c, []store.GetEventRow{ev})
	if err != nil {
		return Event{}, err
	}
	v := out[0]
	if v.Details, err = s.details(ctx, c, ev, v); err != nil {
		return Event{}, err
	}
	return v, nil
}

// ListUpcoming lists the tayfa's events that have not ended, cancelled ones
// included, by start time. Details are not filled.
func (s *Service) ListUpcoming(ctx context.Context, user, tayfa id.ID, size int32, token string) ([]Event, string, error) {
	size, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	after, at, err := decodeCursor(token, "ListUpcomingEvents", tayfa, "")
	if err != nil {
		return nil, "", err
	}
	c, err := s.load(ctx, s.q, user, tayfa, false)
	if err != nil {
		return nil, "", err
	}
	if err := c.authorize(authz.EventView); err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListUpcomingEvents(ctx, store.ListUpcomingEventsParams{
		TayfaID: tayfa.PG(), Now: ts(c.now), AfterStartsAt: ts(at), AfterID: after.PG(), PageSize: size + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("etkinlikler: %w", err)
	}
	next := ""
	if len(rows) > int(size) {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = encodeCursor("ListUpcomingEvents", tayfa, "", last.StartsAt.Time, id.ID(last.ID.Bytes))
	}
	evs := make([]store.GetEventRow, len(rows))
	for i, r := range rows {
		evs[i] = store.GetEventRow(r)
	}
	out, err := s.views(ctx, c, evs)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// views builds the views of rows with batched queries: answer counts, the
// caller's answers and fishing spot visibility.
func (s *Service) views(ctx context.Context, c caller, rows []store.GetEventRow) ([]Event, error) {
	if len(rows) == 0 {
		return []Event{}, nil
	}
	ids := make([]pgtype.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	counts, err := s.q.CountRsvpsForEvents(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("katılım sayıları: %w", err)
	}
	mine, err := s.q.ListUserRsvpsForEvents(ctx, store.ListUserRsvpsForEventsParams{UserID: c.user.PG(), EventIds: ids})
	if err != nil {
		return nil, fmt.Errorf("katılımlarım: %w", err)
	}
	spots, err := s.q.ListFishingVisibility(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("mera görünürlüğü: %w", err)
	}
	byCount := map[id.ID]map[string]int32{}
	for _, r := range counts {
		k := id.ID(r.EventID.Bytes)
		if byCount[k] == nil {
			byCount[k] = map[string]int32{}
		}
		byCount[k][r.Status] = int32(min(r.N, 1<<31-1)) //nolint:gosec // clamped
	}
	byMine := map[id.ID]string{}
	attendance := map[id.ID]string{}
	for _, r := range mine {
		byMine[id.ID(r.EventID.Bytes)] = r.Status
		attendance[id.ID(r.EventID.Bytes)] = r.Attendance
	}
	bySpot := map[id.ID]string{}
	for _, r := range spots {
		bySpot[id.ID(r.EventID.Bytes)] = r.SpotVisibility
	}

	out := make([]Event, len(rows))
	for i, r := range rows {
		k := id.ID(r.ID.Bytes)
		v := Event{
			ID: k, TayfaID: id.ID(r.TayfaID.Bytes), Type: r.Type, Title: r.Title, Description: r.Description.String,
			StartsAt: r.StartsAt.Time.UTC(), EndsAt: r.EndsAt.Time.UTC(),
			Location: Location{
				Mode: r.Mode, MeetingPointName: r.MeetingPointName.String, City: r.City.String,
				District: r.District.String, OnlineURL: r.OnlineUrl.String,
			},
			Capacity: r.Capacity.Int32, Status: r.Status, CancelReason: r.CancelReason.String,
			Discoverable: r.Discoverable, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
			Going: byCount[k][RSVPGoing], Maybe: byCount[k][RSVPMaybe], Waitlist: byCount[k][RSVPWaitlist],
			CallerRSVP: byMine[k], CallerCanManage: c.canManage(r),
			CallerAttendance: AttendanceUnknown,
		}
		if a, ok := attendance[k]; ok {
			v.CallerAttendance = a
		}
		if r.MeetingLat.Valid && r.MeetingLng.Valid {
			v.MeetingPoint = &Point{Lat: r.MeetingLat.Float64, Lng: r.MeetingLng.Float64}
		}
		if r.CancelledAt.Valid {
			v.CancelledAt = r.CancelledAt.Time.UTC()
		}
		if r.CoverUploadID.Valid {
			v.CoverUploadID = id.ID(r.CoverUploadID.Bytes)
		}
		if r.CreatedBy.Valid {
			v.CreatedBy = id.ID(r.CreatedBy.Bytes)
		}
		if r.SeriesID.Valid {
			v.SeriesID, v.SeriesOverride = id.ID(r.SeriesID.Bytes), r.SeriesOverride
		}
		// A secret fishing spot is shown only to participants, the
		// organizer, moderators and admins.
		if bySpot[k] == SpotParticipants && !c.seesSpot(r, v.CallerRSVP) {
			v.MeetingPoint, v.MeetingPointName = nil, ""
		}
		out[i] = v
	}
	return out, nil
}

// seesSpot reports whether the caller may see a participants-only spot.
// This is a read, so a mute does not hide it.
func (c caller) seesSpot(ev store.GetEventRow, rsvp string) bool {
	if rsvp == RSVPGoing || rsvp == RSVPMaybe {
		return true
	}
	if ev.CreatedBy.Valid && id.ID(ev.CreatedBy.Bytes) == c.user {
		return true
	}
	unmuted := c.m
	unmuted.MutedUntil = time.Time{}
	return authz.AllowedIn(unmuted, authz.EventEditAny, c.now, c.settings)
}

// participant reports whether the caller may see plates on a convoy: an
// answer of going, or the organizer.
func (c caller) participant(ev store.GetEventRow, rsvp string) bool {
	return rsvp == RSVPGoing || (ev.CreatedBy.Valid && id.ID(ev.CreatedBy.Bytes) == c.user)
}

// details loads the type details of ev for Get, with the convoy's vehicles
// and the camp's items.
func (s *Service) details(ctx context.Context, c caller, ev store.GetEventRow, v Event) (Details, error) {
	d, err := typeDetails(ctx, s.q, ev)
	if err != nil {
		return Details{}, err
	}
	switch {
	case d.Convoy != nil:
		d.Convoy.Vehicles = []Vehicle{}
		vs, err := s.q.ListConvoyVehicles(ctx, ev.ID)
		if err != nil {
			return Details{}, fmt.Errorf("araçlar: %w", err)
		}
		seesPlates := c.participant(ev, v.CallerRSVP)
		for _, r := range vs {
			veh := Vehicle{
				ID: id.ID(r.ID.Bytes), UserID: id.ID(r.UserID.Bytes), Make: r.Make, Model: r.Model.String,
				ModelYear: r.ModelYear.Int32, Color: r.Color.String,
			}
			// Plates are personal data: only the event's participants and
			// the organizer see them, and everyone sees their own.
			if seesPlates || veh.UserID == c.user {
				veh.Plate = r.Plate.String
			}
			d.Convoy.Vehicles = append(d.Convoy.Vehicles, veh)
		}
	case d.Camp != nil:
		d.Camp.Items = []CampItem{}
		items, err := s.q.ListCampItems(ctx, ev.ID)
		if err != nil {
			return Details{}, fmt.Errorf("kamp malzemeleri: %w", err)
		}
		for _, it := range items {
			ci := CampItem{ID: id.ID(it.ID.Bytes), Name: it.Name, Quantity: it.Quantity}
			if it.AssignedUserID.Valid {
				ci.AssignedUserID = id.ID(it.AssignedUserID.Bytes)
			}
			d.Camp.Items = append(d.Camp.Items, ci)
		}
	}
	return d, nil
}

// typeDetails loads the type details of ev from its type's table, without
// lists (vehicles, camp items). A series copies these to new occurrences.
func typeDetails(ctx context.Context, q *store.Queries, ev store.GetEventRow) (Details, error) {
	evID := ev.ID
	var d Details
	var err error
	switch ev.Type {
	case TypeConvoy:
		var route []byte
		route, err = q.GetEventConvoy(ctx, evID)
		if err != nil {
			break
		}
		cv := &Convoy{Route: []Point{}}
		if len(route) > 0 {
			var pts []struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			}
			if err := json.Unmarshal(route, &pts); err != nil {
				return Details{}, fmt.Errorf("rota: %w", err)
			}
			for _, p := range pts {
				cv.Route = append(cv.Route, Point{Lat: p.Lat, Lng: p.Lng})
			}
		}
		d.Convoy = cv
	case TypeCamp:
		var r store.GetEventCampRow
		r, err = q.GetEventCamp(ctx, evID)
		if err != nil {
			break
		}
		cp := &Camp{SiteName: r.SiteName.String, Notes: r.Notes.String}
		if r.HasWater.Valid {
			cp.HasWater = &r.HasWater.Bool
		}
		if r.HasToilet.Valid {
			cp.HasToilet = &r.HasToilet.Bool
		}
		d.Camp = cp
	case TypeHike:
		var r store.GetEventHikeRow
		r, err = q.GetEventHike(ctx, evID)
		if err != nil {
			break
		}
		h := &Hike{Difficulty: r.Difficulty.Int32, DistanceKm: r.DistanceKm, ElevationM: r.ElevationM.Int32, ReturnGraceMinutes: r.ReturnGraceMinutes}
		if r.ExpectedReturnAt.Valid {
			h.ExpectedReturnAt = r.ExpectedReturnAt.Time.UTC()
		}
		d.Hike = h
	case TypeFishing:
		var vis string
		vis, err = q.GetEventFishing(ctx, evID)
		d.Fishing = &Fishing{SpotVisibility: vis}
	case TypeGaming:
		var r store.GetEventGamingRow
		r, err = q.GetEventGaming(ctx, evID)
		d.Gaming = &Gaming{Game: r.Game, Platform: r.Platform}
	case TypeSports:
		var r store.GetEventSportsRow
		r, err = q.GetEventSports(ctx, evID)
		d.Sports = &Sports{Sport: r.Sport, VenueName: r.VenueName.String}
	}
	// Every event except a general one is created with its detail row; a
	// missing row means an inconsistency, not a client error.
	if errors.Is(err, pgx.ErrNoRows) {
		return Details{}, fmt.Errorf("etkinlik detayı yok: %w", err)
	}
	if err != nil {
		return Details{}, fmt.Errorf("etkinlik detayı: %w", err)
	}
	return d, nil
}

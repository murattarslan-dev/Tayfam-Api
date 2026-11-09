package event

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Fishing catch visibility (fishing_catches.visibility): who gets the
// catch's location. The catch itself (species, length) is seen by every
// active member.
const (
	// CatchSelf: the location goes to the angler only.
	CatchSelf = "self"
	// CatchTayfa: the location goes to everyone who may see the event's
	// spot (SpotVisibility).
	CatchTayfa = "tayfa"
)

// Outbox event types and audit actions of the catch log.
const (
	CatchAdded   = "fishing_catch.added"
	CatchRemoved = "fishing_catch.removed"
)

const (
	speciesMax = 60
	// lengthMax is in centimetres (fishing_catches_length_cm).
	lengthMax = 1000
	// catchesPerUser bounds one angler's catches on one event.
	catchesPerUser = 100
)

// Catch is a logged fishing catch.
type Catch struct {
	ID       id.ID
	UserID   id.ID
	Species  string
	LengthCm float64
	// Location is nil when not logged or not visible to the caller.
	Location   *Point
	Visibility string
	CreatedAt  time.Time
}

// CatchInput is a new catch.
type CatchInput struct {
	Species  string
	LengthCm float64
	// Location is optional.
	Location *Point
	// Visibility "" means CatchSelf.
	Visibility      string
	ClientRequestID string
}

func cleanCatch(in CatchInput) (CatchInput, error) {
	sp, err := cleanText(in.Species, "species", 1, speciesMax, false)
	if err != nil {
		return CatchInput{}, err
	}
	in.Species = sp
	if math.IsNaN(in.LengthCm) || in.LengthCm <= 0 || in.LengthCm > lengthMax {
		return CatchInput{}, apperr.Validation("length_cm")
	}
	// numeric(5, 1): one decimal.
	in.LengthCm = math.Round(in.LengthCm*10) / 10
	if in.LengthCm <= 0 {
		return CatchInput{}, apperr.Validation("length_cm")
	}
	if in.Location != nil && !in.Location.valid() {
		return CatchInput{}, apperr.Validation("location")
	}
	switch in.Visibility {
	case "":
		in.Visibility = CatchSelf
	case CatchSelf, CatchTayfa:
	default:
		return CatchInput{}, apperr.Validation("visibility")
	}
	return in, nil
}

// AddCatch logs a catch on a fishing event. The caller must have verified
// attendance; catches are accepted until the correction window closes (seven
// days after the end). A repeat with the same ClientRequestID returns the
// first catch. The location never reaches the audit record or the event.
func (s *Service) AddCatch(ctx context.Context, user, tayfa, event id.ID, in CatchInput) (Catch, error) {
	in, err := cleanCatch(in)
	if err != nil {
		return Catch{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Catch{}, apperr.Validation("client_request_id")
	}
	var out Catch
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCatchAdd); err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeFishing {
			return apperr.ErrEventTypeMismatch
		}
		if ev.Status == StatusCancelled {
			return apperr.ErrEventCancelled
		}
		if !inCorrectionWindow(ev, c.now) {
			return apperr.ErrEventEnded
		}
		prev, err := q.GetCatchByClientRequest(ctx, store.GetCatchByClientRequestParams{UserID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid || id.ID(prev.EventID.Bytes) != event {
				return apperr.Validation("client_request_id")
			}
			r, err := q.GetCatch(ctx, store.GetCatchParams{EventID: event.PG(), ID: prev.ID})
			if err != nil {
				return fmt.Errorf("av kaydı: %w", err)
			}
			out = catchOf(r, true)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		r, ok, err := rsvpRow(ctx, q, event, user)
		if err != nil {
			return err
		}
		if !ok || r.Attendance != AttendanceAttended {
			return apperr.ErrNotAttended
		}
		n, err := q.CountUserCatches(ctx, store.CountUserCatchesParams{EventID: event.PG(), UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("av kayıtları: %w", err)
		}
		if n >= catchesPerUser {
			return apperr.ErrEventLimit
		}
		cid := id.New()
		p := store.CreateCatchParams{
			ID: cid.PG(), EventID: event.PG(), UserID: user.PG(), Species: in.Species, LengthCm: in.LengthCm,
			Visibility: in.Visibility, ClientRequestID: crid.PG(),
		}
		if in.Location != nil {
			p.Lat = pgtype.Float8{Float64: in.Location.Lat, Valid: true}
			p.Lng = pgtype.Float8{Float64: in.Location.Lng, Valid: true}
		}
		if err := q.CreateCatch(ctx, p); err != nil {
			return fmt.Errorf("av kaydı: %w", err)
		}
		out = Catch{ID: cid, UserID: user, Species: in.Species, LengthCm: in.LengthCm, Location: in.Location, Visibility: in.Visibility, CreatedAt: c.now}
		meta := map[string]any{"catch_id": cid.String()}
		if err := audit(ctx, q, c, CatchAdded, event, meta); err != nil {
			return err
		}
		b.add(CatchAdded, event, map[string]any{"event_id": event.String(), "catch_id": cid.String(), "user_id": user.String()})
		return nil
	})
	if err != nil {
		return Catch{}, err
	}
	return out, nil
}

// ListCatches lists an event's catches, oldest first. Every active member
// sees them; a catch's location goes to its angler, and for CatchTayfa also
// to those who may see the event's spot.
func (s *Service) ListCatches(ctx context.Context, user, tayfa, event id.ID, size int32, token string) ([]Catch, string, error) {
	size, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	after, at, err := decodeCursor(token, "ListCatches", tayfa, event.String())
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
	ev, err := getEvent(ctx, s.q, tayfa, event)
	if err != nil {
		return nil, "", err
	}
	if ev.Type != TypeFishing {
		return nil, "", apperr.ErrEventTypeMismatch
	}
	spot, err := s.seesSpotOf(ctx, c, ev)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListCatches(ctx, store.ListCatchesParams{EventID: event.PG(), AfterTime: ts(at), AfterID: after.PG(), PageSize: size + 1})
	if err != nil {
		return nil, "", fmt.Errorf("av kayıtları: %w", err)
	}
	next := ""
	if len(rows) > int(size) {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = encodeCursor("ListCatches", tayfa, event.String(), last.CreatedAt.Time, id.ID(last.ID.Bytes))
	}
	out := make([]Catch, len(rows))
	for i, r := range rows {
		own := id.ID(r.UserID.Bytes) == user
		out[i] = catchOf(store.GetCatchRow(r), own || (r.Visibility == CatchTayfa && spot))
	}
	return out, next, nil
}

// seesSpotOf reports whether the caller may see the fishing event's spot.
func (s *Service) seesSpotOf(ctx context.Context, c caller, ev store.GetEventRow) (bool, error) {
	vis, err := s.q.GetEventFishing(ctx, ev.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		vis = SpotTayfa
	case err != nil:
		return false, fmt.Errorf("mera görünürlüğü: %w", err)
	}
	if vis != SpotParticipants {
		return true, nil
	}
	rsvp := ""
	r, err := s.q.GetRsvp(ctx, store.GetRsvpParams{EventID: ev.ID, UserID: c.user.PG()})
	switch {
	case err == nil:
		rsvp = r.Status
	case !errors.Is(err, pgx.ErrNoRows):
		return false, fmt.Errorf("katılım: %w", err)
	}
	return c.seesSpot(ev, rsvp), nil
}

// RemoveCatch removes a catch (soft delete): one's own, or anyone's for
// moderators and admins. Works on cancelled and ended events too.
func (s *Service) RemoveCatch(ctx context.Context, user, tayfa, event, catch id.ID) error {
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeFishing {
			return apperr.ErrEventTypeMismatch
		}
		r, err := q.GetCatch(ctx, store.GetCatchParams{EventID: event.PG(), ID: catch.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("av kaydı: %w", err)
		}
		if id.ID(r.UserID.Bytes) == user {
			err = c.authorize(authz.EventCatchAdd)
		} else {
			err = c.authorize(authz.EventCatchDeleteAny)
		}
		if err != nil {
			return err
		}
		n, err := q.DeleteCatch(ctx, store.DeleteCatchParams{DeletedAt: ts(c.now), DeletedBy: user.PG(), EventID: event.PG(), ID: catch.PG()})
		if err != nil {
			return fmt.Errorf("av kaydı: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		if err := audit(ctx, q, c, CatchRemoved, event, map[string]any{"catch_id": catch.String()}); err != nil {
			return err
		}
		b.add(CatchRemoved, event, map[string]any{"event_id": event.String(), "catch_id": catch.String()})
		return nil
	})
}

func catchOf(r store.GetCatchRow, withLocation bool) Catch {
	out := Catch{
		ID: id.ID(r.ID.Bytes), UserID: id.ID(r.UserID.Bytes), Species: r.Species, LengthCm: r.LengthCm,
		Visibility: r.Visibility, CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if withLocation && r.Lat.Valid && r.Lng.Valid {
		out.Location = &Point{Lat: r.Lat.Float64, Lng: r.Lng.Float64}
	}
	return out
}

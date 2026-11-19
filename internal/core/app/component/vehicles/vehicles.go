// Package vehicles is the vehicles component (kind "vehicles", issue #240;
// ADR-0025 §4, ADR-0037 §3): the vehicles the participants of an event
// come with, one per person, and, with the component's `seats` parameter,
// the free seats an owner offers and the requests of participants to ride
// along. It replaces the Faz 1 convoy vehicles for every type that has the
// component. There is no cost or money field.
//
// A plate is personal data: it is written only when the component's
// `plate` parameter is on, shown only to the event's participants, the
// organizer and the owner, cleared 30 days after the event's end (package
// jobs, ClearPlates) and never reaches a log, an audit record or an
// outbox event.
//
// Who may do what (the framework's actions, package component):
//
//	read              every active member who may see the event's components
//	set own vehicle   a participant (going, not muted) until the event ends
//	remove a vehicle  its owner (muted too); managers any
//	ask for a seat    a participant without a vehicle, until the event ends
//	approve a seat    the vehicle's owner only
//	release a seat    the requester (muted too), the vehicle's owner, managers
package vehicles

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompVehicles

// The component's parameters in a type definition.
const (
	ParamPlate = "plate"
	ParamSeats = "seats"
)

// Limits; the same CHECKs are on event_vehicles (00035).
const (
	MakeMax      = 40
	ModelMax     = 40
	ColorMax     = 30
	ModelYearMin = 1900
	// MaxSeats is the most free seats a vehicle offers (ADR-0037 §3).
	MaxSeats = 8
	// plateInputMax bounds a plate before it is cleaned.
	plateInputMax = 64
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// statusApproved is the stored event_vehicle_seat_requests.status of a
// request the owner approved; a waiting one is "pending".
const statusApproved = "approved"

// Audit actions of the component's changes. The records name the component
// and the vehicle only, never a plate.
const (
	AuditSet           = "event.vehicle_set"
	AuditRemoved       = "event.vehicle_removed"
	AuditSeatRequested = "event.vehicle_seat_requested"
	AuditSeatApproved  = "event.vehicle_seat_approved"
	AuditSeatReleased  = "event.vehicle_seat_released"
)

// platePattern is the event_vehicles_plate CHECK; single spaces only
// (cleanPlate collapses runs).
var platePattern = regexp.MustCompile(`^[0-9A-Z][0-9A-Z ]{0,14}[0-9A-Z]$`)

// Input is the caller's vehicle to set.
type Input struct {
	Make      string
	Model     string
	ModelYear int32
	Color     string
	// Plate is optional; allowed only with the component's plate parameter.
	Plate string
	// Seats is the free seats offered, 0 to MaxSeats; not 0 only with the
	// component's seats parameter.
	Seats int32
}

// Vehicle is one participant's vehicle, as the caller sees it.
type Vehicle struct {
	ID        id.ID
	UserID    id.ID
	Make      string
	Model     string
	ModelYear int32
	Color     string
	// Plate is empty for callers who are not participants.
	Plate string
	Seats int32
	// Riders are the users whose request the owner approved, in request
	// order.
	Riders []id.ID
	// Pending are the users whose request waits: all of them for the
	// owner, only the caller's own for anyone else.
	Pending []id.ID
}

// Vehicles is the vehicles of one component of an event, as the caller
// sees them.
type Vehicles struct {
	// Items are in the order the vehicles were added.
	Items []Vehicle
	// PlateEnabled and SeatsEnabled are the component's parameters for
	// this event.
	PlateEnabled bool
	SeatsEnabled bool
	// CallerCanParticipate reports whether the caller may set a vehicle or
	// ask for a seat now.
	CallerCanParticipate bool
	// CallerCanManage reports whether the caller may remove other people's
	// vehicles and seat requests now.
	CallerCanManage bool
}

// Service is the vehicles component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// clean checks and normalizes in apart from what depends on the event (the
// upper bound of the model year and the component's parameters).
func clean(in Input) (Input, error) {
	var (
		out Input
		err error
	)
	if out.Make, err = cleanText(in.Make, "make", 1, MakeMax); err != nil {
		return Input{}, err
	}
	if out.Model, err = cleanText(in.Model, "model", 0, ModelMax); err != nil {
		return Input{}, err
	}
	if out.Color, err = cleanText(in.Color, "color", 0, ColorMax); err != nil {
		return Input{}, err
	}
	if in.ModelYear != 0 && in.ModelYear < ModelYearMin {
		return Input{}, apperr.Validation("model_year")
	}
	out.ModelYear = in.ModelYear
	if out.Plate, err = cleanPlate(in.Plate); err != nil {
		return Input{}, err
	}
	if in.Seats < 0 || in.Seats > MaxSeats {
		return Input{}, apperr.Validation("seats")
	}
	out.Seats = in.Seats
	return out, nil
}

// cleanText trims s and refuses invalid UTF-8, control characters and a
// length outside lo..hi characters.
func cleanText(s, field string, lo, hi int) (string, error) {
	if len(s) > (hi+64)*maxRuneBytes {
		return "", apperr.Validation(field)
	}
	t := strings.TrimSpace(s)
	if !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", apperr.Validation(field)
	}
	if n := utf8.RuneCountInString(t); n < lo || n > hi {
		return "", apperr.Validation(field)
	}
	return t, nil
}

// cleanPlate upper-cases the plate and collapses runs of spaces; the
// result has only A–Z, digits and single spaces. Empty means no plate.
func cleanPlate(s string) (string, error) {
	if len(s) > plateInputMax {
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

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func int4(n int32) pgtype.Int4 { return pgtype.Int4{Int32: n, Valid: n != 0} }

// open reports whether the event, the tayfa and the write window allow
// changes: the checks component.Service.Change makes.
func open(sc *component.Scope) bool {
	return !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// load reads the component's vehicles as the caller sees them.
func load(ctx context.Context, sc *component.Scope) (Vehicles, error) {
	going, err := sc.Going(ctx)
	if err != nil {
		return Vehicles{}, err
	}
	out := Vehicles{
		Items: []Vehicle{}, PlateEnabled: sc.ParamBool(ParamPlate), SeatsEnabled: sc.ParamBool(ParamSeats),
		CallerCanParticipate: going && open(sc) && !sc.Ended() && sc.Authorize(authz.EventComponentParticipate) == nil,
		CallerCanManage:      open(sc) && sc.IsManager(),
	}
	rows, err := sc.Q().ListEventVehicles(ctx, store.ListEventVehiclesParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	if err != nil {
		return Vehicles{}, fmt.Errorf("araçlar: %w", err)
	}
	reqs, err := sc.Q().ListEventSeatRequests(ctx, store.ListEventSeatRequestsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	if err != nil {
		return Vehicles{}, fmt.Errorf("koltuk istekleri: %w", err)
	}
	// Plates are personal data: only the event's participants and the
	// organizer see them, and everyone sees their own.
	seesPlates := going || (sc.Organizer != (id.ID{}) && sc.Organizer == sc.User)
	at := make(map[id.ID]int, len(rows))
	for _, r := range rows {
		v := Vehicle{
			ID: id.ID(r.ID.Bytes), UserID: id.ID(r.UserID.Bytes), Make: r.Make, Model: r.Model.String,
			ModelYear: r.ModelYear.Int32, Color: r.Color.String, Seats: r.Seats,
			Riders: []id.ID{}, Pending: []id.ID{},
		}
		if seesPlates || v.UserID == sc.User {
			v.Plate = r.Plate.String
		}
		at[v.ID] = len(out.Items)
		out.Items = append(out.Items, v)
	}
	for _, r := range reqs {
		i, ok := at[id.ID(r.EventVehicleID.Bytes)]
		if !ok {
			continue
		}
		v, rider := &out.Items[i], id.ID(r.UserID.Bytes)
		switch {
		case r.Status == statusApproved:
			v.Riders = append(v.Riders, rider)
		case v.UserID == sc.User || rider == sc.User:
			// Who waits for an answer is between the requester and the
			// vehicle's owner.
			v.Pending = append(v.Pending, rider)
		}
	}
	return out, nil
}

// List returns the component's vehicles. Refusals are those of
// component.Service.View.
func (s *Service) List(ctx context.Context, ref component.Ref) (Vehicles, error) {
	var out Vehicles
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

// participant refuses a caller who may not add to the component now: the
// event has ended (apperr.ErrEventEnded) or the caller did not answer
// going (apperr.ErrEventNotGoing).
func participant(ctx context.Context, sc *component.Scope) error {
	if sc.Ended() {
		return apperr.ErrEventEnded
	}
	going, err := sc.Going(ctx)
	if err != nil {
		return err
	}
	if !going {
		return apperr.ErrEventNotGoing
	}
	return nil
}

// vehicle returns the owner and the seats of a vehicle of the component;
// apperr.ErrEventItemNotFound without one.
func vehicle(ctx context.Context, sc *component.Scope, v id.ID) (owner id.ID, seats int32, err error) {
	r, err := sc.Q().GetEventVehicle(ctx, store.GetEventVehicleParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: v.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return id.ID{}, 0, apperr.ErrEventItemNotFound
	}
	if err != nil {
		return id.ID{}, 0, fmt.Errorf("araç: %w", err)
	}
	return id.ID(r.UserID.Bytes), r.Seats, nil
}

// ownVehicle returns the caller's vehicle in the component; ok is false
// without one.
func ownVehicle(ctx context.Context, sc *component.Scope) (v id.ID, ok bool, err error) {
	r, err := sc.Q().GetUserEventVehicle(ctx, store.GetUserEventVehicleParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return id.ID{}, false, nil
	}
	if err != nil {
		return id.ID{}, false, fmt.Errorf("araç: %w", err)
	}
	return id.ID(r.Bytes), true, nil
}

// approved counts the approved riders of a vehicle.
func approved(ctx context.Context, sc *component.Scope, v id.ID) (int64, error) {
	n, err := sc.Q().CountApprovedSeats(ctx, store.CountApprovedSeatsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, EventVehicleID: v.PG()})
	if err != nil {
		return 0, fmt.Errorf("onaylı koltuklar: %w", err)
	}
	return n, nil
}

// request returns user's seat request in the component; ok is false
// without one.
func request(ctx context.Context, sc *component.Scope, user id.ID) (v id.ID, status string, ok bool, err error) {
	r, err := sc.Q().GetEventSeatRequest(ctx, store.GetEventSeatRequestParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return id.ID{}, "", false, nil
	}
	if err != nil {
		return id.ID{}, "", false, fmt.Errorf("koltuk isteği: %w", err)
	}
	return id.ID(r.EventVehicleID.Bytes), r.Status, true, nil
}

// release deletes user's seat request in the component; it reports
// whether there was one.
func release(ctx context.Context, sc *component.Scope, user id.ID) (bool, error) {
	n, err := sc.Q().DeleteEventSeatRequest(ctx, store.DeleteEventSeatRequestParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: user.PG()})
	if err != nil {
		return false, fmt.Errorf("koltuk isteğini silme: %w", err)
	}
	return n > 0, nil
}

// done marks the change, writes its audit record (the vehicle's ID only)
// and reads the result for the caller.
func done(ctx context.Context, sc *component.Scope, action string, v id.ID) (Vehicles, error) {
	sc.Changed()
	if err := sc.Audit(ctx, action, map[string]any{"vehicle_id": v.String()}); err != nil {
		return Vehicles{}, err
	}
	return load(ctx, sc)
}

// Set sets the caller's vehicle, replacing a previous one (its ID stays).
// The caller
// must have answered going, and the event must not have ended. Refused: a
// bad value (validation error on its field; a plate or seats the
// component's parameters do not allow), fewer seats than the riders
// already approved (apperr.ErrVehicleSeatsTaken) and the refusals of
// component.Service.Change. A driver rides in their own vehicle: a seat
// request of the caller's to another vehicle is removed.
func (s *Service) Set(ctx context.Context, ref component.Ref, in Input) (Vehicles, error) {
	in, err := clean(in)
	if err != nil {
		return Vehicles{}, err
	}
	var out Vehicles
	err = s.fw.Change(ctx, ref, Kind, component.ByParticipant(), func(sc *component.Scope) error {
		if err := participant(ctx, sc); err != nil {
			return err
		}
		if int(in.ModelYear) > sc.Now.Year()+1 {
			return apperr.Validation("model_year")
		}
		if in.Plate != "" && !sc.ParamBool(ParamPlate) {
			return apperr.Validation("plate")
		}
		if in.Seats != 0 && !sc.ParamBool(ParamSeats) {
			return apperr.Validation("seats")
		}
		prev, has, err := ownVehicle(ctx, sc)
		if err != nil {
			return err
		}
		if has {
			n, err := approved(ctx, sc, prev)
			if err != nil {
				return err
			}
			if int64(in.Seats) < n {
				return apperr.ErrVehicleSeatsTaken
			}
		}
		if !has {
			if _, err := release(ctx, sc, sc.User); err != nil {
				return err
			}
		}
		r, err := sc.Q().SetEventVehicle(ctx, store.SetEventVehicleParams{
			ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
			Make: in.Make, Model: text(in.Model), ModelYear: int4(in.ModelYear), Color: text(in.Color),
			Plate: text(in.Plate), Seats: in.Seats, UpdatedAt: pgtype.Timestamptz{Time: sc.Now, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("aracı yazma: %w", err)
		}
		out, err = done(ctx, sc, AuditSet, id.ID(r.Bytes))
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

// Remove removes a vehicle with the seat requests made to it: one's own
// (a muted member's too: it only removes personal data), or anyone's for
// the event's managers. Refused: an unknown vehicle
// (apperr.ErrEventItemNotFound), someone else's vehicle for a caller who
// is not a manager, and the refusals of component.Service.Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, v id.ID) (Vehicles, error) {
	var out Vehicles
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventComponentView), func(sc *component.Scope) error {
		owner, _, err := vehicle(ctx, sc, v)
		if err != nil {
			return err
		}
		if owner != sc.User {
			if err := sc.AuthorizeManager(); err != nil {
				return err
			}
		}
		n, err := sc.Q().DeleteEventVehicle(ctx, store.DeleteEventVehicleParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: v.PG()})
		if err != nil {
			return fmt.Errorf("aracı silme: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditRemoved, v)
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

// RequestSeat asks for a seat in vehicle v for the caller, who must have
// answered going and has no vehicle of their own in the component. A
// person has one request in a component: a request to another vehicle
// replaces the previous one, an approved seat included; the same request
// again changes nothing. Refused: an unknown vehicle
// (apperr.ErrEventItemNotFound), a caller with a vehicle
// (apperr.ErrSeatRequestDriver), a vehicle without a free seat
// (apperr.ErrVehicleFull; always without the component's seats parameter)
// and the refusals of component.Service.Change.
func (s *Service) RequestSeat(ctx context.Context, ref component.Ref, v id.ID) (Vehicles, error) {
	var out Vehicles
	err := s.fw.Change(ctx, ref, Kind, component.ByParticipant(), func(sc *component.Scope) error {
		if err := participant(ctx, sc); err != nil {
			return err
		}
		_, seats, err := vehicle(ctx, sc, v)
		if err != nil {
			return err
		}
		// The caller's own vehicle is v or another one: either way a
		// driver does not ride along.
		_, drives, err := ownVehicle(ctx, sc)
		if err != nil {
			return err
		}
		if drives {
			return apperr.ErrSeatRequestDriver
		}
		cur, _, has, err := request(ctx, sc, sc.User)
		if err != nil {
			return err
		}
		if has && cur == v {
			out, err = load(ctx, sc)
			return err
		}
		n, err := approved(ctx, sc, v)
		if err != nil {
			return err
		}
		if n >= int64(seats) {
			return apperr.ErrVehicleFull
		}
		if has {
			if _, err := release(ctx, sc, sc.User); err != nil {
				return err
			}
		}
		if err := sc.Q().CreateEventSeatRequest(ctx, store.CreateEventSeatRequestParams{
			ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
			EventVehicleID: v.PG(), CreatedAt: pgtype.Timestamptz{Time: sc.Now, Valid: true},
		}); err != nil {
			return fmt.Errorf("koltuk isteği: %w", err)
		}
		out, err = done(ctx, sc, AuditSeatRequested, v)
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

// ApproveSeat approves rider's waiting request to the caller's own vehicle
// v; only the vehicle's owner may. Approving an approved request changes
// nothing. Refused: an unknown vehicle or no request of rider to it
// (apperr.ErrEventItemNotFound), a caller who is not the owner
// (apperr.ErrPermissionDenied), an ended event (apperr.ErrEventEnded), no
// free seat (apperr.ErrVehicleFull) and the refusals of
// component.Service.Change.
func (s *Service) ApproveSeat(ctx context.Context, ref component.Ref, v, rider id.ID) (Vehicles, error) {
	var out Vehicles
	err := s.fw.Change(ctx, ref, Kind, component.ByParticipant(), func(sc *component.Scope) error {
		owner, seats, err := vehicle(ctx, sc, v)
		if err != nil {
			return err
		}
		// Before the request is looked up: whether someone asked is not
		// told to anyone but the owner.
		if owner != sc.User {
			return apperr.ErrPermissionDenied
		}
		if sc.Ended() {
			return apperr.ErrEventEnded
		}
		cur, status, has, err := request(ctx, sc, rider)
		if err != nil {
			return err
		}
		if !has || cur != v {
			return apperr.ErrEventItemNotFound
		}
		if status == statusApproved {
			out, err = load(ctx, sc)
			return err
		}
		n, err := approved(ctx, sc, v)
		if err != nil {
			return err
		}
		if n >= int64(seats) {
			return apperr.ErrVehicleFull
		}
		changed, err := sc.Q().ApproveEventSeatRequest(ctx, store.ApproveEventSeatRequestParams{
			DecidedAt: pgtype.Timestamptz{Time: sc.Now, Valid: true}, EventID: sc.Event.PG(), ComponentKey: sc.Key,
			UserID: rider.PG(), EventVehicleID: v.PG(),
		})
		if err != nil {
			return fmt.Errorf("koltuk onayı: %w", err)
		}
		if changed == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditSeatApproved, v)
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

// ReleaseSeat removes rider's seat request to vehicle v, waiting or
// approved: the requester withdraws their own (a muted member too), the
// vehicle's owner declines it or removes a rider, a manager removes any.
// Refused: an unknown vehicle or no request of rider to it
// (apperr.ErrEventItemNotFound), anyone else (apperr.ErrPermissionDenied)
// and the refusals of component.Service.Change.
func (s *Service) ReleaseSeat(ctx context.Context, ref component.Ref, v, rider id.ID) (Vehicles, error) {
	var out Vehicles
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventComponentView), func(sc *component.Scope) error {
		owner, _, err := vehicle(ctx, sc, v)
		if err != nil {
			return err
		}
		// Before the request is looked up: whether someone asked is not
		// told to a caller who has no part in it.
		if rider != sc.User && owner != sc.User {
			if err := sc.AuthorizeManager(); err != nil {
				return err
			}
		}
		cur, _, has, err := request(ctx, sc, rider)
		if err != nil {
			return err
		}
		if !has || cur != v {
			return apperr.ErrEventItemNotFound
		}
		if _, err := release(ctx, sc, rider); err != nil {
			return err
		}
		out, err = done(ctx, sc, AuditSeatReleased, v)
		return err
	})
	if err != nil {
		return Vehicles{}, err
	}
	return out, nil
}

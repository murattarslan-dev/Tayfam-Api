package event

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// campItemsMax bounds a camp's item list.
const campItemsMax = 200

// Audit actions of list changes; their outbox event is event.updated with
// the list's field name.
const (
	auditCampItemAdded   = "event.camp_item_added"
	auditCampItemRemoved = "event.camp_item_removed"
	auditVehicleSet      = "event.vehicle_set"
	auditVehicleRemoved  = "event.vehicle_removed"

	fieldCampItems = "camp_items"
	fieldVehicles  = "convoy_vehicles"
)

// AddCampItem adds an item to a camp's list. The organizer, moderators and
// admins manage the list. assigned may be zero; otherwise it must be an
// active member of the tayfa.
func (s *Service) AddCampItem(ctx context.Context, user, tayfa, event id.ID, name string, quantity int32, assigned id.ID) (CampItem, error) {
	n, err := cleanText(name, "name", 1, itemNameMax, false)
	if err != nil {
		return CampItem{}, err
	}
	if quantity == 0 {
		quantity = 1
	}
	if quantity < 1 || quantity > itemQuantityMax {
		return CampItem{}, apperr.Validation("quantity")
	}
	item := CampItem{ID: id.New(), Name: n, Quantity: quantity, AssignedUserID: assigned}
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := s.listEvent(ctx, q, c, tayfa, event, TypeCamp)
		if err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		count, err := q.CountCampItems(ctx, event.PG())
		if err != nil {
			return fmt.Errorf("kamp malzemeleri: %w", err)
		}
		if count >= campItemsMax {
			return apperr.ErrEventLimit
		}
		if assigned != (id.ID{}) {
			m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: assigned.PG()})
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Status != "active") {
				return apperr.Validation("assigned_user_id")
			}
			if err != nil {
				return fmt.Errorf("üyelik: %w", err)
			}
		}
		if err := q.CreateCampItem(ctx, store.CreateCampItemParams{
			ID: item.ID.PG(), EventID: event.PG(), Name: n, Quantity: quantity,
			AssignedUserID: optID(assigned), CreatedBy: user.PG(),
		}); err != nil {
			return fmt.Errorf("kamp malzemesi: %w", err)
		}
		return listChanged(ctx, q, c, b, event, auditCampItemAdded, fieldCampItems, "item_id", item.ID)
	})
	if err != nil {
		return CampItem{}, err
	}
	return item, nil
}

// RemoveCampItem removes an item from a camp's list.
func (s *Service) RemoveCampItem(ctx context.Context, user, tayfa, event, item id.ID) error {
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := s.listEvent(ctx, q, c, tayfa, event, TypeCamp)
		if err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		n, err := q.DeleteCampItem(ctx, store.DeleteCampItemParams{EventID: event.PG(), ID: item.PG()})
		if err != nil {
			return fmt.Errorf("kamp malzemesi: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		return listChanged(ctx, q, c, b, event, auditCampItemRemoved, fieldCampItems, "item_id", item)
	})
}

// listEvent locks a camp or convoy event for a list change by its managers:
// the organizer, moderators and admins.
func (s *Service) listEvent(ctx context.Context, q *store.Queries, c caller, tayfa, event id.ID, typ string) (store.GetEventRow, error) {
	ev, err := lockEvent(ctx, q, tayfa, event)
	if err != nil {
		return store.GetEventRow{}, err
	}
	if err := c.manage(ev, authz.EventEditOwn, authz.EventEditAny); err != nil {
		return store.GetEventRow{}, err
	}
	if ev.Type != typ {
		return store.GetEventRow{}, apperr.ErrEventTypeMismatch
	}
	return ev, nil
}

// SetVehicle sets the caller's vehicle on a convoy, replacing a previous
// one. The caller must have answered going.
func (s *Service) SetVehicle(ctx context.Context, user, tayfa, event id.ID, in VehicleInput) (Vehicle, error) {
	v, err := cleanVehicle(in, s.now())
	if err != nil {
		return Vehicle{}, err
	}
	out := Vehicle{UserID: user, Make: v.Make, Model: v.Model, ModelYear: v.ModelYear, Color: v.Color, Plate: v.Plate}
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventRSVP); err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeConvoy {
			return apperr.ErrEventTypeMismatch
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		r, err := q.GetRsvp(ctx, store.GetRsvpParams{EventID: event.PG(), UserID: user.PG()})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.Status != RSVPGoing) {
			return apperr.ErrEventNotGoing
		}
		if err != nil {
			return fmt.Errorf("katılım: %w", err)
		}
		vid, err := q.SetConvoyVehicle(ctx, store.SetConvoyVehicleParams{
			ID: id.New().PG(), EventID: event.PG(), UserID: user.PG(), Make: v.Make, Model: text(v.Model),
			ModelYear: int4(v.ModelYear), Color: text(v.Color), Plate: text(v.Plate),
		})
		if err != nil {
			return fmt.Errorf("araç: %w", err)
		}
		out.ID = id.ID(vid.Bytes)
		// The plate never reaches the audit record or the event.
		return listChanged(ctx, q, c, b, event, auditVehicleSet, fieldVehicles, "vehicle_id", out.ID)
	})
	if err != nil {
		return Vehicle{}, err
	}
	return out, nil
}

// RemoveVehicle removes a vehicle from a convoy: one's own (an answerer's
// right), or anyone's for the event's managers. Works on cancelled and
// ended events too: removing a vehicle only removes personal data.
func (s *Service) RemoveVehicle(ctx context.Context, user, tayfa, event, vehicle id.ID) error {
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeConvoy {
			return apperr.ErrEventTypeMismatch
		}
		owner, err := q.GetConvoyVehicleOwner(ctx, store.GetConvoyVehicleOwnerParams{EventID: event.PG(), ID: vehicle.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("araç: %w", err)
		}
		if id.ID(owner.Bytes) == user {
			err = c.authorize(authz.EventRSVP)
		} else {
			err = c.manage(ev, authz.EventEditOwn, authz.EventEditAny)
		}
		if err != nil {
			return err
		}
		n, err := q.DeleteConvoyVehicle(ctx, store.DeleteConvoyVehicleParams{EventID: event.PG(), ID: vehicle.PG()})
		if err != nil {
			return fmt.Errorf("araç: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		return listChanged(ctx, q, c, b, event, auditVehicleRemoved, fieldVehicles, "vehicle_id", vehicle)
	})
}

// listChanged records a list change: an audit row with the entry's ID and
// an event.updated event naming the list.
func listChanged(ctx context.Context, q *store.Queries, c caller, b *batch, event id.ID, action, field, key string, entry id.ID) error {
	if err := audit(ctx, q, c, action, event, map[string]any{key: entry.String()}); err != nil {
		return err
	}
	b.add(EventUpdated, event, map[string]any{"event_id": event.String(), "fields": []string{field}})
	return nil
}

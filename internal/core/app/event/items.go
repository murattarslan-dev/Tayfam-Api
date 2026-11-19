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
	auditCampItemAssign  = "event.camp_item_assigned"

	fieldCampItems = "camp_items"
)

// AddCampItem adds an item to a camp's list. The organizer, moderators and
// admins manage the list. assigned may be zero; otherwise it must be an
// active member of the tayfa who answered going (issue #198).
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
			if err := goingMember(ctx, q, tayfa, event, assigned); err != nil {
				return err
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

// AssignCampItem sets or clears who brings a camp item (issue #198);
// assigned zero clears it. The organizer, moderators and admins assign any
// participant who answered going, or clear any assignment. Other members
// who answered going may only take a free item for themselves and drop
// their own; an item someone else took is ErrEventItemTaken. Setting the
// value it already has succeeds without a change. The event and the item
// are locked, so of two people taking one item at once the second sees the
// first's assignment.
func (s *Service) AssignCampItem(ctx context.Context, user, tayfa, event, item, assigned id.ID) (CampItem, error) {
	var out CampItem
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		manager := c.manage(ev, authz.EventEditOwn, authz.EventEditAny) == nil
		if !manager {
			// Default no: a member needs the RSVP right (a mute takes it).
			if err := c.authorize(authz.EventRSVP); err != nil {
				return err
			}
		}
		if ev.Type != TypeCamp {
			return apperr.ErrEventTypeMismatch
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		row, err := q.GetCampItemForUpdate(ctx, store.GetCampItemForUpdateParams{EventID: event.PG(), ID: item.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("kamp malzemesi: %w", err)
		}
		current := id.ID{}
		if row.AssignedUserID.Valid {
			current = id.ID(row.AssignedUserID.Bytes)
		}
		out = CampItem{ID: item, Name: row.Name, Quantity: row.Quantity, AssignedUserID: assigned}
		if !manager {
			if err := memberAssign(ctx, q, event, user, current, assigned); err != nil {
				return err
			}
		} else if assigned != (id.ID{}) && assigned != current {
			if err := goingMember(ctx, q, tayfa, event, assigned); err != nil {
				return err
			}
		}
		if current == assigned {
			return nil
		}
		if err := q.SetCampItemAssignee(ctx, store.SetCampItemAssigneeParams{
			EventID: event.PG(), ID: item.PG(), AssignedUserID: optID(assigned),
		}); err != nil {
			return fmt.Errorf("kamp malzemesi: %w", err)
		}
		return listChanged(ctx, q, c, b, event, auditCampItemAssign, fieldCampItems, "item_id", item)
	})
	if err != nil {
		return CampItem{}, err
	}
	return out, nil
}

// memberAssign checks a change by a member who does not manage the event:
// only for themselves, only with a going answer, only on a free item or
// their own.
func memberAssign(ctx context.Context, q *store.Queries, event, user, current, assigned id.ID) error {
	if assigned != (id.ID{}) && assigned != user {
		return apperr.ErrPermissionDenied
	}
	if err := going(ctx, q, event, user); err != nil {
		return err
	}
	if current != (id.ID{}) && current != user {
		// Taken by someone else: neither taking it nor dropping it is ours.
		return apperr.ErrEventItemTaken
	}
	return nil
}

// going checks that user answered going on event.
func going(ctx context.Context, q *store.Queries, event, user id.ID) error {
	r, err := q.GetRsvp(ctx, store.GetRsvpParams{EventID: event.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.Status != RSVPGoing) {
		return apperr.ErrEventNotGoing
	}
	if err != nil {
		return fmt.Errorf("katılım: %w", err)
	}
	return nil
}

// goingMember checks that an assignee is an active member of the tayfa who
// answered going. Both cases give ErrEventNotGoing, so the answer says no
// more about the person than the attendee list does.
func goingMember(ctx context.Context, q *store.Queries, tayfa, event, user id.ID) error {
	m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Status != "active") {
		return apperr.ErrEventNotGoing
	}
	if err != nil {
		return fmt.Errorf("üyelik: %w", err)
	}
	return going(ctx, q, event, user)
}

// listEvent locks a camp event for a list change by its managers:
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

// listChanged records a list change: an audit row with the entry's ID and
// an event.updated event naming the list.
func listChanged(ctx context.Context, q *store.Queries, c caller, b *batch, event id.ID, action, field, key string, entry id.ID) error {
	if err := audit(ctx, q, c, action, event, map[string]any{key: entry.String()}); err != nil {
		return err
	}
	b.add(EventUpdated, event, map[string]any{"event_id": event.String(), "fields": []string{field}})
	return nil
}

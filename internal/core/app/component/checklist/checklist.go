// Package checklist is the list component (kind "checklist", issue #241;
// ADR-0025 §4, §5): who brings, does or takes what. An event may have more
// than one list (the definition's key: gear, duty …); each is a list of
// items with a name, a quantity (with the component's `quantity`
// parameter) and, with the `assignment` parameter, the member who took it.
// It replaces the Faz 1 camp item list for every type that has the
// component.
//
// Who may do what (the framework's actions, package component):
//
//	read               every active member who may see the event's components
//	add, remove        managers (the organizer, moderators and admins)
//	assign anyone      managers: any active member who answered going, or nobody
//	take, drop         a participant (going, not muted): a free item for
//	                   themselves, and their own item back
//
// Changes are open until the event ends (apperr.ErrEventEnded), as the
// Faz 1 list was, within the framework's write window. An item's name is
// user content: it never reaches a log, an audit record or an outbox event.
package checklist

import (
	"context"
	"errors"
	"fmt"
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
const Kind = definition.CompChecklist

// The component's parameters in a type definition.
const (
	ParamLabel      = "label"
	ParamAssignment = "assignment"
	ParamQuantity   = "quantity"
)

// DefaultLabel is the label of a list whose definition sets none.
const DefaultLabel = "item"

// Limits; the same CHECKs are on event_checklist_items (00036).
const (
	NameMax     = 60
	QuantityMax = 999
	// MaxItems bounds one list (the Faz 1 camp list's bound).
	MaxItems = 200
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Audit actions of the component's changes. The records name the item only,
// never its name.
const (
	AuditAdded    = "event.checklist_item_added"
	AuditRemoved  = "event.checklist_item_removed"
	AuditAssigned = "event.checklist_item_assigned"
)

// Input is an item to add.
type Input struct {
	Name string
	// Quantity is 1 to QuantityMax; 0 means 1. Above 1 only with the
	// component's quantity parameter.
	Quantity int32
	// AssignedUserID is zero for nobody; otherwise an active member who
	// answered going, and only with the component's assignment parameter.
	AssignedUserID id.ID
}

// Item is one entry of a list.
type Item struct {
	ID       id.ID
	Name     string
	Quantity int32
	// AssignedUserID is zero when nobody took the item.
	AssignedUserID id.ID
}

// List is one list component of an event, as the caller sees it.
type List struct {
	// Items are in the order they were added.
	Items []Item
	// Label is the component's label parameter (gear, task, section,
	// duty, item); DefaultLabel when the definition sets none.
	Label string
	// AssignmentEnabled and QuantityEnabled are the component's parameters
	// for this event.
	AssignmentEnabled bool
	QuantityEnabled   bool
	// CallerCanManage reports whether the caller may add, remove and
	// assign items now.
	CallerCanManage bool
	// CallerCanTake reports whether the caller may take a free item for
	// themselves and drop their own now.
	CallerCanTake bool
}

// Service is the list component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// cleanName trims s and refuses invalid UTF-8, control characters and a
// length outside 1..NameMax characters.
func cleanName(s string) (string, error) {
	if len(s) > (NameMax+64)*maxRuneBytes {
		return "", apperr.Validation("name")
	}
	t := strings.TrimSpace(s)
	if !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", apperr.Validation("name")
	}
	if n := utf8.RuneCountInString(t); n < 1 || n > NameMax {
		return "", apperr.Validation("name")
	}
	return t, nil
}

func optID(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}

func ts(sc *component.Scope) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: sc.Now, Valid: true}
}

// open reports whether the event, the tayfa and the write window allow
// changes now: the checks component.Service.Change makes, and the list's
// own end.
func open(sc *component.Scope) bool {
	return !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow()) && !sc.Ended()
}

// load reads the list as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (List, error) {
	out := List{
		Items: []Item{}, Label: sc.ParamString(ParamLabel),
		AssignmentEnabled: sc.ParamBool(ParamAssignment), QuantityEnabled: sc.ParamBool(ParamQuantity),
		CallerCanManage: open(sc) && sc.Enabled && sc.AuthorizeManager() == nil,
	}
	if out.Label == "" {
		out.Label = DefaultLabel
	}
	if out.AssignmentEnabled && open(sc) && sc.Enabled && sc.Authorize(authz.EventComponentParticipate) == nil {
		going, err := sc.Going(ctx)
		if err != nil {
			return List{}, err
		}
		out.CallerCanTake = going
	}
	rows, err := sc.Q().ListChecklistItems(ctx, store.ListChecklistItemsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	if err != nil {
		return List{}, fmt.Errorf("liste: %w", err)
	}
	for _, r := range rows {
		it := Item{ID: id.ID(r.ID.Bytes), Name: r.Name, Quantity: r.Quantity}
		if r.AssignedUserID.Valid {
			it.AssignedUserID = id.ID(r.AssignedUserID.Bytes)
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// List returns the list. Refusals are those of component.Service.View.
func (s *Service) List(ctx context.Context, ref component.Ref) (List, error) {
	var out List
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// goingMember checks that an assignee is an active member of the tayfa who
// answered going. Both cases give apperr.ErrEventNotGoing, so the answer
// says no more about the person than the attendee list does.
func goingMember(ctx context.Context, sc *component.Scope, user id.ID) error {
	ok, err := sc.ActiveMember(ctx, user)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrEventNotGoing
	}
	going, _, err := sc.Participation(ctx, user)
	if err != nil {
		return err
	}
	if !going {
		return apperr.ErrEventNotGoing
	}
	return nil
}

// done marks the change, writes its audit record (the item's ID only) and
// reads the result for the caller.
func done(ctx context.Context, sc *component.Scope, action string, item id.ID) (List, error) {
	sc.Changed()
	if err := sc.Audit(ctx, action, map[string]any{"item_id": item.String()}); err != nil {
		return List{}, err
	}
	return load(ctx, sc)
}

// Add adds an item to the list; managers only. Refused: a bad value
// (validation error on its field; a quantity above 1 or an assignee the
// component's parameters do not allow), an assignee who is not an active
// member who answered going (apperr.ErrEventNotGoing), a full list
// (apperr.ErrEventLimit), an ended event (apperr.ErrEventEnded) and the
// refusals of component.Service.Change.
func (s *Service) Add(ctx context.Context, ref component.Ref, in Input) (List, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return List{}, err
	}
	quantity := in.Quantity
	if quantity == 0 {
		quantity = 1
	}
	if quantity < 1 || quantity > QuantityMax {
		return List{}, apperr.Validation("quantity")
	}
	var out List
	err = s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if quantity != 1 && !sc.ParamBool(ParamQuantity) {
			return apperr.Validation("quantity")
		}
		if in.AssignedUserID != (id.ID{}) && !sc.ParamBool(ParamAssignment) {
			return apperr.Validation("assigned_user_id")
		}
		if sc.Ended() {
			return apperr.ErrEventEnded
		}
		n, err := sc.Q().CountChecklistItems(ctx, store.CountChecklistItemsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("liste: %w", err)
		}
		if n >= MaxItems {
			return apperr.ErrEventLimit
		}
		if in.AssignedUserID != (id.ID{}) {
			if err := goingMember(ctx, sc, in.AssignedUserID); err != nil {
				return err
			}
		}
		item := id.New()
		if err := sc.Q().CreateChecklistItem(ctx, store.CreateChecklistItemParams{
			ID: item.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Name: name, Quantity: quantity,
			AssignedUserID: optID(in.AssignedUserID), CreatedBy: sc.User.PG(), CreatedAt: ts(sc),
		}); err != nil {
			return fmt.Errorf("liste maddesi: %w", err)
		}
		out, err = done(ctx, sc, AuditAdded, item)
		return err
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// Remove removes an item; managers only. Refused: an unknown item
// (apperr.ErrEventItemNotFound), an ended event (apperr.ErrEventEnded)
// and the refusals of component.Service.Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, item id.ID) (List, error) {
	var out List
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if sc.Ended() {
			return apperr.ErrEventEnded
		}
		n, err := sc.Q().DeleteChecklistItem(ctx, store.DeleteChecklistItemParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: item.PG()})
		if err != nil {
			return fmt.Errorf("liste maddesini silme: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditRemoved, item)
		return err
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// Assign sets or clears who took an item; assigned zero clears it. It
// needs the component's assignment parameter (otherwise a validation
// error on assigned_user_id). Managers assign any active member who
// answered going, or clear any assignment. Other members who answered
// going (not muted) may only take a free item for themselves and drop
// their own; an item someone else took is apperr.ErrEventItemTaken.
// Setting the value it already has changes nothing. The event is locked
// (component.Service.Change), so of two people taking one item at once the
// second sees the first's assignment. Refused also: an unknown item
// (apperr.ErrEventItemNotFound), an assignee who is not going or a caller
// who is not (apperr.ErrEventNotGoing), an ended event
// (apperr.ErrEventEnded) and the refusals of component.Service.Change.
func (s *Service) Assign(ctx context.Context, ref component.Ref, item, assigned id.ID) (List, error) {
	var out List
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventComponentView), func(sc *component.Scope) error {
		manager := sc.AuthorizeManager() == nil
		if !manager {
			// Default no: a member needs the participant's right (a mute
			// takes it).
			if err := sc.Authorize(authz.EventComponentParticipate); err != nil {
				return err
			}
		}
		if !sc.ParamBool(ParamAssignment) {
			return apperr.Validation("assigned_user_id")
		}
		if sc.Ended() {
			return apperr.ErrEventEnded
		}
		cur, err := sc.Q().GetChecklistItem(ctx, store.GetChecklistItemParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: item.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("liste maddesi: %w", err)
		}
		current := id.ID{}
		if cur.Valid {
			current = id.ID(cur.Bytes)
		}
		if manager {
			if assigned != (id.ID{}) && assigned != current {
				if err := goingMember(ctx, sc, assigned); err != nil {
					return err
				}
			}
		} else if err := memberAssign(ctx, sc, current, assigned); err != nil {
			return err
		}
		if current == assigned {
			out, err = load(ctx, sc)
			return err
		}
		n, err := sc.Q().SetChecklistItemAssignee(ctx, store.SetChecklistItemAssigneeParams{
			AssignedUserID: optID(assigned), UpdatedAt: ts(sc), EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: item.PG(),
		})
		if err != nil {
			return fmt.Errorf("liste maddesi: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditAssigned, item)
		return err
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// memberAssign checks a change by a member who does not manage the event:
// only for themselves, only with a going answer, only on a free item or
// their own.
func memberAssign(ctx context.Context, sc *component.Scope, current, assigned id.ID) error {
	if assigned != (id.ID{}) && assigned != sc.User {
		return apperr.ErrPermissionDenied
	}
	going, err := sc.Going(ctx)
	if err != nil {
		return err
	}
	if !going {
		return apperr.ErrEventNotGoing
	}
	if current != (id.ID{}) && current != sc.User {
		// Taken by someone else: neither taking it nor dropping it is ours.
		return apperr.ErrEventItemTaken
	}
	return nil
}

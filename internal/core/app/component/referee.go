package component

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MaxReferees bounds the referee assignments of one event.
const MaxReferees = 50

// Audit actions of referee changes. The records name the referee and the
// component only.
const (
	AuditRefereeAssigned = "event.referee_assigned"
	AuditRefereeRemoved  = "event.referee_removed"
)

// Referee is one assignment: User may enter results (ADR-0025 §5) in the
// whole event, in one component or in one scope of a component.
type Referee struct {
	ID   id.ID
	User id.ID
	// ComponentKey is "" for an assignment to the whole event.
	ComponentKey string
	// Scope is a table or match of the component; zero for the whole
	// component.
	Scope     id.ID
	CreatedAt time.Time
}

// RefereeInput is a new assignment.
type RefereeInput struct {
	User         id.ID
	ComponentKey string
	Scope        id.ID
}

// ListReferees returns the event's referee assignments, oldest first, for
// a member who may read its components.
func (s *Service) ListReferees(ctx context.Context, user, tayfa, event id.ID) ([]Referee, error) {
	b, err := s.load(ctx, s.q, user, tayfa, event, false)
	if err != nil {
		return nil, err
	}
	if err := b.authorize(authz.EventComponentView); err != nil {
		return nil, err
	}
	rows, err := s.q.ListEventReferees(ctx, event.PG())
	if err != nil {
		return nil, fmt.Errorf("hakemler: %w", err)
	}
	out := make([]Referee, 0, len(rows))
	for _, r := range rows {
		ref := Referee{ID: id.ID(r.ID.Bytes), User: id.ID(r.UserID.Bytes), ComponentKey: r.ComponentKey.String, CreatedAt: r.CreatedAt.Time}
		if r.ScopeID.Valid {
			ref.Scope = id.ID(r.ScopeID.Bytes)
		}
		out = append(out, ref)
	}
	return out, nil
}

// AssignReferee assigns an active member of the tayfa as referee
// (ADR-0025 §5: the organizer assigns; moderators as for every event).
// Assigning the same again returns the first assignment. Refused: a user
// who is not an active member (apperr.ErrMemberNotFound), a component the
// event does not have (apperr.ErrComponentNotFound) or that is switched
// off (apperr.ErrComponentDisabled), a full list (apperr.ErrEventLimit)
// and the refusals of every change (read-only tayfa, cancelled event,
// closed write window).
//
// Whether a scope exists is the component's knowledge: the framework
// stores the ID, and the component that deletes the scope removes its
// assignments (Scope.RemoveScopeReferees). An assignment to a scope nobody
// has allows nothing.
func (s *Service) AssignReferee(ctx context.Context, user, tayfa, event id.ID, in RefereeInput) (Referee, error) {
	switch {
	case in.User == (id.ID{}):
		return Referee{}, apperr.Validation("user_id")
	case in.ComponentKey != "" && !definition.ValidKey(in.ComponentKey):
		return Referee{}, apperr.Validation("component_key")
	case in.Scope != (id.ID{}) && in.ComponentKey == "":
		return Referee{}, apperr.Validation("scope_id")
	}
	var out Referee
	err := txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		b, err := s.load(ctx, q, user, tayfa, event, true)
		if err != nil {
			return err
		}
		if err := b.manages(authz.EventRefereeAssignOwn, authz.EventRefereeAssignAny); err != nil {
			return err
		}
		if err := b.writable(); err != nil {
			return err
		}
		if in.ComponentKey != "" {
			comp, ok := b.et.Parsed.Component(in.ComponentKey)
			if !ok {
				return apperr.ErrComponentNotFound
			}
			st, has, err := storedOf(ctx, q, event, comp)
			if err != nil {
				return err
			}
			if (has && !st.enabled) || (!has && !defaultEnabled(comp)) {
				return apperr.ErrComponentDisabled
			}
		}
		member, err := q.IsActiveMember(ctx, store.IsActiveMemberParams{TayfaID: tayfa.PG(), UserID: in.User.PG()})
		if err != nil {
			return fmt.Errorf("üyelik: %w", err)
		}
		if !member {
			return apperr.ErrMemberNotFound
		}
		find := store.FindEventRefereeParams{EventID: event.PG(), UserID: in.User.PG()}
		create := store.CreateEventRefereeParams{EventID: event.PG(), UserID: in.User.PG(), CreatedAt: ts(b.now)}
		if in.ComponentKey != "" {
			find.ComponentKey.String, find.ComponentKey.Valid = in.ComponentKey, true
			create.ComponentKey = find.ComponentKey
		}
		if in.Scope != (id.ID{}) {
			find.ScopeID = in.Scope.PG()
			create.ScopeID = find.ScopeID
		}
		out = Referee{User: in.User, ComponentKey: in.ComponentKey, Scope: in.Scope}
		prev, err := q.FindEventReferee(ctx, find)
		switch {
		case err == nil:
			out.ID, out.CreatedAt = id.ID(prev.ID.Bytes), prev.CreatedAt.Time
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("hakem: %w", err)
		}
		n, err := q.CountEventReferees(ctx, event.PG())
		if err != nil {
			return fmt.Errorf("hakem sayısı: %w", err)
		}
		if n >= MaxReferees {
			return apperr.ErrEventLimit
		}
		out.ID, out.CreatedAt = id.New(), b.now
		create.ID = out.ID.PG()
		// The event row is locked: no other assignment can slip in between
		// the lookup and the insert.
		if rows, err := q.CreateEventReferee(ctx, create); err != nil {
			return fmt.Errorf("hakem atama: %w", err)
		} else if rows != 1 {
			return errors.New("hakem ataması yazılamadı")
		}
		if err := txn.Audit(ctx, q, tayfa, user, AuditRefereeAssigned, txn.Target{Type: "event", ID: event},
			map[string]any{"user_id": in.User.String(), "component_key": in.ComponentKey}); err != nil {
			return err
		}
		return changed(ctx, q, tayfa, user, event, in.ComponentKey)
	})
	if err != nil {
		return Referee{}, err
	}
	return out, nil
}

// RemoveReferee removes an assignment. Removing one that is not there (any
// more) changes nothing and is not an error.
func (s *Service) RemoveReferee(ctx context.Context, user, tayfa, event, referee id.ID) error {
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		b, err := s.load(ctx, q, user, tayfa, event, true)
		if err != nil {
			return err
		}
		if err := b.manages(authz.EventRefereeAssignOwn, authz.EventRefereeAssignAny); err != nil {
			return err
		}
		if err := b.writable(); err != nil {
			return err
		}
		n, err := q.DeleteEventReferee(ctx, store.DeleteEventRefereeParams{EventID: event.PG(), ID: referee.PG()})
		if err != nil {
			return fmt.Errorf("hakem kaldırma: %w", err)
		}
		if n == 0 {
			return nil
		}
		if err := txn.Audit(ctx, q, tayfa, user, AuditRefereeRemoved, txn.Target{Type: "event", ID: event},
			map[string]any{"referee_id": referee.String()}); err != nil {
			return err
		}
		// Which component the assignment was for is not looked up: clients
		// read the referees again.
		return changed(ctx, q, tayfa, user, event, "")
	})
}

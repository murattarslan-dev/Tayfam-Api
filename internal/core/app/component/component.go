package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventChanged is the outbox event of a component change (ADR-0025 §6). Its
// payload holds the tayfa, the event and the component key only; an empty
// component key means the event's selections or referees changed.
const EventChanged = "event.component_changed"

// FreezeAfter is how long after the event's end component data may still
// change (ADR-0025 §5, the same window as the attendance correction).
const FreezeAfter = 7 * 24 * time.Hour

// Stored values of events.status and event_rsvps this package reads.
const (
	statusCancelled    = "cancelled"
	rsvpGoing          = "going"
	attendanceAttended = "attended"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time
}

// Service is the component framework. Components keep it and call View and
// Change; EventComponentService (selections, referees) is served by its
// own methods.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("component: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Log is the service's logger, for the components built on it.
func (s *Service) Log() *slog.Logger { return s.cfg.Log }

// Ref names a component of an event for a caller. User is the verified
// caller (never a request field); Tayfa and Event come from the request
// path and are always used together (IDOR).
type Ref struct {
	User  id.ID
	Tayfa id.ID
	Event id.ID
	// Key is the component's key in the event's type version.
	Key string
}

// Who says who may make a change (Change).
type Who struct {
	role   role
	scope  id.ID
	action authz.Action
}

type role int

const (
	roleParticipant role = iota + 1
	roleManager
	roleReferee
	roleAction
)

// ByParticipant is every active member who is not muted
// (authz.EventComponentParticipate). The component checks the answer or
// verified attendance its rule needs (Scope.Going, Scope.Attended).
func ByParticipant() Who { return Who{role: roleParticipant} }

// ByManager is the event's organizer, and moderators and admins as for every
// event (authz.EventComponentManageOwn / EventComponentManageAny).
func ByManager() Who { return Who{role: roleManager} }

// ByReferee is a manager, or a referee the organizer assigned to the event,
// to this component or to exactly this scope of it (a table, a match;
// event_referees). The zero scope asks for an assignment without a scope.
func ByReferee(scope id.ID) Who { return Who{role: roleReferee, scope: scope} }

// ByAction is an action of the component's own in the permission table.
func ByAction(a authz.Action) Who { return Who{role: roleAction, action: a} }

// Scope is one request on one component of an event, resolved and checked.
// It is valid only inside the View or Change call that made it.
type Scope struct {
	Ref
	// Now is the request's time (UTC, microseconds).
	Now time.Time
	// Type and TypeVersion are the event's type and the version it was
	// created with; Definition is that version's definition and Component
	// the component Key names in it.
	Type        string
	TypeVersion int32
	Definition  *definition.Definition
	Component   definition.Component
	// StartsAt and EndsAt are the event's times; Cancelled its state.
	StartsAt  time.Time
	EndsAt    time.Time
	Cancelled bool
	// Organizer is the event's organizer; zero if their account was
	// deleted.
	Organizer id.ID
	// Enabled is false for an optional component the organizer left off.
	Enabled bool

	q        *store.Queries
	tayfa    access.Tayfa
	m        authz.Membership
	settings authz.Settings
	// chosen holds the organizer's choices by parameter name.
	chosen  map[string]any
	changed bool
	after   []func(ctx context.Context) error
}

// Q returns the request's queries: inside Change they run in its
// transaction.
func (sc *Scope) Q() *store.Queries { return sc.q }

// TayfaRow is the tayfa row the caller's standing was read from.
func (sc *Scope) TayfaRow() access.Tayfa { return sc.tayfa }

// Authorize asks the permission table for the caller: nil,
// apperr.ErrMuted or apperr.ErrPermissionDenied.
func (sc *Scope) Authorize(a authz.Action) error {
	return access.AuthorizeIn(sc.m, a, sc.Now, sc.settings)
}

// manages checks an action the organizer may perform on their own event
// with own and anyone else with anyAction.
func (sc *Scope) manages(own, anyAction authz.Action) error {
	if sc.Authorize(anyAction) == nil {
		return nil
	}
	if sc.Organizer != (id.ID{}) && sc.Organizer == sc.User {
		return sc.Authorize(own)
	}
	return sc.Authorize(anyAction)
}

// IsManager reports whether the caller may manage the event's components
// (ByManager); a mute counts.
func (sc *Scope) IsManager() bool {
	return sc.manages(authz.EventComponentManageOwn, authz.EventComponentManageAny) == nil
}

// AuthorizeManager is the check behind ByManager, for a rule inside a change
// opened to others.
func (sc *Scope) AuthorizeManager() error {
	return sc.manages(authz.EventComponentManageOwn, authz.EventComponentManageAny)
}

// AuthorizeReferee is the check behind ByReferee, for a scope known only
// inside the change (the table a score belongs to).
func (sc *Scope) AuthorizeReferee(ctx context.Context, scope id.ID) error {
	if sc.AuthorizeManager() == nil {
		return nil
	}
	if err := sc.Authorize(authz.EventComponentReferee); err != nil {
		return err
	}
	p := store.IsEventRefereeParams{EventID: sc.Event.PG(), UserID: sc.User.PG(), ComponentKey: sc.Key}
	if scope != (id.ID{}) {
		p.ScopeID = scope.PG()
	}
	ok, err := sc.q.IsEventReferee(ctx, p)
	if err != nil {
		return fmt.Errorf("hakem: %w", err)
	}
	if !ok {
		return apperr.ErrPermissionDenied
	}
	return nil
}

func (sc *Scope) authorize(ctx context.Context, who Who) error {
	switch who.role {
	case roleParticipant:
		return sc.Authorize(authz.EventComponentParticipate)
	case roleManager:
		return sc.AuthorizeManager()
	case roleReferee:
		return sc.AuthorizeReferee(ctx, who.scope)
	case roleAction:
		return sc.Authorize(who.action)
	}
	// The zero Who allows nobody.
	return apperr.ErrPermissionDenied
}

// Started reports whether the event has started.
func (sc *Scope) Started() bool { return !sc.Now.Before(sc.StartsAt) }

// Ended reports whether the event has ended.
func (sc *Scope) Ended() bool { return !sc.Now.Before(sc.EndsAt) }

// Frozen reports whether the write window has closed: seven days after
// the event's end.
func (sc *Scope) Frozen() bool { return frozen(sc.EndsAt, sc.Now) }

func frozen(end, now time.Time) bool { return !now.Before(end.Add(FreezeAfter)) }

// Param returns a parameter of the component as it holds for this event:
// the organizer's choice for a parameter the definition leaves to them
// (the default without a choice), the definition's value otherwise. ok is
// false for a parameter the definition does not set.
func (sc *Scope) Param(name string) (p definition.Param, ok bool) {
	p, ok = sc.Component.Params[name]
	if !ok {
		return definition.Param{}, false
	}
	if v, chose := sc.chosen[name]; chose {
		p.Value = v
	}
	return p, true
}

// ParamString returns a string parameter (Param); "" when it is not set.
func (sc *Scope) ParamString(name string) string {
	p, _ := sc.Param(name)
	return p.String()
}

// ParamBool returns a boolean parameter (Param); false when it is not
// set.
func (sc *Scope) ParamBool(name string) bool {
	p, _ := sc.Param(name)
	return p.Bool()
}

// ParamInt returns an integer parameter (Param); ok is false when it is
// not set.
func (sc *Scope) ParamInt(name string) (n int64, ok bool) {
	p, _ := sc.Param(name)
	n, ok = p.Value.(int64)
	return n, ok
}

// Participation returns user's answer to the event: whether it is "going"
// and whether their attendance was verified.
func (sc *Scope) Participation(ctx context.Context, user id.ID) (going, attended bool, err error) {
	r, err := sc.q.GetEventParticipation(ctx, store.GetEventParticipationParams{EventID: sc.Event.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("katılım: %w", err)
	}
	return r.Status == rsvpGoing, r.Attendance == attendanceAttended, nil
}

// Going reports whether the caller answered "going".
func (sc *Scope) Going(ctx context.Context) (bool, error) {
	going, _, err := sc.Participation(ctx, sc.User)
	return going, err
}

// Attended reports whether the caller's attendance was verified
// ("check-in'i doğrulanmış katılımcı", ADR-0025 §5).
func (sc *Scope) Attended(ctx context.Context) (bool, error) {
	_, attended, err := sc.Participation(ctx, sc.User)
	return attended, err
}

// ActiveMember reports whether user is an active member of the tayfa: the
// check before a component names another member (an assignee, a player).
func (sc *Scope) ActiveMember(ctx context.Context, user id.ID) (bool, error) {
	ok, err := sc.q.IsActiveMember(ctx, store.IsActiveMemberParams{TayfaID: sc.Tayfa.PG(), UserID: user.PG()})
	if err != nil {
		return false, fmt.Errorf("üyelik: %w", err)
	}
	return ok, nil
}

// Changed marks the change for a live update: Change writes
// event.component_changed after fn. Call it when what members see changed.
func (sc *Scope) Changed() { sc.changed = true }

// Defer queues fn to run after the component's own writes, in order and
// before the framework's event: the place for outbox events (Emit) and
// notification requests, which must be the transaction's last writes
// (outbox.Write).
func (sc *Scope) Defer(fn func(ctx context.Context) error) { sc.after = append(sc.after, fn) }

// Emit queues an outbox event of the component's own about the event
// (Defer). The payload names the tayfa, the event and the component; add
// IDs and classification only, never content.
func (sc *Scope) Emit(typ string, payload map[string]any) {
	p := map[string]any{"tayfa_id": sc.Tayfa.String(), "event_id": sc.Event.String(), "component_key": sc.Key}
	for k, v := range payload {
		p[k] = v
	}
	sc.Defer(func(ctx context.Context) error {
		return txn.Event(ctx, sc.q, typ, sc.Tayfa, sc.User, txn.Target{Type: "event", ID: sc.Event}, p)
	})
}

// Audit writes an audit record about the event for a change of the
// component. meta holds IDs and classification only; the component key is
// added.
func (sc *Scope) Audit(ctx context.Context, action string, meta map[string]any) error {
	m := map[string]any{"component_key": sc.Key}
	for k, v := range meta {
		m[k] = v
	}
	return txn.Audit(ctx, sc.q, sc.Tayfa, sc.User, action, txn.Target{Type: "event", ID: sc.Event}, m)
}

// RemoveScopeReferees removes the referee assignments of a scope the
// component deletes (a table, a match).
func (sc *Scope) RemoveScopeReferees(ctx context.Context, scope id.ID) error {
	if err := sc.q.DeleteEventScopeReferees(ctx, store.DeleteEventScopeRefereesParams{
		EventID: sc.Event.PG(), ComponentKey: sc.Key, ScopeID: scope.PG(),
	}); err != nil {
		return fmt.Errorf("kapsamın hakemleri: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Loading

// base is the caller's standing and the event with its type version.
type base struct {
	user     id.ID
	now      time.Time
	tayfa    access.Tayfa
	m        authz.Membership
	settings authz.Settings
	ev       store.GetEventRow
	et       catalog.EventType
}

func (b *base) authorize(a authz.Action) error {
	return access.AuthorizeIn(b.m, a, b.now, b.settings)
}

// manages is Scope.manages before a component is resolved.
func (b *base) manages(own, anyAction authz.Action) error {
	if b.authorize(anyAction) == nil {
		return nil
	}
	if b.ev.CreatedBy.Valid && id.ID(b.ev.CreatedBy.Bytes) == b.user {
		return b.authorize(own)
	}
	return b.authorize(anyAction)
}

// writable refuses changes to a read-only tayfa, a cancelled event and a
// frozen one.
func (b *base) writable() error {
	switch {
	case access.ReadOnly(b.tayfa):
		return apperr.ErrTayfaReadOnly
	case b.ev.Status == statusCancelled:
		return apperr.ErrEventCancelled
	case frozen(b.ev.EndsAt.Time, b.now):
		return apperr.ErrComponentFrozen
	}
	return nil
}

// load loads the caller's standing in the tayfa (an active member; the
// events module on), the event of the tayfa and its type version. With
// lock the tayfa and event rows are locked for the transaction, in the
// order every event write takes them.
func (s *Service) load(ctx context.Context, q *store.Queries, user, tayfa, event id.ID, lock bool) (*base, error) {
	t, m, err := access.Load(ctx, q, tayfa, user, lock)
	if err != nil {
		return nil, err
	}
	if err := access.RequireModule(ctx, q, tayfa, access.ModuleEvents); err != nil {
		return nil, err
	}
	b := &base{user: user, now: s.now(), tayfa: t, m: m, settings: access.Settings(t)}
	if lock {
		var r store.GetEventForUpdateRow
		r, err = q.GetEventForUpdate(ctx, store.GetEventForUpdateParams{TayfaID: tayfa.PG(), ID: event.PG()})
		b.ev = store.GetEventRow(r)
	} else {
		b.ev, err = q.GetEvent(ctx, store.GetEventParams{TayfaID: tayfa.PG(), ID: event.PG()})
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, apperr.ErrEventNotFound
	case err != nil:
		return nil, fmt.Errorf("etkinlik: %w", err)
	}
	if b.et, err = catalog.TypeVersion(ctx, q, b.ev.Type, b.ev.TypeVersion); err != nil {
		// The foreign key guarantees the version exists.
		return nil, fmt.Errorf("etkinliğin tür sürümü: %w", err)
	}
	return b, nil
}

// stored is an event_components row: the organizer's selections for one
// component.
type stored struct {
	enabled bool
	chosen  map[string]any
}

// storedOf reads a component's row; ok is false without one.
func storedOf(ctx context.Context, q *store.Queries, event id.ID, comp definition.Component) (st stored, ok bool, err error) {
	r, err := q.GetEventComponent(ctx, store.GetEventComponentParams{EventID: event.PG(), ComponentKey: comp.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, fmt.Errorf("bileşen seçimi: %w", err)
	}
	st, err = decodeStored(comp, r.Enabled, r.Params)
	return st, err == nil, err
}

// decodeStored checks a row against the definition again (ADR-0011): a
// stored choice that the definition does not offer is an internal error,
// not a value a component would act on.
func decodeStored(comp definition.Component, enabled bool, params []byte) (stored, error) {
	st := stored{enabled: enabled || comp.Optional == nil, chosen: map[string]any{}}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return stored{}, fmt.Errorf("bileşen %q: seçimler okunamadı: %w", comp.Key, err)
	}
	for name, v := range raw {
		p, ok := comp.Params[name]
		if !ok || len(p.Choices) == 0 {
			return stored{}, fmt.Errorf("bileşen %q: %q seçilebilir bir parametre değil", comp.Key, name)
		}
		c, ok := choiceOf(p, jsonText(v))
		if !ok {
			return stored{}, fmt.Errorf("bileşen %q: %q için kayıtlı seçim tanımda yok", comp.Key, name)
		}
		st.chosen[name] = c
	}
	return st, nil
}

// jsonText returns a JSON string's text, or a number's digits.
func jsonText(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return string(v)
}

// defaultEnabled is whether a component is on without a selection.
func defaultEnabled(comp definition.Component) bool {
	return comp.Optional == nil || *comp.Optional
}

// scope resolves ref's component for a loaded request.
func (b *base) scope(ctx context.Context, q *store.Queries, ref Ref, kind string) (*Scope, error) {
	if !definition.ValidKey(ref.Key) {
		return nil, apperr.Validation("component_key")
	}
	comp, ok := b.et.Parsed.Component(ref.Key)
	if !ok || comp.Kind != kind {
		return nil, apperr.ErrComponentNotFound
	}
	sc := &Scope{
		Ref: ref, Now: b.now, Type: b.ev.Type, TypeVersion: b.ev.TypeVersion, Definition: b.et.Parsed, Component: comp,
		StartsAt: b.ev.StartsAt.Time, EndsAt: b.ev.EndsAt.Time, Cancelled: b.ev.Status == statusCancelled,
		Enabled: defaultEnabled(comp), q: q, tayfa: b.tayfa, m: b.m, settings: b.settings,
	}
	if b.ev.CreatedBy.Valid {
		sc.Organizer = id.ID(b.ev.CreatedBy.Bytes)
	}
	st, ok, err := storedOf(ctx, q, ref.Event, comp)
	if err != nil {
		return nil, err
	}
	if ok {
		sc.Enabled, sc.chosen = st.enabled, st.chosen
	}
	return sc, nil
}

// View runs fn for a member who may read the component named by ref, of
// the given kind. Refused: anyone but an active member
// (apperr.ErrTayfaNotFound), the events module off
// (apperr.ErrModuleDisabled), an unknown event (apperr.ErrEventNotFound), a
// component the event's type version does not have or of another kind
// (apperr.ErrComponentNotFound) and a component switched off
// (apperr.ErrComponentDisabled). Reading works on cancelled and frozen
// events too.
func (s *Service) View(ctx context.Context, ref Ref, kind string, fn func(sc *Scope) error) error {
	b, err := s.load(ctx, s.q, ref.User, ref.Tayfa, ref.Event, false)
	if err != nil {
		return err
	}
	if err := b.authorize(authz.EventComponentView); err != nil {
		return err
	}
	sc, err := b.scope(ctx, s.q, ref, kind)
	if err != nil {
		return err
	}
	if !sc.Enabled {
		return apperr.ErrComponentDisabled
	}
	return fn(sc)
}

// Change runs fn in one transaction, with the tayfa and the event locked,
// for a caller who may make the change (who). Besides View's refusals:
// a role that is too low or a mute (apperr.ErrPermissionDenied,
// apperr.ErrMuted), a read-only tayfa (apperr.ErrTayfaReadOnly), a
// cancelled event (apperr.ErrEventCancelled) and the closed write window
// (apperr.ErrComponentFrozen). Rules about the event's phase (before the
// start, after the end) are the component's: Scope.Started, Scope.Ended.
//
// After fn the deferred writes run (Scope.Defer, Scope.Emit) and, if fn
// called Scope.Changed, event.component_changed is written.
func (s *Service) Change(ctx context.Context, ref Ref, kind string, who Who, fn func(sc *Scope) error) error {
	return txn.WithActor(ctx, s.cfg.Pool, ref.User, func(_ pgx.Tx, q *store.Queries) error {
		b, err := s.load(ctx, q, ref.User, ref.Tayfa, ref.Event, true)
		if err != nil {
			return err
		}
		if err := b.authorize(authz.EventComponentView); err != nil {
			return err
		}
		sc, err := b.scope(ctx, q, ref, kind)
		if err != nil {
			return err
		}
		if err := sc.authorize(ctx, who); err != nil {
			return err
		}
		if err := b.writable(); err != nil {
			return err
		}
		if !sc.Enabled {
			return apperr.ErrComponentDisabled
		}
		if err := fn(sc); err != nil {
			return err
		}
		for _, after := range sc.after {
			if err := after(ctx); err != nil {
				return err
			}
		}
		if !sc.changed {
			return nil
		}
		if err := txn.TouchTayfa(ctx, q, ref.Tayfa, b.now); err != nil {
			return err
		}
		return changed(ctx, q, ref.Tayfa, ref.User, ref.Event, ref.Key)
	})
}

// changed writes event.component_changed: IDs only (ADR-0025 §6).
func changed(ctx context.Context, q *store.Queries, tayfa, actor, event id.ID, key string) error {
	return txn.Event(ctx, q, EventChanged, tayfa, actor, txn.Target{Type: "event", ID: event},
		map[string]any{"tayfa_id": tayfa.String(), "event_id": event.String(), "component_key": key})
}

// CheckAccess reports whether user may follow the event's live component
// updates: nil for an active member of the tayfa who may read its
// components. chat asks it, with the user's own token, before it lets a
// connection watch the event and whenever the user's standing may have
// changed (ADR-0025 §6).
func (s *Service) CheckAccess(ctx context.Context, user, tayfa, event id.ID) error {
	b, err := s.load(ctx, s.q, user, tayfa, event, false)
	if err != nil {
		return err
	}
	return b.authorize(authz.EventComponentView)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// Package celebration is the celebration component (kind "celebration",
// issue #255; ADR-0025 §4, §5): the winner's screen of an event and the
// tayfa's notice that the winner was announced.
//
// The definition decides where the winner comes from:
//
//   - source: the key of a component of the same type version with a
//     result (checked by the catalog): a scoreboard (the winning seats of
//     every finished table), a bracket (its champions once finished) or a
//     ranking (the first place). The winners are read from the source's
//     stored result whenever the celebration is read: there is no second
//     record, and a correction of the source within its write window shows
//     at once. A source the organizer switched off has no winner. The vote
//     (issue #254) is a source the catalog takes, but it has no result to
//     read yet: its line is added here with the vote component.
//   - no source: the organizer names the winners by hand when announcing,
//     from the event's participants (event_celebration_winners), at most
//     MaxWinners.
//
// A winner may be a person, a team or a guest player (ADR-0037 §6). Nothing
// is announced that the source hides: a ranking of a sensitive measure has
// no winner here for anyone, and a type whose attendance is sensitive
// (sensitive_attendance) never shows a winner, since naming one would tell
// who came.
//
// Announcing (event_celebrations) is the organizer's (managers): once the
// event has started, while the component's write window is open
// (component.FreezeAfter), and only with a winner. The first announcement
// tells the tayfa's members (notification type "celebration", in-app only,
// ADR-0019's rule of no content: the notice names the event, never the
// winner, so a guest's nickname never reaches it). Announcing again
// changes the winners chosen by hand and notifies no one.
//
// Who may do what (the framework's actions, package component):
//
//	read      every active member who may see the event's components
//	announce  managers (the organizer, and moderators and admins as for
//	          every event)
//
// The badge job (ADR-0039 §2) and the tayfa page's champions (ADR-0027 §4)
// read a celebration with Load.
package celebration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/bracket"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/ranking"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompCelebration

// ParamSource is the definition's source parameter.
const ParamSource = "source"

// MaxWinners bounds the winners chosen by hand.
const MaxWinners = 10

// Audit actions. The records hold IDs only.
const (
	AuditAnnounced = "event.celebration_announced"
	AuditWinners   = "event.celebration_winners_changed"
)

// Winner is one winner: a person, a team or a guest player (exactly one of
// User, Team and Guest). Table is the scoreboard table the winner won at;
// zero for the other sources.
type Winner struct {
	User  id.ID
	Team  id.ID
	Guest id.ID
	Table id.ID
}

// Result is a celebration as Load reads it.
type Result struct {
	// Source is the key of the component the winners come from and
	// SourceKind its kind; both "" for winners chosen by hand.
	Source     string
	SourceKind string
	// Winners in the source's order (tables in their order, a tie in the
	// order of seats or places); those chosen by hand in the order given.
	Winners []Winner
	// Hidden reports that the source hides its result from everyone (a
	// ranking of a sensitive measure) or that the type's attendance is
	// sensitive; Winners is then empty.
	Hidden bool
	// Announced reports that the organizer announced the winner, first at
	// AnnouncedAt.
	Announced   bool
	AnnouncedAt time.Time
}

// Celebration is one celebration component of an event, as the caller sees
// it.
type Celebration struct {
	Result
	// CallerCanAnnounce reports whether the caller may announce now: a
	// manager, the event started, not cancelled or frozen, the tayfa not
	// read-only. Whether there is a winner is in Winners.
	CallerCanAnnounce bool
}

// Service is the celebration component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// ---------------------------------------------------------------------------
// Reading

func optID(v pgtype.UUID) id.ID {
	if !v.Valid {
		return id.ID{}
	}
	return id.ID(v.Bytes)
}

// source returns the component the celebration reads its winners from; ok
// is false for winners chosen by hand. A source the definition does not
// have is an internal error: the catalog checks it.
func source(def *definition.Definition, comp definition.Component) (src definition.Component, ok bool, err error) {
	p, has := comp.Params[ParamSource]
	if !has || p.String() == "" {
		return definition.Component{}, false, nil
	}
	src, found := def.Component(p.String())
	if !found {
		return definition.Component{}, false, fmt.Errorf("kutlama %q: kaynak bileşen %q tanımda yok", comp.Key, p.String())
	}
	return src, true, nil
}

// enabled reports whether an optional component is switched on for event.
func enabled(ctx context.Context, q *store.Queries, event id.ID, c definition.Component) (bool, error) {
	if c.Optional == nil {
		return true, nil
	}
	row, err := q.GetEventComponent(ctx, store.GetEventComponentParams{EventID: event.PG(), ComponentKey: c.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return *c.Optional, nil
	}
	if err != nil {
		return false, fmt.Errorf("kaynak bileşenin seçimi: %w", err)
	}
	return row.Enabled, nil
}

// fromSource reads the source's winners; hidden reports a result the
// source hides from everyone.
func fromSource(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, src definition.Component) (out []Winner, hidden bool, err error) {
	on, err := enabled(ctx, q, event, src)
	if err != nil || !on {
		return nil, false, err
	}
	switch src.Kind {
	case definition.CompScoreboard:
		tables, err := scoreboard.Load(ctx, q, event, src.Key)
		if err != nil {
			return nil, false, err
		}
		for _, t := range tables {
			for _, seat := range t.Result.Winners {
				i := slices.IndexFunc(t.Players, func(p scoreboard.Player) bool { return p.ID == seat })
				if i < 0 {
					continue
				}
				p := t.Players[i]
				w := Winner{User: p.User, Team: p.Team, Guest: p.Guest, Table: t.ID}
				if !w.empty() {
					out = append(out, w)
				}
			}
		}
	case definition.CompBracket:
		r, started, err := bracket.Load(ctx, q, event, src.Key)
		if err != nil || !started {
			return nil, false, err
		}
		for _, e := range r.Champions() {
			if !e.Empty() {
				out = append(out, Winner{User: e.User, Team: e.Team, Guest: e.Guest})
			}
		}
	case definition.CompRanking:
		r, err := ranking.Load(ctx, q, event, def, src)
		if err != nil {
			return nil, false, err
		}
		if r.Hidden {
			return nil, true, nil
		}
		for _, e := range r.Winners() {
			out = append(out, Winner{User: e.User})
		}
	case definition.CompVote:
		// The vote component (issue #254) adds its result here.
	default:
		return nil, false, fmt.Errorf("kutlama: %q kazananı olan bir bileşen değil", src.Kind)
	}
	return out, false, nil
}

// empty reports a winner whose person or team is gone (a deleted account).
func (w Winner) empty() bool {
	return w.User == (id.ID{}) && w.Team == (id.ID{}) && w.Guest == (id.ID{})
}

// Load reads the celebration comp of the definition def for event: for the
// readers that build on it (the badge job, the tayfa page's champions),
// inside their own transaction. Whether the celebration itself is switched
// on is the caller's to check; whether the reader may see the event too.
func Load(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, comp definition.Component) (Result, error) {
	out := Result{Winners: []Winner{}}
	row, err := q.GetEventCelebration(ctx, store.GetEventCelebrationParams{EventID: event.PG(), ComponentKey: comp.Key})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Result{}, fmt.Errorf("kutlama: %w", err)
	default:
		out.Announced, out.AnnouncedAt = true, row.AnnouncedAt.Time.UTC()
	}
	src, sourced, err := source(def, comp)
	if err != nil {
		return Result{}, err
	}
	if sourced {
		out.Source, out.SourceKind = src.Key, src.Kind
	}
	if def.Flags.SensitiveAttendance {
		out.Hidden = true
		return out, nil
	}
	if sourced {
		winners, hidden, err := fromSource(ctx, q, event, def, src)
		if err != nil {
			return Result{}, err
		}
		out.Hidden = hidden
		if len(winners) > 0 {
			out.Winners = winners
		}
		return out, nil
	}
	users, err := q.ListEventCelebrationWinners(ctx, store.ListEventCelebrationWinnersParams{EventID: event.PG(), ComponentKey: comp.Key})
	if err != nil {
		return Result{}, fmt.Errorf("kutlamanın kazananları: %w", err)
	}
	for _, u := range users {
		out.Winners = append(out.Winners, Winner{User: optID(u)})
	}
	return out, nil
}

// writable reports whether the event, the tayfa and the window take an
// announcement now, apart from the caller.
func writable(sc *component.Scope) bool {
	return sc.Enabled && sc.Started() && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// read builds the celebration as the caller sees it, inside View or Change.
func read(ctx context.Context, sc *component.Scope) (Celebration, error) {
	r, err := Load(ctx, sc.Q(), sc.Event, sc.Definition, sc.Component)
	if err != nil {
		return Celebration{}, err
	}
	return Celebration{Result: r, CallerCanAnnounce: writable(sc) && sc.IsManager()}, nil
}

// Get returns the celebration. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Celebration, error) {
	var out Celebration
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Celebration{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Announcing

// cleanWinners checks the winners chosen by hand: one to MaxWinners
// distinct people, in the order given.
func cleanWinners(users []id.ID) ([]id.ID, bool) {
	if len(users) == 0 || len(users) > MaxWinners {
		return nil, false
	}
	out := make([]id.ID, 0, len(users))
	for _, u := range users {
		if u == (id.ID{}) || slices.Contains(out, u) {
			return nil, false
		}
		out = append(out, u)
	}
	return out, true
}

// Announce announces the celebration's winner; managers only. With a
// source, winners must be empty and the source must have a winner; without
// one, winners are the people chosen by hand, each a participant of the
// event. The first announcement notifies the tayfa's members but the
// caller; announcing again only replaces the winners chosen by hand.
// Refused: winners given with a source or bad ones without (validation
// error on "winner_ids"), an event that has not started
// (apperr.ErrCelebrationNotStarted), no winner (apperr.ErrCelebrationNoWinner),
// a winner who is not a participant (apperr.ErrCelebrationNotParticipant)
// and the refusals of component.Service.Change.
func (s *Service) Announce(ctx context.Context, ref component.Ref, winners []id.ID) (Celebration, error) {
	var out Celebration
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		_, sourced, err := source(sc.Definition, sc.Component)
		if err != nil {
			return err
		}
		var chosen []id.ID
		if sourced {
			if len(winners) > 0 {
				return apperr.Validation("winner_ids")
			}
		} else {
			var ok bool
			if chosen, ok = cleanWinners(winners); !ok {
				return apperr.Validation("winner_ids")
			}
		}
		if !sc.Started() {
			return apperr.ErrCelebrationNotStarted
		}
		if sc.Definition.Flags.SensitiveAttendance {
			return apperr.ErrCelebrationNoWinner
		}
		if !sourced {
			for _, u := range chosen {
				member, err := sc.ActiveMember(ctx, u)
				if err != nil {
					return err
				}
				going, attended, err := sc.Participation(ctx, u)
				if err != nil {
					return err
				}
				if !member || (!going && !attended) {
					return apperr.ErrCelebrationNotParticipant
				}
			}
		}
		cur, err := Load(ctx, sc.Q(), sc.Event, sc.Definition, sc.Component)
		if err != nil {
			return err
		}
		if sourced && len(cur.Winners) == 0 {
			return apperr.ErrCelebrationNoWinner
		}
		if !cur.Announced {
			if err := first(ctx, sc, chosen); err != nil {
				return err
			}
		} else if !sourced && !sameUsers(cur.Winners, chosen) {
			if err := replace(ctx, sc, chosen); err != nil {
				return err
			}
			if err := sc.Q().TouchEventCelebration(ctx, store.TouchEventCelebrationParams{
				EventID: sc.Event.PG(), ComponentKey: sc.Key, Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("kutlamayı güncelleme: %w", err)
			}
			sc.Changed()
			if err := sc.Audit(ctx, AuditWinners, map[string]any{"winner_count": len(chosen)}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Celebration{}, err
	}
	return out, nil
}

// first writes the first announcement, its winners chosen by hand and the
// tayfa's notice.
func first(ctx context.Context, sc *component.Scope, chosen []id.ID) error {
	if err := sc.Q().CreateEventCelebration(ctx, store.CreateEventCelebrationParams{
		EventID: sc.Event.PG(), ComponentKey: sc.Key, AnnouncedBy: sc.User.PG(), Now: ts(sc.Now),
	}); err != nil {
		return fmt.Errorf("kutlamayı ilan etme: %w", err)
	}
	if err := replace(ctx, sc, chosen); err != nil {
		return err
	}
	sc.Changed()
	if err := sc.Audit(ctx, AuditAnnounced, map[string]any{"winner_count": len(chosen)}); err != nil {
		return err
	}
	sc.Defer(func(ctx context.Context) error {
		to, err := notify.Members(ctx, sc.Q(), sc.Tayfa, sc.User)
		if err != nil {
			return err
		}
		return notify.Write(ctx, sc.Q(), notify.Request{
			Type: notify.TypeCelebration, Tayfa: sc.Tayfa, Actor: sc.User, TargetType: notify.TargetEvent, Target: sc.Event,
		}, to)
	})
	return nil
}

// replace writes the winners chosen by hand in place of the stored ones.
func replace(ctx context.Context, sc *component.Scope, chosen []id.ID) error {
	if err := sc.Q().DeleteEventCelebrationWinners(ctx, store.DeleteEventCelebrationWinnersParams{
		EventID: sc.Event.PG(), ComponentKey: sc.Key,
	}); err != nil {
		return fmt.Errorf("kutlamanın kazananlarını silme: %w", err)
	}
	for _, u := range chosen {
		if err := sc.Q().AddEventCelebrationWinner(ctx, store.AddEventCelebrationWinnerParams{
			ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: u.PG(), Now: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("kutlamanın kazananını yazma: %w", err)
		}
	}
	return nil
}

// sameUsers reports whether winners are the people of users, in order.
func sameUsers(winners []Winner, users []id.ID) bool {
	if len(winners) != len(users) {
		return false
	}
	for i, w := range winners {
		if w.User != users[i] {
			return false
		}
	}
	return true
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

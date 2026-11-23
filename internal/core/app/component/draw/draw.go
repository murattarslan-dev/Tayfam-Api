// Package draw is the draw component (kind "draw", issue #249; ADR-0025
// §4–§6): a random order of the event's entrants, made once and fixed. Two
// places in a row are a pairing (1–2, 3–4 …); with an odd number the last
// place has a bye. The bracket component (#251) seeds from it (Load).
//
// What is drawn (Subject): the teams of the type's first teams component
// that is on for the event, if the type has one, the teams with people in
// them; otherwise the event's participants, active members of the tayfa
// who answered "going" or whose attendance was verified, together with the
// event's guest players (package guest, issue #252). A guest's place never
// empties: when its nickname is cleared it is still the guest's.
//
// The order comes from crypto/rand only. The result is fixed and auditable:
// core_app may only write the rows, a trigger refuses any change but a
// place emptying (00044), there is no second draw (apperr.ErrDrawAlreadyMade)
// and the audit log records who drew (event.draw_made, with the subject
// and the number of places only). A place empties when its person leaves
// the tayfa (shown empty) or deletes their account, or when its team is
// removed.
//
// Who may do what (the framework's actions, package component):
//
//	read   every active member who may see the event's components
//	draw   managers (the organizer, and moderators and admins as for every
//	       event)
//
// A draw is attendance, not the plan: a series occurrence does not take it.
// A person's place is emptied with their account and exported.
package draw

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompDraw

// Subjects: what a draw orders.
const (
	SubjectParticipants = "participants"
	SubjectTeams        = "teams"
)

// MaxEntries is the most places in a draw (the CHECK on
// event_draw_entries.position).
const MaxEntries = 500

// AuditDrawn is the audit action of a draw. The record holds the subject
// and the number of places only, never a person or a team.
const AuditDrawn = "event.draw_made"

// Entry is one place of a draw. An empty place has a zero User, Team and
// Guest (Empty).
type Entry struct {
	// Position is 1-based.
	Position int32
	// User is the person in a participant draw.
	User id.ID
	// Team is the team in a team draw.
	Team id.ID
	// Guest is the guest player in a participant draw (issue #252).
	Guest id.ID
}

// Empty reports whether nobody holds the place.
func (e Entry) Empty() bool {
	return e.User == (id.ID{}) && e.Team == (id.ID{}) && e.Guest == (id.ID{})
}

// Result is a made draw, as Load reads it.
type Result struct {
	// Subject is SubjectParticipants or SubjectTeams.
	Subject string
	// TeamsKey is the teams component's key for SubjectTeams.
	TeamsKey string
	DrawnAt  time.Time
	// Entries are ordered by position.
	Entries []Entry
}

// Draw is one draw component of an event, as the caller sees it.
type Draw struct {
	// Drawn reports whether the draw was made; Result is set then.
	Drawn bool
	// Result holds the draw; before it, its Subject and TeamsKey are what a
	// draw would take now.
	Result
	// CallerCanDraw reports whether the caller may make the draw now.
	CallerCanDraw bool
}

// Service is the draw component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// Load reads the draw of the component key of event: for the components
// that use it (the bracket), inside their own View or Change. ok is false
// when no draw was made. A person who is no longer an active member of
// the tayfa is an empty place.
func Load(ctx context.Context, q *store.Queries, event id.ID, key string) (r Result, ok bool, err error) {
	d, err := q.GetDraw(ctx, store.GetDrawParams{EventID: event.PG(), ComponentKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("kura: %w", err)
	}
	rows, err := q.ListDrawEntries(ctx, store.ListDrawEntriesParams{EventID: event.PG(), ComponentKey: key})
	if err != nil {
		return Result{}, false, fmt.Errorf("kura sırası: %w", err)
	}
	r = Result{Subject: d.Subject, TeamsKey: d.TeamKey.String, DrawnAt: d.DrawnAt.Time.UTC(), Entries: make([]Entry, 0, len(rows))}
	for _, row := range rows {
		e := Entry{Position: int32(row.Position)}
		if row.UserID.Valid && row.ActiveMember {
			e.User = id.ID(row.UserID.Bytes)
		}
		if row.TeamID.Valid {
			e.Team = id.ID(row.TeamID.Bytes)
		}
		if row.GuestID.Valid {
			e.Guest = id.ID(row.GuestID.Bytes)
		}
		r.Entries = append(r.Entries, e)
	}
	return r, true, nil
}

// teamsKey is the key of the type's first teams component that is on for
// the event, or "" when there is none: then the participants are drawn.
func teamsKey(ctx context.Context, sc *component.Scope) (string, error) {
	for _, c := range sc.Definition.Components {
		if c.Kind != definition.CompTeams {
			continue
		}
		on := c.Optional == nil || *c.Optional
		row, err := sc.Q().GetEventComponent(ctx, store.GetEventComponentParams{EventID: sc.Event.PG(), ComponentKey: c.Key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return "", fmt.Errorf("takım bileşeni: %w", err)
		default:
			on = row.Enabled || c.Optional == nil
		}
		if on {
			return c.Key, nil
		}
	}
	return "", nil
}

// canDraw is what CallerCanDraw reports: the checks Make makes.
func canDraw(sc *component.Scope, drawn bool) bool {
	return !drawn && sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow()) && sc.IsManager()
}

// load reads the draw as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (Draw, error) {
	r, ok, err := Load(ctx, sc.Q(), sc.Event, sc.Key)
	if err != nil {
		return Draw{}, err
	}
	if !ok {
		key, err := teamsKey(ctx, sc)
		if err != nil {
			return Draw{}, err
		}
		r = Result{Subject: SubjectParticipants, TeamsKey: key, Entries: []Entry{}}
		if key != "" {
			r.Subject = SubjectTeams
		}
	}
	return Draw{Drawn: ok, Result: r, CallerCanDraw: canDraw(sc, ok)}, nil
}

// Get returns the draw. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Draw, error) {
	var out Draw
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Draw{}, err
	}
	return out, nil
}

// entrants are what a draw orders now: the teams with people of the
// teams component key, or the participants and the guest players when key
// is "". Their positions are not set.
func entrants(ctx context.Context, sc *component.Scope, key string) ([]Entry, error) {
	if key != "" {
		list, err := teams.Load(ctx, sc.Q(), sc.Event, key)
		if err != nil {
			return nil, err
		}
		out := make([]Entry, 0, len(list))
		for _, t := range list {
			if len(t.Members) > 0 {
				out = append(out, Entry{Team: t.ID})
			}
		}
		return out, nil
	}
	rows, err := sc.Q().ListDrawCandidates(ctx, sc.Event.PG())
	if err != nil {
		return nil, fmt.Errorf("katılımcılar: %w", err)
	}
	guests, err := guest.List(ctx, sc.Q(), sc.Event)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows)+len(guests))
	for _, r := range rows {
		out = append(out, Entry{User: id.ID(r.Bytes)})
	}
	for _, g := range guests {
		out = append(out, Entry{Guest: g.ID})
	}
	return out, nil
}

// Shuffle permutes s in place: Fisher–Yates with crypto/rand.
func Shuffle[T any](s []T) error {
	for i := len(s) - 1; i > 0; i-- {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("rastgele sayı: %w", err)
		}
		j := int(v.Int64()) //nolint:gosec // 0..i
		s[i], s[j] = s[j], s[i]
	}
	return nil
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// Make draws the order and fixes it; managers only. Refused: a draw made
// before (apperr.ErrDrawAlreadyMade), fewer than two to draw
// (apperr.ErrDrawTooFew), more than MaxEntries (apperr.ErrEventLimit) and
// the refusals of component.Service.Change.
func (s *Service) Make(ctx context.Context, ref component.Ref) (Draw, error) {
	var out Draw
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		q := sc.Q()
		// Change locks the event: two draws cannot pass this check
		// together, and the primary key refuses a second row anyway.
		_, made, err := Load(ctx, q, sc.Event, sc.Key)
		if err != nil {
			return err
		}
		if made {
			return apperr.ErrDrawAlreadyMade
		}
		key, err := teamsKey(ctx, sc)
		if err != nil {
			return err
		}
		order, err := entrants(ctx, sc, key)
		if err != nil {
			return err
		}
		if len(order) < 2 {
			return apperr.ErrDrawTooFew
		}
		if len(order) > MaxEntries {
			return apperr.ErrEventLimit
		}
		if err := Shuffle(order); err != nil {
			return err
		}
		subject := SubjectParticipants
		if key != "" {
			subject = SubjectTeams
		}
		now := pgtype.Timestamptz{Time: sc.Now, Valid: true}
		if err := q.CreateDraw(ctx, store.CreateDrawParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, Subject: subject, TeamKey: optText(key), DrawnAt: now,
		}); err != nil {
			return fmt.Errorf("kura: %w", err)
		}
		for i, e := range order {
			p := store.AddDrawEntryParams{
				ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
				Position: int16(i + 1), CreatedAt: now, //nolint:gosec // 1..500
			}
			switch {
			case e.Team != (id.ID{}):
				p.TeamKey, p.TeamID = optText(key), e.Team.PG()
			case e.Guest != (id.ID{}):
				p.GuestID = e.Guest.PG()
			default:
				p.UserID = e.User.PG()
			}
			if err := q.AddDrawEntry(ctx, p); err != nil {
				return fmt.Errorf("kura yeri: %w", err)
			}
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditDrawn, map[string]any{"subject": subject, "entries": len(order)}); err != nil {
			return err
		}
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Draw{}, err
	}
	return out, nil
}

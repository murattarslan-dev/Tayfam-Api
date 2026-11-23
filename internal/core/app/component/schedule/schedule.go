// Package schedule is the schedule component (kind "schedule", issue #247;
// ADR-0025 §4, §5): the event's programme as timed items (arrival at
// 09:00, the talk from 10:00 to 11:30, lunch at 12:30).
//
// An item has a start, an optional end after it, a title and an optional
// note. Items are ordered by their start, and those with the same start in
// the order they were added. The definition bounds the number of items
// (parameter max_items, 1–100; DefaultMaxItems without it). An item's
// times lie within a day of the event: no earlier than a day before its
// start and no later than a day after its end (Margin), so that a
// programme may hold the gathering the night before or the drive home.
//
// The programme is the event's plan: it is written from creating the event
// until the write window closes, and a series occurrence starts with the
// template's items, moved with its start (event/series.go,
// CopyScheduleItems).
//
// Who may do what (the framework's actions, package component):
//
//	read                  every active member who may see the event's components
//	add, edit, remove     managers (the organizer, and moderators and admins
//	                      as for every event)
//
// An item's title and note are user content: they never reach a log, an
// audit record or an outbox event. Rows hold no person.
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompSchedule

// ParamMaxItems is the component's parameter that bounds the items.
const ParamMaxItems = "max_items"

// Limits; the same CHECKs are on event_schedule_items (00042).
const (
	TitleMax = 120
	NoteMax  = 500
	// DefaultMaxItems bounds a programme whose definition sets no
	// max_items: the parameter's own upper bound.
	DefaultMaxItems = 100
	// Margin is how far before the event's start and after its end an
	// item's times may lie.
	Margin = 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Audit actions of the component's changes. The records name the item only,
// never its title or note.
const (
	AuditAdded   = "event.schedule_item_added"
	AuditUpdated = "event.schedule_item_updated"
	AuditRemoved = "event.schedule_item_removed"
)

// Input is an item to add or the new values of an item.
type Input struct {
	StartsAt time.Time
	// EndsAt is zero for an item without an end.
	EndsAt time.Time
	Title  string
	// Note is optional: empty (after trimming) for none.
	Note string
}

// Item is one entry of a programme.
type Item struct {
	ID       id.ID
	StartsAt time.Time
	// EndsAt is zero for an item without an end.
	EndsAt time.Time
	Title  string
	// Note is empty for none.
	Note string
}

// Schedule is one schedule component of an event, as the caller sees it.
type Schedule struct {
	// Items are ordered by their start.
	Items []Item
	// MaxItems is the number of items the programme may hold.
	MaxItems int32
	// CallerCanEdit reports whether the caller may add, edit and remove
	// items now.
	CallerCanEdit bool
}

// Service is the schedule component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// cleanText trims s and refuses invalid UTF-8, control characters and a
// length above maxLen characters; empty is allowed here and refused by the
// caller where the field is required.
func cleanText(s string, maxLen int, field string) (string, error) {
	if len(s) > (maxLen+64)*maxRuneBytes {
		return "", apperr.Validation(field)
	}
	t := strings.TrimSpace(s)
	if !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", apperr.Validation(field)
	}
	if utf8.RuneCountInString(t) > maxLen {
		return "", apperr.Validation(field)
	}
	return t, nil
}

// clean checks an item's values that do not depend on the event.
func clean(in Input) (Input, error) {
	title, err := cleanText(in.Title, TitleMax, "title")
	if err != nil {
		return Input{}, err
	}
	if title == "" {
		return Input{}, apperr.Validation("title")
	}
	note, err := cleanText(in.Note, NoteMax, "note")
	if err != nil {
		return Input{}, err
	}
	if in.StartsAt.IsZero() {
		return Input{}, apperr.Validation("start_time")
	}
	out := Input{StartsAt: in.StartsAt.UTC().Truncate(time.Microsecond), Title: title, Note: note}
	if !in.EndsAt.IsZero() {
		out.EndsAt = in.EndsAt.UTC().Truncate(time.Microsecond)
		if !out.EndsAt.After(out.StartsAt) {
			return Input{}, apperr.Validation("end_time")
		}
	}
	return out, nil
}

// within checks that the item's times lie within Margin of the event.
func within(sc *component.Scope, in Input) error {
	lo, hi := sc.StartsAt.Add(-Margin), sc.EndsAt.Add(Margin)
	if in.StartsAt.Before(lo) || in.StartsAt.After(hi) {
		return apperr.Validation("start_time")
	}
	if !in.EndsAt.IsZero() && in.EndsAt.After(hi) {
		return apperr.Validation("end_time")
	}
	return nil
}

// maxItems is the component's max_items parameter, or DefaultMaxItems.
func maxItems(sc *component.Scope) int64 {
	if n, ok := sc.ParamInt(ParamMaxItems); ok && n >= 1 && n <= DefaultMaxItems {
		return n
	}
	return DefaultMaxItems
}

// canEdit is what CallerCanEdit reports: the checks Change makes.
func canEdit(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow()) && sc.IsManager()
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func optTS(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return ts(t)
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// load reads the programme as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (Schedule, error) {
	out := Schedule{Items: []Item{}, MaxItems: int32(maxItems(sc)), CallerCanEdit: canEdit(sc)} //nolint:gosec // 1..100
	rows, err := sc.Q().ListScheduleItems(ctx, store.ListScheduleItemsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	if err != nil {
		return Schedule{}, fmt.Errorf("program: %w", err)
	}
	for _, r := range rows {
		it := Item{ID: id.ID(r.ID.Bytes), StartsAt: r.StartsAt.Time.UTC(), Title: r.Title, Note: r.Note.String}
		if r.EndsAt.Valid {
			it.EndsAt = r.EndsAt.Time.UTC()
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// Get returns the programme. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Schedule, error) {
	var out Schedule
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Schedule{}, err
	}
	return out, nil
}

// done marks the change, writes its audit record (the item's ID only) and
// reads the result for the caller.
func done(ctx context.Context, sc *component.Scope, action string, item id.ID) (Schedule, error) {
	sc.Changed()
	if err := sc.Audit(ctx, action, map[string]any{"item_id": item.String()}); err != nil {
		return Schedule{}, err
	}
	return load(ctx, sc)
}

// Add adds an item; managers only. Refused: a bad value (validation error
// on title, note, start_time or end_time; a time more than Margin outside the
// event), a full programme (apperr.ErrEventLimit) and the refusals of
// component.Service.Change.
func (s *Service) Add(ctx context.Context, ref component.Ref, in Input) (Schedule, error) {
	in, err := clean(in)
	if err != nil {
		return Schedule{}, err
	}
	var out Schedule
	err = s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := within(sc, in); err != nil {
			return err
		}
		n, err := sc.Q().CountScheduleItems(ctx, store.CountScheduleItemsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("program: %w", err)
		}
		if n >= maxItems(sc) {
			return apperr.ErrEventLimit
		}
		item := id.New()
		if err := sc.Q().CreateScheduleItem(ctx, store.CreateScheduleItemParams{
			ID: item.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, StartsAt: ts(in.StartsAt), EndsAt: optTS(in.EndsAt),
			Title: in.Title, Note: optText(in.Note), CreatedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("program maddesi: %w", err)
		}
		out, err = done(ctx, sc, AuditAdded, item)
		return err
	})
	if err != nil {
		return Schedule{}, err
	}
	return out, nil
}

// Update replaces an item's values; managers only. Refused: a bad value
// (as Add), an unknown item (apperr.ErrEventItemNotFound) and the refusals
// of component.Service.Change.
func (s *Service) Update(ctx context.Context, ref component.Ref, item id.ID, in Input) (Schedule, error) {
	in, err := clean(in)
	if err != nil {
		return Schedule{}, err
	}
	var out Schedule
	err = s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := within(sc, in); err != nil {
			return err
		}
		n, err := sc.Q().UpdateScheduleItem(ctx, store.UpdateScheduleItemParams{
			StartsAt: ts(in.StartsAt), EndsAt: optTS(in.EndsAt), Title: in.Title, Note: optText(in.Note), UpdatedAt: ts(sc.Now),
			EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: item.PG(),
		})
		if err != nil {
			return fmt.Errorf("program maddesi: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditUpdated, item)
		return err
	})
	if err != nil {
		return Schedule{}, err
	}
	return out, nil
}

// Remove removes an item; managers only. Refused: an unknown item
// (apperr.ErrEventItemNotFound) and the refusals of
// component.Service.Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, item id.ID) (Schedule, error) {
	var out Schedule
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		n, err := sc.Q().DeleteScheduleItem(ctx, store.DeleteScheduleItemParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: item.PG()})
		if err != nil {
			return fmt.Errorf("program maddesini silme: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		out, err = done(ctx, sc, AuditRemoved, item)
		return err
	})
	if err != nil {
		return Schedule{}, err
	}
	return out, nil
}

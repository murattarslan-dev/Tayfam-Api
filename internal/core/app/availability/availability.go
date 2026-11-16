// Package availability holds "Boşum": the days a user is free, shown to all
// of their friends and nobody else (issue #207, ADR-0029; Notion "Faz 2
// (v2)" §3).
//
// Rules:
//
//   - A day is a calendar date, not an instant. "Today" is the
//     Europe/Istanbul date (as for ages); a day from today through today +
//     MaxDaysAhead can be written. Past days are never returned and the
//     retention job deletes them.
//   - A day holds one to three slots (morning, afternoon, evening) or
//     all_day alone, and an optional note of at most MaxNoteLength
//     characters without control characters.
//   - Writing a day replaces it; deleting is for good (no soft delete: it is
//     the user's own state, not tayfa content).
//   - The owner and every accepted friend with no block in either direction
//     read the days; everyone else gets apperr.ErrUserNotFound, the same as
//     for an unknown user. The rule is read from friendships and user_blocks
//     on every call; nothing is copied.
//   - No events and no notifications (platform owner, 2026-11-15).
//
// The note is user content: this package logs nothing, and errors never
// carry it, the dates or the slots.
package availability

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/localday"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (Notion "Faz 2 (v2)" §3; also CHECKs and a trigger in core
// migration 00024).
const (
	// MaxDaysAhead: the last day that can be written is today + 30.
	MaxDaysAhead = 30
	// MaxNoteLength in characters (runes).
	MaxNoteLength = 60
)

// DateLayout is the wire form of a day.
const DateLayout = time.DateOnly

// Slot is a rough part of a day.
type Slot string

// Slots, in their order.
const (
	SlotMorning   Slot = "morning"
	SlotAfternoon Slot = "afternoon"
	SlotEvening   Slot = "evening"
	SlotAllDay    Slot = "all_day"
)

var slotOrder = []Slot{SlotMorning, SlotAfternoon, SlotEvening, SlotAllDay}

// Day is one free day.
type Day struct {
	// Date is the day at midnight UTC; only its date matters.
	Date  time.Time
	Slots []Slot
	// Note is empty when there is none.
	Note      string
	UpdatedAt time.Time
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time // optional, for tests
}

// Service implements AvailabilityService.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	now  func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("availability: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: cfg.Pool, q: store.New(cfg.Pool), now: cfg.Now}, nil
}

// Today returns the Europe/Istanbul date of now, at midnight UTC.
func Today(now time.Time) time.Time { return localday.Today(now) }

// ParseDate reads a YYYY-MM-DD day.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil || t.Format(DateLayout) != s {
		return time.Time{}, apperr.Validation("date")
	}
	return t, nil
}

// NormalizeSlots drops duplicates and sorts slots; all_day must be alone.
func NormalizeSlots(in []Slot) ([]Slot, error) {
	bad := apperr.Validation("slots")
	seen := map[Slot]bool{}
	for _, s := range in {
		if !slices.Contains(slotOrder, s) {
			return nil, bad
		}
		seen[s] = true
	}
	var out []Slot
	for _, s := range slotOrder {
		if seen[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 || (seen[SlotAllDay] && len(out) > 1) {
		return nil, bad
	}
	return out, nil
}

// NormalizeNote trims note; empty means none. It must be valid UTF-8, at
// most MaxNoteLength characters and without a ForbiddenNoteRune.
func NormalizeNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > MaxNoteLength ||
		strings.ContainsFunc(note, ForbiddenNoteRune) {
		return "", apperr.Validation("note")
	}
	return note, nil
}

// ForbiddenNoteRune reports the characters a note may not hold: control
// characters (Cc), line and paragraph separators, bidirectional controls
// (text that reads backwards, U+202E) and invisible characters. The zero
// width joiner (U+200D), variation selectors and tag characters stay
// allowed: emoji sequences use them. The database CHECK
// user_availability_note (core migration 00024) lists exactly the same
// characters; a test keeps the two in step.
func ForbiddenNoteRune(r rune) bool {
	switch {
	case r <= 0x1f, r >= 0x7f && r <= 0x9f: // Cc
		return true
	case r == 0x00ad, r == 0x061c, r == 0x180e: // soft hyphen, Arabic letter mark, Mongolian vowel separator
		return true
	case r == 0x200b, r == 0x200c, r == 0x200e, r == 0x200f: // zero width space and non-joiner, LRM, RLM
		return true
	case r == 0x2028, r == 0x2029: // line and paragraph separators
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x206f: // word joiner, invisible operators, bidi isolates
		return true
	case r == 0xfeff, r >= 0xfff9 && r <= 0xfffb: // BOM, interlinear annotation
		return true
	}
	return false
}

// Set writes the caller's day, replacing what was there.
func (s *Service) Set(ctx context.Context, caller id.ID, date string, slots []Slot, note string) (Day, error) {
	day, err := ParseDate(date)
	if err != nil {
		return Day{}, err
	}
	today := Today(s.now())
	if day.Before(today) || day.After(today.AddDate(0, 0, MaxDaysAhead)) {
		return Day{}, apperr.Validation("date")
	}
	slots, err = NormalizeSlots(slots)
	if err != nil {
		return Day{}, err
	}
	note, err = NormalizeNote(note)
	if err != nil {
		return Day{}, err
	}
	r, err := s.q.UpsertAvailability(ctx, store.UpsertAvailabilityParams{
		ID: id.New().PG(), UserID: caller.PG(), Day: pgDate(day), Slots: slotStrings(slots),
		Note: pgtype.Text{String: note, Valid: note != ""},
	})
	if err != nil {
		if field := checkField(err); field != "" {
			// Go and the database check the same rules; should they ever
			// differ, the caller still gets a validation error, not a 500.
			return Day{}, apperr.Validation(field)
		}
		return Day{}, fmt.Errorf("boş vakit: %w", err)
	}
	return dayOf(r.Day, r.Slots, r.Note, r.UpdatedAt), nil
}

// Delete deletes the caller's day. Deleting nothing is not an error.
func (s *Service) Delete(ctx context.Context, caller id.ID, date string) error {
	day, err := ParseDate(date)
	if err != nil {
		return err
	}
	if _, err := s.q.DeleteAvailability(ctx, store.DeleteAvailabilityParams{UserID: caller.PG(), Day: pgDate(day)}); err != nil {
		return fmt.Errorf("boş vakit silme: %w", err)
	}
	return nil
}

// ListMine lists the caller's days from today on.
func (s *Service) ListMine(ctx context.Context, caller id.ID) ([]Day, error) {
	return list(ctx, s.q, caller, Today(s.now()))
}

// ListUser lists target's days for the caller: their own, or an accepted
// friend's with no block either way. Anyone else looks unknown.
func (s *Service) ListUser(ctx context.Context, caller, target id.ID) ([]Day, error) {
	if target == (id.ID{}) {
		return nil, apperr.Validation("user_id")
	}
	if target == caller {
		return s.ListMine(ctx, caller)
	}
	var out []Day
	// One snapshot: the friendship, the block and the days are read
	// together, so a block committed meanwhile cannot leak a later read.
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.GetLiveUserProfile(ctx, target.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrUserNotFound
			}
			return fmt.Errorf("profil: %w", err)
		}
		blocked, err := social.IsBlocked(ctx, q, caller, target)
		if err != nil {
			return err
		}
		friends, err := social.Friends(ctx, q, caller, target)
		if err != nil {
			return err
		}
		if blocked || !friends {
			return apperr.ErrUserNotFound
		}
		out, err = list(ctx, q, target, Today(s.now()))
		return err
	})
	return out, err
}

func list(ctx context.Context, q *store.Queries, user id.ID, today time.Time) ([]Day, error) {
	rows, err := q.ListAvailability(ctx, store.ListAvailabilityParams{UserID: user.PG(), Today: pgDate(today)})
	if err != nil {
		return nil, fmt.Errorf("boş vakitler: %w", err)
	}
	out := make([]Day, 0, len(rows))
	for _, r := range rows {
		out = append(out, dayOf(r.Day, r.Slots, r.Note, r.UpdatedAt))
	}
	return out, nil
}

// DeletePast deletes days before today for the retention job and returns
// how many went.
func DeletePast(ctx context.Context, q *store.Queries, now time.Time) (int64, error) {
	n, err := q.DeletePastAvailability(ctx, pgDate(Today(now)))
	if err != nil {
		return 0, fmt.Errorf("geçmiş boş vakitler: %w", err)
	}
	return n, nil
}

// SlotsOf converts stored slots (for the friend list badge).
func SlotsOf(in []string) []Slot {
	out := make([]Slot, len(in))
	for i, s := range in {
		out[i] = Slot(s)
	}
	return out
}

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func slotStrings(in []Slot) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func dayOf(d pgtype.Date, slots []string, note pgtype.Text, updated pgtype.Timestamptz) Day {
	return Day{
		Date:  time.Date(d.Time.Year(), d.Time.Month(), d.Time.Day(), 0, 0, 0, 0, time.UTC),
		Slots: SlotsOf(slots), Note: note.String, UpdatedAt: updated.Time.UTC(),
	}
}

// checkField maps a check_violation (23514) of user_availability to the
// request field it is about; "" for any other error. The day window
// trigger raises it without a constraint name.
func checkField(err error) string {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != store.CodeCheckViolation {
		return ""
	}
	switch pg.ConstraintName {
	case "user_availability_note":
		return "note"
	case "user_availability_slots":
		return "slots"
	default:
		return "date"
	}
}

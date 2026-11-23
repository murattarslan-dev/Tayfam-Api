// Package daily is günün içeriği (issue #269; ADR-0027 §5): the tayfa's
// own pool of short texts (a saying, a verse, a note) and the one the home
// page shows today. Tayfam never puts content in a pool (dış içerik yok).
//
// The pool is a module (tayfa_modules "daily"); its home widget "daily"
// (package home) is the module's showcase and reads Today. With the module
// off every method gets apperr.ErrModuleDisabled.
//
// Who may do what (the permission table, package authz):
//
//	read the pool and today's item  every active member, muted ones
//	                                included (authz.TayfaView)
//	add, edit, delete, restore,     moderators and admins
//	order, pin                      (authz.TayfaDailyManage), not on a
//	                                read-only tayfa
//
// # Choosing today's item
//
// The day is the Europe/Istanbul day (package localday). An item pinned to
// the day (its year, or every year) wins; among several the first in pool
// order (position, then ID). Otherwise the next unpinned item in pool
// order: tayfa_daily_state keeps where the rotation goes on
// (next_position) and the item chosen on the last day it moved (shown_on,
// shown_item_id). The first request of a day chooses and writes; the rest
// of the day reads the same item. The rotation moves one step per day the
// card is asked for, back to the start after the last item, so a day
// nobody opens skips no item. A pinned day does not move it either. Pinned
// items are never in the rotation. Reordering keeps next_position: the
// rotation goes on from the same place number.
//
// Texts are content: they never reach a log, an audit record or an event;
// IDs do. Account deletion keeps the items (they are the tayfa's) and
// clears who added and deleted them; export lists the items one added
// (package export, section daily_items).
package daily

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/localday"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (ADR-0027 §5; the same CHECKs are in the database).
const (
	// BodyMax bounds an item's text, in characters.
	BodyMax = 500
	// AttributionMax bounds its attribution ("Atatürk", "Yunus Emre"…).
	AttributionMax = 120
	// MaxItems bounds a tayfa's pool (live items).
	MaxItems = 1000
	// RestoreWindow is how long a deleted item can be restored; the purge
	// job deletes it for good after that.
	RestoreWindow = 30 * 24 * time.Hour
	// MinPinYear and MaxPinYear bound a pin's year.
	MinPinYear = 2000
	MaxPinYear = 2100
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Validation fields.
const (
	FieldBody            = "body"
	FieldAttribution     = "attribution"
	FieldPin             = "pin"
	FieldItemID          = "item_id"
	FieldItemIDs         = "item_ids"
	FieldClientRequestID = "client_request_id"
)

// Audit actions. The records name the item by ID only.
const (
	AuditCreated   = "tayfa.daily_item_created"
	AuditUpdated   = "tayfa.daily_item_updated"
	AuditDeleted   = "tayfa.daily_item_deleted"
	AuditRestored  = "tayfa.daily_item_restored"
	AuditReordered = "tayfa.daily_reordered"
)

// targetType is the audit record's subject type (and the report target).
const targetType = "daily_item"

// Pin pins an item to a day of the year.
type Pin struct {
	Month int
	Day   int
	// Year is 0 for every year.
	Year int
}

// Item is an item of a tayfa's pool.
type Item struct {
	ID          id.ID
	Body        string
	Attribution string // "" for none
	Position    int32
	Pin         *Pin // nil when not pinned
	// CreatedBy is zero once the account is deleted.
	CreatedBy id.ID
	CreatedAt time.Time
	UpdatedAt time.Time
	// DeletedAt is zero for a live item.
	DeletedAt time.Time
}

// Input is an item's text and pin as a manager writes them.
type Input struct {
	Body        string
	Attribution string
	Pin         *Pin
}

// Saved is a written item and whether another live item is pinned to the
// same day (ADR-0027 §5 "Çakışma uyarısı": not refused, only reported).
type Saved struct {
	Item        Item
	PinConflict bool
}

// Today is the item shown today.
type Today struct {
	// Item is nil when the pool has nothing to show.
	Item *Item
	// Pinned: the item is pinned to today.
	Pinned bool
	// Date is today in Europe/Istanbul, at midnight UTC.
	Date time.Time
}

// List is a pool, in order.
type List struct {
	Items []Item
	// CallerCanManage reports whether the caller may manage the pool now.
	CallerCanManage bool
}

// Service serves the pool, built on the component framework's tayfa scope
// (component.Service.Tayfa).
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// badRune reports a control or format character (zero-width and direction
// marks), the line breaks and tabs allowed by keep aside.
func badRune(keep func(rune) bool) func(rune) bool {
	return func(r rune) bool {
		if keep(r) {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}
}

// CleanBody returns an item's text as stored: line breaks kept ("\r\n"
// and "\r" become "\n"), every line trimmed with its whitespace folded to
// single spaces, empty lines at the start and the end dropped, at most one
// empty line in a row. Refused: empty, invalid UTF-8, any other control or
// format character, more than BodyMax characters.
func CleanBody(s string) (string, bool) {
	if len(s) > (BodyMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if strings.IndexFunc(s, badRune(func(r rune) bool { return r == '\n' || r == '\t' })) >= 0 {
		return "", false
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, l)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	t := strings.Join(lines, "\n")
	if t == "" || utf8.RuneCountInString(t) > BodyMax {
		return "", false
	}
	return t, true
}

// CleanAttribution returns an attribution as stored: one line, trimmed,
// whitespace (line breaks too) folded to single spaces; "" for none.
// Refused: invalid UTF-8, any other control or format character, more than
// AttributionMax characters.
func CleanAttribution(s string) (string, bool) {
	if len(s) > (AttributionMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, badRune(func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' })) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(t) > AttributionMax {
		return "", false
	}
	return t, true
}

// ValidPin reports whether p is a day of the year: a month, a day of it
// (29 February with no year is shown in leap years only) and a year of
// MinPinYear–MaxPinYear or none. nil (no pin) is valid.
func ValidPin(p *Pin) bool {
	if p == nil {
		return true
	}
	if p.Month < 1 || p.Month > 12 || p.Day < 1 {
		return false
	}
	year := p.Year
	if year == 0 {
		year = 2000 // a leap year: 29 February is allowed without a year
	} else if year < MinPinYear || year > MaxPinYear {
		return false
	}
	return p.Day <= daysIn(year, time.Month(p.Month))
}

func daysIn(year int, m time.Month) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// checkTerms is the one place a daily item meets the abuse term check
// (ADR-0012). The term list and its check are not in core yet; when they
// come, the text and the attribution are checked here and nowhere else.
func checkTerms(...string) error { return nil }

// clean checks an input; the error names the field.
func clean(in Input) (Input, error) {
	body, ok := CleanBody(in.Body)
	if !ok {
		return Input{}, apperr.Validation(FieldBody)
	}
	attr, ok := CleanAttribution(in.Attribution)
	if !ok {
		return Input{}, apperr.Validation(FieldAttribution)
	}
	if !ValidPin(in.Pin) {
		return Input{}, apperr.Validation(FieldPin)
	}
	if err := checkTerms(body, attr); err != nil {
		return Input{}, err
	}
	out := Input{Body: body, Attribution: attr}
	if in.Pin != nil {
		p := *in.Pin
		out.Pin = &p
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func int2(v int) pgtype.Int2 { return pgtype.Int2{Int16: int16(v), Valid: true} } //nolint:gosec // bounded by ValidPin and the calendar

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// pinColumns returns the pin's columns; all NULL without a pin, the year
// NULL for every year.
func pinColumns(p *Pin) (month, day, year pgtype.Int2) {
	if p == nil {
		return
	}
	month, day = int2(p.Month), int2(p.Day)
	if p.Year != 0 {
		year = int2(p.Year)
	}
	return
}

func itemOf(r store.TayfaDailyItem) Item {
	it := Item{
		ID: id.ID(r.ID.Bytes), Body: r.Body, Attribution: r.Attribution.String, Position: r.Position,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if r.PinMonth.Valid && r.PinDay.Valid {
		it.Pin = &Pin{Month: int(r.PinMonth.Int16), Day: int(r.PinDay.Int16)}
		if r.PinYear.Valid {
			it.Pin.Year = int(r.PinYear.Int16)
		}
	}
	if r.CreatedBy.Valid {
		it.CreatedBy = id.ID(r.CreatedBy.Bytes)
	}
	if r.DeletedAt.Valid {
		it.DeletedAt = r.DeletedAt.Time.UTC()
	}
	return it
}

// read runs fn for a reader: an active member (anyone else gets
// apperr.ErrTayfaNotFound), the module on, TayfaView.
func (s *Service) read(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleDaily); err != nil {
			return err
		}
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		return fn(sc)
	})
}

// write runs fn for a manager in a transaction with the tayfa locked: the
// module on, authz.TayfaDailyManage, not a read-only tayfa.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleDaily); err != nil {
			return err
		}
		if err := sc.Authorize(authz.TayfaDailyManage); err != nil {
			return err
		}
		return fn(sc)
	})
}

// saved reads an item back with its pin conflict.
func saved(ctx context.Context, q *store.Queries, tayfa, item id.ID) (Saved, error) {
	r, err := q.GetDailyItem(ctx, store.GetDailyItemParams{TayfaID: tayfa.PG(), ID: item.PG()})
	if err != nil {
		return Saved{}, fmt.Errorf("günün içeriği: %w", err)
	}
	out := Saved{Item: itemOf(r)}
	if out.Item.Pin == nil || r.DeletedAt.Valid {
		return out, nil
	}
	n, err := q.CountDailyPinConflicts(ctx, store.CountDailyPinConflictsParams{
		TayfaID: tayfa.PG(), ID: item.PG(), PinMonth: r.PinMonth, PinDay: r.PinDay, PinYear: r.PinYear,
	})
	if err != nil {
		return Saved{}, fmt.Errorf("sabitleme çakışması: %w", err)
	}
	out.PinConflict = n > 0
	return out, nil
}

// lock reads an item of the tayfa for a change.
func lock(ctx context.Context, q *store.Queries, tayfa, item id.ID) (store.TayfaDailyItem, error) {
	r, err := q.GetDailyItemForUpdate(ctx, store.GetDailyItemForUpdateParams{TayfaID: tayfa.PG(), ID: item.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.TayfaDailyItem{}, apperr.ErrDailyItemNotFound
	case err != nil:
		return store.TayfaDailyItem{}, fmt.Errorf("günün içeriği: %w", err)
	}
	return r, nil
}

// room refuses an addition to a full pool.
func room(ctx context.Context, q *store.Queries, tayfa id.ID) error {
	n, err := q.CountDailyItems(ctx, tayfa.PG())
	if err != nil {
		return fmt.Errorf("havuz: %w", err)
	}
	if n >= MaxItems {
		return apperr.ErrDailyItemLimit
	}
	return nil
}

func audit(ctx context.Context, sc *component.TayfaScope, action string, item id.ID, meta map[string]any) error {
	return sc.Audit(ctx, action, txn.Target{Type: targetType, ID: item}, meta)
}

// List returns the pool in its order. With deleted it returns instead the
// items deleted within RestoreWindow, the latest first; that list is the
// managers' (apperr.ErrPermissionDenied, apperr.ErrMuted otherwise).
func (s *Service) List(ctx context.Context, user, tayfa id.ID, deleted bool) (List, error) {
	var out List
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		manage := sc.Authorize(authz.TayfaDailyManage)
		out = List{Items: []Item{}, CallerCanManage: manage == nil && !sc.ReadOnly()}
		var (
			rows []store.TayfaDailyItem
			err  error
		)
		if deleted {
			if manage != nil {
				return manage
			}
			rows, err = sc.Q().ListDeletedDailyItems(ctx, store.ListDeletedDailyItemsParams{
				TayfaID: tayfa.PG(), Since: ts(sc.Now.Add(-RestoreWindow)),
			})
		} else {
			rows, err = sc.Q().ListDailyItems(ctx, tayfa.PG())
		}
		if err != nil {
			return fmt.Errorf("havuz: %w", err)
		}
		for _, r := range rows {
			out.Items = append(out.Items, itemOf(r))
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// Create adds an item at the end of the pool. A repeated client request ID
// returns the item it created. Refused: a bad input (validation error on
// body, attribution, pin or client_request_id), a full pool
// (apperr.ErrDailyItemLimit), and the refusals of every write.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in Input, clientRequestID string) (Saved, error) {
	crid, ok := id.Parse(clientRequestID)
	if !ok {
		return Saved{}, apperr.Validation(FieldClientRequestID)
	}
	c, err := clean(in)
	if err != nil {
		return Saved{}, err
	}
	var out Saved
	err = s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		prev, err := q.GetDailyItemByClientRequest(ctx, store.GetDailyItemByClientRequestParams{TayfaID: tayfa.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			out, err = saved(ctx, q, tayfa, id.ID(prev.ID.Bytes))
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("günün içeriği: %w", err)
		}
		if err := room(ctx, q, tayfa); err != nil {
			return err
		}
		pos, err := q.NextDailyPosition(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("havuz sırası: %w", err)
		}
		item := id.New()
		month, day, year := pinColumns(c.Pin)
		if err := q.CreateDailyItem(ctx, store.CreateDailyItemParams{
			ID: item.PG(), TayfaID: tayfa.PG(), Body: c.Body, Attribution: text(c.Attribution), Position: pos,
			PinMonth: month, PinDay: day, PinYear: year, CreatedBy: user.PG(), ClientRequestID: crid.PG(), Now: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("günün içeriği ekleme: %w", err)
		}
		if err := audit(ctx, sc, AuditCreated, item, map[string]any{"pinned": c.Pin != nil}); err != nil {
			return err
		}
		out, err = saved(ctx, q, tayfa, item)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	return out, nil
}

// Update replaces an item's text, attribution and pin; its place does not
// change. Refused: a bad input, an item not in the pool
// (apperr.ErrDailyItemNotFound), and the refusals of every write.
func (s *Service) Update(ctx context.Context, user, tayfa, item id.ID, in Input) (Saved, error) {
	c, err := clean(in)
	if err != nil {
		return Saved{}, err
	}
	var out Saved
	err = s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		r, err := lock(ctx, q, tayfa, item)
		if err != nil {
			return err
		}
		if r.DeletedAt.Valid {
			return apperr.ErrDailyItemNotFound
		}
		month, day, year := pinColumns(c.Pin)
		n, err := q.UpdateDailyItem(ctx, store.UpdateDailyItemParams{
			Body: c.Body, Attribution: text(c.Attribution), PinMonth: month, PinDay: day, PinYear: year,
			Now: ts(sc.Now), TayfaID: tayfa.PG(), ID: item.PG(),
		})
		if err != nil {
			return fmt.Errorf("günün içeriği düzenleme: %w", err)
		}
		if n != 1 {
			return apperr.ErrDailyItemNotFound
		}
		if err := audit(ctx, sc, AuditUpdated, item, map[string]any{"pinned": c.Pin != nil}); err != nil {
			return err
		}
		out, err = saved(ctx, q, tayfa, item)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	return out, nil
}

// Delete deletes an item; Restore brings it back within RestoreWindow.
// Refused: an item not in the pool (apperr.ErrDailyItemNotFound), and the
// refusals of every write.
func (s *Service) Delete(ctx context.Context, user, tayfa, item id.ID) error {
	return s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		n, err := q.DeleteDailyItem(ctx, store.DeleteDailyItemParams{
			Now: ts(sc.Now), DeletedBy: user.PG(), TayfaID: tayfa.PG(), ID: item.PG(),
		})
		if err != nil {
			return fmt.Errorf("günün içeriği silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrDailyItemNotFound
		}
		return audit(ctx, sc, AuditDeleted, item, nil)
	})
}

// Restore brings back an item deleted within RestoreWindow, at the end of
// the pool. Restoring a live item changes nothing. Refused: an unknown item
// or one deleted too long ago (apperr.ErrDailyItemNotFound), a full pool
// (apperr.ErrDailyItemLimit), and the refusals of every write.
func (s *Service) Restore(ctx context.Context, user, tayfa, item id.ID) (Saved, error) {
	var out Saved
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		r, err := lock(ctx, q, tayfa, item)
		if err != nil {
			return err
		}
		if r.DeletedAt.Valid {
			if sc.Now.Sub(r.DeletedAt.Time) > RestoreWindow {
				return apperr.ErrDailyItemNotFound
			}
			if err := room(ctx, q, tayfa); err != nil {
				return err
			}
			pos, err := q.NextDailyPosition(ctx, tayfa.PG())
			if err != nil {
				return fmt.Errorf("havuz sırası: %w", err)
			}
			n, err := q.RestoreDailyItem(ctx, store.RestoreDailyItemParams{
				Position: pos, Now: ts(sc.Now), TayfaID: tayfa.PG(), ID: item.PG(),
			})
			if err != nil {
				return fmt.Errorf("geri alma: %w", err)
			}
			if n != 1 {
				return apperr.ErrDailyItemNotFound
			}
			if err := audit(ctx, sc, AuditRestored, item, nil); err != nil {
				return err
			}
		}
		out, err = saved(ctx, q, tayfa, item)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	return out, nil
}

// Reorder sets the pool's order: ids holds every live item exactly once.
// Refused: an item missing, repeated or not in the pool (validation error
// on item_ids), and the refusals of every write.
func (s *Service) Reorder(ctx context.Context, user, tayfa id.ID, ids []id.ID) ([]Item, error) {
	if len(ids) > MaxItems {
		return nil, apperr.Validation(FieldItemIDs)
	}
	var out []Item
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		rows, err := q.ListDailyItems(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("havuz: %w", err)
		}
		if len(rows) != len(ids) {
			return apperr.Validation(FieldItemIDs)
		}
		live := make(map[id.ID]bool, len(rows))
		for _, r := range rows {
			live[id.ID(r.ID.Bytes)] = true
		}
		pg := make([]pgtype.UUID, 0, len(ids))
		for _, v := range ids {
			if !live[v] {
				return apperr.Validation(FieldItemIDs) // unknown or repeated
			}
			delete(live, v)
			pg = append(pg, v.PG())
		}
		n, err := q.ReorderDailyItems(ctx, store.ReorderDailyItemsParams{Ids: pg, TayfaID: tayfa.PG()})
		if err != nil {
			return fmt.Errorf("havuz sırası: %w", err)
		}
		if n != int64(len(ids)) {
			return errors.New("havuz sırası yazılamadı")
		}
		if err := sc.Audit(ctx, AuditReordered, txn.Target{Type: "tayfa", ID: tayfa}, map[string]any{"items": len(ids)}); err != nil {
			return err
		}
		rows, err = q.ListDailyItems(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("havuz: %w", err)
		}
		out = make([]Item, 0, len(rows))
		for _, r := range rows {
			out = append(out, itemOf(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// maxAdvance bounds the attempts to move the rotation when other requests
// move it at the same time.
const maxAdvance = 5

// Today returns the item shown today (see the package comment). The first
// request of a day moves the rotation; it writes nothing else and works on
// a read-only tayfa too. Refused: anyone but an active member
// (apperr.ErrTayfaNotFound) and the module off.
func (s *Service) Today(ctx context.Context, user, tayfa id.ID) (Today, error) {
	var out Today
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		var err error
		out, err = choose(ctx, sc.Q(), tayfa, sc.Now)
		return err
	})
	if err != nil {
		return Today{}, err
	}
	return out, nil
}

// choose picks the tayfa's item of now's Europe/Istanbul day, moving the
// rotation once per day.
func choose(ctx context.Context, q *store.Queries, tayfa id.ID, now time.Time) (Today, error) {
	day := localday.Today(now)
	out := Today{Date: day}
	y, m, d := day.Date()
	pinned, err := q.GetPinnedDailyItem(ctx, store.GetPinnedDailyItemParams{
		TayfaID: tayfa.PG(), PinMonth: int2(int(m)), PinDay: int2(d), PinYear: int2(y),
	})
	switch {
	case err == nil:
		it := itemOf(pinned)
		out.Item, out.Pinned = &it, true
		return out, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Today{}, fmt.Errorf("günün sabitlemesi: %w", err)
	}

	if err := q.EnsureDailyState(ctx, store.EnsureDailyStateParams{TayfaID: tayfa.PG(), Now: ts(now)}); err != nil {
		return Today{}, fmt.Errorf("günün içeriği durumu: %w", err)
	}
	for range maxAdvance {
		st, err := q.GetDailyState(ctx, tayfa.PG())
		if err != nil {
			return Today{}, fmt.Errorf("günün içeriği durumu: %w", err)
		}
		if st.ShownOn.Valid && st.ShownItemID.Valid && sameDay(st.ShownOn.Time, day) {
			r, err := q.GetDailyItem(ctx, store.GetDailyItemParams{TayfaID: tayfa.PG(), ID: st.ShownItemID})
			switch {
			case err == nil && !r.DeletedAt.Valid && !r.PinMonth.Valid:
				it := itemOf(r)
				out.Item = &it
				return out, nil
			case err != nil && !errors.Is(err, pgx.ErrNoRows):
				return Today{}, fmt.Errorf("günün içeriği: %w", err)
			}
			// Deleted or pinned since: the rotation's next item.
		}
		next, ok, err := nextInRotation(ctx, q, tayfa, st.NextPosition)
		if err != nil {
			return Today{}, err
		}
		if !ok {
			return out, nil // nothing to show
		}
		n, err := q.AdvanceDailyState(ctx, store.AdvanceDailyStateParams{
			NextPosition: next.Position + 1, ShownOn: pgtype.Date{Time: day, Valid: true}, ShownItemID: next.ID,
			Now: ts(now), TayfaID: tayfa.PG(), OldNextPosition: st.NextPosition,
			OldShownOn: st.ShownOn, OldShownItemID: st.ShownItemID,
		})
		if err != nil {
			return Today{}, fmt.Errorf("günün içeriği durumu: %w", err)
		}
		if n == 1 {
			it := itemOf(next)
			out.Item = &it
			return out, nil
		}
		// Moved by another request meanwhile: read it again.
	}
	return Today{}, errors.New("günün içeriği seçilemedi")
}

// nextInRotation returns the first unpinned live item at or after from in
// pool order, or the first one when none is after it.
func nextInRotation(ctx context.Context, q *store.Queries, tayfa id.ID, from int32) (store.TayfaDailyItem, bool, error) {
	for _, start := range slices.Compact([]int32{from, 0}) {
		r, err := q.NextDailyRotationItem(ctx, store.NextDailyRotationItemParams{TayfaID: tayfa.PG(), FromPosition: start})
		switch {
		case err == nil:
			return r, true, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return store.TayfaDailyItem{}, false, fmt.Errorf("sıradaki içerik: %w", err)
		}
	}
	return store.TayfaDailyItem{}, false, nil
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

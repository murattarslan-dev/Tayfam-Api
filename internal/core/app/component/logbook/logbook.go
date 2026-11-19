// Package logbook is the log component (kind "log", issue #243; ADR-0025 §4,
// §5; ADR-0037 §3): entries people log during an event — a catch, a book, a
// bird, a summit. The record's fields come from the component's `fields`
// parameter in the type definition (ADR-0025 §3 field kinds); an entry may
// carry a location (the `location` parameter) and a photo (ADR-0020). It
// replaces the Faz 1 fishing catch log for every type that has the
// component. The package is not called "log" so that it does not shadow
// the standard library's.
//
// Who may do what (the permission table, package authz):
//
//	read             every active member who may see the event's components;
//	                 an entry's location only as its visibility allows
//	add              a member (not muted) whose attendance was verified
//	                 (authz.EventLogAdd), up to the `max_entries` parameter
//	remove own       the author (authz.EventLogAdd), within the write window
//	remove anyone's  moderators and admins (authz.EventLogDeleteAny), also on
//	                 cancelled, frozen and switched-off logs: moderation
//
// Changes are open within the framework's write window: until seven days
// after the event's end (ADR-0025 §5), not on a cancelled event or a
// read-only tayfa.
//
// Location (ADR-0008, ADR-0025 §4): the location is content the author
// shares knowingly. Who sees it is the entry's visibility: self (the author
// only), participants (also those who answered going or maybe, whose
// attendance was verified, the organizer, moderators and admins) or tayfa
// (every member who sees the entry). The default is the component's
// `location_default` parameter. The device's current location is never
// asked for; the client sends the point the author chose. Values and
// locations never reach a log, an audit record or an outbox event.
//
// Values are stored in event_log_entries.field_values in
// definition.CleanValues' stored forms, except the text fields bound to the
// tayfa vocabulary (`vocabulary`, issue #244; ADR-0037 §4): the client sends
// an existing value's ID (Input.VocabularyIDs) or a text, a new text is added
// to the vocabulary (package vocabulary), and the entry keeps the value's ID
// in event_log_entry_vocabulary. Such a field is returned as the text of the
// value's merge target, with its ID in Entry.VocabularyIDs. The statistics
// (`stat`, issue #266) read both.
package logbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompLog

// The component's parameters in a type definition (besides its fields).
const (
	ParamLocation        = "location"
	ParamLocationDefault = "location_default"
	ParamMaxEntries      = "max_entries"
)

// Location visibilities (event_log_entries.location_visibility;
// definition.LocationLevels).
const (
	LocationSelf         = "self"
	LocationParticipants = "participants"
	LocationTayfa        = "tayfa"
)

// DefaultMaxEntries bounds one person's entries in one log when the
// definition sets no max_entries (the Faz 1 catch log's bound).
const DefaultMaxEntries = 100

// Audit actions of the component's changes. The records name the entry
// only, never its values or location.
const (
	AuditAdded   = "event.log_entry_added"
	AuditRemoved = "event.log_entry_removed"
)

// Paging: page_size 0 means 50, above 100 means 100, negative is a
// validation error.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// Point is a WGS 84 coordinate in degrees.
type Point struct {
	Lat, Lng float64
}

func (p Point) valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

// Input is a new entry.
type Input struct {
	// Values maps a record field's key to its JSON value; every required
	// field of the log must be there, in Values or VocabularyIDs.
	Values map[string]json.RawMessage
	// VocabularyIDs maps a field bound to the tayfa vocabulary to an
	// existing value of the tayfa's vocabulary in the field's namespace.
	// Such a field takes a value here or a text in Values, not both.
	VocabularyIDs map[string]id.ID
	// Location is optional, and only for a log with the location
	// parameter.
	Location *Point
	// LocationVisibility is LocationSelf, LocationParticipants or
	// LocationTayfa; "" means the log's default. Only with a location.
	LocationVisibility string
	// PhotoUploadID is optional: the caller's own READY log_photo upload
	// opened for the tayfa. Its metadata (GPS included) was removed in
	// processing; the entry's location stays in Location.
	PhotoUploadID id.ID
	// ClientRequestID makes a repeat return the first entry.
	ClientRequestID string
}

// Entry is one entry of a log, as the caller sees it.
type Entry struct {
	ID     id.ID
	UserID id.ID
	// Values are the stored values by field key; a field bound to the
	// vocabulary is the text of its value's merge target, a JSON string.
	Values map[string]json.RawMessage
	// VocabularyIDs are the vocabulary values of the fields bound to the
	// vocabulary, resolved to their merge targets; nil when there is none.
	VocabularyIDs map[string]id.ID
	// Location is nil when the entry has none or the caller may not see
	// it.
	Location *Point
	// LocationVisibility is set on the caller's own entries that have a
	// location; "" otherwise.
	LocationVisibility string
	// PhotoUploadID is zero when there is no photo.
	PhotoUploadID id.ID
	CreatedAt     time.Time
	// CallerCanRemove reports whether the caller may remove the entry now.
	CallerCanRemove bool
}

// Page is a page of a log's entries, oldest first, with the log's settings.
type Page struct {
	Entries       []Entry
	NextPageToken string
	// LocationEnabled and LocationDefault are the component's parameters.
	LocationEnabled bool
	LocationDefault string
	// MaxEntries bounds one person's entries in this log.
	MaxEntries int32
	// CallerCanAdd reports whether the caller may add an entry now (the
	// write window, the permission and a verified attendance; not the
	// count).
	CallerCanAdd bool
}

// Service is the log component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// locationDefault is the log's default visibility.
func locationDefault(sc *component.Scope) string {
	if v := sc.ParamString(ParamLocationDefault); v != "" {
		return v
	}
	return LocationSelf
}

// maxEntries is the log's per-person bound.
func maxEntries(sc *component.Scope) int64 {
	if n, ok := sc.ParamInt(ParamMaxEntries); ok && n > 0 {
		return n
	}
	return DefaultMaxEntries
}

// writable reports whether the event, the tayfa and the write window allow
// changes now: the checks component.Service.Change makes.
func writable(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// participant reports whether the caller sees participants-only locations:
// an answer of going or maybe, a verified attendance, the organizer, or a
// moderator or admin. This is a read, so a mute does not hide it.
func participant(ctx context.Context, sc *component.Scope) (bool, error) {
	if sc.Organizer != (id.ID{}) && sc.Organizer == sc.User {
		return true, nil
	}
	if sc.IsManager() {
		return true, nil
	}
	r, err := sc.Q().GetEventParticipation(ctx, store.GetEventParticipationParams{EventID: sc.Event.PG(), UserID: sc.User.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("katılım: %w", err)
	}
	return r.Status == "going" || r.Status == "maybe" || r.Attendance == "attended", nil
}

// row is an entry as read from the store.
type row struct {
	ID                 pgtype.UUID
	UserID             pgtype.UUID
	FieldValues        []byte
	Lat                pgtype.Float8
	Lng                pgtype.Float8
	LocationVisibility pgtype.Text
	PhotoUploadID      pgtype.UUID
	CreatedAt          pgtype.Timestamptz
}

// resolved is a vocabulary field of an entry: its value's merge target.
type resolved struct {
	id    id.ID
	value string
}

// vocabularyOf reads the vocabulary fields of the entries, by entry and
// field key. The entries were read in the caller's event and component.
func vocabularyOf(ctx context.Context, sc *component.Scope, entries []pgtype.UUID) (map[id.ID]map[string]resolved, error) {
	out := map[id.ID]map[string]resolved{}
	if len(entries) == 0 {
		return out, nil
	}
	rows, err := sc.Q().ListLogEntryVocabulary(ctx, entries)
	if err != nil {
		return nil, fmt.Errorf("günlük kayıtlarının sözlük değerleri: %w", err)
	}
	for _, r := range rows {
		e := id.ID(r.EntryID.Bytes)
		if out[e] == nil {
			out[e] = map[string]resolved{}
		}
		out[e][r.FieldKey] = resolved{id: id.ID(r.VocabularyID.Bytes), value: r.Value}
	}
	return out, nil
}

// entryOf converts r for the caller; seesParticipants is whether the caller
// sees participants-only locations, voc the entry's vocabulary fields.
func entryOf(sc *component.Scope, r row, voc map[string]resolved, seesParticipants bool) (Entry, error) {
	e := Entry{ID: id.ID(r.ID.Bytes), UserID: id.ID(r.UserID.Bytes), CreatedAt: r.CreatedAt.Time.UTC()}
	if err := json.Unmarshal(r.FieldValues, &e.Values); err != nil {
		return Entry{}, fmt.Errorf("günlük kaydının değerleri: %w", err)
	}
	if e.Values == nil {
		e.Values = map[string]json.RawMessage{}
	}
	for k, v := range voc {
		text, err := json.Marshal(v.value)
		if err != nil {
			return Entry{}, fmt.Errorf("günlük kaydının sözlük değeri: %w", err)
		}
		e.Values[k] = text
		if e.VocabularyIDs == nil {
			e.VocabularyIDs = map[string]id.ID{}
		}
		e.VocabularyIDs[k] = v.id
	}
	if r.PhotoUploadID.Valid {
		e.PhotoUploadID = id.ID(r.PhotoUploadID.Bytes)
	}
	own := e.UserID == sc.User
	if r.Lat.Valid && r.Lng.Valid {
		vis := r.LocationVisibility.String
		if own || vis == LocationTayfa || (vis == LocationParticipants && seesParticipants) {
			e.Location = &Point{Lat: r.Lat.Float64, Lng: r.Lng.Float64}
		}
		if own {
			e.LocationVisibility = vis
		}
	}
	if own {
		e.CallerCanRemove = writable(sc) && sc.Authorize(authz.EventLogAdd) == nil
	} else {
		e.CallerCanRemove = !access.ReadOnly(sc.TayfaRow()) && sc.Authorize(authz.EventLogDeleteAny) == nil
	}
	return e, nil
}

// List returns a page of the log's entries, oldest first. Refusals are
// those of component.Service.View and a bad page size or token
// (validation error).
func (s *Service) List(ctx context.Context, ref component.Ref, size int32, token string) (Page, error) {
	size, err := pageSize(size)
	if err != nil {
		return Page{}, err
	}
	scope := ref.Event.String() + "/" + ref.Key
	after, at, err := decodeCursor(token, ref.Tayfa, scope)
	if err != nil {
		return Page{}, err
	}
	var out Page
	err = s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		out = Page{
			Entries: []Entry{}, LocationEnabled: sc.ParamBool(ParamLocation), MaxEntries: int32(maxEntries(sc)), //nolint:gosec // at most 500 (definition)
		}
		if out.LocationEnabled {
			out.LocationDefault = locationDefault(sc)
		}
		if writable(sc) && sc.Authorize(authz.EventLogAdd) == nil {
			attended, err := sc.Attended(ctx)
			if err != nil {
				return err
			}
			out.CallerCanAdd = attended
		}
		sees, err := participant(ctx, sc)
		if err != nil {
			return err
		}
		rows, err := sc.Q().ListLogEntries(ctx, store.ListLogEntriesParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, AfterTime: ts(at), AfterID: after.PG(), PageSize: size + 1,
		})
		if err != nil {
			return fmt.Errorf("günlük: %w", err)
		}
		if len(rows) > int(size) {
			rows = rows[:size]
			last := rows[len(rows)-1]
			out.NextPageToken = encodeCursor(ref.Tayfa, scope, last.CreatedAt.Time, id.ID(last.ID.Bytes))
		}
		ids := make([]pgtype.UUID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		voc, err := vocabularyOf(ctx, sc, ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			e, err := entryOf(sc, row(r), voc[id.ID(r.ID.Bytes)], sees)
			if err != nil {
				return err
			}
			out.Entries = append(out.Entries, e)
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return out, nil
}

// link is a field of a new entry bound to the vocabulary.
type link struct {
	key   string
	value id.ID
}

// cleanEntry checks the values against the log's record fields and returns
// the stored values and the vocabulary fields. Known texts of vocabulary
// fields are looked up and new ones added to the tayfa's vocabulary, in
// the caller's transaction, after every other check.
func cleanEntry(ctx context.Context, sc *component.Scope, in Input) ([]byte, []link, error) {
	plain := make(map[string]json.RawMessage, len(in.Values))
	for k, v := range in.Values {
		plain[k] = v
	}
	type pending struct {
		field definition.Field
		text  string
		id    id.ID
	}
	var (
		fields []definition.Field
		vocab  []pending
	)
	for _, f := range sc.Component.LogFields {
		if f.Vocabulary == "" {
			fields = append(fields, f)
			continue
		}
		raw, hasText := plain[f.Key]
		v, hasID := in.VocabularyIDs[f.Key]
		delete(plain, f.Key)
		switch {
		case hasText && hasID:
			return nil, nil, apperr.Validation("vocabulary_ids." + f.Key)
		case hasID:
			vocab = append(vocab, pending{field: f, id: v})
		case hasText:
			clean, err := f.Clean(raw)
			if err != nil {
				return nil, nil, apperr.Validation("values." + f.Key)
			}
			var text string
			if err := json.Unmarshal(clean, &text); err != nil {
				return nil, nil, apperr.Validation("values." + f.Key)
			}
			vocab = append(vocab, pending{field: f, text: text})
		case f.Required:
			return nil, nil, apperr.Validation("values." + f.Key)
		}
	}
	for k := range in.VocabularyIDs {
		if !slices.ContainsFunc(sc.Component.LogFields, func(f definition.Field) bool { return f.Key == k && f.Vocabulary != "" }) {
			return nil, nil, apperr.Validation("vocabulary_ids")
		}
	}
	values, err := cleanValues(fields, plain)
	if err != nil {
		return nil, nil, err
	}
	links := make([]link, 0, len(vocab))
	for _, p := range vocab {
		if p.id != (id.ID{}) {
			if err := vocabulary.Lookup(ctx, sc.Q(), sc.Tayfa, p.field.Vocabulary, p.id, "vocabulary_ids."+p.field.Key); err != nil {
				return nil, nil, err
			}
			links = append(links, link{key: p.field.Key, value: p.id})
			continue
		}
		v, err := vocabulary.Ensure(ctx, sc.Q(), sc.Tayfa, sc.User, p.field.Vocabulary, p.text, p.field.MaxLength, sc.Now, "values."+p.field.Key)
		if err != nil {
			return nil, nil, err
		}
		links = append(links, link{key: p.field.Key, value: v})
	}
	return values, links, nil
}

// cleanValues checks the values against the log's record fields that are
// not bound to the vocabulary.
func cleanValues(fields []definition.Field, in map[string]json.RawMessage) ([]byte, error) {
	def := &definition.Definition{Fields: fields}
	values, err := def.CleanValues(in, true)
	var ve *definition.ValueError
	switch {
	case errors.As(err, &ve) && ve.Field != "":
		return nil, apperr.Validation("values." + ve.Field)
	case err != nil:
		return nil, apperr.Validation("values")
	}
	b, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("günlük kaydının değerleri: %w", err)
	}
	return b, nil
}

// Add logs an entry; the caller must have a verified attendance. A repeat
// with the same ClientRequestID returns the first entry. Refused: a bad
// value (validation error on values.<field>; vocabulary_ids.<field> for a
// value that is not in the tayfa's vocabulary of the field's namespace or
// a field given twice; vocabulary_ids for a field not bound to it;
// location, location_visibility, client_request_id), a location on a log without
// one (validation error on location), an unverified attendance
// (apperr.ErrNotAttended), the caller's full log (apperr.ErrEventLimit), a
// photo that is not the caller's ready log photo of the tayfa (the media
// errors) and the refusals of component.Service.Change.
func (s *Service) Add(ctx context.Context, ref component.Ref, in Input) (Entry, error) {
	if len(in.Values) > definition.MaxLogFields {
		return Entry{}, apperr.Validation("values")
	}
	if len(in.VocabularyIDs) > definition.MaxLogFields {
		return Entry{}, apperr.Validation("vocabulary_ids")
	}
	if in.Location != nil && !in.Location.valid() {
		return Entry{}, apperr.Validation("location")
	}
	if in.LocationVisibility != "" && (in.Location == nil || !slices.Contains(definition.LocationLevels, in.LocationVisibility)) {
		return Entry{}, apperr.Validation("location_visibility")
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Entry{}, apperr.Validation("client_request_id")
	}
	var out Entry
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventLogAdd), func(sc *component.Scope) error {
		prev, err := sc.Q().GetLogEntryByClientRequest(ctx, store.GetLogEntryByClientRequestParams{UserID: sc.User.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid || id.ID(prev.EventID.Bytes) != sc.Event || prev.ComponentKey != sc.Key {
				return apperr.Validation("client_request_id")
			}
			r, err := sc.Q().GetLogEntry(ctx, store.GetLogEntryParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: prev.ID})
			if err != nil {
				return fmt.Errorf("günlük kaydı: %w", err)
			}
			voc, err := vocabularyOf(ctx, sc, []pgtype.UUID{prev.ID})
			if err != nil {
				return err
			}
			out, err = entryOf(sc, row(r), voc[id.ID(prev.ID.Bytes)], true)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		values, links, err := cleanEntry(ctx, sc, in)
		if err != nil {
			return err
		}
		p := store.CreateLogEntryParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(), FieldValues: values,
			ClientRequestID: crid.PG(), CreatedAt: ts(sc.Now),
		}
		if in.Location != nil {
			if !sc.ParamBool(ParamLocation) {
				return apperr.Validation("location")
			}
			vis := in.LocationVisibility
			if vis == "" {
				vis = locationDefault(sc)
			}
			p.Lat = pgtype.Float8{Float64: in.Location.Lat, Valid: true}
			p.Lng = pgtype.Float8{Float64: in.Location.Lng, Valid: true}
			p.LocationVisibility = pgtype.Text{String: vis, Valid: true}
		}
		attended, err := sc.Attended(ctx)
		if err != nil {
			return err
		}
		if !attended {
			return apperr.ErrNotAttended
		}
		n, err := sc.Q().CountUserLogEntries(ctx, store.CountUserLogEntriesParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
		})
		if err != nil {
			return fmt.Errorf("günlük kayıtları: %w", err)
		}
		if n >= maxEntries(sc) {
			return apperr.ErrEventLimit
		}
		entry := id.New()
		p.ID = entry.PG()
		if in.PhotoUploadID != (id.ID{}) {
			// Attached first; the column's trigger checks it again.
			if err := media.Attach(ctx, sc.Q(), sc.User, in.PhotoUploadID, media.PurposeLogPhoto, sc.Tayfa,
				media.Target{Type: media.TargetLogEntry, ID: entry}, sc.Now); err != nil {
				return err
			}
			p.PhotoUploadID = in.PhotoUploadID.PG()
		}
		if err := sc.Q().CreateLogEntry(ctx, p); err != nil {
			return fmt.Errorf("günlük kaydı: %w", err)
		}
		for _, l := range links {
			if err := sc.Q().CreateLogEntryVocabulary(ctx, store.CreateLogEntryVocabularyParams{
				EntryID: p.ID, FieldKey: l.key, VocabularyID: l.value.PG(),
			}); err != nil {
				return fmt.Errorf("günlük kaydının sözlük değeri: %w", err)
			}
		}
		voc, err := vocabularyOf(ctx, sc, []pgtype.UUID{p.ID})
		if err != nil {
			return err
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditAdded, map[string]any{"entry_id": entry.String()}); err != nil {
			return err
		}
		out, err = entryOf(sc, row{
			ID: p.ID, UserID: p.UserID, FieldValues: values, Lat: p.Lat, Lng: p.Lng,
			LocationVisibility: p.LocationVisibility, PhotoUploadID: p.PhotoUploadID, CreatedAt: p.CreatedAt,
		}, voc[entry], true)
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}

// Remove removes an entry (soft delete; kept for 30 days, then purged with
// its photo). The author removes their own within the write window
// (authz.EventLogAdd); moderators and admins remove anyone's whatever the
// event's state (authz.EventLogDeleteAny), as moderation. Refused: an
// unknown entry (apperr.ErrEventItemNotFound) and the refusals of
// component.Service.View and Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, entry id.ID) error {
	var author id.ID
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		r, err := sc.Q().GetLogEntry(ctx, store.GetLogEntryParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: entry.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("günlük kaydı: %w", err)
		}
		author = id.ID(r.UserID.Bytes)
		return nil
	})
	if errors.Is(err, apperr.ErrComponentDisabled) {
		// A switched-off log still has entries moderators may remove; the
		// author's own removal is refused below by Change.
		author = id.ID{}
	} else if err != nil {
		return err
	}
	who := component.BySafetySignal(authz.EventLogDeleteAny)
	if author == ref.User {
		who = component.ByAction(authz.EventLogAdd)
	}
	return s.fw.Change(ctx, ref, Kind, who, func(sc *component.Scope) error {
		n, err := sc.Q().DeleteLogEntry(ctx, store.DeleteLogEntryParams{
			DeletedAt: ts(sc.Now), DeletedBy: sc.User.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: entry.PG(),
		})
		if err != nil {
			return fmt.Errorf("günlük kaydı: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		sc.Changed()
		return sc.Audit(ctx, AuditRemoved, map[string]any{"entry_id": entry.String()})
	})
}

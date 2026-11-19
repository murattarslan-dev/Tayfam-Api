// Package event manages a tayfa's events: creating, editing and cancelling
// them, RSVPs with a waitlist and camp item lists
// (EventService, proto/tayfam/core/v1/event.proto; issue #49, data model
// v14 "Duyuru ve Etkinlik", "Türe özel tablolar", "Etkinlik iptali",
// "Online etkinlik", "Konvoy aracı").
//
// Every method starts the same way: the caller comes from the verified
// token, access.Load checks for an active membership (anyone else gets
// apperr.ErrTayfaNotFound), the events module must be on
// (apperr.ErrModuleDisabled), and the permission table decides the rest.
// Events are always loaded by tayfa_id and event ID together (IDOR).
//
// Writes run in txn.WithActor with the tayfa row locked; changes to an
// event also lock its row (SELECT … FOR UPDATE), so capacity, the waitlist
// and the event's state cannot change between the check and the write.
// Each write records an audit log row in the same transaction and, as the
// last step, its outbox events with txn.Event (outbox.Write; ADR-0002).
// Audit records and events hold IDs and field names only: never titles,
// descriptions, locations, URLs, plates or reasons.
package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Outbox event types (issue #49). Payloads hold IDs and field names only.
const (
	EventCreated   = "event.created"
	EventUpdated   = "event.updated"
	EventCancelled = "event.cancelled"
	RSVPChanged    = "rsvp.changed"
	RSVPPromoted   = "rsvp.promoted"
)

// Update mask paths (UpdateEventRequest.update_mask), in audit order.
const (
	FieldTitle        = "title"
	FieldDescription  = "description"
	FieldTime         = "time"
	FieldLocation     = "location"
	FieldCapacity     = "capacity"
	FieldDiscoverable = "discoverable"
	FieldDetails      = "details"
	// FieldFields replaces the values of the type's fields
	// (event_field_values, issue #237).
	FieldFields = "fields"
	// FieldCover is the cover image (events.cover_upload_id, issue #135).
	FieldCover = "cover_upload_id"
)

var updateFields = []string{FieldTitle, FieldDescription, FieldTime, FieldLocation, FieldCapacity, FieldDiscoverable, FieldDetails, FieldFields, FieldCover}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time
}

// Service implements EventService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("event: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Event is an event as an active member of its tayfa sees it.
type Event struct {
	ID      id.ID
	TayfaID id.ID
	// Type is the catalog key of the event's type; TypeVersion the version
	// of the type the event was created with (ADR-0025 §2).
	Type        string
	TypeVersion int32
	Title       string
	Description string
	StartsAt    time.Time
	EndsAt      time.Time
	Location
	// Capacity 0 means no limit.
	Capacity     int32
	Status       string
	CancelledAt  time.Time
	CancelReason string
	Discoverable bool
	// CreatedBy is the organizer; zero if their account was deleted.
	CreatedBy id.ID
	CreatedAt time.Time
	UpdatedAt time.Time

	Going, Maybe, Waitlist int32
	// CallerRSVP is the caller's answer; "" if none.
	CallerRSVP string
	// CallerAttendance is the caller's verified attendance:
	// AttendanceUnknown without an answer.
	CallerAttendance string
	// SeriesID is the event's series; zero for a single event.
	SeriesID id.ID
	// SeriesOverride: the event was edited on its own; series changes
	// leave it alone.
	SeriesOverride bool
	// CallerCanManage tells whether the caller may edit and cancel.
	CallerCanManage bool
	// CoverUploadID is the cover image; zero when there is none.
	CoverUploadID id.ID
	// Details are filled by Get only.
	Details Details
	// Fields holds the values of the type's fields by field key, as JSON
	// (definition.CleanValues); filled by Get only, never nil there.
	Fields map[string]json.RawMessage
}

// Input is a create request, and the new values of an update.
type Input struct {
	Type         string
	Title        string
	Description  string
	StartsAt     time.Time
	EndsAt       time.Time
	Location     Location
	Capacity     int32
	Discoverable bool
	Details      Details
	// Fields holds the values of the type's fields by field key, as JSON.
	// They are checked against the event's type version.
	Fields map[string]json.RawMessage
	// ClientRequestID is required on create and ignored on update.
	ClientRequestID string
	// Recurrence, on create only, makes the event the first of a series.
	Recurrence *Recurrence
	// Series, on update only, applies the change to the event's series
	// (UpdateEventRequest.series).
	Series bool
	// CoverUploadID is the caller's own READY cover upload opened for the
	// tayfa; on update zero removes the cover (issue #135).
	CoverUploadID id.ID
	// Components, on create only, are the organizer's selections: optional
	// components on or off and the parameters the type leaves to them
	// (issue #238, ADR-0037 §2). Later changes go through
	// component.Service.Set.
	Components []component.Selection
}

// setCover attaches the caller's cover upload to the event (zero: removes
// the cover) and writes the column, whose trigger checks the attachment
// again; the previous cover goes to the delete queue.
func setCover(ctx context.Context, q *store.Queries, c caller, tayfa, event, upload id.ID) error {
	cover := pgtype.UUID{}
	if upload != (id.ID{}) {
		if err := media.Attach(ctx, q, c.user, upload, media.PurposeCover, tayfa,
			media.Target{Type: media.TargetEvent, ID: event}, c.now); err != nil {
			return err
		}
		cover = upload.PG()
	}
	if err := q.SetEventCover(ctx, store.SetEventCoverParams{ID: event.PG(), TayfaID: tayfa.PG(), CoverUploadID: cover}); err != nil {
		return fmt.Errorf("kapak: %w", err)
	}
	return nil
}

// caller is the verified caller's standing in a tayfa, loaded inside the
// request's transaction.
type caller struct {
	user     id.ID
	tayfa    access.Tayfa
	m        authz.Membership
	settings authz.Settings
	now      time.Time
}

func (c caller) authorize(a authz.Action) error {
	return access.AuthorizeIn(c.m, a, c.now, c.settings)
}

// manage checks an action on ev that its organizer may perform with own
// and anyone else with anyAction. Organizers whose role is high enough for
// anyAction need nothing else.
func (c caller) manage(ev store.GetEventRow, own, anyAction authz.Action) error {
	if c.authorize(anyAction) == nil {
		return nil
	}
	if ev.CreatedBy.Valid && id.ID(ev.CreatedBy.Bytes) == c.user {
		return c.authorize(own)
	}
	return c.authorize(anyAction)
}

// canManage is manage for the view flag; a mute counts.
func (c caller) canManage(ev store.GetEventRow) bool {
	return c.manage(ev, authz.EventEditOwn, authz.EventEditAny) == nil
}

// load loads the caller's standing (access.Load) and checks that the events
// module is on.
func (s *Service) load(ctx context.Context, q *store.Queries, user, tayfa id.ID, lock bool) (caller, error) {
	t, m, err := access.Load(ctx, q, tayfa, user, lock)
	if err != nil {
		return caller{}, err
	}
	if err := access.RequireModule(ctx, q, tayfa, access.ModuleEvents); err != nil {
		return caller{}, err
	}
	return caller{user: user, tayfa: t, m: m, settings: access.Settings(t), now: s.now()}, nil
}

// batch collects a write's outbox events; write writes them with txn.Event
// as the last step of the transaction (outbox.Write ordering).
type batch struct {
	tayfa  id.ID
	actor  id.ID
	events []queued
}

type queued struct {
	typ     string
	subject id.ID
	payload map[string]any
}

// add queues an event about the event subject. The payload also names the
// tayfa, like membership.changed.
func (b *batch) add(typ string, subject id.ID, payload map[string]any) {
	p := map[string]any{"tayfa_id": b.tayfa.String()}
	for k, v := range payload {
		p[k] = v
	}
	b.events = append(b.events, queued{typ: typ, subject: subject, payload: p})
}

type writeFunc func(q *store.Queries, c caller, b *batch) error

// write runs fn for an active member of a tayfa whose events module is on
// and which is not read-only, in one transaction with the tayfa locked,
// then writes the queued outbox events.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, fn writeFunc) error {
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		c, err := s.load(ctx, q, user, tayfa, true)
		if err != nil {
			return err
		}
		if access.ReadOnly(c.tayfa) {
			return apperr.ErrTayfaReadOnly
		}
		b := &batch{tayfa: tayfa, actor: user}
		if err := fn(q, c, b); err != nil {
			return err
		}
		return flush(ctx, q, b)
	})
}

// flush writes a write's queued outbox events and then their notification
// requests (outbox.Write ordering).
func flush(ctx context.Context, q *store.Queries, b *batch) error {
	for _, e := range b.events {
		if err := txn.Event(ctx, q, e.typ, b.tayfa, b.actor, txn.Target{Type: "event", ID: e.subject}, e.payload); err != nil {
			return err
		}
	}
	return notifyBatch(ctx, q, b)
}

// notifyBatch writes the notification requests of a write's events, after
// them (ADR-0009, package notify): a new event for every active member,
// a change or cancellation for its going, maybe and waitlist participants,
// a waitlist promotion for the promoted member. The actor never hears
// about their own change. Changes to the lists on an event (camp items)
// notify nobody.
func notifyBatch(ctx context.Context, q *store.Queries, b *batch) error {
	for _, e := range b.events {
		req := notify.Request{Tayfa: b.tayfa, Actor: b.actor, TargetType: notify.TargetEvent, Target: e.subject}
		var (
			to  []id.ID
			err error
		)
		switch e.typ {
		case EventCreated:
			req.Type = notify.TypeEventCreated
			to, err = notify.Members(ctx, q, b.tayfa, b.actor)
		case EventUpdated:
			if listsOnly(e.payload["fields"]) {
				continue
			}
			req.Type = notify.TypeEventUpdated
			to, err = notify.Participants(ctx, q, b.tayfa, e.subject, b.actor)
		case EventCancelled:
			req.Type = notify.TypeEventCancelled
			to, err = notify.Participants(ctx, q, b.tayfa, e.subject, b.actor)
		case RSVPPromoted:
			req.Type = notify.TypeRSVPPromoted
			u, _ := e.payload["user_id"].(string)
			promoted, ok := id.Parse(u)
			if !ok {
				return errors.New("terfi olayında kullanıcı yok")
			}
			to, err = notify.Only(ctx, q, b.tayfa, []id.ID{promoted}, b.actor)
		default:
			continue
		}
		if err != nil {
			return err
		}
		if err := notify.Write(ctx, q, req, to); err != nil {
			return err
		}
	}
	return nil
}

// listsOnly reports an event.updated whose fields are all lists on the
// event (fieldCampItems).
func listsOnly(v any) bool {
	fields, ok := v.([]string)
	if !ok || len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != fieldCampItems {
			return false
		}
	}
	return true
}

// lockEvent loads and locks an event of the tayfa.
func lockEvent(ctx context.Context, q *store.Queries, tayfa, event id.ID) (store.GetEventRow, error) {
	r, err := q.GetEventForUpdate(ctx, store.GetEventForUpdateParams{TayfaID: tayfa.PG(), ID: event.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetEventRow{}, apperr.ErrEventNotFound
	}
	if err != nil {
		return store.GetEventRow{}, fmt.Errorf("etkinlik: %w", err)
	}
	return store.GetEventRow(r), nil
}

func getEvent(ctx context.Context, q *store.Queries, tayfa, event id.ID) (store.GetEventRow, error) {
	r, err := q.GetEvent(ctx, store.GetEventParams{TayfaID: tayfa.PG(), ID: event.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetEventRow{}, apperr.ErrEventNotFound
	}
	if err != nil {
		return store.GetEventRow{}, fmt.Errorf("etkinlik: %w", err)
	}
	return r, nil
}

// changeable refuses changes to a cancelled or ended event.
func changeable(ev store.GetEventRow, now time.Time) error {
	if ev.Status == StatusCancelled {
		return apperr.ErrEventCancelled
	}
	if !ev.EndsAt.Time.After(now) {
		return apperr.ErrEventEnded
	}
	return nil
}

// audit writes the audit record of a change to an event.
func audit(ctx context.Context, q *store.Queries, c caller, action string, event id.ID, meta map[string]any) error {
	return txn.Audit(ctx, q, id.ID(c.tayfa.ID.Bytes), c.user, action, txn.Target{Type: "event", ID: event}, meta)
}

// ---------------------------------------------------------------------------
// Create, update, cancel

// Plan is a validated create request (Prepare).
type Plan struct {
	in       Input
	title    string
	desc     string
	start    time.Time
	end      time.Time
	loc      Location
	capacity int32
	details  Details
	crid     id.ID
	rec      *recurrence
}

// Title is the cleaned title: the name of an instant event's tayfa
// (ADR-0032 §2).
func (p Plan) Title() string { return p.title }

// ClientRequestID is the request's retry key.
func (p Plan) ClientRequestID() id.ID { return p.crid }

// Prepare validates a create request.
func (s *Service) Prepare(in Input) (Plan, error) {
	// Whether the catalog has the type is checked in the transaction that
	// creates the event (createIn).
	if !definition.ValidKey(in.Type) {
		return Plan{}, apperr.Validation("type")
	}
	if len(in.Fields) > definition.MaxFields {
		return Plan{}, apperr.Validation("fields")
	}
	if len(in.Components) > definition.MaxComponents {
		return Plan{}, apperr.Validation("components")
	}
	p := Plan{in: in}
	var err error
	if p.title, err = cleanText(in.Title, "title", 1, titleMax, false); err != nil {
		return Plan{}, err
	}
	if p.desc, err = cleanText(in.Description, "description", 0, descriptionMax, true); err != nil {
		return Plan{}, err
	}
	if p.start, p.end, err = cleanTime(in.StartsAt, in.EndsAt, s.now()); err != nil {
		return Plan{}, err
	}
	if p.loc, err = cleanLocation(in.Location); err != nil {
		return Plan{}, err
	}
	if p.capacity, err = cleanCapacity(in.Capacity); err != nil {
		return Plan{}, err
	}
	if p.details, err = cleanDetails(in.Type, in.Details, p.start, true); err != nil {
		return Plan{}, err
	}
	var ok bool
	if p.crid, ok = id.Parse(in.ClientRequestID); !ok {
		return Plan{}, apperr.Validation("client_request_id")
	}
	if p.rec, err = cleanRecurrence(in.Recurrence, p.start); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Create creates an event with user as organizer. A repeat with the same
// ClientRequestID returns the first event.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in Input) (Event, error) {
	p, err := s.Prepare(in)
	if err != nil {
		return Event{}, err
	}
	var eventID, seriesID id.ID
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCreate); err != nil {
			return err
		}
		prev, err := q.GetEventByClientRequest(ctx, store.GetEventByClientRequestParams{CreatedBy: user.PG(), ClientRequestID: p.crid.PG()})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid || id.ID(prev.TayfaID.Bytes) != tayfa {
				return apperr.Validation("client_request_id")
			}
			eventID = id.ID(prev.ID.Bytes)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		eventID, seriesID, err = createIn(ctx, q, c, b, p)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	if seriesID != (id.ID{}) {
		// The first occurrences right away; the generator retries on
		// failure, so the request does not fail for it.
		if _, err := s.GenerateSeries(ctx, tayfa, seriesID); err != nil {
			s.cfg.Log.WarnContext(ctx, "seri örnekleri üretilemedi", slog.String("seri", seriesID.String()), slog.String("hata", err.Error()))
		}
	}
	return s.Get(ctx, user, tayfa, eventID)
}

// CreateInstantTx creates the single event of a new instant tayfa inside
// the caller's transaction q (package instant, ADR-0032 §2), with user as
// organizer, and writes its outbox events. The caller created the tayfa in
// the same transaction with user as its only admin; EventCreate is not
// asked, because the permission table refuses it in instant tayfas for
// every later event. A recurrence or a cover is refused.
func (s *Service) CreateInstantTx(ctx context.Context, q *store.Queries, user, tayfa id.ID, p Plan) (id.ID, error) {
	if p.rec != nil {
		return id.ID{}, apperr.Validation("recurrence")
	}
	if p.in.CoverUploadID != (id.ID{}) {
		return id.ID{}, apperr.Validation("cover_upload_id")
	}
	if p.in.Discoverable {
		return id.ID{}, apperr.Validation("discoverable")
	}
	c, err := s.load(ctx, q, user, tayfa, true)
	if err != nil {
		return id.ID{}, err
	}
	if !access.Instant(c.tayfa) || access.ReadOnly(c.tayfa) {
		return id.ID{}, apperr.ErrPermissionDenied
	}
	b := &batch{tayfa: tayfa, actor: user}
	eventID, _, err := createIn(ctx, q, c, b, p)
	if err != nil {
		return id.ID{}, err
	}
	return eventID, flush(ctx, q, b)
}

// createIn writes a new event (and, with a recurrence, its series) of the
// caller's tayfa and queues its outbox events.
func createIn(ctx context.Context, q *store.Queries, c caller, b *batch, p Plan) (eventID, seriesID id.ID, err error) {
	tayfa := id.ID(c.tayfa.ID.Bytes)
	// The event is bound to the type's current version and checked against
	// it (ADR-0025 §2).
	et, err := currentType(ctx, q, p.in.Type, p.loc.Mode)
	if err != nil {
		return id.ID{}, id.ID{}, err
	}
	values, err := cleanFields(et, p.in.Fields)
	if err != nil {
		return id.ID{}, id.ID{}, err
	}
	eventID = id.New()
	cp := store.CreateEventParams{
		ID: eventID.PG(), TayfaID: tayfa.PG(), Type: et.Key, TypeVersion: et.Version, Title: p.title, Description: text(p.desc),
		StartsAt: ts(p.start), EndsAt: ts(p.end), Capacity: int4(p.capacity), Discoverable: p.in.Discoverable,
		CreatedBy: c.user.PG(), ClientRequestID: p.crid.PG(),
	}
	setLocation(&cp.Mode, &cp.MeetingPointName, &cp.MeetingLat, &cp.MeetingLng, &cp.City, &cp.District, &cp.OnlineUrl, p.loc)
	if err := q.CreateEvent(ctx, cp); err != nil {
		return id.ID{}, id.ID{}, fmt.Errorf("etkinlik: %w", err)
	}
	if err := setDetails(ctx, q, eventID, p.details); err != nil {
		return id.ID{}, id.ID{}, err
	}
	if err := setFields(ctx, q, eventID, values, false); err != nil {
		return id.ID{}, id.ID{}, err
	}
	// The organizer's selections, checked against the same type version.
	if err := component.Apply(ctx, q, et, eventID, p.in.Components); err != nil {
		return id.ID{}, id.ID{}, err
	}
	if p.in.CoverUploadID != (id.ID{}) {
		if err := setCover(ctx, q, c, tayfa, eventID, p.in.CoverUploadID); err != nil {
			return id.ID{}, id.ID{}, err
		}
	}
	if err := audit(ctx, q, c, EventCreated, eventID, map[string]any{"type": p.in.Type}); err != nil {
		return id.ID{}, id.ID{}, err
	}
	payload := map[string]any{"event_id": eventID.String(), "type": p.in.Type}
	if p.rec != nil {
		if seriesID, err = createSeries(ctx, q, c, eventID, p.start, p.rec); err != nil {
			return id.ID{}, id.ID{}, err
		}
		payload["series_id"] = seriesID.String()
	}
	b.add(EventCreated, eventID, payload)
	return eventID, seriesID, txn.TouchTayfa(ctx, q, tayfa, c.now)
}

// Update changes the fields named in in.Fields (update mask paths).
func (s *Service) Update(ctx context.Context, user, tayfa, event id.ID, fields []string, in Input) (Event, error) {
	if len(fields) == 0 {
		return Event{}, apperr.Validation("update_mask")
	}
	set := map[string]bool{}
	for _, f := range fields {
		if !slices.Contains(updateFields, f) {
			return Event{}, apperr.Validation("update_mask")
		}
		set[f] = true
	}
	var (
		title, desc string
		start, end  time.Time
		loc         Location
		capacity    int32
		err         error
	)
	if set[FieldTitle] {
		if title, err = cleanText(in.Title, "title", 1, titleMax, false); err != nil {
			return Event{}, err
		}
	}
	if set[FieldDescription] {
		if desc, err = cleanText(in.Description, "description", 0, descriptionMax, true); err != nil {
			return Event{}, err
		}
	}
	if set[FieldTime] {
		if start, end, err = cleanTime(in.StartsAt, in.EndsAt, s.now()); err != nil {
			return Event{}, err
		}
	}
	if set[FieldLocation] {
		if loc, err = cleanLocation(in.Location); err != nil {
			return Event{}, err
		}
	}
	if set[FieldCapacity] {
		if capacity, err = cleanCapacity(in.Capacity); err != nil {
			return Event{}, err
		}
	}
	if in.Series && (set[FieldTime] || set[FieldCover]) {
		// A series' times come from its rule; an upload covers one event.
		return Event{}, apperr.Validation("update_mask")
	}
	var changed []string
	for _, f := range updateFields {
		if set[f] {
			changed = append(changed, f)
		}
	}

	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := c.manage(ev, authz.EventEditOwn, authz.EventEditAny); err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		if set[FieldLocation] {
			// The mode must be one the event's type version allows.
			if err := modeAllowed(ctx, q, ev, loc.Mode); err != nil {
				return err
			}
		}
		if in.Series {
			if !ev.SeriesID.Valid {
				return apperr.Validation("series")
			}
			return updateSeries(ctx, q, c, b, ev, set, changed, in, loc, title, desc, capacity)
		}
		if ev.SeriesID.Valid {
			// Edited alone: the series stops copying this event.
			sr, err := lockSeries(ctx, q, tayfa, ev.SeriesID)
			if err != nil {
				return err
			}
			if err := retemplate(ctx, q, sr, event, c.now); err != nil {
				return err
			}
		}
		p := store.UpdateEventParams{
			TayfaID: tayfa.PG(), ID: event.PG(), Title: ev.Title, Description: ev.Description,
			StartsAt: ev.StartsAt, EndsAt: ev.EndsAt, Mode: ev.Mode, MeetingPointName: ev.MeetingPointName,
			MeetingLat: ev.MeetingLat, MeetingLng: ev.MeetingLng, City: ev.City, District: ev.District,
			OnlineUrl: ev.OnlineUrl, Capacity: ev.Capacity, Discoverable: ev.Discoverable,
		}
		if set[FieldTitle] {
			p.Title = title
		}
		if set[FieldDescription] {
			p.Description = text(desc)
		}
		if set[FieldTime] {
			p.StartsAt, p.EndsAt = ts(start), ts(end)
		}
		if set[FieldLocation] {
			setLocation(&p.Mode, &p.MeetingPointName, &p.MeetingLat, &p.MeetingLng, &p.City, &p.District, &p.OnlineUrl, loc)
		}
		if set[FieldCapacity] {
			p.Capacity = int4(capacity)
		}
		if set[FieldDiscoverable] {
			// An instant tayfa's event never reaches discovery (ADR-0032).
			if in.Discoverable && access.Instant(c.tayfa) {
				return apperr.Validation("discoverable")
			}
			p.Discoverable = in.Discoverable
		}
		if set[FieldDetails] {
			if !slices.Contains(detailTypes, ev.Type) {
				return apperr.Validation("details")
			}
			d, err := cleanDetails(ev.Type, in.Details, p.StartsAt.Time, false)
			if err != nil {
				return err
			}
			if d.typ() == "" {
				return apperr.Validation("details")
			}
			if err := setDetails(ctx, q, event, d); err != nil {
				return err
			}
		}
		if set[FieldFields] {
			values, err := cleanEventFields(ctx, q, ev, in.Fields)
			if err != nil {
				return err
			}
			if err := setFields(ctx, q, event, values, true); err != nil {
				return err
			}
		}
		if n, err := q.UpdateEvent(ctx, p); err != nil {
			// Another event of the series already starts at this time.
			if store.SQLState(err) == store.CodeUniqueViolation && store.Constraint(err) == "events_series_starts_at_key" {
				return apperr.Validation("start_time")
			}
			return fmt.Errorf("etkinlik: %w", err)
		} else if n != 1 {
			return apperr.ErrEventNotFound
		}
		if set[FieldCover] {
			if err := setCover(ctx, q, c, tayfa, event, in.CoverUploadID); err != nil {
				return err
			}
		}
		if set[FieldTitle] && access.Instant(c.tayfa) {
			// An instant tayfa is named after its event (ADR-0032 §2).
			if _, err := q.SetInstantTayfaName(ctx, store.SetInstantTayfaNameParams{
				ID: tayfa.PG(), Name: title, NameNormalized: strings.ToLowerSpecial(unicode.TurkishCase, title),
			}); err != nil {
				return fmt.Errorf("anlık tayfa adı: %w", err)
			}
		}
		if err := audit(ctx, q, c, EventUpdated, event, map[string]any{"fields": changed}); err != nil {
			return err
		}
		payload := map[string]any{"event_id": event.String(), "fields": changed}
		if set[FieldTime] {
			// chat archives the event's channel 7 days after its end
			// (issue #183).
			payload["ends_at"] = p.EndsAt.Time.UTC()
		}
		b.add(EventUpdated, event, payload)
		// Activity records are snapshots of the event (issue #50).
		if set[FieldTitle] || set[FieldTime] || set[FieldLocation] {
			if err := refreshActivity(ctx, q, tayfa, event, c.now); err != nil {
				return err
			}
		}
		if set[FieldCapacity] {
			return promote(ctx, q, c, b, event, p.Capacity)
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, user, tayfa, event)
}

// Cancel cancels a scheduled event. The row stays; reason may be empty.
func (s *Service) Cancel(ctx context.Context, user, tayfa, event id.ID, reason string) (Event, error) {
	r, err := cleanText(reason, "reason", 0, cancelReasonMax, true)
	if err != nil {
		return Event{}, err
	}
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := c.manage(ev, authz.EventCancelOwn, authz.EventCancelAny); err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		n, err := q.CancelEvent(ctx, store.CancelEventParams{
			TayfaID: tayfa.PG(), ID: event.PG(), CancelledAt: ts(c.now), CancelledBy: user.PG(), CancelReason: text(r),
		})
		if err != nil {
			return fmt.Errorf("iptal: %w", err)
		}
		if n != 1 {
			return apperr.ErrEventCancelled
		}
		// The reason is free text; it stays out of the audit record and the
		// event.
		if err := audit(ctx, q, c, EventCancelled, event, nil); err != nil {
			return err
		}
		b.add(EventCancelled, event, map[string]any{"event_id": event.String(), "cancelled_at": c.now.UTC()})
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, user, tayfa, event)
}

// currentType returns the catalog's current version of the type key for a
// new event with the given mode. An unknown or retired type and a mode the
// type does not allow are validation errors: the client sent a type the
// catalog it holds should not have offered.
func currentType(ctx context.Context, q *store.Queries, key, mode string) (catalog.EventType, error) {
	et, err := catalog.CurrentType(ctx, q, key)
	if errors.Is(err, apperr.ErrCatalogTypeNotFound) {
		return catalog.EventType{}, apperr.Validation("type")
	}
	if err != nil {
		return catalog.EventType{}, err
	}
	if et.Status != catalog.StatusActive {
		return catalog.EventType{}, apperr.Validation("type")
	}
	if !slices.Contains(et.Modes, mode) {
		return catalog.EventType{}, apperr.Validation("mode")
	}
	return et, nil
}

// modeAllowed checks a new mode of ev against the modes of the type
// version it was created with.
func modeAllowed(ctx context.Context, q *store.Queries, ev store.GetEventRow, mode string) error {
	et, err := catalog.TypeVersion(ctx, q, ev.Type, ev.TypeVersion)
	if err != nil {
		// The foreign key guarantees the version exists.
		return fmt.Errorf("etkinliğin tür sürümü: %w", err)
	}
	if !slices.Contains(et.Modes, mode) {
		return apperr.Validation("mode")
	}
	return nil
}

// cleanFields checks a complete set of field values against a type
// version's definition. The error names the field ("fields.platform"),
// never its value.
func cleanFields(et catalog.EventType, in map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	values, err := et.Parsed.CleanValues(in, true)
	var ve *definition.ValueError
	switch {
	case errors.As(err, &ve) && ve.Field != "":
		return nil, apperr.Validation("fields." + ve.Field)
	case err != nil:
		return nil, apperr.Validation("fields")
	}
	return values, nil
}

// cleanEventFields is cleanFields against the type version ev was created
// with, which may be older than the catalog's current one.
func cleanEventFields(ctx context.Context, q *store.Queries, ev store.GetEventRow, in map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if len(in) > definition.MaxFields {
		return nil, apperr.Validation("fields")
	}
	et, err := catalog.TypeVersion(ctx, q, ev.Type, ev.TypeVersion)
	if err != nil {
		// The foreign key guarantees the version exists.
		return nil, fmt.Errorf("etkinliğin tür sürümü: %w", err)
	}
	return cleanFields(et, in)
}

// setFields writes an event's field values; replace first removes the
// values it had. The values were cleaned by cleanFields.
func setFields(ctx context.Context, q *store.Queries, event id.ID, values map[string]json.RawMessage, replace bool) error {
	if replace {
		if err := q.DeleteEventFieldValues(ctx, event.PG()); err != nil {
			return fmt.Errorf("alan değerleri: %w", err)
		}
	}
	// In key order, so that concurrent writers lock rows in one order.
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if err := q.SetEventFieldValue(ctx, store.SetEventFieldValueParams{EventID: event.PG(), FieldKey: k, Value: values[k]}); err != nil {
			return fmt.Errorf("alan değeri: %w", err)
		}
	}
	return nil
}

// setDetails writes the type details; d has exactly one field set, or none
// for a type without a detail table.
func setDetails(ctx context.Context, q *store.Queries, event id.ID, d Details) error {
	var err error
	switch {
	case d.Camp != nil:
		err = q.SetEventCamp(ctx, store.SetEventCampParams{
			EventID: event.PG(), SiteName: text(d.Camp.SiteName), HasWater: boolPtr(d.Camp.HasWater),
			HasToilet: boolPtr(d.Camp.HasToilet), Notes: text(d.Camp.Notes),
		})
	case d.Hike != nil:
		h := d.Hike
		p := store.SetEventHikeParams{
			EventID: event.PG(), Difficulty: int4(h.Difficulty), ElevationM: int4(h.ElevationM),
			ReturnGraceMinutes: h.ReturnGraceMinutes,
		}
		if h.DistanceKm > 0 {
			p.DistanceKm = pgtype.Float8{Float64: h.DistanceKm, Valid: true}
		}
		if !h.ExpectedReturnAt.IsZero() {
			p.ExpectedReturnAt = ts(h.ExpectedReturnAt)
		}
		err = q.SetEventHike(ctx, p)
	case d.Fishing != nil:
		err = q.SetEventFishing(ctx, store.SetEventFishingParams{EventID: event.PG(), SpotVisibility: d.Fishing.SpotVisibility})
	}
	if err != nil {
		return fmt.Errorf("etkinlik detayı: %w", err)
	}
	return nil
}

func setLocation(mode *string, name *pgtype.Text, lat, lng *pgtype.Float8, city, district, url *pgtype.Text, l Location) {
	*mode, *name, *city, *district, *url = l.Mode, text(l.MeetingPointName), text(l.City), text(l.District), text(l.OnlineURL)
	*lat, *lng = pgtype.Float8{}, pgtype.Float8{}
	if l.MeetingPoint != nil {
		*lat = pgtype.Float8{Float64: l.MeetingPoint.Lat, Valid: true}
		*lng = pgtype.Float8{Float64: l.MeetingPoint.Lng, Valid: true}
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// int4 stores 0 as NULL ("not given" / no limit).
func int4(n int32) pgtype.Int4 {
	if n == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: n, Valid: true}
}

func boolPtr(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}

// optID is NULL for the zero ID.
func optID(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}

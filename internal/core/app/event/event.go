// Package event manages a tayfa's events: creating, editing and cancelling
// them, RSVPs with a waitlist, camp item lists and convoy vehicles
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
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
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
	// FieldCover is the cover image (events.cover_upload_id, issue #135).
	FieldCover = "cover_upload_id"
)

var updateFields = []string{FieldTitle, FieldDescription, FieldTime, FieldLocation, FieldCapacity, FieldDiscoverable, FieldDetails, FieldCover}

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
	ID          id.ID
	TayfaID     id.ID
	Type        string
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
		for _, e := range b.events {
			if err := txn.Event(ctx, q, e.typ, tayfa, user, txn.Target{Type: "event", ID: e.subject}, e.payload); err != nil {
				return err
			}
		}
		return notifyBatch(ctx, q, b)
	})
}

// notifyBatch writes the notification requests of a write's events, after
// them (ADR-0009, package notify): a new event for every active member,
// a change or cancellation for its going, maybe and waitlist participants,
// a waitlist promotion for the promoted member. The actor never hears
// about their own change. Changes to the lists on an event (camp items,
// convoy vehicles) notify nobody.
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
// event (fieldCampItems, fieldVehicles).
func listsOnly(v any) bool {
	fields, ok := v.([]string)
	if !ok || len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != fieldCampItems && f != fieldVehicles {
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

// Create creates an event with user as organizer. A repeat with the same
// ClientRequestID returns the first event.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in Input) (Event, error) {
	if !slices.Contains(Types, in.Type) {
		return Event{}, apperr.Validation("type")
	}
	title, err := cleanText(in.Title, "title", 1, titleMax, false)
	if err != nil {
		return Event{}, err
	}
	desc, err := cleanText(in.Description, "description", 0, descriptionMax, true)
	if err != nil {
		return Event{}, err
	}
	start, end, err := cleanTime(in.StartsAt, in.EndsAt, s.now())
	if err != nil {
		return Event{}, err
	}
	loc, err := cleanLocation(in.Location)
	if err != nil {
		return Event{}, err
	}
	capacity, err := cleanCapacity(in.Capacity)
	if err != nil {
		return Event{}, err
	}
	details, err := cleanDetails(in.Type, in.Details, start, true)
	if err != nil {
		return Event{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Event{}, apperr.Validation("client_request_id")
	}
	rec, err := cleanRecurrence(in.Recurrence, start)
	if err != nil {
		return Event{}, err
	}

	var eventID, seriesID id.ID
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCreate); err != nil {
			return err
		}
		prev, err := q.GetEventByClientRequest(ctx, store.GetEventByClientRequestParams{CreatedBy: user.PG(), ClientRequestID: crid.PG()})
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

		eventID = id.New()
		p := store.CreateEventParams{
			ID: eventID.PG(), TayfaID: tayfa.PG(), Type: in.Type, Title: title, Description: text(desc),
			StartsAt: ts(start), EndsAt: ts(end), Capacity: int4(capacity), Discoverable: in.Discoverable,
			CreatedBy: user.PG(), ClientRequestID: crid.PG(),
		}
		setLocation(&p.Mode, &p.MeetingPointName, &p.MeetingLat, &p.MeetingLng, &p.City, &p.District, &p.OnlineUrl, loc)
		if err := q.CreateEvent(ctx, p); err != nil {
			return fmt.Errorf("etkinlik: %w", err)
		}
		if err := setDetails(ctx, q, eventID, details); err != nil {
			return err
		}
		if in.CoverUploadID != (id.ID{}) {
			if err := setCover(ctx, q, c, tayfa, eventID, in.CoverUploadID); err != nil {
				return err
			}
		}
		if err := audit(ctx, q, c, EventCreated, eventID, map[string]any{"type": in.Type}); err != nil {
			return err
		}
		payload := map[string]any{"event_id": eventID.String(), "type": in.Type}
		if rec != nil {
			if seriesID, err = createSeries(ctx, q, c, eventID, start, rec); err != nil {
				return err
			}
			payload["series_id"] = seriesID.String()
		}
		b.add(EventCreated, eventID, payload)
		return txn.TouchTayfa(ctx, q, tayfa, c.now)
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
			p.Discoverable = in.Discoverable
		}
		if set[FieldDetails] {
			if ev.Type == TypeGeneral {
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
		if err := audit(ctx, q, c, EventUpdated, event, map[string]any{"fields": changed}); err != nil {
			return err
		}
		b.add(EventUpdated, event, map[string]any{"event_id": event.String(), "fields": changed})
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
		b.add(EventCancelled, event, map[string]any{"event_id": event.String()})
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, user, tayfa, event)
}

// setDetails writes the type details; d has exactly one field set, or none
// for a general event.
func setDetails(ctx context.Context, q *store.Queries, event id.ID, d Details) error {
	var err error
	switch {
	case d.Convoy != nil:
		var route []byte
		if len(d.Convoy.Route) > 0 {
			pts := make([]map[string]float64, len(d.Convoy.Route))
			for i, p := range d.Convoy.Route {
				pts[i] = map[string]float64{"lat": p.Lat, "lng": p.Lng}
			}
			if route, err = json.Marshal(pts); err != nil {
				return fmt.Errorf("rota: %w", err)
			}
		}
		err = q.SetEventConvoy(ctx, store.SetEventConvoyParams{EventID: event.PG(), Route: route})
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
	case d.Gaming != nil:
		err = q.SetEventGaming(ctx, store.SetEventGamingParams{EventID: event.PG(), Game: d.Gaming.Game, Platform: d.Gaming.Platform})
	case d.Sports != nil:
		err = q.SetEventSports(ctx, store.SetEventSportsParams{EventID: event.PG(), Sport: d.Sports.Sport, VenueName: text(d.Sports.VenueName)})
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

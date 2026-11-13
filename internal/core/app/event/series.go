package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event/rrule"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Repeating events (issue #51; data model v14 "Tekrarlayan etkinlik",
// "Arka plan işleri").
//
// A series is created with its first event (Create with a Recurrence); that
// event is the series' template. A background job (RunSeriesGenerator)
// creates the occurrences of the next eight weeks as ordinary events, so
// RSVPs, cancellation and notifications work on each of them, and moves
// generated_until forward.
//
// No occurrence is created twice, even with several pods: the job locks the
// tayfa row and then the series row (the same order as user requests), the
// unique index events_series_starts_at_key allows one event per series and
// start time, and the insert is ON CONFLICT DO NOTHING. generated_until
// only moves forward, so an occurrence whose event was moved elsewhere is
// not created again.
//
// Content: new occurrences copy the template (type details included, lists
// not). Editing one event alone marks it series_override, and series
// updates and ending the series leave it alone. When the template itself
// is edited alone, or has started, the template moves to the next upcoming
// occurrence that was not edited alone, so the series keeps its own content.
// The rule's start (dtstart) never moves: COUNT and INTERVAL always count
// from the first occurrence.

// Series outbox and audit types.
const (
	SeriesCreated = "event_series.created"
	SeriesUpdated = "event_series.updated"
	SeriesEnded   = "event_series.ended"
)

const (
	// DefaultTimeZone is the series time zone when the client sends none.
	DefaultTimeZone = "Europe/Istanbul"
	// seriesHorizon is how far ahead occurrences are created.
	seriesHorizon = 8 * 7 * 24 * time.Hour
	// seriesRefresh: a series is processed again once its generated_until
	// is less than seriesHorizon - seriesRefresh ahead.
	seriesRefresh = 24 * time.Hour
	// seriesPerRun bounds the occurrences one series gets per run; the rest
	// come on the next run (8 weeks × 7 days fit).
	seriesPerRun = 64
	// seriesBatch bounds the series one run processes.
	seriesBatch = 200
	// SeriesInterval is how often the generator runs.
	SeriesInterval = 10 * time.Minute
)

// timeZonePattern matches IANA names (event_series_timezone_format); it
// keeps paths and "Local" out of time.LoadLocation.
var timeZonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// Recurrence is how a series repeats.
type Recurrence struct {
	// RRule is an RFC 5545 rule in the subset of package rrule.
	RRule string
	// TimeZone is an IANA name; "" means DefaultTimeZone.
	TimeZone string
}

// Series is an event series.
type Series struct {
	ID              id.ID
	TayfaID         id.ID
	TemplateEventID id.ID
	RRule           string
	TimeZone        string
	StartsAt        time.Time
	// EndedAt is zero while the series runs.
	EndedAt   time.Time
	CreatedBy id.ID
	CreatedAt time.Time
}

// recurrence is a checked Recurrence.
type recurrence struct {
	rule rrule.Rule
	loc  *time.Location
}

// cleanRecurrence checks r for a series whose first event starts at start.
func cleanRecurrence(r *Recurrence, start time.Time) (*recurrence, error) {
	if r == nil {
		return nil, nil
	}
	tz := r.TimeZone
	if tz == "" {
		tz = DefaultTimeZone
	}
	loc, err := loadZone(tz)
	if err != nil {
		return nil, apperr.Validation("recurrence.time_zone")
	}
	rule, err := rrule.Parse(r.RRule)
	if err != nil || rule.Validate(start.In(loc)) != nil {
		return nil, apperr.Validation("recurrence.rrule")
	}
	return &recurrence{rule: rule, loc: loc}, nil
}

func loadZone(name string) (*time.Location, error) {
	if len(name) > 64 || !timeZonePattern.MatchString(name) || name == "Local" {
		return nil, errors.New("saat dilimi geçersiz")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("saat dilimi: %w", err)
	}
	return loc, nil
}

type seriesRow = store.GetEventSeriesRow

func seriesView(r seriesRow) Series {
	v := Series{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), RRule: r.Rrule, TimeZone: r.Timezone,
		StartsAt: r.Dtstart.Time.UTC(), CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if r.TemplateEventID.Valid {
		v.TemplateEventID = id.ID(r.TemplateEventID.Bytes)
	}
	if r.EndedAt.Valid {
		v.EndedAt = r.EndedAt.Time.UTC()
	}
	if r.CreatedBy.Valid {
		v.CreatedBy = id.ID(r.CreatedBy.Bytes)
	}
	return v
}

func lockSeries(ctx context.Context, q *store.Queries, tayfa id.ID, series pgtype.UUID) (seriesRow, error) {
	r, err := q.GetEventSeriesForUpdate(ctx, store.GetEventSeriesForUpdateParams{TayfaID: tayfa.PG(), ID: series})
	if errors.Is(err, pgx.ErrNoRows) {
		return seriesRow{}, apperr.ErrEventSeriesNotFound
	}
	if err != nil {
		return seriesRow{}, fmt.Errorf("seri: %w", err)
	}
	return seriesRow(r), nil
}

// createSeries makes event (just created, starting at start) the first
// event and template of a new series. Called inside Create's transaction.
func createSeries(ctx context.Context, q *store.Queries, c caller, event id.ID, start time.Time, r *recurrence) (id.ID, error) {
	sid := id.New()
	tayfa := id.ID(c.tayfa.ID.Bytes)
	if err := q.CreateEventSeries(ctx, store.CreateEventSeriesParams{
		ID: sid.PG(), TayfaID: tayfa.PG(), TemplateEventID: event.PG(), Rrule: r.rule.String(),
		Timezone: r.loc.String(), Dtstart: ts(start), CreatedBy: c.user.PG(),
	}); err != nil {
		return id.ID{}, fmt.Errorf("seri: %w", err)
	}
	if n, err := q.SetEventSeriesID(ctx, store.SetEventSeriesIDParams{SeriesID: sid.PG(), TayfaID: tayfa.PG(), ID: event.PG()}); err != nil {
		return id.ID{}, fmt.Errorf("seri: %w", err)
	} else if n != 1 {
		return id.ID{}, errors.New("seri: etkinlik bağlanamadı")
	}
	if err := txn.Audit(ctx, q, tayfa, c.user, SeriesCreated, txn.Target{Type: "event_series", ID: sid}, map[string]any{"event_id": event.String()}); err != nil {
		return id.ID{}, err
	}
	return sid, nil
}

// retemplate moves the series' template off exclude to the next upcoming
// event of the series that was not edited alone. It does nothing when the
// template is another event, or when no such event exists (the series then
// keeps copying exclude).
func retemplate(ctx context.Context, q *store.Queries, s seriesRow, exclude id.ID, now time.Time) error {
	if !s.TemplateEventID.Valid || id.ID(s.TemplateEventID.Bytes) != exclude {
		return nil
	}
	ids, err := q.ListSeriesUpcomingEvents(ctx, store.ListSeriesUpcomingEventsParams{TayfaID: s.TayfaID, SeriesID: s.ID, Now: ts(now)})
	if err != nil {
		return fmt.Errorf("seri örnekleri: %w", err)
	}
	for _, e := range ids {
		if id.ID(e.Bytes) != exclude {
			return setTemplate(ctx, q, s, id.ID(e.Bytes))
		}
	}
	return nil
}

func setTemplate(ctx context.Context, q *store.Queries, s seriesRow, event id.ID) error {
	if err := q.SetEventSeriesTemplate(ctx, store.SetEventSeriesTemplateParams{TemplateEventID: event.PG(), TayfaID: s.TayfaID, ID: s.ID}); err != nil {
		return fmt.Errorf("seri şablonu: %w", err)
	}
	return nil
}

// updateSeries applies an update (UpdateEventRequest.series = true) through
// ev to its series: ev itself, every upcoming scheduled event of the series
// that was not edited alone, and the template. set holds the mask paths;
// "time" is refused by the caller.
func updateSeries(ctx context.Context, q *store.Queries, c caller, b *batch, ev store.GetEventRow, set map[string]bool, changed []string, in Input, loc Location, title, desc string, capacity int32) error {
	tayfa := id.ID(c.tayfa.ID.Bytes)
	s, err := lockSeries(ctx, q, tayfa, ev.SeriesID)
	if err != nil {
		return err
	}
	if s.EndedAt.Valid {
		return apperr.ErrEventSeriesEnded
	}
	var details Details
	if set[FieldDetails] {
		if ev.Type == TypeGeneral {
			return apperr.Validation("details")
		}
		if details, err = cleanDetails(ev.Type, in.Details, ev.StartsAt.Time, false); err != nil {
			return err
		}
		if details.typ() == "" {
			return apperr.Validation("details")
		}
	}
	upcoming, err := q.ListSeriesUpcomingEvents(ctx, store.ListSeriesUpcomingEventsParams{TayfaID: tayfa.PG(), SeriesID: s.ID, Now: ts(c.now)})
	if err != nil {
		return fmt.Errorf("seri örnekleri: %w", err)
	}
	evID := id.ID(ev.ID.Bytes)
	targets := make([]id.ID, 0, len(upcoming)+2)
	for _, u := range upcoming {
		targets = append(targets, id.ID(u.Bytes))
	}
	// The template moves to the first upcoming event when it has started
	// or was edited alone, so past events keep their content.
	tmpl := id.ID(s.TemplateEventID.Bytes)
	if s.TemplateEventID.Valid && !slices.Contains(targets, tmpl) {
		if len(targets) > 0 {
			if err := setTemplate(ctx, q, s, targets[0]); err != nil {
				return err
			}
		} else {
			targets = append(targets, tmpl)
		}
	}
	if !slices.Contains(targets, evID) {
		targets = append(targets, evID)
	}

	for _, t := range targets {
		row, err := lockEvent(ctx, q, tayfa, t)
		if errors.Is(err, apperr.ErrEventNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		p := store.UpdateSeriesEventParams{
			TayfaID: row.TayfaID, ID: row.ID, Title: row.Title, Description: row.Description, Mode: row.Mode,
			MeetingPointName: row.MeetingPointName, MeetingLat: row.MeetingLat, MeetingLng: row.MeetingLng,
			City: row.City, District: row.District, OnlineUrl: row.OnlineUrl, Capacity: row.Capacity,
			Discoverable: row.Discoverable,
		}
		if set[FieldTitle] {
			p.Title = title
		}
		if set[FieldDescription] {
			p.Description = text(desc)
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
		if _, err := q.UpdateSeriesEvent(ctx, p); err != nil {
			return fmt.Errorf("seri örneği: %w", err)
		}
		// Activity records are snapshots of the event (issue #50).
		if set[FieldTitle] || set[FieldLocation] {
			if err := refreshActivity(ctx, q, tayfa, t, c.now); err != nil {
				return err
			}
		}
		if set[FieldDetails] {
			if err := setDetails(ctx, q, t, shiftDetails(details, row.StartsAt.Time.Sub(ev.StartsAt.Time))); err != nil {
				return err
			}
		}
		b.add(EventUpdated, t, map[string]any{"event_id": t.String(), "fields": changed, "series_id": id.ID(s.ID.Bytes).String()})
		// Only events that can still change get waitlist promotions.
		if set[FieldCapacity] && row.Status == StatusScheduled && row.EndsAt.Time.After(c.now) {
			if err := promote(ctx, q, c, b, t, p.Capacity); err != nil {
				return err
			}
		}
	}
	return txn.Audit(ctx, q, tayfa, c.user, SeriesUpdated, txn.Target{Type: "event_series", ID: id.ID(s.ID.Bytes)},
		map[string]any{"fields": changed, "events": len(targets)})
}

// shiftDetails moves the times inside d (a hike's expected return) by off,
// for a copy of d on an event that starts off later.
func shiftDetails(d Details, off time.Duration) Details {
	if d.Hike != nil && !d.Hike.ExpectedReturnAt.IsZero() {
		h := *d.Hike
		h.ExpectedReturnAt = h.ExpectedReturnAt.Add(off)
		d.Hike = &h
	}
	return d
}

// GetSeries returns a series of the tayfa.
func (s *Service) GetSeries(ctx context.Context, user, tayfa, series id.ID) (Series, error) {
	c, err := s.load(ctx, s.q, user, tayfa, false)
	if err != nil {
		return Series{}, err
	}
	if err := c.authorize(authz.EventView); err != nil {
		return Series{}, err
	}
	r, err := s.q.GetEventSeries(ctx, store.GetEventSeriesParams{TayfaID: tayfa.PG(), ID: series.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Series{}, apperr.ErrEventSeriesNotFound
	}
	if err != nil {
		return Series{}, fmt.Errorf("seri: %w", err)
	}
	return seriesView(r), nil
}

// EndSeries ends a series and cancels its upcoming events that were not
// edited alone. It returns the series and how many events were cancelled.
func (s *Service) EndSeries(ctx context.Context, user, tayfa, series id.ID) (Series, int, error) {
	var cancelled int
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		sr, err := lockSeries(ctx, q, tayfa, series.PG())
		if err != nil {
			return err
		}
		// The series' creator is its organizer.
		if err := c.manage(store.GetEventRow{CreatedBy: sr.CreatedBy}, authz.EventCancelOwn, authz.EventCancelAny); err != nil {
			return err
		}
		if sr.EndedAt.Valid {
			return apperr.ErrEventSeriesEnded
		}
		if _, err := q.EndEventSeries(ctx, store.EndEventSeriesParams{EndedAt: ts(c.now), TayfaID: tayfa.PG(), ID: series.PG()}); err != nil {
			return fmt.Errorf("seri: %w", err)
		}
		ids, err := q.CancelSeriesUpcomingEvents(ctx, store.CancelSeriesUpcomingEventsParams{
			Now: ts(c.now), CancelledBy: user.PG(), TayfaID: tayfa.PG(), SeriesID: series.PG(),
		})
		if err != nil {
			return fmt.Errorf("seri iptali: %w", err)
		}
		cancelled = len(ids)
		for _, e := range ids {
			eid := id.ID(e.Bytes)
			b.add(EventCancelled, eid, map[string]any{"event_id": eid.String(), "series_id": series.String(), "cancelled_at": c.now.UTC()})
		}
		return txn.Audit(ctx, q, tayfa, user, SeriesEnded, txn.Target{Type: "event_series", ID: series}, map[string]any{"cancelled": cancelled})
	})
	if err != nil {
		return Series{}, 0, err
	}
	v, err := s.GetSeries(ctx, user, tayfa, series)
	return v, cancelled, err
}

// ---------------------------------------------------------------------------
// Generation

// RunSeriesGenerator creates upcoming occurrences every SeriesInterval
// until ctx is done, then returns nil. A failed run is logged (IDs and the
// error only) and retried on the next tick; it never stops the service.
func (s *Service) RunSeriesGenerator(ctx context.Context) error {
	t := time.NewTicker(SeriesInterval)
	defer t.Stop()
	for {
		n, err := s.GenerateDueSeries(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			s.cfg.Log.WarnContext(ctx, "seri örnekleri üretilemedi", slog.String("hata", err.Error()))
		case n > 0:
			s.cfg.Log.InfoContext(ctx, "seri örnekleri üretildi", slog.Int("adet", n))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// GenerateDueSeries runs one pass over the series whose occurrences need
// extending and returns how many events it created. One failing series
// does not stop the others; the first error is returned.
func (s *Service) GenerateDueSeries(ctx context.Context) (int, error) {
	now := s.now()
	due, err := s.q.ListDueEventSeries(ctx, store.ListDueEventSeriesParams{DueBefore: ts(now.Add(seriesHorizon - seriesRefresh)), N: seriesBatch})
	if err != nil {
		return 0, fmt.Errorf("seriler: %w", err)
	}
	total := 0
	var first error
	for _, d := range due {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		n, err := s.GenerateSeries(ctx, id.ID(d.TayfaID.Bytes), id.ID(d.ID.Bytes))
		total += n
		if err != nil && first == nil {
			first = fmt.Errorf("seri %s: %w", id.ID(d.ID.Bytes), err)
		}
	}
	return total, first
}

// GenerateSeries creates the occurrences of one series up to eight weeks
// ahead and returns how many events it created. It is safe to run
// concurrently and repeatedly (see the package notes above).
//
// A deleted, read-only or events-disabled tayfa gets no occurrences for
// this window; generated_until still moves, so the series does not hold up
// the others.
func (s *Service) GenerateSeries(ctx context.Context, tayfa, series id.ID) (int, error) {
	created := 0
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		now := s.now()
		horizon := now.Add(seriesHorizon)
		advance := func(until pgtype.Timestamptz) error {
			if err := q.AdvanceEventSeries(ctx, store.AdvanceEventSeriesParams{GeneratedUntil: until, ID: series.PG()}); err != nil {
				return fmt.Errorf("generated_until: %w", err)
			}
			return nil
		}

		t, err := q.GetTayfaForUpdate(ctx, tayfa.PG())
		if errors.Is(err, pgx.ErrNoRows) {
			return advance(infinity())
		}
		if err != nil {
			return fmt.Errorf("tayfa: %w", err)
		}
		sr, err := lockSeries(ctx, q, tayfa, series.PG())
		if errors.Is(err, apperr.ErrEventSeriesNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if sr.EndedAt.Valid || !sr.TemplateEventID.Valid {
			return nil
		}
		on, err := q.IsTayfaModuleEnabled(ctx, store.IsTayfaModuleEnabledParams{TayfaID: tayfa.PG(), Module: access.ModuleEvents})
		if err != nil {
			return fmt.Errorf("modül: %w", err)
		}
		if !on || access.ReadOnly(access.Tayfa(t)) {
			return advance(ts(horizon))
		}
		tmpl, err := getEvent(ctx, q, tayfa, id.ID(sr.TemplateEventID.Bytes))
		if errors.Is(err, apperr.ErrEventNotFound) {
			return advance(ts(horizon))
		}
		if err != nil {
			return err
		}
		loc, err := loadZone(sr.Timezone)
		if err != nil {
			return err
		}
		rule, err := rrule.Parse(sr.Rrule)
		if err != nil {
			return fmt.Errorf("seri kuralı: %w", err)
		}
		details, err := typeDetails(ctx, q, tmpl)
		if err != nil {
			return err
		}

		after := sr.Dtstart.Time
		if sr.GeneratedUntil.Valid && sr.GeneratedUntil.InfinityModifier == pgtype.Finite && sr.GeneratedUntil.Time.After(after) {
			after = sr.GeneratedUntil.Time
		}
		if now.After(after) {
			after = now
		}
		occ, more := rule.Between(sr.Dtstart.Time.In(loc), after, horizon, seriesPerRun)
		duration := tmpl.EndsAt.Time.Sub(tmpl.StartsAt.Time)
		type made struct{ id id.ID }
		var events []made
		for _, o := range occ {
			eid := id.New()
			start := o.UTC().Truncate(time.Microsecond)
			n, err := q.CreateSeriesEvent(ctx, store.CreateSeriesEventParams{
				ID: eid.PG(), TayfaID: tayfa.PG(), Type: tmpl.Type, Mode: tmpl.Mode, Title: tmpl.Title,
				Description: tmpl.Description, StartsAt: ts(start), EndsAt: ts(start.Add(duration)),
				MeetingPointName: tmpl.MeetingPointName, MeetingLat: tmpl.MeetingLat, MeetingLng: tmpl.MeetingLng,
				City: tmpl.City, District: tmpl.District, OnlineUrl: tmpl.OnlineUrl, Capacity: tmpl.Capacity,
				Discoverable: tmpl.Discoverable, CreatedBy: sr.CreatedBy, SeriesID: sr.ID,
			})
			if err != nil {
				return fmt.Errorf("seri örneği: %w", err)
			}
			if n == 0 {
				// Already there (another pod, or an earlier run).
				continue
			}
			if err := setDetails(ctx, q, eid, shiftDetails(details, start.Sub(tmpl.StartsAt.Time))); err != nil {
				return err
			}
			events = append(events, made{eid})
		}

		// generated_until: the last occurrence when the run was cut short,
		// the horizon otherwise, and "infinity" once the rule has no more.
		until := ts(horizon)
		switch {
		case len(occ) == seriesPerRun && more:
			until = ts(occ[len(occ)-1])
		case !more:
			until = infinity()
		}
		if err := advance(until); err != nil {
			return err
		}
		// Outbox events last (outbox.Write); no actor: the system made them.
		for _, e := range events {
			if err := txn.Event(ctx, q, EventCreated, tayfa, id.ID{}, txn.Target{Type: "event", ID: e.id},
				map[string]any{"tayfa_id": tayfa.String(), "event_id": e.id.String(), "type": tmpl.Type, "series_id": series.String()}); err != nil {
				return err
			}
		}
		created = len(events)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

func infinity() pgtype.Timestamptz {
	return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
}

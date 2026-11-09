package event

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// at returns a Service on the same database whose clock reads now.
func (e *env) at(t *testing.T, now time.Time) *Service {
	t.Helper()
	s, err := New(Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	must(t, err)
	return s
}

func weekly(in Input, rule string) Input {
	in.Recurrence = &Recurrence{RRule: rule}
	return in
}

// seriesEvents returns the series' events by start time.
func (e *env) seriesEvents(t *testing.T, series id.ID) []store.GetEventRow {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT id FROM events WHERE series_id = $1 ORDER BY starts_at`, series.PG())
	must(t, err)
	var ids []pgtype.UUID
	for rows.Next() {
		var u pgtype.UUID
		must(t, rows.Scan(&u))
		ids = append(ids, u)
	}
	must(t, rows.Err())
	out := make([]store.GetEventRow, len(ids))
	for i, u := range ids {
		r, err := e.q.GetEvent(ctx, store.GetEventParams{TayfaID: e.tayfaOf(t, series).PG(), ID: u})
		must(t, err)
		out[i] = r
	}
	return out
}

func (e *env) tayfaOf(t *testing.T, series id.ID) id.ID {
	t.Helper()
	var u pgtype.UUID
	must(t, e.pool.QueryRow(ctx, `SELECT tayfa_id FROM event_series WHERE id = $1`, series.PG()).Scan(&u))
	return id.ID(u.Bytes)
}

func (e *env) generatedUntil(t *testing.T, series id.ID) pgtype.Timestamptz {
	t.Helper()
	var g pgtype.Timestamptz
	must(t, e.pool.QueryRow(ctx, `SELECT generated_until FROM event_series WHERE id = $1`, series.PG()).Scan(&g))
	return g
}

func TestSeriesCreateAndGenerate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := weekly(input(), "FREQ=WEEKLY")
	in.Type = TypeHike
	in.Details = Details{Hike: &Hike{Difficulty: 3, ExpectedReturnAt: in.StartsAt.Add(5 * time.Hour)}}
	in.Capacity = 12
	first := e.create(t, tf, admin, in)
	if first.SeriesID == (id.ID{}) || first.SeriesOverride {
		t.Fatalf("ilk etkinlik = %+v", first)
	}
	sid := first.SeriesID

	// Created right away: the first event plus every week within 8 weeks.
	evs := e.seriesEvents(t, sid)
	if len(evs) < 8 || len(evs) > 9 {
		t.Fatalf("%d örnek", len(evs))
	}
	if id.ID(evs[0].ID.Bytes) != first.ID {
		t.Fatal("ilk örnek şablon değil")
	}
	for i, r := range evs[1:] {
		want := first.StartsAt.AddDate(0, 0, 7*(i+1))
		if !r.StartsAt.Time.Equal(want) || !r.EndsAt.Time.Equal(want.Add(3*time.Hour)) {
			t.Errorf("örnek %d: %v – %v, %v bekleniyordu", i+1, r.StartsAt.Time, r.EndsAt.Time, want)
		}
		if r.Title != first.Title || r.Type != TypeHike || r.Capacity.Int32 != 12 || r.City.String != "İzmir" ||
			r.MeetingLat.Float64 != izmir.Lat || id.ID(r.CreatedBy.Bytes) != admin || r.SeriesOverride || r.Status != StatusScheduled {
			t.Errorf("örnek %d kopyası = %+v", i+1, r)
		}
		// Type details are copied; the expected return keeps its distance
		// from the start.
		v, err := e.svc.Get(ctx, admin, tf, id.ID(r.ID.Bytes))
		must(t, err)
		if v.Details.Hike == nil || v.Details.Hike.Difficulty != 3 || !v.Details.Hike.ExpectedReturnAt.Equal(want.Add(5*time.Hour)) {
			t.Errorf("örnek %d detayı = %+v", i+1, v.Details.Hike)
		}
	}
	// Outbox: one event.created per generated occurrence, without an actor,
	// naming the series and nothing else.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.created' AND actor_id IS NULL AND tayfa_id = $1 AND payload->>'series_id' = $2`, tf.PG(), sid.String()); n != len(evs)-1 {
		t.Errorf("üretim olayları = %d", n)
	}
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'event.created' AND actor_id IS NULL LIMIT 1`).Scan(&payload))
	if strings.Contains(payload, "Pazar") || strings.Contains(payload, "İzmir") {
		t.Errorf("olayda içerik: %s", payload)
	}
	g := e.generatedUntil(t, sid)
	if !g.Valid || g.InfinityModifier != pgtype.Finite || time.Until(g.Time) < 55*24*time.Hour {
		t.Errorf("generated_until = %+v", g)
	}

	// Running again creates nothing; nor does forgetting generated_until
	// (the unique index and ON CONFLICT).
	n, err := e.svc.GenerateSeries(ctx, tf, sid)
	must(t, err)
	e.exec(t, `UPDATE event_series SET generated_until = NULL WHERE id = $1`, sid.PG())
	n2, err := e.svc.GenerateSeries(ctx, tf, sid)
	must(t, err)
	if n != 0 || n2 != 0 || len(e.seriesEvents(t, sid)) != len(evs) {
		t.Fatalf("tekrar üretim: %d, %d", n, n2)
	}

	// Three weeks later the job adds three weeks.
	later := e.at(t, time.Now().Add(21*24*time.Hour+time.Hour))
	n, err = later.GenerateDueSeries(ctx)
	must(t, err)
	if n != 3 || len(e.seriesEvents(t, sid)) != len(evs)+3 {
		t.Fatalf("üç hafta sonra: %d yeni, %d toplam", n, len(e.seriesEvents(t, sid)))
	}
	// Nothing is due right after.
	if n, err := later.GenerateDueSeries(ctx); err != nil || n != 0 {
		t.Fatalf("hemen ardından: %d %v", n, err)
	}
}

func TestSeriesCountExhausts(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	v := e.create(t, tf, admin, weekly(input(), "FREQ=WEEKLY;COUNT=3"))
	if n := len(e.seriesEvents(t, v.SeriesID)); n != 3 {
		t.Fatalf("COUNT=3: %d örnek", n)
	}
	// The rule has no more: never due again.
	if g := e.generatedUntil(t, v.SeriesID); g.InfinityModifier != pgtype.Infinity {
		t.Errorf("generated_until = %+v", g)
	}
	n, err := e.at(t, time.Now().AddDate(0, 3, 0)).GenerateDueSeries(ctx)
	if err != nil || n != 0 {
		t.Fatalf("bitmiş seri: %d %v", n, err)
	}
}

// TestSeriesConcurrentGeneration: several pods generating the same series
// at once create every occurrence exactly once.
func TestSeriesConcurrentGeneration(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	v := e.create(t, tf, admin, weekly(input(), "FREQ=WEEKLY;BYDAY="+dayCode(input().StartsAt)))
	want := len(e.seriesEvents(t, v.SeriesID))
	// Forget everything generated, then race.
	e.exec(t, `DELETE FROM events WHERE series_id = $1 AND id <> $2`, v.SeriesID.PG(), v.ID.PG())
	e.exec(t, `UPDATE event_series SET generated_until = NULL WHERE id = $1`, v.SeriesID.PG())

	var wg sync.WaitGroup
	total := make(chan int, 16)
	for range 8 {
		wg.Go(func() {
			for range 2 {
				n, err := e.svc.GenerateSeries(ctx, tf, v.SeriesID)
				if err != nil {
					t.Error(err)
				}
				total <- n
			}
		})
	}
	wg.Wait()
	close(total)
	sum := 0
	for n := range total {
		sum += n
	}
	if got := len(e.seriesEvents(t, v.SeriesID)); got != want || sum != want-1 {
		t.Fatalf("eşzamanlı üretim: %d örnek (%d bekleniyordu), %d üretildi", got, want, sum)
	}
	if n := e.count(t, `SELECT count(*) FROM (SELECT starts_at FROM events WHERE series_id = $1 GROUP BY starts_at HAVING count(*) > 1) d`, v.SeriesID.PG()); n != 0 {
		t.Fatalf("%d yinelenen örnek", n)
	}
	// The database refuses a duplicate even without the job's locks.
	r := e.seriesEvents(t, v.SeriesID)[1]
	_, err := e.pool.Exec(ctx, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, meeting_lat, meeting_lng, city, district, series_id)
		VALUES ($1, $2, 'general', 'in_person', 'x', $3, $4, 1, 1, 'a', 'b', $5)`, id.New().PG(), tf.PG(), r.StartsAt, r.EndsAt, v.SeriesID.PG())
	if store.SQLState(err) != store.CodeUniqueViolation {
		t.Fatalf("yinelenen örnek kabul edildi: %v", err)
	}
}

func dayCode(t time.Time) string {
	return [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[t.In(istanbul()).Weekday()]
}

func istanbul() *time.Location {
	l, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		panic(err)
	}
	return l
}

func TestSeriesValidation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := input()
	other := [...]string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}[(int(in.StartsAt.In(istanbul()).Weekday())+1)%7]
	for name, tc := range map[string]struct {
		rec   Recurrence
		field string
	}{
		"boş kural":           {Recurrence{}, "recurrence.rrule"},
		"günlük":              {Recurrence{RRule: "FREQ=DAILY"}, "recurrence.rrule"},
		"BYHOUR":              {Recurrence{RRule: "FREQ=WEEKLY;BYHOUR=9"}, "recurrence.rrule"},
		"başlangıç uymuyor":   {Recurrence{RRule: "FREQ=WEEKLY;BYDAY=" + other}, "recurrence.rrule"},
		"UNTIL geçmişte":      {Recurrence{RRule: "FREQ=WEEKLY;UNTIL=20200101T000000Z"}, "recurrence.rrule"},
		"COUNT ve UNTIL":      {Recurrence{RRule: "FREQ=WEEKLY;COUNT=2;UNTIL=20991231T000000Z"}, "recurrence.rrule"},
		"bilinmeyen dilim":    {Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "Mars/Olympus_Mons"}, "recurrence.time_zone"},
		"Local":               {Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "Local"}, "recurrence.time_zone"},
		"yol":                 {Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "../../etc/passwd"}, "recurrence.time_zone"},
		"mutlak yol":          {Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "/etc/localtime"}, "recurrence.time_zone"},
		"çok uzun kural":      {Recurrence{RRule: "FREQ=WEEKLY;" + strings.Repeat("X", 600)}, "recurrence.rrule"},
		"aylık sıra eksik":    {Recurrence{RRule: "FREQ=MONTHLY;BYDAY=SA"}, "recurrence.rrule"},
		"haftalık sıra fazla": {Recurrence{RRule: "FREQ=WEEKLY;BYDAY=1SA"}, "recurrence.rrule"},
	} {
		in := input()
		rec := tc.rec
		in.Recurrence = &rec
		_, err := e.svc.Create(ctx, admin, tf, in)
		wantField(t, name, err, tc.field)
	}
	if n := e.count(t, `SELECT count(*) FROM event_series`); n != 0 {
		t.Fatalf("%d seri oluştu", n)
	}

	// The stored rule is canonical and the zone defaults to Istanbul.
	in = input()
	in.Recurrence = &Recurrence{RRule: "rrule:freq=weekly;interval=1;byday=" + strings.ToLower(dayCode(in.StartsAt))}
	v := e.create(t, tf, admin, in)
	s, err := e.svc.GetSeries(ctx, admin, tf, v.SeriesID)
	must(t, err)
	if s.RRule != "FREQ=WEEKLY;BYDAY="+dayCode(in.StartsAt) || s.TimeZone != DefaultTimeZone || s.TemplateEventID != v.ID ||
		!s.StartsAt.Equal(v.StartsAt) || s.CreatedBy != admin || !s.EndedAt.IsZero() {
		t.Fatalf("seri = %+v", s)
	}
	// IDOR: a series of another tayfa is not found.
	tf2, admin2 := e.newTayfa(t)
	_, err = e.svc.GetSeries(ctx, admin2, tf2, v.SeriesID)
	wantErr(t, "başka tayfanın serisi", err, apperr.ErrEventSeriesNotFound)
	_, _, err = e.svc.EndSeries(ctx, admin2, tf2, v.SeriesID)
	wantErr(t, "başka tayfanın serisini bitirme", err, apperr.ErrEventSeriesNotFound)
}

// TestSeriesDaylightSaving: occurrences keep the local wall-clock time of
// the first one across a DST change (Europe/Istanbul has none; Berlin
// moves to summer time on 28 March 2027).
func TestSeriesDaylightSaving(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ber, err := time.LoadLocation("Europe/Berlin")
	must(t, err)
	now := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := e.at(t, now)
	start := time.Date(2027, 3, 18, 19, 0, 0, 0, ber) // Thursday, CET
	in := input()
	in.StartsAt, in.EndsAt = start, start.Add(2*time.Hour)
	in.Recurrence = &Recurrence{RRule: "FREQ=WEEKLY;COUNT=3", TimeZone: "Europe/Berlin"}
	v, err := svc.Create(ctx, admin, tf, in)
	must(t, err)
	evs := e.seriesEvents(t, v.SeriesID)
	if len(evs) != 3 {
		t.Fatalf("%d örnek", len(evs))
	}
	for i, want := range []string{"2027-03-18T18:00:00Z", "2027-03-25T18:00:00Z", "2027-04-01T17:00:00Z"} {
		got := evs[i].StartsAt.Time.UTC()
		if got.Format(time.RFC3339) != want || got.In(ber).Hour() != 19 {
			t.Errorf("örnek %d: %s (yerel %s), %s bekleniyordu", i, got.Format(time.RFC3339), got.In(ber).Format("15:04 MST"), want)
		}
		if evs[i].EndsAt.Time.Sub(evs[i].StartsAt.Time) != 2*time.Hour {
			t.Errorf("örnek %d süresi", i)
		}
	}
}

func TestSeriesOverrideAndUpdate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	in := weekly(withCapacity(1), "FREQ=WEEKLY")
	first := e.create(t, tf, admin, in)
	sid := first.SeriesID
	evs := e.seriesEvents(t, sid)
	e2, e3, e4, e5 := id.ID(evs[1].ID.Bytes), id.ID(evs[2].ID.Bytes), id.ID(evs[3].ID.Bytes), id.ID(evs[4].ID.Bytes)

	// "series" on a single event is refused; so is a series time change.
	single := e.create(t, tf, admin, input())
	_, err := e.svc.Update(ctx, admin, tf, single.ID, []string{FieldTitle}, Input{Title: "x", Series: true})
	wantField(t, "tek etkinlikte seri", err, "series")
	_, err = e.svc.Update(ctx, admin, tf, e3, []string{FieldTime}, Input{StartsAt: evs[2].StartsAt.Time, EndsAt: evs[2].EndsAt.Time, Series: true})
	wantField(t, "seri saati", err, "update_mask")

	// Editing one event alone marks it.
	v2, err := e.svc.Update(ctx, admin, tf, e2, []string{FieldTitle}, Input{Title: "Sadece bu hafta"})
	must(t, err)
	if !v2.SeriesOverride || v2.SeriesID != sid {
		t.Fatalf("tek düzenleme = %+v", v2)
	}
	// Moving an event onto another occurrence's start is refused.
	_, err = e.svc.Update(ctx, admin, tf, e4, []string{FieldTime}, Input{StartsAt: evs[4].StartsAt.Time, EndsAt: evs[4].EndsAt.Time})
	wantField(t, "çakışan saat", err, "start_time")

	// A waitlisted answer on e5, then a series update raising the capacity.
	_, err = e.svc.SetRSVP(ctx, admin, tf, e5, RSVPGoing)
	must(t, err)
	st, err := e.svc.SetRSVP(ctx, member, tf, e5, RSVPGoing)
	must(t, err)
	if st != RSVPWaitlist {
		t.Fatalf("bekleme = %s", st)
	}
	_, err = e.svc.Update(ctx, admin, tf, e3, []string{FieldTitle, FieldCapacity}, Input{Title: "Yeni ad", Capacity: 5, Series: true})
	must(t, err)
	for _, r := range e.seriesEvents(t, sid) {
		eid := id.ID(r.ID.Bytes)
		switch eid {
		case e2:
			if r.Title != "Sadece bu hafta" || r.Capacity.Int32 != 1 {
				t.Errorf("tek düzenlenen örnek değişti: %q %d", r.Title, r.Capacity.Int32)
			}
		default:
			if r.Title != "Yeni ad" || r.Capacity.Int32 != 5 || r.SeriesOverride {
				t.Errorf("örnek %s: %q %d %v", eid, r.Title, r.Capacity.Int32, r.SeriesOverride)
			}
		}
	}
	if got, err := e.q.GetRsvp(ctx, store.GetRsvpParams{EventID: e5.PG(), UserID: member.PG()}); err != nil || got.Status != RSVPGoing {
		t.Errorf("kapasite artınca terfi yok: %+v %v", got, err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'event_series.updated' AND target_id = $1`, sid.PG()); n != 1 {
		t.Errorf("seri denetim kaydı = %d", n)
	}

	// Editing the template alone moves the template to the next upcoming
	// occurrence that was not edited alone (e2 was), so the series keeps
	// its own content.
	_, err = e.svc.Update(ctx, admin, tf, first.ID, []string{FieldTitle}, Input{Title: "İlk hafta özel"})
	must(t, err)
	s, err := e.svc.GetSeries(ctx, admin, tf, sid)
	must(t, err)
	if s.TemplateEventID != e3 || !s.StartsAt.Equal(first.StartsAt) {
		t.Fatalf("şablon = %s (%s bekleniyordu), başlangıç %v", s.TemplateEventID, e3, s.StartsAt)
	}
	// New occurrences copy the series content.
	later := e.at(t, time.Now().Add(14*24*time.Hour+time.Hour))
	n, err := later.GenerateDueSeries(ctx)
	must(t, err)
	all := e.seriesEvents(t, sid)
	if n != 2 || all[len(all)-1].Title != "Yeni ad" || all[len(all)-1].Capacity.Int32 != 5 {
		t.Fatalf("yeni örnekler: %d, son = %q", n, all[len(all)-1].Title)
	}
}

func TestSeriesEnd(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	organizer := e.seed(t, tf, authz.Member)
	other := e.seed(t, tf, authz.Member)
	first := e.create(t, tf, organizer, weekly(input(), "FREQ=WEEKLY"))
	sid := first.SeriesID
	evs := e.seriesEvents(t, sid)
	kept := id.ID(evs[2].ID.Bytes)
	_, err := e.svc.Update(ctx, organizer, tf, kept, []string{FieldTitle}, Input{Title: "Özel hafta"})
	must(t, err)

	// Another member may not end it; the organizer may.
	_, _, err = e.svc.EndSeries(ctx, other, tf, sid)
	wantErr(t, "başka üye", err, apperr.ErrPermissionDenied)
	s, n, err := e.svc.EndSeries(ctx, organizer, tf, sid)
	must(t, err)
	if s.EndedAt.IsZero() || n != len(evs)-1 {
		t.Fatalf("sonlandırma: %+v, %d iptal (%d bekleniyordu)", s, n, len(evs)-1)
	}
	for _, r := range e.seriesEvents(t, sid) {
		want := StatusCancelled
		if id.ID(r.ID.Bytes) == kept {
			want = StatusScheduled
		}
		if r.Status != want {
			t.Errorf("örnek %s: %s, %s bekleniyordu", id.ID(r.ID.Bytes), r.Status, want)
		}
	}
	if c := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.cancelled' AND payload->>'series_id' = $1`, sid.String()); c != n {
		t.Errorf("iptal olayları = %d", c)
	}

	// Ended: nothing more is generated, the series no longer changes.
	e.exec(t, `UPDATE event_series SET generated_until = NULL WHERE id = $1`, sid.PG())
	if n, err := e.at(t, time.Now().AddDate(0, 2, 0)).GenerateDueSeries(ctx); err != nil || n != 0 {
		t.Fatalf("bitmiş seride üretim: %d %v", n, err)
	}
	_, _, err = e.svc.EndSeries(ctx, organizer, tf, sid)
	wantErr(t, "ikinci kez", err, apperr.ErrEventSeriesEnded)
	_, err = e.svc.Update(ctx, organizer, tf, kept, []string{FieldTitle}, Input{Title: "x", Series: true})
	wantErr(t, "bitmiş seri güncellemesi", err, apperr.ErrEventSeriesEnded)
	// The event edited alone still works on its own.
	_, err = e.svc.Update(ctx, organizer, tf, kept, []string{FieldTitle}, Input{Title: "Hâlâ var"})
	must(t, err)
}

// TestSeriesSkipsClosedTayfa: no occurrences while the events module is off,
// but the series does not stay due.
func TestSeriesSkipsClosedTayfa(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	v := e.create(t, tf, admin, weekly(input(), "FREQ=WEEKLY"))
	before := len(e.seriesEvents(t, v.SeriesID))
	e.exec(t, `UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'events'`, tf.PG())
	later := e.at(t, time.Now().Add(21*24*time.Hour+time.Hour))
	n, err := later.GenerateDueSeries(ctx)
	must(t, err)
	if n != 0 || len(e.seriesEvents(t, v.SeriesID)) != before {
		t.Fatalf("kapalı modülde %d örnek", n)
	}
	if n, err := later.GenerateDueSeries(ctx); err != nil || n != 0 {
		t.Fatalf("hâlâ sırada: %d %v", n, err)
	}
}

func TestExport(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	in := input()
	in.Title = "Ege turu, kahvaltı; yol"
	in.Description = "Satır 1\nLOCATION:sahte"
	v := e.create(t, tf, admin, in)

	ics, err := e.svc.Export(ctx, member, tf, v.ID)
	must(t, err)
	out := string(ics)
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "UID:" + v.ID.String() + "\r\n", `SUMMARY:Ege turu\, kahvaltı\; yol` + "\r\n",
		`DESCRIPTION:Satır 1\nLOCATION:sahte` + "\r\n", `LOCATION:Kordon\, Konak\, İzmir` + "\r\n",
		"DTSTART:" + v.StartsAt.UTC().Format("20060102T150405Z") + "\r\n", "STATUS:CONFIRMED\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q yok:\n%s", want, out)
		}
	}
	// Coordinates never go into the file.
	for _, secret := range []string{"GEO", "38.4192", "27.1287"} {
		if strings.Contains(out, secret) {
			t.Errorf("dosyada koordinat: %s", out)
		}
	}

	// Online: the link is included for members.
	on := input()
	on.Location = Location{Mode: ModeOnline, OnlineURL: "https://meet.example.com/tayfa"}
	ov := e.create(t, tf, admin, on)
	ics, err = e.svc.Export(ctx, member, tf, ov.ID)
	must(t, err)
	if !strings.Contains(string(ics), "\r\nURL:https://meet.example.com/tayfa\r\n") {
		t.Errorf("çevrimiçi bağlantı yok: %s", ics)
	}
	// Non-members get nothing, the link included.
	_, err = e.svc.Export(ctx, id.New(), tf, ov.ID)
	wantErr(t, "yabancı", err, apperr.ErrTayfaNotFound)

	// A participants-only fishing spot stays out for others.
	fi := input()
	fi.Type = TypeFishing
	fi.Details = Details{Fishing: &Fishing{SpotVisibility: SpotParticipants}}
	fv := e.create(t, tf, admin, fi)
	ics, err = e.svc.Export(ctx, member, tf, fv.ID)
	must(t, err)
	if strings.Contains(string(ics), "Kordon") || !strings.Contains(string(ics), `LOCATION:Konak\, İzmir`) {
		t.Errorf("gizli mera: %s", ics)
	}
	ics, err = e.svc.Export(ctx, admin, tf, fv.ID)
	must(t, err)
	if !strings.Contains(string(ics), "Kordon") {
		t.Errorf("organizatör merayı görmüyor: %s", ics)
	}

	// Cancelled.
	_, err = e.svc.Cancel(ctx, admin, tf, v.ID, "yağmur")
	must(t, err)
	ics, err = e.svc.Export(ctx, member, tf, v.ID)
	must(t, err)
	if !strings.Contains(string(ics), "\r\nSTATUS:CANCELLED\r\n") {
		t.Errorf("iptal: %s", ics)
	}
	// Module off.
	e.exec(t, `UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'events'`, tf.PG())
	_, err = e.svc.Export(ctx, member, tf, v.ID)
	wantErr(t, "modül kapalı", err, apperr.ErrModuleDisabled)
}

// A series update refreshes the activity records of its events, like a
// single edit (issue #50): check-in opens before the start, so an
// upcoming event of the series can already have attendance.
func TestSeriesUpdateRefreshesActivity(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	first := e.create(t, tf, admin, weekly(input(), "FREQ=WEEKLY"))
	u := e.going(t, tf, first.ID, authz.Member)
	c := &clock{t: first.StartsAt.Add(-10 * time.Minute)}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, u, tf, first.ID, izmir)
	must(t, err)
	_, err = svc.Update(ctx, admin, tf, first.ID, []string{FieldTitle}, Input{Title: "Seri başlığı", Series: true})
	must(t, err)
	var title string
	must(t, e.pool.QueryRow(ctx, `SELECT event_title FROM user_activity_records WHERE user_id = $1`, u.PG()).Scan(&title))
	if title != "Seri başlığı" {
		t.Fatalf("seri güncellemesinde geçmiş güncellenmedi: %q", title)
	}
}

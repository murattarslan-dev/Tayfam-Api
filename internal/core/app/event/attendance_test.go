package event

import (
	"bytes"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// clock is a settable time source for a Service.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// clocked returns a Service on e's database whose time is c and whose log
// goes to logs (JSON, every level).
func (e *env) clocked(t *testing.T, c *clock, logs *bytes.Buffer) *Service {
	t.Helper()
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	svc, err := New(Config{Pool: e.pool, Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: c.now})
	must(t, err)
	return svc
}

// north and east return the point d metres north or east of p, exactly
// for the haversine formula used by distanceM.
func north(p Point, d float64) *Point {
	return &Point{Lat: p.Lat + d/earthRadiusM*180/math.Pi, Lng: p.Lng}
}

func east(p Point, d float64) *Point {
	rad := math.Pi / 180
	dLng := 2 * math.Asin(math.Sin(d/(2*earthRadiusM))/math.Cos(p.Lat*rad))
	return &Point{Lat: p.Lat, Lng: p.Lng + dLng/rad}
}

func (e *env) going(t *testing.T, tf, ev id.ID, role authz.Role) id.ID {
	t.Helper()
	u := e.seed(t, tf, role)
	st, err := e.svc.SetRSVP(ctx, u, tf, ev, RSVPGoing)
	must(t, err)
	if st != RSVPGoing {
		t.Fatalf("katılım = %s", st)
	}
	return u
}

func (e *env) attendance(t *testing.T, ev, u id.ID) (status, method string, attempts int) {
	t.Helper()
	var m *string
	must(t, e.pool.QueryRow(ctx, `SELECT attendance, attendance_method, check_in_attempts FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG()).Scan(&status, &m, &attempts))
	if m != nil {
		method = *m
	}
	return status, method, attempts
}

// ---------------------------------------------------------------------------

func TestDistance(t *testing.T) {
	a := Point{Lat: 38, Lng: 27}
	// One degree of latitude on the mean sphere: 111 195 m.
	if d := distanceM(a, Point{Lat: 39, Lng: 27}); math.Abs(d-111195.08) > 0.1 {
		t.Errorf("1 derece = %.2f m", d)
	}
	// Ankara–İstanbul (Kızılay–Taksim): about 350 km.
	if d := distanceM(Point{39.9208, 32.8541}, Point{41.0369, 28.9850}); d < 345e3 || d > 355e3 {
		t.Errorf("Ankara–İstanbul = %.0f m", d)
	}
	if d := distanceM(a, a); d != 0 {
		t.Errorf("aynı nokta = %v", d)
	}
	if distanceM(a, *izmir) != distanceM(*izmir, a) {
		t.Error("simetrik değil")
	}
	// Antipodes do not break the formula.
	if d := distanceM(Point{0, 0}, Point{0, 180}); math.Abs(d-math.Pi*earthRadiusM) > 1 {
		t.Errorf("karşı kutup = %v", d)
	}
	// The limit, both ways, in both directions.
	for _, tc := range []struct {
		name string
		p    *Point
		want bool
	}{
		{"kuzey 299 m", north(*izmir, 299), true},
		{"kuzey 300 m", north(*izmir, 300), true},
		{"kuzey 301 m", north(*izmir, 301), false},
		{"doğu 299 m", east(*izmir, 299), true},
		{"doğu 301 m", east(*izmir, 301), false},
		{"güney 301 m", north(*izmir, -301), false},
		{"batı 299 m", east(*izmir, -299), true},
	} {
		if got := withinCheckIn(*izmir, *tc.p); got != tc.want {
			t.Errorf("%s: %v (%.3f m)", tc.name, got, distanceM(*izmir, *tc.p))
		}
	}
}

func TestActivityMinutes(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want int32
	}{
		{-time.Hour, 0},
		{0, 0},
		{3 * time.Hour, 180},
		{12 * time.Hour, 720},
		{20 * time.Hour, 720},               // one day: at most 12 hours
		{30 * time.Hour, 24 * 60},           // two started days: 24 h cap
		{48 * time.Hour, 24 * 60},           // two days
		{60 * 24 * time.Hour, 30 * 12 * 60}, // overall cap
	} {
		if got := activityMinutes(tc.d); got != tc.want {
			t.Errorf("%v: %d, %d bekleniyordu", tc.d, got, tc.want)
		}
	}
}

// TestCheckInLocation covers the window, the 300 m limit and the record.
func TestCheckInLocation(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, withCapacity(5))
	g := e.going(t, tf, ev.ID, authz.Member)
	maybe := e.seed(t, tf, authz.Member)
	_, err := e.svc.SetRSVP(ctx, maybe, tf, ev.ID, RSVPMaybe)
	must(t, err)
	none := e.seed(t, tf, authz.Member)
	c := &clock{t: ev.StartsAt.Add(-checkInLead - time.Microsecond)}
	svc := e.clocked(t, c, nil)
	near := north(*izmir, 299)

	_, err = svc.CheckIn(ctx, g, tf, ev.ID, near)
	wantErr(t, "pencereden önce", err, apperr.ErrCheckInClosed)

	c.t = ev.StartsAt.Add(-checkInLead)
	_, err = svc.CheckIn(ctx, g, tf, ev.ID, nil)
	wantField(t, "konumsuz yüz yüze", err, "location")
	_, err = svc.CheckIn(ctx, g, tf, ev.ID, &Point{Lat: 91, Lng: 0})
	wantField(t, "aralık dışı konum", err, "location")
	_, err = svc.CheckIn(ctx, g, tf, ev.ID, &Point{Lat: math.NaN(), Lng: 0})
	wantField(t, "NaN konum", err, "location")
	for name, u := range map[string]id.ID{"belki": maybe, "yanıtsız": none} {
		_, err = svc.CheckIn(ctx, u, tf, ev.ID, near)
		wantErr(t, name, err, apperr.ErrEventNotGoing)
	}

	_, err = svc.CheckIn(ctx, g, tf, ev.ID, north(*izmir, 301))
	wantErr(t, "301 m", err, apperr.ErrCheckInTooFar)
	if st, _, n := e.attendance(t, ev.ID, g); st != AttendanceUnknown || n != 1 {
		t.Fatalf("başarısız deneme sayılmadı: %s %d", st, n)
	}
	a, err := svc.CheckIn(ctx, g, tf, ev.ID, near)
	must(t, err)
	if a.Status != AttendanceAttended || a.Method != MethodLocation {
		t.Fatalf("yoklama = %+v", a)
	}
	// Again: nothing changes and no attempt counts.
	a, err = svc.CheckIn(ctx, g, tf, ev.ID, north(*izmir, 5000))
	must(t, err)
	if st, m, n := e.attendance(t, ev.ID, g); a.Status != AttendanceAttended || st != AttendanceAttended || m != MethodLocation || n != 2 {
		t.Fatalf("tekrar = %+v %s %s %d", a, st, m, n)
	}
	var checkedIn time.Time
	must(t, e.pool.QueryRow(ctx, `SELECT checked_in_at FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.ID.PG(), g.PG()).Scan(&checkedIn))
	if !checkedIn.Equal(c.t) {
		t.Errorf("checked_in_at = %v", checkedIn)
	}

	// The activity record is a snapshot: type, mode, title, city, district,
	// start, duration.
	var typ, mode, title, city, district, method string
	var started time.Time
	var minutes int
	must(t, e.pool.QueryRow(ctx, `SELECT event_type, mode, event_title, city, district, started_at, duration_minutes, attendance_method, tayfa_id = $3
		FROM user_activity_records WHERE user_id = $1 AND event_id = $2`, g.PG(), ev.ID.PG(), tf.PG()).
		Scan(&typ, &mode, &title, &city, &district, &started, &minutes, &method, new(bool)))
	if typ != TypeGeneral || mode != ModeInPerson || title != "Pazar buluşması" || city != "İzmir" || district != "Konak" ||
		!started.Equal(ev.StartsAt) || minutes != 180 || method != MethodLocation {
		t.Fatalf("geçmiş = %s %s %s %s %s %v %d %s", typ, mode, title, city, district, started, minutes, method)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'attendance.recorded' AND subject_id = $1 AND actor_id = $2 AND payload->>'method' = 'location'`, ev.ID.PG(), g.PG()); n != 1 {
		t.Errorf("olay = %d", n)
	}
	got, err := svc.Get(ctx, g, tf, ev.ID)
	must(t, err)
	if got.CallerAttendance != AttendanceAttended {
		t.Errorf("caller_attendance = %s", got.CallerAttendance)
	}
	list, _, err := svc.ListAttendees(ctx, admin, tf, ev.ID, RSVPGoing, 0, "")
	must(t, err)
	for _, x := range list {
		if x.UserID == g && (x.Attendance != AttendanceAttended || x.AttendanceMethod != MethodLocation) {
			t.Errorf("katılımcı listesi = %+v", x)
		}
	}

	// The window closes at the end.
	late := e.going(t, tf, ev.ID, authz.Member)
	c.t = ev.EndsAt.Add(-time.Microsecond)
	_, err = svc.CheckIn(ctx, late, tf, ev.ID, near)
	must(t, err)
	c.t = ev.EndsAt
	_, err = svc.CheckIn(ctx, e.going(t, tf, ev.ID, authz.Member), tf, ev.ID, near)
	wantErr(t, "bitişte", err, apperr.ErrCheckInClosed)
}

func TestCheckInAttemptLimit(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	g := e.going(t, tf, ev.ID, authz.Member)
	svc := e.clocked(t, &clock{t: ev.StartsAt}, nil)
	for i := range 5 {
		_, err := svc.CheckIn(ctx, g, tf, ev.ID, east(*izmir, 400))
		wantErr(t, fmt.Sprintf("deneme %d", i+1), err, apperr.ErrCheckInTooFar)
	}
	// The sixth attempt is refused even from the right place.
	_, err := svc.CheckIn(ctx, g, tf, ev.ID, izmir)
	wantErr(t, "6. deneme", err, apperr.ErrCheckInAttempts)
	if st, _, n := e.attendance(t, ev.ID, g); st != AttendanceUnknown || n != 5 {
		t.Fatalf("deneme sınırı = %s %d", st, n)
	}
	// Per user and event: someone else still has five.
	other := e.going(t, tf, ev.ID, authz.Member)
	_, err = svc.CheckIn(ctx, other, tf, ev.ID, izmir)
	must(t, err)
	// Changing the answer does not reset the counter.
	_, err = svc.SetRSVP(ctx, g, tf, ev.ID, RSVPNotGoing)
	must(t, err)
	_, err = svc.SetRSVP(ctx, g, tf, ev.ID, RSVPGoing)
	must(t, err)
	_, err = svc.CheckIn(ctx, g, tf, ev.ID, izmir)
	wantErr(t, "yanıt değişince", err, apperr.ErrCheckInAttempts)
}

// TestCheckInLocationNeverStored: the location sent with a check-in reaches
// no table, no log line, no audit record, no event and no error.
func TestCheckInLocationNeverStored(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	g := e.going(t, tf, ev.ID, authz.Member)
	logs := &bytes.Buffer{}
	svc := e.clocked(t, &clock{t: ev.StartsAt}, logs)

	far := north(*east(*izmir, 900), 900)
	near := north(*east(*izmir, 150), 150)
	var errs []string
	for _, p := range []*Point{far, far, near} {
		_, err := svc.CheckIn(ctx, g, tf, ev.ID, p)
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if st, _, _ := e.attendance(t, ev.ID, g); st != AttendanceAttended || len(errs) != 2 {
		t.Fatalf("yoklama = %s, hatalar %v", st, errs)
	}

	secrets := map[string]bool{}
	for _, p := range []*Point{far, near} {
		for _, f := range []string{"%.4f", "%.5f"} {
			secrets[fmt.Sprintf(f, p.Lat)] = true
			secrets[fmt.Sprintf(f, p.Lng)] = true
		}
		secrets[fmt.Sprint(p.Lat)] = true
		secrets[fmt.Sprint(p.Lng)] = true
	}
	// The meeting point itself is stored with the event; make sure the
	// secrets are not just its digits.
	for s := range secrets {
		if strings.HasPrefix(fmt.Sprint(izmir.Lat), s) || strings.HasPrefix(fmt.Sprint(izmir.Lng), s) {
			t.Fatalf("test noktası buluşma noktasından ayırt edilemiyor: %s", s)
		}
	}

	// Every row of every table, as text.
	rows, err := e.pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	must(t, err)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	var dump strings.Builder
	for _, name := range tables {
		var s string
		must(t, e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(t::text, E'\n'), '') FROM `+pgx.Identifier{"public", name}.Sanitize()+` t`).Scan(&s))
		dump.WriteString(s)
	}
	if !strings.Contains(dump.String(), fmt.Sprint(izmir.Lat)) {
		t.Fatal("döküm buluşma noktasını içermiyor; döküm çalışmıyor")
	}
	for s := range secrets {
		if strings.Contains(dump.String(), s) {
			t.Errorf("gönderilen konum veritabanında: %s", s)
		}
		if strings.Contains(logs.String(), s) {
			t.Errorf("gönderilen konum log'da: %s", s)
		}
		for _, msg := range errs {
			if strings.Contains(msg, s) {
				t.Errorf("gönderilen konum hata mesajında: %s", msg)
			}
		}
	}
}

func TestCheckInOnline(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	in := input()
	in.Type = TypeGaming
	in.Location = Location{Mode: ModeOnline, OnlineURL: "https://discord.gg/tayfa"}
	in.Details = Details{Gaming: &Gaming{Game: "CS2", Platform: "pc"}}
	ev := e.create(t, tf, admin, in)
	early := e.going(t, tf, ev.ID, authz.Member)
	tooLate := e.going(t, tf, ev.ID, authz.Member)
	// tooLate said going only after the start.
	e.exec(t, `UPDATE event_rsvps SET status_changed_at = $3 WHERE event_id = $1 AND user_id = $2`, ev.ID.PG(), tooLate.PG(), ev.StartsAt.Add(time.Minute))
	svc := e.clocked(t, &clock{t: ev.StartsAt.Add(10 * time.Minute)}, nil)

	_, err := svc.CheckIn(ctx, early, tf, ev.ID, izmir)
	wantField(t, "online etkinlikte konum", err, "location")
	a, err := svc.CheckIn(ctx, early, tf, ev.ID, nil)
	must(t, err)
	if a.Method != MethodOnline {
		t.Fatalf("yöntem = %s", a.Method)
	}
	_, err = svc.CheckIn(ctx, tooLate, tf, ev.ID, nil)
	wantErr(t, "başladıktan sonra geliyorum", err, apperr.ErrEventNotGoing)
	// No attempts are counted online.
	if _, _, n := e.attendance(t, ev.ID, early); n != 0 {
		t.Errorf("online deneme sayıldı: %d", n)
	}
	var city *string
	must(t, e.pool.QueryRow(ctx, `SELECT city FROM user_activity_records WHERE user_id = $1`, early.PG()).Scan(&city))
	if city != nil {
		t.Errorf("online kayıtta il = %v", *city)
	}
}

// The status_changed_at trigger follows status changes only.
func TestStatusChangedAt(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	u := e.going(t, tf, ev.ID, authz.Member)
	e.exec(t, `UPDATE event_rsvps SET status_changed_at = '2026-01-01' WHERE event_id = $1`, ev.ID.PG())
	read := func() time.Time {
		var at time.Time
		must(t, e.pool.QueryRow(ctx, `SELECT status_changed_at FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.ID.PG(), u.PG()).Scan(&at))
		return at
	}
	svc := e.clocked(t, &clock{t: ev.StartsAt}, nil)
	_, err := svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
	if read().Year() != 2026 || read().Month() != 1 {
		t.Fatal("yoklama status_changed_at'i değiştirdi")
	}
	_, err = e.svc.SetRSVP(ctx, u, tf, ev.ID, RSVPMaybe)
	must(t, err)
	if read().Month() == 1 {
		t.Fatal("durum değişikliği status_changed_at'i ilerletmedi")
	}
}

func TestMutedMemberChecksIn(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	u := e.going(t, tf, ev.ID, authz.Member)
	e.mute(t, tf, u)
	svc := e.clocked(t, &clock{t: ev.StartsAt}, nil)
	_, err := svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
}

func hikeInput() Input {
	in := input()
	in.Type = TypeHike
	in.Details = Details{Hike: &Hike{Difficulty: 3}}
	return in
}

func TestHikeRollCall(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, hikeInput())
	u := e.going(t, tf, ev.ID, authz.Member)
	c := &clock{t: ev.StartsAt.Add(-10 * time.Minute)}
	svc := e.clocked(t, c, nil)

	_, err := svc.FinishHike(ctx, u, tf, ev.ID, time.Time{})
	wantErr(t, "başlamadan dönüş", err, apperr.ErrHikeNotStarted)
	_, err = svc.StartHike(ctx, u, tf, ev.ID, c.t.Add(clientSkew+time.Second))
	wantField(t, "gelecekte çıkış", err, "start_time")
	_, err = svc.StartHike(ctx, u, tf, ev.ID, c.t.Add(-clientMaxAge-time.Second))
	wantField(t, "48 saatten eski çıkış", err, "start_time")
	_, err = svc.StartHike(ctx, e.seed(t, tf, authz.Member), tf, ev.ID, time.Time{})
	wantErr(t, "geliyorum demeyen", err, apperr.ErrEventNotGoing)

	start := c.t.Add(-time.Minute)
	h, err := svc.StartHike(ctx, u, tf, ev.ID, start)
	must(t, err)
	if !h.StartedAt.Equal(start) || !h.ReturnedAt.IsZero() {
		t.Fatalf("çıkış = %+v", h)
	}
	if st, m, _ := e.attendance(t, ev.ID, u); st != AttendanceAttended || m != MethodHikeStart {
		t.Fatalf("çıkış katılım sayılmadı: %s %s", st, m)
	}
	// A repeat returns the first start.
	again, err := svc.StartHike(ctx, u, tf, ev.ID, time.Time{})
	must(t, err)
	if !again.StartedAt.Equal(start) {
		t.Fatalf("tekrar çıkış = %+v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'hike.started'`); n != 1 {
		t.Fatalf("çıkış olayı = %d", n)
	}
	var started time.Time
	must(t, e.pool.QueryRow(ctx, `SELECT started_at FROM user_activity_records WHERE user_id = $1`, u.PG()).Scan(&started))
	if !started.Equal(start) {
		t.Errorf("geçmiş başlangıcı = %v", started)
	}

	// Back 150 minutes later: the real duration replaces the planned one.
	c.t = start.Add(150 * time.Minute)
	_, err = svc.FinishHike(ctx, u, tf, ev.ID, start.Add(-time.Second))
	wantField(t, "çıkıştan önce dönüş", err, "return_time")
	h, err = svc.FinishHike(ctx, u, tf, ev.ID, time.Time{})
	must(t, err)
	if !h.ReturnedAt.Equal(c.t) {
		t.Fatalf("dönüş = %+v", h)
	}
	var minutes int
	must(t, e.pool.QueryRow(ctx, `SELECT duration_minutes FROM user_activity_records WHERE user_id = $1`, u.PG()).Scan(&minutes))
	if minutes != 150 {
		t.Errorf("süre = %d", minutes)
	}
	again, err = svc.FinishHike(ctx, u, tf, ev.ID, time.Time{})
	must(t, err)
	if !again.ReturnedAt.Equal(h.ReturnedAt) {
		t.Error("tekrar dönüş değişti")
	}

	// Someone who checked in by location and then starts: the method
	// stays, the record starts with the hike.
	loc := e.going(t, tf, ev.ID, authz.Member)
	c.t = ev.StartsAt
	_, err = svc.CheckIn(ctx, loc, tf, ev.ID, izmir)
	must(t, err)
	_, err = svc.StartHike(ctx, loc, tf, ev.ID, time.Time{})
	must(t, err)
	if _, m, _ := e.attendance(t, ev.ID, loc); m != MethodLocation {
		t.Errorf("yöntem = %s", m)
	}

	// A late return after the cancellation and after the end still works;
	// after the correction window the record is frozen.
	late := e.going(t, tf, ev.ID, authz.Member)
	notOut := e.going(t, tf, ev.ID, authz.Member)
	_, err = svc.StartHike(ctx, late, tf, ev.ID, time.Time{})
	must(t, err)
	_, err = e.svc.Cancel(ctx, admin, tf, ev.ID, "")
	must(t, err)
	_, err = svc.StartHike(ctx, notOut, tf, ev.ID, time.Time{})
	wantErr(t, "iptal sonrası çıkış", err, apperr.ErrEventCancelled)
	c.t = ev.EndsAt.Add(correctionWindow + time.Hour)
	_, err = svc.FinishHike(ctx, late, tf, ev.ID, time.Time{})
	must(t, err)
	must(t, e.pool.QueryRow(ctx, `SELECT duration_minutes FROM user_activity_records WHERE user_id = $1`, late.PG()).Scan(&minutes))
	if minutes != 180 {
		t.Errorf("donmuş kayıt değişti: %d", minutes)
	}

	// Not a hike.
	other := e.create(t, tf, admin, input())
	_, err = svc.StartHike(ctx, admin, tf, other.ID, time.Time{})
	wantErr(t, "yürüyüş değil", err, apperr.ErrEventTypeMismatch)
	_, err = svc.FinishHike(ctx, admin, tf, other.ID, time.Time{})
	wantErr(t, "yürüyüş değil dönüş", err, apperr.ErrEventTypeMismatch)
}

func TestOrganizerCorrection(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	e.allowMembers(t, tf, admin)
	organizer := e.seed(t, tf, authz.Member)
	ev := e.create(t, tf, organizer, input())
	faker := e.going(t, tf, ev.ID, authz.Member)
	walkIn := e.seed(t, tf, authz.Member)
	bystander := e.seed(t, tf, authz.Member)
	mod := e.seed(t, tf, authz.Moderator)
	c := &clock{t: ev.StartsAt}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, faker, tf, ev.ID, izmir)
	must(t, err)

	c.t = ev.EndsAt.Add(-time.Microsecond)
	wantErr(t, "bitmeden", svc.SetAttendance(ctx, organizer, tf, ev.ID, walkIn, true), apperr.ErrCorrectionClosed)

	c.t = ev.EndsAt
	wantErr(t, "sıradan üye", svc.SetAttendance(ctx, bystander, tf, ev.ID, walkIn, true), apperr.ErrPermissionDenied)
	wantErr(t, "kendi katılımı", svc.SetAttendance(ctx, organizer, tf, ev.ID, organizer, true), apperr.ErrPermissionDenied)
	wantErr(t, "üye olmayan", svc.SetAttendance(ctx, organizer, tf, ev.ID, id.New(), true), apperr.ErrMemberNotFound)
	wantField(t, "boş kişi", svc.SetAttendance(ctx, organizer, tf, ev.ID, id.ID{}, true), "user_id")

	// The organizer adds the walk-in (no answer before) …
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, walkIn, true))
	var status, method string
	var by *string
	must(t, e.pool.QueryRow(ctx, `SELECT status, attendance_method, attendance_marked_by::text FROM event_rsvps WHERE event_id = $1 AND user_id = $2 AND attendance = 'attended'`,
		ev.ID.PG(), walkIn.PG()).Scan(&status, &method, &by))
	if status != RSVPGoing || method != MethodOrganizer || by == nil || *by != organizer.String() {
		t.Fatalf("eklenen = %s %s %v", status, method, by)
	}
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE user_id = $1 AND attendance_method = 'organizer'`, walkIn.PG()); n != 1 {
		t.Fatal("eklenen kişinin geçmiş kaydı yok")
	}
	// … and a moderator removes the fake check-in.
	must(t, svc.SetAttendance(ctx, mod, tf, ev.ID, faker, false))
	if st, m, _ := e.attendance(t, ev.ID, faker); st != AttendanceNoShow || m != MethodOrganizer {
		t.Fatalf("kaldırılan = %s %s", st, m)
	}
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE user_id = $1`, faker.PG()); n != 0 {
		t.Fatal("kaldırılan katılımın geçmiş kaydı silinmedi")
	}
	// Repeats change nothing.
	audits := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'attendance.corrected'`)
	must(t, svc.SetAttendance(ctx, mod, tf, ev.ID, faker, false))
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, walkIn, true))
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, bystander, false))
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'attendance.corrected'`); n != audits || audits != 2 {
		t.Fatalf("denetim kayıtları = %d (önce %d)", n, audits)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'attendance.corrected' AND actor_id = $1 AND metadata->>'user_id' = $2 AND metadata->>'attended' = 'false'`, mod.PG(), faker.String()); n != 1 {
		t.Fatal("kaldırmanın denetim kaydı yok")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'attendance.removed' AND payload->>'user_id' = $1`, faker.String()); n != 1 {
		t.Fatal("kaldırma olayı yok")
	}
	// The organizer may add back what a moderator removed.
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, faker, true))

	// The window closes seven days after the end.
	c.t = ev.EndsAt.Add(correctionWindow - time.Microsecond)
	must(t, svc.SetAttendance(ctx, organizer, tf, ev.ID, bystander, true))
	c.t = ev.EndsAt.Add(correctionWindow)
	wantErr(t, "7 gün sonra", svc.SetAttendance(ctx, organizer, tf, ev.ID, bystander, false), apperr.ErrCorrectionClosed)

	// A muted organizer cannot correct; a cancelled event cannot be
	// corrected.
	ev2 := e.create(t, tf, organizer, input())
	c.t = ev2.EndsAt
	e.exec(t, `UPDATE memberships SET muted_until = $3, muted_by = $4 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), organizer.PG(), ev2.EndsAt.Add(time.Hour), admin.PG())
	wantErr(t, "susturulmuş organizatör", svc.SetAttendance(ctx, organizer, tf, ev2.ID, walkIn, true), apperr.ErrMuted)
	e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, ev2.ID.PG())
	wantErr(t, "iptal", svc.SetAttendance(ctx, admin, tf, ev2.ID, walkIn, true), apperr.ErrEventCancelled)
}

// Activity records follow edits of the event until the correction window
// closes.
func TestActivityFollowsEdits(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, input())
	u := e.going(t, tf, ev.ID, authz.Member)
	c := &clock{t: ev.StartsAt.Add(-10 * time.Minute)}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
	in := input()
	in.Title = "Yeni başlık"
	in.Location = inPerson()
	in.Location.District = "Karşıyaka"
	_, err = svc.Update(ctx, admin, tf, ev.ID, []string{FieldTitle, FieldLocation}, in)
	must(t, err)
	var title, district string
	must(t, e.pool.QueryRow(ctx, `SELECT event_title, district FROM user_activity_records WHERE user_id = $1`, u.PG()).Scan(&title, &district))
	if title != "Yeni başlık" || district != "Karşıyaka" {
		t.Fatalf("geçmiş güncellenmedi: %s %s", title, district)
	}
	// The record outlives the event.
	// Not e.exec: foreign keys are triggers, which replica mode turns off.
	_, err = e.pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, ev.ID.PG())
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM user_activity_records WHERE user_id = $1 AND event_id IS NULL AND event_title = 'Yeni başlık'`, u.PG()); n != 1 {
		t.Fatal("etkinlik silinince geçmiş kaydı gitti")
	}
}

// Writes record activity in the tayfa (last_activity_at).
func TestTayfaActivity(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	stale := func() {
		e.exec(t, `UPDATE tayfas SET last_activity_at = $2 WHERE id = $1`, tf.PG(), old)
	}
	fresh := func(what string) {
		t.Helper()
		var at time.Time
		must(t, e.pool.QueryRow(ctx, `SELECT last_activity_at FROM tayfas WHERE id = $1`, tf.PG()).Scan(&at))
		if at.Before(old.Add(time.Hour)) {
			t.Errorf("%s tayfa aktivitesini güncellemedi", what)
		}
	}
	stale()
	ev := e.create(t, tf, admin, input())
	fresh("etkinlik oluşturma")
	stale()
	u := e.going(t, tf, ev.ID, authz.Member)
	fresh("katılım")
	stale()
	svc := e.clocked(t, &clock{t: ev.StartsAt}, nil)
	_, err := svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
	fresh("check-in")
}

func fishingInput(spot string) Input {
	in := input()
	in.Type = TypeFishing
	in.Details = Details{Fishing: &Fishing{SpotVisibility: spot}}
	return in
}

func TestFishingCatches(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev := e.create(t, tf, admin, fishingInput(SpotParticipants))
	angler := e.going(t, tf, ev.ID, authz.Member)
	participant := e.going(t, tf, ev.ID, authz.Member)
	outsider := e.seed(t, tf, authz.Member)
	c := &clock{t: ev.StartsAt}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, angler, tf, ev.ID, izmir)
	must(t, err)
	spot := &Point{Lat: 38.3512, Lng: 26.7731}
	add := func(u id.ID, in CatchInput) (Catch, error) {
		if in.ClientRequestID == "" {
			in.ClientRequestID = id.New().String()
		}
		return svc.AddCatch(ctx, u, tf, ev.ID, in)
	}

	_, err = add(participant, CatchInput{Species: "Levrek", LengthCm: 40})
	wantErr(t, "katılımı doğrulanmamış", err, apperr.ErrNotAttended)
	for name, tc := range map[string]struct {
		in    CatchInput
		field string
	}{
		"boş tür":        {CatchInput{Species: " ", LengthCm: 40}, "species"},
		"sıfır boy":      {CatchInput{Species: "Levrek"}, "length_cm"},
		"NaN boy":        {CatchInput{Species: "Levrek", LengthCm: math.NaN()}, "length_cm"},
		"dev boy":        {CatchInput{Species: "Levrek", LengthCm: 1000.1}, "length_cm"},
		"bozuk konum":    {CatchInput{Species: "Levrek", LengthCm: 40, Location: &Point{Lat: 100}}, "location"},
		"bozuk görünüm":  {CatchInput{Species: "Levrek", LengthCm: 40, Visibility: "public"}, "visibility"},
		"bozuk istek id": {CatchInput{Species: "Levrek", LengthCm: 40, ClientRequestID: "x"}, "client_request_id"},
	} {
		_, err := add(angler, tc.in)
		wantField(t, name, err, tc.field)
	}

	crid := id.New().String()
	shared, err := add(angler, CatchInput{Species: "Levrek", LengthCm: 42.46, Location: spot, Visibility: CatchTayfa, ClientRequestID: crid})
	must(t, err)
	if shared.LengthCm != 42.5 || shared.Location == nil || shared.Visibility != CatchTayfa {
		t.Fatalf("av = %+v", shared)
	}
	again, err := add(angler, CatchInput{Species: "Levrek", LengthCm: 42.46, Location: spot, Visibility: CatchTayfa, ClientRequestID: crid})
	must(t, err)
	if again.ID != shared.ID || e.count(t, `SELECT count(*) FROM fishing_catches`) != 1 {
		t.Fatal("tekrar gönderim ikinci av kaydı açtı")
	}
	private, err := add(angler, CatchInput{Species: "Çipura", LengthCm: 25, Location: spot})
	must(t, err)
	if private.Visibility != CatchSelf {
		t.Fatalf("varsayılan görünürlük = %s", private.Visibility)
	}

	locations := func(u id.ID) map[id.ID]bool {
		t.Helper()
		list, _, err := svc.ListCatches(ctx, u, tf, ev.ID, 0, "")
		must(t, err)
		if len(list) != 2 {
			t.Fatalf("liste = %d", len(list))
		}
		out := map[id.ID]bool{}
		for _, x := range list {
			out[x.ID] = x.Location != nil
		}
		return out
	}
	if l := locations(angler); !l[shared.ID] || !l[private.ID] {
		t.Errorf("sahibi = %v", l)
	}
	if l := locations(participant); !l[shared.ID] || l[private.ID] {
		t.Errorf("katılımcı = %v", l)
	}
	// The spot is for participants only: an outsider sees no location.
	if l := locations(outsider); l[shared.ID] || l[private.ID] {
		t.Errorf("katılımcı olmayan = %v", l)
	}

	// The location never reaches the audit record or the event.
	var meta, payload string
	must(t, e.pool.QueryRow(ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_logs WHERE action = 'fishing_catch.added'`).Scan(&meta))
	must(t, e.pool.QueryRow(ctx, `SELECT string_agg(payload::text, ' ') FROM domain_events WHERE type = 'fishing_catch.added'`).Scan(&payload))
	for _, s := range []string{meta, payload} {
		if strings.Contains(s, "38.35") || strings.Contains(s, "26.77") || strings.Contains(s, "Levrek") {
			t.Errorf("kayıtta konum/içerik: %s", s)
		}
	}

	wantErr(t, "başkasının avı", svc.RemoveCatch(ctx, participant, tf, ev.ID, shared.ID), apperr.ErrPermissionDenied)
	must(t, svc.RemoveCatch(ctx, angler, tf, ev.ID, private.ID))
	wantErr(t, "silinmiş av", svc.RemoveCatch(ctx, angler, tf, ev.ID, private.ID), apperr.ErrEventItemNotFound)
	must(t, svc.RemoveCatch(ctx, admin, tf, ev.ID, shared.ID))
	list, _, err := svc.ListCatches(ctx, angler, tf, ev.ID, 0, "")
	must(t, err)
	if len(list) != 0 {
		t.Fatalf("silinenler listede: %d", len(list))
	}

	// Catches close with the correction window; other types refuse.
	c.t = ev.EndsAt.Add(correctionWindow)
	_, err = add(angler, CatchInput{Species: "Levrek", LengthCm: 40})
	wantErr(t, "pencere sonrası", err, apperr.ErrEventEnded)
	general := e.create(t, tf, admin, input())
	_, err = svc.AddCatch(ctx, angler, tf, general.ID, CatchInput{Species: "Levrek", LengthCm: 40, ClientRequestID: id.New().String()})
	wantErr(t, "balık etkinliği değil", err, apperr.ErrEventTypeMismatch)
}

// TestAttendanceIDOR: an event is reached only through its own tayfa, and
// a correction only targets members of that tayfa.
func TestAttendanceIDOR(t *testing.T) {
	e := setup(t)
	tfA, adminA := e.newTayfa(t)
	tfB, adminB := e.newTayfa(t)
	evA := e.create(t, tfA, adminA, input())
	c := &clock{t: evA.StartsAt}
	svc := e.clocked(t, c, nil)
	_, err := svc.CheckIn(ctx, adminB, tfB, evA.ID, izmir)
	wantErr(t, "B üzerinden check-in", err, apperr.ErrEventNotFound)
	_, err = svc.CheckIn(ctx, adminB, tfA, evA.ID, izmir)
	wantErr(t, "üye olmayan check-in", err, apperr.ErrTayfaNotFound)
	c.t = evA.EndsAt
	wantErr(t, "B üzerinden düzeltme", svc.SetAttendance(ctx, adminB, tfB, evA.ID, id.New(), true), apperr.ErrEventNotFound)
	memberB := e.seed(t, tfB, authz.Member)
	wantErr(t, "başka tayfanın üyesi", svc.SetAttendance(ctx, adminA, tfA, evA.ID, memberB, true), apperr.ErrMemberNotFound)
}

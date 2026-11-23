package event

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Şimdi başlat (issue #263; ADR-0038 §1, §2).

// nowInput is a valid in-person event that starts now.
func nowInput() Input {
	return Input{
		Type: TypeGeneral, Title: "Batak masası", Location: inPerson(),
		ClientRequestID: id.New().String(), StartsNow: true,
	}
}

// nowClock is a settable clock at the current time, with the database's
// precision.
func nowClock() *clock {
	return &clock{t: time.Now().UTC().Truncate(time.Microsecond)}
}

// The start is the server's now and the end 12 hours later; the client's
// clock is never used, so no time may be sent.
func TestStartsNowCreate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	c := nowClock()
	svc := e.clocked(t, c, nil)

	v, err := svc.Create(ctx, admin, tf, nowInput())
	must(t, err)
	if !v.StartsAt.Equal(c.t) || !v.EndsAt.Equal(c.t.Add(12*time.Hour)) || !v.OpenEnded || v.Status != StatusScheduled {
		t.Fatalf("şimdi etkinliği = %v – %v açık uçlu %v", v.StartsAt, v.EndsAt, v.OpenEnded)
	}
	var open bool
	must(t, e.pool.QueryRow(ctx, `SELECT open_ended FROM events WHERE id = $1`, v.ID.PG()).Scan(&open))
	if !open {
		t.Fatal("open_ended yazılmadı")
	}
	// #265 sends the now notifications from the event's mark.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.created' AND subject_id = $1 AND payload ->> 'starts_now' = 'true'`, v.ID.PG()); n != 1 {
		t.Errorf("event.created starts_now = %d", n)
	}
	// It is running at once: the organizer's view lists it and check-in is
	// open.
	list, _, err := svc.ListUpcoming(ctx, admin, tf, 0, "")
	must(t, err)
	if len(list) != 1 || !list[0].OpenEnded {
		t.Fatalf("liste = %+v", list)
	}
	u := e.going(t, tf, v.ID, authz.Member)
	if _, err := svc.CheckIn(ctx, u, tf, v.ID, izmir); err != nil {
		t.Fatalf("check-in: %v", err)
	}

	// A planned event is not open-ended.
	planned := e.create(t, tf, admin, input())
	if planned.OpenEnded {
		t.Fatal("planlı etkinlik açık uçlu")
	}

	for name, tc := range map[string]struct {
		edit  func(*Input)
		field string
	}{
		"geçmiş başlangıç": {func(in *Input) { in.StartsAt = c.t.Add(-time.Hour) }, "start_time"},
		"ileri başlangıç":  {func(in *Input) { in.StartsAt = c.t.Add(time.Hour) }, "start_time"},
		"bitiş":            {func(in *Input) { in.EndsAt = c.t.Add(time.Hour) }, "end_time"},
		"tekrar":           {func(in *Input) { in.Recurrence = &Recurrence{RRule: "FREQ=WEEKLY", TimeZone: "Europe/Istanbul"} }, "recurrence"},
		"keşfet":           {func(in *Input) { in.Discoverable = true }, "discoverable"},
	} {
		in := nowInput()
		tc.edit(&in)
		_, err := svc.Create(ctx, u, tf, in)
		wantField(t, name, err, tc.field)
	}
	// The meeting point stays required in person.
	in := nowInput()
	in.Location.MeetingPoint = nil
	_, err = svc.Create(ctx, u, tf, in)
	wantField(t, "buluşma noktası", err, "meeting_point")
}

// Starting now has its own setting, now_create_min_role (member by
// default), independent of members_can_create_events; only admins change
// it.
func TestStartsNowPermission(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	member := e.seed(t, tf, authz.Member)
	member2 := e.seed(t, tf, authz.Member)
	mod := e.seed(t, tf, authz.Moderator)
	mod2 := e.seed(t, tf, authz.Moderator)
	muted := e.seed(t, tf, authz.Member)
	e.mute(t, tf, muted)

	// Members cannot plan events here, but they can start one now.
	_, err := e.svc.Create(ctx, member, tf, input())
	wantErr(t, "planlı etkinlik", err, apperr.ErrPermissionDenied)
	v, err := e.svc.Create(ctx, member, tf, nowInput())
	must(t, err)
	if !v.OpenEnded || v.CreatedBy != member {
		t.Fatalf("üyenin şimdi etkinliği = %+v", v)
	}
	_, err = e.svc.Create(ctx, muted, tf, nowInput())
	wantErr(t, "susturulmuş üye", err, apperr.ErrMuted)
	_, err = e.svc.Create(ctx, id.New(), tf, nowInput())
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)

	// The setting: admins only, valid roles only.
	set := func(user id.ID, r authz.Role) error {
		_, err := e.tayfas.Update(ctx, user, tf, tayfa.UpdateInput{Fields: []string{tayfa.FieldNowCreateMinRole}, NowCreateMinRole: r})
		return err
	}
	wantErr(t, "moderatör ayarı değiştiremez", set(mod, authz.Admin), apperr.ErrPermissionDenied)
	wantField(t, "geçersiz rol", set(admin, 0), tayfa.FieldNowCreateMinRole)
	must(t, set(admin, authz.Moderator))
	tv, _, err := e.tayfas.Get(ctx, admin, tf)
	must(t, err)
	if tv.NowCreateMinRole != authz.Moderator {
		t.Fatalf("ayar = %v", tv.NowCreateMinRole)
	}
	_, err = e.svc.Create(ctx, member2, tf, nowInput())
	wantErr(t, "ayar moderatörken üye", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, mod, tf, nowInput())
	must(t, err)

	must(t, set(admin, authz.Admin))
	_, err = e.svc.Create(ctx, mod2, tf, nowInput())
	wantErr(t, "ayar yöneticiyken moderatör", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, admin, tf, nowInput())
	must(t, err)

	// members_can_create_events opens planned events, not starting now.
	e.allowMembers(t, tf, admin)
	_, err = e.svc.Create(ctx, member2, tf, nowInput())
	wantErr(t, "etkinlik izni şimdi başlatmayı açmaz", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, member2, tf, input())
	must(t, err)
}

// One open now event per person, in every tayfa together. Finishing,
// cancelling or the 12-hour end frees the place; a retry returns the
// first event.
func TestStartsNowLimit(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	tf2, _ := e.newTayfa(t)
	user := e.seed(t, tf, authz.Member)
	e.seedAs(t, tf2, user, authz.Member)
	c := nowClock()
	svc := e.clocked(t, c, nil)

	in := nowInput()
	first, err := svc.Create(ctx, user, tf, in)
	must(t, err)
	again, err := svc.Create(ctx, user, tf, in)
	must(t, err)
	if again.ID != first.ID {
		t.Fatal("tekrar gönderim yeni etkinlik açtı")
	}
	_, err = svc.Create(ctx, user, tf, nowInput())
	wantErr(t, "aynı tayfada ikinci", err, apperr.ErrNowLimitReached)
	_, err = svc.Create(ctx, user, tf2, nowInput())
	wantErr(t, "başka tayfada ikinci", err, apperr.ErrNowLimitReached)
	// Planned events and other people are not limited.
	e.allowMembers(t, tf, admin)
	_, err = svc.Create(ctx, user, tf, input())
	must(t, err)
	_, err = svc.Create(ctx, admin, tf, nowInput())
	must(t, err)

	// Finishing frees the place.
	c.t = c.t.Add(time.Minute)
	_, err = svc.Finish(ctx, user, tf, first.ID)
	must(t, err)
	second, err := svc.Create(ctx, user, tf2, nowInput())
	must(t, err)

	// So does cancelling.
	_, err = svc.Cancel(ctx, user, tf2, second.ID, "")
	must(t, err)
	third, err := svc.Create(ctx, user, tf, nowInput())
	must(t, err)

	// And the end 12 hours later, without a job.
	c.t = third.EndsAt.Add(-time.Microsecond)
	_, err = svc.Create(ctx, user, tf, nowInput())
	wantErr(t, "12 saat dolmadan", err, apperr.ErrNowLimitReached)
	c.t = third.EndsAt
	_, err = svc.Create(ctx, user, tf, nowInput())
	must(t, err)
}

// "Bitir": the organizer ends their open-ended event; the end becomes now
// and every window that counts from the end follows it.
func TestFinish(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	org := e.seed(t, tf, authz.Member)
	mod := e.seed(t, tf, authz.Moderator)
	c := nowClock()
	svc := e.clocked(t, c, nil)

	ev, err := svc.Create(ctx, org, tf, nowInput())
	must(t, err)
	u := e.going(t, tf, ev.ID, authz.Member)
	late := e.going(t, tf, ev.ID, authz.Member)
	_, err = svc.CheckIn(ctx, u, tf, ev.ID, izmir)
	must(t, err)
	minutes := func() int {
		t.Helper()
		return e.count(t, `SELECT duration_minutes FROM user_activity_records WHERE user_id = $1 AND event_id = $2`, u.PG(), ev.ID.PG())
	}
	if m := minutes(); m != 720 {
		t.Fatalf("bitmeden kayıt = %d dk", m)
	}
	c.t = c.t.Add(2 * time.Hour)
	// Before the end the organizer cannot correct attendance.
	wantErr(t, "bitmeden düzeltme", svc.SetAttendance(ctx, org, tf, ev.ID, late, true), apperr.ErrCorrectionClosed)

	// Only the organizer finishes, not even a moderator.
	_, err = svc.Finish(ctx, mod, tf, ev.ID)
	wantErr(t, "moderatör", err, apperr.ErrPermissionDenied)
	_, err = svc.Finish(ctx, u, tf, ev.ID)
	wantErr(t, "katılımcı", err, apperr.ErrPermissionDenied)
	_, err = svc.Finish(ctx, id.New(), tf, ev.ID)
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	_, err = svc.Finish(ctx, org, tf, id.New())
	wantErr(t, "bilinmeyen etkinlik", err, apperr.ErrEventNotFound)

	updates := len(noticesOf(t, e.pool, notify.TypeEventUpdated))
	v, err := svc.Finish(ctx, org, tf, ev.ID)
	must(t, err)
	if !v.EndsAt.Equal(c.t) || v.OpenEnded || !v.StartsAt.Equal(ev.StartsAt) {
		t.Fatalf("bitirilen = %v – %v açık uçlu %v", v.StartsAt, v.EndsAt, v.OpenEnded)
	}
	// The activity record takes the real duration.
	if m := minutes(); m != 120 {
		t.Fatalf("bitince kayıt = %d dk", m)
	}
	// Nobody is notified; chat gets the new end.
	if n := len(noticesOf(t, e.pool, notify.TypeEventUpdated)); n != updates {
		t.Errorf("bitir bildirim üretti: %d", n-updates)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'event.updated' AND subject_id = $1 AND (payload ->> 'ends_at')::timestamptz = $2`, ev.ID.PG(), c.t); n != 1 {
		t.Errorf("event.updated bitiş = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'event.finished' AND actor_id = $1`, org.PG()); n != 1 {
		t.Errorf("denetim kaydı = %d", n)
	}
	// Check-in closes, the correction window opens from the new end.
	_, err = svc.CheckIn(ctx, late, tf, ev.ID, izmir)
	wantErr(t, "bitince check-in", err, apperr.ErrCheckInClosed)
	must(t, svc.SetAttendance(ctx, org, tf, ev.ID, late, true))
	c.t = v.EndsAt.Add(correctionWindow)
	wantErr(t, "7 gün sonra", svc.SetAttendance(ctx, org, tf, ev.ID, late, false), apperr.ErrCorrectionClosed)

	// Finishing twice, a planned event, an automatic end, a cancelled one.
	_, err = svc.Finish(ctx, org, tf, ev.ID)
	wantErr(t, "ikinci kez", err, apperr.ErrEventEnded)

	c.t = time.Now().UTC().Truncate(time.Microsecond)
	e.allowMembers(t, tf, admin)
	planned, err := svc.Create(ctx, org, tf, input())
	must(t, err)
	_, err = svc.Finish(ctx, org, tf, planned.ID)
	wantErr(t, "planlı", err, apperr.ErrEventNotOpenEnded)

	auto, err := svc.Create(ctx, org, tf, nowInput())
	must(t, err)
	c.t = auto.EndsAt
	_, err = svc.Finish(ctx, org, tf, auto.ID)
	wantErr(t, "kendiliğinden bitmiş", err, apperr.ErrEventEnded)

	c.t = auto.EndsAt.Add(time.Hour)
	cancelled, err := svc.Create(ctx, org, tf, nowInput())
	must(t, err)
	_, err = svc.Cancel(ctx, org, tf, cancelled.ID, "")
	must(t, err)
	_, err = svc.Finish(ctx, org, tf, cancelled.ID)
	wantErr(t, "iptal", err, apperr.ErrEventCancelled)

	// A muted organizer cannot finish; the event still ends at 12 hours.
	muted, err := svc.Create(ctx, org, tf, nowInput())
	must(t, err)
	e.exec(t, `UPDATE memberships SET muted_until = $3, muted_by = $4 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), org.PG(), c.t.Add(time.Hour), admin.PG())
	_, err = svc.Finish(ctx, org, tf, muted.ID)
	wantErr(t, "susturulmuş", err, apperr.ErrMuted)
}

// An open-ended event's times are the server's and it never reaches
// Keşfet; the rest may be edited.
func TestStartsNowUpdate(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	ev, err := e.svc.Create(ctx, admin, tf, nowInput())
	must(t, err)

	in := input()
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTime}, in)
	wantField(t, "zaman", err, "update_mask")
	in.Discoverable = true
	_, err = e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldDiscoverable}, in)
	wantField(t, "keşfet", err, "discoverable")
	in.Title = "Okey masası"
	v, err := e.svc.Update(ctx, admin, tf, ev.ID, []string{FieldTitle}, in)
	must(t, err)
	if v.Title != "Okey masası" || !v.OpenEnded || !v.EndsAt.Equal(ev.EndsAt) {
		t.Fatalf("düzenlenen = %+v", v)
	}
}

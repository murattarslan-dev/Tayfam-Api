package channel

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Channel message reports (issue #205, ADR-0031; platform owner,
// 2026-11-16).

// TestReportTargetAudience: only someone who can read the message's channel
// gets an answer; everyone else gets the same not found (IDOR).
func TestReportTargetAudience(t *testing.T) {
	e := setup(t)
	a := e.audience(t)
	organizer, going := id.New(), id.New()
	for _, u := range []id.ID{organizer, going} {
		e.members.set(e.tayfa, u, Member{Active: true, Role: RoleMember})
	}
	ev := id.New()
	a.events[ev] = &fakeEvent{title: "Kamp", organizer: organizer, going: []id.ID{going}, end: time.Now().Add(48 * time.Hour)}

	members := e.channel(t, ChannelInput{Name: "Genel"})
	staff := e.channel(t, ChannelInput{Name: "Yönetim", Visibility: VisibilityStaff})
	event, err := e.svc.Create(ctx, organizer, e.tayfa, ChannelInput{Name: "Kamp", Visibility: VisibilityEvent, Event: ev})
	must(t, err)
	selected, err := e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Seçili", Visibility: VisibilitySelected, Members: []id.ID{e.member}})
	must(t, err)

	msgs := map[string]Message{
		"members":  e.send(t, e.mod, members.ID, "herkese"),
		"staff":    e.send(t, e.mod, staff.ID, "yönetime"),
		"event":    e.send(t, organizer, event.ID, "kampa"),
		"selected": e.send(t, e.mod, selected.ID, "seçililere"),
	}
	users := map[string]id.ID{
		"yönetici": e.admin, "moderatör": e.mod, "üye": e.member, "diğer üye": e.other,
		"organizatör": organizer, "gidiyor": going, "tayfa dışı": id.New(),
	}
	want := map[string]map[string]bool{
		"members":  {"yönetici": true, "moderatör": true, "üye": true, "diğer üye": true, "organizatör": true, "gidiyor": true},
		"staff":    {"yönetici": true, "moderatör": true},
		"event":    {"organizatör": true, "gidiyor": true},
		"selected": {"moderatör": true, "üye": true},
	}
	for ch, m := range msgs {
		for who, u := range users {
			got, err := e.svc.ReportTarget(ctx, u, e.tayfa, m.ID)
			if want[ch][who] {
				if err != nil || got.ID != m.ID || got.Body != m.Body || got.AuthorID != m.AuthorID || got.ChannelID != m.ChannelID {
					t.Errorf("%s kanalı, %s: %+v %v", ch, who, got, err)
				}
				continue
			}
			if !errors.Is(err, apperr.ErrMessageNotFound) {
				t.Errorf("%s kanalı, %s: %v, mesaj bulunamadı bekleniyordu", ch, who, err)
			}
		}
	}
	// Another tayfa, a made-up message, a deleted one: not found.
	m := msgs["members"]
	_, err = e.svc.ReportTarget(ctx, e.member, id.New(), m.ID)
	is(t, err, apperr.ErrMessageNotFound)
	_, err = e.svc.ReportTarget(ctx, e.member, e.tayfa, id.New())
	is(t, err, apperr.ErrMessageNotFound)
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, members.ID, m.ID))
	_, err = e.svc.ReportTarget(ctx, e.member, e.tayfa, m.ID)
	is(t, err, apperr.ErrMessageNotFound)
	// Leaving the event's audience ends it.
	a.setGoing(ev)
	_, err = e.svc.ReportTarget(ctx, going, e.tayfa, msgs["event"].ID)
	is(t, err, apperr.ErrMessageNotFound)
}

// TestReportTargetKindsAndStatus: the answer carries the message as the
// reporter sees it; core learns removal as one flag; the platform's removal
// is the moderator's hiding and can be applied twice.
func TestReportTargetKindsAndStatus(t *testing.T) {
	e := setup(t)
	p := e.photos()
	c := e.channel(t, ChannelInput{Name: "Buluşma"})
	place, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPlace, Place: &Place{Latitude: 38.4, Longitude: 27.1, Name: "İskele"}, ClientRequestID: id.New()})
	must(t, err)
	got, err := e.svc.ReportTarget(ctx, e.other, e.tayfa, place.ID)
	must(t, err)
	if got.Kind != KindPlace || got.Place == nil || got.Place.Name != "İskele" || got.Place.Latitude != 38.4 {
		t.Fatalf("yer = %+v", got)
	}
	photo := e.sendPhoto(t, p, e.member, c.ID)
	if got, err := e.svc.ReportTarget(ctx, e.other, e.tayfa, photo.ID); err != nil || got.Kind != KindPhoto || got.PhotoUploadID != photo.PhotoUploadID {
		t.Fatalf("fotoğraf = %+v %v", got, err)
	}

	removed := func(tayfa, m id.ID) bool {
		t.Helper()
		r, err := e.svc.MessageRemoved(ctx, tayfa, m)
		must(t, err)
		return r
	}
	if removed(e.tayfa, place.ID) {
		t.Fatal("görünen mesaj kaldırılmış sayıldı")
	}
	if !removed(id.New(), place.ID) || !removed(e.tayfa, id.New()) {
		t.Fatal("başka tayfanın ya da bilinmeyen mesaj kaldırılmış sayılmalı")
	}

	// The platform removes it: hidden, restorable, recorded with the
	// administrator; a second delivery changes nothing.
	platformAdmin := id.New()
	for i := range 2 {
		var ch id.ID
		var changed bool
		must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			ch, changed, err = HideReported(ctx, tx, e.tayfa, place.ID, platformAdmin, time.Now())
			return err
		}))
		if changed != (i == 0) || (i == 0 && ch != c.ID) {
			t.Fatalf("%d. uygulama: %v %v", i+1, ch, changed)
		}
	}
	if !removed(e.tayfa, place.ID) {
		t.Fatal("platformun kaldırdığı mesaj görünüyor")
	}
	if e.count(t, "SELECT count(*) FROM channel_messages WHERE id = $1 AND deleted_by = $2 AND body_encrypted IS NOT NULL", place.ID.PG(), platformAdmin.PG()) != 1 {
		t.Fatal("moderasyon gizlemesi gibi olmalı (içerik geri alınabilir kalır)")
	}
	if e.count(t, "SELECT count(*) FROM domain_events WHERE type = $1 AND actor_id = $2", EventMessageDeleted, platformAdmin.PG()) != 1 {
		t.Fatal("kaldırma bir kez kaydedilmeli")
	}
	// Another tayfa's ID changes nothing.
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, changed, err := HideReported(ctx, tx, id.New(), photo.ID, platformAdmin, time.Now())
		if changed {
			t.Error("başka tayfanın mesajı gizlendi")
		}
		return err
	}))
	// The tayfa's admin may restore it.
	if _, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, place.ID); err != nil {
		t.Fatalf("geri alma: %v", err)
	}
}

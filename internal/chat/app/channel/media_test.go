package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/corerelay"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tests for photo and place messages (issue #205, ADR-0028).

// fakePhotos stands in for core's AttachChannelPhoto: an upload is the
// owner's and attaches to one message.
type fakePhotos struct {
	mu       sync.Mutex
	owner    map[id.ID]id.ID // upload → tayfa it belongs to
	attached map[id.ID]id.ID // upload → message
	err      error
}

func (f *fakePhotos) AttachChannelPhoto(_ context.Context, upload, tayfa, message id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.owner[upload] != tayfa {
		return apperr.ErrUploadNotFound
	}
	if m, ok := f.attached[upload]; ok && m != message {
		return apperr.ErrUploadInUse
	}
	f.attached[upload] = message
	return nil
}

func (e *env) photos() *fakePhotos {
	f := &fakePhotos{owner: map[id.ID]id.ID{}, attached: map[id.ID]id.ID{}}
	e.svc.cfg.Photos = f
	return f
}

func (f *fakePhotos) upload(tayfa id.ID) id.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := id.New()
	f.owner[u] = tayfa
	return u
}

// released returns the chat.media_released events: upload → message.
func (e *env) released(t *testing.T) map[id.ID]id.ID {
	t.Helper()
	rows, err := e.pool.Query(ctx, "SELECT payload FROM domain_events WHERE type = $1", corerelay.TypeMediaReleased)
	must(t, err)
	defer rows.Close()
	out := map[id.ID]id.ID{}
	for rows.Next() {
		var p map[string]string
		must(t, rows.Scan(&p))
		u, _ := id.Parse(p["upload_id"])
		m, _ := id.Parse(p["message_id"])
		out[u] = m
	}
	return out
}

func TestPlaceMessage(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "Buluşma"})
	place := &Place{Latitude: 38.4192, Longitude: 27.1287, Name: "  Saat Kulesi  "}
	m, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPlace, Place: place, ClientRequestID: id.New()})
	must(t, err)
	if m.Kind != KindPlace || m.Place == nil || m.Place.Name != "Saat Kulesi" || m.Body != "" {
		t.Fatalf("mesaj = %+v", m)
	}
	list, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)
	if len(list) != 1 || list[0].Place == nil || list[0].Place.Latitude != 38.4192 || list[0].Place.Longitude != 27.1287 || list[0].Kind != KindPlace {
		t.Fatalf("liste = %+v", list)
	}
	// The database never holds the coordinates in the clear.
	var raw []byte
	must(t, e.pool.QueryRow(ctx, "SELECT body_encrypted FROM channel_messages WHERE id = $1", m.ID.PG()).Scan(&raw))
	if bytes.Contains(raw, []byte("38.4192")) || bytes.Contains(raw, []byte("Saat Kulesi")) {
		t.Fatal("yer açık saklandı")
	}
	// Nor the logs or the events.
	var events string
	must(t, e.pool.QueryRow(ctx, "SELECT COALESCE(string_agg(payload::text, ' '), '') FROM domain_events").Scan(&events))
	for _, s := range []string{e.logs.String(), events} {
		if strings.Contains(s, "38.4") || strings.Contains(s, "27.12") || strings.Contains(s, "Saat Kulesi") {
			t.Fatalf("yer sızdı: %s", s)
		}
	}
	// The author's deletion erases the place at once.
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, m.ID))
	list, _, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)
	if !list[0].Deleted || list[0].Place != nil {
		t.Fatalf("silinen yer = %+v", list[0])
	}
	if e.count(t, "SELECT count(*) FROM channel_messages WHERE body_encrypted IS NOT NULL") != 0 {
		t.Fatal("silinen yerin şifreli hali kaldı")
	}

	for field, in := range map[string]SendInput{
		"place":           {Kind: KindPlace},
		"place.latitude":  {Kind: KindPlace, Place: &Place{Latitude: 90.0001}},
		"place.longitude": {Kind: KindPlace, Place: &Place{Longitude: -180.5}},
		"place.name":      {Kind: KindPlace, Place: &Place{Name: strings.Repeat("ş", PlaceNameMax+1)}},
		"body":            {Kind: KindPlace, Place: &Place{}, Body: "x"},
		"photo_upload_id": {Kind: KindPlace, Place: &Place{}, PhotoUploadID: id.New()},
		"kind":            {Kind: "video", Body: "x"},
	} {
		in.ClientRequestID = id.New()
		_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, in)
		isValidation(t, err, field)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1)} {
		_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPlace, Place: &Place{Latitude: bad}, ClientRequestID: id.New()})
		isValidation(t, err, "place.latitude")
	}
	for _, name := range []string{" ", "a\nb", "a\x00b"} {
		_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPlace, Place: &Place{Name: name}, ClientRequestID: id.New()})
		isValidation(t, err, "place.name")
	}
	// A place without a name is fine.
	if _, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPlace, Place: &Place{Latitude: -90, Longitude: 180}, ClientRequestID: id.New()}); err != nil {
		t.Fatal(err)
	}
}

func TestPhotoMessage(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "Fotoğraflar"})
	// Without core, photo messages are off.
	_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPhoto, PhotoUploadID: id.New(), ClientRequestID: id.New()})
	is(t, err, apperr.ErrUnavailable)

	p := e.photos()
	up := p.upload(e.tayfa)
	req := id.New()
	m, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPhoto, PhotoUploadID: up, ClientRequestID: req})
	must(t, err)
	if m.Kind != KindPhoto || m.PhotoUploadID != up || p.attached[up] != m.ID {
		t.Fatalf("mesaj = %+v, bağlı %v", m, p.attached[up])
	}
	// A retry returns the first message.
	again, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPhoto, PhotoUploadID: up, ClientRequestID: req})
	if err != nil || again.ID != m.ID || again.PhotoUploadID != up {
		t.Fatalf("tekrar = %+v %v", again, err)
	}
	list, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)
	if list[0].PhotoUploadID != up || list[0].Body != "" {
		t.Fatalf("liste = %+v", list[0])
	}
	// core refuses: the message is not written (the transaction rolls back
	// and a retry starts afresh).
	for _, tc := range []struct {
		up   id.ID
		want error
	}{{up, apperr.ErrUploadInUse}, {id.New(), apperr.ErrUploadNotFound}, {p.upload(id.New()), apperr.ErrUploadNotFound}} {
		before := e.count(t, "SELECT count(*) FROM channel_messages")
		_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPhoto, PhotoUploadID: tc.up, ClientRequestID: id.New()})
		is(t, err, tc.want)
		if e.count(t, "SELECT count(*) FROM channel_messages") != before {
			t.Fatal("reddedilen fotoğraf mesajı yazıldı")
		}
	}
	p.err = apperr.ErrUploadNotReady
	_, err = e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Kind: KindPhoto, PhotoUploadID: p.upload(e.tayfa), ClientRequestID: id.New()})
	is(t, err, apperr.ErrUploadNotReady)
	p.err = nil
	for field, in := range map[string]SendInput{
		"photo_upload_id": {Kind: KindPhoto},
		"body":            {Kind: KindPhoto, PhotoUploadID: up, Body: "alt yazı"},
		"place":           {Kind: KindPhoto, PhotoUploadID: up, Place: &Place{}},
	} {
		in.ClientRequestID = id.New()
		_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, in)
		isValidation(t, err, field)
	}
	in := SendInput{Body: "metin", PhotoUploadID: up, ClientRequestID: id.New()}
	_, err = e.svc.Send(ctx, e.member, e.tayfa, c.ID, in)
	isValidation(t, err, "photo_upload_id")

	// A moderator hides it: the photo stays (restorable); nothing is
	// released.
	hidden := e.sendPhoto(t, p, e.member, c.ID)
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, hidden.ID))
	if len(e.released(t)) != 0 {
		t.Fatal("gizleme dosyayı bırakmamalı")
	}
	list, _, err = e.svc.Messages(ctx, e.admin, e.tayfa, c.ID, ListInput{})
	must(t, err)
	for _, x := range list {
		if x.ID == hidden.ID && (!x.Deleted || x.PhotoUploadID != (id.ID{}) || !x.Restorable) {
			t.Fatalf("gizlenen = %+v", x)
		}
	}
	restored, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, hidden.ID)
	must(t, err)
	if restored.PhotoUploadID != hidden.PhotoUploadID {
		t.Fatalf("geri alınan = %+v", restored)
	}
	// The author's own deletion releases the photo at once.
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, m.ID))
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, m.ID)) // a repeat
	if r := e.released(t); len(r) != 1 || r[up] != m.ID {
		t.Fatalf("bırakılan = %v", r)
	}
	if _, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, m.ID); !errors.Is(err, apperr.ErrMessageNotFound) {
		t.Fatalf("yazarın sildiği geri alınamaz: %v", err)
	}
	// After 30 days a hidden photo message is deleted for good and its
	// photo released in the same transaction.
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, hidden.ID))
	e.shift = RestoreWindow + time.Hour
	n, err := e.svc.PurgeDeleted(ctx)
	if err != nil || n != 2 {
		t.Fatalf("kalıcı silme = %d %v", n, err)
	}
	if r := e.released(t); len(r) != 2 || r[hidden.PhotoUploadID] != hidden.ID {
		t.Fatalf("kalıcı silmede bırakılan = %v", r)
	}
	// The events carry IDs only.
	var payload []byte
	must(t, e.pool.QueryRow(ctx, "SELECT payload FROM domain_events WHERE type = $1 LIMIT 1", corerelay.TypeMediaReleased).Scan(&payload))
	var keys map[string]any
	must(t, json.Unmarshal(payload, &keys))
	if len(keys) != 2 {
		t.Fatalf("olay yükü = %s", payload)
	}
}

func (e *env) sendPhoto(t *testing.T, p *fakePhotos, user, ch id.ID) Message {
	t.Helper()
	m, err := e.svc.Send(ctx, user, e.tayfa, ch, SendInput{Kind: KindPhoto, PhotoUploadID: p.upload(e.tayfa), ClientRequestID: id.New()})
	must(t, err)
	return m
}

// TestPhotoVisibility: who core may give a channel photo's link to, for
// every channel type and role (ADR-0024 audiences): the same rule as
// reading the channel's messages.
func TestPhotoVisibility(t *testing.T) {
	e := setup(t)
	a := e.audience(t)
	p := e.photos()
	organizer, going := id.New(), id.New()
	for _, u := range []id.ID{organizer, going} {
		e.members.set(e.tayfa, u, Member{Active: true, Role: RoleMember})
	}
	stranger := id.New() // not a member of the tayfa
	ev := id.New()
	a.events[ev] = &fakeEvent{title: "Kamp", organizer: organizer, going: []id.ID{going}, end: time.Now().Add(48 * time.Hour)}

	members := e.channel(t, ChannelInput{Name: "Genel"})
	staff := e.channel(t, ChannelInput{Name: "Yönetim", Visibility: VisibilityStaff})
	event, err := e.svc.Create(ctx, organizer, e.tayfa, ChannelInput{Name: "Kamp", Visibility: VisibilityEvent, Event: ev})
	must(t, err)
	selected, err := e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Seçili", Visibility: VisibilitySelected, Members: []id.ID{e.member}})
	must(t, err)

	photos := map[string]Message{
		"members":  e.sendPhoto(t, p, e.mod, members.ID),
		"staff":    e.sendPhoto(t, p, e.mod, staff.ID),
		"event":    e.sendPhoto(t, p, organizer, event.ID),
		"selected": e.sendPhoto(t, p, e.mod, selected.ID),
	}
	users := map[string]id.ID{
		"yönetici": e.admin, "moderatör": e.mod, "üye": e.member, "diğer üye": e.other,
		"organizatör": organizer, "gidiyor": going, "tayfa dışı": stranger,
	}
	want := map[string]map[string]bool{
		"members":  {"yönetici": true, "moderatör": true, "üye": true, "diğer üye": true, "organizatör": true, "gidiyor": true},
		"staff":    {"yönetici": true, "moderatör": true},
		"event":    {"organizatör": true, "gidiyor": true},
		"selected": {"moderatör": true, "üye": true},
	}
	for ch, m := range photos {
		for who, u := range users {
			got, err := e.svc.VisiblePhotos(ctx, u, []MediaItem{{Upload: m.PhotoUploadID, Message: m.ID}})
			must(t, err)
			if got[m.PhotoUploadID] != want[ch][who] {
				t.Errorf("%s kanalı, %s: %v, %v bekleniyordu", ch, who, got[m.PhotoUploadID], want[ch][who])
			}
		}
	}
	// The pair must match: another message's photo, or a made-up message,
	// shows nothing.
	m := photos["members"]
	other := photos["staff"]
	for name, it := range map[string]MediaItem{
		"başka mesajın fotoğrafı": {Upload: other.PhotoUploadID, Message: m.ID},
		"bilinmeyen mesaj":        {Upload: m.PhotoUploadID, Message: id.New()},
	} {
		got, err := e.svc.VisiblePhotos(ctx, e.admin, []MediaItem{it})
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	// A deleted message's photo is not shown, even to admins.
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, members.ID, m.ID))
	if got, err := e.svc.VisiblePhotos(ctx, e.admin, []MediaItem{{Upload: m.PhotoUploadID, Message: m.ID}}); err != nil || len(got) != 0 {
		t.Fatalf("silinen: %v %v", got, err)
	}
	// Leaving the event's audience hides its photos.
	a.setGoing(ev)
	ep := photos["event"]
	if got, err := e.svc.VisiblePhotos(ctx, going, []MediaItem{{Upload: ep.PhotoUploadID, Message: ep.ID}}); err != nil || len(got) != 0 {
		t.Fatalf("katılımdan çıkan: %v %v", got, err)
	}
}

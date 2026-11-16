package report

import (
	"context"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeChannelChat stands in for chat: which messages each reporter may read (the
// channel's audience is chat's), and which are removed.
type fakeChannelChat struct {
	readable map[[2]id.ID]ChannelMessage // (reporter, message)
	removed  map[id.ID]bool
	asked    []id.ID // reporters asked about
	err      error
}

// ReportTarget answers as chat does for the caller of the token; the test
// passes the reporter through the context.
func (f *fakeChannelChat) ReportTarget(ctx context.Context, tayfa, message id.ID) (ChannelMessage, error) {
	u, _ := ctx.Value(reporterKey{}).(id.ID)
	f.asked = append(f.asked, u)
	if f.err != nil {
		return ChannelMessage{}, f.err
	}
	m, ok := f.readable[[2]id.ID{u, message}]
	if !ok {
		return ChannelMessage{}, apperr.ErrReportTargetNotFound
	}
	return m, nil
}

func (f *fakeChannelChat) Removed(_ context.Context, _, message id.ID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.removed[message], nil
}

type reporterKey struct{}

// asReporter puts the reporter where chat would see their token.
func asReporter(u id.ID) context.Context { return context.WithValue(ctx, reporterKey{}, u) }

// TestChannelMessageReport (issue #205, ADR-0031; platform owner,
// 2026-11-16): a channel message is reportable content; chat decides who
// may report it (the channel's audience), the report keeps the message as
// it was, it goes to the tayfa's queue and content_removed needs chat to
// say it is gone.
func TestChannelMessageReport(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	fc := &fakeChannelChat{readable: map[[2]id.ID]ChannelMessage{}, removed: map[id.ID]bool{}}
	msg, staffMsg, ch := id.New(), id.New(), id.New()
	text := ChannelMessage{Channel: ch, Author: c.member2, Kind: messageText, Body: "Kaba bir mesaj"}
	for _, u := range []id.ID{c.member, c.mod, c.member2} {
		fc.readable[[2]id.ID{u, msg}] = text
	}
	// A staff channel's message: only the moderators read it.
	fc.readable[[2]id.ID{c.mod, staffMsg}] = ChannelMessage{Channel: id.New(), Author: c.mod2, Kind: messageText, Body: "yönetim"}

	in := func(m id.ID) CreateInput {
		return CreateInput{TargetType: TargetChannelMessage, TargetID: m.String(), Reason: ReasonHarassment}
	}
	// Without chat: unavailable, nothing written.
	_, _, err := e.svc.Create(asReporter(c.member), c.member, c.tayfa, in(msg))
	is(t, "chat yok", err, apperr.ErrUnavailable)
	e.svc.cfg.ChannelMessages, e.svc.cfg.ChannelStatus = fc, fc

	// Outside the audience, unknown, or a member of another tayfa: the
	// same not found (IDOR).
	_, _, err = e.svc.Create(asReporter(c.member), c.member, c.tayfa, in(staffMsg))
	is(t, "kitle dışı", err, apperr.ErrReportTargetNotFound)
	_, _, err = e.svc.Create(asReporter(c.member), c.member, c.tayfa, in(id.New()))
	is(t, "bilinmeyen", err, apperr.ErrReportTargetNotFound)
	other := e.crew(t)
	_, _, err = e.svc.Create(asReporter(other.member), other.member, c.tayfa, in(msg))
	if err == nil {
		t.Fatal("tayfa dışı şikâyet etti")
	}
	// One's own message.
	_, _, err = e.svc.Create(asReporter(c.member2), c.member2, c.tayfa, in(msg))
	isValidation(t, "kendi mesajı", err, "target_id")
	if n := e.count(t, `SELECT count(*) FROM reports WHERE target_type = 'channel_message'`); n != 0 {
		t.Fatalf("reddedilen şikâyet yazıldı: %d", n)
	}

	v, created, err := e.svc.Create(asReporter(c.member), c.member, c.tayfa, in(msg))
	if err != nil || !created || v.TargetType != TargetChannelMessage || v.TargetID != msg {
		t.Fatalf("şikâyet = %+v %v %v", v, created, err)
	}
	// The same report again does not ask chat.
	asked := len(fc.asked)
	if again, created, err := e.svc.Create(asReporter(c.member), c.member, c.tayfa, in(msg)); err != nil || created || again.ID != v.ID || len(fc.asked) != asked {
		t.Fatalf("ikinci şikâyet = %+v %v %v", again, created, err)
	}
	// The report keeps the message as it was, for the platform only.
	var kind, body string
	must(t, e.pool.QueryRow(ctx, `SELECT message_kind, message_body FROM reports WHERE id = $1`, v.ID.PG()).Scan(&kind, &body))
	if kind != "text" || body != "Kaba bir mesaj" {
		t.Fatalf("saklanan = %s %q", kind, body)
	}
	var events string
	must(t, e.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(payload::text, ' '), '') FROM domain_events`).Scan(&events))
	if strings.Contains(events, "Kaba") {
		t.Fatal("mesaj olaya yazıldı")
	}

	// The tayfa's queue: the author is the subject, the channel is there to
	// open it.
	q := e.queue(t, c, c.mod, StatusOpen)
	if len(q) != 1 || q[0].ID != v.ID || q[0].SubjectID != c.member2 || q[0].ChannelID != ch {
		t.Fatalf("kuyruk: %+v", q)
	}
	// content_removed needs chat to say it is gone; chat unreachable is
	// unavailable.
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, v.ID, ResolutionContentRemoved)
	is(t, "görünen mesaj", err, apperr.ErrReportActionMissing)
	fc.err = apperr.ErrUnavailable
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, v.ID, ResolutionContentRemoved)
	is(t, "chat yanıt vermedi", err, apperr.ErrUnavailable)
	fc.err = nil
	fc.removed[msg] = true
	r, err := e.svc.Resolve(ctx, c.mod, c.tayfa, v.ID, ResolutionContentRemoved)
	if err != nil || r.Resolution != ResolutionContentRemoved || r.ChannelID != ch {
		t.Fatalf("çözüm = %+v %v", r, err)
	}
}

// TestChannelMessageReportKinds: a photo or a place message is kept as
// such; chat's answer that breaks the report's rules is refused.
func TestChannelMessageReportKinds(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	photo, place, bad := id.New(), id.New(), id.New()
	up := id.New()
	fc := &fakeChannelChat{readable: map[[2]id.ID]ChannelMessage{
		{c.member, photo}: {Channel: id.New(), Author: c.member2, Kind: messagePhoto, Photo: up},
		{c.member, place}: {Channel: id.New(), Author: c.member2, Kind: messagePlace, Place: &Place{Latitude: 38.4, Longitude: 27.1, Name: "İskele"}},
		{c.member, bad}:   {Channel: id.New(), Author: c.member2, Kind: messagePlace, Place: &Place{Latitude: 120}},
	}}
	e.svc.cfg.ChannelMessages = fc
	for _, m := range []id.ID{photo, place} {
		if _, _, err := e.svc.Create(asReporter(c.member), c.member, c.tayfa, CreateInput{TargetType: TargetChannelMessage, TargetID: m.String(), Reason: ReasonPrivacy}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.svc.Create(asReporter(c.member), c.member, c.tayfa, CreateInput{TargetType: TargetChannelMessage, TargetID: bad.String(), Reason: ReasonPrivacy}); err == nil {
		t.Fatal("geçersiz yer kabul edildi")
	}
	if n := e.count(t, `SELECT count(*) FROM reports WHERE message_kind = 'photo' AND message_photo_upload_id = $1`, up.PG()); n != 1 {
		t.Fatal("fotoğraf mesajı saklanmadı")
	}
	if n := e.count(t, `SELECT count(*) FROM reports WHERE message_kind = 'place' AND place_name = 'İskele' AND place_latitude = 38.4`); n != 1 {
		t.Fatal("yer mesajı saklanmadı")
	}
}

package dm

import (
	"context"
	"sync"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/corerelay"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Encrypted attachments of direct messages (issue #205, ADR-0031).

// fakeAttachments stands in for core's AttachDirectMessageAttachment.
type fakeAttachments struct {
	mu       sync.Mutex
	known    map[id.ID]bool
	attached map[id.ID]id.ID
	calls    int
}

func (f *fakeAttachments) AttachDirectMessageAttachment(_ context.Context, upload, message id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if !f.known[upload] {
		return apperr.ErrUploadNotFound
	}
	// core moves an attachment chat has not confirmed (ADR-0031).
	f.attached[upload] = message
	return nil
}

func TestDirectMessageAttachment(t *testing.T) {
	e := setupE2E(t)
	f := &fakeAttachments{known: map[id.ID]bool{}, attached: map[id.ID]id.ID{}}
	a, b, c := id.New(), id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	ca, _ := e.device(t, a, 1)
	_, db := e.device(t, b, 3)

	// Without core, an attachment cannot be sent.
	_, _, err := e.svc.Send(ctx, a, SendInput{Recipient: b, ClientRequestID: id.New(), Envelopes: []OutgoingEnvelope{envelope(db, 1)}, Session: ca.Session, Attachment: id.New()})
	is(t, err, apperr.ErrUnavailable)
	e.svc.cfg.Attachments = f

	up := id.New()
	f.known[up] = true
	send := func(att id.ID) (Message, error) {
		m, _, err := e.svc.Send(ctx, a, SendInput{Recipient: b, ClientRequestID: id.New(), Envelopes: []OutgoingEnvelope{envelope(db, 1)}, Session: ca.Session, Attachment: att})
		return m, err
	}
	m, err := send(up)
	must(t, err)
	if m.AttachmentUploadID != up || f.attached[up] != m.ID {
		t.Fatalf("mesaj = %+v, bağlı %v", m, f.attached[up])
	}
	// The same attachment in a second message, or an unknown one: nothing
	// is written.
	before := e.count(t, "SELECT count(*) FROM dm_messages")
	_, err = send(up)
	is(t, err, apperr.ErrUploadInUse)
	_, err = send(id.New())
	is(t, err, apperr.ErrUploadNotFound)
	if e.count(t, "SELECT count(*) FROM dm_messages") != before {
		t.Fatal("reddedilen ekli mesaj yazıldı")
	}
	// core is asked outside chat's transaction (ADR-0031). A device set
	// that changed refuses the message after core attached the upload to
	// it; the retry with the refreshed set moves the attachment to its
	// own message (core moves unconfirmed attachments).
	up2 := id.New()
	f.known[up2] = true
	_, _, err = e.svc.Send(ctx, a, SendInput{Recipient: b, ClientRequestID: id.New(), Envelopes: []OutgoingEnvelope{envelope(db, 1), envelope(db, 2)}, Session: ca.Session, Attachment: up2})
	if err == nil || e.count(t, "SELECT count(*) FROM dm_messages WHERE attachment_upload_id = $1", up2.PG()) != 0 {
		t.Fatalf("cihaz kümesi hatası: %v", err)
	}
	m2, err := send(up2)
	if err != nil || f.attached[up2] != m2.ID {
		t.Fatalf("düzeltilmiş tekrar: %v, bağlı %v", err, f.attached[up2])
	}
	// core's reconciliation asks which message holds each file.
	hold, err := e.svc.AttachmentHolders(ctx, []id.ID{up, up2, id.New()})
	if err != nil || len(hold) != 2 || hold[up] != m.ID || hold[up2] != m2.ID {
		t.Fatalf("sahipler = %v %v", hold, err)
	}

	// Who may get the link: the two parties, while the message stands.
	list, _, err := e.svc.Messages(ctx, b, convOf(t, e, a, b), ListInput{})
	must(t, err)
	if len(list) == 0 || list[len(list)-1].AttachmentUploadID != up {
		t.Fatalf("alıcının listesi = %+v", list)
	}
	items := []MediaItem{{Upload: up, Message: m.ID}}
	for who, tc := range map[string]struct {
		user id.ID
		want bool
	}{"gönderen": {a, true}, "alıcı": {b, true}, "üçüncü kişi": {c, false}} {
		got, err := e.svc.VisibleAttachments(ctx, tc.user, items)
		must(t, err)
		if got[up] != tc.want {
			t.Errorf("%s: %v", who, got[up])
		}
	}
	if got, _ := e.svc.VisibleAttachments(ctx, b, []MediaItem{{Upload: up2, Message: m.ID}}); len(got) != 0 {
		t.Fatalf("başka mesajın eki: %v", got)
	}
	// The sender deletes it: the attachment goes at once (event to core)
	// and nobody may fetch it.
	must(t, e.svc.Delete(ctx, a, convOf(t, e, a, b), m.ID))
	if got, _ := e.svc.VisibleAttachments(ctx, b, items); len(got) != 0 {
		t.Fatalf("silinen mesajın eki: %v", got)
	}
	var payload map[string]string
	must(t, e.pool.QueryRow(ctx, "SELECT payload FROM domain_events WHERE type = $1", corerelay.TypeMediaReleased).Scan(&payload))
	if payload["upload_id"] != up.String() || payload["message_id"] != m.ID.String() || len(payload) != 2 {
		t.Fatalf("olay = %v", payload)
	}
	if e.count(t, "SELECT count(*) FROM dm_messages WHERE id = $1 AND attachment_upload_id IS NULL", m.ID.PG()) != 1 {
		t.Fatal("silinen mesajın eki boşalmalı")
	}
}

func convOf(t *testing.T, e *e2eEnv, a, b id.ID) id.ID {
	t.Helper()
	var c id.ID
	lo, hi := a, b
	if hi.String() < lo.String() {
		lo, hi = hi, lo
	}
	must(t, e.pool.QueryRow(ctx, "SELECT id FROM dm_conversations WHERE user_low_id = $1 AND user_high_id = $2", lo.PG(), hi.PG()).Scan(&c))
	return c
}

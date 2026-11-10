package dm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// End-to-end direct messages (issue #127, ADR-0018): dm's send path with
// envelopes, together with keys.Service and dm's own rules as its
// Contacts.

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }

// lockedBuffer collects log output of every service in a test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type e2eEnv struct {
	*env
	keys *keys.Service
	logs *lockedBuffer
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	e := &e2eEnv{env: setup(t), logs: &lockedBuffer{}}
	log := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc.cfg.Log = log
	k, err := keys.New(keys.Config{
		Pool: e.pool, Log: log, Contacts: e.svc, Bundles: allowAll{}, KeyOps: allowAll{}, Archive: allowAll{},
		Now: e.svc.cfg.Now,
	})
	must(t, err)
	e.keys = k
	return e
}

func ecKey(b byte) []byte { return append([]byte{5}, bytes.Repeat([]byte{b}, 32)...) }

// secret marks content: it must never appear in a log, in any encoding.
var secret = []byte("GIZLI-ZARF-ICERIGI-0123456789ABCDEF")

func secretContent(n byte) []byte { return append(append([]byte{}, secret...), n) }

// device registers a new session of user as a device.
func (e *e2eEnv) device(t *testing.T, user id.ID, seed byte) (keys.Caller, keys.Device) {
	t.Helper()
	c := keys.Caller{User: user, Session: id.New(), IssuedAt: e.now}
	in := keys.RegisterInput{
		RegistrationID: 100 + uint32(seed), IdentityKey: append(ecKey(seed)[:1], secret[:32]...),
		SignedPreKey:    keys.SignedPreKey{ID: 1, PublicKey: ecKey(seed + 1), Signature: bytes.Repeat([]byte{seed}, 64)},
		KyberLastResort: keys.SignedPreKey{ID: 1, PublicKey: append([]byte{8}, bytes.Repeat([]byte{seed}, 1568)...), Signature: bytes.Repeat([]byte{seed}, 64)},
		OneTime:         []keys.PreKey{{ID: 7, PublicKey: ecKey(seed + 2)}},
	}
	d, _, err := e.keys.Register(ctx, c, in)
	must(t, err)
	return c, d
}

func envelope(d keys.Device, n byte) OutgoingEnvelope {
	return OutgoingEnvelope{Device: d.ID, RegistrationID: uint32(d.RegistrationID), Type: keys.EnvelopePreKey, Content: secretContent(n)} //nolint:gosec // test values
}

func (e *e2eEnv) sendE2E(c keys.Caller, to id.ID, req id.ID, es ...OutgoingEnvelope) (Message, Conversation, error) {
	return e.svc.Send(ctx, c.User, SendInput{Recipient: to, ClientRequestID: req, Envelopes: es, Session: c.Session})
}

func TestSendE2E(t *testing.T) {
	e := setupE2E(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	ca, _ := e.device(t, a, 1)
	ca2, da2 := e.device(t, a, 2)
	cb, db := e.device(t, b, 3)

	req := id.New()
	m, c, err := e.sendE2E(ca, b, req, envelope(db, 1), envelope(da2, 2))
	must(t, err)
	if m.Encryption != EncryptionE2E || m.Body != "" || c.Status != StatusActive {
		t.Fatalf("mesaj = %+v %+v", m, c)
	}
	// The server keeps no content: no body, no key version.
	if n := e.count(t, `SELECT count(*) FROM dm_messages WHERE id = $1 AND encryption = 'e2e' AND body_encrypted IS NULL AND key_version IS NULL AND sender_device_id IS NOT NULL`, m.ID.PG()); n != 1 {
		t.Fatal("e2e mesaj satırı içerik taşıyor")
	}
	// Each device gets its own envelope, byte for byte; the sending device
	// gets none.
	for _, x := range []struct {
		c    keys.Caller
		want []byte
	}{{cb, secretContent(1)}, {ca2, secretContent(2)}} {
		got, _, err := e.keys.Envelopes(ctx, x.c, 0, "")
		must(t, err)
		if len(got) != 1 || !bytes.Equal(got[0].Content, x.want) || got[0].MessageID != m.ID || got[0].SenderID != a || got[0].SenderNumber != 1 || got[0].ConversationID != c.ID {
			t.Fatalf("zarf = %+v", got)
		}
	}
	if got, _, _ := e.keys.Envelopes(ctx, ca, 0, ""); len(got) != 0 {
		t.Fatalf("gönderen cihaza zarf: %+v", got)
	}
	e.pub.mu.Lock()
	sessions := map[id.ID]bool{}
	for _, d := range e.pub.envelopes {
		sessions[d.Session] = true
	}
	e.pub.mu.Unlock()
	if len(sessions) != 2 || !sessions[cb.Session] || !sessions[ca2.Session] {
		t.Fatalf("canlı teslim = %v", sessions)
	}
	// The recipient's notice and unread count work as for plain messages.
	if e.notices(t, b, "dm_message") != 1 {
		t.Fatal("bildirim yok")
	}
	page, _, err := e.svc.Messages(ctx, b, c.ID, ListInput{})
	must(t, err)
	if len(page) != 1 || page[0].Body != "" || page[0].Encryption != EncryptionE2E {
		t.Fatalf("liste = %+v", page)
	}
	// A retry returns the first message and writes no envelope.
	again, _, err := e.sendE2E(ca, b, req, envelope(db, 9), envelope(da2, 9))
	must(t, err)
	if again.ID != m.ID || e.count(t, `SELECT count(*) FROM dm_envelopes`) != 2 {
		t.Fatal("tekrar deneme yeni zarf yazdı")
	}
	// Acknowledged envelopes are gone.
	got, _, _ := e.keys.Envelopes(ctx, cb, 0, "")
	must(t, e.keys.Ack(ctx, cb, []id.ID{got[0].ID}))
	if got, _, _ := e.keys.Envelopes(ctx, cb, 0, ""); len(got) != 0 {
		t.Fatal("onaylanan zarf kaldı")
	}

	// The sender deletes: waiting envelopes and archive entries go too.
	e.keys.ClaimHistoryKey(ctx, cb) //nolint:errcheck // checked below
	_, err = e.keys.PutArchive(ctx, cb, 1, []keys.ArchiveInput{{MessageID: m.ID, Content: secretContent(3)}})
	must(t, err)
	must(t, e.svc.Delete(ctx, a, c.ID, m.ID))
	if n := e.count(t, `SELECT count(*) FROM dm_envelopes WHERE message_id = $1`, m.ID.PG()) + e.count(t, `SELECT count(*) FROM dm_archive_entries WHERE message_id = $1`, m.ID.PG()); n != 0 {
		t.Fatalf("silinen mesajın %d zarfı/arşivi kaldı", n)
	}
	e.assertNoSecretInLogs(t)
}

func TestSendE2EDeviceSet(t *testing.T) {
	e := setupE2E(t)
	a, b, c := id.New(), id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	e.rules.set(a, c, Verdict{Decision: Active})
	ca, _ := e.device(t, a, 1)
	_, da2 := e.device(t, a, 2)
	_, db := e.device(t, b, 3)

	var changed *apperr.DevicesChangedError
	// The caller's other device is missing.
	_, _, err := e.sendE2E(ca, b, id.New(), envelope(db, 1))
	if !errors.As(err, &changed) || len(changed.Missing) != 1 || changed.Missing[0] != da2.ID.String() || len(changed.Stale) != 0 {
		t.Fatalf("eksik cihaz: %v", err)
	}
	// A replaced device (other registration ID) and an unknown one.
	old := envelope(db, 1)
	old.RegistrationID++
	unknown := OutgoingEnvelope{Device: id.New(), RegistrationID: 1, Type: keys.EnvelopeWhisper, Content: []byte{1}}
	_, _, err = e.sendE2E(ca, b, id.New(), old, envelope(da2, 2), unknown)
	if !errors.As(err, &changed) || len(changed.Stale) != 2 || len(changed.Missing) != 0 {
		t.Fatalf("eskimiş cihaz: %+v %v", changed, err)
	}
	// Twice the same device.
	_, _, err = e.sendE2E(ca, b, id.New(), envelope(db, 1), envelope(db, 1), envelope(da2, 2))
	if !errors.As(err, &changed) || len(changed.Stale) != 1 {
		t.Fatalf("aynı cihaz iki kez: %v", err)
	}
	// Nothing was written by the refused sends.
	if n := e.count(t, `SELECT count(*) FROM dm_messages`) + e.count(t, `SELECT count(*) FROM dm_conversations`); n != 0 {
		t.Fatalf("reddedilen gönderim %d satır yazdı", n)
	}
	// A recipient without devices cannot get anything.
	_, _, err = e.sendE2E(ca, c, id.New(), envelope(da2, 2))
	is(t, err, apperr.ErrE2ERecipientUnavailable)
	// A session without a device cannot send end to end.
	_, _, err = e.sendE2E(keys.Caller{User: a, Session: id.New()}, b, id.New(), envelope(db, 1), envelope(da2, 2))
	is(t, err, apperr.ErrE2EDeviceNotRegistered)
	// No downgrade: plain text is refused once a party has a device.
	_, _, err = e.send(t, a, b, "düz metin")
	is(t, err, apperr.ErrE2ERequired)
	_, _, err = e.send(t, c, a, "düz metin")
	is(t, err, apperr.ErrE2ERequired)

	for name, in := range map[string]SendInput{
		"body":      {Recipient: b, Body: "x", Envelopes: []OutgoingEnvelope{envelope(db, 1)}, Session: ca.Session, ClientRequestID: id.New()},
		"envelopes": {Recipient: b, Envelopes: []OutgoingEnvelope{{Device: db.ID, Type: 1, Content: []byte{1}}}, Session: ca.Session, ClientRequestID: id.New()},
	} {
		_, _, err := e.svc.Send(ctx, a, in)
		var v *apperr.ValidationError
		if !errors.As(err, &v) || v.Field != name {
			t.Fatalf("%s: %v", name, err)
		}
	}
	big := envelope(db, 1)
	big.Content = make([]byte, keys.MaxEnvelopeBytes+1)
	_, _, err = e.sendE2E(ca, b, id.New(), big, envelope(da2, 2))
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "envelopes" {
		t.Fatalf("büyük zarf: %v", err)
	}
}

// End-to-end messages need no server key.
func TestSendE2EWithoutMessageKey(t *testing.T) {
	e := setupE2E(t)
	e.svc.cfg.Cipher = nil
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 5})
	ca, _ := e.device(t, a, 1)
	_, db := e.device(t, b, 2)
	_, c, err := e.sendE2E(ca, b, id.New(), envelope(db, 1))
	must(t, err)
	if c.Status != StatusRequestSent {
		t.Fatalf("istek = %+v", c)
	}
	// The request rules hold for end-to-end messages too.
	_, _, err = e.sendE2E(ca, b, id.New(), envelope(db, 2))
	is(t, err, apperr.ErrMessageRequestPending)
}

// Pre-key bundles follow the send rules: strangers, a blocked user and a
// requester waiting for an answer cannot use up one-time pre-keys.
func TestBundlesFollowSendRules(t *testing.T) {
	e := setupE2E(t)
	a, b, stranger := id.New(), id.New(), id.New()
	ca, _ := e.device(t, a, 1)
	_, db := e.device(t, b, 2)
	cs := keys.Caller{User: stranger, Session: id.New()}

	_, err := e.keys.Bundles(ctx, cs, b, nil)
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	e.rules.set(a, b, Verdict{Decision: Request, RequestLimit: 5})
	got, err := e.keys.Bundles(ctx, ca, b, nil)
	must(t, err)
	if len(got) != 1 || got[0].DeviceID != db.ID || got[0].OneTime == nil {
		t.Fatalf("paket = %+v", got)
	}
	_, _, err = e.sendE2E(ca, b, id.New(), envelope(db, 1))
	must(t, err)
	_, err = e.keys.Bundles(ctx, ca, b, nil)
	is(t, err, apperr.ErrMessageRequestPending)
	// The recipient may answer: their bundles for a are allowed.
	e.device(t, b, 3)
	_, err = e.keys.Devices(ctx, stranger, b)
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	list, err := e.keys.Devices(ctx, a, b)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("cihazlar = %+v", list)
	}
	// The daily request limit counts for opening a new conversation.
	other := id.New()
	e.rules.set(a, other, Verdict{Decision: Request, RequestLimit: 1})
	e.device(t, other, 4)
	_, err = e.keys.Bundles(ctx, ca, other, nil)
	is(t, err, apperr.ErrMessageRequestLimit)
	e.assertNoSecretInLogs(t)
}

// Nothing the clients send as content or keys reaches a log, in any
// encoding, also on error paths (issue #127 acceptance criterion; CLAUDE.md
// rule 3).
func TestE2ENothingSecretInLogs(t *testing.T) {
	e := setupE2E(t)
	a, b := id.New(), id.New()
	e.rules.set(a, b, Verdict{Decision: Active})
	ca, _ := e.device(t, a, 1)
	cb, db := e.device(t, b, 2)
	m, c, err := e.sendE2E(ca, b, id.New(), envelope(db, 1))
	must(t, err)
	_, _, _ = e.sendE2E(ca, b, id.New(), envelope(db, 1), OutgoingEnvelope{Device: id.New(), Type: 2, Content: secret})
	_, _ = e.keys.ClaimHistoryKey(ctx, cb)
	_, _ = e.keys.PutArchive(ctx, cb, 1, []keys.ArchiveInput{{MessageID: m.ID, Content: secret}})
	_, _ = e.keys.PutArchive(ctx, cb, 9, []keys.ArchiveInput{{MessageID: m.ID, Content: secret}})
	_, _, _ = e.keys.Archive(ctx, cb, c.ID, keys.ArchiveListInput{})
	_, _, _ = e.keys.Envelopes(ctx, cb, 0, "")
	link, _ := e.keys.CreateLink(ctx, ca)
	_ = e.keys.AnswerLink(ctx, cb, link.ID, 1, secret)
	_, _ = e.keys.GetLink(ctx, ca, link.ID)
	_ = e.keys.Rotate(ctx, cb, 2, []keys.ShareInput{{Device: id.New(), Bundle: secret}})
	_, _ = e.keys.Upload(ctx, ca, keys.UploadInput{OneTime: []keys.PreKey{{ID: 1, PublicKey: append([]byte{5}, secret[:32]...)}}})
	_, _ = e.keys.Bundles(ctx, ca, b, nil)
	e.advance(keys.DeviceRetention + time.Hour)
	_, _ = e.keys.Retain(ctx)
	must(t, e.keys.SessionEnded(ctx, cb.Session))
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	must(t, keys.Cleanup(ctx, store.New(tx), a))
	must(t, tx.Commit(ctx))
	// The retention job logs its counts (and only them).
	e.advance(keys.KeyEventRetention + time.Hour)
	run, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	must(t, e.keys.RunRetention(run))
	if !strings.Contains(e.logs.String(), "e2e saklama") {
		t.Fatal("log yakalanmadı")
	}
	e.assertNoSecretInLogs(t)
}

func (e *e2eEnv) assertNoSecretInLogs(t *testing.T) {
	t.Helper()
	logs := e.logs.String()
	marker := secret[:20]
	for name, enc := range map[string]string{
		"ham":    string(marker),
		"base64": base64.StdEncoding.EncodeToString(secret)[:24],
		"hex":    hex.EncodeToString(marker),
	} {
		if strings.Contains(logs, enc) {
			t.Fatalf("log içerik taşıyor (%s)", name)
		}
	}
}

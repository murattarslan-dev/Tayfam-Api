package keys_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("hata = %v, %v bekleniyordu", err, want)
	}
}

func invalidField(t *testing.T, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Fatalf("hata = %v, %s alanı bekleniyordu", err, field)
	}
}

// Well-formed (not real) keys: the server checks the format only.
func ecKey(b byte) []byte    { return append([]byte{5}, bytes.Repeat([]byte{b}, 32)...) }
func kyberKey(b byte) []byte { return append([]byte{8}, bytes.Repeat([]byte{b}, 1568)...) }
func sig(b byte) []byte      { return bytes.Repeat([]byte{b}, 64) }

// fakeContacts stands in for dm's rules: who may write to whom and who has
// a conversation.
type fakeContacts struct {
	mu    sync.Mutex
	write map[[2]id.ID]error // missing: ErrDirectMessageNotAllowed
	conv  map[[2]id.ID]bool
}

func (f *fakeContacts) MayWrite(_ context.Context, caller, recipient id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.write[[2]id.ID{caller, recipient}]; ok {
		return err
	}
	return apperr.ErrDirectMessageNotAllowed
}

func (f *fakeContacts) HasConversation(_ context.Context, caller, other id.ID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conv[[2]id.ID{caller, other}], nil
}

func (f *fakeContacts) allow(a, b id.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.write[[2]id.ID{a, b}], f.write[[2]id.ID{b, a}] = nil, nil
}

type recPub struct {
	mu     sync.Mutex
	low    []id.ID
	linked []id.ID
	shared []id.ID
}

func (r *recPub) PreKeysLow(s id.ID, _ keys.Counts) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.low = append(r.low, s)
}

func (r *recPub) LinkAnswered(s, _ id.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.linked = append(r.linked, s)
}

func (r *recPub) HistoryKeyShared(s id.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shared = append(r.shared, s)
}

type allow struct{ deny bool }

func (a *allow) Allow(string) bool { return !a.deny }

type env struct {
	svc      *keys.Service
	pool     *pgxpool.Pool
	contacts *fakeContacts
	pub      *recPub
	limit    *allow
	log      *bytes.Buffer
	mu       sync.Mutex
	now      time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{
		pool:     dbtest.New(t, "chat", chatdb.Migrations()),
		contacts: &fakeContacts{write: map[[2]id.ID]error{}, conv: map[[2]id.ID]bool{}},
		pub:      &recPub{}, limit: &allow{}, log: &bytes.Buffer{}, now: time.Now().UTC(),
	}
	svc, err := keys.New(keys.Config{
		Pool: e.pool, Log: slog.New(slog.NewJSONHandler(syncWriter{e}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Contacts: e.contacts, Publisher: e.pub, Bundles: e.limit, KeyOps: e.limit, Archive: e.limit,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
	})
	must(t, err)
	e.svc = svc
	return e
}

type syncWriter struct{ e *env }

func (w syncWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.e.log.Write(p)
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// caller is a new session of user with a token issued now.
func (e *env) caller(user id.ID) keys.Caller {
	e.mu.Lock()
	defer e.mu.Unlock()
	return keys.Caller{User: user, Session: id.New(), IssuedAt: e.now}
}

func registration(seed byte, oneTime int) keys.RegisterInput {
	in := keys.RegisterInput{
		RegistrationID: 100 + uint32(seed), IdentityKey: ecKey(seed),
		SignedPreKey:    keys.SignedPreKey{ID: 1, PublicKey: ecKey(seed + 1), Signature: sig(seed)},
		KyberLastResort: keys.SignedPreKey{ID: 1, PublicKey: kyberKey(seed), Signature: sig(seed)},
	}
	for i := range oneTime {
		in.OneTime = append(in.OneTime, keys.PreKey{ID: uint32(i + 10), PublicKey: ecKey(byte(i))})
		in.Kyber = append(in.Kyber, keys.SignedPreKey{ID: uint32(i + 10), PublicKey: kyberKey(byte(i)), Signature: sig(byte(i))})
	}
	return in
}

func (e *env) register(t *testing.T, c keys.Caller, seed byte) keys.Device {
	t.Helper()
	d, _, err := e.svc.Register(ctx, c, registration(seed, 12))
	must(t, err)
	return d
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func TestRegister(t *testing.T) {
	e := setup(t)
	u := id.New()
	c := e.caller(u)
	d, n, err := e.svc.Register(ctx, c, registration(1, 12))
	must(t, err)
	if d.Number != 1 || d.Paired || n.EC != 12 || n.Kyber != 12 || n.Low() {
		t.Fatalf("kayıt = %+v %+v", d, n)
	}
	// A retry with the same keys returns the same device.
	again, _, err := e.svc.Register(ctx, c, registration(1, 0))
	must(t, err)
	if again.ID != d.ID {
		t.Fatalf("tekrar deneme yeni cihaz açtı")
	}
	// Other keys in the same session: unregister first.
	_, _, err = e.svc.Register(ctx, c, registration(2, 0))
	is(t, err, apperr.ErrE2EDeviceAlreadyRegistered)
	if got := e.count(t, `SELECT count(*) FROM user_key_events WHERE user_id = $1 AND event_type = 'device_added'`, u.PG()); got != 1 {
		t.Fatalf("device_added = %d", got)
	}
	// The next devices get the next numbers; the sixth is refused.
	for i := 2; i <= keys.MaxDevices; i++ {
		if d := e.register(t, e.caller(u), byte(i)); d.Number != i {
			t.Fatalf("cihaz numarası %d, %d bekleniyordu", d.Number, i)
		}
	}
	_, _, err = e.svc.Register(ctx, e.caller(u), registration(9, 0))
	is(t, err, apperr.ErrE2EDeviceLimit)
	// A freed number is not reused at once: numbers go on increasing.
	must(t, e.svc.Unregister(ctx, c))
	if d := e.register(t, e.caller(u), 7); d.Number != 6 {
		t.Fatalf("yeni numara %d, 6 bekleniyordu", d.Number)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := setup(t)
	c := e.caller(id.New())
	for field, change := range map[string]func(*keys.RegisterInput){
		"registration_id":          func(in *keys.RegisterInput) { in.RegistrationID = keys.MaxRegistrationID + 1 },
		"identity_key":             func(in *keys.RegisterInput) { in.IdentityKey = in.IdentityKey[1:] },
		"signed_prekey":            func(in *keys.RegisterInput) { in.SignedPreKey.Signature = nil },
		"kyber_last_resort_prekey": func(in *keys.RegisterInput) { in.KyberLastResort.PublicKey = ecKey(1) },
		"one_time_prekeys":         func(in *keys.RegisterInput) { in.OneTime = append(in.OneTime, in.OneTime[0]) },
		"kyber_prekeys":            func(in *keys.RegisterInput) { in.Kyber[0].ID = keys.MaxPreKeyID + 1 },
	} {
		in := registration(1, 2)
		change(&in)
		_, _, err := e.svc.Register(ctx, c, in)
		invalidField(t, err, field)
	}
	in := registration(1, 0)
	for i := range keys.MaxPreKeysPerCall + 1 {
		in.OneTime = append(in.OneTime, keys.PreKey{ID: uint32(i), PublicKey: ecKey(1)})
	}
	_, _, err := e.svc.Register(ctx, c, in)
	invalidField(t, err, "one_time_prekeys")
	e.limit.deny = true
	_, _, err = e.svc.Register(ctx, c, registration(1, 0))
	is(t, err, apperr.ErrRateLimited)
}

// A device is a session: an ended session, or a token issued before a
// sanction, cannot register (ADR-0018 item 1).
func TestEndedSessions(t *testing.T) {
	e := setup(t)
	u := id.New()
	c := e.caller(u)
	d := e.register(t, c, 1)
	must(t, e.svc.SessionEnded(ctx, c.Session))
	must(t, e.svc.SessionEnded(ctx, c.Session)) // idempotent
	if got := e.count(t, `SELECT count(*) FROM device_keys WHERE id = $1`, d.ID.PG()); got != 0 {
		t.Fatal("biten oturumun cihazı kaldı")
	}
	if got := e.count(t, `SELECT count(*) FROM user_key_events WHERE device_id = $1 AND event_type = 'device_removed'`, d.ID.PG()); got != 1 {
		t.Fatalf("device_removed = %d", got)
	}
	_, _, err := e.svc.Register(ctx, c, registration(1, 0))
	is(t, err, apperr.ErrSessionEnded)

	old := e.caller(u) // issued before the sanction
	e.register(t, e.caller(u), 2)
	e.advance(time.Second)
	must(t, e.svc.UserSanctioned(ctx, u, e.now))
	if got := e.count(t, `SELECT count(*) FROM device_keys WHERE user_id = $1`, u.PG()); got != 0 {
		t.Fatalf("yaptırımda %d cihaz kaldı", got)
	}
	_, _, err = e.svc.Register(ctx, old, registration(3, 0))
	is(t, err, apperr.ErrSessionEnded)
	e.advance(time.Second)
	e.register(t, e.caller(u), 4) // a new sign-in after the sanction
}

func TestUploadAndBundles(t *testing.T) {
	e := setup(t)
	a, b, stranger := id.New(), id.New(), id.New()
	ca, cb := e.caller(a), e.caller(b)
	e.register(t, ca, 1)
	db := e.register(t, cb, 2)
	cb2 := e.caller(b)
	db2 := e.register(t, cb2, 3)

	// Strangers cannot fetch (and so cannot use up one-time pre-keys).
	_, err := e.svc.Bundles(ctx, e.caller(stranger), b, nil)
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	e.contacts.mu.Lock()
	e.contacts.write[[2]id.ID{a, b}] = apperr.ErrMessageRequestPending
	e.contacts.mu.Unlock()
	_, err = e.svc.Bundles(ctx, ca, b, nil)
	is(t, err, apperr.ErrMessageRequestPending)

	e.contacts.allow(a, b)
	got, err := e.svc.Bundles(ctx, ca, b, nil)
	must(t, err)
	if len(got) != 2 || got[0].OneTime == nil || got[0].KyberLastResort || got[0].RegistrationID != 102 {
		t.Fatalf("paketler = %+v", got)
	}
	// One-time keys were used up by that fetch.
	st, err := e.svc.MyDevice(ctx, cb)
	must(t, err)
	if st.Counts.EC != 11 || st.Counts.Kyber != 11 {
		t.Fatalf("kalan = %+v", st.Counts)
	}
	// The user's own other devices, never the calling one.
	own, err := e.svc.Bundles(ctx, cb, b, nil)
	must(t, err)
	if len(own) != 1 || own[0].DeviceID != db2.ID {
		t.Fatalf("kendi paketleri = %+v", own)
	}
	only, err := e.svc.Bundles(ctx, ca, b, []id.ID{db.ID})
	must(t, err)
	if len(only) != 1 || only[0].DeviceID != db.ID {
		t.Fatalf("seçili cihaz = %+v", only)
	}
	// Running out: no one-time EC key, the last-resort Kyber key, and a
	// "low" signal to the device's session.
	for range 12 {
		_, err := e.svc.Bundles(ctx, ca, b, []id.ID{db.ID})
		must(t, err)
	}
	last, err := e.svc.Bundles(ctx, ca, b, []id.ID{db.ID})
	must(t, err)
	if last[0].OneTime != nil || !last[0].KyberLastResort || !bytes.Equal(last[0].Kyber.PublicKey, kyberKey(2)) {
		t.Fatalf("tükenmiş paket = %+v", last[0])
	}
	e.pub.mu.Lock()
	lowSent := len(e.pub.low) > 0 && e.pub.low[len(e.pub.low)-1] == cb.Session
	e.pub.mu.Unlock()
	if !lowSent {
		t.Fatal("azalma sinyali gitmedi")
	}

	// Upload refills and replaces the signed pre-key.
	n, err := e.svc.Upload(ctx, cb, keys.UploadInput{
		SignedPreKey: &keys.SignedPreKey{ID: 2, PublicKey: ecKey(9), Signature: sig(9)},
		OneTime:      []keys.PreKey{{ID: 500, PublicKey: ecKey(5)}},
	})
	must(t, err)
	if n.EC != 1 {
		t.Fatalf("yükleme sonrası = %+v", n)
	}
	again, err := e.svc.Bundles(ctx, ca, b, []id.ID{db.ID})
	must(t, err)
	if again[0].SignedPreKey.ID != 2 || again[0].OneTime == nil || again[0].OneTime.ID != 500 {
		t.Fatalf("yeni paket = %+v", again[0])
	}
	_, err = e.svc.Upload(ctx, e.caller(stranger), keys.UploadInput{})
	is(t, err, apperr.ErrE2EDeviceNotRegistered)
}

func TestDevicesAndKeyEventsPermission(t *testing.T) {
	e := setup(t)
	a, b, stranger := id.New(), id.New(), id.New()
	e.register(t, e.caller(b), 1)
	_, err := e.svc.Devices(ctx, stranger, b)
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	_, _, err = e.svc.KeyEvents(ctx, stranger, b, 0, "")
	is(t, err, apperr.ErrDirectMessageNotAllowed)
	// A party of a conversation sees them even when they may not write.
	e.contacts.mu.Lock()
	e.contacts.conv[[2]id.ID{a, b}] = true
	e.contacts.mu.Unlock()
	list, err := e.svc.Devices(ctx, a, b)
	must(t, err)
	if len(list) != 1 || !bytes.Equal(list[0].IdentityKey, ecKey(1)) {
		t.Fatalf("cihazlar = %+v", list)
	}
	ev, _, err := e.svc.KeyEvents(ctx, a, b, 0, "")
	must(t, err)
	if len(ev) != 1 || ev[0].Type != keys.EventDeviceAdded {
		t.Fatalf("olaylar = %+v", ev)
	}
	own, err := e.svc.Devices(ctx, b, b)
	must(t, err)
	if len(own) != 1 {
		t.Fatalf("kendi cihazları = %+v", own)
	}
}

// pairFirst makes c's device the user's first paired device.
func (e *env) pairFirst(t *testing.T, c keys.Caller) int {
	t.Helper()
	v, err := e.svc.ClaimHistoryKey(ctx, c)
	must(t, err)
	return v
}

func TestPairingAndRotation(t *testing.T) {
	e := setup(t)
	u, other := id.New(), id.New()
	first, second, third := e.caller(u), e.caller(u), e.caller(u)
	d1 := e.register(t, first, 1)
	d2 := e.register(t, second, 2)
	e.register(t, third, 3)

	if v := e.pairFirst(t, first); v != 1 {
		t.Fatalf("ilk sürüm %d", v)
	}
	if v := e.pairFirst(t, first); v != 1 { // already paired: the current one
		t.Fatalf("tekrar sürüm %d", v)
	}
	_, err := e.svc.ClaimHistoryKey(ctx, second)
	is(t, err, apperr.ErrE2EPairedDeviceExists)
	_, err = e.svc.CreateLink(ctx, first)
	is(t, err, apperr.ErrE2EAlreadyPaired)

	// QR pairing: the new device opens, the paired one answers, the new one
	// reads and completes.
	req, err := e.svc.CreateLink(ctx, second)
	must(t, err)
	is(t, e.svc.AnswerLink(ctx, third, req.ID, 1, []byte{1}), apperr.ErrE2ENotPaired)
	is(t, e.svc.AnswerLink(ctx, e.caller(other), req.ID, 1, []byte{1}), apperr.ErrE2EDeviceNotRegistered)
	is(t, e.svc.AnswerLink(ctx, first, req.ID, 2, []byte{1}), apperr.ErrE2EHistoryKeyStale)
	is(t, e.svc.AnswerLink(ctx, first, id.New(), 1, []byte{1}), apperr.ErrE2ELinkRequestNotFound)
	_, err = e.svc.CompleteLink(ctx, second, req.ID)
	is(t, err, apperr.ErrE2ELinkRequestNotFound) // not answered yet
	must(t, e.svc.AnswerLink(ctx, first, req.ID, 1, []byte("anahtar")))
	is(t, e.svc.AnswerLink(ctx, first, req.ID, 1, []byte{1}), apperr.ErrE2ELinkRequestNotFound) // once
	_, err = e.svc.GetLink(ctx, third, req.ID)
	is(t, err, apperr.ErrE2ELinkRequestNotFound) // another device's request
	got, err := e.svc.GetLink(ctx, second, req.ID)
	must(t, err)
	if !got.Answered || string(got.Answer) != "anahtar" || got.AnsweredBy != d1.ID || got.Version != 1 {
		t.Fatalf("istek = %+v", got)
	}
	dev, err := e.svc.CompleteLink(ctx, second, req.ID)
	must(t, err)
	if !dev.Paired || dev.ID != d2.ID {
		t.Fatalf("tamamlama = %+v", dev)
	}
	e.pub.mu.Lock()
	if len(e.pub.linked) != 1 || e.pub.linked[0] != second.Session {
		t.Fatalf("eşleştirme bildirimi = %v", e.pub.linked)
	}
	e.pub.mu.Unlock()

	// An expired request cannot be answered.
	late, err := e.svc.CreateLink(ctx, third)
	must(t, err)
	e.advance(keys.LinkLifetime + time.Second)
	is(t, e.svc.AnswerLink(ctx, first, late.ID, 1, []byte{1}), apperr.ErrE2ELinkRequestNotFound)

	// Removing a paired device asks for a rotation; the rotation must
	// cover exactly the other paired devices.
	must(t, e.svc.SessionEnded(ctx, third.Session)) // unpaired: no rotation
	st, err := e.svc.MyDevice(ctx, first)
	must(t, err)
	if st.RotationNeeded || !st.OtherPaired {
		t.Fatalf("durum = %+v", st)
	}
	fourth := e.caller(u)
	e.register(t, fourth, 4)
	req4, err := e.svc.CreateLink(ctx, fourth)
	must(t, err)
	must(t, e.svc.AnswerLink(ctx, first, req4.ID, 1, []byte{1}))
	_, err = e.svc.CompleteLink(ctx, fourth, req4.ID)
	must(t, err)
	must(t, e.svc.Unregister(ctx, second)) // paired
	st, err = e.svc.MyDevice(ctx, first)
	must(t, err)
	if !st.RotationNeeded {
		t.Fatal("yenileme istenmedi")
	}
	d4, err := e.svc.MyDevice(ctx, fourth)
	must(t, err)
	var changed *apperr.DevicesChangedError
	err = e.svc.Rotate(ctx, first, 2, []keys.ShareInput{{Device: d2.ID, Bundle: []byte{1}}})
	if !errors.As(err, &changed) || len(changed.Missing) != 1 || changed.Missing[0] != d4.Device.ID.String() || len(changed.Stale) != 1 {
		t.Fatalf("eksik paylaşım: %v", err)
	}
	is(t, e.svc.Rotate(ctx, first, 3, []keys.ShareInput{{Device: d4.Device.ID, Bundle: []byte{1}}}), apperr.ErrE2EHistoryKeyStale)
	must(t, e.svc.Rotate(ctx, first, 2, []keys.ShareInput{{Device: d4.Device.ID, Bundle: []byte("yeni")}}))
	st, err = e.svc.MyDevice(ctx, first)
	must(t, err)
	if st.RotationNeeded || st.HistoryKeyVersion != 2 {
		t.Fatalf("yenileme sonrası = %+v", st)
	}
	shares, err := e.svc.Shares(ctx, fourth)
	must(t, err)
	if len(shares) != 1 || shares[0].Version != 2 || string(shares[0].Bundle) != "yeni" {
		t.Fatalf("paylaşımlar = %+v", shares)
	}
	// Another device cannot acknowledge them.
	must(t, e.svc.AckShares(ctx, first, []id.ID{shares[0].ID}))
	if s, _ := e.svc.Shares(ctx, fourth); len(s) != 1 {
		t.Fatal("başka cihaz paylaşımı sildi")
	}
	must(t, e.svc.AckShares(ctx, fourth, []id.ID{shares[0].ID}))
	if s, _ := e.svc.Shares(ctx, fourth); len(s) != 0 {
		t.Fatal("paylaşım onaylanmadı")
	}
}

func TestArchiveNeedsPairedDeviceAndCurrentVersion(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	ca := e.caller(a)
	e.register(t, ca, 1)
	conv, msg := id.New(), id.New()
	_, err := e.pool.Exec(ctx, `INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, last_message_at) VALUES ($1, least($2::uuid, $3::uuid), greatest($2::uuid, $3::uuid), 'active', now())`, conv.PG(), a.PG(), b.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO dm_messages (id, conversation_id, sender_id, encryption, client_request_id) VALUES ($1, $2, $3, 'e2e', $4)`, msg.PG(), conv.PG(), b.PG(), id.New().PG())
	must(t, err)
	in := []keys.ArchiveInput{{MessageID: msg, Content: []byte("arşiv")}}

	_, err = e.svc.PutArchive(ctx, ca, 1, in)
	is(t, err, apperr.ErrE2ENotPaired)
	e.pairFirst(t, ca)
	_, err = e.svc.PutArchive(ctx, ca, 2, in)
	is(t, err, apperr.ErrE2EHistoryKeyStale)
	got, err := e.svc.PutArchive(ctx, ca, 1, append(in, keys.ArchiveInput{MessageID: id.New(), Content: []byte{1}}))
	must(t, err)
	if len(got) != 1 || got[0] != msg {
		t.Fatalf("kabul edilen = %v", got)
	}
	list, _, err := e.svc.Archive(ctx, ca, conv, keys.ArchiveListInput{})
	must(t, err)
	if len(list) != 1 || string(list[0].Content) != "arşiv" || list[0].KeyVersion != 1 {
		t.Fatalf("arşiv = %+v", list)
	}
	_, err = e.svc.PutArchive(ctx, ca, 1, []keys.ArchiveInput{{MessageID: msg, Content: make([]byte, keys.MaxArchiveBytes+1)}})
	invalidField(t, err, "entries")
	must(t, e.svc.DeleteArchive(ctx, ca, conv))
	if list, _, _ := e.svc.Archive(ctx, ca, conv, keys.ArchiveListInput{}); len(list) != 0 {
		t.Fatal("arşiv silinmedi")
	}
}

func TestCleanupAndRetention(t *testing.T) {
	e := setup(t)
	u, v := id.New(), id.New()
	cu := e.caller(u)
	e.register(t, cu, 1)
	e.pairFirst(t, cu)
	cv := e.caller(v)
	e.register(t, cv, 2)

	tx, err := e.pool.Begin(ctx)
	must(t, err)
	must(t, keys.Cleanup(ctx, store.New(tx), u))
	must(t, keys.Cleanup(ctx, store.New(tx), u)) // idempotent
	must(t, tx.Commit(ctx))
	for table, sql := range map[string]string{
		"device_keys":     `SELECT count(*) FROM device_keys WHERE user_id = $1`,
		"user_key_events": `SELECT count(*) FROM user_key_events WHERE user_id = $1`,
		"prekeys":         `SELECT count(*) FROM device_one_time_prekeys p JOIN device_keys d ON d.id = p.device_id WHERE d.user_id = $1`,
	} {
		if got := e.count(t, sql, u.PG()); got != 0 {
			t.Errorf("%s: %d kayıt kaldı", table, got)
		}
	}
	// A token the deleted user still holds cannot register.
	_, _, err = e.svc.Register(ctx, cu, registration(3, 0))
	is(t, err, apperr.ErrSessionEnded)

	// 90 days unseen: the device goes, with a key event; later the ended
	// state of the deleted user goes too.
	e.advance(keys.DeviceRetention + time.Hour)
	r, err := e.svc.Retain(ctx)
	must(t, err)
	// The device_added event is older than 90 days already.
	if r.Devices != 1 || r.States != 1 || r.KeyEvents != 1 {
		t.Fatalf("saklama = %+v", r)
	}
	if got := e.count(t, `SELECT count(*) FROM user_key_events WHERE user_id = $1 AND event_type = 'device_removed'`, v.PG()); got != 1 {
		t.Fatalf("device_removed = %d", got)
	}
	e.advance(keys.KeyEventRetention + time.Hour)
	r, err = e.svc.Retain(ctx)
	must(t, err)
	if r.KeyEvents != 1 {
		t.Fatalf("anahtar olayı saklaması = %+v", r)
	}
}

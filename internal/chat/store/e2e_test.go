package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ecKey is a well-formed (not real) Curve25519 public key: type byte 0x05
// and 32 bytes. The server checks the format only (ADR-0018).
func ecKey(b byte) []byte { return append([]byte{5}, bytes.Repeat([]byte{b}, 32)...) }

func kyberKey(b byte) []byte { return append([]byte{8}, bytes.Repeat([]byte{b}, 1568)...) }

func sig(b byte) []byte { return bytes.Repeat([]byte{b}, 64) }

func newDevice(t *testing.T, q *Queries, user id.ID, number int16) id.ID {
	t.Helper()
	d := id.New()
	now := time.Now()
	if _, err := q.CreateDevice(context.Background(), CreateDeviceParams{
		ID: d.PG(), UserID: user.PG(), SessionID: id.New().PG(), DeviceNumber: number, RegistrationID: 1234,
		IdentityKey: ecKey(1), SignedPrekeyID: 1, SignedPrekey: ecKey(2), SignedPrekeySignature: sig(3),
		KyberPrekeyID: 1, KyberPrekey: kyberKey(4), KyberPrekeySignature: sig(5), CreatedAt: at(now),
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func addECPreKeys(t *testing.T, q *Queries, device id.ID, from, n int32) {
	t.Helper()
	p := InsertECPreKeysParams{DeviceID: device.PG()}
	for i := range n {
		p.KeyIds = append(p.KeyIds, from+i)
		p.PublicKeys = append(p.PublicKeys, ecKey(byte(i)))
	}
	got, err := q.InsertECPreKeys(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(n) {
		t.Fatalf("eklenen ön anahtar: %d, %d bekleniyordu", got, n)
	}
}

// claimAll claims from device with workers concurrent calls, each in its
// own transaction, and returns the key IDs handed out.
func claimAll(t *testing.T, pool *pgxpool.Pool, device id.ID, workers int) []int32 {
	t.Helper()
	var (
		mu   sync.Mutex
		got  []int32
		wg   sync.WaitGroup
		errs []error
	)
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
				r, err := New(tx).ClaimOneTimePreKey(context.Background(), ClaimOneTimePreKeyParams{DeviceID: device.PG(), Kind: "ec"})
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				// Hold the lock a moment so the others really overlap.
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				got = append(got, r.KeyID)
				mu.Unlock()
				return nil
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("çekme hatası: %v", errs[0])
	}
	return got
}

// A one-time pre-key is handed out once, even to concurrent callers
// (ADR-0018 madde 2; issue #127 acceptance criterion).
func TestClaimOneTimePreKeyIsSingleUseUnderConcurrency(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	user := id.New()
	device := newDevice(t, q, user, 1)

	t.Run("tek anahtar", func(t *testing.T) {
		addECPreKeys(t, q, device, 100, 1)
		if got := claimAll(t, pool, device, 20); len(got) != 1 || got[0] != 100 {
			t.Fatalf("tek anahtar %v kez verildi", got)
		}
	})
	t.Run("çok anahtar", func(t *testing.T) {
		addECPreKeys(t, q, device, 1000, 30)
		got := claimAll(t, pool, device, 60)
		seen := map[int32]bool{}
		for _, k := range got {
			if seen[k] {
				t.Fatalf("anahtar %d iki kez verildi", k)
			}
			seen[k] = true
		}
		// SKIP LOCKED may leave a caller empty-handed while another holds a
		// lock, but never hands out more than exist or the same key twice.
		if len(got) == 0 || len(got) > 30 {
			t.Fatalf("%d anahtar verildi", len(got))
		}
		c, err := q.CountOneTimePreKeys(ctx, device.PG())
		if err != nil {
			t.Fatal(err)
		}
		if int(c.Ec)+len(got) != 30 {
			t.Fatalf("kalan %d + verilen %d ≠ 30", c.Ec, len(got))
		}
	})
	t.Run("başka tür ve cihaz etkilenmez", func(t *testing.T) {
		other := newDevice(t, q, user, 2)
		addECPreKeys(t, q, other, 1, 1)
		if _, err := q.ClaimOneTimePreKey(ctx, ClaimOneTimePreKeyParams{DeviceID: device.PG(), Kind: "kyber"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("kyber yokken: %v", err)
		}
		c, err := q.CountOneTimePreKeys(ctx, other.PG())
		if err != nil || c.Ec != 1 {
			t.Fatalf("diğer cihazın anahtarı: %v %v", c, err)
		}
	})
}

func TestPreKeyUploadIsIdempotent(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	device := newDevice(t, q, id.New(), 1)
	addECPreKeys(t, q, device, 1, 3)
	// The same IDs again (a retry) change nothing, even with other bytes.
	n, err := q.InsertECPreKeys(ctx, InsertECPreKeysParams{DeviceID: device.PG(), KeyIds: []int32{1, 2}, PublicKeys: [][]byte{ecKey(9), ecKey(9)}})
	if err != nil || n != 0 {
		t.Fatalf("tekrar yükleme: %d %v", n, err)
	}
	if _, err := q.InsertKyberPreKeys(ctx, InsertKyberPreKeysParams{
		DeviceID: device.PG(), KeyIds: []int32{1}, PublicKeys: [][]byte{kyberKey(1)}, Signatures: [][]byte{sig(1)},
	}); err != nil {
		t.Fatal(err)
	}
	c, err := q.CountOneTimePreKeys(ctx, device.PG())
	if err != nil || c.Ec != 3 || c.Kyber != 1 {
		t.Fatalf("sayılar: %+v %v", c, err)
	}
	r, err := q.ClaimOneTimePreKey(ctx, ClaimOneTimePreKeyParams{DeviceID: device.PG(), Kind: "ec"})
	if err != nil || r.KeyID != 1 || !bytes.Equal(r.PublicKey, ecKey(0)) || r.Signature != nil {
		t.Fatalf("ilk anahtar: %+v %v", r, err)
	}
}

func wantConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != constraint {
		t.Fatalf("hata = %v, %s bekleniyordu", err, constraint)
	}
}

func TestE2EConstraints(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	user := id.New()
	newDevice(t, q, user, 1)
	base := func() CreateDeviceParams {
		return CreateDeviceParams{
			ID: id.New().PG(), UserID: user.PG(), SessionID: id.New().PG(), DeviceNumber: 2, RegistrationID: 1,
			IdentityKey: ecKey(1), SignedPrekeyID: 1, SignedPrekey: ecKey(2), SignedPrekeySignature: sig(3),
			KyberPrekeyID: 1, KyberPrekey: kyberKey(4), KyberPrekeySignature: sig(5), CreatedAt: at(time.Now()),
		}
	}
	for name, c := range map[string]struct {
		change     func(*CreateDeviceParams)
		constraint string
	}{
		"kimlik anahtarı türü":  {func(p *CreateDeviceParams) { p.IdentityKey[0] = 8 }, "device_keys_identity_key"},
		"kimlik anahtarı boyu":  {func(p *CreateDeviceParams) { p.IdentityKey = p.IdentityKey[:32] }, "device_keys_identity_key"},
		"kayıt kimliği":         {func(p *CreateDeviceParams) { p.RegistrationID = 16381 }, "device_keys_registration_id"},
		"cihaz numarası":        {func(p *CreateDeviceParams) { p.DeviceNumber = 128 }, "device_keys_device_number"},
		"aynı numara":           {func(p *CreateDeviceParams) { p.DeviceNumber = 1 }, "device_keys_number"},
		"imza boyu":             {func(p *CreateDeviceParams) { p.SignedPrekeySignature = sig(1)[:63] }, "device_keys_signed_prekey_signature"},
		"kyber boyu":            {func(p *CreateDeviceParams) { p.KyberPrekey = p.KyberPrekey[:1185] }, "device_keys_kyber_prekey"},
		"ön anahtar kimliği":    {func(p *CreateDeviceParams) { p.SignedPrekeyID = 1 << 24 }, "device_keys_signed_prekey_id"},
		"kyber ön anahtar kim.": {func(p *CreateDeviceParams) { p.KyberPrekeyID = -1 }, "device_keys_kyber_prekey_id"},
	} {
		t.Run(name, func(t *testing.T) {
			p := base()
			c.change(&p)
			_, err := q.CreateDevice(ctx, p)
			wantConstraint(t, err, c.constraint)
		})
	}
	t.Run("oturum başına bir cihaz", func(t *testing.T) {
		p := base()
		if _, err := q.CreateDevice(ctx, p); err != nil {
			t.Fatal(err)
		}
		q2 := base()
		q2.SessionID, q2.DeviceNumber = p.SessionID, 3
		_, err := q.CreateDevice(ctx, q2)
		wantConstraint(t, err, "device_keys_session")
	})
	t.Run("ec ön anahtarına imza", func(t *testing.T) {
		d := newDevice(t, q, id.New(), 1)
		_, err := q.InsertKyberPreKeys(ctx, InsertKyberPreKeysParams{DeviceID: d.PG(), KeyIds: []int32{1}, PublicKeys: [][]byte{ecKey(1)}, Signatures: [][]byte{sig(1)}})
		wantConstraint(t, err, "device_one_time_prekeys_key")
	})
}

// A direct message never has a body on the server: there is no column for
// one, and encryption is e2e only (issue #151, dm_messages_encryption).
func TestE2EMessageHasNoBody(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	conv := newConversation(t, q, a, b, "active", id.ID{}, time.Now())
	dev := newDevice(t, q, a, 1)
	m := testMessageIDs.next()
	r, err := q.CreateDirectMessageE2E(ctx, CreateDirectMessageE2EParams{
		ID: m.PG(), ConversationID: conv.PG(), SenderID: a.PG(), SenderDeviceID: dev.PG(), ClientRequestID: id.New().PG(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Encryption != "e2e" {
		t.Fatalf("e2e mesajı: %+v", r)
	}
	_, err = pool.Exec(ctx, `UPDATE dm_messages SET encryption = 'server' WHERE id = $1`, m.PG())
	wantConstraint(t, err, "dm_messages_encryption")
	if _, err := pool.Exec(ctx, `UPDATE dm_messages SET body_encrypted = '\x00' WHERE id = $1`, m.PG()); err == nil {
		t.Fatal("body_encrypted kolonu var")
	}
}

func newEnvelope(t *testing.T, q *Queries, message, device, sender id.ID) id.ID {
	t.Helper()
	e := id.New()
	if err := q.InsertEnvelope(context.Background(), InsertEnvelopeParams{
		ID: e.PG(), MessageID: message.PG(), RecipientDeviceID: device.PG(), SenderID: sender.PG(), SenderDeviceID: id.New().PG(),
		SenderDeviceNumber: 1, MessageType: 3, Ciphertext: []byte{1, 2, 3}, CreatedAt: at(time.Now()),
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

// A device acknowledges only its own envelopes (IDOR).
func TestAckEnvelopesOnlyOwnDevice(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	conv := newConversation(t, q, a, b, "active", id.ID{}, time.Now())
	msg := newDirectMessage(t, q, conv, a)
	devA, devB := newDevice(t, q, a, 1), newDevice(t, q, b, 1)
	envB := newEnvelope(t, q, msg, devB, a)

	n, err := q.AckEnvelopes(ctx, AckEnvelopesParams{DeviceID: devA.PG(), Ids: []pgtype.UUID{envB.PG()}})
	if err != nil || n != 0 {
		t.Fatalf("başkasının zarfı onaylandı: %d %v", n, err)
	}
	rows, err := q.ListEnvelopes(ctx, ListEnvelopesParams{DeviceID: devB.PG(), N: 10})
	if err != nil || len(rows) != 1 || rows[0].ConversationID != conv.PG() || !bytes.Equal(rows[0].Ciphertext, []byte{1, 2, 3}) {
		t.Fatalf("zarf listesi: %+v %v", rows, err)
	}
	if rows, _ := q.ListEnvelopes(ctx, ListEnvelopesParams{DeviceID: devA.PG(), N: 10}); len(rows) != 0 {
		t.Fatalf("A, B'nin zarfını gördü")
	}
	n, err = q.AckEnvelopes(ctx, AckEnvelopesParams{DeviceID: devB.PG(), Ids: []pgtype.UUID{envB.PG()}})
	if err != nil || n != 1 {
		t.Fatalf("onay: %d %v", n, err)
	}
	// Removing a device removes what waits for it.
	newEnvelope(t, q, msg, devB, a)
	if _, err := q.DeleteDevice(ctx, DeleteDeviceParams{ID: devB.PG(), UserID: b.PG()}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := q.ListEnvelopes(ctx, ListEnvelopesParams{DeviceID: devB.PG(), N: 10}); len(rows) != 0 {
		t.Fatalf("kaldırılan cihazın zarfı kaldı")
	}
	// Another user cannot remove someone's device by its ID.
	if _, err := q.DeleteDevice(ctx, DeleteDeviceParams{ID: devA.PG(), UserID: b.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("başkasının cihazı silindi: %v", err)
	}
}

// A user writes archive entries only for messages of their own
// conversations, and the first write wins.
func TestArchiveEntriesOnlyForParty(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	a, b, c := id.New(), id.New(), id.New()
	conv := newConversation(t, q, a, b, "active", id.ID{}, time.Now())
	msg := newDirectMessage(t, q, conv, a)
	put := func(user id.ID, data byte) []pgtype.UUID {
		t.Helper()
		if err := q.InsertArchiveEntries(ctx, InsertArchiveEntriesParams{
			UserID: user.PG(), KeyVersion: 1, CreatedAt: at(time.Now()), MessageIds: []pgtype.UUID{msg.PG(), id.New().PG()}, Ciphertexts: [][]byte{{data}, {data}},
		}); err != nil {
			t.Fatal(err)
		}
		ids, err := q.ListArchivedMessageIDs(ctx, ListArchivedMessageIDsParams{UserID: user.PG(), MessageIds: []pgtype.UUID{msg.PG()}})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := put(c, 1); len(got) != 0 {
		t.Fatalf("taraf olmayan arşive yazdı")
	}
	if got := put(a, 1); len(got) != 1 {
		t.Fatalf("taraf yazamadı")
	}
	put(a, 2) // a retry with other bytes changes nothing
	rows, err := q.ListArchiveBefore(ctx, ListArchiveBeforeParams{UserID: a.PG(), ConversationID: conv.PG(), N: 10})
	if err != nil || len(rows) != 1 || !bytes.Equal(rows[0].Ciphertext, []byte{1}) {
		t.Fatalf("arşiv: %+v %v", rows, err)
	}
	if rows, _ := q.ListArchiveBefore(ctx, ListArchiveBeforeParams{UserID: b.PG(), ConversationID: conv.PG(), N: 10}); len(rows) != 0 {
		t.Fatalf("b, a'nın arşivini gördü")
	}
}

func TestLinkRequestAnswerOnce(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	user := id.New()
	dev, paired := newDevice(t, q, user, 1), newDevice(t, q, user, 2)
	now := time.Now()
	r, err := q.CreateLinkRequest(ctx, CreateLinkRequestParams{ID: id.New().PG(), UserID: user.PG(), DeviceID: dev.PG(), ExpiresAt: at(now.Add(10 * time.Minute)), CreatedAt: at(now)})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(user id.ID, when time.Time) int64 {
		t.Helper()
		n, err := q.AnswerLinkRequest(ctx, AnswerLinkRequestParams{
			ID: r.ID, UserID: user.PG(), Answer: []byte{7}, HistoryKeyVersion: pgtype.Int2{Int16: 1, Valid: true},
			AnsweredByDeviceID: paired.PG(), AnsweredAt: at(when),
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if answer(id.New(), now) != 0 {
		t.Fatal("başka kullanıcı cevapladı")
	}
	if answer(user, now.Add(11*time.Minute)) != 0 {
		t.Fatal("süresi geçmiş istek cevaplandı")
	}
	if answer(user, now) != 1 || answer(user, now) != 0 {
		t.Fatal("istek bir kez cevaplanmalı")
	}
}

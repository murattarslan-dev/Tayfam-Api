package channel

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// unlimited lets every message through; tests that are not about the rate.
type unlimited struct{}

func (unlimited) Allow(string) bool { return true }

// activeRules lets everyone write directly.
type activeRules struct{}

func (activeRules) Check(context.Context, id.ID, id.ID) (dm.Verdict, error) {
	return dm.Verdict{Decision: dm.Active}, nil
}

// e2eDevice registers a device for user so that direct messages can be
// sent (they are end to end only, issue #151).
func e2eDevice(t *testing.T, k *keys.Service, user id.ID, seed byte) (keys.Caller, keys.Device) {
	t.Helper()
	ec := func(b byte) []byte { return append([]byte{5}, bytes.Repeat([]byte{b}, 32)...) }
	c := keys.Caller{User: user, Session: id.New(), IssuedAt: time.Now()}
	d, _, err := k.Register(ctx, c, keys.RegisterInput{
		RegistrationID:  uint32(seed),
		IdentityKey:     ec(seed),
		SignedPreKey:    keys.SignedPreKey{ID: 1, PublicKey: ec(seed + 1), Signature: bytes.Repeat([]byte{seed}, 64)},
		KyberLastResort: keys.SignedPreKey{ID: 1, PublicKey: append([]byte{8}, bytes.Repeat([]byte{seed}, 1568)...), Signature: bytes.Repeat([]byte{seed}, 64)},
	})
	must(t, err)
	return c, d
}

// Issue #101, decision 3: one per-user counter for channel and direct
// messages (the limiter cmd/chat wires into both services: 30 a minute,
// bursts of 10). REST (gRPC) and the WebSocket both send through these
// Send functions, so they share it too. Another user is not affected.
func TestSendRateSharedWithDirectMessages(t *testing.T) {
	e := setup(t)
	limit := ratelimit.New(30, 10)
	svc, err := New(Config{
		Pool: e.pool, Log: slog.New(slog.DiscardHandler), Access: e.members, SendLimit: limit, Cipher: channelcrypt.Ephemeral(),
	})
	must(t, err)
	direct, err := dm.New(dm.Config{
		Pool: e.pool, Log: slog.New(slog.DiscardHandler), Rules: activeRules{}, SendLimit: limit,
	})
	must(t, err)
	k, err := keys.New(keys.Config{
		Pool: e.pool, Log: slog.New(slog.DiscardHandler), Contacts: direct, Bundles: unlimited{}, KeyOps: unlimited{}, Archive: unlimited{},
	})
	must(t, err)
	from, _ := e2eDevice(t, k, e.member, 1)
	friend := id.New()
	_, to := e2eDevice(t, k, friend, 2)
	dmInput := func() dm.SendInput {
		return dm.SendInput{
			Recipient: friend, ClientRequestID: id.New(), Session: from.Session,
			Envelopes: []dm.OutgoingEnvelope{{Device: to.ID, RegistrationID: uint32(to.RegistrationID), Type: keys.EnvelopePreKey, Content: []byte{1}}}, //nolint:gosec // test value
		}
	}
	ch := e.channel(t, ChannelInput{Name: "genel"})
	for i := range 10 {
		if i%2 == 0 {
			_, err = svc.Send(ctx, e.member, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
		} else {
			_, _, err = direct.Send(ctx, e.member, dmInput())
		}
		must(t, err)
	}
	_, err = svc.Send(ctx, e.member, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
	is(t, err, apperr.ErrRateLimited)
	_, _, err = direct.Send(ctx, e.member, dmInput())
	is(t, err, apperr.ErrRateLimited)
	// Nothing of the refused ones was written, and core was not asked again.
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channel_messages) + (SELECT count(*) FROM dm_messages)`).Scan(&n))
	if n != 10 {
		t.Fatalf("yazılan = %d", n)
	}
	// Another user has their own counter.
	_, err = svc.Send(ctx, e.other, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
	must(t, err)
}

// The rate is checked before slow mode and before core is asked.
func TestSendRateBeforeSlowModeAndCore(t *testing.T) {
	e := setup(t)
	svc, err := New(Config{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Access: e.members, SendLimit: denyAll{}, Cipher: channelcrypt.Ephemeral()})
	must(t, err)
	ch := e.channel(t, ChannelInput{Name: "genel", SlowModeSeconds: 60})
	e.members.mu.Lock()
	calls := e.members.calls
	e.members.mu.Unlock()
	_, err = svc.Send(ctx, e.member, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
	is(t, err, apperr.ErrRateLimited)
	e.members.mu.Lock()
	defer e.members.mu.Unlock()
	if e.members.calls != calls {
		t.Fatal("hız sınırında core'a soruldu")
	}
}

type denyAll struct{}

func (denyAll) Allow(string) bool { return false }

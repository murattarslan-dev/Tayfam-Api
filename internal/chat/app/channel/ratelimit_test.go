package channel

import (
	"context"
	"log/slog"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
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
		Pool: e.pool, Log: slog.New(slog.DiscardHandler), Rules: activeRules{}, SendLimit: limit, Cipher: channelcrypt.Ephemeral(),
	})
	must(t, err)
	ch := e.channel(t, ChannelInput{Name: "genel"})
	friend := id.New()
	for i := range 10 {
		if i%2 == 0 {
			_, err = svc.Send(ctx, e.member, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
		} else {
			_, _, err = direct.Send(ctx, e.member, dm.SendInput{Recipient: friend, Body: "dm", ClientRequestID: id.New()})
		}
		must(t, err)
	}
	_, err = svc.Send(ctx, e.member, e.tayfa, ch.ID, SendInput{Body: "kanal", ClientRequestID: id.New()})
	is(t, err, apperr.ErrRateLimited)
	_, _, err = direct.Send(ctx, e.member, dm.SendInput{Recipient: friend, Body: "dm", ClientRequestID: id.New()})
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

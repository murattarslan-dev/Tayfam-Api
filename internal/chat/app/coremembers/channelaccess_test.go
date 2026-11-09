package coremembers

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeAccessCore struct {
	calls int
	got   *corev1.AuthorizeChannelActionRequest
	resp  *corev1.AuthorizeChannelActionResponse
	err   error
}

func (f *fakeAccessCore) AuthorizeChannelAction(_ context.Context, in *corev1.AuthorizeChannelActionRequest, _ ...grpc.CallOption) (*corev1.AuthorizeChannelActionResponse, error) {
	f.calls++
	f.got = in
	return f.resp, f.err
}

// moderatorAnswer: everything but restore; posting refused by a mute.
func moderatorAnswer() *corev1.AuthorizeChannelActionResponse {
	r := &corev1.AuthorizeChannelActionResponse{Allowed: true}
	for _, a := range actions {
		d := corev1.ChannelDenial_CHANNEL_DENIAL_UNSPECIFIED
		switch a {
		case corev1.ChannelAction_CHANNEL_ACTION_RESTORE:
			d = corev1.ChannelDenial_CHANNEL_DENIAL_ROLE
		case corev1.ChannelAction_CHANNEL_ACTION_POST:
			d = corev1.ChannelDenial_CHANNEL_DENIAL_MUTED
		}
		r.Decisions = append(r.Decisions, &corev1.ChannelActionDecision{Action: a, Denial: d})
	}
	return r
}

func TestChannelAccessDecisionsAndCache(t *testing.T) {
	core := &fakeAccessCore{resp: moderatorAnswer()}
	c := NewChannelAccess(core, 5*time.Second)
	now := time.Now()
	c.now = func() time.Time { return now }
	tf, u := id.New(), id.New()
	ctx := context.Background()

	d, err := c.Channel(ctx, tf, u, channel.ActionView, id.ID{})
	if err != nil || d.Denial != channel.DenialNone || !d.Allowed(channel.ActionViewStaff) || d.Allowed(channel.ActionRestore) {
		t.Fatalf("karar = %+v, %v", d, err)
	}
	if core.got.GetTayfaId() != tf.String() || core.got.GetAction() != corev1.ChannelAction_CHANNEL_ACTION_VIEW || core.got.GetTargetUserId() != "" {
		t.Fatalf("istek = %v", core.got)
	}
	// Another action within the TTL: from the cache, with its own answer.
	d, err = c.Channel(ctx, tf, u, channel.ActionPost, id.ID{})
	if err != nil || d.Denial != channel.DenialMuted || core.calls != 1 {
		t.Fatalf("önbellek = %+v, %v (%d çağrı)", d, err, core.calls)
	}
	// A target is always asked fresh, and core's answer for it is used.
	target := id.New()
	core.resp = moderatorAnswer()
	core.resp.Allowed, core.resp.Denial = false, corev1.ChannelDenial_CHANNEL_DENIAL_HIERARCHY
	d, err = c.Channel(ctx, tf, u, channel.ActionModerate, target)
	if err != nil || d.Denial != channel.DenialHierarchy || core.calls != 2 || core.got.GetTargetUserId() != target.String() ||
		core.got.GetAction() != corev1.ChannelAction_CHANNEL_ACTION_MODERATE {
		t.Fatalf("hedefli = %+v, %v", d, err)
	}
	// The targeted answer did not replace the cached one.
	if d, _ := c.Channel(ctx, tf, u, channel.ActionModerate, id.ID{}); d.Denial != channel.DenialNone || core.calls != 2 {
		t.Fatalf("önbellek bozuldu: %+v", d)
	}

	// Forget and ForgetTayfa drop answers at once.
	c.Forget(tf, u)
	_, _ = c.Channel(ctx, tf, u, channel.ActionView, id.ID{})
	if core.calls != 3 {
		t.Fatalf("Forget sonrası %d çağrı", core.calls)
	}
	other := id.New()
	_, _ = c.Channel(ctx, tf, other, channel.ActionView, id.ID{})
	c.ForgetTayfa(tf)
	_, _ = c.Channel(ctx, tf, u, channel.ActionView, id.ID{})
	_, _ = c.Channel(ctx, tf, other, channel.ActionView, id.ID{})
	if core.calls != 6 {
		t.Fatalf("ForgetTayfa sonrası %d çağrı", core.calls)
	}
	// After the TTL core is asked again; errors are not cached.
	now = now.Add(5 * time.Second)
	core.err = status.Error(codes.Unavailable, "x")
	if _, err := c.Channel(ctx, tf, u, channel.ActionView, id.ID{}); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("core kapalı: %v", err)
	}
	core.err = nil
	if _, err := c.Channel(ctx, tf, u, channel.ActionView, id.ID{}); err != nil || core.calls != 8 {
		t.Fatalf("yeniden: %v (%d)", err, core.calls)
	}
}

func TestChannelAccessNotMemberAndUnknown(t *testing.T) {
	notFound := corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String()
	core := &fakeAccessCore{err: withReason(codes.NotFound, "core.tayfam.app", notFound)}
	c := NewChannelAccess(core, 5*time.Second)
	tf, u := id.New(), id.New()
	for range 2 {
		if _, err := c.Channel(context.Background(), tf, u, channel.ActionView, id.ID{}); !errors.Is(err, apperr.ErrTayfaNotFound) {
			t.Fatalf("üye değil: %v", err)
		}
	}
	if core.calls != 1 {
		t.Fatalf("üye değil cevabı önbelleğe alınmadı: %d", core.calls)
	}
	// A reason from another domain is an error, not "not a member".
	core.err = withReason(codes.NotFound, "chat.tayfam.app", notFound)
	if _, err := NewChannelAccess(core, time.Second).Channel(context.Background(), tf, u, channel.ActionView, id.ID{}); err == nil || errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("başka alan adı: %v", err)
	}
	// An action or denial chat does not know is refused.
	core.err = nil
	core.resp = &corev1.AuthorizeChannelActionResponse{Decisions: []*corev1.ChannelActionDecision{
		{Action: corev1.ChannelAction_CHANNEL_ACTION_VIEW, Denial: corev1.ChannelDenial(99)},
	}}
	d, err := NewChannelAccess(core, time.Second).Channel(context.Background(), tf, u, channel.ActionView, id.ID{})
	if err != nil || d.Denial == channel.DenialNone || d.Allowed(channel.ActionPost) {
		t.Fatalf("bilinmeyen = %+v %v", d, err)
	}
}

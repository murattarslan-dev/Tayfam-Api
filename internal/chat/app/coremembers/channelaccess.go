package coremembers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// maxCached bounds the cache; when it is full, expired answers are dropped
// and, if that is not enough, the whole cache.
const maxCached = 10000

// ChannelAccessClient is core's ChannelAccessService as chat uses it.
type ChannelAccessClient interface {
	AuthorizeChannelAction(ctx context.Context, in *corev1.AuthorizeChannelActionRequest, opts ...grpc.CallOption) (*corev1.AuthorizeChannelActionResponse, error)
}

var actions = map[channel.Action]corev1.ChannelAction{
	channel.ActionView:         corev1.ChannelAction_CHANNEL_ACTION_VIEW,
	channel.ActionViewStaff:    corev1.ChannelAction_CHANNEL_ACTION_VIEW_STAFF,
	channel.ActionManage:       corev1.ChannelAction_CHANNEL_ACTION_MANAGE,
	channel.ActionPost:         corev1.ChannelAction_CHANNEL_ACTION_POST,
	channel.ActionSkipSlowMode: corev1.ChannelAction_CHANNEL_ACTION_SKIP_SLOW_MODE,
	channel.ActionDeleteOwn:    corev1.ChannelAction_CHANNEL_ACTION_DELETE_OWN,
	channel.ActionModerate:     corev1.ChannelAction_CHANNEL_ACTION_MODERATE,
	channel.ActionRestore:      corev1.ChannelAction_CHANNEL_ACTION_RESTORE,
	channel.ActionMarkRead:     corev1.ChannelAction_CHANNEL_ACTION_MARK_READ,
}

var denials = map[corev1.ChannelDenial]channel.Denial{
	corev1.ChannelDenial_CHANNEL_DENIAL_UNSPECIFIED: channel.DenialNone,
	corev1.ChannelDenial_CHANNEL_DENIAL_ROLE:        channel.DenialRole,
	corev1.ChannelDenial_CHANNEL_DENIAL_MUTED:       channel.DenialMuted,
	corev1.ChannelDenial_CHANNEL_DENIAL_READ_ONLY:   channel.DenialReadOnly,
	corev1.ChannelDenial_CHANNEL_DENIAL_HIERARCHY:   channel.DenialHierarchy,
}

// denial converts core's answer; an unknown value refuses (default "no").
func denial(d corev1.ChannelDenial) channel.Denial {
	if v, ok := denials[d]; ok {
		return v
	}
	return channel.DenialRole
}

type accessEntry struct {
	d       channel.Decision
	member  bool
	expires time.Time
}

// ChannelAccess implements channel.Access with core's
// ChannelAccessService (issue #81): core's single permission table decides,
// with the caller's own token (the connection forwards it,
// auth.ForwardToken).
//
// Answers without a target (core's decisions for every action) are kept
// for ttl per (tayfa, user), "not a member" too; errors are not kept.
// core's membership.changed and tayfa state events drop them earlier
// (Forget, ForgetTayfa). Answers about a target (the role hierarchy for
// hiding and restoring) are always asked fresh.
type ChannelAccess struct {
	core ChannelAccessClient
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	cache map[[2]id.ID]accessEntry
}

// NewChannelAccess returns a ChannelAccess that keeps answers for ttl.
func NewChannelAccess(core ChannelAccessClient, ttl time.Duration) *ChannelAccess {
	return &ChannelAccess{core: core, ttl: ttl, now: time.Now, cache: map[[2]id.ID]accessEntry{}}
}

// Channel implements channel.Access.
func (c *ChannelAccess) Channel(ctx context.Context, tayfa, user id.ID, action channel.Action, target id.ID) (channel.Decision, error) {
	pa, ok := actions[action]
	if !ok {
		return channel.Decision{}, fmt.Errorf("bilinmeyen kanal işlemi %d", action)
	}
	if target != (id.ID{}) {
		// The role hierarchy depends on the target: always asked fresh.
		e, err := c.ask(ctx, tayfa, pa, target)
		if err != nil {
			return channel.Decision{}, err
		}
		if !e.member {
			return channel.Decision{}, apperr.ErrTayfaNotFound
		}
		return e.d, nil
	}
	key := [2]id.ID{tayfa, user}
	c.mu.Lock()
	e, ok := c.cache[key]
	c.mu.Unlock()
	if !ok || !c.now().Before(e.expires) {
		var err error
		if e, err = c.ask(ctx, tayfa, pa, id.ID{}); err != nil {
			return channel.Decision{}, err
		}
		c.store(key, e)
	}
	if !e.member {
		return channel.Decision{}, apperr.ErrTayfaNotFound
	}
	d := channel.Decision{Denial: channel.DenialRole, All: e.d.All}
	if v, ok := e.d.All[action]; ok {
		d.Denial = v
	}
	return d, nil
}

func (c *ChannelAccess) ask(ctx context.Context, tayfa id.ID, action corev1.ChannelAction, target id.ID) (accessEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := &corev1.AuthorizeChannelActionRequest{TayfaId: tayfa.String(), Action: action}
	if target != (id.ID{}) {
		req.TargetUserId = target.String()
	}
	resp, err := c.core.AuthorizeChannelAction(ctx, req)
	if err != nil {
		st := status.Convert(err)
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded:
			return accessEntry{}, apperr.ErrUnavailable
		case codes.NotFound:
			for _, d := range st.Details() {
				if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == "core.tayfam.app" &&
					info.GetReason() == corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
					return accessEntry{}, nil // not a member: cached as such
				}
			}
		}
		return accessEntry{}, fmt.Errorf("core kanal izni: %s", st.Code())
	}
	d := channel.Decision{Denial: denial(resp.GetDenial()), All: make(map[channel.Action]channel.Denial, len(actions))}
	for _, x := range resp.GetDecisions() {
		for a, pa := range actions {
			if pa == x.GetAction() {
				d.All[a] = denial(x.GetDenial())
			}
		}
	}
	return accessEntry{d: d, member: true}, nil
}

func (c *ChannelAccess) store(key [2]id.ID, e accessEntry) {
	now := c.now()
	e.expires = now.Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCached {
		for k, v := range c.cache {
			if !now.Before(v.expires) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= maxCached {
			clear(c.cache)
		}
	}
	c.cache[key] = e
}

// Forget drops the answer kept for user in tayfa (membership.changed).
func (c *ChannelAccess) Forget(tayfa, user id.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, [2]id.ID{tayfa, user})
}

// ForgetTayfa drops every answer kept for tayfa (its state changed).
func (c *ChannelAccess) ForgetTayfa(tayfa id.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if k[0] == tayfa {
			delete(c.cache, k)
		}
	}
}

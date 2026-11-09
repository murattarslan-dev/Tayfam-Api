// Package coremembers asks core about a user's membership in a tayfa
// (ADR-0009 "Senkron sorgular": chat → core GetMembership). The call
// carries the user's own access token (auth.ForwardToken on the
// connection), so core checks it again and answers only about the caller.
// chat keeps no copy of memberships.
//
// Answers may be kept for a short time (ADR-0005 "kısa süre önbellekte
// tutulur"): a Checker made with NewCached remembers each (tayfa, user)
// answer for its TTL. core's membership.changed event does not reach chat
// yet, so a role change, a mute or a removal takes effect in chat at most
// one TTL later. Errors are never cached.
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

// callTimeout bounds one call to core.
const callTimeout = 5 * time.Second

// maxCached bounds the cache; when it is full, expired answers are dropped
// and, if that is not enough, the whole cache.
const maxCached = 10000

// Client is the part of core's MembershipService chat uses.
type Client interface {
	GetMembership(ctx context.Context, in *corev1.GetMembershipRequest, opts ...grpc.CallOption) (*corev1.GetMembershipResponse, error)
}

type cached struct {
	m       channel.Member
	expires time.Time
}

// Checker implements notification.Memberships and channel.Memberships with
// core.
type Checker struct {
	core Client
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	cache map[[2]id.ID]cached
}

// New returns a Checker without a cache. core's connection must forward the
// caller's token (auth.ForwardToken).
func New(core Client) *Checker { return &Checker{core: core, now: time.Now} }

// NewCached returns a Checker that keeps each answer for ttl.
func NewCached(core Client, ttl time.Duration) *Checker {
	return &Checker{core: core, ttl: ttl, now: time.Now, cache: map[[2]id.ID]cached{}}
}

// IsActiveMember reports whether user, the caller of the current request,
// is an active member of tayfa. A tayfa core does not show the caller (none,
// deleted, not a member) is false. Core being unreachable is
// apperr.ErrUnavailable; anything else core refuses is an error.
func (c *Checker) IsActiveMember(ctx context.Context, tayfa, user id.ID) (bool, error) {
	m, err := c.Member(ctx, tayfa, user)
	return m.Active, err
}

// Member returns user's membership in tayfa as core sees it. user must be
// the caller of the current request (core refuses anything else). A tayfa
// core does not show the caller is the zero Member (not active).
func (c *Checker) Member(ctx context.Context, tayfa, user id.ID) (channel.Member, error) {
	key := [2]id.ID{tayfa, user}
	if c.cache != nil {
		c.mu.Lock()
		e, ok := c.cache[key]
		c.mu.Unlock()
		if ok && c.now().Before(e.expires) {
			return e.m, nil
		}
	}
	m, err := c.ask(ctx, tayfa, user)
	if err != nil {
		return channel.Member{}, err
	}
	if c.cache != nil {
		now := c.now()
		c.mu.Lock()
		if len(c.cache) >= maxCached {
			for k, e := range c.cache {
				if !now.Before(e.expires) {
					delete(c.cache, k)
				}
			}
			if len(c.cache) >= maxCached {
				clear(c.cache)
			}
		}
		c.cache[key] = cached{m: m, expires: now.Add(c.ttl)}
		c.mu.Unlock()
	}
	return m, nil
}

func (c *Checker) ask(ctx context.Context, tayfa, user id.ID) (channel.Member, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.core.GetMembership(ctx, &corev1.GetMembershipRequest{TayfaId: tayfa.String(), UserId: user.String()})
	if err != nil {
		st := status.Convert(err)
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded:
			return channel.Member{}, apperr.ErrUnavailable
		case codes.NotFound:
			for _, d := range st.Details() {
				if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == "core.tayfam.app" &&
					info.GetReason() == corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String() {
					return channel.Member{}, nil
				}
			}
		}
		return channel.Member{}, fmt.Errorf("core üyelik: %s", st.Code())
	}
	ms := resp.GetMembership()
	if ms.GetStatus() != corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE {
		return channel.Member{}, nil
	}
	m := channel.Member{Active: true}
	switch ms.GetRole() {
	case corev1.Role_ROLE_ADMIN:
		m.Role = channel.RoleAdmin
	case corev1.Role_ROLE_MODERATOR:
		m.Role = channel.RoleModerator
	case corev1.Role_ROLE_MEMBER:
		m.Role = channel.RoleMember
	}
	// An unknown role stays zero: the member is active, but the channel
	// permission table refuses every action for it (default "no").
	if ms.GetMuteExpireTime() != nil {
		m.MutedUntil = ms.GetMuteExpireTime().AsTime().UTC()
	}
	return m, nil
}

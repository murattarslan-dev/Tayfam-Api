// Package channelaccess answers chat's permission questions about tayfa
// channels (ChannelAccessService, proto/tayfam/core/v1/channel_access.proto;
// issue #81). Channels and their messages live in chat; the rules live in
// core's single permission table (authz, CLAUDE.md rule 5), so chat asks
// here with the caller's own token and keeps no rules of its own.
//
// The caller comes from the verified token. access.Load refuses anyone who
// is not an active member of a live tayfa (apperr.ErrTayfaNotFound, the
// same as an unknown tayfa). Then every channel action is decided:
//
//   - authz.Allowed with the caller's role and mute (access.Authorize: a
//     refusal only because of the mute is "muted");
//   - a suspended, archived or deletion-pending tayfa (access.ReadOnly)
//     refuses channel changes; reads and read markers stay open;
//   - for MODERATE and RESTORE with a target, the role hierarchy against
//     the author's current role (authz.CanActOnContent).
package channelaccess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Denial says why an action is refused; DenialNone means allowed.
type Denial int

// Denials (channel_access.proto ChannelDenial).
const (
	DenialNone Denial = iota
	DenialRole
	DenialMuted
	DenialReadOnly
	DenialHierarchy
)

// Actions are the channel actions in the order of the contract.
var Actions = []authz.Action{
	authz.ChannelView, authz.ChannelViewStaff, authz.ChannelManage, authz.ChannelPost, authz.ChannelSkipSlowMode,
	authz.ChannelDeleteOwn, authz.ChannelModerate, authz.ChannelRestore, authz.ChannelMarkRead,
}

// writes are the channel actions a read-only tayfa refuses.
var writes = map[authz.Action]bool{
	authz.ChannelManage:    true,
	authz.ChannelPost:      true,
	authz.ChannelDeleteOwn: true,
	authz.ChannelModerate:  true,
	authz.ChannelRestore:   true,
}

// targeted are the actions that take a target (the message's author).
var targeted = map[authz.Action]bool{authz.ChannelModerate: true, authz.ChannelRestore: true}

// Decision is the answer for the caller.
type Decision struct {
	// Denial is the answer for the asked action (and target).
	Denial Denial
	// All is the answer for every action in Actions, without a target.
	All        map[authz.Action]Denial
	Role       authz.Role
	MutedUntil time.Time // zero: not muted
	ReadOnly   bool
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time // optional, for tests
}

// Service implements ChannelAccessService.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("channelaccess: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// IsChannelAction reports whether a is one of Actions.
func IsChannelAction(a authz.Action) bool {
	for _, c := range Actions {
		if c == a {
			return true
		}
	}
	return false
}

// Authorize decides action for caller in tayfa. target is the zero ID or,
// for ChannelModerate and ChannelRestore only, the message's author.
func (s *Service) Authorize(ctx context.Context, caller, tayfa id.ID, action authz.Action, target id.ID) (Decision, error) {
	if !IsChannelAction(action) {
		return Decision{}, apperr.Validation("action")
	}
	if target != (id.ID{}) && !targeted[action] {
		return Decision{}, apperr.Validation("target_user_id")
	}
	t, m, err := access.Load(ctx, s.q, tayfa, caller, false)
	if err != nil {
		return Decision{}, err
	}
	now := s.cfg.Now().UTC()
	d := Decision{All: make(map[authz.Action]Denial, len(Actions)), Role: m.Role, ReadOnly: access.ReadOnly(t)}
	if m.Muted(now) {
		d.MutedUntil = m.MutedUntil.UTC()
	}
	for _, a := range Actions {
		d.All[a] = decide(m, a, now, d.ReadOnly)
	}
	d.Denial = d.All[action]
	if d.Denial == DenialNone && target != (id.ID{}) && target != caller {
		author, err := s.currentRole(ctx, tayfa, target)
		if err != nil {
			return Decision{}, err
		}
		if !authz.CanActOnContent(m.Role, author) {
			d.Denial = DenialHierarchy
		}
	}
	return d, nil
}

func decide(m authz.Membership, a authz.Action, now time.Time, readOnly bool) Denial {
	switch err := access.Authorize(m, a, now); {
	case errors.Is(err, apperr.ErrMuted):
		return DenialMuted
	case err != nil:
		return DenialRole
	}
	if readOnly && writes[a] {
		return DenialReadOnly
	}
	return DenialNone
}

// currentRole is the target's role if they are an active member, else 0
// (content of a former member belongs to the tayfa, CanActOnContent).
func (s *Service) currentRole(ctx context.Context, tayfa, user id.ID) (authz.Role, error) {
	r, err := s.q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("yazarın üyeliği: %w", err)
	}
	m, err := access.Membership(r.Role, r.Status, r.MutedUntil)
	if err != nil {
		return 0, err
	}
	if m.Status != authz.StatusActive {
		return 0, nil
	}
	return m.Role, nil
}

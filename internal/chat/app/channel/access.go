package channel

import (
	"context"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Action is a channel action whose permission core decides
// (ChannelAccessService.AuthorizeChannelAction; core's single permission
// table, CLAUDE.md rule 5). chat holds no rule of its own.
type Action int

// Actions, as in proto/tayfam/core/v1/channel_access.proto.
const (
	ActionView Action = iota + 1
	ActionViewStaff
	ActionManage
	ActionPost
	ActionSkipSlowMode
	ActionDeleteOwn
	ActionModerate
	ActionRestore
	ActionMarkRead
)

// Denial is core's reason for refusing an action; DenialNone means allowed.
type Denial int

// Denials, as in channel_access.proto ChannelDenial.
const (
	DenialNone Denial = iota
	DenialRole
	DenialMuted
	DenialReadOnly
	DenialHierarchy
)

// Decision is core's answer for the caller in a tayfa.
type Decision struct {
	// Denial is the answer for the asked action (and target).
	Denial Denial
	// All is the answer for every action without a target. An action
	// missing here is refused.
	All map[Action]Denial
	// caller is the user the answer is about (set by Service.access).
	caller id.ID
}

// Allowed reports whether a (without a target) is allowed.
func (d Decision) Allowed(a Action) bool {
	v, ok := d.All[a]
	return ok && v == DenialNone
}

// Access asks core about the caller's channel permissions. user must be
// the caller of the current request: the call carries their own token. A
// caller who is not an active member of the tayfa gets
// apperr.ErrTayfaNotFound. target is the zero ID or, with ActionModerate
// and ActionRestore, the message's author for the role hierarchy.
type Access interface {
	Channel(ctx context.Context, tayfa, user id.ID, action Action, target id.ID) (Decision, error)
}

// denialErr turns core's denial into the error the caller sees.
func denialErr(d Denial) error {
	switch d {
	case DenialNone:
		return nil
	case DenialMuted:
		return apperr.ErrMuted
	case DenialReadOnly:
		return apperr.ErrTayfaReadOnly
	case DenialHierarchy:
		return apperr.ErrRoleHierarchy
	}
	return apperr.ErrPermissionDenied
}

// authorize returns nil if a is allowed by d, else why not.
func authorize(d Decision, a Action) error {
	v, ok := d.All[a]
	if !ok {
		return apperr.ErrPermissionDenied
	}
	return denialErr(v)
}

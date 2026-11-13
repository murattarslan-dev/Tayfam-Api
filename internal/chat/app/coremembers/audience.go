package coremembers

import (
	"context"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AudienceClient is the part of core's ChannelAccessService that answers
// about event and selected-member channels (issue #183).
type AudienceClient interface {
	GetEventChannelAccess(ctx context.Context, in *corev1.GetEventChannelAccessRequest, opts ...grpc.CallOption) (*corev1.GetEventChannelAccessResponse, error)
	ListEventChannelMembers(ctx context.Context, in *corev1.ListEventChannelMembersRequest, opts ...grpc.CallOption) (*corev1.ListEventChannelMembersResponse, error)
	FilterActiveMembers(ctx context.Context, in *corev1.FilterActiveMembersRequest, opts ...grpc.CallOption) (*corev1.FilterActiveMembersResponse, error)
}

// Audience implements channel.Audience with core, always with the caller's
// own token (the connection forwards it, auth.ForwardToken); core answers
// about the token's user, so the caller argument is not sent. Answers are
// not kept: who goes to an event changes, and core is the only source.
type Audience struct{ core AudienceClient }

// NewAudience returns an Audience.
func NewAudience(core AudienceClient) *Audience { return &Audience{core: core} }

// coreErr maps core's refusals: not an active member is
// apperr.ErrTayfaNotFound, an unknown event apperr.ErrEventNotFound, core
// unreachable apperr.ErrUnavailable.
func coreErr(err error) error {
	st := status.Convert(err)
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return apperr.ErrUnavailable
	case codes.NotFound:
		for _, d := range st.Details() {
			info, ok := d.(*errdetails.ErrorInfo)
			if !ok || info.GetDomain() != "core.tayfam.app" {
				continue
			}
			switch info.GetReason() {
			case corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String():
				return apperr.ErrTayfaNotFound
			case corev1.ErrorReason_ERROR_REASON_EVENT_NOT_FOUND.String():
				return apperr.ErrEventNotFound
			}
		}
	}
	return fmt.Errorf("core kanal kitlesi: %s", st.Code())
}

func optional(u id.ID) string {
	if u == (id.ID{}) {
		return ""
	}
	return u.String()
}

// EventChannels implements channel.Audience.
func (a *Audience) EventChannels(ctx context.Context, _, tayfa id.ID, refs []channel.EventRef) (map[channel.EventRef]channel.EventAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := &corev1.GetEventChannelAccessRequest{TayfaId: tayfa.String(), Channels: make([]*corev1.EventChannelRef, 0, len(refs))}
	for _, r := range refs {
		req.Channels = append(req.Channels, &corev1.EventChannelRef{EventId: r.Event.String(), OpenerUserId: optional(r.Opener)})
	}
	resp, err := a.core.GetEventChannelAccess(ctx, req)
	if err != nil {
		return nil, coreErr(err)
	}
	asked := make(map[channel.EventRef]bool, len(refs))
	for _, r := range refs {
		asked[r] = true
	}
	out := make(map[channel.EventRef]channel.EventAccess, len(resp.GetChannels()))
	for _, c := range resp.GetChannels() {
		ref := channel.EventRef{}
		var ok bool
		if ref.Event, ok = id.Parse(c.GetEventId()); !ok {
			return nil, fmt.Errorf("core kanal kitlesi: bozuk etkinlik kimliği")
		}
		if o := c.GetOpenerUserId(); o != "" {
			if ref.Opener, ok = id.Parse(o); !ok {
				return nil, fmt.Errorf("core kanal kitlesi: bozuk kullanıcı kimliği")
			}
		}
		// Only what was asked: an answer about another channel is dropped.
		if !asked[ref] {
			continue
		}
		out[ref] = channel.EventAccess{
			Member: c.GetMember(), CanOpen: c.GetCanOpenChannel(), End: c.GetEndTime().AsTime().UTC(),
			Cancelled: c.GetCancelled(), MemberCount: c.GetMemberCount(),
		}
	}
	return out, nil
}

// EventMembers implements channel.Audience.
func (a *Audience) EventMembers(ctx context.Context, _, tayfa id.ID, ref channel.EventRef, size int32, token string) ([]id.ID, string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := a.core.ListEventChannelMembers(ctx, &corev1.ListEventChannelMembersRequest{
		TayfaId: tayfa.String(), EventId: ref.Event.String(), OpenerUserId: optional(ref.Opener), PageSize: size, PageToken: token,
	})
	if err != nil {
		return nil, "", coreErr(err)
	}
	out := make([]id.ID, 0, len(resp.GetUserIds()))
	for _, s := range resp.GetUserIds() {
		u, ok := id.Parse(s)
		if !ok {
			return nil, "", fmt.Errorf("core kanal kitlesi: bozuk kullanıcı kimliği")
		}
		out = append(out, u)
	}
	return out, resp.GetNextPageToken(), nil
}

// ActiveMembers implements channel.Audience.
func (a *Audience) ActiveMembers(ctx context.Context, _, tayfa id.ID, users []id.ID) ([]id.ID, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := &corev1.FilterActiveMembersRequest{TayfaId: tayfa.String(), UserIds: make([]string, 0, len(users))}
	for _, u := range users {
		req.UserIds = append(req.UserIds, u.String())
	}
	resp, err := a.core.FilterActiveMembers(ctx, req)
	if err != nil {
		return nil, coreErr(err)
	}
	out := make([]id.ID, 0, len(resp.GetUserIds()))
	for _, s := range resp.GetUserIds() {
		u, ok := id.Parse(s)
		if !ok {
			return nil, fmt.Errorf("core kanal kitlesi: bozuk kullanıcı kimliği")
		}
		out = append(out, u)
	}
	return out, nil
}

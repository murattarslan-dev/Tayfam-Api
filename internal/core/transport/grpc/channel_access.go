package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/channelaccess"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ChannelAccess is what ChannelAccessServer needs from the app layer.
type ChannelAccess interface {
	Authorize(ctx context.Context, caller, tayfa id.ID, action authz.Action, target id.ID) (channelaccess.Decision, error)
	EventChannels(ctx context.Context, caller, tayfa id.ID, chans []channelaccess.EventChannel) ([]channelaccess.EventAccess, error)
	EventAudience(ctx context.Context, caller, tayfa id.ID, ch channelaccess.EventChannel, size int32, token string) ([]id.ID, string, error)
	ActiveMembers(ctx context.Context, caller, tayfa id.ID, users []id.ID) ([]id.ID, error)
}

// ChannelAccessServer implements corev1.ChannelAccessServiceServer for chat
// (issue #81). The caller always comes from the verified token; chat
// forwards the user's own token.
type ChannelAccessServer struct {
	corev1.UnimplementedChannelAccessServiceServer
	app ChannelAccess
	log *slog.Logger
}

// NewChannelAccessServer returns a ChannelAccessServer.
func NewChannelAccessServer(app ChannelAccess, log *slog.Logger) *ChannelAccessServer {
	return &ChannelAccessServer{app: app, log: log}
}

var channelActions = map[corev1.ChannelAction]authz.Action{
	corev1.ChannelAction_CHANNEL_ACTION_VIEW:           authz.ChannelView,
	corev1.ChannelAction_CHANNEL_ACTION_VIEW_STAFF:     authz.ChannelViewStaff,
	corev1.ChannelAction_CHANNEL_ACTION_MANAGE:         authz.ChannelManage,
	corev1.ChannelAction_CHANNEL_ACTION_POST:           authz.ChannelPost,
	corev1.ChannelAction_CHANNEL_ACTION_SKIP_SLOW_MODE: authz.ChannelSkipSlowMode,
	corev1.ChannelAction_CHANNEL_ACTION_DELETE_OWN:     authz.ChannelDeleteOwn,
	corev1.ChannelAction_CHANNEL_ACTION_MODERATE:       authz.ChannelModerate,
	corev1.ChannelAction_CHANNEL_ACTION_RESTORE:        authz.ChannelRestore,
	corev1.ChannelAction_CHANNEL_ACTION_MARK_READ:      authz.ChannelMarkRead,
}

var channelDenials = map[channelaccess.Denial]corev1.ChannelDenial{
	channelaccess.DenialNone:      corev1.ChannelDenial_CHANNEL_DENIAL_UNSPECIFIED,
	channelaccess.DenialRole:      corev1.ChannelDenial_CHANNEL_DENIAL_ROLE,
	channelaccess.DenialMuted:     corev1.ChannelDenial_CHANNEL_DENIAL_MUTED,
	channelaccess.DenialReadOnly:  corev1.ChannelDenial_CHANNEL_DENIAL_READ_ONLY,
	channelaccess.DenialHierarchy: corev1.ChannelDenial_CHANNEL_DENIAL_HIERARCHY,
}

func channelActionToProto(a authz.Action) corev1.ChannelAction {
	for k, v := range channelActions {
		if v == a {
			return k
		}
	}
	return corev1.ChannelAction_CHANNEL_ACTION_UNSPECIFIED
}

// AuthorizeChannelAction implements corev1.ChannelAccessServiceServer.
func (s *ChannelAccessServer) AuthorizeChannelAction(ctx context.Context, req *corev1.AuthorizeChannelActionRequest) (*corev1.AuthorizeChannelActionResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	action, ok := channelActions[req.GetAction()]
	if !ok {
		return nil, validation("action")
	}
	var target id.ID
	if req.GetTargetUserId() != "" {
		if target, err = parseID(req.GetTargetUserId(), "target_user_id"); err != nil {
			return nil, err
		}
	}
	d, err := s.app.Authorize(ctx, u, tayfa, action, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.AuthorizeChannelActionResponse{
		Allowed: d.Denial == channelaccess.DenialNone, Denial: channelDenials[d.Denial],
		Role: roleToProto(d.Role), TayfaReadOnly: d.ReadOnly,
		Decisions: make([]*corev1.ChannelActionDecision, 0, len(channelaccess.Actions)),
	}
	for _, a := range channelaccess.Actions {
		out.Decisions = append(out.Decisions, &corev1.ChannelActionDecision{Action: channelActionToProto(a), Denial: channelDenials[d.All[a]]})
	}
	if !d.MutedUntil.IsZero() {
		out.MuteExpireTime = timestamppb.New(d.MutedUntil)
	}
	return out, nil
}

// eventChannelRef parses one event channel; the opener may be empty.
func eventChannelRef(event, opener string) (channelaccess.EventChannel, error) {
	var c channelaccess.EventChannel
	var err error
	if c.Event, err = parseID(event, "event_id"); err != nil {
		return c, err
	}
	if opener != "" {
		if c.Opener, err = parseID(opener, "opener_user_id"); err != nil {
			return c, err
		}
	}
	return c, nil
}

// GetEventChannelAccess implements corev1.ChannelAccessServiceServer
// (issue #183).
func (s *ChannelAccessServer) GetEventChannelAccess(ctx context.Context, req *corev1.GetEventChannelAccessRequest) (*corev1.GetEventChannelAccessResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	if n := len(req.GetChannels()); n == 0 || n > channelaccess.MaxBatch {
		return nil, validation("channels")
	}
	chans := make([]channelaccess.EventChannel, 0, len(req.GetChannels()))
	for _, c := range req.GetChannels() {
		ref, err := eventChannelRef(c.GetEventId(), c.GetOpenerUserId())
		if err != nil {
			return nil, err
		}
		chans = append(chans, ref)
	}
	got, err := s.app.EventChannels(ctx, u, tayfa, chans)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetEventChannelAccessResponse{Channels: make([]*corev1.EventChannelAccess, 0, len(got))}
	for _, a := range got {
		out.Channels = append(out.Channels, &corev1.EventChannelAccess{
			EventId: a.Event.String(), OpenerUserId: optionalID(a.Opener), Member: a.Member, CanOpenChannel: a.CanOpen,
			EndTime: timestamppb.New(a.End), Cancelled: a.Cancelled, MemberCount: a.MemberCount,
		})
	}
	return out, nil
}

// ListEventChannelMembers implements corev1.ChannelAccessServiceServer.
func (s *ChannelAccessServer) ListEventChannelMembers(ctx context.Context, req *corev1.ListEventChannelMembersRequest) (*corev1.ListEventChannelMembersResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	ref, err := eventChannelRef(req.GetEventId(), req.GetOpenerUserId())
	if err != nil {
		return nil, err
	}
	users, next, err := s.app.EventAudience(ctx, u, tayfa, ref, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListEventChannelMembersResponse{UserIds: make([]string, 0, len(users)), NextPageToken: next}
	for _, x := range users {
		out.UserIds = append(out.UserIds, x.String())
	}
	return out, nil
}

// FilterActiveMembers implements corev1.ChannelAccessServiceServer.
func (s *ChannelAccessServer) FilterActiveMembers(ctx context.Context, req *corev1.FilterActiveMembersRequest) (*corev1.FilterActiveMembersResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	tayfa, err := parseID(req.GetTayfaId(), "tayfa_id")
	if err != nil {
		return nil, err
	}
	if len(req.GetUserIds()) > channelaccess.MaxBatch {
		return nil, validation("user_ids")
	}
	users := make([]id.ID, 0, len(req.GetUserIds()))
	for _, x := range req.GetUserIds() {
		v, err := parseID(x, "user_ids")
		if err != nil {
			return nil, err
		}
		users = append(users, v)
	}
	got, err := s.app.ActiveMembers(ctx, u, tayfa, users)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.FilterActiveMembersResponse{UserIds: make([]string, 0, len(got))}
	for _, x := range got {
		out.UserIds = append(out.UserIds, x.String())
	}
	return out, nil
}

package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Channels is what ChannelServer needs from the app layer.
type Channels interface {
	List(ctx context.Context, caller, tayfa id.ID) ([]channel.Channel, error)
	Get(ctx context.Context, caller, tayfa, ch id.ID) (channel.Channel, error)
	Create(ctx context.Context, caller, tayfa id.ID, in channel.ChannelInput) (channel.Channel, error)
	Update(ctx context.Context, caller, tayfa, ch id.ID, in channel.ChannelUpdate) (channel.Channel, error)
	SetArchived(ctx context.Context, caller, tayfa, ch id.ID, archived bool) (channel.Channel, error)
	Send(ctx context.Context, caller, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error)
	Messages(ctx context.Context, caller, tayfa, ch id.ID, in channel.ListInput) ([]channel.Message, string, error)
	Delete(ctx context.Context, caller, tayfa, ch, message id.ID) error
	Restore(ctx context.Context, caller, tayfa, ch, message id.ID) (channel.Message, error)
	MarkRead(ctx context.Context, caller, tayfa, ch, message id.ID) error
	Members(ctx context.Context, caller, tayfa, ch id.ID, size int32, token string) (channel.MemberPage, error)
	AddMembers(ctx context.Context, caller, tayfa, ch id.ID, users []id.ID) (channel.Channel, error)
	RemoveMember(ctx context.Context, caller, tayfa, ch, user id.ID) error
}

// ChannelServer implements chatv1.ChannelServiceServer. Every method acts
// for the caller of the verified access token only. It never logs request
// fields: a message body must not reach any log.
type ChannelServer struct {
	chatv1.UnimplementedChannelServiceServer
	app Channels
	log *slog.Logger
}

// NewChannelServer returns a ChannelServer.
func NewChannelServer(app Channels, log *slog.Logger) *ChannelServer {
	return &ChannelServer{app: app, log: log}
}

var visibilities = map[chatv1.ChannelVisibility]string{
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_MEMBERS: channel.VisibilityMembers,
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF:   channel.VisibilityStaff,
	// Issue #183.
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_EVENT:    channel.VisibilityEvent,
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_SELECTED: channel.VisibilitySelected,
}

// ids parses the caller and the request's UUID fields, in order; names are
// the proto field names for validation errors.
func ids(ctx context.Context, fields ...[2]string) (id.ID, []id.ID, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return id.ID{}, nil, err
	}
	out := make([]id.ID, 0, len(fields))
	for _, f := range fields {
		v, ok := id.Parse(f[1])
		if !ok {
			return id.ID{}, nil, validation(f[0])
		}
		out = append(out, v)
	}
	return caller, out, nil
}

func f(name, value string) [2]string { return [2]string{name, value} }

func channelToProto(c channel.Channel) *chatv1.Channel {
	out := &chatv1.Channel{
		Id: c.ID.String(), TayfaId: c.TayfaID.String(), Name: c.Name, Visibility: toProto(visibilities, c.Visibility),
		SlowModeSeconds: c.SlowModeSeconds, CreatorUserId: idOrEmpty(c.CreatedBy),
		CreateTime: timestamppb.New(c.CreatedAt), UpdateTime: timestamppb.New(c.UpdatedAt),
		UnreadCount: c.Unread, LastReadMessageId: idOrEmpty(c.LastRead),
	}
	if !c.ArchivedAt.IsZero() {
		out.ArchiveTime = timestamppb.New(c.ArchivedAt)
	}
	if c.EventID != (id.ID{}) {
		out.EventId = c.EventID.String()
	}
	if !c.PlannedArchiveAt.IsZero() {
		out.PlannedArchiveTime = timestamppb.New(c.PlannedArchiveAt)
	}
	if c.Visibility == channel.VisibilityEvent || c.Visibility == channel.VisibilitySelected {
		out.MemberCount = c.MemberCount
	}
	return out
}

// userIDs parses a list of user IDs (field is the proto field name).
func userIDs(in []string, field string) ([]id.ID, error) {
	if len(in) > channel.MaxMembersBatch {
		return nil, validation(field)
	}
	out := make([]id.ID, 0, len(in))
	for _, x := range in {
		u, ok := id.Parse(x)
		if !ok {
			return nil, validation(field)
		}
		out = append(out, u)
	}
	return out, nil
}

func messageToProto(m channel.Message) *chatv1.Message {
	return &chatv1.Message{
		Id: m.ID.String(), ChannelId: m.ChannelID.String(), AuthorUserId: idOrEmpty(m.AuthorID),
		Body: m.Body, Deleted: m.Deleted, CreateTime: timestamppb.New(m.CreatedAt), Restorable: m.Restorable,
	}
}

// ListChannels implements chatv1.ChannelServiceServer.
func (s *ChannelServer) ListChannels(ctx context.Context, req *chatv1.ListChannelsRequest) (*chatv1.ListChannelsResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()))
	if err != nil {
		return nil, err
	}
	list, err := s.app.List(ctx, caller, v[0])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListChannelsResponse{Channels: make([]*chatv1.Channel, 0, len(list))}
	for _, c := range list {
		out.Channels = append(out.Channels, channelToProto(c))
	}
	return out, nil
}

// GetChannel implements chatv1.ChannelServiceServer.
func (s *ChannelServer) GetChannel(ctx context.Context, req *chatv1.GetChannelRequest) (*chatv1.GetChannelResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	c, err := s.app.Get(ctx, caller, v[0], v[1])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetChannelResponse{Channel: channelToProto(c)}, nil
}

// CreateChannel implements chatv1.ChannelServiceServer.
func (s *ChannelServer) CreateChannel(ctx context.Context, req *chatv1.CreateChannelRequest) (*chatv1.CreateChannelResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()))
	if err != nil {
		return nil, err
	}
	vis := channel.VisibilityMembers
	if req.GetVisibility() != chatv1.ChannelVisibility_CHANNEL_VISIBILITY_UNSPECIFIED {
		var ok bool
		if vis, ok = visibilities[req.GetVisibility()]; !ok {
			return nil, validation("visibility")
		}
	}
	in := channel.ChannelInput{Name: req.GetName(), Visibility: vis, SlowModeSeconds: req.GetSlowModeSeconds()}
	if e := req.GetEventId(); e != "" {
		var ok bool
		if in.Event, ok = id.Parse(e); !ok {
			return nil, validation("event_id")
		}
	}
	if in.Members, err = userIDs(req.GetMemberUserIds(), "member_user_ids"); err != nil {
		return nil, err
	}
	c, err := s.app.Create(ctx, caller, v[0], in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.CreateChannelResponse{Channel: channelToProto(c)}, nil
}

// UpdateChannel implements chatv1.ChannelServiceServer.
func (s *ChannelServer) UpdateChannel(ctx context.Context, req *chatv1.UpdateChannelRequest) (*chatv1.UpdateChannelResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		return nil, validation("update_mask")
	}
	var in channel.ChannelUpdate
	for _, p := range paths {
		switch p {
		case "name":
			name := req.GetName()
			in.Name = &name
		case "visibility":
			vis, ok := visibilities[req.GetVisibility()]
			if !ok {
				return nil, validation("visibility")
			}
			in.Visibility = &vis
		case "slow_mode_seconds":
			secs := req.GetSlowModeSeconds()
			in.SlowModeSeconds = &secs
		default:
			return nil, validation("update_mask")
		}
	}
	c, err := s.app.Update(ctx, caller, v[0], v[1], in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.UpdateChannelResponse{Channel: channelToProto(c)}, nil
}

// ArchiveChannel implements chatv1.ChannelServiceServer.
func (s *ChannelServer) ArchiveChannel(ctx context.Context, req *chatv1.ArchiveChannelRequest) (*chatv1.ArchiveChannelResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	c, err := s.app.SetArchived(ctx, caller, v[0], v[1], true)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.ArchiveChannelResponse{Channel: channelToProto(c)}, nil
}

// UnarchiveChannel implements chatv1.ChannelServiceServer.
func (s *ChannelServer) UnarchiveChannel(ctx context.Context, req *chatv1.UnarchiveChannelRequest) (*chatv1.UnarchiveChannelResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	c, err := s.app.SetArchived(ctx, caller, v[0], v[1], false)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.UnarchiveChannelResponse{Channel: channelToProto(c)}, nil
}

// SendMessage implements chatv1.ChannelServiceServer.
func (s *ChannelServer) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()), f("client_request_id", req.GetClientRequestId()))
	if err != nil {
		return nil, err
	}
	m, err := s.app.Send(ctx, caller, v[0], v[1], channel.SendInput{Body: req.GetBody(), ClientRequestID: v[2]})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.SendMessageResponse{Message: messageToProto(m)}, nil
}

// ListMessages implements chatv1.ChannelServiceServer.
func (s *ChannelServer) ListMessages(ctx context.Context, req *chatv1.ListMessagesRequest) (*chatv1.ListMessagesResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	in := channel.ListInput{PageSize: req.GetPageSize(), PageToken: req.GetPageToken()}
	if a := req.GetAfterMessageId(); a != "" {
		after, ok := id.Parse(a)
		if !ok {
			return nil, validation("after_message_id")
		}
		in.After = after
	}
	list, next, err := s.app.Messages(ctx, caller, v[0], v[1], in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListMessagesResponse{Messages: make([]*chatv1.Message, 0, len(list)), NextPageToken: next}
	for _, m := range list {
		out.Messages = append(out.Messages, messageToProto(m))
	}
	return out, nil
}

// DeleteMessage implements chatv1.ChannelServiceServer.
func (s *ChannelServer) DeleteMessage(ctx context.Context, req *chatv1.DeleteMessageRequest) (*chatv1.DeleteMessageResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()), f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, caller, v[0], v[1], v[2]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.DeleteMessageResponse{}, nil
}

// RestoreMessage implements chatv1.ChannelServiceServer.
func (s *ChannelServer) RestoreMessage(ctx context.Context, req *chatv1.RestoreMessageRequest) (*chatv1.RestoreMessageResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()), f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	m, err := s.app.Restore(ctx, caller, v[0], v[1], v[2])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RestoreMessageResponse{Message: messageToProto(m)}, nil
}

// MarkChannelRead implements chatv1.ChannelServiceServer.
func (s *ChannelServer) MarkChannelRead(ctx context.Context, req *chatv1.MarkChannelReadRequest) (*chatv1.MarkChannelReadResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()), f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.MarkRead(ctx, caller, v[0], v[1], v[2]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.MarkChannelReadResponse{}, nil
}

// ListChannelMembers implements chatv1.ChannelServiceServer (issue #183).
func (s *ChannelServer) ListChannelMembers(ctx context.Context, req *chatv1.ListChannelMembersRequest) (*chatv1.ListChannelMembersResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	page, err := s.app.Members(ctx, caller, v[0], v[1], req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListChannelMembersResponse{UserIds: make([]string, 0, len(page.Users)), NextPageToken: page.Next}
	for _, u := range page.Users {
		out.UserIds = append(out.UserIds, u.String())
	}
	return out, nil
}

// AddChannelMembers implements chatv1.ChannelServiceServer (issue #183).
func (s *ChannelServer) AddChannelMembers(ctx context.Context, req *chatv1.AddChannelMembersRequest) (*chatv1.AddChannelMembersResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()))
	if err != nil {
		return nil, err
	}
	users, err := userIDs(req.GetUserIds(), "user_ids")
	if err != nil {
		return nil, err
	}
	c, err := s.app.AddMembers(ctx, caller, v[0], v[1], users)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.AddChannelMembersResponse{Channel: channelToProto(c)}, nil
}

// RemoveChannelMember implements chatv1.ChannelServiceServer (issue #183).
func (s *ChannelServer) RemoveChannelMember(ctx context.Context, req *chatv1.RemoveChannelMemberRequest) (*chatv1.RemoveChannelMemberResponse, error) {
	caller, v, err := ids(ctx, f("tayfa_id", req.GetTayfaId()), f("channel_id", req.GetChannelId()), f("user_id", req.GetUserId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.RemoveMember(ctx, caller, v[0], v[1], v[2]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RemoveChannelMemberResponse{}, nil
}

package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// DirectMessages is what DirectMessageServer needs from the app layer.
type DirectMessages interface {
	Send(ctx context.Context, caller id.ID, in dm.SendInput) (dm.Message, dm.Conversation, error)
	List(ctx context.Context, caller id.ID, size int32, token string) ([]dm.Conversation, string, error)
	Get(ctx context.Context, caller, conversation id.ID) (dm.Conversation, error)
	Messages(ctx context.Context, caller, conversation id.ID, in dm.ListInput) ([]dm.Message, string, error)
	Delete(ctx context.Context, caller, conversation, message id.ID) error
	MarkRead(ctx context.Context, caller, conversation, message id.ID) error
	Accept(ctx context.Context, caller, conversation id.ID) (dm.Conversation, error)
	Decline(ctx context.Context, caller, conversation id.ID) error
	Declined(ctx context.Context, caller id.ID, size int32, token string) ([]dm.DeclinedRequest, string, error)
	RemoveDeclined(ctx context.Context, caller, conversation id.ID) error
	VerifyForReport(ctx context.Context, caller, message id.ID) (conversation, sender id.ID, err error)
}

// DirectMessageServer implements chatv1.DirectMessageServiceServer (issue
// #95). Every method acts for the caller of the verified access token
// only; the token is forwarded to core for the write rule. It never logs
// request fields: a message body must not reach any log.
type DirectMessageServer struct {
	chatv1.UnimplementedDirectMessageServiceServer
	app DirectMessages
	log *slog.Logger
}

// NewDirectMessageServer returns a DirectMessageServer.
func NewDirectMessageServer(app DirectMessages, log *slog.Logger) *DirectMessageServer {
	return &DirectMessageServer{app: app, log: log}
}

var conversationStatuses = map[dm.Status]chatv1.ConversationStatus{
	dm.StatusActive:          chatv1.ConversationStatus_CONVERSATION_STATUS_ACTIVE,
	dm.StatusRequestSent:     chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_SENT,
	dm.StatusRequestReceived: chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_RECEIVED,
}

func conversationToProto(c dm.Conversation) *chatv1.Conversation {
	return &chatv1.Conversation{
		Id: c.ID.String(), OtherUserId: idOrEmpty(c.Other), Status: conversationStatuses[c.Status],
		UnreadCount: c.Unread, LastReadMessageId: idOrEmpty(c.LastRead), PeerLastReadMessageId: idOrEmpty(c.PeerLastRead),
		LastMessageTime: timestamppb.New(c.LastMessageAt), CreateTime: timestamppb.New(c.CreatedAt),
	}
}

func directMessageToProto(m dm.Message) *chatv1.DirectMessage {
	return &chatv1.DirectMessage{
		Id: m.ID.String(), ConversationId: m.ConversationID.String(), SenderUserId: idOrEmpty(m.SenderID),
		Body: m.Body, Deleted: m.Deleted, CreateTime: timestamppb.New(m.CreatedAt),
	}
}

// SendDirectMessage implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) SendDirectMessage(ctx context.Context, req *chatv1.SendDirectMessageRequest) (*chatv1.SendDirectMessageResponse, error) {
	caller, v, err := ids(ctx, f("recipient_user_id", req.GetRecipientUserId()), f("client_request_id", req.GetClientRequestId()))
	if err != nil {
		return nil, err
	}
	m, c, err := s.app.Send(ctx, caller, dm.SendInput{Recipient: v[0], Body: req.GetBody(), ClientRequestID: v[1]})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.SendDirectMessageResponse{Message: directMessageToProto(m), Conversation: conversationToProto(c)}, nil
}

// ListConversations implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) ListConversations(ctx context.Context, req *chatv1.ListConversationsRequest) (*chatv1.ListConversationsResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.List(ctx, caller, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListConversationsResponse{Conversations: make([]*chatv1.Conversation, 0, len(list)), NextPageToken: next}
	for _, c := range list {
		out.Conversations = append(out.Conversations, conversationToProto(c))
	}
	return out, nil
}

// GetConversation implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) GetConversation(ctx context.Context, req *chatv1.GetConversationRequest) (*chatv1.GetConversationResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()))
	if err != nil {
		return nil, err
	}
	c, err := s.app.Get(ctx, caller, v[0])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetConversationResponse{Conversation: conversationToProto(c)}, nil
}

// ListDirectMessages implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) ListDirectMessages(ctx context.Context, req *chatv1.ListDirectMessagesRequest) (*chatv1.ListDirectMessagesResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()))
	if err != nil {
		return nil, err
	}
	in := dm.ListInput{PageSize: req.GetPageSize(), PageToken: req.GetPageToken()}
	if a := req.GetAfterMessageId(); a != "" {
		after, ok := id.Parse(a)
		if !ok {
			return nil, validation("after_message_id")
		}
		in.After = after
	}
	list, next, err := s.app.Messages(ctx, caller, v[0], in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListDirectMessagesResponse{Messages: make([]*chatv1.DirectMessage, 0, len(list)), NextPageToken: next}
	for _, m := range list {
		out.Messages = append(out.Messages, directMessageToProto(m))
	}
	return out, nil
}

// DeleteDirectMessage implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) DeleteDirectMessage(ctx context.Context, req *chatv1.DeleteDirectMessageRequest) (*chatv1.DeleteDirectMessageResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()), f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, caller, v[0], v[1]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.DeleteDirectMessageResponse{}, nil
}

// MarkConversationRead implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) MarkConversationRead(ctx context.Context, req *chatv1.MarkConversationReadRequest) (*chatv1.MarkConversationReadResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()), f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.MarkRead(ctx, caller, v[0], v[1]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.MarkConversationReadResponse{}, nil
}

// AcceptMessageRequest implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) AcceptMessageRequest(ctx context.Context, req *chatv1.AcceptMessageRequestRequest) (*chatv1.AcceptMessageRequestResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()))
	if err != nil {
		return nil, err
	}
	c, err := s.app.Accept(ctx, caller, v[0])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.AcceptMessageRequestResponse{Conversation: conversationToProto(c)}, nil
}

// DeclineMessageRequest implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) DeclineMessageRequest(ctx context.Context, req *chatv1.DeclineMessageRequestRequest) (*chatv1.DeclineMessageRequestResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.Decline(ctx, caller, v[0]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.DeclineMessageRequestResponse{}, nil
}

// ListDeclinedRequests implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) ListDeclinedRequests(ctx context.Context, req *chatv1.ListDeclinedRequestsRequest) (*chatv1.ListDeclinedRequestsResponse, error) {
	caller, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.Declined(ctx, caller, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListDeclinedRequestsResponse{DeclinedRequests: make([]*chatv1.DeclinedRequest, 0, len(list)), NextPageToken: next}
	for _, d := range list {
		out.DeclinedRequests = append(out.DeclinedRequests, &chatv1.DeclinedRequest{
			ConversationId: d.ConversationID.String(), SenderUserId: d.Sender.String(),
			DeclineTime: timestamppb.New(d.DeclinedAt), CreateTime: timestamppb.New(d.CreatedAt),
		})
	}
	return out, nil
}

// RemoveDeclinedRequest implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) RemoveDeclinedRequest(ctx context.Context, req *chatv1.RemoveDeclinedRequestRequest) (*chatv1.RemoveDeclinedRequestResponse, error) {
	caller, v, err := ids(ctx, f("conversation_id", req.GetConversationId()))
	if err != nil {
		return nil, err
	}
	if err := s.app.RemoveDeclined(ctx, caller, v[0]); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RemoveDeclinedRequestResponse{}, nil
}

// VerifyDirectMessageForReport implements chatv1.DirectMessageServiceServer.
func (s *DirectMessageServer) VerifyDirectMessageForReport(ctx context.Context, req *chatv1.VerifyDirectMessageForReportRequest) (*chatv1.VerifyDirectMessageForReportResponse, error) {
	caller, v, err := ids(ctx, f("message_id", req.GetMessageId()))
	if err != nil {
		return nil, err
	}
	conv, sender, err := s.app.VerifyForReport(ctx, caller, v[0])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.VerifyDirectMessageForReportResponse{ConversationId: conv.String(), SenderUserId: sender.String()}, nil
}

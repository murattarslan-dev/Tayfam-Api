package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

// DirectMessageClient is chat's DirectMessageService as the gateway uses
// it (issue #95).
type DirectMessageClient interface {
	SendDirectMessage(ctx context.Context, in *chatv1.SendDirectMessageRequest, opts ...grpc.CallOption) (*chatv1.SendDirectMessageResponse, error)
	ListConversations(ctx context.Context, in *chatv1.ListConversationsRequest, opts ...grpc.CallOption) (*chatv1.ListConversationsResponse, error)
	GetConversation(ctx context.Context, in *chatv1.GetConversationRequest, opts ...grpc.CallOption) (*chatv1.GetConversationResponse, error)
	ListDirectMessages(ctx context.Context, in *chatv1.ListDirectMessagesRequest, opts ...grpc.CallOption) (*chatv1.ListDirectMessagesResponse, error)
	DeleteDirectMessage(ctx context.Context, in *chatv1.DeleteDirectMessageRequest, opts ...grpc.CallOption) (*chatv1.DeleteDirectMessageResponse, error)
	MarkConversationRead(ctx context.Context, in *chatv1.MarkConversationReadRequest, opts ...grpc.CallOption) (*chatv1.MarkConversationReadResponse, error)
	AcceptMessageRequest(ctx context.Context, in *chatv1.AcceptMessageRequestRequest, opts ...grpc.CallOption) (*chatv1.AcceptMessageRequestResponse, error)
	DeclineMessageRequest(ctx context.Context, in *chatv1.DeclineMessageRequestRequest, opts ...grpc.CallOption) (*chatv1.DeclineMessageRequestResponse, error)
}

func registerDirectMessages(mux *http.ServeMux, h chatHandlers) {
	a := func(next coreHandler) http.HandlerFunc { return authenticated(h.d.Verifier, h.d.Log, next) }
	const c = "/v1/direct-messages/conversations/{conversation_id}"
	mux.HandleFunc("POST /v1/direct-messages", a(h.sendDirectMessage))
	mux.HandleFunc("GET /v1/direct-messages/conversations", a(h.listConversations))
	mux.HandleFunc("GET "+c, a(h.getConversation))
	mux.HandleFunc("GET "+c+"/messages", a(h.listDirectMessages))
	mux.HandleFunc("DELETE "+c+"/messages/{message_id}", a(h.deleteDirectMessage))
	mux.HandleFunc("PUT "+c+"/read", a(h.markConversationRead))
	mux.HandleFunc("POST "+c+"/accept", a(h.acceptMessageRequest))
	mux.HandleFunc("POST "+c+"/decline", a(h.declineMessageRequest))
}

var conversationStatusNames = map[chatv1.ConversationStatus]string{
	chatv1.ConversationStatus_CONVERSATION_STATUS_ACTIVE:           "active",
	chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_SENT:     "request_sent",
	chatv1.ConversationStatus_CONVERSATION_STATUS_REQUEST_RECEIVED: "request_received",
}

type conversationJSON struct {
	ID string `json:"id"`
	// Absent when the other party's account was deleted.
	OtherUserID           string    `json:"other_user_id,omitempty"`
	Status                string    `json:"status"`
	UnreadCount           int32     `json:"unread_count"`
	LastReadMessageID     string    `json:"last_read_message_id,omitempty"`
	PeerLastReadMessageID string    `json:"peer_last_read_message_id,omitempty"`
	LastMessageAt         time.Time `json:"last_message_at"`
	CreatedAt             time.Time `json:"created_at"`
}

func conversationFrom(c *chatv1.Conversation) conversationJSON {
	return conversationJSON{
		ID: c.GetId(), OtherUserID: c.GetOtherUserId(), Status: nameOr(conversationStatusNames, c.GetStatus()),
		UnreadCount: c.GetUnreadCount(), LastReadMessageID: c.GetLastReadMessageId(), PeerLastReadMessageID: c.GetPeerLastReadMessageId(),
		LastMessageAt: c.GetLastMessageTime().AsTime(), CreatedAt: c.GetCreateTime().AsTime(),
	}
}

type directMessageJSON struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	// Absent when the sender's account was deleted.
	SenderUserID string `json:"sender_user_id,omitempty"`
	// Absent when deleted.
	Body      string    `json:"body,omitempty"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"created_at"`
}

func directMessageFrom(m *chatv1.DirectMessage) directMessageJSON {
	return directMessageJSON{
		ID: m.GetId(), ConversationID: m.GetConversationId(), SenderUserID: m.GetSenderUserId(), Body: m.GetBody(),
		Deleted: m.GetDeleted(), CreatedAt: m.GetCreateTime().AsTime(),
	}
}

type conversationListJSON struct {
	Conversations []conversationJSON `json:"conversations"`
	NextPageToken string             `json:"next_page_token,omitempty"`
}

type directMessageListJSON struct {
	Messages      []directMessageJSON `json:"messages"`
	NextPageToken string              `json:"next_page_token,omitempty"`
}

type directMessageSentJSON struct {
	Message      directMessageJSON `json:"message"`
	Conversation conversationJSON  `json:"conversation"`
}

type directMessageSendBody struct {
	RecipientUserID string `json:"recipient_user_id"`
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

func (h chatHandlers) sendDirectMessage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b directMessageSendBody
	if !decodeLimit(w, r, &b, maxMessageBody) {
		return
	}
	if !validUUID(b.RecipientUserID) || !messageBodyOK(b.Body) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.DirectMessages.SendDirectMessage(ctx, &chatv1.SendDirectMessageRequest{
		RecipientUserId: b.RecipientUserID, Body: b.Body, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, directMessageSentJSON{
		Message: directMessageFrom(resp.GetMessage()), Conversation: conversationFrom(resp.GetConversation()),
	})
}

func (h chatHandlers) listConversations(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.DirectMessages.ListConversations(ctx, &chatv1.ListConversationsRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := conversationListJSON{Conversations: make([]conversationJSON, 0, len(resp.GetConversations())), NextPageToken: resp.GetNextPageToken()}
	for _, c := range resp.GetConversations() {
		out.Conversations = append(out.Conversations, conversationFrom(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) getConversation(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	resp, err := h.d.DirectMessages.GetConversation(ctx, &chatv1.GetConversationRequest{ConversationId: cid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, conversationFrom(resp.GetConversation()))
}

func (h chatHandlers) listDirectMessages(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after_message_id")
	if after != "" && !validUUID(after) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.DirectMessages.ListDirectMessages(ctx, &chatv1.ListDirectMessagesRequest{
		ConversationId: cid, PageSize: size, PageToken: token, AfterMessageId: after,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := directMessageListJSON{Messages: make([]directMessageJSON, 0, len(resp.GetMessages())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetMessages() {
		out.Messages = append(out.Messages, directMessageFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) deleteDirectMessage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	mid, ok := pathUUID(w, r, "message_id")
	if !ok {
		return
	}
	if _, err := h.d.DirectMessages.DeleteDirectMessage(ctx, &chatv1.DeleteDirectMessageRequest{ConversationId: cid, MessageId: mid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) markConversationRead(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	var b channelReadBody
	if !decodeLimit(w, r, &b, maxReadBody) {
		return
	}
	if !validUUID(b.MessageID) {
		badRequest(w, r)
		return
	}
	if _, err := h.d.DirectMessages.MarkConversationRead(ctx, &chatv1.MarkConversationReadRequest{ConversationId: cid, MessageId: b.MessageID}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) acceptMessageRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	resp, err := h.d.DirectMessages.AcceptMessageRequest(ctx, &chatv1.AcceptMessageRequestRequest{ConversationId: cid})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, conversationFrom(resp.GetConversation()))
}

func (h chatHandlers) declineMessageRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	if _, err := h.d.DirectMessages.DeclineMessageRequest(ctx, &chatv1.DeclineMessageRequestRequest{ConversationId: cid}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

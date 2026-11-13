package rest

import (
	"context"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

// ChannelClient is chat's ChannelService as the gateway uses it (issue #81).
type ChannelClient interface {
	ListChannels(ctx context.Context, in *chatv1.ListChannelsRequest, opts ...grpc.CallOption) (*chatv1.ListChannelsResponse, error)
	GetChannel(ctx context.Context, in *chatv1.GetChannelRequest, opts ...grpc.CallOption) (*chatv1.GetChannelResponse, error)
	CreateChannel(ctx context.Context, in *chatv1.CreateChannelRequest, opts ...grpc.CallOption) (*chatv1.CreateChannelResponse, error)
	UpdateChannel(ctx context.Context, in *chatv1.UpdateChannelRequest, opts ...grpc.CallOption) (*chatv1.UpdateChannelResponse, error)
	ArchiveChannel(ctx context.Context, in *chatv1.ArchiveChannelRequest, opts ...grpc.CallOption) (*chatv1.ArchiveChannelResponse, error)
	UnarchiveChannel(ctx context.Context, in *chatv1.UnarchiveChannelRequest, opts ...grpc.CallOption) (*chatv1.UnarchiveChannelResponse, error)
	SendMessage(ctx context.Context, in *chatv1.SendMessageRequest, opts ...grpc.CallOption) (*chatv1.SendMessageResponse, error)
	ListMessages(ctx context.Context, in *chatv1.ListMessagesRequest, opts ...grpc.CallOption) (*chatv1.ListMessagesResponse, error)
	DeleteMessage(ctx context.Context, in *chatv1.DeleteMessageRequest, opts ...grpc.CallOption) (*chatv1.DeleteMessageResponse, error)
	RestoreMessage(ctx context.Context, in *chatv1.RestoreMessageRequest, opts ...grpc.CallOption) (*chatv1.RestoreMessageResponse, error)
	MarkChannelRead(ctx context.Context, in *chatv1.MarkChannelReadRequest, opts ...grpc.CallOption) (*chatv1.MarkChannelReadResponse, error)
	// Event and selected-member channels (issue #183).
	ListChannelMembers(ctx context.Context, in *chatv1.ListChannelMembersRequest, opts ...grpc.CallOption) (*chatv1.ListChannelMembersResponse, error)
	AddChannelMembers(ctx context.Context, in *chatv1.AddChannelMembersRequest, opts ...grpc.CallOption) (*chatv1.AddChannelMembersResponse, error)
	RemoveChannelMember(ctx context.Context, in *chatv1.RemoveChannelMemberRequest, opts ...grpc.CallOption) (*chatv1.RemoveChannelMemberResponse, error)
}

const (
	channelNameMax = 40
	messageBodyMax = 4000
	// maxMessageBody fits 4000 characters in the worst JSON escaping
	// (\uXXXX surrogate pairs) plus the request ID.
	maxMessageBody = 64 << 10
	// maxChannelBody fits a 40-character name, the settings and up to
	// channelMembersMax member IDs (issue #183).
	maxChannelBody = 8 << 10
	// channelMembersMax bounds the user IDs of one request (issue #183).
	channelMembersMax = 100
	// maxReadBody fits one message ID.
	maxReadBody = 1 << 10
	// maxRetryAfter bounds the Retry-After header taken from chat.
	maxRetryAfter = 3600
)

func registerChannels(mux *http.ServeMux, h chatHandlers) {
	a := func(next coreHandler) http.HandlerFunc { return authenticated(h.d.Verifier, h.d.Log, next) }
	const ch = "/v1/tayfas/{tayfa_id}/channels/{channel_id}"
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/channels", a(h.listChannels))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/channels", a(h.createChannel))
	mux.HandleFunc("GET "+ch, a(h.getChannel))
	mux.HandleFunc("PATCH "+ch, a(h.updateChannel))
	mux.HandleFunc("PUT "+ch+"/archive", a(h.archiveChannel(true)))
	mux.HandleFunc("DELETE "+ch+"/archive", a(h.archiveChannel(false)))
	mux.HandleFunc("GET "+ch+"/messages", a(h.listMessages))
	mux.HandleFunc("POST "+ch+"/messages", a(h.sendMessage))
	mux.HandleFunc("DELETE "+ch+"/messages/{message_id}", a(h.deleteMessage))
	mux.HandleFunc("POST "+ch+"/messages/{message_id}/restoration", a(h.restoreMessage))
	mux.HandleFunc("PUT "+ch+"/read", a(h.markChannelRead))
	// Event and selected-member channels (issue #183).
	mux.HandleFunc("GET "+ch+"/members", a(h.listChannelMembers))
	mux.HandleFunc("POST "+ch+"/members", a(h.addChannelMembers))
	mux.HandleFunc("DELETE "+ch+"/members/{user_id}", a(h.removeChannelMember))
}

var channelVisibilityNames = map[chatv1.ChannelVisibility]string{
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_MEMBERS: "members",
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF:   "staff",
	// Issue #183.
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_EVENT:    "event",
	chatv1.ChannelVisibility_CHANNEL_VISIBILITY_SELECTED: "selected",
}

func channelVisibilityFrom(s string) (chatv1.ChannelVisibility, bool) {
	for k, v := range channelVisibilityNames {
		if v == s {
			return k, true
		}
	}
	return chatv1.ChannelVisibility_CHANNEL_VISIBILITY_UNSPECIFIED, false
}

type channelJSON struct {
	ID              string `json:"id"`
	TayfaID         string `json:"tayfa_id"`
	Name            string `json:"name"`
	Visibility      string `json:"visibility"`
	SlowModeSeconds int32  `json:"slow_mode_seconds"`
	// Absent when the creator's account was deleted.
	CreatorUserID string     `json:"creator_user_id,omitempty"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	// List only.
	UnreadCount       *int32 `json:"unread_count,omitempty"`
	LastReadMessageID string `json:"last_read_message_id,omitempty"`
	// Event and selected-member channels (issue #183).
	EventID          string     `json:"event_id,omitempty"`
	MemberCount      *int32     `json:"member_count,omitempty"`
	PlannedArchiveAt *time.Time `json:"archive_at,omitempty"`
}

func channelFrom(c *chatv1.Channel, withReads bool) channelJSON {
	out := channelJSON{
		ID: c.GetId(), TayfaID: c.GetTayfaId(), Name: c.GetName(), Visibility: nameOr(channelVisibilityNames, c.GetVisibility()),
		SlowModeSeconds: c.GetSlowModeSeconds(), CreatorUserID: c.GetCreatorUserId(), ArchivedAt: optTime(c.GetArchiveTime()),
		CreatedAt: c.GetCreateTime().AsTime(), UpdatedAt: c.GetUpdateTime().AsTime(),
	}
	if withReads {
		n := c.GetUnreadCount()
		out.UnreadCount, out.LastReadMessageID = &n, c.GetLastReadMessageId()
	}
	switch c.GetVisibility() {
	case chatv1.ChannelVisibility_CHANNEL_VISIBILITY_EVENT:
		out.EventID, out.PlannedArchiveAt = c.GetEventId(), optTime(c.GetPlannedArchiveTime())
		fallthrough
	case chatv1.ChannelVisibility_CHANNEL_VISIBILITY_SELECTED:
		n := c.GetMemberCount()
		out.MemberCount = &n
	}
	return out
}

type channelListJSON struct {
	Channels []channelJSON `json:"channels"`
}

type messageJSON struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	// Absent when the author's account was deleted.
	AuthorUserID string `json:"author_user_id,omitempty"`
	// Absent when deleted.
	Body       string    `json:"body,omitempty"`
	Deleted    bool      `json:"deleted"`
	Restorable bool      `json:"restorable,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func messageFrom(m *chatv1.Message) messageJSON {
	return messageJSON{
		ID: m.GetId(), ChannelID: m.GetChannelId(), AuthorUserID: m.GetAuthorUserId(), Body: m.GetBody(),
		Deleted: m.GetDeleted(), Restorable: m.GetRestorable(), CreatedAt: m.GetCreateTime().AsTime(),
	}
}

type messageListJSON struct {
	Messages      []messageJSON `json:"messages"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

// failChannel answers a chat error; slow mode also gets Retry-After.
func (h chatHandlers) failChannel(w http.ResponseWriter, r *http.Request, err error) {
	for _, d := range status.Convert(err).Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "chat.tayfam.app" || info.GetReason() != chatv1.ErrorReason_ERROR_REASON_SLOW_MODE.String() {
			continue
		}
		if n, err := strconv.Atoi(info.GetMetadata()["retry_after_seconds"]); err == nil && n > 0 && n <= maxRetryAfter {
			w.Header().Set("Retry-After", strconv.Itoa(n))
		}
	}
	h.fail(w, r, err)
}

func pathTayfaChannel(w http.ResponseWriter, r *http.Request) (tayfaID, channelID string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if channelID, ok = pathUUID(w, r, "channel_id"); !ok {
		return "", "", false
	}
	return tayfaID, channelID, true
}

func pathChannelMessage(w http.ResponseWriter, r *http.Request) (tayfaID, channelID, messageID string, ok bool) {
	if tayfaID, channelID, ok = pathTayfaChannel(w, r); !ok {
		return "", "", "", false
	}
	if messageID, ok = pathUUID(w, r, "message_id"); !ok {
		return "", "", "", false
	}
	return tayfaID, channelID, messageID, true
}

// The checks below mirror chat's limits so oversized input stops here;
// chat checks everything again.
func channelNameOK(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= channelNameMax
}

func messageBodyOK(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= messageBodyMax
}

func slowModeOK(n int32) bool { return n >= 0 && n <= 3600 }

func (h chatHandlers) listChannels(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Channels.ListChannels(ctx, &chatv1.ListChannelsRequest{TayfaId: id})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	out := channelListJSON{Channels: make([]channelJSON, 0, len(resp.GetChannels()))}
	for _, c := range resp.GetChannels() {
		out.Channels = append(out.Channels, channelFrom(c, true))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) getChannel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, ok := pathTayfaChannel(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Channels.GetChannel(ctx, &chatv1.GetChannelRequest{TayfaId: id, ChannelId: cid})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, channelFrom(resp.GetChannel(), false))
}

type channelCreateBody struct {
	Name            string `json:"name"`
	Visibility      string `json:"visibility"`
	SlowModeSeconds int32  `json:"slow_mode_seconds"`
	// Issue #183: event channels need the event, selected-member channels
	// may name their first members.
	EventID       string   `json:"event_id"`
	MemberUserIDs []string `json:"member_user_ids"`
}

func (h chatHandlers) createChannel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b channelCreateBody
	if !decodeLimit(w, r, &b, maxChannelBody) {
		return
	}
	req := &chatv1.CreateChannelRequest{TayfaId: id, Name: b.Name, SlowModeSeconds: b.SlowModeSeconds,
		EventId: b.EventID, MemberUserIds: b.MemberUserIDs}
	if (b.EventID != "" && !validUUID(b.EventID)) || !uuidsOK(b.MemberUserIDs) {
		badRequest(w, r)
		return
	}
	if b.Visibility != "" {
		v, ok := channelVisibilityFrom(b.Visibility)
		if !ok {
			badRequest(w, r)
			return
		}
		req.Visibility = v
	}
	if !channelNameOK(b.Name) || !slowModeOK(b.SlowModeSeconds) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Channels.CreateChannel(ctx, req)
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, channelFrom(resp.GetChannel(), false))
}

type channelUpdateBody struct {
	Name            *string `json:"name"`
	Visibility      *string `json:"visibility"`
	SlowModeSeconds *int32  `json:"slow_mode_seconds"`
}

func (h chatHandlers) updateChannel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, ok := pathTayfaChannel(w, r)
	if !ok {
		return
	}
	var b channelUpdateBody
	if !decodeLimit(w, r, &b, maxChannelBody) {
		return
	}
	req := &chatv1.UpdateChannelRequest{TayfaId: id, ChannelId: cid, UpdateMask: &fieldmaskpb.FieldMask{}}
	if b.Name != nil {
		if !channelNameOK(*b.Name) {
			badRequest(w, r)
			return
		}
		req.Name = *b.Name
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "name")
	}
	if b.Visibility != nil {
		v, ok := channelVisibilityFrom(*b.Visibility)
		if !ok {
			badRequest(w, r)
			return
		}
		req.Visibility = v
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "visibility")
	}
	if b.SlowModeSeconds != nil {
		if !slowModeOK(*b.SlowModeSeconds) {
			badRequest(w, r)
			return
		}
		req.SlowModeSeconds = *b.SlowModeSeconds
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "slow_mode_seconds")
	}
	if len(req.UpdateMask.Paths) == 0 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Channels.UpdateChannel(ctx, req)
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, channelFrom(resp.GetChannel(), false))
}

func (h chatHandlers) archiveChannel(archive bool) coreHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		id, cid, ok := pathTayfaChannel(w, r)
		if !ok {
			return
		}
		var (
			c   *chatv1.Channel
			err error
		)
		if archive {
			var resp *chatv1.ArchiveChannelResponse
			resp, err = h.d.Channels.ArchiveChannel(ctx, &chatv1.ArchiveChannelRequest{TayfaId: id, ChannelId: cid})
			c = resp.GetChannel()
		} else {
			var resp *chatv1.UnarchiveChannelResponse
			resp, err = h.d.Channels.UnarchiveChannel(ctx, &chatv1.UnarchiveChannelRequest{TayfaId: id, ChannelId: cid})
			c = resp.GetChannel()
		}
		if err != nil {
			h.failChannel(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, channelFrom(c, false))
	}
}

func (h chatHandlers) listMessages(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, ok := pathTayfaChannel(w, r)
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
	resp, err := h.d.Channels.ListMessages(ctx, &chatv1.ListMessagesRequest{
		TayfaId: id, ChannelId: cid, PageSize: size, PageToken: token, AfterMessageId: after,
	})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	out := messageListJSON{Messages: make([]messageJSON, 0, len(resp.GetMessages())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetMessages() {
		out.Messages = append(out.Messages, messageFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

type messageSendBody struct {
	Body            string `json:"body"`
	ClientRequestID string `json:"client_request_id"`
}

func (h chatHandlers) sendMessage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, ok := pathTayfaChannel(w, r)
	if !ok {
		return
	}
	var b messageSendBody
	if !decodeLimit(w, r, &b, maxMessageBody) {
		return
	}
	if !messageBodyOK(b.Body) || !validUUID(b.ClientRequestID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Channels.SendMessage(ctx, &chatv1.SendMessageRequest{
		TayfaId: id, ChannelId: cid, Body: b.Body, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, messageFrom(resp.GetMessage()))
}

func (h chatHandlers) deleteMessage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, mid, ok := pathChannelMessage(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Channels.DeleteMessage(ctx, &chatv1.DeleteMessageRequest{TayfaId: id, ChannelId: cid, MessageId: mid}); err != nil {
		h.failChannel(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) restoreMessage(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, mid, ok := pathChannelMessage(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Channels.RestoreMessage(ctx, &chatv1.RestoreMessageRequest{TayfaId: id, ChannelId: cid, MessageId: mid})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, messageFrom(resp.GetMessage()))
}

type channelReadBody struct {
	MessageID string `json:"message_id"`
}

func (h chatHandlers) markChannelRead(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, cid, ok := pathTayfaChannel(w, r)
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
	if _, err := h.d.Channels.MarkChannelRead(ctx, &chatv1.MarkChannelReadRequest{TayfaId: id, ChannelId: cid, MessageId: b.MessageID}); err != nil {
		h.failChannel(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uuidsOK checks a list of user IDs: at most channelMembersMax UUIDs.
func uuidsOK(ids []string) bool {
	if len(ids) > channelMembersMax {
		return false
	}
	for _, x := range ids {
		if !validUUID(x) {
			return false
		}
	}
	return true
}

type channelMembersJSON struct {
	UserIDs       []string `json:"user_ids"`
	NextPageToken string   `json:"next_page_token,omitempty"`
}

type channelMembersBody struct {
	UserIDs []string `json:"user_ids"`
}

// listChannelMembers lists the audience of an event or selected-member
// channel (issue #183).
func (h chatHandlers) listChannelMembers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	ch, ok := pathUUID(w, r, "channel_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Channels.ListChannelMembers(ctx, &chatv1.ListChannelMembersRequest{TayfaId: tf, ChannelId: ch, PageSize: size, PageToken: token})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, channelMembersJSON{UserIDs: append([]string{}, resp.GetUserIds()...), NextPageToken: resp.GetNextPageToken()})
}

// addChannelMembers adds members to a selected-member channel.
func (h chatHandlers) addChannelMembers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	ch, ok := pathUUID(w, r, "channel_id")
	if !ok {
		return
	}
	var b channelMembersBody
	if !decodeLimit(w, r, &b, maxChannelBody) {
		return
	}
	if len(b.UserIDs) == 0 || !uuidsOK(b.UserIDs) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Channels.AddChannelMembers(ctx, &chatv1.AddChannelMembersRequest{TayfaId: tf, ChannelId: ch, UserIds: b.UserIDs})
	if err != nil {
		h.failChannel(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, channelFrom(resp.GetChannel(), false))
}

// removeChannelMember takes a member out of a selected-member channel.
func (h chatHandlers) removeChannelMember(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	ch, ok := pathUUID(w, r, "channel_id")
	if !ok {
		return
	}
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if _, err := h.d.Channels.RemoveChannelMember(ctx, &chatv1.RemoveChannelMemberRequest{TayfaId: tf, ChannelId: ch, UserId: u}); err != nil {
		h.failChannel(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

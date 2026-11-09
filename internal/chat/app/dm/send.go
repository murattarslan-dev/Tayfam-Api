package dm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Send writes a message from caller to in.Recipient and returns it with the
// conversation as the caller sees it. It is the single send path for REST
// and the WebSocket (ADR-0005, v7 "Kurallar hem REST hem WebSocket mesaj
// gönderiminde aynı fonksiyonla kontrol edilir"):
//
//  0. the caller's message rate, shared with channel messages
//     (apperr.ErrRateLimited; retries count too);
//  1. a retry with the same ClientRequestID returns the first message;
//  2. core decides, with the caller's token, whether the caller may write
//     (Denied: apperr.ErrDirectMessageNotAllowed);
//  3. the conversation is opened (active, or a request within the caller's
//     daily limit) or its state applied: a waiting or declined request of
//     the caller refuses (apperr.ErrMessageRequestPending; the sender never
//     learns of a decline), the recipient's reply accepts it;
//  4. the message is stored encrypted, the caller's read marker moves to
//     it, and the recipient's notice (without content) carries their
//     unread count.
func (s *Service) Send(ctx context.Context, caller id.ID, in SendInput) (Message, Conversation, error) {
	switch {
	case in.Recipient == (id.ID{}) || in.Recipient == caller:
		return Message{}, Conversation{}, invalid("recipient_user_id")
	case !validBody(in.Body):
		return Message{}, Conversation{}, invalid("body")
	case in.ClientRequestID == (id.ID{}):
		return Message{}, Conversation{}, invalid("client_request_id")
	}
	if err := s.ready(); err != nil {
		return Message{}, Conversation{}, err
	}
	if !s.cfg.SendLimit.Allow(caller.String()) {
		return Message{}, Conversation{}, apperr.ErrRateLimited
	}
	q := store.New(s.cfg.Pool)
	if m, c, ok, err := s.existing(ctx, q, caller, in.Recipient, in.ClientRequestID); ok || err != nil {
		return m, c, err
	}
	v, err := s.cfg.Rules.Check(ctx, caller, in.Recipient)
	if err != nil {
		return Message{}, Conversation{}, fmt.Errorf("birebir mesaj kuralı: %w", err)
	}
	switch v.Decision {
	case Active, Request:
	default: // Denied, or anything unknown: the default answer is no.
		return Message{}, Conversation{}, apperr.ErrDirectMessageNotAllowed
	}

	now := s.now()
	msgID := s.ids.next()
	var (
		conv     convRow
		accepted bool // a request became active
		notice   *notification.Request
	)
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, opened, err := s.conversationFor(ctx, q, caller, in.Recipient, v, now)
		if err != nil {
			return err
		}
		conv = c
		if !opened {
			if accepted, err = s.applyState(ctx, q, c, caller, v, now); err != nil {
				return err
			}
			if accepted {
				conv.Status = stateActive
			}
		}
		convID := uuidOf(c.ID)
		sealed := s.cfg.Cipher.EncryptDirect(msgID, convID, in.Body)
		if _, err := q.CreateDirectMessage(ctx, store.CreateDirectMessageParams{
			ID: msgID.PG(), ConversationID: convID.PG(), SenderID: caller.PG(), BodyEncrypted: sealed.Data,
			KeyVersion: pgtype.Int2{Int16: sealed.Version, Valid: true}, ClientRequestID: in.ClientRequestID.PG(),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errRetried // a concurrent retry won; nothing of this one is kept
			}
			return fmt.Errorf("mesaj yazılamadı: %w", err)
		}
		if err := q.TouchConversation(ctx, store.TouchConversationParams{ID: convID.PG(), At: ts(now)}); err != nil {
			return fmt.Errorf("konuşma: %w", err)
		}
		// Writing means having read everything before.
		if err := markRead(ctx, q, caller, convID, msgID, now); err != nil {
			return err
		}
		if notice, err = s.notify(ctx, q, conv, opened, in.Recipient, msgID); err != nil {
			return err
		}
		if accepted {
			if err := record(ctx, tx, EventRequestAccepted, caller, "dm_conversation", convID, nil); err != nil {
				return err
			}
		}
		return record(ctx, tx, EventMessageCreated, caller, "dm_message", msgID, map[string]any{"conversation_id": convID.String()})
	})
	if errors.Is(err, errRetried) {
		m, c, ok, err := s.existing(ctx, store.New(s.cfg.Pool), caller, in.Recipient, in.ClientRequestID)
		if err == nil && !ok {
			err = errors.New("tekrar gönderilen mesaj bulunamadı")
		}
		return m, c, err
	}
	if err != nil {
		return Message{}, Conversation{}, err
	}
	convID := uuidOf(conv.ID)
	msg := Message{ID: msgID, ConversationID: convID, SenderID: caller, Body: in.Body, CreatedAt: now}
	view, err := s.view(ctx, store.New(s.cfg.Pool), caller, convID)
	if err != nil {
		return Message{}, Conversation{}, err
	}
	ps := parties(conv)
	if accepted {
		s.cfg.Publisher.ConversationAccepted(ps, convID)
	}
	s.cfg.Publisher.MessageCreated(ps, msg)
	if notice != nil {
		s.cfg.Publisher.Notified(*notice)
	}
	return msg, view, nil
}

// errRetried: the same ClientRequestID was written by a concurrent call.
var errRetried = errors.New("eşzamanlı tekrar gönderim")

// existing returns the caller's message sent with request, if any. The
// request ID belongs to one conversation: using it towards another
// recipient is a validation error.
func (s *Service) existing(ctx context.Context, q *store.Queries, caller, recipient, request id.ID) (Message, Conversation, bool, error) {
	r, err := q.GetDirectMessageByRequest(ctx, store.GetDirectMessageByRequestParams{SenderID: caller.PG(), ClientRequestID: request.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Message{}, Conversation{}, false, nil
	case err != nil:
		return Message{}, Conversation{}, false, fmt.Errorf("tekrar gönderim: %w", err)
	}
	c, err := q.GetConversationForUser(ctx, store.GetConversationForUserParams{ID: r.ConversationID, UserID: caller.PG()})
	if err != nil || other(c.UserLowID, c.UserHighID, caller) != recipient {
		return Message{}, Conversation{}, false, invalid("client_request_id")
	}
	m, err := s.messageOf(msgRow(r))
	if err != nil {
		return Message{}, Conversation{}, false, err
	}
	view, err := s.view(ctx, q, caller, uuidOf(c.ID))
	if err != nil {
		return Message{}, Conversation{}, false, err
	}
	return m, view, true, nil
}

// conversationFor locks the pair's conversation or opens it: active, or a
// request within the caller's daily limit. opened reports a new one.
func (s *Service) conversationFor(ctx context.Context, q *store.Queries, caller, recipient id.ID, v Verdict, now time.Time) (convRow, bool, error) {
	low, high := pair(caller, recipient)
	r, err := q.LockConversationByPair(ctx, store.LockConversationByPairParams{UserLowID: low.PG(), UserHighID: high.PG()})
	if err == nil {
		return convRow(r), false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return convRow{}, false, fmt.Errorf("konuşma: %w", err)
	}
	p := store.CreateConversationParams{
		ID: id.New().PG(), UserLowID: low.PG(), UserHighID: high.PG(), Status: stateActive, LastMessageAt: ts(now),
	}
	if v.Decision == Request {
		limit := v.RequestLimit
		if limit < 1 || limit > maxRequestLimit {
			limit = fallbackRequestLimit
		}
		if err := q.LockRequestLimit(ctx, caller.PG()); err != nil {
			return convRow{}, false, fmt.Errorf("istek sınırı kilidi: %w", err)
		}
		n, err := q.CountRecentRequests(ctx, store.CountRecentRequestsParams{RequestedBy: caller.PG(), Since: ts(now.Add(-RequestWindow))})
		if err != nil {
			return convRow{}, false, fmt.Errorf("istek sayısı: %w", err)
		}
		if n >= int64(limit) {
			return convRow{}, false, apperr.ErrMessageRequestLimit
		}
		p.Status, p.RequestedBy = stateRequest, caller.PG()
	}
	c, err := q.CreateConversation(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		// Opened concurrently by the other party or a parallel send: use it.
		r, err := q.LockConversationByPair(ctx, store.LockConversationByPairParams{UserLowID: low.PG(), UserHighID: high.PG()})
		if err != nil {
			return convRow{}, false, fmt.Errorf("konuşma: %w", err)
		}
		return convRow(r), false, nil
	}
	if err != nil {
		return convRow{}, false, fmt.Errorf("konuşma açılamadı: %w", err)
	}
	return convRow(c), true, nil
}

// applyState applies an existing conversation's state to a new message of
// caller and reports whether a request became active.
func (s *Service) applyState(ctx context.Context, q *store.Queries, c convRow, caller id.ID, v Verdict, now time.Time) (bool, error) {
	mine := uuidOf(c.RequestedBy) == caller
	switch {
	case c.Status == stateActive:
		return false, nil
	case c.Status == stateRequest && mine && v.Decision != Active:
		// One message until the recipient answers.
		return false, apperr.ErrMessageRequestPending
	case c.Status == stateDeclined && mine:
		// The sender is never told about the decline, and even core's
		// Active (friends) does not undo it: only the decliner can (issue
		// #101).
		return false, apperr.ErrMessageRequestPending
	}
	// The recipient replies (or writes after declining), or the requester
	// may now write freely anyway (for example they became friends).
	n, err := q.AcceptConversation(ctx, store.AcceptConversationParams{ID: c.ID, AcceptedAt: ts(now)})
	if err != nil {
		return false, fmt.Errorf("istek kabulü: %w", err)
	}
	return n == 1, nil
}

// notify keeps the recipient's one notice for the conversation (issue
// #101): a waiting (unread) notice gets the recipient's current unread
// count; otherwise a new one is written, dm_request for a new request and
// dm_message else, with that count. The notice carries the conversation ID
// and the count only; the message ID is the new notice's source.
func (s *Service) notify(ctx context.Context, q *store.Queries, c convRow, opened bool, recipient, message id.ID) (*notification.Request, error) {
	convID := uuidOf(c.ID)
	n, err := q.CountUnreadDirectMessages(ctx, store.CountUnreadDirectMessagesParams{UserID: recipient.PG(), ConversationID: convID.PG()})
	if err != nil {
		return nil, fmt.Errorf("okunmamış: %w", err)
	}
	n = min(max(n, 1), notification.MaxDirectUnread) // the new message is unread
	r := notification.Request{
		Event: message, TargetType: notification.TargetDMConversation, Target: convID, Recipients: []id.ID{recipient}, Unread: n,
	}
	typ, err := q.UpdateConversationNotificationUnread(ctx, store.UpdateConversationNotificationUnreadParams{
		UnreadCount: pgtype.Int4{Int32: n, Valid: true}, UserID: recipient.PG(), ConversationID: convID.PG(),
	})
	switch {
	case err == nil:
		r.Type = typ
		return &r, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("bildirim: %w", err)
	}
	r.Type = notification.TypeDMMessage
	if opened && c.Status == stateRequest {
		r.Type = notification.TypeDMRequest
	}
	if err := notification.Store(ctx, q, r); err != nil {
		return nil, err
	}
	return &r, nil
}

// markRead moves user's read marker forward and marks the conversation's
// notices read.
func markRead(ctx context.Context, q *store.Queries, user, conversation, message id.ID, now time.Time) error {
	if err := q.MarkConversationRead(ctx, store.MarkConversationReadParams{
		ConversationID: conversation.PG(), UserID: user.PG(), MessageID: message.PG(), ReadAt: ts(now),
	}); err != nil {
		return fmt.Errorf("okundu: %w", err)
	}
	if err := q.MarkConversationNotificationsRead(ctx, store.MarkConversationNotificationsReadParams{
		ReadAt: ts(now), UserID: user.PG(), ConversationID: conversation.PG(),
	}); err != nil {
		return fmt.Errorf("bildirim okundu: %w", err)
	}
	return nil
}

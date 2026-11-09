package dm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, invalid("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// conversationCursor is "<last_message_at unix µs>:<id>", base64url.
func encodeCursor(at time.Time, c id.ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + ":" + c.String()))
}

func decodeCursor(token string) (time.Time, id.ID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, id.ID{}, invalid("page_token")
	}
	us, cs, ok := strings.Cut(string(raw), ":")
	n, err := strconv.ParseInt(us, 10, 64)
	c, okID := id.Parse(cs)
	if !ok || err != nil || !okID {
		return time.Time{}, id.ID{}, invalid("page_token")
	}
	return time.UnixMicro(n).UTC(), c, nil
}

// List returns a page of the caller's conversations, the latest message
// first. Requests the caller declined are not listed.
func (s *Service) List(ctx context.Context, caller id.ID, size int32, token string) ([]Conversation, string, error) {
	n, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	p := store.ListConversationsPageParams{UserID: caller.PG(), N: n + 1}
	if token != "" {
		at, after, err := decodeCursor(token)
		if err != nil {
			return nil, "", err
		}
		p.AfterAt, p.AfterID = ts(at), after.PG()
	}
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	rows, err := store.New(s.cfg.Pool).ListConversationsPage(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("konuşmalar: %w", err)
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		last := rows[n-1]
		next = encodeCursor(last.LastMessageAt.Time, uuidOf(last.ID))
	}
	out := make([]Conversation, 0, len(rows))
	for _, r := range rows {
		if c, ok := viewOf(viewRow(r), caller); ok {
			out = append(out, c)
		}
	}
	return out, next, nil
}

// Get returns one of the caller's conversations.
func (s *Service) Get(ctx context.Context, caller, conversation id.ID) (Conversation, error) {
	if err := s.ready(); err != nil {
		return Conversation{}, err
	}
	return s.view(ctx, store.New(s.cfg.Pool), caller, conversation)
}

// Messages returns a page of a conversation's messages (ListInput, as in
// channel.Service.Messages). Deleted messages stay in place without a body.
func (s *Service) Messages(ctx context.Context, caller, conversation id.ID, in ListInput) ([]Message, string, error) {
	n, err := pageSize(in.PageSize)
	if err != nil {
		return nil, "", err
	}
	var cursor id.ID
	if in.PageToken != "" {
		v, ok := id.Parse(in.PageToken)
		if !ok {
			return nil, "", invalid("page_token")
		}
		cursor = v
	}
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	q := store.New(s.cfg.Pool)
	if _, err := load(ctx, q, caller, conversation); err != nil {
		return nil, "", err
	}
	var rows []msgRow
	if in.After != (id.ID{}) {
		after := in.After
		if cursor != (id.ID{}) {
			if bytes.Compare(cursor[:], after[:]) < 0 {
				return nil, "", invalid("page_token")
			}
			after = cursor
		}
		rs, err := q.ListDirectMessagesAfter(ctx, store.ListDirectMessagesAfterParams{ConversationID: conversation.PG(), AfterID: after.PG(), N: n + 1})
		if err != nil {
			return nil, "", fmt.Errorf("mesajlar: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, msgRow(r))
		}
	} else {
		before := pgtype.UUID{}
		if cursor != (id.ID{}) {
			before = cursor.PG()
		}
		rs, err := q.ListDirectMessagesBefore(ctx, store.ListDirectMessagesBeforeParams{ConversationID: conversation.PG(), BeforeID: before, N: n + 1})
		if err != nil {
			return nil, "", fmt.Errorf("mesajlar: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, msgRow(r))
		}
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		next = uuidOf(rows[n-1].ID).String()
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		m, err := s.messageOf(r)
		if err != nil {
			return nil, "", err
		}
		out = append(out, m)
	}
	return out, next, nil
}

// Delete erases one of the caller's own messages: the body goes at once
// and cannot be restored. Others' messages are "not found"; deleting again
// changes nothing. Deleting needs no permission from core: one's own words
// can always be taken back, also after a block.
func (s *Service) Delete(ctx context.Context, caller, conversation, message id.ID) error {
	if err := s.ready(); err != nil {
		return err
	}
	q := store.New(s.cfg.Pool)
	c, err := load(ctx, q, caller, conversation)
	if err != nil {
		return err
	}
	r, err := q.GetDirectMessage(ctx, store.GetDirectMessageParams{ConversationID: conversation.PG(), ID: message.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.ErrMessageNotFound
	case err != nil:
		return fmt.Errorf("mesaj: %w", err)
	case uuidOf(r.SenderID) != caller:
		return apperr.ErrMessageNotFound
	case r.DeletedAt.Valid:
		return nil
	}
	changed := false
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).EraseDirectMessage(ctx, store.EraseDirectMessageParams{
			DeletedAt: ts(s.now()), ConversationID: conversation.PG(), ID: message.PG(), SenderID: caller.PG(),
		})
		if err != nil {
			return fmt.Errorf("mesaj silme: %w", err)
		}
		if n == 0 {
			return nil // erased concurrently
		}
		changed = true
		return record(ctx, tx, EventMessageDeleted, caller, "dm_message", message, map[string]any{"conversation_id": conversation.String()})
	})
	if err != nil {
		return err
	}
	if changed {
		s.cfg.Publisher.MessageDeleted(parties(c), conversation, message)
	}
	return nil
}

// MarkRead moves the caller's read marker to message (a message of the
// conversation). It only moves forward. The other party hears about it in
// active conversations only: a requester does not learn that the request
// was read.
func (s *Service) MarkRead(ctx context.Context, caller, conversation, message id.ID) error {
	if err := s.ready(); err != nil {
		return err
	}
	q := store.New(s.cfg.Pool)
	c, err := load(ctx, q, caller, conversation)
	if err != nil {
		return err
	}
	if _, err := q.GetDirectMessage(ctx, store.GetDirectMessageParams{ConversationID: conversation.PG(), ID: message.PG()}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrMessageNotFound
		}
		return fmt.Errorf("mesaj: %w", err)
	}
	if err := markRead(ctx, q, caller, conversation, message, s.now()); err != nil {
		return err
	}
	if c.Status != stateActive {
		return nil
	}
	r, err := q.GetConversationRead(ctx, store.GetConversationReadParams{ConversationID: conversation.PG(), UserID: caller.PG()})
	if err != nil {
		return fmt.Errorf("okundu: %w", err)
	}
	if peer := other(c.UserLowID, c.UserHighID, caller); peer != (id.ID{}) {
		s.cfg.Publisher.ConversationRead(peer, caller, conversation, uuidOf(r.LastReadMessageID))
	}
	return nil
}

// Accept accepts a request the caller received. Accepting an active
// conversation changes nothing; anything else is
// apperr.ErrMessageRequestNotFound.
func (s *Service) Accept(ctx context.Context, caller, conversation id.ID) (Conversation, error) {
	if err := s.ready(); err != nil {
		return Conversation{}, err
	}
	c, err := load(ctx, store.New(s.cfg.Pool), caller, conversation)
	if err != nil {
		return Conversation{}, err
	}
	accepted := false
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.LockConversationByPair(ctx, store.LockConversationByPairParams{UserLowID: c.UserLowID, UserHighID: c.UserHighID})
		if err != nil {
			return fmt.Errorf("konuşma: %w", err)
		}
		switch {
		case r.Status == stateActive:
			return nil
		case r.Status != stateRequest || uuidOf(r.RequestedBy) == caller:
			return apperr.ErrMessageRequestNotFound
		}
		if _, err := q.AcceptConversation(ctx, store.AcceptConversationParams{ID: r.ID, AcceptedAt: ts(s.now())}); err != nil {
			return fmt.Errorf("istek kabulü: %w", err)
		}
		accepted = true
		return record(ctx, tx, EventRequestAccepted, caller, "dm_conversation", conversation, nil)
	})
	if err != nil {
		return Conversation{}, err
	}
	if accepted {
		s.cfg.Publisher.ConversationAccepted(parties(c), conversation)
	}
	return s.view(ctx, store.New(s.cfg.Pool), caller, conversation)
}

// Decline declines a request the caller received: the sender is not told
// and cannot write again; for the caller the conversation disappears (its
// notices are marked read). Anything else is
// apperr.ErrMessageRequestNotFound.
func (s *Service) Decline(ctx context.Context, caller, conversation id.ID) error {
	if err := s.ready(); err != nil {
		return err
	}
	c, err := load(ctx, store.New(s.cfg.Pool), caller, conversation)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.LockConversationByPair(ctx, store.LockConversationByPairParams{UserLowID: c.UserLowID, UserHighID: c.UserHighID})
		if err != nil {
			return fmt.Errorf("konuşma: %w", err)
		}
		if r.Status != stateRequest || uuidOf(r.RequestedBy) == caller {
			return apperr.ErrMessageRequestNotFound
		}
		if _, err := q.DeclineConversation(ctx, store.DeclineConversationParams{ID: r.ID, DeclinedAt: ts(s.now())}); err != nil {
			return fmt.Errorf("istek reddi: %w", err)
		}
		if err := q.MarkConversationNotificationsRead(ctx, store.MarkConversationNotificationsReadParams{
			ReadAt: ts(s.now()), UserID: caller.PG(), ConversationID: conversation.PG(),
		}); err != nil {
			return fmt.Errorf("bildirim okundu: %w", err)
		}
		return record(ctx, tx, EventRequestDeclined, caller, "dm_conversation", conversation, nil)
	})
}

// DeclinedRequest is a request the caller declined (their "declined list",
// issue #101).
type DeclinedRequest struct {
	ConversationID id.ID
	Sender         id.ID
	DeclinedAt     time.Time
	CreatedAt      time.Time
}

// Declined returns a page of the requests the caller declined, the latest
// decline first. Only the decliner sees the list; the sender never does.
// A request whose sender's account was deleted is not listed.
func (s *Service) Declined(ctx context.Context, caller id.ID, size int32, token string) ([]DeclinedRequest, string, error) {
	n, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	p := store.ListDeclinedRequestsParams{UserID: caller.PG(), N: n + 1}
	if token != "" {
		at, after, err := decodeCursor(token)
		if err != nil {
			return nil, "", err
		}
		p.AfterAt, p.AfterID = ts(at), after.PG()
	}
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	rows, err := store.New(s.cfg.Pool).ListDeclinedRequests(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("reddedilenler: %w", err)
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		last := rows[n-1]
		next = encodeCursor(last.DeclinedAt.Time, uuidOf(last.ID))
	}
	out := make([]DeclinedRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, DeclinedRequest{
			ConversationID: uuidOf(r.ID), Sender: uuidOf(r.RequestedBy),
			DeclinedAt: r.DeclinedAt.Time.UTC(), CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, next, nil
}

// RemoveDeclined takes a request off the caller's declined list: the
// conversation becomes a waiting request again (the caller may accept,
// reply or decline it; the sender still has only their one message until
// then, unless core lets them write freely). The sender is not told: no
// notice, no live event. A conversation the caller is not a party of is
// apperr.ErrConversationNotFound; one that is not a request the caller
// declined is apperr.ErrMessageRequestNotFound.
func (s *Service) RemoveDeclined(ctx context.Context, caller, conversation id.ID) error {
	if err := s.ready(); err != nil {
		return err
	}
	if _, err := store.New(s.cfg.Pool).GetConversationForUser(ctx, store.GetConversationForUserParams{ID: conversation.PG(), UserID: caller.PG()}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrConversationNotFound
		}
		return fmt.Errorf("konuşma: %w", err)
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).RestoreDeclinedRequest(ctx, store.RestoreDeclinedRequestParams{ID: conversation.PG(), UserID: caller.PG()})
		if err != nil {
			return fmt.Errorf("reddedilenlerden çıkarma: %w", err)
		}
		if n == 0 {
			return apperr.ErrMessageRequestNotFound
		}
		return record(ctx, tx, EventRequestRestored, caller, "dm_conversation", conversation, nil)
	})
}

// Cleanup removes a deleted user from direct messages inside the deletion's
// transaction (userevents.Cleanup; v14 "İlişkiler ve Silme Davranışı"):
// their side of each conversation, their requests and their sender fields
// become empty, their read markers go; conversations whose both parties are
// gone are deleted with their messages.
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	for _, f := range []func(context.Context, pgtype.UUID) error{
		q.ClearConversationLowSide, q.ClearConversationHighSide, q.ClearDirectMessageSender, q.DeleteUserConversationReads,
	} {
		if err := f(ctx, user.PG()); err != nil {
			return fmt.Errorf("birebir mesaj temizliği: %w", err)
		}
	}
	if err := q.DeleteAbandonedConversations(ctx); err != nil {
		return fmt.Errorf("terk edilmiş konuşmalar: %w", err)
	}
	return nil
}

// PurgeDeleted permanently deletes messages deleted more than PurgeAfter
// ago and returns how many.
func (s *Service) PurgeDeleted(ctx context.Context) (int64, error) {
	before := ts(s.now().Add(-PurgeAfter))
	var total int64
	for {
		n, err := store.New(s.cfg.Pool).PurgeDeletedDirectMessages(ctx, store.PurgeDeletedDirectMessagesParams{Before: before, N: purgeBatch})
		if err != nil {
			return total, fmt.Errorf("silinmiş birebir mesajlar: %w", err)
		}
		total += n
		if n < purgeBatch {
			return total, nil
		}
	}
}

// RunPurge runs PurgeDeleted every hour until ctx is done.
func (s *Service) RunPurge(ctx context.Context) error {
	t := time.NewTicker(purgeEvery)
	defer t.Stop()
	for {
		if n, err := s.PurgeDeleted(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.WarnContext(ctx, "silinmiş birebir mesajlar silinemedi", slog.String("hata", err.Error()))
		} else if n > 0 {
			s.cfg.Log.InfoContext(ctx, "silinmiş birebir mesajlar kalıcı silindi", slog.Int64("adet", n))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// VerifyForReport answers core when caller reports a direct message
// (issue #95; core forwards the reporter's own token): the message's
// conversation and its sender, never the body. caller must be a party of
// the conversation (a request they declined included); a deleted message
// that is not yet purged counts. The caller's own message and a message
// whose sender's account was deleted are apperr.ErrMessageNotFound. It
// needs no message key.
func (s *Service) VerifyForReport(ctx context.Context, caller, message id.ID) (conversation, sender id.ID, err error) {
	r, err := store.New(s.cfg.Pool).GetDirectMessageForParty(ctx, store.GetDirectMessageForPartyParams{ID: message.PG(), UserID: caller.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return id.ID{}, id.ID{}, apperr.ErrMessageNotFound
	case err != nil:
		return id.ID{}, id.ID{}, fmt.Errorf("şikâyet doğrulaması: %w", err)
	}
	sender = uuidOf(r.SenderID)
	if sender == (id.ID{}) || sender == caller {
		return id.ID{}, id.ID{}, apperr.ErrMessageNotFound
	}
	return uuidOf(r.ConversationID), sender, nil
}

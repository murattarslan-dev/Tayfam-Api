package channel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// msgRow is a message row; every message query returns these columns.
type msgRow = store.GetMessageRow

// view converts a row for a caller with membership m. A body that does not
// decrypt is an error: it means a wrong key or a tampered row, never
// something to show.
func (s *Service) view(r msgRow, m Decision, now time.Time) (Message, error) {
	msg := Message{ID: id.ID(r.ID.Bytes), ChannelID: id.ID(r.ChannelID.Bytes), CreatedAt: r.CreatedAt.Time.UTC()}
	if r.AuthorID.Valid {
		msg.AuthorID = id.ID(r.AuthorID.Bytes)
	}
	if r.DeletedAt.Valid {
		msg.Deleted = true
		msg.Restorable = r.BodyEncrypted != nil && now.Sub(r.DeletedAt.Time) <= RestoreWindow && m.Allowed(ActionRestore)
		return msg, nil
	}
	body, err := s.cfg.Cipher.Decrypt(msg.ID, msg.ChannelID, channelcrypt.Sealed{Version: r.KeyVersion.Int16, Data: r.BodyEncrypted})
	if err != nil {
		return Message{}, fmt.Errorf("mesaj %s: %w", msg.ID, err)
	}
	msg.Body = body
	return msg, nil
}

// Send writes a message to a channel and returns it. It is the single send
// path for REST and the WebSocket (ADR-0005): the caller must be an active
// member who may see the channel, not muted, the channel not archived, and
// slow mode must allow it (moderators and admins are exempt). Sending again
// with the same ClientRequestID returns the first message without checking
// slow mode again; using it for another channel is a validation error.
func (s *Service) Send(ctx context.Context, caller, tayfa, channel id.ID, in SendInput) (Message, error) {
	switch {
	case in.ClientRequestID == (id.ID{}):
		return Message{}, invalid("client_request_id")
	case !validBody(in.Body):
		return Message{}, invalid("body")
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Message{}, err
	}
	now := s.now()
	q := store.New(s.cfg.Pool)
	ch, err := s.loadChannel(ctx, q, m, tayfa, channel, false)
	if err != nil {
		return Message{}, err
	}
	if msg, ok, err := s.existing(ctx, q, m, caller, channel, in.ClientRequestID, now); ok || err != nil {
		return msg, err
	}
	if err := authorize(m, ActionPost); err != nil {
		return Message{}, err
	}
	if ch.ArchivedAt.Valid {
		return Message{}, apperr.ErrChannelArchived
	}
	if ch.SlowModeSeconds > 0 && !m.Allowed(ActionSkipSlowMode) {
		wait, undo := s.slow.take(channel, caller, now, time.Duration(ch.SlowModeSeconds)*time.Second)
		if undo == nil {
			return Message{}, &apperr.SlowModeError{RetryAfter: wait}
		}
		defer func() {
			if err != nil {
				undo()
			}
		}()
	}

	msgID := s.ids.next()
	sealed := s.cfg.Cipher.Encrypt(msgID, channel, in.Body)
	var row msgRow
	created := false
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		r, err := store.New(tx).CreateMessage(ctx, store.CreateMessageParams{
			ID: msgID.PG(), ChannelID: channel.PG(), AuthorID: caller.PG(), BodyEncrypted: sealed.Data,
			KeyVersion: pgtype.Int2{Int16: sealed.Version, Valid: true}, ClientRequestID: in.ClientRequestID.PG(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a concurrent retry won; read it below
		}
		if err != nil {
			return fmt.Errorf("mesaj yazılamadı: %w", err)
		}
		row, created = msgRow(r), true
		return record(ctx, tx, EventMessageCreated, caller, "channel_message", msgID, map[string]any{
			"tayfa_id": tayfa.String(), "channel_id": channel.String(),
		})
	})
	if err != nil {
		return Message{}, err
	}
	if !created {
		msg, ok, err := s.existing(ctx, q, m, caller, channel, in.ClientRequestID, now)
		if err == nil && !ok {
			err = errors.New("tekrar gönderilen mesaj bulunamadı")
		}
		return msg, err
	}
	// The caller wrote it: return the body without decrypting it again.
	msg := Message{ID: msgID, ChannelID: channel, AuthorID: caller, Body: in.Body, CreatedAt: row.CreatedAt.Time.UTC()}
	return msg, nil
}

// existing returns the caller's message sent with request, if any.
func (s *Service) existing(ctx context.Context, q *store.Queries, m Decision, caller, channel, request id.ID, now time.Time) (Message, bool, error) {
	r, err := q.GetMessageByRequest(ctx, store.GetMessageByRequestParams{AuthorID: caller.PG(), ClientRequestID: request.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Message{}, false, nil
	case err != nil:
		return Message{}, false, fmt.Errorf("tekrar gönderim: %w", err)
	case id.ID(r.ChannelID.Bytes) != channel:
		return Message{}, false, invalid("client_request_id")
	}
	msg, err := s.view(msgRow(r), m, now)
	return msg, err == nil, err
}

// Messages returns a page of a channel's messages (ListInput). Deleted
// messages stay in place without a body.
func (s *Service) Messages(ctx context.Context, caller, tayfa, channel id.ID, in ListInput) ([]Message, string, error) {
	n := int32(defaultPageSize)
	switch {
	case in.PageSize < 0:
		return nil, "", invalid("page_size")
	case in.PageSize > maxPageSize:
		n = maxPageSize
	case in.PageSize > 0:
		n = in.PageSize
	}
	var cursor id.ID
	if in.PageToken != "" {
		v, ok := id.Parse(in.PageToken)
		if !ok {
			return nil, "", invalid("page_token")
		}
		cursor = v
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	if err := authorize(m, ActionView); err != nil {
		return nil, "", err
	}
	q := store.New(s.cfg.Pool)
	if _, err := s.loadChannel(ctx, q, m, tayfa, channel, false); err != nil {
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
		rs, err := q.ListMessagesAfter(ctx, store.ListMessagesAfterParams{ChannelID: channel.PG(), AfterID: after.PG(), N: n + 1})
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
		rs, err := q.ListMessagesBefore(ctx, store.ListMessagesBeforeParams{ChannelID: channel.PG(), BeforeID: before, N: n + 1})
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
		next = id.ID(rows[n-1].ID.Bytes).String()
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(r, m, now)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	return out, next, nil
}

// Delete removes a message. The author's own deletion erases the body at
// once and cannot be undone, also when a moderator had hidden it before.
// Hiding someone else's message needs core's ActionModerate against the
// author's current role (a moderator cannot hide a moderator's or an
// admin's message); an admin can restore it within RestoreWindow. Deleting
// an already deleted message changes nothing. A read-only tayfa refuses
// both.
func (s *Service) Delete(ctx context.Context, caller, tayfa, channel, message id.ID) error {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return err
	}
	if err := authorize(m, ActionView); err != nil {
		return err
	}
	row, err := s.findMessage(ctx, m, tayfa, channel, message)
	if err != nil {
		return err
	}
	own := row.AuthorID.Valid && id.ID(row.AuthorID.Bytes) == caller
	if own {
		err = authorize(m, ActionDeleteOwn)
	} else {
		err = s.onAuthor(ctx, caller, tayfa, ActionModerate, row)
	}
	if err != nil {
		return err
	}
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.GetMessageForUpdate(ctx, store.GetMessageForUpdateParams{ChannelID: channel.PG(), ID: message.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrMessageNotFound
		case err != nil:
			return fmt.Errorf("mesaj: %w", err)
		}
		var n int64
		if own {
			if r.BodyEncrypted == nil {
				return nil // already erased
			}
			n, err = q.EraseMessage(ctx, store.EraseMessageParams{DeletedAt: ts(now), DeletedBy: caller.PG(), ChannelID: channel.PG(), ID: message.PG()})
		} else {
			if r.DeletedAt.Valid {
				return nil // already deleted
			}
			n, err = q.HideMessage(ctx, store.HideMessageParams{DeletedAt: ts(now), DeletedBy: caller.PG(), ChannelID: channel.PG(), ID: message.PG()})
		}
		if err != nil {
			return fmt.Errorf("mesaj silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrMessageNotFound
		}
		return record(ctx, tx, EventMessageDeleted, caller, "channel_message", message, map[string]any{
			"tayfa_id": tayfa.String(), "channel_id": channel.String(), "restorable": !own,
		})
	})
}

// findMessage loads a message of a channel the caller may see.
func (s *Service) findMessage(ctx context.Context, m Decision, tayfa, channel, message id.ID) (msgRow, error) {
	q := store.New(s.cfg.Pool)
	if _, err := s.loadChannel(ctx, q, m, tayfa, channel, false); err != nil {
		return msgRow{}, err
	}
	r, err := q.GetMessage(ctx, store.GetMessageParams{ChannelID: channel.PG(), ID: message.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return msgRow{}, apperr.ErrMessageNotFound
	case err != nil:
		return msgRow{}, fmt.Errorf("mesaj: %w", err)
	}
	return r, nil
}

// onAuthor asks core whether the caller may perform a on the message's
// author (role hierarchy against the author's current role). A message
// whose author's account was deleted has no target.
func (s *Service) onAuthor(ctx context.Context, caller, tayfa id.ID, a Action, row msgRow) error {
	var author id.ID
	if row.AuthorID.Valid {
		author = id.ID(row.AuthorID.Bytes)
	}
	d, err := s.cfg.Access.Channel(ctx, tayfa, caller, a, author)
	if err != nil {
		return fmt.Errorf("kanal izni: %w", err)
	}
	return denialErr(d.Denial)
}

// Restore undoes a moderation deletion within RestoreWindow. Admins only,
// above the author's current role (core decides). The author's own
// deletions cannot be restored (the body is gone). Restoring a visible
// message changes nothing. Every restore is recorded
// (channel.message_restored with the admin as actor and who had deleted
// it).
func (s *Service) Restore(ctx context.Context, caller, tayfa, channel, message id.ID) (Message, error) {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Message{}, err
	}
	now := s.now()
	if err := authorize(m, ActionRestore); err != nil {
		return Message{}, err
	}
	row, err := s.findMessage(ctx, m, tayfa, channel, message)
	if err != nil {
		return Message{}, err
	}
	if !row.DeletedAt.Valid {
		return s.view(row, m, now)
	}
	if row.BodyEncrypted == nil || now.Sub(row.DeletedAt.Time) > RestoreWindow {
		return Message{}, apperr.ErrMessageNotFound
	}
	if err := s.onAuthor(ctx, caller, tayfa, ActionRestore, row); err != nil {
		return Message{}, err
	}
	var msg Message
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.GetMessageForUpdate(ctx, store.GetMessageForUpdateParams{ChannelID: channel.PG(), ID: message.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrMessageNotFound
		case err != nil:
			return fmt.Errorf("mesaj: %w", err)
		}
		locked := msgRow(r)
		if !locked.DeletedAt.Valid {
			msg, err = s.view(locked, m, now)
			return err
		}
		n, err := q.RestoreMessage(ctx, store.RestoreMessageParams{ChannelID: channel.PG(), ID: message.PG()})
		if err != nil {
			return fmt.Errorf("geri alma: %w", err)
		}
		if n != 1 {
			return apperr.ErrMessageNotFound
		}
		deletedBy := ""
		if locked.DeletedBy.Valid {
			deletedBy = id.ID(locked.DeletedBy.Bytes).String()
		}
		locked.DeletedAt, locked.DeletedBy = pgtype.Timestamptz{}, pgtype.UUID{}
		if msg, err = s.view(locked, m, now); err != nil {
			return err
		}
		return record(ctx, tx, EventMessageRestored, caller, "channel_message", message, map[string]any{
			"tayfa_id": tayfa.String(), "channel_id": channel.String(), "deleted_by": deletedBy,
		})
	})
	if err != nil {
		return Message{}, err
	}
	return msg, nil
}

// MarkRead moves the caller's read marker in the channel to message. The
// marker only moves forward; marking an older message changes nothing.
func (s *Service) MarkRead(ctx context.Context, caller, tayfa, channel, message id.ID) error {
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return err
	}
	if err := authorize(m, ActionMarkRead); err != nil {
		return err
	}
	q := store.New(s.cfg.Pool)
	if _, err := s.loadChannel(ctx, q, m, tayfa, channel, false); err != nil {
		return err
	}
	if _, err := q.GetMessage(ctx, store.GetMessageParams{ChannelID: channel.PG(), ID: message.PG()}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrMessageNotFound
		}
		return fmt.Errorf("mesaj: %w", err)
	}
	if err := q.MarkChannelRead(ctx, store.MarkChannelReadParams{ChannelID: channel.PG(), UserID: caller.PG(), MessageID: message.PG()}); err != nil {
		return fmt.Errorf("okundu: %w", err)
	}
	return nil
}

// Cleanup removes a deleted user from channels inside the deletion's
// transaction (userevents.Cleanup, ADR-0009): their messages stay with no
// author ("Silinmiş kullanıcı"), their read markers go.
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	for _, f := range []func(context.Context, pgtype.UUID) error{
		q.ClearChannelMessageAuthor, q.ClearChannelMessageDeletedBy, q.ClearChannelCreator, q.DeleteUserChannelReads,
	} {
		if err := f(ctx, user.PG()); err != nil {
			return fmt.Errorf("kanal temizliği: %w", err)
		}
	}
	return nil
}

// PurgeTayfa removes a permanently deleted tayfa's channels with their
// messages and read markers (tayfa.purged).
func PurgeTayfa(ctx context.Context, q *store.Queries, tayfa id.ID) error {
	if err := q.DeleteTayfaChannels(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("tayfa kanalları: %w", err)
	}
	return nil
}

// PurgeDeleted permanently deletes messages deleted more than RestoreWindow
// ago and returns how many.
func (s *Service) PurgeDeleted(ctx context.Context) (int64, error) {
	before := ts(s.now().Add(-RestoreWindow))
	var total int64
	for {
		n, err := store.New(s.cfg.Pool).PurgeDeletedMessages(ctx, store.PurgeDeletedMessagesParams{Before: before, N: purgeBatch})
		if err != nil {
			return total, fmt.Errorf("silinmiş mesajlar: %w", err)
		}
		total += n
		if n < purgeBatch {
			return total, nil
		}
	}
}

// RunPurge runs PurgeDeleted every hour until ctx is done. Running it in
// more than one pod is harmless.
func (s *Service) RunPurge(ctx context.Context) error {
	t := time.NewTicker(purgeEvery)
	defer t.Stop()
	for {
		if n, err := s.PurgeDeleted(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.WarnContext(ctx, "silinmiş kanal mesajları silinemedi", slog.String("hata", err.Error()))
		} else if n > 0 {
			s.cfg.Log.InfoContext(ctx, "silinmiş kanal mesajları kalıcı silindi", slog.Int64("adet", n))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

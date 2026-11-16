package channel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Reported channel messages (issue #205, ADR-0031; platform owner,
// 2026-11-16). Reports live in core; chat answers three questions about a
// message: may the reporter report it (and what did it say), is it removed,
// and the platform's removal.

// ReportTarget returns message as caller sees it when caller reports it
// (core ReportService.CreateReport with the reporter's own token): caller
// must be able to read its channel by the same rule as Messages (the
// channel's audience included), the message must be of tayfa, not deleted,
// and its author's account not deleted. Anything else is
// apperr.ErrMessageNotFound, with no reason given. The caller's own message
// is answered; core refuses it.
func (s *Service) ReportTarget(ctx context.Context, caller, tayfa, message id.ID) (Message, error) {
	q := store.New(s.cfg.Pool)
	r, err := q.GetChannelMessageForReport(ctx, message.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Message{}, apperr.ErrMessageNotFound
	case err != nil:
		return Message{}, fmt.Errorf("şikâyet edilen mesaj: %w", err)
	case id.ID(r.TayfaID.Bytes) != tayfa, r.DeletedAt.Valid, !r.AuthorID.Valid:
		return Message{}, apperr.ErrMessageNotFound
	}
	m, err := s.access(ctx, caller, tayfa)
	if err != nil {
		return Message{}, notFoundForReport(err)
	}
	if err := authorize(m, ActionView); err != nil {
		return Message{}, notFoundForReport(err)
	}
	if _, err := s.loadChannel(ctx, q, m, tayfa, id.ID(r.ChannelID.Bytes), false); err != nil {
		return Message{}, notFoundForReport(err)
	}
	return s.view(msgRow{
		ID: r.ID, ChannelID: r.ChannelID, AuthorID: r.AuthorID, BodyEncrypted: r.BodyEncrypted, KeyVersion: r.KeyVersion,
		CreatedAt: r.CreatedAt, DeletedAt: r.DeletedAt, DeletedBy: r.DeletedBy, Kind: r.Kind, PhotoUploadID: r.PhotoUploadID,
	}, m, s.now())
}

// notFoundForReport hides why a reporter may not read a message: not a
// member, outside the channel's audience, no such channel. Failures (core
// unreachable) stay as they are.
func notFoundForReport(err error) error {
	switch {
	case errors.Is(err, apperr.ErrChannelNotFound), errors.Is(err, apperr.ErrTayfaNotFound),
		errors.Is(err, apperr.ErrPermissionDenied), errors.Is(err, apperr.ErrNotTayfaMember):
		return apperr.ErrMessageNotFound
	}
	return err
}

// MessageRemoved reports whether a message of tayfa is no longer visible:
// deleted (by its author or hidden by moderation) or gone for good. A
// message of another tayfa counts as gone. Only core's service token
// reaches it (ReportedMessageService); the answer is one flag.
func (s *Service) MessageRemoved(ctx context.Context, tayfa, message id.ID) (bool, error) {
	r, err := store.New(s.cfg.Pool).GetChannelMessageForReport(ctx, message.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("şikâyet edilen mesaj: %w", err)
	}
	return id.ID(r.TayfaID.Bytes) != tayfa || r.DeletedAt.Valid, nil
}

// HideReported hides a reported message on the platform's decision
// (report.channel_message_removed from core): the moderator's path, so the
// tayfa's admins may restore it within RestoreWindow, recorded with the
// platform administrator as actor. A message already deleted or gone, or of
// another tayfa, changes nothing (changed is false). It runs in the
// caller's transaction; the caller tells connected clients.
func HideReported(ctx context.Context, tx pgx.Tx, tayfa, message, actor id.ID, now time.Time) (ch id.ID, changed bool, err error) {
	q := store.New(tx)
	r, err := q.GetChannelMessageForReport(ctx, message.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return id.ID{}, false, nil
	case err != nil:
		return id.ID{}, false, fmt.Errorf("şikâyet edilen mesaj: %w", err)
	case id.ID(r.TayfaID.Bytes) != tayfa || r.DeletedAt.Valid:
		return id.ID{}, false, nil
	}
	ch = id.ID(r.ChannelID.Bytes)
	n, err := q.HideMessage(ctx, store.HideMessageParams{DeletedAt: ts(now.UTC()), DeletedBy: actor.PG(), ChannelID: ch.PG(), ID: message.PG()})
	if err != nil {
		return id.ID{}, false, fmt.Errorf("mesaj gizleme: %w", err)
	}
	if n != 1 {
		return id.ID{}, false, nil
	}
	if err := record(ctx, tx, EventMessageDeleted, actor, "channel_message", message, map[string]any{
		"tayfa_id": tayfa.String(), "channel_id": ch.String(), "restorable": true, "platform": true,
	}); err != nil {
		return id.ID{}, false, err
	}
	return ch, true, nil
}

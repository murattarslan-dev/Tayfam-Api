package report

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Channel message reports (issue #205, ADR-0031; platform owner,
// 2026-11-16). Channel messages are chat's (ADR-0009): core keeps no copy of
// channels or their audiences (ADR-0024). The rules are those of every
// content report (one report per reporter and target, the daily limit,
// the priority, the tayfa's queue, escalation, content_removed tied to an
// action taken with the content's own methods), with chat answering for
// the message:
//
//   - Filing: core asks chat with the reporter's own token
//     (ChannelService.VerifyChannelMessageForReport). chat answers only when
//     the reporter can read the message's channel (the channel's audience
//     included) and the message is of the tayfa and not deleted; anything
//     else is apperr.ErrReportTargetNotFound, so nobody reports, or learns
//     about, a message outside their audience. The reporter's own message
//     is a validation error.
//   - What was reported: chat's answer (kind, text or place or photo ID,
//     channel) is kept on the report for the platform panel only. The
//     author's own deletion erases a channel message's text at once; the
//     report must not lose what it is about. It is nothing the reporter
//     could not read already, and core never reads other messages. It is
//     never logged, put in an event or returned to the reporter or the
//     tayfa's queue. The photo is not copied: the panel can open it while
//     the message still holds it.
//   - content_removed: core asks chat with its service token
//     (ReportedMessageService.GetChannelMessageStatus, one flag) whether the
//     message is deleted or hidden, the moderator's own path in chat.

// ChannelMessage is a reported channel message as chat answered it to the
// reporter.
type ChannelMessage struct {
	Channel id.ID
	Author  id.ID
	// Kind is "text", "photo" or "place".
	Kind      string
	Body      string
	Photo     id.ID
	Place     *Place
	CreatedAt time.Time
}

// Place is a shared place: content, not a device location (ADR-0008).
type Place struct {
	Latitude, Longitude float64
	Name                string
}

// Message kinds as chat names them.
const (
	messageText  = "text"
	messagePhoto = "photo"
	messagePlace = "place"
)

// ChannelMessages asks chat about a reported channel message with the
// reporter's own token. It returns apperr.ErrReportTargetNotFound when chat
// does not confirm the message for the caller, apperr.ErrUnavailable when
// chat cannot answer.
type ChannelMessages interface {
	ReportTarget(ctx context.Context, tayfa, message id.ID) (ChannelMessage, error)
}

// ChannelMessageStatus asks chat, with core's service token, whether a
// message of tayfa is removed (deleted, hidden or gone). It returns
// apperr.ErrUnavailable when chat cannot answer.
type ChannelMessageStatus interface {
	Removed(ctx context.Context, tayfa, message id.ID) (bool, error)
}

// channelMessage asks chat about the message and checks the answer.
func (s *Service) channelMessage(ctx context.Context, user, tayfa, message id.ID) (ChannelMessage, error) {
	if s.cfg.ChannelMessages == nil {
		return ChannelMessage{}, apperr.ErrUnavailable
	}
	m, err := s.cfg.ChannelMessages.ReportTarget(ctx, tayfa, message)
	if err != nil {
		return ChannelMessage{}, err
	}
	if m.Author == user {
		return ChannelMessage{}, apperr.Validation("target_id")
	}
	if !m.valid() {
		// Never the values: they are content.
		return ChannelMessage{}, errors.New("chat'in şikâyet cevabı geçersiz")
	}
	return m, nil
}

// valid checks chat's answer against what the report may keep (the same
// CHECKs are in the database).
func (m ChannelMessage) valid() bool {
	if m.Channel == (id.ID{}) || m.Author == (id.ID{}) {
		return false
	}
	switch m.Kind {
	case messageText:
		return m.Body != "" && m.Photo == (id.ID{}) && m.Place == nil
	case messagePhoto:
		return m.Body == "" && m.Photo != (id.ID{}) && m.Place == nil
	case messagePlace:
		p := m.Place
		return m.Body == "" && m.Photo == (id.ID{}) && p != nil &&
			p.Latitude >= -90 && p.Latitude <= 90 && p.Longitude >= -180 && p.Longitude <= 180
	}
	return false
}

// params writes the message into a new report.
func (m ChannelMessage) params(p *store.CreateReportParams) {
	p.ChannelID = m.Channel.PG()
	p.MessageKind = pgtype.Text{String: m.Kind, Valid: true}
	switch m.Kind {
	case messageText:
		p.MessageBody = pgtype.Text{String: m.Body, Valid: true}
	case messagePhoto:
		p.MessagePhotoUploadID = m.Photo.PG()
	case messagePlace:
		p.PlaceLatitude = pgtype.Float8{Float64: m.Place.Latitude, Valid: true}
		p.PlaceLongitude = pgtype.Float8{Float64: m.Place.Longitude, Valid: true}
		if m.Place.Name != "" {
			p.PlaceName = pgtype.Text{String: m.Place.Name, Valid: true}
		}
	}
}

// channelRemovedForResolve asks chat, before Resolve locks anything,
// whether a reported channel message is removed. It returns nil when the
// resolution or the report does not need it (Resolve checks the report
// again under lock).
func (s *Service) channelRemovedForResolve(ctx context.Context, user, tayfa, report id.ID, r Resolution) (*bool, error) {
	if r != ResolutionContentRemoved {
		return nil, nil
	}
	row, err := s.q.GetTayfaReportTarget(ctx, store.GetTayfaReportTargetParams{TayfaID: tayfa.PG(), ID: report.PG(), ViewerID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil // Resolve answers
	case err != nil:
		return nil, fmt.Errorf("şikâyet: %w", err)
	case TargetType(row.TargetType) != TargetChannelMessage:
		return nil, nil
	}
	if s.cfg.ChannelStatus == nil {
		return nil, apperr.ErrUnavailable
	}
	removed, err := s.cfg.ChannelStatus.Removed(ctx, tayfa, id.ID(row.TargetID.Bytes))
	if err != nil {
		return nil, err
	}
	return &removed, nil
}

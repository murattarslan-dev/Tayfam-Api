package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MediaURLs returns GET links for the READY uploads among ids that viewer
// may see, in request order (MediaService.GetMediaUrls). The rest are left
// out without a reason: unknown, not ready, deleted and not visible are
// the same.
func (s *Service) MediaURLs(ctx context.Context, viewer id.ID, ids []id.ID) ([]URLs, error) {
	if !s.Enabled() {
		return nil, apperr.ErrUnavailable
	}
	if len(ids) == 0 || len(ids) > MaxURLRequest {
		return nil, apperr.Validation("upload_ids")
	}
	seen := make(map[id.ID]bool, len(ids))
	pg := make([]pgtype.UUID, 0, len(ids))
	for _, u := range ids {
		if seen[u] {
			return nil, apperr.Validation("upload_ids")
		}
		seen[u] = true
		pg = append(pg, u.PG())
	}
	rows, err := s.q.ListMediaForViewer(ctx, store.ListMediaForViewerParams{ViewerID: viewer.PG(), Ids: pg})
	if err != nil {
		return nil, fmt.Errorf("medya: %w", err)
	}
	now := s.now()
	visible := make(map[id.ID]store.ListMediaForViewerRow, len(rows))
	var chat []MessageMediaItem
	chatRows := make(map[id.ID]store.ListMediaForViewerRow)
	for _, r := range rows {
		if it, ok := messageItem(viewer, r); ok {
			chat = append(chat, it)
			chatRows[it.Upload] = r
			continue
		}
		ok, err := canView(viewer, r, now)
		if err != nil {
			return nil, err
		}
		if ok {
			visible[id.ID(r.ID.Bytes)] = r
		}
	}
	if len(chat) > 0 {
		seen, err := s.chatVisible(ctx, chat)
		if err != nil {
			// chat files are left out; the others are still served.
			s.cfg.Log.WarnContext(ctx, "sohbet dosyalarının görünürlüğü sorulamadı", slog.String("err", err.Error()))
		}
		for u := range seen {
			if r, ok := chatRows[u]; ok {
				visible[u] = r
			}
		}
	}
	out := make([]URLs, 0, len(visible))
	for _, u := range ids {
		r, ok := visible[u]
		if !ok {
			continue
		}
		links, err := s.urls(r.ID, r.R2Key, r.PreviewType, r.FullType, r.Width, r.Height)
		if err != nil {
			return nil, err
		}
		out = append(out, *links)
	}
	return out, nil
}

// MessageMediaItem is a file attached to one of chat's messages.
type MessageMediaItem struct {
	Upload, Message id.ID
	// Direct is true for a direct message, false for a channel message.
	Direct bool
}

// MessageMedia asks chat, with the caller's own token, which of the items
// the caller may see (chat MessageMediaService.FilterVisibleMessageMedia;
// ADR-0031): the channel's audience for a channel photo, the two parties
// for a direct message attachment. chat owns channels and conversations;
// core keeps no copy of either.
type MessageMedia interface {
	Visible(ctx context.Context, items []MessageMediaItem) (map[id.ID]bool, error)
}

// messageItem returns the chat question for a file attached to a chat
// message that is not the viewer's own (the owner always sees their own
// file, canView).
func messageItem(viewer id.ID, r store.ListMediaForViewerRow) (MessageMediaItem, bool) {
	if r.UserID.Valid && id.ID(r.UserID.Bytes) == viewer {
		return MessageMediaItem{}, false
	}
	if !r.AttachedType.Valid || !r.AttachedID.Valid {
		return MessageMediaItem{}, false
	}
	it := MessageMediaItem{Upload: id.ID(r.ID.Bytes), Message: id.ID(r.AttachedID.Bytes)}
	switch {
	case r.AttachedType.String == TargetChannelMessage && r.Purpose == PurposeChatPhoto:
		return it, true
	case r.AttachedType.String == TargetDMMessage && r.Purpose == PurposeDMAttachment:
		it.Direct = true
		return it, true
	}
	return MessageMediaItem{}, false
}

// chatVisible asks chat; without a chat client nothing is visible.
func (s *Service) chatVisible(ctx context.Context, items []MessageMediaItem) (map[id.ID]bool, error) {
	if s.cfg.Messages == nil {
		return nil, nil
	}
	return s.cfg.Messages.Visible(ctx, items)
}

// EvidenceLinkTTL is how long the panel's link to a report's evidence photo
// works.
const EvidenceLinkTTL = 5 * time.Minute

// ReportEvidenceURL returns a GET link (EvidenceLinkTTL) to the full
// variant of the evidence photo attached to report, for the platform panel
// only (admin.Service; issue #205). "" when there is none, it is not READY
// or object storage is off.
func (s *Service) ReportEvidenceURL(ctx context.Context, report, upload id.ID) (string, error) {
	if !s.Enabled() {
		return "", nil
	}
	r, err := s.q.GetReportEvidenceUpload(ctx, store.GetReportEvidenceUploadParams{ID: upload.PG(), ReportID: report.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("kanıt fotoğrafı: %w", err)
	}
	link, err := s.cfg.Store.PresignGet(mediaKey(r.R2Key, KindFull), EvidenceLinkTTL)
	if err != nil {
		return "", fmt.Errorf("kanıt linki: %w", err)
	}
	return link.URL, nil
}

// canView is the visibility rule of ADR-0020 item 4. Tayfa content is
// decided by core's permission table (authz): TayfaView for a tayfa cover,
// EventView (with the events module on) for an event cover or a catch
// photo. The default is no.
func canView(viewer id.ID, r store.ListMediaForViewerRow, now time.Time) (bool, error) {
	if r.UserID.Valid && id.ID(r.UserID.Bytes) == viewer {
		return true, nil
	}
	if !r.AttachedType.Valid {
		return false, nil
	}
	switch r.AttachedType.String {
	case TargetUser:
		return !r.Blocked && !r.OwnerDeleted, nil
	case TargetTayfa, TargetEvent, TargetFishingCatch:
		if !r.ContentTayfaID.Valid || !r.MemberStatus.Valid {
			return false, nil
		}
		m, err := access.Membership(r.MemberRole.String, r.MemberStatus.String, r.MemberMutedUntil)
		if err != nil {
			return false, err
		}
		if m.Status != authz.StatusActive {
			return false, nil
		}
		if r.AttachedType.String == TargetTayfa {
			return authz.Allowed(m, authz.TayfaView, now), nil
		}
		return r.EventsModuleOn && authz.Allowed(m, authz.EventView, now), nil
	}
	return false, nil
}

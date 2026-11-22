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
// only (admin.Service; issue #205); ok is false when there is none, it is
// not READY or object storage is off.
func (s *Service) ReportEvidenceURL(ctx context.Context, report, upload id.ID) (link string, expires time.Time, ok bool, err error) {
	if !s.Enabled() {
		return "", time.Time{}, false, nil
	}
	r, err := s.q.GetReportEvidenceUpload(ctx, store.GetReportEvidenceUploadParams{ID: upload.PG(), ReportID: report.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, fmt.Errorf("kanıt fotoğrafı: %w", err)
	}
	p, err := s.cfg.Store.PresignGet(mediaKey(r.R2Key, KindFull), EvidenceLinkTTL)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("kanıt linki: %w", err)
	}
	return p.URL, p.Expires.UTC(), true, nil
}

// canView is the visibility rule of ADR-0020 item 4. Tayfa content is
// decided by core's permission table (authz): TayfaView for a tayfa cover,
// EventView (with the events module on) for an event cover, a log entry photo
// or an album photo (ADR-0028), TayfaView with the library module on for a
// library item's photo (ADR-0040 §1) and with the question_pool module on
// for a question pool question's or answer's photo (ADR-0040 §2), and with
// the materials module on for a material's note photo (ADR-0040 §3). The
// default is no.
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
	case TargetTayfa, TargetEvent, TargetLogEntry, TargetEventPhoto, TargetLibraryItem, TargetQuestion, TargetAnswer, TargetMaterial:
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
		if r.AttachedType.String == TargetLibraryItem {
			// A library item's photo (issue #270): the library module on.
			return r.LibraryModuleOn && authz.Allowed(m, authz.TayfaView, now), nil
		}
		if r.AttachedType.String == TargetQuestion || r.AttachedType.String == TargetAnswer {
			// A question pool photo (issue #271): the module on.
			return r.QuestionPoolModuleOn && authz.Allowed(m, authz.TayfaView, now), nil
		}
		if r.AttachedType.String == TargetMaterial {
			// A material's note photo (issue #261): the materials module on.
			return r.MaterialsModuleOn && authz.Allowed(m, authz.TayfaView, now), nil
		}
		return r.EventsModuleOn && authz.Allowed(m, authz.EventView, now), nil
	}
	return false, nil
}

// ReportedPhotoLinkTTL is how long the panel's link to a reported album
// photo works (ADR-0028 §5).
const ReportedPhotoLinkTTL = 5 * time.Minute

// ReportedMessagePhotoURL signs a short GET link to a reported channel
// message's photo for the platform panel (ADR-0031): only the upload the
// report recorded, still a ready chat_photo attached to that message.
// Gone (the author deleted the message, or it was purged) is ok = false.
func (s *Service) ReportedMessagePhotoURL(ctx context.Context, message, upload id.ID) (link string, expires time.Time, ok bool, err error) {
	if !s.Enabled() {
		return "", time.Time{}, false, nil
	}
	key, err := s.q.GetReportedMessagePhotoUpload(ctx, store.GetReportedMessagePhotoUploadParams{ID: upload.PG(), MessageID: message.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, fmt.Errorf("şikâyet edilen mesaj fotoğrafı: %w", err)
	}
	p, err := s.cfg.Store.PresignGet(mediaKey(key, KindFull), ReportedPhotoLinkTTL)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("şikâyet edilen mesaj fotoğrafı linki: %w", err)
	}
	return p.URL, p.Expires.UTC(), true, nil
}

// ReportedPhotoURL signs a short GET link to the full variant of a READY
// album photo upload for the platform panel. Only the admin service calls
// it, with the upload ID it read from a report escalated to the platform
// (an unreported photo has no way in); it records the access. ok is false
// without object storage or when the file is gone or not an album photo.
func (s *Service) ReportedPhotoURL(ctx context.Context, upload id.ID) (link string, expires time.Time, ok bool, err error) {
	if !s.Enabled() {
		return "", time.Time{}, false, nil
	}
	r, err := s.q.GetReportedPhotoUpload(ctx, upload.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, fmt.Errorf("şikâyet edilen fotoğraf: %w", err)
	}
	p, err := s.cfg.Store.PresignGet(mediaKey(r.R2Key, KindFull), ReportedPhotoLinkTTL)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("şikâyet edilen fotoğraf linki: %w", err)
	}
	return p.URL, p.Expires.UTC(), true, nil
}

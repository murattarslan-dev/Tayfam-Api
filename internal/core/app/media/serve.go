package media

import (
	"context"
	"fmt"
	"time"

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
	for _, r := range rows {
		ok, err := canView(viewer, r, now)
		if err != nil {
			return nil, err
		}
		if ok {
			visible[id.ID(r.ID.Bytes)] = r
		}
	}
	out := make([]URLs, 0, len(visible))
	for _, u := range ids {
		r, ok := visible[u]
		if !ok {
			continue
		}
		links, err := s.urls(r.ID, r.R2Key, r.OutputType, r.Width, r.Height)
		if err != nil {
			return nil, err
		}
		out = append(out, *links)
	}
	return out, nil
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

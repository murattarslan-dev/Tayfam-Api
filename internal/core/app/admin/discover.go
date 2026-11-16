package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Keşfet review (issue #210, ADR-0033 §8). The panel reviews metadata only
// (name, description, city, district) and sees counts, never who answered
// a feedback question or the tayfa's content.

// FeedbackWindow is how far back the queue counts negative feedback.
const FeedbackWindow = 90 * 24 * time.Hour

// DiscoverEntry is a tayfa waiting for a Keşfet decision.
type DiscoverEntry struct {
	TayfaID             id.ID
	Name                string
	Description         string
	City, District      string
	CreatedAt           time.Time
	Enabled             bool
	Status              string // pending, dropped
	DropReason          string // member_report, negative_feedback; "" unless dropped
	RequestedAt         time.Time
	StatusChangedAt     time.Time
	MemberCount         int
	DiscoverMemberCount int
	OpenPlatformReports int
	NotOKFeedback       int
	SeriousFeedback     int
}

// ListDiscoverQueue lists PENDING and DROPPED applications, oldest first.
func (s *Service) ListDiscoverQueue(ctx context.Context, c auth.AdminClaims) ([]DiscoverEntry, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.q.AdminListDiscoverQueue(ctx, ts(s.cfg.Now().UTC().Add(-FeedbackWindow)))
	if err != nil {
		return nil, fmt.Errorf("keşfet kuyruğu: %w", err)
	}
	out := make([]DiscoverEntry, len(rows))
	for i, r := range rows {
		out[i] = DiscoverEntry{
			TayfaID: idOf(r.ID), Name: r.Name, Description: r.Description.String, City: r.City.String, District: r.District.String,
			CreatedAt: timeOf(r.CreatedAt), Enabled: r.Visibility == "listed", Status: r.Status, DropReason: r.DropReason.String,
			RequestedAt: timeOf(r.RequestedAt), StatusChangedAt: timeOf(r.StatusChangedAt),
			MemberCount: int(r.MemberCount), DiscoverMemberCount: int(r.DiscoverMemberCount),
			OpenPlatformReports: int(r.OpenPlatformReports), NotOKFeedback: int(r.NotOkCount), SeriousFeedback: int(r.SeriousCount),
		}
	}
	return out, nil
}

// ApproveDiscover approves a PENDING or DROPPED application.
func (s *Service) ApproveDiscover(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string) error {
	return s.reviewDiscover(ctx, c, tayfa, reason, true)
}

// RejectDiscover declines an application or takes a tayfa out of Keşfet.
func (s *Service) RejectDiscover(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string) error {
	return s.reviewDiscover(ctx, c, tayfa, reason, false)
}

// reviewDiscover writes the decision, the tayfa's audit record (without the
// administrator) and the platform audit entry in one transaction.
func (s *Service) reviewDiscover(ctx context.Context, c auth.AdminClaims, tayfa id.ID, reason string, approve bool) error {
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		t, err := lockTayfa(ctx, q, tayfa)
		if err != nil {
			return err
		}
		if t.DeletedAt.Valid {
			return apperr.ErrTayfaNotFound
		}
		if _, err := q.AdminLockTayfaDiscover(ctx, tayfa.PG()); err != nil {
			return notFound(err, apperr.ErrTayfaStateConflict)
		}
		now := ts(s.cfg.Now().UTC().Truncate(time.Microsecond))
		var n int64
		action, record := "tayfa.discover_reject", "platform.discover_rejected"
		if approve {
			action, record = "tayfa.discover_approve", "platform.discover_approved"
			n, err = q.AdminApproveDiscover(ctx, store.AdminApproveDiscoverParams{TayfaID: tayfa.PG(), Now: now, By: cl.User.PG()})
		} else {
			n, err = q.AdminRejectDiscover(ctx, store.AdminRejectDiscoverParams{TayfaID: tayfa.PG(), Now: now, By: cl.User.PG()})
		}
		if err != nil {
			return fmt.Errorf("keşfet kararı: %w", err)
		}
		if n != 1 {
			return apperr.ErrTayfaStateConflict
		}
		if err := tayfaRecord(ctx, q, tayfa, record, txn.Target{}); err != nil {
			return err
		}
		return audit(ctx, q, cl, tayfa, action, "tayfa", tayfa, reason, nil)
	})
}

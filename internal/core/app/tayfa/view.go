package tayfa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// viewRow is what views needs from a tayfa row and the caller's membership.
type viewRow struct {
	ID                   id.ID
	Name                 string
	Description          pgtype.Text
	JoinCode             string
	Status               string
	JoinPolicy           string
	SuspendedAt          pgtype.Timestamptz
	DeletionScheduledFor pgtype.Timestamptz
	// MembersCanCreateEvents is tayfas.members_can_create_events.
	MembersCanCreateEvents bool
	CreatedAt              pgtype.Timestamptz
	UpdatedAt              pgtype.Timestamptz
	CoverUploadID          pgtype.UUID
	// Kind is tayfas.kind (ADR-0032).
	Kind   string
	Member authz.Membership
}

func (r viewRow) settings() authz.Settings {
	return authz.Settings{MembersCanCreateEvents: r.MembersCanCreateEvents, Instant: r.Kind == access.KindInstant}
}

func fromTayfa(t access.Tayfa, m authz.Membership) viewRow {
	return viewRow{
		ID: id.ID(t.ID.Bytes), Name: t.Name, Description: t.Description, JoinCode: t.JoinCode,
		Status: t.Status, JoinPolicy: t.JoinPolicy, SuspendedAt: t.SuspendedAt,
		DeletionScheduledFor: t.DeletionScheduledFor, MembersCanCreateEvents: t.MembersCanCreateEvents,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, CoverUploadID: t.CoverUploadID, Kind: t.Kind, Member: m,
	}
}

// views builds the full views of rows with batched queries. The join code
// is filled only for callers allowed to rotate it, the pending join request
// count only for callers allowed to approve requests.
func (s *Service) views(ctx context.Context, rows []viewRow) ([]View, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]pgtype.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID.PG()
	}
	interests, err := s.q.ListInterestsForTayfas(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("ilgi alanları: %w", err)
	}
	modules, err := s.q.ListModulesForTayfas(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("modüller: %w", err)
	}
	counts, err := s.q.CountActiveMembersForTayfas(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("üye sayısı: %w", err)
	}

	byInterest := map[id.ID]map[string]bool{}
	for _, r := range interests {
		k := id.ID(r.TayfaID.Bytes)
		if byInterest[k] == nil {
			byInterest[k] = map[string]bool{}
		}
		byInterest[k][r.EventType] = true
	}
	byModule := map[id.ID]map[string]bool{}
	for _, r := range modules {
		k := id.ID(r.TayfaID.Bytes)
		if byModule[k] == nil {
			byModule[k] = map[string]bool{}
		}
		byModule[k][r.Module] = r.Enabled
	}
	byCount := map[id.ID]int64{}
	for _, r := range counts {
		byCount[id.ID(r.TayfaID.Bytes)] = r.MemberCount
	}

	now := s.now()
	// Pending requests are counted only for the tayfas whose count the
	// caller may see.
	var approvable []pgtype.UUID
	for _, r := range rows {
		if authz.Allowed(r.Member, authz.MemberApprove, now) {
			approvable = append(approvable, r.ID.PG())
		}
	}
	// The single event of each instant tayfa.
	var instant []pgtype.UUID
	for _, r := range rows {
		if r.Kind == access.KindInstant {
			instant = append(instant, r.ID.PG())
		}
	}
	byEvent := map[id.ID]id.ID{}
	if len(instant) > 0 {
		evs, err := s.q.ListInstantEventIDs(ctx, instant)
		if err != nil {
			return nil, fmt.Errorf("anlık etkinlik: %w", err)
		}
		for _, e := range evs {
			byEvent[id.ID(e.TayfaID.Bytes)] = id.ID(e.ID.Bytes)
		}
	}

	byPending := map[id.ID]int64{}
	if len(approvable) > 0 {
		pending, err := s.q.CountPendingMembersForTayfas(ctx, approvable)
		if err != nil {
			return nil, fmt.Errorf("bekleyen istekler: %w", err)
		}
		for _, r := range pending {
			byPending[id.ID(r.TayfaID.Bytes)] = r.PendingCount
		}
	}

	out := make([]View, len(rows))
	for i, r := range rows {
		v := View{
			ID: r.ID, Name: r.Name, Description: r.Description.String,
			JoinPolicy: r.JoinPolicy, Status: r.Status, MemberCount: byCount[r.ID],
			MembersCanCreateEvents: r.MembersCanCreateEvents,
			CreatedAt:              r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
			Interests: []string{}, Kind: r.Kind, InstantEventID: byEvent[r.ID],
		}
		if r.CoverUploadID.Valid {
			v.CoverUploadID = id.ID(r.CoverUploadID.Bytes)
		}
		if r.SuspendedAt.Valid {
			v.SuspendedAt = r.SuspendedAt.Time.UTC()
		}
		if r.DeletionScheduledFor.Valid {
			v.DeletionAt = r.DeletionScheduledFor.Time.UTC()
		}
		for _, e := range Interests {
			if byInterest[r.ID][e] {
				v.Interests = append(v.Interests, e)
			}
		}
		// A module without a row counts as off: the safe default.
		for _, m := range Modules {
			v.Modules = append(v.Modules, ModuleState{Module: m, Enabled: byModule[r.ID][m]})
		}
		if authz.AllowedIn(r.Member, authz.TayfaRotateJoinCode, now, r.settings()) {
			v.JoinCode = r.JoinCode
		}
		if authz.Allowed(r.Member, authz.MemberApprove, now) {
			n := byPending[r.ID]
			v.PendingJoinRequests = &n
		}
		out[i] = v
	}
	return out, nil
}

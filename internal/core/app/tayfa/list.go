package tayfa

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (membership.proto "Paging").
const (
	defaultPageSize = 50
	maxPageSize     = 100
	// myTayfasToken prefixes ListMyTayfas page tokens, so a token of another
	// method is refused.
	myTayfasToken = "my1"
)

// MyTayfa is one entry of the caller's list.
type MyTayfa struct {
	TayfaID id.ID
	Name    string
	Role    authz.Role
	// Status is StatusActive or StatusPending.
	Status authz.Status
	// Tayfa is set only for active memberships.
	Tayfa *View
}

// ListMine lists the caller's active memberships and pending join requests,
// most recent membership first. The returned token is empty on the last
// page.
func (s *Service) ListMine(ctx context.Context, user id.ID, pageSize int32, pageToken string) ([]MyTayfa, string, error) {
	switch {
	case pageSize < 0:
		return nil, "", apperr.Validation("page_size")
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	before, beforeID, err := decodeToken(pageToken)
	if err != nil {
		return nil, "", err
	}

	rows, err := s.q.ListMyTayfas(ctx, store.ListMyTayfasParams{
		UserID: user.PG(), BeforeCreatedAt: before, BeforeTayfaID: beforeID, PageSize: pageSize + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("tayfalarım: %w", err)
	}
	next := ""
	if len(rows) > int(pageSize) {
		rows = rows[:pageSize]
		last := rows[len(rows)-1]
		next = encodeToken(last.MembershipCreatedAt.Time, id.ID(last.ID.Bytes))
	}

	out := make([]MyTayfa, len(rows))
	var active []viewRow
	var activeIdx []int
	for i, r := range rows {
		m, err := access.Membership(r.Role, r.MembershipStatus, r.MutedUntil)
		if err != nil {
			return nil, "", err
		}
		out[i] = MyTayfa{TayfaID: id.ID(r.ID.Bytes), Name: r.Name, Role: m.Role, Status: m.Status}
		if m.Status == authz.StatusActive {
			active = append(active, viewRow{
				ID: id.ID(r.ID.Bytes), Name: r.Name, Description: r.Description, JoinCode: r.JoinCode,
				Status: r.Status, JoinPolicy: r.JoinPolicy, SuspendedAt: r.SuspendedAt,
				DeletionScheduledFor: r.DeletionScheduledFor, MembersCanCreateEvents: r.MembersCanCreateEvents,
				CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CoverUploadID: r.CoverUploadID, Member: m,
			})
			activeIdx = append(activeIdx, i)
		}
	}
	views, err := s.views(ctx, active)
	if err != nil {
		return nil, "", err
	}
	for j, i := range activeIdx {
		out[i].Tayfa = &views[j]
	}
	return out, next, nil
}

// Page tokens are "my1.<unix micro>.<tayfa id>" in unpadded base64url: the
// position of the last entry. They carry no user ID; the query is always
// scoped to the caller.
func encodeToken(t time.Time, tayfa id.ID) string {
	raw := myTayfasToken + "." + strconv.FormatInt(t.UnixMicro(), 10) + "." + tayfa.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeToken(tok string) (pgtype.Timestamptz, pgtype.UUID, error) {
	if tok == "" {
		var maxID id.ID
		for i := range maxID {
			maxID[i] = 0xff
		}
		return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}, maxID.PG(), nil
	}
	bad := apperr.Validation("page_token")
	if len(tok) > 128 {
		return pgtype.Timestamptz{}, pgtype.UUID{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, bad
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || parts[0] != myTayfasToken {
		return pgtype.Timestamptz{}, pgtype.UUID{}, bad
	}
	micro, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || micro <= 0 {
		return pgtype.Timestamptz{}, pgtype.UUID{}, bad
	}
	tid, ok := id.Parse(parts[2])
	if !ok {
		return pgtype.Timestamptz{}, pgtype.UUID{}, bad
	}
	return pgtype.Timestamptz{Time: time.UnixMicro(micro).UTC(), Valid: true}, tid.PG(), nil
}

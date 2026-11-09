package announcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (announcement.proto): page_size 0 means 20, above 100 means 100,
// negative is a validation error.
const (
	defaultPageSize = 20
	maxPageSize     = 100
	cursorMethod    = "announcements"
)

// cursor is the position after the last row of a page; the page token is
// its JSON form in base64url. It carries the tayfa it was made for, and
// nothing the caller could not already see.
type cursor struct {
	Method string `json:"m"`
	Tayfa  string `json:"t"`
	Pinned bool   `json:"p"`
	// Micros is created_at in Unix microseconds (Postgres precision).
	Micros int64  `json:"c"`
	ID     string `json:"i"`
}

// List returns visible announcements, pinned first, then newest first.
func (s *Service) List(ctx context.Context, user, tayfa id.ID, pageSize int32, pageToken string) ([]View, string, error) {
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	p := store.ListAnnouncementsPageParams{TayfaID: tayfa.PG(), PageSize: size}
	if pageToken != "" {
		c, after, ok := decodeCursor(pageToken, tayfa)
		if !ok {
			return nil, "", apperr.Validation("page_token")
		}
		p.HasCursor, p.AfterPinned, p.AfterID = true, c.Pinned, after.PG()
		p.AfterCreatedAt = pgtype.Timestamptz{Time: time.UnixMicro(c.Micros).UTC(), Valid: true}
	}
	if err := s.read(ctx, user, tayfa); err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListAnnouncementsPage(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("duyurular: %w", err)
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		out = append(out, view(r))
	}
	next := ""
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		next = cursor{
			Method: cursorMethod, Tayfa: tayfa.String(), Pinned: last.Pinned,
			Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.ID.Bytes).String(),
		}.encode()
	}
	return out, next, nil
}

func normalizePageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses a token made by List for the same tayfa.
func decodeCursor(token string, tayfa id.ID) (cursor, id.ID, bool) {
	if len(token) > 512 {
		return cursor{}, id.ID{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return cursor{}, id.ID{}, false
	}
	after, ok := id.Parse(c.ID)
	if !ok || c.Method != cursorMethod || c.Tayfa != tayfa.String() {
		return cursor{}, id.ID{}, false
	}
	return c, after, true
}

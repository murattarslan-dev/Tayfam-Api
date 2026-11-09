package report

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (report.proto): page_size 0 means 20, above 100 means 100,
// negative is a validation error.
const (
	defaultPageSize = 20
	maxPageSize     = 100
	methodMine      = "my_reports"
	methodOpen      = "reports_open"
	methodResolved  = "reports_resolved"
)

// cursor is the position after the last row of a page; the page token is
// its JSON form in base64url. It names the list (method and scope: the
// user or the tayfa) it was made for and holds nothing the caller could not
// already see.
type cursor struct {
	Method string `json:"m"`
	Scope  string `json:"s"`
	// Urgent: the last row was MinorInvolved or a child safety report (open
	// reports only).
	Urgent bool `json:"u,omitempty"`
	// Micros is created_at (handled_at for resolved reports) in Unix
	// microseconds (Postgres precision).
	Micros int64  `json:"c"`
	ID     string `json:"i"`
}

// ListMine returns user's own reports, newest first.
func (s *Service) ListMine(ctx context.Context, user id.ID, pageSize int32, pageToken string) ([]MyView, string, error) {
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	p := store.ListReportsOfReporterParams{ReporterID: user.PG(), PageSize: size}
	if pageToken != "" {
		c, after, ok := decodeCursor(pageToken, methodMine, user)
		if !ok {
			return nil, "", apperr.Validation("page_token")
		}
		p.HasCursor, p.AfterID, p.AfterCreatedAt = true, after.PG(), micros(c.Micros)
	}
	rows, err := s.q.ListReportsOfReporter(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("şikâyetler: %w", err)
	}
	out := make([]MyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, myView(r))
	}
	next := ""
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		next = cursor{Method: methodMine, Scope: user.String(), Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.ID.Bytes).String()}.encode()
	}
	return out, next, nil
}

// ListQueue returns tayfa's moderation queue for user: open reports
// (MinorInvolved and child safety first, then oldest first) or resolved ones (last handled
// first). Platform-only reports and reports about user are never listed.
func (s *Service) ListQueue(ctx context.Context, user, tayfa id.ID, status Status, pageSize int32, pageToken string) ([]ModView, string, error) {
	if status == "" {
		status = StatusOpen
	}
	if status != StatusOpen && status != StatusResolved {
		return nil, "", apperr.Validation("status")
	}
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	method := methodOpen
	if status == StatusResolved {
		method = methodResolved
	}
	var (
		c     cursor
		after id.ID
		has   bool
	)
	if pageToken != "" {
		var ok bool
		if c, after, ok = decodeCursor(pageToken, method, tayfa); !ok {
			return nil, "", apperr.Validation("page_token")
		}
		has = true
	}
	_, m, err := access.Load(ctx, s.q, tayfa, user, false)
	if err != nil {
		return nil, "", err
	}
	if err := access.Authorize(m, authz.ReportQueueView, s.now()); err != nil {
		return nil, "", err
	}

	var rows []store.ListOpenTayfaReportsRow
	if status == StatusOpen {
		p := store.ListOpenTayfaReportsParams{TayfaID: tayfa.PG(), ViewerID: user.PG(), PageSize: size}
		if has {
			p.HasCursor, p.AfterID, p.AfterCreatedAt = true, after.PG(), micros(c.Micros)
			if c.Urgent {
				p.AfterUrgent = 1
			}
		}
		rows, err = s.q.ListOpenTayfaReports(ctx, p)
	} else {
		p := store.ListResolvedTayfaReportsParams{TayfaID: tayfa.PG(), ViewerID: user.PG(), PageSize: size}
		if has {
			p.HasCursor, p.AfterID, p.AfterHandledAt = true, after.PG(), micros(c.Micros)
		}
		var res []store.ListResolvedTayfaReportsRow
		res, err = s.q.ListResolvedTayfaReports(ctx, p)
		for _, r := range res {
			rows = append(rows, store.ListOpenTayfaReportsRow(r))
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("şikâyet kuyruğu: %w", err)
	}
	out := make([]ModView, 0, len(rows))
	for _, r := range rows {
		out = append(out, modView(r))
	}
	next := ""
	if len(rows) == int(size) {
		last := out[len(out)-1]
		nc := cursor{Method: method, Scope: tayfa.String(), ID: last.ID.String()}
		if status == StatusOpen {
			nc.Urgent, nc.Micros = last.urgent(), last.CreatedAt.UnixMicro()
		} else {
			nc.Micros = last.ResolvedAt.UnixMicro()
		}
		next = nc.encode()
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

// decodeCursor parses a token made for the same list and scope.
func decodeCursor(token, method string, scope id.ID) (cursor, id.ID, bool) {
	if len(token) > 512 {
		return cursor{}, id.ID{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return cursor{}, id.ID{}, false
	}
	after, ok := id.Parse(c.ID)
	if !ok || c.Method != method || c.Scope != scope.String() {
		return cursor{}, id.ID{}, false
	}
	return c, after, true
}

func micros(n int64) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.UnixMicro(n).UTC(), Valid: true}
}

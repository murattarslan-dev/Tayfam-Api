package platformadmin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/retention"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The panel's audit log page and dashboard counts (issue #110; Notion
// "Platform Yönetim Paneli → Pano", "Denetim kaydı").

// Paging of ListAuditLogs: 0 means 50, above 100 means 100.
const (
	defaultAuditPage = 50
	maxAuditPage     = 100
	maxPageToken     = 512
)

// ActiveUserWindow: a user is active with a session refreshed this recently.
const ActiveUserWindow = 30 * 24 * time.Hour

// deletionGrace: a deleted account still present this long after
// retention.DeletedUserRetention means the retention job is behind (it runs
// hourly).
const deletionGrace = 24 * time.Hour

// AuditFilter narrows ListAuditLogs; zero fields do not filter.
type AuditFilter struct {
	Action     string
	TargetType string
	// Related matches the entry's target or the tayfa, user or report ID in
	// its metadata.
	Related id.ID
	Since   time.Time
	Until   time.Time
}

// AuditEntry is one platform audit log entry as the panel shows it. The
// reason is shown on the panel only; it is never logged.
type AuditEntry struct {
	ID         id.ID
	CreatedAt  time.Time
	Actor      id.ID
	ActorName  string
	Action     string
	TargetType string
	TargetID   id.ID
	Reason     string
	Tayfa      id.ID
	User       id.ID
	Report     id.ID
}

// AuditPage is a page of entries and the token of the next one ("" on the
// last page).
type AuditPage struct {
	Entries []AuditEntry
	Next    string
}

// auditCursor is the position after the last entry of a page. The page
// token is its JSON in base64url; F binds it to the filters it was made
// for, so a token only continues the same list.
type auditCursor struct {
	F      string `json:"f"`
	Micros int64  `json:"c"`
	ID     string `json:"i"`
}

func (f AuditFilter) key() string {
	h := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%d\x00%d", f.Action, f.TargetType, f.Related, unixMicro(f.Since), unixMicro(f.Until)))
	return hex.EncodeToString(h[:8])
}

func unixMicro(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func (f AuditFilter) valid() bool {
	if f.Action != "" && !actionPattern.MatchString(f.Action) {
		return false
	}
	if f.TargetType != "" && !targetTypes[f.TargetType] {
		return false
	}
	return f.Since.IsZero() || f.Until.IsZero() || f.Since.Before(f.Until)
}

// ListAuditLogs lists platform audit entries newest first. The caller must
// still be an administrator.
func (s *Service) ListAuditLogs(ctx context.Context, c auth.AdminClaims, f AuditFilter, pageSize int, token string) (AuditPage, error) {
	if _, err := s.Get(ctx, c); err != nil {
		return AuditPage{}, err
	}
	switch {
	case pageSize < 0 || !f.valid():
		return AuditPage{}, ErrValidation
	case pageSize == 0:
		pageSize = defaultAuditPage
	case pageSize > maxAuditPage:
		pageSize = maxAuditPage
	}
	p := store.ListPlatformAuditLogsParams{N: int32(pageSize + 1)} // #nosec G115 -- at most maxAuditPage+1
	if f.Action != "" {
		p.Action = pgtype.Text{String: f.Action, Valid: true}
	}
	if f.TargetType != "" {
		p.TargetType = pgtype.Text{String: f.TargetType, Valid: true}
	}
	if f.Related != (id.ID{}) {
		p.RelatedID = f.Related.PG()
	}
	if !f.Since.IsZero() {
		p.Since = pgtype.Timestamptz{Time: f.Since.UTC(), Valid: true}
	}
	if !f.Until.IsZero() {
		p.Until = pgtype.Timestamptz{Time: f.Until.UTC(), Valid: true}
	}
	key := f.key()
	if token != "" {
		cur, ok := decodeAuditCursor(token, key)
		if !ok {
			return AuditPage{}, ErrValidation
		}
		after, _ := id.Parse(cur.ID)
		p.HasCursor = true
		p.AfterCreatedAt = pgtype.Timestamptz{Time: time.UnixMicro(cur.Micros).UTC(), Valid: true}
		p.AfterID = after.PG()
	}
	rows, err := s.q.ListPlatformAuditLogs(ctx, p)
	if err != nil {
		return AuditPage{}, fmt.Errorf("denetim kaydı: %w", err)
	}
	var out AuditPage
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[len(rows)-1]
		out.Next = encodeAuditCursor(auditCursor{F: key, Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.ID.Bytes).String()})
	}
	out.Entries = make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		e := AuditEntry{
			ID: id.ID(r.ID.Bytes), CreatedAt: r.CreatedAt.Time.UTC(), Actor: id.ID(r.ActorID.Bytes),
			ActorName: r.ActorName.String, Action: r.Action, TargetType: r.TargetType.String, Reason: r.Reason,
		}
		if r.TargetID.Valid {
			e.TargetID = id.ID(r.TargetID.Bytes)
		}
		// Metadata values were checked as IDs when written; anything else
		// is dropped rather than shown.
		e.Tayfa, _ = id.Parse(r.TayfaID)
		e.User, _ = id.Parse(r.UserID)
		e.Report, _ = id.Parse(r.ReportID)
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

func encodeAuditCursor(c auditCursor) string {
	b, _ := json.Marshal(c) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeAuditCursor(token, key string) (auditCursor, bool) {
	var c auditCursor
	if len(token) > maxPageToken {
		return c, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, &c) != nil || c.F != key {
		return c, false
	}
	if _, ok := id.Parse(c.ID); !ok {
		return c, false
	}
	return c, true
}

// UserStats are the dashboard's user counts. Counts only.
type UserStats struct {
	Total, Active, Suspended, Banned int64
	// Account deletions (KVKK erasure): pending removal for good, and
	// pending past the retention period (the retention job is behind).
	DeletionsPending, DeletionsOverdue int64
}

// UserStats returns the dashboard's user counts. The caller must still be
// an administrator.
func (s *Service) UserStats(ctx context.Context, c auth.AdminClaims) (UserStats, error) {
	if _, err := s.Get(ctx, c); err != nil {
		return UserStats{}, err
	}
	now := s.cfg.Now().UTC()
	r, err := s.q.AdminUserStats(ctx, store.AdminUserStatsParams{
		ActiveSince:   pgtype.Timestamptz{Time: now.Add(-ActiveUserWindow), Valid: true},
		OverdueBefore: pgtype.Timestamptz{Time: now.Add(-retention.DeletedUserRetention - deletionGrace), Valid: true},
	})
	if err != nil {
		return UserStats{}, fmt.Errorf("kullanıcı sayıları: %w", err)
	}
	return UserStats(r), nil
}

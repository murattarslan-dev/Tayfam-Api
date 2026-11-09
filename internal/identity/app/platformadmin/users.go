package platformadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// User management from the panel (issue #104 stage 2; Notion "Platform
// Yönetim Paneli → Kullanıcılar", "Kurallar"). The panel sees an account's
// state only: never the birth date, IP addresses, devices or tokens.

// Sanction types (platform_sanctions.type).
const (
	SanctionSuspend = "suspend"
	SanctionBan     = "ban"
)

// Suspension length limits.
const (
	MinSuspension = time.Hour
	MaxSuspension = 365 * 24 * time.Hour
)

// Session revoke reasons written by this package.
const (
	reasonAdminRevoked = "admin_revoked"
	reasonSanctioned   = "sanctioned"
)

var (
	// ErrUserNotFound: no such user, or the account is deleted (changes).
	ErrUserNotFound = errors.New("kullanıcı bulunamadı")
	// ErrSanctionConflict: SanctionUser on a user with an active sanction,
	// or LiftSanction on one without.
	ErrSanctionConflict = errors.New("yaptırım durumu uygun değil")
	// ErrValidation: a malformed field.
	ErrValidation = errors.New("geçersiz alan")
)

// UserSummary is a search result.
type UserSummary struct {
	ID             id.ID
	DisplayName    string
	RefCode        string
	Registered     bool
	Deleted        bool
	ActiveSanction string // "", suspend or ban
}

// Sanction is a platform sanction.
type Sanction struct {
	ID       id.ID
	Type     string
	Reason   string
	StartsAt time.Time
	EndsAt   time.Time // zero for a ban
}

// UserDetail is one user's account state.
type UserDetail struct {
	UserSummary
	CreatedAt      time.Time
	ActiveSessions int
	// Sanction is set while a sanction is active.
	Sanction *Sanction
}

const (
	minQuery = 2
	maxQuery = 100
)

// SearchUsers finds users by referral code (TYF-…), e-mail address (with
// @) or display name (substring). The caller must still be an
// administrator.
func (s *Service) SearchUsers(ctx context.Context, c auth.AdminClaims, query string) ([]UserSummary, error) {
	if _, err := s.Get(ctx, c); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if n := utf8.RuneCountInString(query); n < minQuery || n > maxQuery || !utf8.ValidString(query) {
		return nil, ErrValidation
	}
	type row struct {
		ID           pgtype.UUID
		DisplayName  pgtype.Text
		RefCode      pgtype.Text
		RegisteredAt pgtype.Timestamptz
		DeletedAt    pgtype.Timestamptz
	}
	var rows []row
	switch upper := strings.ToUpper(query); {
	case strings.HasPrefix(upper, "TYF-"):
		rs, err := s.q.SearchUsersByRefCode(ctx, pgtype.Text{String: upper, Valid: true})
		if err != nil {
			return nil, fmt.Errorf("arama: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row(r))
		}
	case strings.Contains(query, "@"):
		rs, err := s.q.SearchUsersByEmail(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("arama: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row(r))
		}
	default:
		rs, err := s.q.SearchUsersByName(ctx, likePattern(query))
		if err != nil {
			return nil, fmt.Errorf("arama: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row(r))
		}
	}
	ids := make([]pgtype.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	active := map[pgtype.UUID]string{}
	if len(ids) > 0 {
		sanctions, err := s.q.ListActiveSanctionTypes(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("yaptırımlar: %w", err)
		}
		for _, a := range sanctions {
			active[a.UserID] = a.Type
		}
	}
	out := make([]UserSummary, 0, len(rows))
	for _, r := range rows {
		u, _ := id.FromPG(r.ID)
		out = append(out, UserSummary{
			ID: u, DisplayName: r.DisplayName.String, RefCode: r.RefCode.String,
			Registered: r.RegisteredAt.Valid, Deleted: r.DeletedAt.Valid, ActiveSanction: active[r.ID],
		})
	}
	return out, nil
}

// likePattern lowercases q and escapes LIKE's wildcards. Display names are
// stored as typed; lower() in the query matches case-insensitively.
func likePattern(q string) string {
	q = strings.ToLower(q)
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// GetUser returns one user's account state.
func (s *Service) GetUser(ctx context.Context, c auth.AdminClaims, user id.ID) (UserDetail, error) {
	if _, err := s.Get(ctx, c); err != nil {
		return UserDetail{}, err
	}
	r, err := s.q.GetUserForAdmin(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return UserDetail{}, ErrUserNotFound
	}
	if err != nil {
		return UserDetail{}, fmt.Errorf("kullanıcı: %w", err)
	}
	d := UserDetail{
		UserSummary: UserSummary{
			ID: user, DisplayName: r.DisplayName.String, RefCode: r.RefCode.String,
			Registered: r.RegisteredAt.Valid, Deleted: r.DeletedAt.Valid,
		},
		CreatedAt: r.CreatedAt.Time.UTC(), ActiveSessions: int(r.ActiveSessions),
	}
	sn, err := s.q.GetOpenSanction(ctx, user.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return UserDetail{}, fmt.Errorf("yaptırım: %w", err)
	case !sn.EndsAt.Valid || sn.EndsAt.Time.After(s.cfg.Now()):
		d.Sanction = sanctionOf(sn.ID, sn.Type, sn.Reason, sn.StartsAt, sn.EndsAt)
		d.ActiveSanction = sn.Type
	}
	return d, nil
}

func sanctionOf(sid pgtype.UUID, typ, reason string, starts, ends pgtype.Timestamptz) *Sanction {
	i, _ := id.FromPG(sid)
	out := &Sanction{ID: i, Type: typ, Reason: reason, StartsAt: starts.Time.UTC()}
	if ends.Valid {
		out.EndsAt = ends.Time.UTC()
	}
	return out
}

// lockUser locks a live (not deleted) account for a change.
func lockUser(ctx context.Context, q *store.Queries, user id.ID) error {
	_, err := q.LockUserForAdmin(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("kullanıcı kilidi: %w", err)
	}
	return nil
}

// revokeAll ends every open session of user and returns their IDs.
func revokeAll(ctx context.Context, q *store.Queries, user id.ID, reason string) ([]id.ID, error) {
	rows, err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: user.PG(), Reason: pgtype.Text{String: reason, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("oturum kapatma: %w", err)
	}
	out := make([]id.ID, len(rows))
	for i, r := range rows {
		out[i], _ = id.FromPG(r)
	}
	return out, nil
}

// RevokeUserSessions ends every session of user (destructive; reason and a
// recent sign-in needed). Each ended session sends session.revoked, so chat
// closes its real-time connections.
func (s *Service) RevokeUserSessions(ctx context.Context, c auth.AdminClaims, user id.ID, reason string) (int, error) {
	var ended []id.ID
	err := s.Destructive(ctx, c, Entry{Action: "user.revoke_sessions", TargetType: "user", TargetID: user, Reason: reason}, Action{
		Do: func(ctx context.Context, q *store.Queries, e *Entry) error {
			if err := lockUser(ctx, q, user); err != nil {
				return err
			}
			var err error
			ended, err = revokeAll(ctx, q, user, reasonAdminRevoked)
			e.Metadata = map[string]string{"sessions": fmt.Sprint(len(ended))}
			return err
		},
		Events: func(ctx context.Context, tx pgx.Tx) error {
			return userevents.SessionsRevoked(ctx, tx, user, ended...)
		},
	})
	if err != nil {
		return 0, err
	}
	s.cfg.Log.WarnContext(ctx, "panelden oturumlar kapatıldı", slog.String("user_id", user.String()), slog.Int("sessions", len(ended)))
	return len(ended), nil
}

// SanctionInput is one platform sanction.
type SanctionInput struct {
	User   id.ID
	Type   string // SanctionSuspend or SanctionBan
	Reason string
	// EndsAt is required for a suspension and must be zero for a ban.
	EndsAt time.Time
	// Report is the platform report behind it, if any (audit metadata).
	Report id.ID
}

// SanctionUser suspends or bans a user platform-wide (Notion "Kurallar":
// every session ends, no new sign-in; a ban hides the user's content in
// core without deleting it). In one transaction: the sanction row, every
// session revoked, the audit entry, then session.revoked per session and
// user.sanctioned for chat and, for a ban, user.banned for core. A
// suspension whose time is over but was never lifted is lifted first.
func (s *Service) SanctionUser(ctx context.Context, c auth.AdminClaims, in SanctionInput) (Sanction, error) {
	now := s.cfg.Now().UTC().Truncate(time.Second)
	switch in.Type {
	case SanctionBan:
		if !in.EndsAt.IsZero() {
			return Sanction{}, ErrValidation
		}
	case SanctionSuspend:
		if d := in.EndsAt.Sub(now); d < MinSuspension || d > MaxSuspension {
			return Sanction{}, ErrValidation
		}
	default:
		return Sanction{}, ErrValidation
	}
	var (
		out   Sanction
		ended []id.ID
	)
	entry := Entry{Action: "user." + in.Type, TargetType: "user", TargetID: in.User, Reason: in.Reason}
	err := s.Destructive(ctx, c, entry, Action{
		Do: func(ctx context.Context, q *store.Queries, e *Entry) error {
			if err := lockUser(ctx, q, in.User); err != nil {
				return err
			}
			admin, _ := id.Parse(c.Subject)
			open, err := q.GetOpenSanction(ctx, in.User.PG())
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return fmt.Errorf("yaptırım: %w", err)
			case !open.EndsAt.Valid || open.EndsAt.Time.After(now):
				return ErrSanctionConflict
			default:
				// An expired suspension is closed before the new one.
				if _, err := q.LiftSanction(ctx, store.LiftSanctionParams{ID: open.ID, LiftedAt: ts(now), LiftedBy: admin.PG()}); err != nil {
					return fmt.Errorf("eski askı: %w", err)
				}
			}
			ends := pgtype.Timestamptz{}
			if !in.EndsAt.IsZero() {
				ends = ts(in.EndsAt.UTC())
			}
			row, err := q.CreateSanction(ctx, store.CreateSanctionParams{
				ID: id.New().PG(), UserID: in.User.PG(), Type: in.Type, Reason: in.Reason,
				StartsAt: ts(now), EndsAt: ends, CreatedBy: admin.PG(),
			})
			if err != nil {
				return fmt.Errorf("yaptırım: %w", err)
			}
			out = *sanctionOf(row.ID, row.Type, row.Reason, row.StartsAt, row.EndsAt)
			if ended, err = revokeAll(ctx, q, in.User, reasonSanctioned); err != nil {
				return err
			}
			e.Metadata = map[string]string{"sanction_id": out.ID.String()}
			if in.Report != (id.ID{}) {
				e.Metadata["report_id"] = in.Report.String()
			}
			if !out.EndsAt.IsZero() {
				e.Metadata["ends_at"] = out.EndsAt.Format(time.RFC3339)
			}
			return nil
		},
		Events: func(ctx context.Context, tx pgx.Tx) error {
			if err := userevents.SessionsRevoked(ctx, tx, in.User, ended...); err != nil {
				return err
			}
			if err := userevents.Sanctioned(ctx, tx, in.User); err != nil {
				return err
			}
			if in.Type == SanctionBan {
				return userevents.Banned(ctx, tx, in.User)
			}
			return nil
		},
	})
	if err != nil {
		return Sanction{}, err
	}
	s.cfg.Log.WarnContext(ctx, "platform yaptırımı uygulandı",
		slog.String("user_id", in.User.String()), slog.String("type", in.Type), slog.String("sanction_id", out.ID.String()))
	return out, nil
}

// LiftSanction ends the user's open sanction (an expired suspension
// included). Lifting a ban sends user.unbanned so core shows the content
// again. Sessions are not restored; the user signs in again.
func (s *Service) LiftSanction(ctx context.Context, c auth.AdminClaims, user id.ID, reason string) error {
	var wasBan bool
	err := s.Destructive(ctx, c, Entry{Action: "user.lift_sanction", TargetType: "user", TargetID: user, Reason: reason}, Action{
		Do: func(ctx context.Context, q *store.Queries, e *Entry) error {
			if err := lockUser(ctx, q, user); err != nil {
				return err
			}
			open, err := q.GetOpenSanction(ctx, user.PG())
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSanctionConflict
			}
			if err != nil {
				return fmt.Errorf("yaptırım: %w", err)
			}
			admin, _ := id.Parse(c.Subject)
			if n, err := q.LiftSanction(ctx, store.LiftSanctionParams{ID: open.ID, LiftedAt: ts(s.cfg.Now().UTC()), LiftedBy: admin.PG()}); err != nil || n != 1 {
				return fmt.Errorf("yaptırım kaldırma: n=%d: %w", n, err)
			}
			wasBan = open.Type == SanctionBan
			sid, _ := id.FromPG(open.ID)
			e.Metadata = map[string]string{"sanction_id": sid.String()}
			return nil
		},
		Events: func(ctx context.Context, tx pgx.Tx) error {
			if wasBan {
				return userevents.Unbanned(ctx, tx, user)
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	s.cfg.Log.WarnContext(ctx, "platform yaptırımı kaldırıldı", slog.String("user_id", user.String()))
	return nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

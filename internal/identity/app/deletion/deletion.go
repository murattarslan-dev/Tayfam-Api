// Package deletion deletes a user's account at the user's own request
// (issue #75; data model v14 "Hesap silme", "İlişkiler ve Silme
// Davranışı"; ADR-0004, ADR-0008).
//
// Deleting needs a recent sign-in: the access token must have been issued
// in the last ReauthWindow AND its session must have been opened with the
// provider (Google/Apple) in that window. A refresh issues a new access
// token without the provider, so a fresh iat alone would let anyone holding
// a copied refresh token delete the account; requiring a fresh session
// means the provider has checked the person again.
//
// In one transaction, the account's personal data is cleared at once (name,
// avatar, encrypted birth date, referral code and referrer), its sign-in
// identities, devices, sessions and refresh tokens are deleted, and
// user.deleted is written to the outbox so core and chat remove their
// records (userevents.Deleted). The users row stays for 30 days and is then
// deleted by the retention job; consent records stay (no foreign key,
// retention to be set with legal advice).
//
// Signing in again with the same Google/Apple account afterwards opens a
// new, empty account: the identity link is gone (v14: auth_identities are
// deleted at once). There is no undo within the 30 days.
package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ReauthWindow is how recent the sign-in must be (issue #75).
const ReauthWindow = 10 * time.Minute

var (
	// ErrReauthenticationRequired: the sign-in is older than ReauthWindow.
	// The client signs in with the provider again and retries with the new
	// access token.
	ErrReauthenticationRequired = errors.New("yeniden kimlik doğrulama gerekli")
	// ErrUnknownUser: the account is already deleted, not registered, or
	// the caller's session is ended.
	ErrUnknownUser = errors.New("kullanıcı bulunamadı")
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time // optional, for tests
}

// Service deletes accounts.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("deletion: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: cfg.Pool, log: cfg.Log, now: func() time.Time { return cfg.Now().UTC() }}, nil
}

// Caller is the verified access token of the request.
type Caller struct {
	User     id.ID
	Session  id.ID
	IssuedAt time.Time // iat
}

// Delete deletes the caller's account.
func (s *Service) Delete(ctx context.Context, c Caller) error {
	now := s.now()
	since := now.Add(-ReauthWindow)
	if c.IssuedAt.Before(since) {
		return ErrReauthenticationRequired
	}
	var devices int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		u, err := q.LockLiveUser(ctx, c.User.PG())
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownUser
		}
		if err != nil {
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		if !u.RegisteredAt.Valid {
			return ErrUnknownUser
		}
		sess, err := q.GetCallerSession(ctx, store.GetCallerSessionParams{
			ID: c.Session.PG(), UserID: c.User.PG(), Now: pgtype.Timestamptz{Time: now, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownUser
		}
		if err != nil {
			return fmt.Errorf("oturum: %w", err)
		}
		if sess.CreatedAt.Time.Before(since) {
			return ErrReauthenticationRequired
		}

		if n, err := q.SoftDeleteUser(ctx, c.User.PG()); err != nil || n != 1 {
			return fmt.Errorf("hesap silme: n=%d: %w", n, err)
		}
		if err := q.DeleteUserAuthIdentities(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("giriş kimlikleri: %w", err)
		}
		// Sessions and refresh tokens go with the devices (ON DELETE
		// CASCADE): every session of the account ends here, the caller's
		// included.
		if devices, err = q.DeleteUserDevices(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("cihazlar: %w", err)
		}
		if err := q.DeleteUserRefCodeLookups(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("arama kayıtları: %w", err)
		}
		// Written last, see outbox.Write.
		return userevents.Deleted(ctx, tx, c.User)
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "hesap silindi",
		slog.String("user_id", c.User.String()),
		slog.Int64("devices", devices))
	return nil
}

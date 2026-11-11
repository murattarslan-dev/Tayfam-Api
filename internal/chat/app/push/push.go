// Package push sends the notification center's notifications to phones
// that are not connected (FCM HTTP v1; issue #134, ADR-0019, ADR-0005
// "Bildirimler").
//
// A push token belongs to a session (ADR-0019 item 1, as ADR-0018's device
// = session): the session (sid) of the access token that registered it,
// one per session. When the session ends (identity's session.revoked,
// user.sanctioned, user.deleted) the token goes; a session that ended
// cannot register one while its access token is still valid.
//
// Every notification row written gets a push job in the same transaction
// (a trigger on notifications; db/chat/migrations/00011_push.sql). The
// worker here leases due jobs, skips users who are connected (except for
// the hike_overdue safety alert), applies the
// muting preferences and sends one message per token. A message carries
// translation keys and IDs only, never text, names or places; direct
// message pushes say only that there is something new.
//
// Push tokens, FCM access tokens, the service account key and message
// content never reach logs, metrics, events or error messages (CLAUDE.md
// rule 3): logs carry counts, HTTP status codes and FCM error codes.
package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits and timings (ADR-0019).
const (
	MaxTokenBytes    = 1024
	MaxTokensPerUser = 10
	// TokenRetention: a token not refreshed for this long is removed.
	TokenRetention = 60 * 24 * time.Hour
	// MaxAttempts bounds the sends of one job.
	MaxAttempts = 5
	// MaxAge: a job older than this is not sent any more.
	MaxAge = 24 * time.Hour
	// TTL is how long FCM keeps an undelivered message.
	TTL = 24 * time.Hour

	backoffBase = 30 * time.Second
	backoffMax  = 30 * time.Minute
	lease       = 2 * time.Minute
	pollEvery   = 2 * time.Second
	batch       = 50
	workers     = 8
	sendTimeout = 15 * time.Second
	retainEvery = time.Hour
)

// Platforms (push_tokens.platform).
const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

// tokenPattern is an FCM registration token's alphabet (the same CHECK is
// in the database).
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9:_.-]+$`)

// ValidToken reports whether s looks like an FCM registration token.
func ValidToken(s string) bool {
	return len(s) > 0 && len(s) <= MaxTokenBytes && tokenPattern.MatchString(s)
}

// Caller is the verified caller: the user, the session of the token and
// when the token was issued.
type Caller struct {
	User     id.ID
	Session  id.ID
	IssuedAt time.Time
}

// Presence tells whether a user has an open real-time connection
// (realtime.Hub).
type Presence interface {
	Online(user id.ID) bool
}

// Limit is a per-user rate (ratelimit.Limiter).
type Limit interface {
	Allow(key string) bool
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Sender sends to FCM; nil means push is off: tokens are still kept,
	// jobs are not sent and expire after MaxAge.
	Sender Sender
	// Presence is required when Sender is set.
	Presence Presence
	// Register limits token registrations per user; optional.
	Register Limit
	Now      func() time.Time // optional, for tests
}

// Service keeps push tokens and sends pushes.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || (cfg.Sender != nil && cfg.Presence == nil) {
		return nil, errors.New("push: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

// Enabled reports whether pushes are sent.
func (s *Service) Enabled() bool { return s.cfg.Sender != nil }

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Register stores the push token of the caller's session (replacing an
// older one) and marks it fresh. The same token held by another session
// moves here; the user keeps at most MaxTokensPerUser tokens. A session that
// ended cannot register (apperr.ErrSessionEnded).
func (s *Service) Register(ctx context.Context, c Caller, platform, token string) error {
	if platform != PlatformAndroid && platform != PlatformIOS {
		return apperr.Validation("platform")
	}
	if !ValidToken(token) {
		return apperr.Validation("token")
	}
	if s.cfg.Register != nil && !s.cfg.Register.Allow(c.User.String()) {
		return apperr.ErrRateLimited
	}
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// User first, then session: the order of every taker (ADR-0019).
		if err := q.LockPushKey(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("kilit: %w", err)
		}
		if err := q.LockPushKey(ctx, c.Session.PG()); err != nil {
			return fmt.Errorf("kilit: %w", err)
		}
		if err := checkSession(ctx, q, c); err != nil {
			return err
		}
		if err := q.DeletePushTokenElsewhere(ctx, store.DeletePushTokenElsewhereParams{Token: token, SessionID: c.Session.PG()}); err != nil {
			return fmt.Errorf("push token'ı: %w", err)
		}
		n, err := q.UpsertPushToken(ctx, store.UpsertPushTokenParams{
			SessionID: c.Session.PG(), UserID: c.User.PG(), Platform: platform, Token: token, Now: ts(now),
		})
		if err != nil {
			return fmt.Errorf("push token'ı: %w", err)
		}
		if n != 1 {
			// The session is another user's: tokens never say so.
			return apperr.ErrSessionEnded
		}
		if _, err := q.TrimUserPushTokens(ctx, store.TrimUserPushTokensParams{UserID: c.User.PG(), Keep: MaxTokensPerUser}); err != nil {
			return fmt.Errorf("push token sınırı: %w", err)
		}
		return nil
	})
}

// checkSession refuses a session that ended, or a token issued before the
// user's sessions were ended (ADR-0018's records, shared).
func checkSession(ctx context.Context, q *store.Queries, c Caller) error {
	ended, err := q.IsSessionEnded(ctx, c.Session.PG())
	if err != nil {
		return fmt.Errorf("oturum: %w", err)
	}
	if ended {
		return apperr.ErrSessionEnded
	}
	st, err := q.GetE2EState(ctx, c.User.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("oturum: %w", err)
	}
	if st.SessionsEndedAt.Valid && !c.IssuedAt.After(st.SessionsEndedAt.Time) {
		return apperr.ErrSessionEnded
	}
	return nil
}

// Unregister removes the push token of the caller's session; without one
// it changes nothing.
func (s *Service) Unregister(ctx context.Context, c Caller) error {
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockPushKey(ctx, c.Session.PG()); err != nil {
			return fmt.Errorf("kilit: %w", err)
		}
		// Only the caller's own row: the session is the caller's.
		if _, err := q.DeletePushTokenBySession(ctx, c.Session.PG()); err != nil {
			return fmt.Errorf("push token'ı: %w", err)
		}
		return nil
	})
}

// SessionEnded removes an ended session's token (session.revoked) and keeps
// the session from registering again while its access token may still be
// valid. Idempotent.
func (s *Service) SessionEnded(ctx context.Context, session id.ID) error {
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockPushKey(ctx, session.PG()); err != nil {
			return fmt.Errorf("kilit: %w", err)
		}
		if err := q.RecordEndedSession(ctx, store.RecordEndedSessionParams{SessionID: session.PG(), EndedAt: ts(s.now())}); err != nil {
			return fmt.Errorf("biten oturum: %w", err)
		}
		if _, err := q.DeletePushTokenBySession(ctx, session.PG()); err != nil {
			return fmt.Errorf("push token'ı: %w", err)
		}
		return nil
	})
}

// UserSanctioned removes every token of a sanctioned user (all their
// sessions were closed) and refuses access tokens issued before at.
// Idempotent.
func (s *Service) UserSanctioned(ctx context.Context, user id.ID, at time.Time) error {
	now := s.now()
	if at.IsZero() || at.After(now) {
		at = now
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockPushKey(ctx, user.PG()); err != nil {
			return fmt.Errorf("kilit: %w", err)
		}
		if err := q.EndUserSessions(ctx, store.EndUserSessionsParams{UserID: user.PG(), EndedAt: ts(at.UTC())}); err != nil {
			return fmt.Errorf("oturumlar: %w", err)
		}
		if _, err := q.DeleteUserPushTokens(ctx, user.PG()); err != nil {
			return fmt.Errorf("push token'ları: %w", err)
		}
		return nil
	})
}

// Cleanup removes a deleted user's push tokens inside the deletion's
// transaction (userevents.Cleanup). Their queued pushes go with their
// notifications (ON DELETE CASCADE).
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	if _, err := q.DeleteUserPushTokens(ctx, user.PG()); err != nil {
		return fmt.Errorf("push token'ları: %w", err)
	}
	return nil
}

// RetentionResult counts what one retention pass deleted.
type RetentionResult struct {
	Tokens, Jobs int64
}

// Retain removes tokens not refreshed for TokenRetention and jobs older
// than MaxAge.
func (s *Service) Retain(ctx context.Context) (RetentionResult, error) {
	var r RetentionResult
	now := s.now()
	q := store.New(s.cfg.Pool)
	var err error
	if r.Tokens, err = q.PurgeStalePushTokens(ctx, ts(now.Add(-TokenRetention))); err != nil {
		return r, fmt.Errorf("push token saklama: %w", err)
	}
	if r.Jobs, err = q.PurgeOldPushJobs(ctx, ts(now.Add(-MaxAge))); err != nil {
		return r, fmt.Errorf("push işi saklama: %w", err)
	}
	return r, nil
}

// RunRetention runs Retain every hour until ctx is done. Logs carry counts
// only.
func (s *Service) RunRetention(ctx context.Context) error {
	t := time.NewTicker(retainEvery)
	defer t.Stop()
	for {
		r, err := s.Retain(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.cfg.Log.WarnContext(ctx, "push saklama işi başarısız", slog.String("hata", err.Error()))
		case r != (RetentionResult{}):
			s.cfg.Log.InfoContext(ctx, "push saklama işi", slog.Int64("token", r.Tokens), slog.Int64("is", r.Jobs))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

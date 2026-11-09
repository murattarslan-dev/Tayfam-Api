// Package retention deletes identity's records whose retention period is
// over (issue #75; data model v14 "Saklama süreleri", KVKK aydınlatma metni
// §7):
//
//	refresh tokens  used, or of an expired or ended session, 30 days ago
//	sessions        not refreshed for 90 days, or ended 90 days ago
//	devices         not seen for 90 days (their sessions with them)
//	users           deleted 30 days ago (personal data was cleared at
//	                deletion; the row and what still points to it go now)
//
// consent_records are not touched: how long they are kept after an account
// is deleted waits for legal advice (00002).
//
// There is no job queue. Run scans the database every Interval, so after a
// restart the next run continues where the last stopped. Every DELETE checks
// its condition again and removes at most batch rows per statement; several
// pods running the job at once only delete the same rows once.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
)

// Retention periods (v14 "Saklama süreleri").
const (
	RefreshTokenRetention = 30 * 24 * time.Hour
	SessionRetention      = 90 * 24 * time.Hour
	DeviceRetention       = 90 * 24 * time.Hour
	DeletedUserRetention  = 30 * 24 * time.Hour
)

// DefaultInterval is how often Run runs.
const DefaultInterval = time.Hour

const (
	batch = 500
	// maxBatches bounds one step of one run; the rest comes next run.
	maxBatches = 100
)

// Config wires a Service.
type Config struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Interval time.Duration    // zero: DefaultInterval
	Now      func() time.Time // optional, for tests
}

// Service runs the retention job.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("retention: eksik yapılandırma")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// Result counts the rows one run deleted directly (rows removed by ON
// DELETE CASCADE are not counted).
type Result struct {
	RefreshTokens int64
	Sessions      int64
	Devices       int64
	Users         int64
}

// RunOnce deletes everything whose period is over now.
func (s *Service) RunOnce(ctx context.Context) (Result, error) {
	now := s.cfg.Now().UTC()
	var r Result
	steps := []struct {
		name  string
		age   time.Duration
		purge func(context.Context, pgtype.Timestamptz) (int64, error)
		out   *int64
	}{
		{"refresh_tokens", RefreshTokenRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgeRefreshTokens(ctx, store.PurgeRefreshTokensParams{Cutoff: c, Batch: batch})
		}, &r.RefreshTokens},
		{"sessions", SessionRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgeSessions(ctx, store.PurgeSessionsParams{Cutoff: c, Batch: batch})
		}, &r.Sessions},
		{"devices", DeviceRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgeDevices(ctx, store.PurgeDevicesParams{Cutoff: c, Batch: batch})
		}, &r.Devices},
		{"users", DeletedUserRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgeDeletedUsers(ctx, store.PurgeDeletedUsersParams{Cutoff: c, Batch: batch})
		}, &r.Users},
	}
	for _, st := range steps {
		cutoff := pgtype.Timestamptz{Time: now.Add(-st.age), Valid: true}
		for range maxBatches {
			n, err := st.purge(ctx, cutoff)
			if err != nil {
				return r, fmt.Errorf("saklama %s: %w", st.name, err)
			}
			*st.out += n
			if n < batch {
				break
			}
		}
	}
	return r, nil
}

// Run runs RunOnce every Interval until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		r, err := s.RunOnce(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.cfg.Log.WarnContext(ctx, "saklama işi başarısız", slog.Any("err", err))
		case r != (Result{}):
			s.cfg.Log.InfoContext(ctx, "saklama süresi dolan kayıtlar silindi",
				slog.Int64("refresh_tokens", r.RefreshTokens),
				slog.Int64("sessions", r.Sessions),
				slog.Int64("devices", r.Devices),
				slog.Int64("users", r.Users))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

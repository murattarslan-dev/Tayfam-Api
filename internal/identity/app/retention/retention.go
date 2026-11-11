// Package retention deletes identity's records whose retention period is
// over (issue #75; data model v14 "Saklama süreleri", KVKK aydınlatma metni
// §7):
//
//	refresh tokens  used, or of an expired or ended session, 30 days ago
//	sessions        not refreshed for 90 days, or ended 90 days ago
//	devices         not seen for 90 days (their sessions with them)
//	users           deleted 30 days ago (personal data was cleared at
//	                deletion; the row and what still points to it go now)
//	platform audit  365 days old (ADR-0008; the table's trigger refuses
//	                to delete anything younger)
//	data exports    (issue #144, ADR-0021) the key and the file record of
//	                a READY export 7 days after it was ready (expired);
//	                pending or processing for 10 minutes: failed
//	                (timeout); the row 30 days after the request
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
	RefreshTokenRetention  = 30 * 24 * time.Hour
	SessionRetention       = 90 * 24 * time.Hour
	DeviceRetention        = 90 * 24 * time.Hour
	DeletedUserRetention   = 30 * 24 * time.Hour
	PlatformAuditRetention = 365 * 24 * time.Hour
	// DataExportStale and DataExportRowRetention: see the package comment
	// (dataexport.StaleAfter, dataexport.RowRetention; not imported to keep
	// this package free of the export's dependencies).
	DataExportStale        = 10 * time.Minute
	DataExportRowRetention = 30 * 24 * time.Hour
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
	PlatformAudit int64
	// Data export requests: expired (key deleted), failed as stale, rows
	// deleted.
	ExportsExpired int64
	ExportsStale   int64
	ExportsDeleted int64
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
		{"platform_audit_logs", PlatformAuditRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgePlatformAuditLogs(ctx, store.PurgePlatformAuditLogsParams{Cutoff: c, Batch: batch})
		}, &r.PlatformAudit},
		// expires_at is the end itself: the cutoff is now.
		{"data_export_expiry", 0, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.ExpireDataExports(ctx, store.ExpireDataExportsParams{Cutoff: c, Batch: batch})
		}, &r.ExportsExpired},
		{"data_export_stale", DataExportStale, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.FailStaleDataExports(ctx, store.FailStaleDataExportsParams{Cutoff: c, Batch: batch})
		}, &r.ExportsStale},
		{"data_export_requests", DataExportRowRetention, func(ctx context.Context, c pgtype.Timestamptz) (int64, error) {
			return s.q.PurgeDataExports(ctx, store.PurgeDataExportsParams{Cutoff: c, Batch: batch})
		}, &r.ExportsDeleted},
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
				slog.Int64("users", r.Users),
				slog.Int64("platform_audit_logs", r.PlatformAudit),
				slog.Int64("data_exports_expired", r.ExportsExpired),
				slog.Int64("data_exports_stale", r.ExportsStale),
				slog.Int64("data_export_requests", r.ExportsDeleted))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

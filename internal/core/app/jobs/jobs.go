// Package jobs runs core's scheduled background jobs (issue #66; data model
// v14 "Arka plan işleri", "Tayfa yönetimi", "Tayfa silmede bekleme",
// "Yürüyüşte beklenen dönüş", "Konvoy aracı", "Saklama süreleri"; project
// plan §7):
//
//	tayfa-cleanup   inactive tayfas: active → warned → archived, and back to
//	                active on activity (weekly)
//	tayfa-deletion  soft deletes tayfas whose 72-hour deletion wait is over
//	purge           hard deletes tayfas and content soft deleted 30 days ago
//	hike-alert      raises hike.overdue for hikers who did not return
//	plates          clears convoy plates 30 days after the event ended
//	report-escalation  sends reports open for 7 days to the platform (#76)
//	retention       deletes resolved reports and audit records after a
//	                year (#76)
//
// The outcome of every pass (finish time, success, rows changed) is written
// to job_runs for the panel's dashboard (issue #110).
//
// There is no job queue. Each job is a Go ticker that scans the database
// state, so after a restart the next run continues where the last one
// stopped. Every run is idempotent: it only changes rows that are still in
// the state it looks for.
//
// Several pods may run the same job. Each batch is one transaction that
// first tries the job's advisory lock (pg_try_advisory_xact_lock); a pod
// that does not get it skips the run. Rows are also picked with SKIP LOCKED
// and every UPDATE/DELETE checks its condition again, so a user's change
// that commits first (cancelling a deletion, checking back from a hike) wins
// and no row is changed twice.
//
// The jobs run without a user: app.actor_id and app.system_job are not set,
// audit records have no actor and outbox events no actor_id. Events and
// audit metadata hold IDs and classification only.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Job names: worker names, advisory lock keys and log fields.
const (
	JobTayfaCleanup  = "tayfa-cleanup"
	JobTayfaDeletion = "tayfa-deletion"
	JobPurge         = "purge"
	JobHikeAlert     = "hike-alert"
	JobPlates        = "plates"
	// Issue #76.
	JobReportEscalation = "report-escalation"
	JobRetention        = "retention"
)

// Default run intervals.
const (
	// DefaultTayfaCleanupInterval: the cleanup is weekly (project plan §7).
	DefaultTayfaCleanupInterval  = 7 * 24 * time.Hour
	DefaultTayfaDeletionInterval = 10 * time.Minute
	DefaultPurgeInterval         = time.Hour
	// DefaultHikeAlertInterval: every five minutes (v14).
	DefaultHikeAlertInterval = 5 * time.Minute
	DefaultPlatesInterval    = 24 * time.Hour
	// DefaultReportEscalationInterval: a report is escalated at most an
	// hour after its seventh day.
	DefaultReportEscalationInterval = time.Hour
	DefaultRetentionInterval        = 24 * time.Hour
)

// Thresholds (project plan §7, v14 "Saklama süreleri").
const (
	// NewTayfaPeriod: a tayfa should reach MinActiveMembers within this
	// period after it was created.
	NewTayfaPeriod   = 7 * 24 * time.Hour
	MinActiveMembers = 3
	// InactivityPeriod: a tayfa whose last_activity_at is older is warned.
	InactivityPeriod = 30 * 24 * time.Hour
	// WarningPeriod: a warned tayfa without activity for this long is
	// archived.
	WarningPeriod = 7 * 24 * time.Hour
	// RetentionPeriod: soft deleted tayfas and content are deleted for good
	// after this long (CLAUDE.md "Silme").
	RetentionPeriod = 30 * 24 * time.Hour
	// PlateRetention: convoy plates are cleared this long after the event
	// ended.
	PlateRetention = 30 * 24 * time.Hour
)

// Batch sizes: one transaction changes at most this many rows, so a large
// backlog never becomes one long transaction.
const (
	tayfaBatch   = 100
	contentBatch = 500
	hikeBatch    = 100
	plateBatch   = 500
	reportBatch  = 100
	// retentionBatch: reports and audit records deleted per transaction.
	retentionBatch = 500
	// A purged tayfa takes its whole content with it (ON DELETE CASCADE),
	// so tayfas are purged one per transaction.
	purgeTayfaBatch = 1
	// maxBatches bounds the batches of one step in one run; the rest comes
	// on the next run.
	maxBatches = 100
	// maxRecipients bounds the recipient IDs in one event (the payload is
	// limited to 8 KiB).
	maxRecipients = 100
)

// Outbox event types. Every event has no actor and its domain_events
// tayfa_id is the tayfa. Payloads (IDs and classification only):
//
//	tayfa.warned       tayfa_id, reason (inactive|few_members)
//	tayfa.archived     tayfa_id
//	tayfa.reactivated  tayfa_id
//	tayfa.deleted      tayfa_id
//	tayfa.purged       tayfa_id
//	hike.overdue       tayfa_id, event_id, user_id (the hiker),
//	                   organizer_id (absent if the account was deleted)
//
// tayfa.warned, tayfa.archived and hike.overdue are followed by
// notification.requested events (package notify, ADR-0009): tayfa_warned
// and tayfa_archived for the active admins and moderators, hike_overdue
// for the organizer if still an active member and the active admins and
// moderators, never the hiker. At most maxRecipients, admins first.
const (
	EventTayfaWarned      = "tayfa.warned"
	EventTayfaArchived    = "tayfa.archived"
	EventTayfaReactivated = "tayfa.reactivated"
	EventTayfaDeleted     = "tayfa.deleted"
	EventTayfaPurged      = "tayfa.purged"
	EventHikeOverdue      = "hike.overdue"
)

// Reasons of tayfa.warned.
const (
	ReasonInactive   = "inactive"
	ReasonFewMembers = "few_members"
)

// Intervals sets how often each job runs. A zero field keeps the default;
// tests set short ones.
type Intervals struct {
	TayfaCleanup  time.Duration
	TayfaDeletion time.Duration
	Purge         time.Duration
	HikeAlert     time.Duration
	Plates        time.Duration
	// Issue #76.
	ReportEscalation time.Duration
	Retention        time.Duration
}

func (i Intervals) withDefaults() Intervals {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&i.TayfaCleanup, DefaultTayfaCleanupInterval)
	def(&i.TayfaDeletion, DefaultTayfaDeletionInterval)
	def(&i.Purge, DefaultPurgeInterval)
	def(&i.HikeAlert, DefaultHikeAlertInterval)
	def(&i.Plates, DefaultPlatesInterval)
	def(&i.ReportEscalation, DefaultReportEscalationInterval)
	def(&i.Retention, DefaultRetentionInterval)
	return i
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Now is the clock; nil means time.Now.
	Now       func() time.Time
	Intervals Intervals
}

// Service runs the jobs.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("jobs: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Intervals = cfg.Intervals.withDefaults()
	return &Service{cfg: cfg}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Worker is a job for service.Runtime.AddWorker.
type Worker struct {
	Name string
	Run  func(ctx context.Context) error
}

// Workers returns every job, each as its own worker.
func (s *Service) Workers() []Worker {
	iv := s.cfg.Intervals
	return []Worker{
		{JobTayfaCleanup, s.every(JobTayfaCleanup, iv.TayfaCleanup, s.CleanupTayfas)},
		{JobTayfaDeletion, s.every(JobTayfaDeletion, iv.TayfaDeletion, s.DeleteDueTayfas)},
		{JobPurge, s.every(JobPurge, iv.Purge, s.Purge)},
		{JobHikeAlert, s.every(JobHikeAlert, iv.HikeAlert, s.AlertOverdueHikes)},
		{JobPlates, s.every(JobPlates, iv.Plates, s.ClearPlates)},
		{JobReportEscalation, s.every(JobReportEscalation, iv.ReportEscalation, s.EscalateReports)},
		{JobRetention, s.every(JobRetention, iv.Retention, s.ExpireRecords)},
	}
}

// every runs pass at start and then every interval until ctx is done, then
// returns nil. A failed run is logged (job name and error only) and retried
// on the next tick; it never stops the service.
func (s *Service) every(name string, interval time.Duration, pass func(context.Context) (int, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			n, err := pass(ctx)
			switch {
			case ctx.Err() != nil:
				return nil
			case err != nil:
				s.cfg.Log.WarnContext(ctx, "arka plan işi başarısız", slog.String("iş", name), slog.String("hata", err.Error()))
			case n > 0:
				s.cfg.Log.InfoContext(ctx, "arka plan işi", slog.String("iş", name), slog.Int("adet", n))
			}
			s.recordRun(ctx, name, n, err)
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	}
}

// recordRun writes the outcome of a pass to job_runs for the panel's
// dashboard (issue #110): finish time, success and rows changed; never the
// error text. A failure to record is logged and does not stop the job.
func (s *Service) recordRun(ctx context.Context, name string, changed int, runErr error) {
	err := store.New(s.cfg.Pool).RecordJobRun(ctx, store.RecordJobRunParams{
		Job: name, FinishedAt: ts(s.now()), Succeeded: runErr == nil, Changed: int64(max(changed, 0)),
	})
	if err != nil && ctx.Err() == nil {
		s.cfg.Log.WarnContext(ctx, "iş çalışması kaydedilemedi", slog.String("iş", name), slog.String("hata", err.Error()))
	}
}

// errLocked: another worker holds the job's lock.
var errLocked = errors.New("iş başka bir pod'da çalışıyor")

// batch runs fn in one transaction holding the job's advisory lock and
// returns how many rows fn changed. It returns errLocked without running fn
// when another worker holds the lock.
func (s *Service) batch(ctx context.Context, job string, fn func(q *store.Queries) (int, error)) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ok, err := q.TryJobLock(ctx, "tayfam.core.job."+job)
		if err != nil {
			return fmt.Errorf("iş kilidi: %w", err)
		}
		if !ok {
			return errLocked
		}
		n, err = fn(q)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// drain runs batches of size until one changes fewer rows, maxBatches ran
// or another worker holds the lock, and returns the rows changed.
func (s *Service) drain(ctx context.Context, job string, size int, fn func(q *store.Queries) (int, error)) (int, error) {
	total := 0
	for range maxBatches {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.batch(ctx, job, fn)
		if errors.Is(err, errLocked) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		total += n
		if n < size {
			break
		}
	}
	return total, nil
}

// systemAudit appends an audit record without an actor.
func systemAudit(ctx context.Context, q *store.Queries, tayfa id.ID, action string, target txn.Target, meta []byte) error {
	tt := pgtype.Text{String: target.Type, Valid: target.Type != ""}
	tid := pgtype.UUID{}
	if target.Type != "" {
		tid = target.ID.PG()
	}
	if meta == nil {
		meta = []byte("{}")
	}
	if err := q.CreateSystemAuditLog(ctx, store.CreateSystemAuditLogParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), Action: action, TargetType: tt, TargetID: tid, Metadata: meta,
	}); err != nil {
		return fmt.Errorf("denetim kaydı: %w", err)
	}
	return nil
}

// ids returns the IDs as strings for event payloads.
func idList(us []pgtype.UUID) []id.ID {
	out := make([]id.ID, 0, len(us))
	for _, u := range us {
		out = append(out, id.ID(u.Bytes))
	}
	return out
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/imaging"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// Worker settings.
const (
	// leaseTime: a job not finished by then is taken again.
	leaseTime = 2 * time.Minute
	// maxAttempts: a job that failed this often for reasons outside the
	// file is rejected as failed.
	maxAttempts = 5
	// cleanupBatch bounds the uploads one cleanup pass expires or deletes.
	cleanupBatch = 100
)

// Wake makes the processing worker look for work now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunProcessor is the media-process worker: it leases queued uploads one at
// a time (FOR UPDATE SKIP LOCKED; several pods may run it) and processes
// them. It returns when ctx ends. Without object storage it waits idle.
func (s *Service) RunProcessor(ctx context.Context) error {
	if !s.Enabled() {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		for {
			worked, err := s.ProcessNext(ctx)
			if err != nil && ctx.Err() == nil {
				s.cfg.Log.ErrorContext(ctx, "medya işleme turu başarısız", slog.String("err", err.Error()))
			}
			if !worked || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-s.wake:
		}
	}
}

// backoff is the wait before attempt n+1 after n failed attempts.
func backoff(n int32) time.Duration {
	d := 30 * time.Second << (n - 1)
	if n < 1 || d > 10*time.Minute {
		return 10 * time.Minute
	}
	return d
}

// ProcessNext processes one due upload; worked is false when none was due.
func (s *Service) ProcessNext(ctx context.Context) (worked bool, err error) {
	now := s.now()
	job, err := s.q.LeaseUpload(ctx, store.LeaseUploadParams{Now: ts(now), LeaseUntil: ts(now.Add(leaseTime))})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("iş kiralama: %w", err)
	}
	uid := id.ID(job.ID.Bytes)
	log := s.cfg.Log.With(slog.String("upload_id", uid.String()), slog.Int("attempt", int(job.Attempts)))
	start := time.Now()

	retry := func(cause error) (bool, error) {
		if job.Attempts >= maxAttempts {
			log.WarnContext(ctx, "medya işlenemedi; reddedildi", slog.String("reason", "failed"), slog.String("err", cause.Error()))
			return true, s.rejectJob(ctx, job, "failed")
		}
		log.WarnContext(ctx, "medya işlenemedi; yeniden denenecek", slog.String("err", cause.Error()))
		if _, err := s.q.DeferUpload(ctx, store.DeferUploadParams{ID: job.ID, Attempts: job.Attempts, RunAfter: ts(s.now().Add(backoff(job.Attempts)))}); err != nil {
			return true, fmt.Errorf("erteleme: %w", err)
		}
		return true, nil
	}

	raw, err := s.cfg.Store.Get(ctx, incomingKey(job.R2Key), MaxUploadBytes)
	switch {
	case errors.Is(err, objstore.ErrNotFound), errors.Is(err, objstore.ErrTooLarge):
		// Gone (lifecycle) or larger than any accepted upload: nothing
		// will change on a retry.
		log.WarnContext(ctx, "ham dosya okunamadı; reddedildi", slog.String("reason", "failed"), slog.String("err", err.Error()))
		return true, s.rejectJob(ctx, job, "failed")
	case err != nil:
		return retry(err)
	}
	if int64(len(raw)) != job.SizeBytes {
		log.WarnContext(ctx, "ham dosyanın boyutu bildirilenden farklı; reddedildi")
		return true, s.rejectJob(ctx, job, "size_mismatch")
	}

	res, err := s.cfg.Process(raw)
	var re *imaging.RejectError
	switch {
	case errors.As(err, &re):
		log.InfoContext(ctx, "medya reddedildi", slog.String("reason", string(re.Reason)))
		return true, s.rejectJob(ctx, job, string(re.Reason))
	case err != nil:
		return retry(err)
	}
	if err := s.cfg.Store.Put(ctx, previewKey(job.R2Key), res.ContentType, res.Preview); err != nil {
		return retry(err)
	}
	if err := s.cfg.Store.Put(ctx, fullKey(job.R2Key), res.ContentType, res.Full); err != nil {
		return retry(err)
	}
	n, err := s.q.FinishUpload(ctx, store.FinishUploadParams{
		ID: job.ID, Attempts: job.Attempts, OutputType: text(res.ContentType),
		StoredBytes: pgInt8(int64(len(res.Preview) + len(res.Full))),
		Width:       pgInt4(res.Width), Height: pgInt4(res.Height), Now: ts(s.now()),
	})
	if err != nil {
		return true, fmt.Errorf("bitirme: %w", err)
	}
	if n == 0 {
		// The lease ran out and another worker owns the job; it writes the
		// same keys.
		log.WarnContext(ctx, "medya kirası başka işçiye geçti")
		return true, nil
	}
	s.deleteRaw(ctx, log, job.R2Key)
	log.InfoContext(ctx, "medya hazır", slog.Int64("ms", time.Since(start).Milliseconds()))
	return true, nil
}

func (s *Service) rejectJob(ctx context.Context, job store.LeaseUploadRow, reason string) error {
	if _, err := s.q.RejectProcessingUpload(ctx, store.RejectProcessingUploadParams{ID: job.ID, Attempts: job.Attempts, RejectReason: text(reason)}); err != nil {
		return fmt.Errorf("ret: %w", err)
	}
	s.deleteRaw(ctx, s.cfg.Log.With(slog.String("upload_id", id.ID(job.ID.Bytes).String())), job.R2Key)
	return nil
}

// deleteRaw removes the raw file; on failure the incoming/ lifecycle rule
// removes it within 24 hours.
func (s *Service) deleteRaw(ctx context.Context, log *slog.Logger, key string) {
	if err := s.cfg.Store.Delete(ctx, incomingKey(key)); err != nil {
		log.WarnContext(ctx, "ham dosya silinemedi", slog.String("err", err.Error()))
	}
}

// RunCleanup is the media-cleanup worker: it expires uploads past their
// lifetime and deletes the objects and rows of uploads marked deleting
// (by expiry, by content that let go of them, by account deletion).
func (s *Service) RunCleanup(ctx context.Context) error {
	if !s.Enabled() {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(s.cfg.CleanupInterval)
	defer t.Stop()
	for {
		if _, err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.ErrorContext(ctx, "medya temizliği başarısız", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Cleanup runs one pass and returns how many uploads it deleted.
func (s *Service) Cleanup(ctx context.Context) (int, error) {
	now := s.now()
	for {
		expired, err := s.q.ExpireUploads(ctx, store.ExpireUploadsParams{
			PendingBefore: ts(now.Add(-PendingLifetime)), RejectedBefore: ts(now.Add(-RejectedLifetime)),
			UnattachedBefore: ts(now.Add(-UnattachedLifetime)), N: cleanupBatch,
		})
		if err != nil {
			return 0, fmt.Errorf("süre dolumu: %w", err)
		}
		if len(expired) < cleanupBatch {
			break
		}
	}
	deleted := 0
	for {
		rows, err := s.q.ListDeletingUploads(ctx, cleanupBatch)
		if err != nil {
			return deleted, fmt.Errorf("silme kuyruğu: %w", err)
		}
		progress := 0
		for _, r := range rows {
			if err := s.deleteObjects(ctx, r.R2Key); err != nil {
				s.cfg.Log.WarnContext(ctx, "medya nesneleri silinemedi", slog.String("upload_id", id.ID(r.ID.Bytes).String()), slog.String("err", err.Error()))
				continue
			}
			if _, err := s.q.DeleteUploadRow(ctx, r.ID); err != nil {
				return deleted, fmt.Errorf("satır silme: %w", err)
			}
			deleted++
			progress++
		}
		if len(rows) < cleanupBatch || progress == 0 {
			break
		}
	}
	if deleted > 0 {
		s.cfg.Log.InfoContext(ctx, "medya silindi", slog.Int("count", deleted))
	}
	return deleted, nil
}

func (s *Service) deleteObjects(ctx context.Context, key string) error {
	for _, k := range []string{incomingKey(key), previewKey(key), fullKey(key)} {
		if err := s.cfg.Store.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

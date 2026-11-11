package media

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// cleanupBatch bounds the uploads one cleanup pass expires or deletes.
const cleanupBatch = 100

// deleteKeys removes objects; on failure the incoming/ lifecycle rule and
// the cleanup worker remove them later.
func (s *Service) deleteKeys(ctx context.Context, log *slog.Logger, keys ...string) {
	for _, k := range keys {
		if err := s.cfg.Store.Delete(ctx, k); err != nil {
			log.WarnContext(ctx, "nesne silinemedi", slog.String("err", err.Error()))
		}
	}
}

// RunCleanup is the media-cleanup worker: it expires uploads past their
// lifetime and deletes the objects and rows of uploads marked deleting
// (by expiry, by content that let go of them, by account deletion).
// Several pods may run it: every step is idempotent.
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
	for _, k := range []string{
		incomingKey(key, KindPreview), incomingKey(key, KindFull), mediaKey(key, KindPreview), mediaKey(key, KindFull),
	} {
		if err := s.cfg.Store.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

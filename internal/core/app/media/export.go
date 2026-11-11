package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// KVKK data export storage (issue #144, ADR-0021). identity encrypts the
// archive and calls these methods with the user's own token: core keeps
// where the encrypted file is, its size and expiry, and signs the links.
// The key and the content never reach core. Only the caller's own
// data_export upload is ever found; image methods never see one.
const (
	// PurposeDataExport is the encrypted export archive.
	PurposeDataExport = "data_export"
	// ExportLinkTTL is how long an export PUT or GET link works.
	ExportLinkTTL = 5 * time.Minute
	// ExportLifetime: a READY export is deleted after this.
	ExportLifetime = 7 * 24 * time.Hour
	// MinExportBytes and MaxExportBytes bound the encrypted file: at most a
	// 16 MiB archive plus version byte, nonce and tag.
	MinExportBytes = 30
	MaxExportBytes = 16<<20 + 29
	exportType     = "application/octet-stream"
)

// exportKey is the object of an export, apart from incoming/ and media/:
// the R2 lifecycle rule for exports/ removes what cleanup misses.
func exportKey(k string) string { return "exports/" + k }

// ReserveExport opens the user's export slot for identity's request
// (MediaService.ReserveExportUpload). A repeat returns the same upload.
// Another active export of the user is deleted: identity runs one request
// at a time, so an older slot is left over.
func (s *Service) ReserveExport(ctx context.Context, user, request id.ID) (id.ID, error) {
	if !s.Enabled() {
		return id.ID{}, apperr.ErrUnavailable
	}
	if request == (id.ID{}) {
		return id.ID{}, apperr.Validation("data_export_id")
	}
	var out id.ID
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockUserUploads(ctx, user.String()); err != nil {
			return fmt.Errorf("yükleme kilidi: %w", err)
		}
		prev, err := q.GetExportByClientRequest(ctx, store.GetExportByClientRequestParams{UserID: user.PG(), ClientRequestID: request.PG()})
		switch {
		case err == nil:
			if prev.Status == statusDeleting {
				return apperr.ErrUploadNotFound
			}
			out = id.ID(prev.ID.Bytes)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		if _, err := q.ReleaseOtherExports(ctx, store.ReleaseOtherExportsParams{UserID: user.PG(), ClientRequestID: request.PG()}); err != nil {
			return fmt.Errorf("eski dışa aktarma: %w", err)
		}
		key, err := newKey()
		if err != nil {
			return err
		}
		uid := id.New()
		if _, err := q.CreateExportUpload(ctx, store.CreateExportUploadParams{
			ID: uid.PG(), UserID: user.PG(), R2Key: key, ClientRequestID: request.PG(),
		}); err != nil {
			return fmt.Errorf("dışa aktarma kaydı: %w", err)
		}
		out = uid
		return nil
	})
	if err != nil {
		return id.ID{}, err
	}
	s.cfg.Log.InfoContext(ctx, "dışa aktarma yuvası açıldı", slog.String("upload_id", out.String()))
	return out, nil
}

func (s *Service) ownExport(ctx context.Context, user, upload id.ID) (store.Upload, error) {
	row, err := s.q.GetOwnExport(ctx, store.GetOwnExportParams{ID: upload.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.Upload{}, apperr.ErrUploadNotFound
	case err != nil:
		return store.Upload{}, fmt.Errorf("dışa aktarma: %w", err)
	}
	return row, nil
}

// ExportUploadURL fixes the size of the user's pending export and returns
// its PUT link (MediaService.GetExportUploadUrl).
func (s *Service) ExportUploadURL(ctx context.Context, user, upload id.ID, size int64) (objstore.Presigned, error) {
	if !s.Enabled() {
		return objstore.Presigned{}, apperr.ErrUnavailable
	}
	if size < MinExportBytes || size > MaxExportBytes {
		return objstore.Presigned{}, apperr.Validation("size_bytes")
	}
	row, err := s.ownExport(ctx, user, upload)
	if err != nil {
		return objstore.Presigned{}, err
	}
	if row.Status != StatusPending {
		return objstore.Presigned{}, apperr.ErrUploadNotFound
	}
	n, err := s.q.SetExportBytes(ctx, store.SetExportBytesParams{
		ID: row.ID, UserID: user.PG(), ExportBytes: pgtype.Int8{Int64: size, Valid: true},
	})
	if err != nil {
		return objstore.Presigned{}, fmt.Errorf("dışa aktarma boyutu: %w", err)
	}
	if n != 1 {
		// Another size was fixed by the first link.
		return objstore.Presigned{}, apperr.Validation("size_bytes")
	}
	link, err := s.cfg.Store.PresignPut(exportKey(row.R2Key), exportType, size, ExportLinkTTL)
	if err != nil {
		return objstore.Presigned{}, fmt.Errorf("dışa aktarma linki: %w", err)
	}
	return link, nil
}

// CompleteExport checks the stored size and makes the export READY for
// ExportLifetime (MediaService.CompleteExportUpload). It returns the
// expiry.
func (s *Service) CompleteExport(ctx context.Context, user, upload id.ID) (time.Time, error) {
	if !s.Enabled() {
		return time.Time{}, apperr.ErrUnavailable
	}
	row, err := s.ownExport(ctx, user, upload)
	if err != nil {
		return time.Time{}, err
	}
	switch {
	case row.Status == StatusReady:
		return row.ExpiresAt.Time.UTC(), nil
	case row.Status != StatusPending:
		return time.Time{}, apperr.ErrUploadNotFound
	case !row.ExportBytes.Valid:
		return time.Time{}, apperr.ErrUploadNotReceived
	}
	log := s.cfg.Log.With(slog.String("upload_id", upload.String()))
	o, err := s.cfg.Store.Head(ctx, exportKey(row.R2Key))
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		return time.Time{}, apperr.ErrUploadNotReceived
	case err != nil:
		log.WarnContext(ctx, "dışa aktarma denetlenemedi", slog.String("err", err.Error()))
		return time.Time{}, apperr.ErrUnavailable
	}
	if o.Size != row.ExportBytes.Int64 {
		// The signed length makes this unreachable unless the storage
		// ignored it; the object is not kept.
		if _, err := s.q.DropExport(ctx, store.DropExportParams{ID: row.ID, UserID: user.PG()}); err != nil {
			return time.Time{}, fmt.Errorf("dışa aktarma silme: %w", err)
		}
		log.WarnContext(ctx, "dışa aktarma boyutu tutmadı; silindi")
		return time.Time{}, apperr.Validation("size_bytes")
	}
	now := s.now()
	expires := now.Add(ExportLifetime)
	n, err := s.q.MarkExportReady(ctx, store.MarkExportReadyParams{ID: row.ID, UserID: user.PG(), Now: ts(now), ExpiresAt: ts(expires)})
	if err != nil {
		return time.Time{}, fmt.Errorf("dışa aktarma hazır: %w", err)
	}
	if n != 1 {
		// A concurrent completion won; return what it stored.
		row, err = s.ownExport(ctx, user, upload)
		if err != nil {
			return time.Time{}, err
		}
		if row.Status != StatusReady {
			return time.Time{}, apperr.ErrUploadNotFound
		}
		return row.ExpiresAt.Time.UTC(), nil
	}
	log.InfoContext(ctx, "dışa aktarma hazır")
	return expires, nil
}

// ExportDownloadURL returns a GET link for the user's READY, unexpired
// export (MediaService.GetExportDownloadUrl).
func (s *Service) ExportDownloadURL(ctx context.Context, user, upload id.ID) (objstore.Presigned, error) {
	if !s.Enabled() {
		return objstore.Presigned{}, apperr.ErrUnavailable
	}
	row, err := s.ownExport(ctx, user, upload)
	if err != nil {
		return objstore.Presigned{}, err
	}
	if row.Status != StatusReady || !row.ExpiresAt.Valid || !s.now().Before(row.ExpiresAt.Time) {
		return objstore.Presigned{}, apperr.ErrUploadNotReady
	}
	link, err := s.cfg.Store.PresignGet(exportKey(row.R2Key), ExportLinkTTL)
	if err != nil {
		return objstore.Presigned{}, fmt.Errorf("dışa aktarma linki: %w", err)
	}
	return link, nil
}

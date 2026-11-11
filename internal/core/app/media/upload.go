package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// CreateInput is a new upload.
type CreateInput struct {
	Purpose     string
	ContentType string
	SizeBytes   int64
	// Tayfa is required for covers and catch photos, zero for avatars.
	Tayfa           id.ID
	ClientRequestID string
}

// Created is a new upload and its PUT link. Link is the zero value when a
// repeated request finds the upload past PENDING.
type Created struct {
	Upload Upload
	Link   objstore.Presigned
}

func validate(in CreateInput) (id.ID, error) {
	switch in.Purpose {
	case PurposeAvatar:
		if in.Tayfa != (id.ID{}) {
			return id.ID{}, apperr.Validation("tayfa_id")
		}
	case PurposeCover, PurposeCatchPhoto:
		if in.Tayfa == (id.ID{}) {
			return id.ID{}, apperr.Validation("tayfa_id")
		}
	default:
		return id.ID{}, apperr.Validation("purpose")
	}
	if !declarable(in.ContentType) {
		return id.ID{}, apperr.Validation("content_type")
	}
	if in.SizeBytes < 1 || in.SizeBytes > MaxUploadBytes {
		return id.ID{}, apperr.Validation("size_bytes")
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return id.ID{}, apperr.Validation("client_request_id")
	}
	if !processable(in.ContentType) {
		return id.ID{}, apperr.ErrMediaTypeUnsupported
	}
	return crid, nil
}

// CreateUpload opens an upload for user and returns its PUT link
// (MediaService.CreateUpload).
func (s *Service) CreateUpload(ctx context.Context, user id.ID, in CreateInput) (Created, error) {
	if !s.Enabled() {
		return Created{}, apperr.ErrUnavailable
	}
	crid, err := validate(in)
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	var row store.Upload
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if in.Tayfa != (id.ID{}) {
			t, m, err := access.Load(ctx, q, in.Tayfa, user, false)
			if err != nil {
				return err
			}
			if err := access.Authorize(m, authz.TayfaView, now); err != nil {
				return err
			}
			if access.ReadOnly(t) {
				return apperr.ErrTayfaReadOnly
			}
		}
		if err := q.LockUserUploads(ctx, user.String()); err != nil {
			return fmt.Errorf("yükleme kilidi: %w", err)
		}
		prev, err := q.GetUploadByClientRequest(ctx, store.GetUploadByClientRequestParams{UserID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if prev.Purpose != in.Purpose || prev.ContentType != in.ContentType || prev.SizeBytes != in.SizeBytes ||
				prev.TayfaID != optional(in.Tayfa) || prev.Status == statusDeleting {
				return apperr.Validation("client_request_id")
			}
			row = prev
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		n, err := q.CountUploadsSince(ctx, store.CountUploadsSinceParams{UserID: user.PG(), Since: ts(now.Add(-time.Hour))})
		if err != nil {
			return fmt.Errorf("sıklık: %w", err)
		}
		if n >= int64(s.cfg.UploadsPerHour) {
			return apperr.ErrUploadRateLimited
		}
		used, err := q.UserStoredBytes(ctx, user.PG())
		if err != nil {
			return fmt.Errorf("kota: %w", err)
		}
		if used+in.SizeBytes > s.cfg.QuotaBytes {
			return apperr.ErrUploadQuotaExceeded
		}
		key, err := newKey()
		if err != nil {
			return err
		}
		uid := id.New()
		if _, err := q.CreateUpload(ctx, store.CreateUploadParams{
			ID: uid.PG(), UserID: user.PG(), TayfaID: optional(in.Tayfa), Purpose: in.Purpose,
			ContentType: in.ContentType, SizeBytes: in.SizeBytes, R2Key: key, ClientRequestID: crid.PG(),
		}); err != nil {
			return fmt.Errorf("yükleme kaydı: %w", err)
		}
		row, err = q.GetOwnUpload(ctx, store.GetOwnUploadParams{ID: uid.PG(), UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("yükleme kaydı: %w", err)
		}
		return nil
	})
	if err != nil {
		return Created{}, err
	}
	out := Created{Upload: uploadOf(row)}
	if row.Status != StatusPending {
		return out, nil
	}
	link, err := s.cfg.Store.PresignPut(incomingKey(row.R2Key), row.ContentType, row.SizeBytes, UploadLinkTTL)
	if err != nil {
		return Created{}, fmt.Errorf("yükleme linki: %w", err)
	}
	out.Link = link
	return out, nil
}

func optional(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}

// CompleteUpload checks the uploaded object and queues it for processing
// (MediaService.CompleteUpload).
func (s *Service) CompleteUpload(ctx context.Context, user, upload id.ID) (Upload, error) {
	if !s.Enabled() {
		return Upload{}, apperr.ErrUnavailable
	}
	row, err := s.ownUpload(ctx, user, upload)
	if err != nil {
		return Upload{}, err
	}
	if row.Status != StatusPending {
		return s.withURLs(row)
	}
	size, err := s.cfg.Store.Head(ctx, incomingKey(row.R2Key))
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		return Upload{}, apperr.ErrUploadNotReceived
	case err != nil:
		s.cfg.Log.WarnContext(ctx, "yüklenen dosya kontrol edilemedi", slog.String("upload_id", upload.String()), slog.String("err", err.Error()))
		return Upload{}, apperr.ErrUnavailable
	}
	if size != row.SizeBytes {
		// The signed PUT only takes the declared size; anything else means
		// the storage did not enforce it. Refuse and remove the object.
		if _, err := s.q.RejectPendingUpload(ctx, store.RejectPendingUploadParams{ID: row.ID, RejectReason: text("size_mismatch")}); err != nil {
			return Upload{}, fmt.Errorf("ret: %w", err)
		}
		s.cfg.Log.WarnContext(ctx, "yüklenen dosyanın boyutu bildirilenden farklı", slog.String("upload_id", upload.String()))
		if err := s.cfg.Store.Delete(ctx, incomingKey(row.R2Key)); err != nil {
			s.cfg.Log.WarnContext(ctx, "ham dosya silinemedi", slog.String("upload_id", upload.String()), slog.String("err", err.Error()))
		}
	} else if _, err := s.q.MarkUploadReceived(ctx, store.MarkUploadReceivedParams{ID: row.ID, Now: ts(s.now())}); err != nil {
		return Upload{}, fmt.Errorf("alındı: %w", err)
	} else {
		s.Wake()
	}
	row, err = s.ownUpload(ctx, user, upload)
	if err != nil {
		return Upload{}, err
	}
	return s.withURLs(row)
}

// GetUpload returns the caller's own upload (MediaService.GetUpload).
func (s *Service) GetUpload(ctx context.Context, user, upload id.ID) (Upload, error) {
	if !s.Enabled() {
		return Upload{}, apperr.ErrUnavailable
	}
	row, err := s.ownUpload(ctx, user, upload)
	if err != nil {
		return Upload{}, err
	}
	return s.withURLs(row)
}

func (s *Service) ownUpload(ctx context.Context, user, upload id.ID) (store.Upload, error) {
	row, err := s.q.GetOwnUpload(ctx, store.GetOwnUploadParams{ID: upload.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.Upload{}, apperr.ErrUploadNotFound
	case err != nil:
		return store.Upload{}, fmt.Errorf("yükleme: %w", err)
	}
	return row, nil
}

func (s *Service) withURLs(row store.Upload) (Upload, error) {
	out := uploadOf(row)
	if row.Status == StatusReady {
		u, err := s.urls(row.ID, row.R2Key, row.OutputType, row.Width, row.Height)
		if err != nil {
			return Upload{}, err
		}
		out.URLs = u
	}
	return out, nil
}

// Target is the content an upload is attached to.
type Target struct {
	Type string
	ID   id.ID
}

// Attach attaches the user's upload to target inside the caller's
// transaction q, before the content column is written (the column's
// trigger checks the attachment again). The upload must be the user's own,
// of purpose and of tayfa (zero for avatars), READY, and not attached to
// other content; attaching it to the same target again is a no-op.
func Attach(ctx context.Context, q *store.Queries, user, upload id.ID, purpose string, tayfa id.ID, target Target, now time.Time) error {
	row, err := q.GetOwnUploadForUpdate(ctx, store.GetOwnUploadForUpdateParams{ID: upload.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.ErrUploadNotFound
	case err != nil:
		return fmt.Errorf("yükleme: %w", err)
	}
	if row.Purpose != purpose || row.TayfaID != optional(tayfa) {
		return apperr.ErrUploadNotFound
	}
	if row.Status != StatusReady {
		return apperr.ErrUploadNotReady
	}
	if row.AttachedID.Valid && (row.AttachedType.String != target.Type || id.ID(row.AttachedID.Bytes) != target.ID) {
		return apperr.ErrUploadInUse
	}
	n, err := q.AttachUpload(ctx, store.AttachUploadParams{
		ID: upload.PG(), UserID: user.PG(), Purpose: purpose, TayfaID: optional(tayfa),
		AttachedType: target.Type, AttachedID: target.ID.PG(), Now: ts(now),
	})
	if err != nil {
		return fmt.Errorf("bağlama: %w", err)
	}
	if n != 1 {
		return apperr.ErrUploadNotFound
	}
	return nil
}

// AttachAvatar attaches the caller's READY avatar upload to the caller
// (MediaService.AttachAvatar; identity calls it with the caller's token
// before storing the avatar).
func (s *Service) AttachAvatar(ctx context.Context, user, upload id.ID) error {
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		return Attach(ctx, store.New(tx), user, upload, PurposeAvatar, id.ID{}, Target{Type: TargetUser, ID: user}, now)
	})
}

// UserProfileUpdated deletes the user's attached avatars other than the
// current one (identity's user.profile_updated; zero current: no avatar).
// q runs in the consumer's transaction; the method is idempotent.
func UserProfileUpdated(ctx context.Context, q *store.Queries, user, current id.ID) error {
	if _, err := q.ReleaseOtherAvatars(ctx, store.ReleaseOtherAvatarsParams{UserID: user.PG(), Keep: optional(current)}); err != nil {
		return fmt.Errorf("eski avatar: %w", err)
	}
	return nil
}

// UserDeleted deletes the user's files; covers attached to a tayfa or
// event stay with the content without an owner (identity's user.deleted).
// q runs in the consumer's transaction; the method is idempotent.
func UserDeleted(ctx context.Context, q *store.Queries, user id.ID) error {
	if _, err := q.DeleteUserUploads(ctx, user.PG()); err != nil {
		return fmt.Errorf("kullanıcının dosyaları: %w", err)
	}
	if _, err := q.DisownUserCovers(ctx, user.PG()); err != nil {
		return fmt.Errorf("kapak sahipliği: %w", err)
	}
	return nil
}

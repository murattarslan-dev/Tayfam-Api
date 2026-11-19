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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/inspect"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// VariantInput is a variant the client will PUT.
type VariantInput struct {
	Kind        string
	ContentType string
	SizeBytes   int64
}

// CreateInput is a new upload.
type CreateInput struct {
	Purpose string
	// Variants holds exactly one KindPreview and one KindFull.
	Variants []VariantInput
	// Tayfa is required for covers, log entry and album photos, zero for
	// avatars.
	Tayfa           id.ID
	ClientRequestID string
}

// PutLink is where to PUT one variant.
type PutLink struct {
	Kind string
	objstore.Presigned
}

// Created is a new upload and its PUT links; Links is empty when a
// repeated request finds the upload past PENDING.
type Created struct {
	Upload Upload
	Links  []PutLink
}

// variants returns the preview and the full variant of in. A DM
// attachment has exactly one blob, returned as full with no preview.
func variants(purpose string, in []VariantInput) (preview, full VariantInput, err error) {
	if purpose == PurposeDMAttachment {
		if len(in) != 1 || in[0].Kind != KindBlob || in[0].ContentType != BlobType ||
			in[0].SizeBytes < 1 || in[0].SizeBytes > MaxVariantBytes {
			return preview, full, apperr.Validation("variants")
		}
		return preview, in[0], nil
	}
	if len(in) != 2 {
		return preview, full, apperr.Validation("variants")
	}
	for _, v := range in {
		if !validType(v.ContentType) || v.SizeBytes < 1 || v.SizeBytes > MaxVariantBytes {
			return preview, full, apperr.Validation("variants")
		}
		switch {
		case v.Kind == KindPreview && preview.Kind == "":
			preview = v
		case v.Kind == KindFull && full.Kind == "":
			full = v
		default:
			return preview, full, apperr.Validation("variants")
		}
	}
	return preview, full, nil
}

func validate(in CreateInput) (id.ID, VariantInput, VariantInput, error) {
	switch in.Purpose {
	case PurposeAvatar, PurposeDMAttachment, PurposeReportEvidence:
		if in.Tayfa != (id.ID{}) {
			return id.ID{}, VariantInput{}, VariantInput{}, apperr.Validation("tayfa_id")
		}
	case PurposeCover, PurposeLogPhoto, PurposeEventPhoto, PurposeChatPhoto:
		if in.Tayfa == (id.ID{}) {
			return id.ID{}, VariantInput{}, VariantInput{}, apperr.Validation("tayfa_id")
		}
	default:
		return id.ID{}, VariantInput{}, VariantInput{}, apperr.Validation("purpose")
	}
	preview, full, err := variants(in.Purpose, in.Variants)
	if err != nil {
		return id.ID{}, VariantInput{}, VariantInput{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return id.ID{}, VariantInput{}, VariantInput{}, apperr.Validation("client_request_id")
	}
	return crid, preview, full, nil
}

// CreateUpload opens an upload for user and returns its PUT links
// (MediaService.CreateUpload).
func (s *Service) CreateUpload(ctx context.Context, user id.ID, in CreateInput) (Created, error) {
	if !s.Enabled() {
		return Created{}, apperr.ErrUnavailable
	}
	crid, preview, full, err := validate(in)
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
			if prev.Purpose != in.Purpose || prev.TayfaID != optional(in.Tayfa) || prev.Status == statusDeleting ||
				prev.PreviewType.String != preview.ContentType || prev.PreviewBytes.Int64 != preview.SizeBytes ||
				prev.FullType.String != full.ContentType || prev.FullBytes.Int64 != full.SizeBytes {
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
		if used+preview.SizeBytes+full.SizeBytes > s.cfg.QuotaBytes {
			return apperr.ErrUploadQuotaExceeded
		}
		key, err := newKey()
		if err != nil {
			return err
		}
		uid := id.New()
		if _, err := q.CreateUpload(ctx, store.CreateUploadParams{
			ID: uid.PG(), UserID: user.PG(), TayfaID: optional(in.Tayfa), Purpose: in.Purpose, R2Key: key,
			PreviewType: optText(preview.ContentType), PreviewBytes: optInt8(preview.SizeBytes),
			FullType: full.ContentType, FullBytes: full.SizeBytes, ClientRequestID: crid.PG(),
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
	vs := []VariantInput{
		{KindPreview, row.PreviewType.String, row.PreviewBytes.Int64}, {KindFull, row.FullType.String, row.FullBytes.Int64},
	}
	if row.Purpose == PurposeDMAttachment {
		vs = []VariantInput{{KindBlob, row.FullType.String, row.FullBytes.Int64}}
	}
	for _, v := range vs {
		key := v.Kind
		if key == KindBlob {
			key = KindFull
		}
		link, err := s.cfg.Store.PresignPut(incomingKey(row.R2Key, key), v.ContentType, v.SizeBytes, UploadLinkTTL)
		if err != nil {
			return Created{}, fmt.Errorf("yükleme linki: %w", err)
		}
		out.Links = append(out.Links, PutLink{Kind: v.Kind, Presigned: link})
	}
	return out, nil
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return text(s)
}

func optInt8(v int64) pgtype.Int8 {
	if v == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}

func optional(u id.ID) pgtype.UUID {
	if u == (id.ID{}) {
		return pgtype.UUID{}
	}
	return u.PG()
}

// checked is a variant that passed the header check.
type checked struct {
	kind   string
	etag   string
	width  int
	height int
}

// rejection is a refusal caused by the files.
type rejection struct{ reason string }

func (r rejection) Error() string { return "yükleme reddedildi: " + r.reason }

// check reads one variant's size and header. A rejection is the files'
// fault; apperr.ErrUploadNotReceived means the variant is not stored yet;
// any other error is the storage's and leaves the upload pending.
func (s *Service) check(ctx context.Context, key, kind, contentType string, size int64) (checked, error) {
	o, err := s.cfg.Store.Head(ctx, incomingKey(key, kind))
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		return checked{}, apperr.ErrUploadNotReceived
	case err != nil:
		return checked{}, err
	}
	if o.Size != size {
		return checked{}, rejection{"size_mismatch"}
	}
	head, err := s.cfg.Store.GetRange(ctx, incomingKey(key, kind), min(size, headBytes), o.ETag)
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		return checked{}, apperr.ErrUploadNotReceived
	case err != nil:
		return checked{}, err
	}
	info, err := inspect.Inspect(head, o.Size)
	var re *inspect.RejectError
	switch {
	case errors.As(err, &re):
		return checked{}, rejection{string(re.Reason)}
	case err != nil:
		return checked{}, err
	case info.Type != contentType:
		return checked{}, rejection{"not_image"}
	}
	return checked{kind: kind, etag: o.ETag, width: info.Width, height: info.Height}, nil
}

// dimensions checks the variants' sizes against the declared full size:
// the full variant exactly, the preview within its limit and with the same
// aspect ratio up to one pixel of rounding.
func dimensions(preview, full checked, width, height int) error {
	if max(full.width, full.height) > MaxFullSide || max(preview.width, preview.height) > MaxPreviewSide {
		return rejection{"too_large"}
	}
	if full.width != width || full.height != height || preview.width > full.width || preview.height > full.height {
		return rejection{"dimension_mismatch"}
	}
	if d := preview.width*full.height - preview.height*full.width; max(d, -d) > max(full.width, full.height) {
		return rejection{"dimension_mismatch"}
	}
	return nil
}

// CompleteUpload checks both variants and makes the upload READY or
// REJECTED (MediaService.CompleteUpload). width and height are the full
// variant's.
func (s *Service) CompleteUpload(ctx context.Context, user, upload id.ID, width, height int) (Upload, error) {
	if !s.Enabled() {
		return Upload{}, apperr.ErrUnavailable
	}
	if width < 0 || width > MaxFullSide {
		return Upload{}, apperr.Validation("width")
	}
	if height < 0 || height > MaxFullSide {
		return Upload{}, apperr.Validation("height")
	}
	row, err := s.ownUpload(ctx, user, upload)
	if err != nil {
		return Upload{}, err
	}
	blob := row.Purpose == PurposeDMAttachment
	switch {
	case blob && width != 0:
		return Upload{}, apperr.Validation("width")
	case blob && height != 0:
		return Upload{}, apperr.Validation("height")
	case !blob && (width < 1 || width > MaxFullSide):
		return Upload{}, apperr.Validation("width")
	case !blob && (height < 1 || height > MaxFullSide):
		return Upload{}, apperr.Validation("height")
	}
	if row.Status != StatusPending {
		return s.withURLs(row)
	}
	log := s.cfg.Log.With(slog.String("upload_id", upload.String()))
	if blob {
		err = s.verifyBlob(ctx, row)
	} else {
		err = s.verify(ctx, row, width, height)
	}
	var rj rejection
	switch {
	case errors.As(err, &rj):
		if _, err := s.q.RejectPendingUpload(ctx, store.RejectPendingUploadParams{ID: row.ID, RejectReason: text(rj.reason)}); err != nil {
			return Upload{}, fmt.Errorf("ret: %w", err)
		}
		log.InfoContext(ctx, "yükleme reddedildi", slog.String("reason", rj.reason))
		s.deleteKeys(ctx, log, incomingKey(row.R2Key, KindPreview), incomingKey(row.R2Key, KindFull))
	case errors.Is(err, apperr.ErrUploadNotReceived):
		return Upload{}, err
	case err != nil:
		log.WarnContext(ctx, "yükleme denetlenemedi; bekliyor", slog.String("err", err.Error()))
		return Upload{}, apperr.ErrUnavailable
	default:
		p := store.MarkUploadReadyParams{ID: row.ID, Now: ts(s.now())}
		if !blob {
			p.Width, p.Height = pgInt4(width), pgInt4(height)
		}
		if _, err := s.q.MarkUploadReady(ctx, p); err != nil {
			return Upload{}, fmt.Errorf("hazır: %w", err)
		}
		log.InfoContext(ctx, "yükleme hazır")
		s.deleteKeys(ctx, log, incomingKey(row.R2Key, KindPreview), incomingKey(row.R2Key, KindFull))
	}
	row, err = s.ownUpload(ctx, user, upload)
	if err != nil {
		return Upload{}, err
	}
	return s.withURLs(row)
}

// verify checks both variants and copies them out of quarantine, each
// bound to the ETag that was checked.
func (s *Service) verify(ctx context.Context, row store.Upload, width, height int) error {
	preview, err := s.check(ctx, row.R2Key, KindPreview, row.PreviewType.String, row.PreviewBytes.Int64)
	if err != nil {
		return err
	}
	full, err := s.check(ctx, row.R2Key, KindFull, row.FullType.String, row.FullBytes.Int64)
	if err != nil {
		return err
	}
	if err := dimensions(preview, full, width, height); err != nil {
		return err
	}
	for _, c := range []checked{preview, full} {
		err := s.cfg.Store.Copy(ctx, incomingKey(row.R2Key, c.kind), mediaKey(row.R2Key, c.kind), c.etag)
		if errors.Is(err, objstore.ErrNotFound) {
			return apperr.ErrUploadNotReceived
		}
		if err != nil {
			// ErrChanged: the client replaced the file after the check;
			// the next CompleteUpload checks it again.
			return err
		}
	}
	return nil
}

// verifyBlob checks a DM attachment: the stored object has the declared
// size, and it is copied out of quarantine bound to the checked ETag. The
// content is encrypted by the client and never read (ADR-0031): no range
// read, no type, metadata or dimension check.
func (s *Service) verifyBlob(ctx context.Context, row store.Upload) error {
	o, err := s.cfg.Store.Head(ctx, incomingKey(row.R2Key, KindFull))
	switch {
	case errors.Is(err, objstore.ErrNotFound):
		return apperr.ErrUploadNotReceived
	case err != nil:
		return err
	}
	if o.Size != row.FullBytes.Int64 {
		return rejection{"size_mismatch"}
	}
	err = s.cfg.Store.Copy(ctx, incomingKey(row.R2Key, KindFull), mediaKey(row.R2Key, KindFull), o.ETag)
	if errors.Is(err, objstore.ErrNotFound) {
		return apperr.ErrUploadNotReceived
	}
	return err
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
		u, err := s.urls(row.ID, row.R2Key, row.PreviewType.String, row.FullType.String, row.Width, row.Height)
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
		// A chat attachment chat has not confirmed may belong to a message
		// that was never written (ADR-0031): it moves to the owner's new
		// message, so a failed send can be retried.
		if !chatTarget(target.Type) || row.AttachedType.String != target.Type || row.AttachConfirmedAt.Valid {
			return apperr.ErrUploadInUse
		}
		n, err := q.MoveChatAttachment(ctx, store.MoveChatAttachmentParams{
			AttachedID: target.ID.PG(), Now: ts(now), ID: upload.PG(), UserID: user.PG(), Purpose: purpose,
			TayfaID: optional(tayfa), AttachedType: target.Type,
		})
		if err != nil {
			return fmt.Errorf("bağlama: %w", err)
		}
		if n != 1 {
			return apperr.ErrUploadInUse
		}
		return nil
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

func chatTarget(t string) bool { return t == TargetChannelMessage || t == TargetDMMessage }

// AttachChannelPhoto attaches the caller's READY chat photo of tayfa to the
// channel message (MediaService.AttachChannelPhoto; chat calls it with the
// caller's token while it writes the message, ADR-0031).
func (s *Service) AttachChannelPhoto(ctx context.Context, user, upload, tayfa, message id.ID) error {
	if !s.Enabled() {
		return apperr.ErrUnavailable
	}
	if tayfa == (id.ID{}) {
		return apperr.Validation("tayfa_id")
	}
	if message == (id.ID{}) {
		return apperr.Validation("message_id")
	}
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		return Attach(ctx, store.New(tx), user, upload, PurposeChatPhoto, tayfa, Target{Type: TargetChannelMessage, ID: message}, now)
	})
}

// AttachDirectMessageAttachment attaches the caller's READY DM attachment
// to the direct message (MediaService.AttachDirectMessageAttachment).
func (s *Service) AttachDirectMessageAttachment(ctx context.Context, user, upload, message id.ID) error {
	if !s.Enabled() {
		return apperr.ErrUnavailable
	}
	if message == (id.ID{}) {
		return apperr.Validation("message_id")
	}
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		return Attach(ctx, store.New(tx), user, upload, PurposeDMAttachment, id.ID{}, Target{Type: TargetDMMessage, ID: message}, now)
	})
}

// MessageMediaReleased deletes an upload still attached to message (chat's
// chat.media_released event, ADR-0031). q runs in the consumer's
// transaction; the method is idempotent.
func MessageMediaReleased(ctx context.Context, q *store.Queries, upload, message id.ID) error {
	if _, err := q.ReleaseMessageMedia(ctx, store.ReleaseMessageMediaParams{ID: upload.PG(), MessageID: message.PG()}); err != nil {
		return fmt.Errorf("mesaj dosyası: %w", err)
	}
	return nil
}

// TayfaPurged deletes a purged tayfa's channel photos inside the purge's
// transaction (ADR-0031).
func TayfaPurged(ctx context.Context, q *store.Queries, tayfa id.ID) error {
	if _, err := q.ReleaseTayfaChatPhotos(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("kanal fotoğrafları: %w", err)
	}
	return nil
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
// event stay with the content without an owner, and so does report
// evidence attached to a report (the platform's, ADR-0031) (identity's
// user.deleted).
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

// Package media is core's upload, check and serving of images (issue #135;
// ADR-0007, ADR-0020; data model v14 "Dosyalar", "Yüklenen dosya takibi").
//
// The mobile client processes the image: it strips the metadata and
// encodes two variants (preview ≤ 400 px, full ≤ 1600 px; JPEG or WebP).
// The files never pass through core: CreateUpload returns one presigned PUT
// per variant into quarantine (incoming/; 5 minutes, that key, type and
// size only). CompleteUpload checks both objects without decoding pixels
// (size with HEAD, headers with a ranged GET; internal/core/app/media/inspect)
// and copies them, bound to the checked ETag, into media/, which the client
// cannot write. READY uploads are attached by the content that uses them
// (Attach) and served with 1-hour GET links to those who may see the
// content (MediaURLs; core's permission table decides).
//
// Without object storage (Config.Store nil) every method returns
// apperr.ErrUnavailable and the cleanup worker idles; core runs anyway.
//
// Storage keys, links, the access key and the files' content or metadata
// are never logged and never part of an error message. Logs carry upload
// IDs, statuses, reasons and HTTP status codes only.
package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// Purposes (uploads.purpose).
const (
	PurposeAvatar     = "avatar"
	PurposeCatchPhoto = "catch_photo"
	PurposeCover      = "cover"
)

// Statuses (uploads.status). statusDeleting is never returned.
const (
	StatusPending  = "pending"
	StatusReady    = "ready"
	StatusRejected = "rejected"
	statusDeleting = "deleting"
)

// Attachment target types (uploads.attached_type).
const (
	TargetUser         = "user"
	TargetTayfa        = "tayfa"
	TargetEvent        = "event"
	TargetFishingCatch = "fishing_catch"
)

// Variant kinds.
const (
	KindPreview = "preview"
	KindFull    = "full"
)

// Limits and lifetimes (ADR-0007, ADR-0020).
const (
	// MaxVariantBytes is the largest variant (10 MiB).
	MaxVariantBytes = 10 << 20
	// MaxPreviewSide and MaxFullSide bound the longest side of the
	// variants in pixels.
	MaxPreviewSide = 400
	MaxFullSide    = 1600
	// headBytes is how much of a variant is read for the header check.
	headBytes = 256 << 10
	// UploadLinkTTL is how long a PUT link works.
	UploadLinkTTL = 5 * time.Minute
	// DownloadLinkTTL is how long a GET link works.
	DownloadLinkTTL = time.Hour
	// MaxURLRequest is the most uploads one GetMediaUrls may ask for.
	MaxURLRequest = 100

	// DefaultUploadsPerHour and DefaultQuotaBytes apply when the
	// configuration leaves them at zero.
	DefaultUploadsPerHour = 30
	DefaultQuotaBytes     = 500 << 20

	// PendingLifetime: an upload not completed by then is deleted (as the
	// incoming/ lifecycle rule in R2 does for the objects).
	PendingLifetime = 24 * time.Hour
	// RejectedLifetime: a rejected upload stays visible to its owner this
	// long.
	RejectedLifetime = 24 * time.Hour
	// UnattachedLifetime: a READY upload nothing attached is deleted after
	// this.
	UnattachedLifetime = 7 * 24 * time.Hour
)

// Variant types the client may use.
func validType(ct string) bool { return ct == "image/jpeg" || ct == "image/webp" }

// Store is the object storage (objstore.Client).
type Store interface {
	PresignPut(key, contentType string, size int64, ttl time.Duration) (objstore.Presigned, error)
	PresignGet(key string, ttl time.Duration) (objstore.Presigned, error)
	Head(ctx context.Context, key string) (objstore.Object, error)
	GetRange(ctx context.Context, key string, n int64, etag string) ([]byte, error)
	Copy(ctx context.Context, src, dst, etag string) error
	Delete(ctx context.Context, key string) error
}

// Config configures the Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Store is nil when no object storage is configured: media is off.
	Store Store
	// UploadsPerHour per user; 0 means DefaultUploadsPerHour.
	UploadsPerHour int
	// QuotaBytes per user; 0 means DefaultQuotaBytes.
	QuotaBytes int64
	Now        func() time.Time
	// CleanupInterval; 0 means 10 minutes.
	CleanupInterval time.Duration
}

// Service implements MediaService's business logic and the cleanup
// worker.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("media: eksik yapılandırma")
	}
	if cfg.UploadsPerHour <= 0 {
		cfg.UploadsPerHour = DefaultUploadsPerHour
	}
	if cfg.QuotaBytes <= 0 {
		cfg.QuotaBytes = DefaultQuotaBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 10 * time.Minute
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// Enabled reports whether object storage is configured.
func (s *Service) Enabled() bool { return s.cfg.Store != nil }

func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Object keys. The random part (uploads.r2_key) is unguessable; it never
// leaves core except inside a signed link. The client writes incoming/
// only; media/ holds checked copies.
func incomingKey(k, kind string) string { return "incoming/" + k + "/" + kind }
func mediaKey(k, kind string) string    { return "media/" + k + "/" + kind }

func newKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rastgele anahtar: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Upload is an upload as its owner sees it.
type Upload struct {
	ID           id.ID
	Purpose      string
	Status       string
	RejectReason string
	// TayfaID is zero for avatars.
	TayfaID id.ID
	// SizeBytes is both variants' declared sizes together.
	SizeBytes int64
	CreatedAt time.Time
	Attached  bool
	// URLs is set when Status is StatusReady.
	URLs *URLs
}

// URLs are the GET links of a READY upload.
type URLs struct {
	UploadID                            id.ID
	Preview, Full                       string
	PreviewContentType, FullContentType string
	// Width and Height are the full variant's.
	Width, Height int
	Expires       time.Time
}

func uploadOf(u store.Upload) Upload {
	out := Upload{
		ID: id.ID(u.ID.Bytes), Purpose: u.Purpose, Status: u.Status, RejectReason: u.RejectReason.String,
		SizeBytes: u.PreviewBytes + u.FullBytes, CreatedAt: u.CreatedAt.Time.UTC(), Attached: u.AttachedID.Valid,
	}
	if u.TayfaID.Valid {
		out.TayfaID = id.ID(u.TayfaID.Bytes)
	}
	return out
}

// urls signs the GET links of a checked upload.
func (s *Service) urls(uploadID pgtype.UUID, key, previewType, fullType string, w, h pgtype.Int4) (*URLs, error) {
	p, err := s.cfg.Store.PresignGet(mediaKey(key, KindPreview), DownloadLinkTTL)
	if err != nil {
		return nil, fmt.Errorf("önizleme linki: %w", err)
	}
	f, err := s.cfg.Store.PresignGet(mediaKey(key, KindFull), DownloadLinkTTL)
	if err != nil {
		return nil, fmt.Errorf("dosya linki: %w", err)
	}
	return &URLs{
		UploadID: id.ID(uploadID.Bytes), Preview: p.URL, Full: f.URL,
		PreviewContentType: previewType, FullContentType: fullType,
		Width: int(w.Int32), Height: int(h.Int32), Expires: f.Expires.UTC(),
	}, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func pgInt4(v int) pgtype.Int4 { return pgtype.Int4{Int32: int32(v), Valid: true} } //nolint:gosec // at most 1600

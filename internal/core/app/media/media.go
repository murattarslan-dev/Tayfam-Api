// Package media is core's upload, processing and serving of images (issue
// #135; ADR-0007, ADR-0020; data model v14 "Dosyalar", "Yüklenen dosya
// takibi").
//
// The file never passes through core: CreateUpload returns a presigned PUT
// link to R2 (5 minutes, that key, type and size only), CompleteUpload
// checks the object with HEAD and queues it, and the processing worker
// re-encodes it (internal/core/app/media/imaging) into media/ and deletes
// the raw file. READY uploads are attached by the content that uses them
// (Attach) and served with 1-hour GET links to those who may see the
// content (MediaURLs; core's permission table decides).
//
// Without object storage (Config.Store nil) every method returns
// apperr.ErrUnavailable and the workers idle; core runs anyway.
//
// Storage keys, links, the access key and the file's content or metadata
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

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/imaging"
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

// Statuses (uploads.status). StatusDeleting is never returned.
const (
	StatusPending    = "pending"
	StatusUploaded   = "uploaded"
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusRejected   = "rejected"
	statusDeleting   = "deleting"
)

// Attachment target types (uploads.attached_type).
const (
	TargetUser         = "user"
	TargetTayfa        = "tayfa"
	TargetEvent        = "event"
	TargetFishingCatch = "fishing_catch"
)

// Limits and lifetimes (ADR-0007, ADR-0020).
const (
	// MaxUploadBytes is the largest raw file (10 MiB).
	MaxUploadBytes = 10 << 20
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
	// incoming/ lifecycle rule in R2 does for the object).
	PendingLifetime = 24 * time.Hour
	// RejectedLifetime: a rejected upload stays visible to its owner this
	// long.
	RejectedLifetime = 24 * time.Hour
	// UnattachedLifetime: a READY upload nothing attached is deleted after
	// this.
	UnattachedLifetime = 7 * 24 * time.Hour
)

// Content types a client may declare. Only JPEG and PNG are processed
// (ADR-0020 option A); the others are part of the contract and refused
// with apperr.ErrMediaTypeUnsupported.
func declarable(ct string) bool {
	switch ct {
	case "image/jpeg", "image/png", "image/webp", "image/heic":
		return true
	}
	return false
}

// processable are the declared types the processor accepts.
func processable(ct string) bool { return ct == "image/jpeg" || ct == "image/png" }

// Store is the object storage (objstore.Client).
type Store interface {
	PresignPut(key, contentType string, size int64, ttl time.Duration) (objstore.Presigned, error)
	PresignGet(key string, ttl time.Duration) (objstore.Presigned, error)
	Head(ctx context.Context, key string) (int64, error)
	Get(ctx context.Context, key string, max int64) ([]byte, error)
	Put(ctx context.Context, key, contentType string, body []byte) error
	Delete(ctx context.Context, key string) error
}

// Processor re-encodes a raw file (imaging.Process). A *imaging.RejectError
// rejects the upload; any other error is retried.
type Processor func(data []byte) (imaging.Result, error)

// Config configures the Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Store is nil when no object storage is configured: media is off.
	Store Store
	// Process defaults to imaging.Process with imaging.Defaults.
	Process Processor
	// UploadsPerHour per user; 0 means DefaultUploadsPerHour.
	UploadsPerHour int
	// QuotaBytes per user; 0 means DefaultQuotaBytes.
	QuotaBytes int64
	Now        func() time.Time
	// PollInterval is how often the processing worker looks for work when
	// nothing woke it; 0 means 5 seconds.
	PollInterval time.Duration
	// CleanupInterval; 0 means 10 minutes.
	CleanupInterval time.Duration
}

// Service implements MediaService's business logic and the media workers.
type Service struct {
	cfg  Config
	q    *store.Queries
	wake chan struct{}
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("media: eksik yapılandırma")
	}
	if cfg.Process == nil {
		o := imaging.Defaults()
		cfg.Process = func(b []byte) (imaging.Result, error) { return imaging.Process(b, o) }
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
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 10 * time.Minute
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool), wake: make(chan struct{}, 1)}, nil
}

// Enabled reports whether object storage is configured.
func (s *Service) Enabled() bool { return s.cfg.Store != nil }

func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Object keys. The random part (uploads.r2_key) is unguessable; it never
// leaves core except inside a signed link.
func incomingKey(k string) string { return "incoming/" + k }
func previewKey(k string) string  { return "media/" + k + "/preview" }
func fullKey(k string) string     { return "media/" + k + "/full" }

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
	TayfaID   id.ID
	SizeBytes int64
	CreatedAt time.Time
	Attached  bool
	// URLs is set when Status is StatusReady.
	URLs *URLs
}

// URLs are the GET links of a READY upload.
type URLs struct {
	UploadID      id.ID
	Preview, Full string
	ContentType   string
	Width, Height int
	Expires       time.Time
}

func uploadOf(u store.Upload) Upload {
	out := Upload{
		ID: id.ID(u.ID.Bytes), Purpose: u.Purpose, Status: u.Status, RejectReason: u.RejectReason.String,
		SizeBytes: u.SizeBytes, CreatedAt: u.CreatedAt.Time.UTC(), Attached: u.AttachedID.Valid,
	}
	if u.TayfaID.Valid {
		out.TayfaID = id.ID(u.TayfaID.Bytes)
	}
	return out
}

// urls signs the GET links of a processed upload.
func (s *Service) urls(uploadID pgtype.UUID, key string, outputType pgtype.Text, w, h pgtype.Int4) (*URLs, error) {
	p, err := s.cfg.Store.PresignGet(previewKey(key), DownloadLinkTTL)
	if err != nil {
		return nil, fmt.Errorf("önizleme linki: %w", err)
	}
	f, err := s.cfg.Store.PresignGet(fullKey(key), DownloadLinkTTL)
	if err != nil {
		return nil, fmt.Errorf("dosya linki: %w", err)
	}
	return &URLs{
		UploadID: id.ID(uploadID.Bytes), Preview: p.URL, Full: f.URL, ContentType: outputType.String,
		Width: int(w.Int32), Height: int(h.Int32), Expires: f.Expires.UTC(),
	}, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func pgInt8(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }

func pgInt4(v int) pgtype.Int4 { return pgtype.Int4{Int32: int32(v), Valid: true} } //nolint:gosec // at most 1600

package dataexport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The clients below carry the caller's own token (auth.ForwardToken on the
// connection, ADR-0011): core and chat check it themselves and return only
// the caller's data and files. identity has no service identity for any of
// these calls.

// Source is one service's part of the export: it calls emit with a file
// name of the archive and that file's records, files in a stable order.
type Source interface {
	Export(ctx context.Context, emit func(file string, records [][]byte) error) error
	// Files are the archive files the source may send.
	Files() []string
}

// Storage keeps the encrypted file (core's MediaService export methods).
type Storage interface {
	Reserve(ctx context.Context, request id.ID) (id.ID, error)
	UploadURL(ctx context.Context, upload id.ID, size int64) (url string, headers map[string]string, err error)
	Complete(ctx context.Context, upload id.ID) (time.Time, error)
	DownloadURL(ctx context.Context, upload id.ID) (url string, expires time.Time, err error)
}

// ErrStorageNotFound: core does not know the export (deleted, expired or
// another user's).
var ErrStorageNotFound = errors.New("dışa aktarma dosyası yok")

// fileName turns a section enum value into its archive file:
// CORE_EXPORT_SECTION_EVENT_RSVPS becomes core/event_rsvps.json.
func fileName(dir, enum, prefix string) string {
	return dir + "/" + strings.ToLower(strings.TrimPrefix(enum, prefix)) + ".json"
}

func files(dir, prefix string, names map[int32]string) []string {
	nums := make([]int, 0, len(names))
	for n := range names {
		if n != 0 {
			nums = append(nums, int(n))
		}
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, fileName(dir, names[int32(n)], prefix)) //nolint:gosec // enum numbers
	}
	return out
}

// CoreClient is core's ExportService.
type CoreClient interface {
	ExportMyData(ctx context.Context, in *corev1.ExportMyDataRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[corev1.ExportMyDataResponse], error)
}

// CoreSource reads core's ExportMyData stream.
type CoreSource struct{ C CoreClient }

const corePrefix = "CORE_EXPORT_SECTION_"

// Files implements Source.
func (s CoreSource) Files() []string { return files("core", corePrefix, corev1.CoreExportSection_name) }

// Export implements Source.
func (s CoreSource) Export(ctx context.Context, emit func(string, [][]byte) error) error {
	st, err := s.C.ExportMyData(ctx, &corev1.ExportMyDataRequest{})
	if err != nil {
		return fmt.Errorf("core: %w", err)
	}
	for {
		m, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("core: %w", err)
		}
		if m.GetSection() == corev1.CoreExportSection_CORE_EXPORT_SECTION_UNSPECIFIED {
			return errors.New("core: bölümsüz kayıt")
		}
		if err := emit(fileName("core", m.GetSection().String(), corePrefix), m.GetRecords()); err != nil {
			return err
		}
	}
}

// ChatClient is chat's ExportService.
type ChatClient interface {
	ExportMyData(ctx context.Context, in *chatv1.ExportMyDataRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[chatv1.ExportMyDataResponse], error)
}

// ChatSource reads chat's ExportMyData stream.
type ChatSource struct{ C ChatClient }

const chatPrefix = "CHAT_EXPORT_SECTION_"

// Files implements Source.
func (s ChatSource) Files() []string { return files("chat", chatPrefix, chatv1.ChatExportSection_name) }

// Export implements Source.
func (s ChatSource) Export(ctx context.Context, emit func(string, [][]byte) error) error {
	st, err := s.C.ExportMyData(ctx, &chatv1.ExportMyDataRequest{})
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	for {
		m, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		if m.GetSection() == chatv1.ChatExportSection_CHAT_EXPORT_SECTION_UNSPECIFIED {
			return errors.New("chat: bölümsüz kayıt")
		}
		if err := emit(fileName("chat", m.GetSection().String(), chatPrefix), m.GetRecords()); err != nil {
			return err
		}
	}
}

// MediaClient is the part of core's MediaService the export uses.
type MediaClient interface {
	ReserveExportUpload(ctx context.Context, in *corev1.ReserveExportUploadRequest, opts ...grpc.CallOption) (*corev1.ReserveExportUploadResponse, error)
	GetExportUploadUrl(ctx context.Context, in *corev1.GetExportUploadUrlRequest, opts ...grpc.CallOption) (*corev1.GetExportUploadUrlResponse, error)
	CompleteExportUpload(ctx context.Context, in *corev1.CompleteExportUploadRequest, opts ...grpc.CallOption) (*corev1.CompleteExportUploadResponse, error)
	GetExportDownloadUrl(ctx context.Context, in *corev1.GetExportDownloadUrlRequest, opts ...grpc.CallOption) (*corev1.GetExportDownloadUrlResponse, error)
}

// CoreStorage is Storage over core's MediaService.
type CoreStorage struct{ C MediaClient }

// coreNotFound reports core's "no such export of the caller" answers.
func coreNotFound(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == "core.tayfam.app" {
			switch info.GetReason() {
			case corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND.String(), corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY.String():
				return true
			}
		}
	}
	return false
}

// Reserve implements Storage.
func (s CoreStorage) Reserve(ctx context.Context, request id.ID) (id.ID, error) {
	r, err := s.C.ReserveExportUpload(ctx, &corev1.ReserveExportUploadRequest{DataExportId: request.String()})
	if err != nil {
		return id.ID{}, fmt.Errorf("core: %w", err)
	}
	u, ok := id.Parse(r.GetUploadId())
	if !ok {
		return id.ID{}, errors.New("core: geçersiz yükleme kimliği")
	}
	return u, nil
}

// UploadURL implements Storage.
func (s CoreStorage) UploadURL(ctx context.Context, upload id.ID, size int64) (string, map[string]string, error) {
	r, err := s.C.GetExportUploadUrl(ctx, &corev1.GetExportUploadUrlRequest{UploadId: upload.String(), SizeBytes: size})
	if err != nil {
		return "", nil, fmt.Errorf("core: %w", err)
	}
	return r.GetUrl(), r.GetHeaders(), nil
}

// Complete implements Storage.
func (s CoreStorage) Complete(ctx context.Context, upload id.ID) (time.Time, error) {
	r, err := s.C.CompleteExportUpload(ctx, &corev1.CompleteExportUploadRequest{UploadId: upload.String()})
	if err != nil {
		return time.Time{}, fmt.Errorf("core: %w", err)
	}
	return r.GetExpireTime().AsTime(), nil
}

// DownloadURL implements Storage.
func (s CoreStorage) DownloadURL(ctx context.Context, upload id.ID) (string, time.Time, error) {
	r, err := s.C.GetExportDownloadUrl(ctx, &corev1.GetExportDownloadUrlRequest{UploadId: upload.String()})
	if coreNotFound(err) {
		return "", time.Time{}, ErrStorageNotFound
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("core: %w", err)
	}
	return r.GetUrl(), r.GetExpireTime().AsTime(), nil
}

// grpcCode is the status code of err for logs: never the message.
func grpcCode(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Code().String()
	}
	return codes.Unknown.String()
}

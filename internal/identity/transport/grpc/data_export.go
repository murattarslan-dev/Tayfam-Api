package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/dataexport"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// DataExports is what DataExportServer needs from the app layer
// (dataexport.Service).
type DataExports interface {
	Create(ctx context.Context, c dataexport.Caller, clientRequestID id.ID) (dataexport.Request, error)
	List(ctx context.Context, user id.ID) ([]dataexport.Request, time.Time, error)
	Get(ctx context.Context, user, request id.ID) (dataexport.Request, error)
	Download(ctx context.Context, c dataexport.Caller, request id.ID) (dataexport.Download, error)
}

// DataExportServer implements identityv1.DataExportServiceServer (issue
// #144). Like AccountServer it accepts full access tokens only; the caller
// always comes from the verified token. The download link and the key go
// to the caller only and are never logged.
type DataExportServer struct {
	identityv1.UnimplementedDataExportServiceServer
	app         DataExports
	apiAudience string
	log         *slog.Logger
}

// NewDataExportServer returns a DataExportServer.
func NewDataExportServer(app DataExports, apiAudience string, log *slog.Logger) *DataExportServer {
	return &DataExportServer{app: app, apiAudience: apiAudience, log: log}
}

func (s *DataExportServer) caller(ctx context.Context) (dataexport.Caller, error) {
	c, ok := auth.CallerFrom(ctx)
	if !ok || s.apiAudience == "" || c.Audience != s.apiAudience {
		return dataexport.Caller{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	u, okU := id.Parse(c.Subject)
	sid, okS := id.Parse(c.SessionID)
	if !okU || !okS {
		return dataexport.Caller{}, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	return dataexport.Caller{User: u, Session: sid, IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt}, nil
}

var exportStatuses = map[string]identityv1.DataExportStatus{
	dataexport.StatusPending:    identityv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING,
	dataexport.StatusProcessing: identityv1.DataExportStatus_DATA_EXPORT_STATUS_PROCESSING,
	dataexport.StatusReady:      identityv1.DataExportStatus_DATA_EXPORT_STATUS_READY,
	dataexport.StatusExpired:    identityv1.DataExportStatus_DATA_EXPORT_STATUS_EXPIRED,
	dataexport.StatusFailed:     identityv1.DataExportStatus_DATA_EXPORT_STATUS_FAILED,
}

var exportFailures = map[string]identityv1.DataExportFailureReason{
	dataexport.FailUnavailable: identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_UNAVAILABLE,
	dataexport.FailTooLarge:    identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TOO_LARGE,
	dataexport.FailTimeout:     identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TIMEOUT,
	dataexport.FailFailed:      identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_FAILED,
}

func optionalTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() || t.Unix() <= 0 {
		return nil
	}
	return timestamppb.New(t)
}

func exportToProto(r dataexport.Request) *identityv1.DataExport {
	return &identityv1.DataExport{
		Id: r.ID.String(), Status: exportStatuses[r.Status], FailureReason: exportFailures[r.FailureReason],
		RequestTime: timestamppb.New(r.RequestedAt), ReadyTime: optionalTime(r.ReadyAt),
		ExpireTime: optionalTime(r.ExpiresAt), DownloadTime: optionalTime(r.DownloadedAt), SizeBytes: r.SizeBytes,
	}
}

// CreateDataExport implements identityv1.DataExportServiceServer.
func (s *DataExportServer) CreateDataExport(ctx context.Context, req *identityv1.CreateDataExportRequest) (*identityv1.CreateDataExportResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	crid, ok := id.Parse(req.GetClientRequestId())
	if !ok {
		return nil, fail(codes.InvalidArgument, identityv1.ErrorReason_ERROR_REASON_VALIDATION)
	}
	r, err := s.app.Create(ctx, c, crid)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.CreateDataExportResponse{DataExport: exportToProto(r)}, nil
}

// ListDataExports implements identityv1.DataExportServiceServer.
func (s *DataExportServer) ListDataExports(ctx context.Context, _ *identityv1.ListDataExportsRequest) (*identityv1.ListDataExportsResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.List(ctx, c.User)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	out := &identityv1.ListDataExportsResponse{DataExports: make([]*identityv1.DataExport, 0, len(list)), NextRequestTime: optionalTime(next)}
	for _, r := range list {
		out.DataExports = append(out.DataExports, exportToProto(r))
	}
	return out, nil
}

// GetDataExport implements identityv1.DataExportServiceServer.
func (s *DataExportServer) GetDataExport(ctx context.Context, req *identityv1.GetDataExportRequest) (*identityv1.GetDataExportResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	rid, ok := id.Parse(req.GetDataExportId())
	if !ok {
		return nil, fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_NOT_FOUND)
	}
	r, err := s.app.Get(ctx, c.User, rid)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.GetDataExportResponse{DataExport: exportToProto(r)}, nil
}

// GetDataExportDownload implements identityv1.DataExportServiceServer.
func (s *DataExportServer) GetDataExportDownload(ctx context.Context, req *identityv1.GetDataExportDownloadRequest) (*identityv1.GetDataExportDownloadResponse, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	rid, ok := id.Parse(req.GetDataExportId())
	if !ok {
		return nil, fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_NOT_FOUND)
	}
	d, err := s.app.Download(ctx, c, rid)
	if err != nil {
		return nil, s.mapError(ctx, err)
	}
	return &identityv1.GetDataExportDownloadResponse{
		Url: d.URL, ExpireTime: timestamppb.New(d.Expires), Key: d.Key, SizeBytes: d.Size, Format: d.Format,
	}, nil
}

func (s *DataExportServer) mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, dataexport.ErrNotFound):
		return fail(codes.NotFound, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_NOT_FOUND)
	case errors.Is(err, dataexport.ErrInProgress):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_IN_PROGRESS)
	case errors.Is(err, dataexport.ErrLimit):
		return fail(codes.ResourceExhausted, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_LIMIT)
	case errors.Is(err, dataexport.ErrNotReady):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_NOT_READY)
	case errors.Is(err, dataexport.ErrExpired):
		return fail(codes.FailedPrecondition, identityv1.ErrorReason_ERROR_REASON_DATA_EXPORT_EXPIRED)
	case errors.Is(err, dataexport.ErrReauthenticationRequired):
		return fail(codes.PermissionDenied, identityv1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED)
	case errors.Is(err, dataexport.ErrUnavailable):
		return status.Error(codes.Unavailable, "istek reddedildi")
	case errors.Is(err, dataexport.ErrTokenExpiring), errors.Is(err, dataexport.ErrUnknownUser):
		// The client refreshes its token (or signs in) and asks again.
		return status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	default:
		s.log.ErrorContext(ctx, "dışa aktarma işlemi başarısız", slog.String("err", err.Error()))
		return status.Error(codes.Internal, "iç hata")
	}
}

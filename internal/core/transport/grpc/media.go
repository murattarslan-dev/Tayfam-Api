package grpc

import (
	"context"
	"log/slog"
	"net/http"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Media is what MediaServer needs from the app layer (media.Service).
type Media interface {
	CreateUpload(ctx context.Context, user id.ID, in media.CreateInput) (media.Created, error)
	CompleteUpload(ctx context.Context, user, upload id.ID, width, height int) (media.Upload, error)
	GetUpload(ctx context.Context, user, upload id.ID) (media.Upload, error)
	MediaURLs(ctx context.Context, viewer id.ID, ids []id.ID) ([]media.URLs, error)
	AttachAvatar(ctx context.Context, user, upload id.ID) error
}

// MediaServer implements corev1.MediaServiceServer. The owner and the
// viewer always come from the verified token. Signed links are returned to
// the caller only and never logged.
type MediaServer struct {
	corev1.UnimplementedMediaServiceServer
	app Media
	log *slog.Logger
}

// NewMediaServer returns a MediaServer.
func NewMediaServer(app Media, log *slog.Logger) *MediaServer {
	return &MediaServer{app: app, log: log}
}

var uploadPurposes = map[corev1.UploadPurpose]string{
	corev1.UploadPurpose_UPLOAD_PURPOSE_AVATAR:      media.PurposeAvatar,
	corev1.UploadPurpose_UPLOAD_PURPOSE_CATCH_PHOTO: media.PurposeCatchPhoto,
	corev1.UploadPurpose_UPLOAD_PURPOSE_COVER:       media.PurposeCover,
}

var uploadStatuses = map[string]corev1.UploadStatus{
	media.StatusPending:  corev1.UploadStatus_UPLOAD_STATUS_PENDING,
	media.StatusReady:    corev1.UploadStatus_UPLOAD_STATUS_READY,
	media.StatusRejected: corev1.UploadStatus_UPLOAD_STATUS_REJECTED,
}

var rejectReasons = map[string]corev1.UploadRejectReason{
	"not_image":          corev1.UploadRejectReason_UPLOAD_REJECT_REASON_NOT_IMAGE,
	"unsupported_type":   corev1.UploadRejectReason_UPLOAD_REJECT_REASON_UNSUPPORTED_TYPE,
	"metadata_present":   corev1.UploadRejectReason_UPLOAD_REJECT_REASON_METADATA_PRESENT,
	"dimension_mismatch": corev1.UploadRejectReason_UPLOAD_REJECT_REASON_DIMENSION_MISMATCH,
	"size_mismatch":      corev1.UploadRejectReason_UPLOAD_REJECT_REASON_SIZE_MISMATCH,
	"too_large":          corev1.UploadRejectReason_UPLOAD_REJECT_REASON_TOO_LARGE,
	"failed":             corev1.UploadRejectReason_UPLOAD_REJECT_REASON_FAILED,
}

var variantKinds = map[corev1.VariantKind]string{
	corev1.VariantKind_VARIANT_KIND_PREVIEW: media.KindPreview,
	corev1.VariantKind_VARIANT_KIND_FULL:    media.KindFull,
}

func kindToProto(k string) corev1.VariantKind {
	if k == media.KindPreview {
		return corev1.VariantKind_VARIANT_KIND_PREVIEW
	}
	return corev1.VariantKind_VARIANT_KIND_FULL
}

func purposeToProto(p string) corev1.UploadPurpose {
	for k, v := range uploadPurposes {
		if v == p {
			return k
		}
	}
	return corev1.UploadPurpose_UPLOAD_PURPOSE_UNSPECIFIED
}

func uploadToProto(u media.Upload) *corev1.Upload {
	out := &corev1.Upload{
		Id: u.ID.String(), Purpose: purposeToProto(u.Purpose), Status: uploadStatuses[u.Status],
		RejectReason: rejectReasons[u.RejectReason], TayfaId: optionalID(u.TayfaID),
		SizeBytes: u.SizeBytes, CreateTime: timestamppb.New(u.CreatedAt), Attached: u.Attached,
	}
	if u.URLs != nil {
		out.Urls = urlsToProto(*u.URLs)
	}
	return out
}

func urlsToProto(u media.URLs) *corev1.MediaUrls {
	return &corev1.MediaUrls{
		UploadId: u.UploadID.String(), PreviewUrl: u.Preview, PreviewContentType: u.PreviewContentType,
		FullUrl: u.Full, FullContentType: u.FullContentType,
		Width: int32(u.Width), Height: int32(u.Height), ExpireTime: timestamppb.New(u.Expires), //nolint:gosec // at most 1600
	}
}

// optionalUploadID parses an optional upload ID field ("" is zero).
func optionalUploadID(s, field string) (id.ID, error) {
	if s == "" {
		return id.ID{}, nil
	}
	u, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation(field)
	}
	return u, nil
}

func uploadArg(ctx context.Context, s string) (id.ID, id.ID, error) {
	u, err := callerID(ctx)
	if err != nil {
		return id.ID{}, id.ID{}, err
	}
	up, ok := id.Parse(s)
	if !ok {
		return id.ID{}, id.ID{}, validation("upload_id")
	}
	return u, up, nil
}

// CreateUpload implements corev1.MediaServiceServer.
func (s *MediaServer) CreateUpload(ctx context.Context, req *corev1.CreateUploadRequest) (*corev1.CreateUploadResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	purpose, ok := uploadPurposes[req.GetPurpose()]
	if !ok {
		return nil, validation("purpose")
	}
	in := media.CreateInput{Purpose: purpose, ClientRequestID: req.GetClientRequestId()}
	if len(req.GetVariants()) > 2 {
		return nil, validation("variants")
	}
	for _, v := range req.GetVariants() {
		kind, ok := variantKinds[v.GetKind()]
		if !ok {
			return nil, validation("variants")
		}
		in.Variants = append(in.Variants, media.VariantInput{Kind: kind, ContentType: v.GetContentType(), SizeBytes: v.GetSizeBytes()})
	}
	if req.GetTayfaId() != "" {
		t, err := tayfaID(req.GetTayfaId())
		if err != nil {
			return nil, err
		}
		in.Tayfa = t
	}
	c, err := s.app.CreateUpload(ctx, u, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.CreateUploadResponse{Upload: uploadToProto(c.Upload)}
	for _, l := range c.Links {
		out.Targets = append(out.Targets, &corev1.UploadTarget{
			Kind: kindToProto(l.Kind), Method: http.MethodPut, Url: l.URL, Headers: l.Headers, ExpireTime: timestamppb.New(l.Expires),
		})
	}
	return out, nil
}

// CompleteUpload implements corev1.MediaServiceServer.
func (s *MediaServer) CompleteUpload(ctx context.Context, req *corev1.CompleteUploadRequest) (*corev1.CompleteUploadResponse, error) {
	u, up, err := uploadArg(ctx, req.GetUploadId())
	if err != nil {
		return nil, err
	}
	res, err := s.app.CompleteUpload(ctx, u, up, int(req.GetWidth()), int(req.GetHeight()))
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CompleteUploadResponse{Upload: uploadToProto(res)}, nil
}

// GetUpload implements corev1.MediaServiceServer.
func (s *MediaServer) GetUpload(ctx context.Context, req *corev1.GetUploadRequest) (*corev1.GetUploadResponse, error) {
	u, up, err := uploadArg(ctx, req.GetUploadId())
	if err != nil {
		return nil, err
	}
	res, err := s.app.GetUpload(ctx, u, up)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetUploadResponse{Upload: uploadToProto(res)}, nil
}

// GetMediaUrls implements corev1.MediaServiceServer.
func (s *MediaServer) GetMediaUrls(ctx context.Context, req *corev1.GetMediaUrlsRequest) (*corev1.GetMediaUrlsResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetUploadIds()) == 0 || len(req.GetUploadIds()) > media.MaxURLRequest {
		return nil, validation("upload_ids")
	}
	ids := make([]id.ID, 0, len(req.GetUploadIds()))
	for _, raw := range req.GetUploadIds() {
		v, ok := id.Parse(raw)
		if !ok {
			return nil, validation("upload_ids")
		}
		ids = append(ids, v)
	}
	res, err := s.app.MediaURLs(ctx, u, ids)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetMediaUrlsResponse{Media: make([]*corev1.MediaUrls, 0, len(res))}
	for _, m := range res {
		out.Media = append(out.Media, urlsToProto(m))
	}
	return out, nil
}

// AttachAvatar implements corev1.MediaServiceServer.
func (s *MediaServer) AttachAvatar(ctx context.Context, req *corev1.AttachAvatarRequest) (*corev1.AttachAvatarResponse, error) {
	u, up, err := uploadArg(ctx, req.GetUploadId())
	if err != nil {
		return nil, err
	}
	if err := s.app.AttachAvatar(ctx, u, up); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AttachAvatarResponse{}, nil
}

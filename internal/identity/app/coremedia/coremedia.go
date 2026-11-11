// Package coremedia asks core's MediaService to attach an avatar upload
// (issue #135, ADR-0020 item 3). identity does not own media: the call
// carries the caller's own access token (auth.ForwardToken on the
// connection), so core checks the upload against the verified caller, not
// against anything identity says.
package coremedia

import (
	"context"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// coreDomain is the ErrorInfo domain of core's errors.
const coreDomain = "core.tayfam.app"

// Client is the part of core's MediaService identity calls.
type Client interface {
	AttachAvatar(ctx context.Context, in *corev1.AttachAvatarRequest, opts ...grpc.CallOption) (*corev1.AttachAvatarResponse, error)
}

// Media implements account.Media.
type Media struct{ c Client }

// New returns a Media over c.
func New(c Client) *Media { return &Media{c: c} }

// AttachAvatar implements account.Media. core's refusals become the
// account errors; anything else (core unreachable, media off, a token core
// refused) is account.ErrMediaUnavailable.
func (m *Media) AttachAvatar(ctx context.Context, upload id.ID) error {
	_, err := m.c.AttachAvatar(ctx, &corev1.AttachAvatarRequest{UploadId: upload.String()})
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %w", account.ErrMediaUnavailable, err)
	}
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != coreDomain {
			continue
		}
		switch info.GetReason() {
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND.String(), corev1.ErrorReason_ERROR_REASON_VALIDATION.String():
			return account.ErrUploadNotFound
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_READY.String():
			return account.ErrUploadNotReady
		case corev1.ErrorReason_ERROR_REASON_UPLOAD_IN_USE.String():
			return account.ErrUploadInUse
		}
	}
	return fmt.Errorf("%w: %s", account.ErrMediaUnavailable, st.Code().String())
}

package coremembers

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
)

// ResolutionClient is the part of core's SocialService that answers how
// requests stand (issue #192).
type ResolutionClient interface {
	GetRequestResolutions(ctx context.Context, in *corev1.GetRequestResolutionsRequest, opts ...grpc.CallOption) (*corev1.GetRequestResolutionsResponse, error)
}

// Resolutions implements notification.Resolver with core, with the
// caller's own token (the connection forwards it).
type Resolutions struct{ core ResolutionClient }

// NewResolutions returns Resolutions.
func NewResolutions(core ResolutionClient) *Resolutions { return &Resolutions{core: core} }

var resolutionNames = map[corev1.RequestResolution]string{
	corev1.RequestResolution_REQUEST_RESOLUTION_PENDING:   "pending",
	corev1.RequestResolution_REQUEST_RESOLUTION_ACCEPTED:  "accepted",
	corev1.RequestResolution_REQUEST_RESOLUTION_DECLINED:  "declined",
	corev1.RequestResolution_REQUEST_RESOLUTION_WITHDRAWN: "withdrawn",
	corev1.RequestResolution_REQUEST_RESOLUTION_EXPIRED:   "expired",
}

// Resolve implements notification.Resolver. An unknown value from core is
// left empty.
func (r *Resolutions) Resolve(ctx context.Context, refs []notification.RequestRef) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := &corev1.GetRequestResolutionsRequest{Refs: make([]*corev1.RequestRef, 0, len(refs))}
	for _, x := range refs {
		kind := corev1.RequestKind_REQUEST_KIND_FRIEND_REQUEST
		switch {
		case x.Invitation:
			kind = corev1.RequestKind_REQUEST_KIND_TAYFA_INVITATION
		case x.Suggestion:
			kind = corev1.RequestKind_REQUEST_KIND_FRIEND_SUGGESTION
		}
		req.Refs = append(req.Refs, &corev1.RequestRef{Kind: kind, Id: x.ID.String()})
	}
	resp, err := r.core.GetRequestResolutions(ctx, req)
	if err != nil {
		if c := status.Code(err); c == codes.Unavailable || c == codes.DeadlineExceeded {
			return nil, apperr.ErrUnavailable
		}
		return nil, fmt.Errorf("core istek durumu: %s", status.Code(err))
	}
	out := make([]string, 0, len(resp.GetResolutions()))
	for _, v := range resp.GetResolutions() {
		out = append(out, resolutionNames[v])
	}
	return out, nil
}

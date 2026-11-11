package rest

import (
	"context"
	"net/http"

	"google.golang.org/grpc"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// AvatarClient is the avatar part of identity's AccountService (issue #135,
// ADR-0020). identity asks core about the upload with the caller's own
// token.
type AvatarClient interface {
	SetAvatar(ctx context.Context, in *identityv1.SetAvatarRequest, opts ...grpc.CallOption) (*identityv1.SetAvatarResponse, error)
	ClearAvatar(ctx context.Context, in *identityv1.ClearAvatarRequest, opts ...grpc.CallOption) (*identityv1.ClearAvatarResponse, error)
}

const maxAvatarBody = 256

func registerAvatar(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("PUT /v1/me/avatar", a(h.putAvatar))
	mux.HandleFunc("DELETE /v1/me/avatar", a(h.deleteAvatar))
}

type avatarBody struct {
	UploadID string `json:"upload_id"`
}

type avatarJSON struct {
	AvatarUploadID string `json:"avatar_upload_id"`
}

func (h coreHandlers) putAvatar(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b avatarBody
	if !decodeLimit(w, r, &b, maxAvatarBody) {
		return
	}
	if !validUUID(b.UploadID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Avatars.SetAvatar(ctx, &identityv1.SetAvatarRequest{UploadId: b.UploadID})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, avatarJSON{AvatarUploadID: resp.GetAvatarUploadId()})
}

func (h coreHandlers) deleteAvatar(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.Avatars.ClearAvatar(ctx, &identityv1.ClearAvatarRequest{}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

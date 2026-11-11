package rest

import (
	"context"
	"net/http"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

// PushClient is chat's PushService as the gateway uses it (issue #134,
// ADR-0019). The push token belongs to the session of the caller's access
// token; the gateway checks the shape only and never logs the token.
type PushClient interface {
	RegisterPushToken(ctx context.Context, in *chatv1.RegisterPushTokenRequest, opts ...grpc.CallOption) (*chatv1.RegisterPushTokenResponse, error)
	UnregisterPushToken(ctx context.Context, in *chatv1.UnregisterPushTokenRequest, opts ...grpc.CallOption) (*chatv1.UnregisterPushTokenResponse, error)
}

// maxPushTokenBody fits a 1 KiB token and the platform.
const maxPushTokenBody = 2 << 10

func registerPush(mux *http.ServeMux, h chatHandlers) {
	a := func(next coreHandler) http.HandlerFunc { return authenticated(h.d.Verifier, h.d.Log, next) }
	mux.HandleFunc("PUT /v1/me/push-token", a(h.putPushToken))
	mux.HandleFunc("DELETE /v1/me/push-token", a(h.deletePushToken))
}

var pushPlatformValues = map[string]chatv1.PushPlatform{
	"android": chatv1.PushPlatform_PUSH_PLATFORM_ANDROID,
	"ios":     chatv1.PushPlatform_PUSH_PLATFORM_IOS,
}

type pushTokenBody struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

func (h chatHandlers) putPushToken(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b pushTokenBody
	if !decodeLimit(w, r, &b, maxPushTokenBody) {
		return
	}
	platform, ok := pushPlatformValues[b.Platform]
	if !ok || b.Token == "" || len(b.Token) > 1024 {
		badRequest(w, r)
		return
	}
	if _, err := h.d.Push.RegisterPushToken(ctx, &chatv1.RegisterPushTokenRequest{Token: b.Token, Platform: platform}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) deletePushToken(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.Push.UnregisterPushToken(ctx, &chatv1.UnregisterPushTokenRequest{}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

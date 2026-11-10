package rest

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

func (f *fakeChat) RegisterPushToken(ctx context.Context, in *chatv1.RegisterPushTokenRequest, _ ...grpc.CallOption) (*chatv1.RegisterPushTokenResponse, error) {
	return &chatv1.RegisterPushTokenResponse{}, f.record(ctx, in)
}

func (f *fakeChat) UnregisterPushToken(ctx context.Context, in *chatv1.UnregisterPushTokenRequest, _ ...grpc.CallOption) (*chatv1.UnregisterPushTokenResponse, error) {
	return &chatv1.UnregisterPushTokenResponse{}, f.record(ctx, in)
}

func TestPushTokenRoutes(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer USERTOKEN", clientHeader: "android"}
	f := &fakeChat{}
	h := chatRouter(f)

	w := do(h, "PUT", "/v1/me/push-token", `{"token":"tok:abc","platform":"ios"}`, auth)
	got, tok, _ := f.last()
	want := &chatv1.RegisterPushTokenRequest{Token: "tok:abc", Platform: chatv1.PushPlatform_PUSH_PLATFORM_IOS}
	if w.Code != 204 || tok != "USERTOKEN" || !proto.Equal(got, want) {
		t.Fatalf("kayıt: %d %q %v", w.Code, tok, got)
	}
	w = do(h, "DELETE", "/v1/me/push-token", "", auth)
	got, _, _ = f.last()
	if w.Code != 204 || !proto.Equal(got, &chatv1.UnregisterPushTokenRequest{}) {
		t.Fatalf("silme: %d %v", w.Code, got)
	}

	// Shape is checked before chat is called.
	_, _, calls := f.last()
	for name, body := range map[string]string{
		"platform yok": `{"token":"tok:abc"}`,
		"web":          `{"token":"tok:abc","platform":"web"}`,
		"token yok":    `{"platform":"android"}`,
		"bilinmeyen":   `{"token":"tok:abc","platform":"android","user_id":"x"}`,
	} {
		if w := do(h, "PUT", "/v1/me/push-token", body, auth); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if _, _, n := f.last(); n != calls {
		t.Fatalf("geçersiz istek chat'e gitti")
	}
	if w := do(h, "PUT", "/v1/me/push-token", `{"token":"tok:abc","platform":"ios"}`, map[string]string{clientHeader: "android"}); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}

	for _, tc := range []struct {
		err  error
		code int
	}{
		{status.Error(codes.Unauthenticated, "x"), 401},
		{chatErr(codes.ResourceExhausted, "ERROR_REASON_RATE_LIMITED"), 429},
		{chatErr(codes.InvalidArgument, "ERROR_REASON_VALIDATION"), 400},
		{status.Error(codes.Unavailable, "x"), 503},
	} {
		f.err = tc.err
		if w := do(h, "PUT", "/v1/me/push-token", `{"token":"tok:abc","platform":"android"}`, auth); w.Code != tc.code {
			t.Errorf("%v: %d", tc.err, w.Code)
		}
	}
}

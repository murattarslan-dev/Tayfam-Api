package grpc

import (
	"context"
	"log/slog"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
)

// Push is what PushServer needs from the app layer (push.Service).
type Push interface {
	Register(ctx context.Context, c push.Caller, platform, token string) error
	Unregister(ctx context.Context, c push.Caller) error
}

// PushServer implements chatv1.PushServiceServer (issue #134). Every method
// acts for the session of the verified access token. It never logs request
// fields: the push token must not reach any log.
type PushServer struct {
	chatv1.UnimplementedPushServiceServer
	app Push
	log *slog.Logger
}

// NewPushServer returns a PushServer.
func NewPushServer(app Push, log *slog.Logger) *PushServer {
	return &PushServer{app: app, log: log}
}

var pushPlatforms = map[chatv1.PushPlatform]string{
	chatv1.PushPlatform_PUSH_PLATFORM_ANDROID: push.PlatformAndroid,
	chatv1.PushPlatform_PUSH_PLATFORM_IOS:     push.PlatformIOS,
}

func pushCaller(ctx context.Context) (push.Caller, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return push.Caller{}, err
	}
	return push.Caller{User: c.User, Session: c.Session, IssuedAt: c.IssuedAt}, nil
}

// RegisterPushToken implements chatv1.PushServiceServer.
func (s *PushServer) RegisterPushToken(ctx context.Context, req *chatv1.RegisterPushTokenRequest) (*chatv1.RegisterPushTokenResponse, error) {
	c, err := pushCaller(ctx)
	if err != nil {
		return nil, err
	}
	platform, ok := pushPlatforms[req.GetPlatform()]
	if !ok {
		return nil, validation("platform")
	}
	if err := s.app.Register(ctx, c, platform, req.GetToken()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RegisterPushTokenResponse{}, nil
}

// UnregisterPushToken implements chatv1.PushServiceServer.
func (s *PushServer) UnregisterPushToken(ctx context.Context, _ *chatv1.UnregisterPushTokenRequest) (*chatv1.UnregisterPushTokenResponse, error) {
	c, err := pushCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.Unregister(ctx, c); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.UnregisterPushTokenResponse{}, nil
}

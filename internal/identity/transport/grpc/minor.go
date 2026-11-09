package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/minor"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MinorServiceName is the gRPC service whose methods only service tokens
// may call (ADR-0013); cmd/identity gives it to auth.Guard with tayfam-core
// as the only caller.
const MinorServiceName = "tayfam.identity.v1.MinorService"

// Minors is what MinorServer needs from the app layer (minor.Service).
type Minors interface {
	IsMinor(ctx context.Context, users []id.ID) (map[id.ID]bool, error)
}

// MinorServer implements identityv1.MinorServiceServer. auth.Guard lets only
// core's ServiceAccount reach it.
type MinorServer struct {
	identityv1.UnimplementedMinorServiceServer
	app Minors
	log *slog.Logger
}

// NewMinorServer returns a MinorServer.
func NewMinorServer(app Minors, log *slog.Logger) *MinorServer {
	return &MinorServer{app: app, log: log}
}

// IsMinor implements identityv1.MinorServiceServer. Only the yes/no answer
// leaves identity; the request and the answer are never logged.
func (s *MinorServer) IsMinor(ctx context.Context, req *identityv1.IsMinorRequest) (*identityv1.IsMinorResponse, error) {
	// Defence in depth: the Guard has already checked the caller.
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	raw := req.GetUserIds()
	if len(raw) == 0 || len(raw) > minor.MaxUsers {
		return nil, status.Error(codes.InvalidArgument, "istek reddedildi")
	}
	users := make([]id.ID, len(raw))
	for i, r := range raw {
		u, ok := id.Parse(r)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "istek reddedildi")
		}
		users[i] = u
	}
	answers, err := s.app.IsMinor(ctx, users)
	switch {
	case errors.Is(err, minor.ErrTooMany):
		return nil, status.Error(codes.InvalidArgument, "istek reddedildi")
	case err != nil:
		s.log.ErrorContext(ctx, "18 yaş sorgusu başarısız", slog.Any("err", err))
		return nil, status.Error(codes.Internal, "iç hata")
	}
	out := &identityv1.IsMinorResponse{Answers: make([]*identityv1.MinorAnswer, 0, len(answers))}
	for _, u := range users {
		if m, ok := answers[u]; ok {
			out.Answers = append(out.Answers, &identityv1.MinorAnswer{UserId: u.String(), Minor: m})
			delete(answers, u) // a repeated ID is answered once
		}
	}
	return out, nil
}

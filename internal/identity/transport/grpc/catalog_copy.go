package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/catalogcopy"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// CatalogCopyServiceName is the gRPC service of core's catalog.changed
// events (tayfam-core's service token only; issue #280, ADR-0042).
const CatalogCopyServiceName = "tayfam.identity.v1.CatalogCopyService"

// CatalogCopies is what CatalogCopyServer needs from the app layer.
type CatalogCopies interface {
	Apply(ctx context.Context, c catalogcopy.Change) (bool, error)
}

// CatalogCopyServer implements identityv1.CatalogCopyServiceServer.
type CatalogCopyServer struct {
	identityv1.UnimplementedCatalogCopyServiceServer
	copies CatalogCopies
	log    *slog.Logger
}

// NewCatalogCopyServer returns a CatalogCopyServer.
func NewCatalogCopyServer(c CatalogCopies, log *slog.Logger) *CatalogCopyServer {
	return &CatalogCopyServer{copies: c, log: log}
}

// catalogStatuses maps the request's statuses to the stored values; an
// unspecified or unknown status has none.
var catalogStatuses = map[identityv1.CatalogInterestStatus]string{
	identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_ACTIVE:  catalogcopy.StatusActive,
	identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_RETIRED: catalogcopy.StatusRetired,
}

// ApplyCatalogChange implements identityv1.CatalogCopyServiceServer. The
// caller is checked by Guard (tayfam-core's service token); the request is
// checked again by catalogcopy.
func (s *CatalogCopyServer) ApplyCatalogChange(ctx context.Context, req *identityv1.ApplyCatalogChangeRequest) (*identityv1.ApplyCatalogChangeResponse, error) {
	if _, ok := auth.ServiceCallerFrom(ctx); !ok {
		return nil, status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")
	}
	invalid := status.Error(codes.InvalidArgument, "geçersiz katalog değişikliği")
	if len(req.GetInterests()) > catalogcopy.MaxInterests {
		return nil, invalid
	}
	c := catalogcopy.Change{Version: req.GetCatalogVersion(), Interests: make([]catalogcopy.Interest, 0, len(req.GetInterests()))}
	for _, i := range req.GetInterests() {
		st, ok := catalogStatuses[i.GetStatus()]
		if !ok {
			return nil, invalid
		}
		c.Interests = append(c.Interests, catalogcopy.Interest{Key: i.GetKey(), Status: st})
	}
	applied, err := s.copies.Apply(ctx, c)
	switch {
	case errors.Is(err, catalogcopy.ErrInvalid):
		return nil, invalid
	case err != nil:
		s.log.ErrorContext(ctx, "katalog kopyası güncellenemedi", slog.Any("err", err))
		return nil, status.Error(codes.Internal, "iç hata")
	}
	return &identityv1.ApplyCatalogChangeResponse{Applied: applied}, nil
}

package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/poollink"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// PoolLinks is what PoolLinkComponentServer needs from the app layer
// (poollink.Service).
type PoolLinks interface {
	Get(ctx context.Context, ref component.Ref) (poollink.Links, error)
	Set(ctx context.Context, ref component.Ref, questions []id.ID) (poollink.Links, error)
}

// PoolLinkComponentServer implements corev1.PoolLinkComponentServiceServer
// (issue #271; ADR-0037 §3, ADR-0040 §2). Every method needs a verified
// token (auth.Guard); the acting user is always the token's. Question texts
// are never logged.
type PoolLinkComponentServer struct {
	corev1.UnimplementedPoolLinkComponentServiceServer
	app PoolLinks
	log *slog.Logger
}

// NewPoolLinkComponentServer returns a PoolLinkComponentServer.
func NewPoolLinkComponentServer(app PoolLinks, log *slog.Logger) *PoolLinkComponentServer {
	return &PoolLinkComponentServer{app: app, log: log}
}

// registerPoolLinkComponent is the pool_link component's line in the
// registry (componentRegistrars).
func registerPoolLinkComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterPoolLinkComponentServiceServer(srv, NewPoolLinkComponentServer(poollink.New(fw), log))
	return nil
}

func eventPoolLinksPB(l poollink.Links) *corev1.EventPoolLinks {
	out := &corev1.EventPoolLinks{
		Questions:     make([]*corev1.PoolLinkedQuestion, 0, len(l.Questions)),
		MaxLinks:      int32(l.MaxLinks), //nolint:gosec // 1..50
		CallerCanEdit: l.CallerCanEdit,
	}
	for _, q := range l.Questions {
		out.Questions = append(out.Questions, &corev1.PoolLinkedQuestion{
			QuestionId: q.ID.String(), AuthorUserId: optionalID(q.AuthorID), Body: q.Body,
			SubjectKey: q.SubjectKey, TopicKey: q.TopicKey, PhotoUploadIds: idStrings(q.Photos),
			AnswerCount: q.AnswerCount, CreateTime: timestamppb.New(q.CreatedAt),
		})
	}
	return out
}

// GetPoolLinks implements corev1.PoolLinkComponentServiceServer.
func (s *PoolLinkComponentServer) GetPoolLinks(ctx context.Context, req *corev1.GetPoolLinksRequest) (*corev1.GetPoolLinksResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	l, err := s.app.Get(ctx, ref)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetPoolLinksResponse{Links: eventPoolLinksPB(l)}, nil
}

// SetPoolLinks implements corev1.PoolLinkComponentServiceServer.
func (s *PoolLinkComponentServer) SetPoolLinks(ctx context.Context, req *corev1.SetPoolLinksRequest) (*corev1.SetPoolLinksResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if len(req.GetQuestionIds()) > poollink.MaxLinks {
		return nil, validation(poollink.FieldQuestionIDs)
	}
	ids := make([]id.ID, 0, len(req.GetQuestionIds()))
	for _, raw := range req.GetQuestionIds() {
		q, ok := id.Parse(raw)
		if !ok {
			return nil, validation(poollink.FieldQuestionIDs)
		}
		ids = append(ids, q)
	}
	l, err := s.app.Set(ctx, ref, ids)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetPoolLinksResponse{Links: eventPoolLinksPB(l)}, nil
}

package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Vocabulary is what TayfaVocabularyServer needs from the app layer
// (vocabulary.Service).
type Vocabulary interface {
	List(ctx context.Context, user, tayfa id.ID, namespace, query string, merged bool) (vocabulary.List, error)
	Merge(ctx context.Context, user, tayfa id.ID, namespace string, source, target id.ID) (vocabulary.Value, error)
	Unmerge(ctx context.Context, user, tayfa id.ID, namespace string, value id.ID) error
}

// TayfaVocabularyServer implements corev1.TayfaVocabularyServiceServer
// (issue #244; ADR-0037 §4). Every method needs a verified token
// (auth.Guard); the acting user is always the token's. Values are never
// logged.
type TayfaVocabularyServer struct {
	corev1.UnimplementedTayfaVocabularyServiceServer
	app Vocabulary
	log *slog.Logger
}

// NewTayfaVocabularyServer returns a TayfaVocabularyServer.
func NewTayfaVocabularyServer(app Vocabulary, log *slog.Logger) *TayfaVocabularyServer {
	return &TayfaVocabularyServer{app: app, log: log}
}

// registerTayfaVocabulary is the tayfa vocabulary's line in the registry
// (componentRegistrars): the components bound to it share the framework.
func registerTayfaVocabulary(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaVocabularyServiceServer(srv, NewTayfaVocabularyServer(vocabulary.New(fw), log))
	return nil
}

func vocabularyValuePB(v vocabulary.Value) *corev1.VocabularyValue {
	out := &corev1.VocabularyValue{Id: v.ID.String(), Value: v.Value}
	if v.MergedInto != (id.ID{}) {
		out.MergedIntoId, out.MergedIntoValue = v.MergedInto.String(), v.MergedIntoValue
	}
	return out
}

// vocabularyID parses a request's value ID; field names it in the error.
func vocabularyID(s, field string) (id.ID, error) {
	v, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation(field)
	}
	return v, nil
}

// ListVocabulary implements corev1.TayfaVocabularyServiceServer.
func (s *TayfaVocabularyServer) ListVocabulary(ctx context.Context, req *corev1.ListVocabularyRequest) (*corev1.ListVocabularyResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	l, err := s.app.List(ctx, user, t, req.GetNamespace(), req.GetQuery(), req.GetMerged())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListVocabularyResponse{Values: make([]*corev1.VocabularyValue, 0, len(l.Values)), CallerCanMerge: l.CallerCanMerge}
	for _, v := range l.Values {
		out.Values = append(out.Values, vocabularyValuePB(v))
	}
	return out, nil
}

// MergeVocabulary implements corev1.TayfaVocabularyServiceServer.
func (s *TayfaVocabularyServer) MergeVocabulary(ctx context.Context, req *corev1.MergeVocabularyRequest) (*corev1.MergeVocabularyResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	source, err := vocabularyID(req.GetVocabularyId(), "vocabulary_id")
	if err != nil {
		return nil, err
	}
	target, err := vocabularyID(req.GetTargetId(), "target_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.Merge(ctx, user, t, req.GetNamespace(), source, target)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.MergeVocabularyResponse{Value: vocabularyValuePB(v)}, nil
}

// UnmergeVocabulary implements corev1.TayfaVocabularyServiceServer.
func (s *TayfaVocabularyServer) UnmergeVocabulary(ctx context.Context, req *corev1.UnmergeVocabularyRequest) (*corev1.UnmergeVocabularyResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	v, err := vocabularyID(req.GetVocabularyId(), "vocabulary_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.Unmerge(ctx, user, t, req.GetNamespace(), v); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UnmergeVocabularyResponse{}, nil
}

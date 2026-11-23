package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/material"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Materials is what TayfaMaterialServer needs from the app layer
// (material.Service).
type Materials interface {
	List(ctx context.Context, user, tayfa id.ID, f material.Filter) (material.Page, error)
	Get(ctx context.Context, user, tayfa, m id.ID) (material.Material, error)
	Create(ctx context.Context, user, tayfa id.ID, in material.Input, clientRequestID string) (material.Material, error)
	Update(ctx context.Context, user, tayfa, m id.ID, in material.Input) (material.Material, error)
	Delete(ctx context.Context, user, tayfa, m id.ID) error
}

// TayfaMaterialServer implements corev1.TayfaMaterialServiceServer (issue
// #261; ADR-0040 §3). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Titles and links are never logged.
type TayfaMaterialServer struct {
	corev1.UnimplementedTayfaMaterialServiceServer
	app Materials
	log *slog.Logger
}

// NewTayfaMaterialServer returns a TayfaMaterialServer.
func NewTayfaMaterialServer(app Materials, log *slog.Logger) *TayfaMaterialServer {
	return &TayfaMaterialServer{app: app, log: log}
}

// registerTayfaMaterials is the material library's line in the registry
// (componentRegistrars): materials are tayfa-level data on the framework.
func registerTayfaMaterials(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaMaterialServiceServer(srv, NewTayfaMaterialServer(material.New(fw), log))
	return nil
}

var materialKinds = map[corev1.MaterialKind]string{
	corev1.MaterialKind_MATERIAL_KIND_LINK:       material.KindLink,
	corev1.MaterialKind_MATERIAL_KIND_NOTE_PHOTO: material.KindNotePhoto,
}

func materialKindPB(kind string) corev1.MaterialKind {
	for k, v := range materialKinds {
		if v == kind {
			return k
		}
	}
	return corev1.MaterialKind_MATERIAL_KIND_UNSPECIFIED
}

func materialPB(m material.Material) *corev1.Material {
	out := &corev1.Material{
		Id: m.ID.String(), Kind: materialKindPB(m.Kind), Title: m.Title, Url: m.URL, Domain: m.Domain,
		SubjectKey: m.SubjectKey, TopicKey: m.TopicKey, AuthorId: idString(m.Author),
		CreateTime: timestamppb.New(m.CreatedAt), UpdateTime: timestamppb.New(m.UpdatedAt),
		CallerCanEdit: m.CallerCanEdit, CallerCanDelete: m.CallerCanDelete,
	}
	for _, p := range m.Photos {
		out.PhotoUploadIds = append(out.PhotoUploadIds, p.String())
	}
	return out
}

// materialPhotos parses photo upload IDs; how many is the app's to judge.
func materialPhotos(raw []string) ([]id.ID, error) {
	if len(raw) > material.MaxPhotos {
		return nil, validation(material.FieldPhotoIDs)
	}
	out := make([]id.ID, 0, len(raw))
	for _, r := range raw {
		p, err := parseID(r, material.FieldPhotoIDs)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ListMaterials implements corev1.TayfaMaterialServiceServer.
func (s *TayfaMaterialServer) ListMaterials(ctx context.Context, req *corev1.ListMaterialsRequest) (*corev1.ListMaterialsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	f := material.Filter{
		SubjectKey: req.GetSubjectKey(), TopicKey: req.GetTopicKey(), Mine: req.GetMine(),
		PageSize: req.GetPageSize(), PageToken: req.GetPageToken(),
	}
	if req.GetKind() != corev1.MaterialKind_MATERIAL_KIND_UNSPECIFIED {
		var ok bool
		if f.Kind, ok = materialKinds[req.GetKind()]; !ok {
			return nil, validation(material.FieldKind)
		}
	}
	p, err := s.app.List(ctx, user, t, f)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListMaterialsResponse{
		Materials: make([]*corev1.Material, 0, len(p.Materials)), NextPageToken: p.NextPageToken, CallerCanAdd: p.CallerCanAdd,
	}
	for _, m := range p.Materials {
		out.Materials = append(out.Materials, materialPB(m))
	}
	return out, nil
}

// GetMaterial implements corev1.TayfaMaterialServiceServer.
func (s *TayfaMaterialServer) GetMaterial(ctx context.Context, req *corev1.GetMaterialRequest) (*corev1.GetMaterialResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	m, err := parseID(req.GetMaterialId(), material.FieldMaterialID)
	if err != nil {
		return nil, err
	}
	v, err := s.app.Get(ctx, user, t, m)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetMaterialResponse{Material: materialPB(v)}, nil
}

// CreateMaterial implements corev1.TayfaMaterialServiceServer.
func (s *TayfaMaterialServer) CreateMaterial(ctx context.Context, req *corev1.CreateMaterialRequest) (*corev1.CreateMaterialResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	kind, ok := materialKinds[req.GetKind()]
	if !ok {
		return nil, validation(material.FieldKind)
	}
	photos, err := materialPhotos(req.GetPhotoUploadIds())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Create(ctx, user, t, material.Input{
		Kind: kind, Title: req.GetTitle(), URL: req.GetUrl(), Photos: photos,
		SubjectKey: req.GetSubjectKey(), TopicKey: req.GetTopicKey(),
	}, req.GetClientRequestId())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateMaterialResponse{Material: materialPB(v)}, nil
}

// UpdateMaterial implements corev1.TayfaMaterialServiceServer.
func (s *TayfaMaterialServer) UpdateMaterial(ctx context.Context, req *corev1.UpdateMaterialRequest) (*corev1.UpdateMaterialResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	m, err := parseID(req.GetMaterialId(), material.FieldMaterialID)
	if err != nil {
		return nil, err
	}
	photos, err := materialPhotos(req.GetPhotoUploadIds())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Update(ctx, user, t, m, material.Input{
		Title: req.GetTitle(), URL: req.GetUrl(), Photos: photos, SubjectKey: req.GetSubjectKey(), TopicKey: req.GetTopicKey(),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateMaterialResponse{Material: materialPB(v)}, nil
}

// DeleteMaterial implements corev1.TayfaMaterialServiceServer.
func (s *TayfaMaterialServer) DeleteMaterial(ctx context.Context, req *corev1.DeleteMaterialRequest) (*corev1.DeleteMaterialResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	m, err := parseID(req.GetMaterialId(), material.FieldMaterialID)
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, user, t, m); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteMaterialResponse{}, nil
}

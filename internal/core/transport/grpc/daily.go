package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/daily"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Daily is what TayfaDailyServer needs from the app layer (daily.Service).
type Daily interface {
	Today(ctx context.Context, user, tayfa id.ID) (daily.Today, error)
	List(ctx context.Context, user, tayfa id.ID, deleted bool) (daily.List, error)
	Create(ctx context.Context, user, tayfa id.ID, in daily.Input, clientRequestID string) (daily.Saved, error)
	Update(ctx context.Context, user, tayfa, item id.ID, in daily.Input) (daily.Saved, error)
	Delete(ctx context.Context, user, tayfa, item id.ID) error
	Restore(ctx context.Context, user, tayfa, item id.ID) (daily.Saved, error)
	Reorder(ctx context.Context, user, tayfa id.ID, ids []id.ID) ([]daily.Item, error)
}

// TayfaDailyServer implements corev1.TayfaDailyServiceServer (issue #269;
// ADR-0027 §5). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Texts are never logged.
type TayfaDailyServer struct {
	corev1.UnimplementedTayfaDailyServiceServer
	app Daily
	log *slog.Logger
}

// NewTayfaDailyServer returns a TayfaDailyServer.
func NewTayfaDailyServer(app Daily, log *slog.Logger) *TayfaDailyServer {
	return &TayfaDailyServer{app: app, log: log}
}

// registerTayfaDaily is the daily content's line in the registry
// (componentRegistrars): the pool is tayfa-level data on the framework.
func registerTayfaDaily(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaDailyServiceServer(srv, NewTayfaDailyServer(daily.New(fw), log))
	return nil
}

func dailyItemPB(it daily.Item) *corev1.DailyItem {
	out := &corev1.DailyItem{
		Id: it.ID.String(), Body: it.Body, Attribution: it.Attribution, Position: it.Position,
		CreateTime: timestamppb.New(it.CreatedAt), UpdateTime: timestamppb.New(it.UpdatedAt),
		DeleteTime: tsOrNil(it.DeletedAt),
	}
	if it.CreatedBy != (id.ID{}) {
		out.CreatedBy = it.CreatedBy.String()
	}
	if it.Pin != nil {
		out.Pin = &corev1.DailyPin{Month: int32(it.Pin.Month), Day: int32(it.Pin.Day)} //nolint:gosec // a day of the year
		if it.Pin.Year != 0 {
			y := int32(it.Pin.Year) //nolint:gosec // 2000–2100
			out.Pin.Year = &y
		}
	}
	return out
}

func dailyItemsPB(items []daily.Item) []*corev1.DailyItem {
	out := make([]*corev1.DailyItem, 0, len(items))
	for _, it := range items {
		out = append(out, dailyItemPB(it))
	}
	return out
}

// dailyInput converts an item input; a pin out of the int range is left
// for the app layer to refuse.
func dailyInput(in *corev1.DailyItemInput) daily.Input {
	out := daily.Input{Body: in.GetBody(), Attribution: in.GetAttribution()}
	if p := in.GetPin(); p != nil {
		out.Pin = &daily.Pin{Month: int(p.GetMonth()), Day: int(p.GetDay()), Year: int(p.GetYear())}
		if p.Year != nil && p.GetYear() == 0 {
			out.Pin.Year = -1 // an explicit year 0 is not "every year"
		}
	}
	return out
}

func dailyItemID(s string) (id.ID, error) {
	v, ok := id.Parse(s)
	if !ok {
		return id.ID{}, validation(daily.FieldItemID)
	}
	return v, nil
}

// GetDailyToday implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) GetDailyToday(ctx context.Context, req *corev1.GetDailyTodayRequest) (*corev1.GetDailyTodayResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	today, err := s.app.Today(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.GetDailyTodayResponse{Pinned: today.Pinned, Date: today.Date.Format("2006-01-02")}
	if today.Item != nil {
		out.Item = dailyItemPB(*today.Item)
	}
	return out, nil
}

// ListDailyItems implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) ListDailyItems(ctx context.Context, req *corev1.ListDailyItemsRequest) (*corev1.ListDailyItemsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	l, err := s.app.List(ctx, user, t, req.GetDeleted())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ListDailyItemsResponse{Items: dailyItemsPB(l.Items), CallerCanManage: l.CallerCanManage}, nil
}

// CreateDailyItem implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) CreateDailyItem(ctx context.Context, req *corev1.CreateDailyItemRequest) (*corev1.CreateDailyItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	v, err := s.app.Create(ctx, user, t, dailyInput(req.GetItem()), req.GetClientRequestId())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.CreateDailyItemResponse{Item: dailyItemPB(v.Item), PinConflict: v.PinConflict}, nil
}

// UpdateDailyItem implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) UpdateDailyItem(ctx context.Context, req *corev1.UpdateDailyItemRequest) (*corev1.UpdateDailyItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := dailyItemID(req.GetItemId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Update(ctx, user, t, item, dailyInput(req.GetItem()))
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.UpdateDailyItemResponse{Item: dailyItemPB(v.Item), PinConflict: v.PinConflict}, nil
}

// DeleteDailyItem implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) DeleteDailyItem(ctx context.Context, req *corev1.DeleteDailyItemRequest) (*corev1.DeleteDailyItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := dailyItemID(req.GetItemId())
	if err != nil {
		return nil, err
	}
	if err := s.app.Delete(ctx, user, t, item); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.DeleteDailyItemResponse{}, nil
}

// RestoreDailyItem implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) RestoreDailyItem(ctx context.Context, req *corev1.RestoreDailyItemRequest) (*corev1.RestoreDailyItemResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	item, err := dailyItemID(req.GetItemId())
	if err != nil {
		return nil, err
	}
	v, err := s.app.Restore(ctx, user, t, item)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RestoreDailyItemResponse{Item: dailyItemPB(v.Item), PinConflict: v.PinConflict}, nil
}

// ReorderDailyItems implements corev1.TayfaDailyServiceServer.
func (s *TayfaDailyServer) ReorderDailyItems(ctx context.Context, req *corev1.ReorderDailyItemsRequest) (*corev1.ReorderDailyItemsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	if len(req.GetItemIds()) > daily.MaxItems {
		return nil, validation(daily.FieldItemIDs)
	}
	ids := make([]id.ID, 0, len(req.GetItemIds()))
	for _, raw := range req.GetItemIds() {
		v, ok := id.Parse(raw)
		if !ok {
			return nil, validation(daily.FieldItemIDs)
		}
		ids = append(ids, v)
	}
	items, err := s.app.Reorder(ctx, user, t, ids)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ReorderDailyItemsResponse{Items: dailyItemsPB(items)}, nil
}

package grpc

import (
	"context"
	"encoding/json"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/logbook"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Logbook is what LogComponentServer needs from the app layer
// (logbook.Service).
type Logbook interface {
	List(ctx context.Context, ref component.Ref, size int32, token string) (logbook.Page, error)
	Add(ctx context.Context, ref component.Ref, in logbook.Input) (logbook.Entry, error)
	Remove(ctx context.Context, ref component.Ref, entry id.ID) error
}

// LogComponentServer implements corev1.LogComponentServiceServer (issue
// #243; ADR-0025 §4). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Values and locations are never
// logged.
type LogComponentServer struct {
	corev1.UnimplementedLogComponentServiceServer
	app Logbook
	log *slog.Logger
}

// NewLogComponentServer returns a LogComponentServer.
func NewLogComponentServer(app Logbook, log *slog.Logger) *LogComponentServer {
	return &LogComponentServer{app: app, log: log}
}

// registerLogComponent is the log component's line in the registry
// (componentRegistrars).
func registerLogComponent(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterLogComponentServiceServer(srv, NewLogComponentServer(logbook.New(fw), log))
	return nil
}

var logVisibilityToProto = map[string]corev1.LogLocationVisibility{
	logbook.LocationSelf:         corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_SELF,
	logbook.LocationParticipants: corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS,
	logbook.LocationTayfa:        corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_TAYFA,
}

// logVisibilityFromProto maps a request value; UNSPECIFIED is the log's
// default ("").
var logVisibilityFromProto = map[corev1.LogLocationVisibility]string{
	corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_UNSPECIFIED:  "",
	corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_SELF:         logbook.LocationSelf,
	corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS: logbook.LocationParticipants,
	corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_TAYFA:        logbook.LocationTayfa,
}

func logEntryPB(e logbook.Entry) *corev1.LogEntry {
	out := &corev1.LogEntry{
		Id: e.ID.String(), UserId: e.UserID.String(), Values: make(map[string]string, len(e.Values)),
		LocationVisibility: logVisibilityToProto[e.LocationVisibility], PhotoUploadId: optionalID(e.PhotoUploadID),
		CreateTime: timestamppb.New(e.CreatedAt), CallerCanRemove: e.CallerCanRemove,
	}
	for k, v := range e.Values {
		out.Values[k] = string(v)
	}
	if e.Location != nil {
		out.Location = &corev1.LatLng{Lat: e.Location.Lat, Lng: e.Location.Lng}
	}
	return out
}

// logValuesFromProto converts a request's values (JSON text by field key).
// The app layer checks them against the log's fields; here only their
// number and size are bounded.
func logValuesFromProto(in map[string]string) (map[string]json.RawMessage, error) {
	if len(in) > definition.MaxLogFields {
		return nil, validation("values")
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		if !definition.ValidKey(k) || v == "" || len(v) > maxFieldValueBytes {
			return nil, validation("values")
		}
		out[k] = json.RawMessage(v)
	}
	return out, nil
}

// ListLogEntries implements corev1.LogComponentServiceServer.
func (s *LogComponentServer) ListLogEntries(ctx context.Context, req *corev1.ListLogEntriesRequest) (*corev1.ListLogEntriesResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	p, err := s.app.List(ctx, ref, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListLogEntriesResponse{
		Entries: make([]*corev1.LogEntry, 0, len(p.Entries)), NextPageToken: p.NextPageToken,
		LocationEnabled: p.LocationEnabled, LocationDefault: logVisibilityToProto[p.LocationDefault],
		MaxEntries: p.MaxEntries, CallerCanAdd: p.CallerCanAdd,
	}
	for _, e := range p.Entries {
		out.Entries = append(out.Entries, logEntryPB(e))
	}
	return out, nil
}

// AddLogEntry implements corev1.LogComponentServiceServer. The location
// goes to the app layer only; it is never logged.
func (s *LogComponentServer) AddLogEntry(ctx context.Context, req *corev1.AddLogEntryRequest) (*corev1.AddLogEntryResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	values, err := logValuesFromProto(req.GetValues())
	if err != nil {
		return nil, err
	}
	in := logbook.Input{Values: values, ClientRequestID: req.GetClientRequestId()}
	if l := req.GetLocation(); l != nil {
		in.Location = &logbook.Point{Lat: l.GetLat(), Lng: l.GetLng()}
	}
	vis, ok := logVisibilityFromProto[req.GetLocationVisibility()]
	if !ok {
		return nil, validation("location_visibility")
	}
	in.LocationVisibility = vis
	if in.PhotoUploadID, err = optionalUploadID(req.GetPhotoUploadId(), "photo_upload_id"); err != nil {
		return nil, err
	}
	e, err := s.app.Add(ctx, ref, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.AddLogEntryResponse{Entry: logEntryPB(e)}, nil
}

// RemoveLogEntry implements corev1.LogComponentServiceServer.
func (s *LogComponentServer) RemoveLogEntry(ctx context.Context, req *corev1.RemoveLogEntryRequest) (*corev1.RemoveLogEntryResponse, error) {
	ref, err := componentRef(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	entry, ok := id.Parse(req.GetEntryId())
	if !ok {
		return nil, validation("entry_id")
	}
	if err := s.app.Remove(ctx, ref, entry); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.RemoveLogEntryResponse{}, nil
}

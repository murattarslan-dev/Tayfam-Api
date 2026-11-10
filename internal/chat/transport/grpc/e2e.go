package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Keys is what E2EServer needs from the app layer (keys.Service).
type Keys interface {
	Register(ctx context.Context, c keys.Caller, in keys.RegisterInput) (keys.Device, keys.Counts, error)
	MyDevice(ctx context.Context, c keys.Caller) (keys.State, error)
	Unregister(ctx context.Context, c keys.Caller) error
	Upload(ctx context.Context, c keys.Caller, in keys.UploadInput) (keys.Counts, error)
	Devices(ctx context.Context, caller, user id.ID) ([]keys.PublicDevice, error)
	KeyEvents(ctx context.Context, caller, user id.ID, size int32, token string) ([]keys.KeyEvent, string, error)
	Bundles(ctx context.Context, c keys.Caller, user id.ID, only []id.ID) ([]keys.Bundle, error)
	Envelopes(ctx context.Context, c keys.Caller, size int32, token string) ([]keys.Envelope, string, error)
	Ack(ctx context.Context, c keys.Caller, ids []id.ID) error
	PutArchive(ctx context.Context, c keys.Caller, version uint32, entries []keys.ArchiveInput) ([]id.ID, error)
	Archive(ctx context.Context, c keys.Caller, conversation id.ID, in keys.ArchiveListInput) ([]keys.ArchiveEntry, string, error)
	DeleteArchive(ctx context.Context, c keys.Caller, conversation id.ID) error
	ClaimHistoryKey(ctx context.Context, c keys.Caller) (int, error)
	CreateLink(ctx context.Context, c keys.Caller) (keys.LinkRequest, error)
	AnswerLink(ctx context.Context, c keys.Caller, request id.ID, version uint32, answer []byte) error
	GetLink(ctx context.Context, c keys.Caller, request id.ID) (keys.LinkRequest, error)
	CompleteLink(ctx context.Context, c keys.Caller, request id.ID) (keys.Device, error)
	Rotate(ctx context.Context, c keys.Caller, version uint32, shares []keys.ShareInput) error
	Shares(ctx context.Context, c keys.Caller) ([]keys.Share, error)
	AckShares(ctx context.Context, c keys.Caller, ids []id.ID) error
}

// E2EServer implements chatv1.E2EServiceServer (issue #127). Every method
// acts for the caller and the session of the verified access token. It
// never logs request fields: keys, envelopes and archive entries must not
// reach any log.
type E2EServer struct {
	chatv1.UnimplementedE2EServiceServer
	app Keys
	log *slog.Logger
}

// NewE2EServer returns an E2EServer.
func NewE2EServer(app Keys, log *slog.Logger) *E2EServer {
	return &E2EServer{app: app, log: log}
}

func signedFrom(k *chatv1.SignedPreKey) keys.SignedPreKey {
	return keys.SignedPreKey{ID: k.GetKeyId(), PublicKey: k.GetPublicKey(), Signature: k.GetSignature()}
}

func preKeysFrom(ks []*chatv1.PreKey) []keys.PreKey {
	out := make([]keys.PreKey, 0, len(ks))
	for _, k := range ks {
		out = append(out, keys.PreKey{ID: k.GetKeyId(), PublicKey: k.GetPublicKey()})
	}
	return out
}

func signedListFrom(ks []*chatv1.SignedPreKey) []keys.SignedPreKey {
	out := make([]keys.SignedPreKey, 0, len(ks))
	for _, k := range ks {
		out = append(out, signedFrom(k))
	}
	return out
}

func signedToProto(k keys.SignedPreKey) *chatv1.SignedPreKey {
	return &chatv1.SignedPreKey{KeyId: k.ID, PublicKey: k.PublicKey, Signature: k.Signature}
}

func deviceToProto(d keys.Device) *chatv1.Device {
	return &chatv1.Device{
		Id: d.ID.String(), DeviceNumber: uint32(d.Number), RegistrationId: uint32(d.RegistrationID), //nolint:gosec // 1..127, 1..16380
		IdentityKey: d.IdentityKey, Paired: d.Paired, CreateTime: timestamppb.New(d.CreatedAt),
	}
}

func countsToProto(c keys.Counts) *chatv1.PreKeyCounts {
	return &chatv1.PreKeyCounts{Ec: uint32(c.EC), Kyber: uint32(c.Kyber), Low: c.Low()} //nolint:gosec // at most 200
}

// parseIDs parses a list of IDs of field.
func parseIDs(field string, vs []string) ([]id.ID, error) {
	out := make([]id.ID, 0, len(vs))
	for _, v := range vs {
		p, ok := id.Parse(v)
		if !ok {
			return nil, validation(field)
		}
		out = append(out, p)
	}
	return out, nil
}

// optionalID parses an ID that may be empty.
func optionalID(field, v string) (id.ID, error) {
	if v == "" {
		return id.ID{}, nil
	}
	p, ok := id.Parse(v)
	if !ok {
		return id.ID{}, validation(field)
	}
	return p, nil
}

// RegisterDevice implements chatv1.E2EServiceServer.
func (s *E2EServer) RegisterDevice(ctx context.Context, req *chatv1.RegisterDeviceRequest) (*chatv1.RegisterDeviceResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetSignedPrekey() == nil {
		return nil, validation("signed_prekey")
	}
	if req.GetKyberLastResortPrekey() == nil {
		return nil, validation("kyber_last_resort_prekey")
	}
	d, n, err := s.app.Register(ctx, c, keys.RegisterInput{
		RegistrationID: req.GetRegistrationId(), IdentityKey: req.GetIdentityKey(),
		SignedPreKey: signedFrom(req.GetSignedPrekey()), KyberLastResort: signedFrom(req.GetKyberLastResortPrekey()),
		OneTime: preKeysFrom(req.GetOneTimePrekeys()), Kyber: signedListFrom(req.GetKyberPrekeys()),
	})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RegisterDeviceResponse{Device: deviceToProto(d), PrekeyCounts: countsToProto(n)}, nil
}

// GetMyDevice implements chatv1.E2EServiceServer.
func (s *E2EServer) GetMyDevice(ctx context.Context, _ *chatv1.GetMyDeviceRequest) (*chatv1.GetMyDeviceResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.app.MyDevice(ctx, c)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetMyDeviceResponse{
		Device: deviceToProto(st.Device), PrekeyCounts: countsToProto(st.Counts),
		HistoryKeyVersion: uint32(st.HistoryKeyVersion), RotationNeeded: st.RotationNeeded, OtherPairedDevice: st.OtherPaired, //nolint:gosec // 0..32767
	}, nil
}

// UnregisterDevice implements chatv1.E2EServiceServer.
func (s *E2EServer) UnregisterDevice(ctx context.Context, _ *chatv1.UnregisterDeviceRequest) (*chatv1.UnregisterDeviceResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.app.Unregister(ctx, c); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.UnregisterDeviceResponse{}, nil
}

// UploadPreKeys implements chatv1.E2EServiceServer.
func (s *E2EServer) UploadPreKeys(ctx context.Context, req *chatv1.UploadPreKeysRequest) (*chatv1.UploadPreKeysResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	in := keys.UploadInput{OneTime: preKeysFrom(req.GetOneTimePrekeys()), Kyber: signedListFrom(req.GetKyberPrekeys())}
	if k := req.GetSignedPrekey(); k != nil {
		v := signedFrom(k)
		in.SignedPreKey = &v
	}
	if k := req.GetKyberLastResortPrekey(); k != nil {
		v := signedFrom(k)
		in.KyberLastResort = &v
	}
	n, err := s.app.Upload(ctx, c, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.UploadPreKeysResponse{PrekeyCounts: countsToProto(n)}, nil
}

// ListDevices implements chatv1.E2EServiceServer.
func (s *E2EServer) ListDevices(ctx context.Context, req *chatv1.ListDevicesRequest) (*chatv1.ListDevicesResponse, error) {
	caller, v, err := ids(ctx, f("user_id", req.GetUserId()))
	if err != nil {
		return nil, err
	}
	list, err := s.app.Devices(ctx, caller, v[0])
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListDevicesResponse{Devices: make([]*chatv1.PublicDevice, 0, len(list))}
	for _, d := range list {
		out.Devices = append(out.Devices, &chatv1.PublicDevice{
			Id: d.ID.String(), DeviceNumber: uint32(d.Number), RegistrationId: uint32(d.RegistrationID), //nolint:gosec // 1..127, 1..16380
			IdentityKey: d.IdentityKey, CreateTime: timestamppb.New(d.CreatedAt),
		})
	}
	return out, nil
}

var keyEventTypes = map[string]chatv1.KeyEventType{
	keys.EventDeviceAdded:   chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_ADDED,
	keys.EventDeviceRemoved: chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_REMOVED,
	"identity_changed":      chatv1.KeyEventType_KEY_EVENT_TYPE_IDENTITY_CHANGED,
}

// ListKeyEvents implements chatv1.E2EServiceServer.
func (s *E2EServer) ListKeyEvents(ctx context.Context, req *chatv1.ListKeyEventsRequest) (*chatv1.ListKeyEventsResponse, error) {
	caller, v, err := ids(ctx, f("user_id", req.GetUserId()))
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.KeyEvents(ctx, caller, v[0], req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListKeyEventsResponse{Events: make([]*chatv1.KeyEvent, 0, len(list)), NextPageToken: next}
	for _, e := range list {
		out.Events = append(out.Events, &chatv1.KeyEvent{
			Id: e.ID.String(), DeviceId: e.DeviceID.String(), DeviceNumber: uint32(e.Number), //nolint:gosec // 1..127
			Type: keyEventTypes[e.Type], CreateTime: timestamppb.New(e.CreatedAt),
		})
	}
	return out, nil
}

// GetPreKeyBundles implements chatv1.E2EServiceServer.
func (s *E2EServer) GetPreKeyBundles(ctx context.Context, req *chatv1.GetPreKeyBundlesRequest) (*chatv1.GetPreKeyBundlesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	user, ok := id.Parse(req.GetUserId())
	if !ok {
		return nil, validation("user_id")
	}
	only, err := parseIDs("device_ids", req.GetDeviceIds())
	if err != nil {
		return nil, err
	}
	list, err := s.app.Bundles(ctx, c, user, only)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.GetPreKeyBundlesResponse{Bundles: make([]*chatv1.PreKeyBundle, 0, len(list))}
	for _, b := range list {
		p := &chatv1.PreKeyBundle{
			DeviceId: b.DeviceID.String(), DeviceNumber: uint32(b.Number), RegistrationId: uint32(b.RegistrationID), //nolint:gosec // 1..127, 1..16380
			IdentityKey: b.IdentityKey, SignedPrekey: signedToProto(b.SignedPreKey),
			KyberPrekey: signedToProto(b.Kyber), KyberLastResort: b.KyberLastResort,
		}
		if b.OneTime != nil {
			p.OneTimePrekey = &chatv1.PreKey{KeyId: b.OneTime.ID, PublicKey: b.OneTime.PublicKey}
		}
		out.Bundles = append(out.Bundles, p)
	}
	return out, nil
}

// ListEnvelopes implements chatv1.E2EServiceServer.
func (s *E2EServer) ListEnvelopes(ctx context.Context, req *chatv1.ListEnvelopesRequest) (*chatv1.ListEnvelopesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.Envelopes(ctx, c, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListEnvelopesResponse{Envelopes: make([]*chatv1.Envelope, 0, len(list)), NextPageToken: next}
	for _, e := range list {
		out.Envelopes = append(out.Envelopes, &chatv1.Envelope{
			Id: e.ID.String(), MessageId: e.MessageID.String(), ConversationId: e.ConversationID.String(),
			SenderUserId: e.SenderID.String(), SenderDeviceId: e.SenderDevice.String(), SenderDeviceNumber: uint32(e.SenderNumber), //nolint:gosec // 1..127
			Type: chatv1.EnvelopeType(e.Type), Content: e.Content, CreateTime: timestamppb.New(e.CreatedAt), //nolint:gosec // 2 or 3
		})
	}
	return out, nil
}

// AckEnvelopes implements chatv1.E2EServiceServer.
func (s *E2EServer) AckEnvelopes(ctx context.Context, req *chatv1.AckEnvelopesRequest) (*chatv1.AckEnvelopesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	list, err := parseIDs("envelope_ids", req.GetEnvelopeIds())
	if err != nil {
		return nil, err
	}
	if err := s.app.Ack(ctx, c, list); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.AckEnvelopesResponse{}, nil
}

// PutArchiveEntries implements chatv1.E2EServiceServer.
func (s *E2EServer) PutArchiveEntries(ctx context.Context, req *chatv1.PutArchiveEntriesRequest) (*chatv1.PutArchiveEntriesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	in := make([]keys.ArchiveInput, 0, len(req.GetEntries()))
	for _, e := range req.GetEntries() {
		m, ok := id.Parse(e.GetMessageId())
		if !ok {
			return nil, validation("entries")
		}
		in = append(in, keys.ArchiveInput{MessageID: m, Content: e.GetContent()})
	}
	got, err := s.app.PutArchive(ctx, c, req.GetHistoryKeyVersion(), in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.PutArchiveEntriesResponse{AcceptedMessageIds: make([]string, 0, len(got))}
	for _, m := range got {
		out.AcceptedMessageIds = append(out.AcceptedMessageIds, m.String())
	}
	return out, nil
}

// ListArchiveEntries implements chatv1.E2EServiceServer.
func (s *E2EServer) ListArchiveEntries(ctx context.Context, req *chatv1.ListArchiveEntriesRequest) (*chatv1.ListArchiveEntriesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	conv, ok := id.Parse(req.GetConversationId())
	if !ok {
		return nil, validation("conversation_id")
	}
	after, err := optionalID("after_message_id", req.GetAfterMessageId())
	if err != nil {
		return nil, err
	}
	list, next, err := s.app.Archive(ctx, c, conv, keys.ArchiveListInput{PageSize: req.GetPageSize(), PageToken: req.GetPageToken(), After: after})
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListArchiveEntriesResponse{Entries: make([]*chatv1.ArchiveEntry, 0, len(list)), NextPageToken: next}
	for _, e := range list {
		out.Entries = append(out.Entries, &chatv1.ArchiveEntry{
			MessageId: e.MessageID.String(), ConversationId: e.ConversationID.String(), KeyVersion: uint32(e.KeyVersion), //nolint:gosec // 1..32767
			Content: e.Content, CreateTime: timestamppb.New(e.CreatedAt),
		})
	}
	return out, nil
}

// DeleteArchive implements chatv1.E2EServiceServer.
func (s *E2EServer) DeleteArchive(ctx context.Context, req *chatv1.DeleteArchiveRequest) (*chatv1.DeleteArchiveResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := optionalID("conversation_id", req.GetConversationId())
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteArchive(ctx, c, conv); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.DeleteArchiveResponse{}, nil
}

// ClaimHistoryKey implements chatv1.E2EServiceServer.
func (s *E2EServer) ClaimHistoryKey(ctx context.Context, _ *chatv1.ClaimHistoryKeyRequest) (*chatv1.ClaimHistoryKeyResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.app.ClaimHistoryKey(ctx, c)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.ClaimHistoryKeyResponse{HistoryKeyVersion: uint32(v)}, nil //nolint:gosec // 1..32767
}

// CreateLinkRequest implements chatv1.E2EServiceServer.
func (s *E2EServer) CreateLinkRequest(ctx context.Context, _ *chatv1.CreateLinkRequestRequest) (*chatv1.CreateLinkRequestResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.app.CreateLink(ctx, c)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.CreateLinkRequestResponse{LinkRequestId: r.ID.String(), ExpireTime: timestamppb.New(r.ExpiresAt)}, nil
}

// AnswerLinkRequest implements chatv1.E2EServiceServer.
func (s *E2EServer) AnswerLinkRequest(ctx context.Context, req *chatv1.AnswerLinkRequestRequest) (*chatv1.AnswerLinkRequestResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	r, ok := id.Parse(req.GetLinkRequestId())
	if !ok {
		return nil, validation("link_request_id")
	}
	if err := s.app.AnswerLink(ctx, c, r, req.GetHistoryKeyVersion(), req.GetAnswer()); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.AnswerLinkRequestResponse{}, nil
}

// GetLinkRequest implements chatv1.E2EServiceServer.
func (s *E2EServer) GetLinkRequest(ctx context.Context, req *chatv1.GetLinkRequestRequest) (*chatv1.GetLinkRequestResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	r, ok := id.Parse(req.GetLinkRequestId())
	if !ok {
		return nil, validation("link_request_id")
	}
	l, err := s.app.GetLink(ctx, c, r)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.GetLinkRequestResponse{
		LinkRequestId: l.ID.String(), ExpireTime: timestamppb.New(l.ExpiresAt), Answered: l.Answered,
		Answer: l.Answer, HistoryKeyVersion: uint32(l.Version), AnsweredByDeviceId: idOrEmpty(l.AnsweredBy), //nolint:gosec // 0..32767
	}, nil
}

// CompleteLinkRequest implements chatv1.E2EServiceServer.
func (s *E2EServer) CompleteLinkRequest(ctx context.Context, req *chatv1.CompleteLinkRequestRequest) (*chatv1.CompleteLinkRequestResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	r, ok := id.Parse(req.GetLinkRequestId())
	if !ok {
		return nil, validation("link_request_id")
	}
	d, err := s.app.CompleteLink(ctx, c, r)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.CompleteLinkRequestResponse{Device: deviceToProto(d)}, nil
}

// RotateHistoryKey implements chatv1.E2EServiceServer.
func (s *E2EServer) RotateHistoryKey(ctx context.Context, req *chatv1.RotateHistoryKeyRequest) (*chatv1.RotateHistoryKeyResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	shares := make([]keys.ShareInput, 0, len(req.GetShares()))
	for _, sh := range req.GetShares() {
		d, ok := id.Parse(sh.GetDeviceId())
		if !ok {
			return nil, validation("shares")
		}
		shares = append(shares, keys.ShareInput{Device: d, Bundle: sh.GetBundle()})
	}
	if err := s.app.Rotate(ctx, c, req.GetHistoryKeyVersion(), shares); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.RotateHistoryKeyResponse{}, nil
}

// ListHistoryKeyShares implements chatv1.E2EServiceServer.
func (s *E2EServer) ListHistoryKeyShares(ctx context.Context, _ *chatv1.ListHistoryKeySharesRequest) (*chatv1.ListHistoryKeySharesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.app.Shares(ctx, c)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &chatv1.ListHistoryKeySharesResponse{Shares: make([]*chatv1.HistoryKeyShare, 0, len(list))}
	for _, sh := range list {
		out.Shares = append(out.Shares, &chatv1.HistoryKeyShare{
			Id: sh.ID.String(), SenderDeviceId: sh.SenderDevice.String(), HistoryKeyVersion: uint32(sh.Version), //nolint:gosec // 1..32767
			Bundle: sh.Bundle, CreateTime: timestamppb.New(sh.CreatedAt),
		})
	}
	return out, nil
}

// AckHistoryKeyShares implements chatv1.E2EServiceServer.
func (s *E2EServer) AckHistoryKeyShares(ctx context.Context, req *chatv1.AckHistoryKeySharesRequest) (*chatv1.AckHistoryKeySharesResponse, error) {
	c, err := callerSession(ctx)
	if err != nil {
		return nil, err
	}
	list, err := parseIDs("share_ids", req.GetShareIds())
	if err != nil {
		return nil, err
	}
	if err := s.app.AckShares(ctx, c, list); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &chatv1.AckHistoryKeySharesResponse{}, nil
}

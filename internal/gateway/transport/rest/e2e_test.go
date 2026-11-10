package rest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
)

const (
	deviceID      = "01965a3c-0000-7000-8000-000000000050"
	linkRequestID = "01965a3c-0000-7000-8000-000000000051"
)

// b64 is how []byte appears in JSON (standard base64).
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func testDevice() *chatv1.Device {
	return &chatv1.Device{Id: deviceID, DeviceNumber: 2, RegistrationId: 77, IdentityKey: []byte{5, 1}, Paired: true, CreateTime: timestamppb.New(t0)}
}

func (f *fakeChat) RegisterDevice(ctx context.Context, in *chatv1.RegisterDeviceRequest, _ ...grpc.CallOption) (*chatv1.RegisterDeviceResponse, error) {
	return &chatv1.RegisterDeviceResponse{Device: testDevice(), PrekeyCounts: &chatv1.PreKeyCounts{Ec: 3, Kyber: 3, Low: true}}, f.record(ctx, in)
}

func (f *fakeChat) GetMyDevice(ctx context.Context, in *chatv1.GetMyDeviceRequest, _ ...grpc.CallOption) (*chatv1.GetMyDeviceResponse, error) {
	return &chatv1.GetMyDeviceResponse{Device: testDevice(), PrekeyCounts: &chatv1.PreKeyCounts{Ec: 50, Kyber: 50}, HistoryKeyVersion: 2, RotationNeeded: true}, f.record(ctx, in)
}

func (f *fakeChat) UnregisterDevice(ctx context.Context, in *chatv1.UnregisterDeviceRequest, _ ...grpc.CallOption) (*chatv1.UnregisterDeviceResponse, error) {
	return &chatv1.UnregisterDeviceResponse{}, f.record(ctx, in)
}

func (f *fakeChat) UploadPreKeys(ctx context.Context, in *chatv1.UploadPreKeysRequest, _ ...grpc.CallOption) (*chatv1.UploadPreKeysResponse, error) {
	return &chatv1.UploadPreKeysResponse{PrekeyCounts: &chatv1.PreKeyCounts{Ec: 100, Kyber: 100}}, f.record(ctx, in)
}

func (f *fakeChat) ListDevices(ctx context.Context, in *chatv1.ListDevicesRequest, _ ...grpc.CallOption) (*chatv1.ListDevicesResponse, error) {
	return &chatv1.ListDevicesResponse{Devices: []*chatv1.PublicDevice{{Id: deviceID, DeviceNumber: 1, RegistrationId: 9, IdentityKey: []byte{5, 9}, CreateTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}

func (f *fakeChat) ListKeyEvents(ctx context.Context, in *chatv1.ListKeyEventsRequest, _ ...grpc.CallOption) (*chatv1.ListKeyEventsResponse, error) {
	return &chatv1.ListKeyEventsResponse{Events: []*chatv1.KeyEvent{{Id: deviceID, DeviceId: deviceID, DeviceNumber: 1, Type: chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_ADDED, CreateTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}

func (f *fakeChat) GetPreKeyBundles(ctx context.Context, in *chatv1.GetPreKeyBundlesRequest, _ ...grpc.CallOption) (*chatv1.GetPreKeyBundlesResponse, error) {
	return &chatv1.GetPreKeyBundlesResponse{Bundles: []*chatv1.PreKeyBundle{
		{DeviceId: deviceID, SignedPrekey: &chatv1.SignedPreKey{KeyId: 1}, OneTimePrekey: &chatv1.PreKey{KeyId: 4, PublicKey: []byte{5}}, KyberPrekey: &chatv1.SignedPreKey{KeyId: 2}},
		{DeviceId: deviceID, SignedPrekey: &chatv1.SignedPreKey{KeyId: 1}, KyberPrekey: &chatv1.SignedPreKey{KeyId: 3}, KyberLastResort: true},
	}}, f.record(ctx, in)
}

func (f *fakeChat) ListEnvelopes(ctx context.Context, in *chatv1.ListEnvelopesRequest, _ ...grpc.CallOption) (*chatv1.ListEnvelopesResponse, error) {
	return &chatv1.ListEnvelopesResponse{Envelopes: []*chatv1.Envelope{{
		Id: deviceID, MessageId: messageID, ConversationId: conversationID, SenderUserId: userID, SenderDeviceId: deviceID,
		SenderDeviceNumber: 3, Type: chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY, Content: []byte{0, 255, 7}, CreateTime: timestamppb.New(t0),
	}}}, f.record(ctx, in)
}

func (f *fakeChat) AckEnvelopes(ctx context.Context, in *chatv1.AckEnvelopesRequest, _ ...grpc.CallOption) (*chatv1.AckEnvelopesResponse, error) {
	return &chatv1.AckEnvelopesResponse{}, f.record(ctx, in)
}

func (f *fakeChat) PutArchiveEntries(ctx context.Context, in *chatv1.PutArchiveEntriesRequest, _ ...grpc.CallOption) (*chatv1.PutArchiveEntriesResponse, error) {
	return &chatv1.PutArchiveEntriesResponse{AcceptedMessageIds: []string{messageID}}, f.record(ctx, in)
}

func (f *fakeChat) ListArchiveEntries(ctx context.Context, in *chatv1.ListArchiveEntriesRequest, _ ...grpc.CallOption) (*chatv1.ListArchiveEntriesResponse, error) {
	return &chatv1.ListArchiveEntriesResponse{Entries: []*chatv1.ArchiveEntry{{MessageId: messageID, ConversationId: conversationID, KeyVersion: 2, Content: []byte{9}, CreateTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}

func (f *fakeChat) DeleteArchive(ctx context.Context, in *chatv1.DeleteArchiveRequest, _ ...grpc.CallOption) (*chatv1.DeleteArchiveResponse, error) {
	return &chatv1.DeleteArchiveResponse{}, f.record(ctx, in)
}

func (f *fakeChat) ClaimHistoryKey(ctx context.Context, in *chatv1.ClaimHistoryKeyRequest, _ ...grpc.CallOption) (*chatv1.ClaimHistoryKeyResponse, error) {
	return &chatv1.ClaimHistoryKeyResponse{HistoryKeyVersion: 1}, f.record(ctx, in)
}

func (f *fakeChat) CreateLinkRequest(ctx context.Context, in *chatv1.CreateLinkRequestRequest, _ ...grpc.CallOption) (*chatv1.CreateLinkRequestResponse, error) {
	return &chatv1.CreateLinkRequestResponse{LinkRequestId: linkRequestID, ExpireTime: timestamppb.New(t0)}, f.record(ctx, in)
}

func (f *fakeChat) AnswerLinkRequest(ctx context.Context, in *chatv1.AnswerLinkRequestRequest, _ ...grpc.CallOption) (*chatv1.AnswerLinkRequestResponse, error) {
	return &chatv1.AnswerLinkRequestResponse{}, f.record(ctx, in)
}

func (f *fakeChat) GetLinkRequest(ctx context.Context, in *chatv1.GetLinkRequestRequest, _ ...grpc.CallOption) (*chatv1.GetLinkRequestResponse, error) {
	return &chatv1.GetLinkRequestResponse{LinkRequestId: linkRequestID, ExpireTime: timestamppb.New(t0)}, f.record(ctx, in)
}

func (f *fakeChat) CompleteLinkRequest(ctx context.Context, in *chatv1.CompleteLinkRequestRequest, _ ...grpc.CallOption) (*chatv1.CompleteLinkRequestResponse, error) {
	return &chatv1.CompleteLinkRequestResponse{Device: testDevice()}, f.record(ctx, in)
}

func (f *fakeChat) RotateHistoryKey(ctx context.Context, in *chatv1.RotateHistoryKeyRequest, _ ...grpc.CallOption) (*chatv1.RotateHistoryKeyResponse, error) {
	return &chatv1.RotateHistoryKeyResponse{}, f.record(ctx, in)
}

func (f *fakeChat) ListHistoryKeyShares(ctx context.Context, in *chatv1.ListHistoryKeySharesRequest, _ ...grpc.CallOption) (*chatv1.ListHistoryKeySharesResponse, error) {
	return &chatv1.ListHistoryKeySharesResponse{Shares: []*chatv1.HistoryKeyShare{{Id: deviceID, SenderDeviceId: deviceID, HistoryKeyVersion: 3, Bundle: []byte{1}, CreateTime: timestamppb.New(t0)}}}, f.record(ctx, in)
}

func (f *fakeChat) AckHistoryKeyShares(ctx context.Context, in *chatv1.AckHistoryKeySharesRequest, _ ...grpc.CallOption) (*chatv1.AckHistoryKeySharesResponse, error) {
	return &chatv1.AckHistoryKeySharesResponse{}, f.record(ctx, in)
}

func TestE2ERoutes(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	ec, ky, sig := []byte{5, 1, 2}, []byte{8, 3}, []byte{7, 7}
	signed := func(id uint32, k []byte) string {
		return `{"key_id":` + string(rune('0'+id)) + `,"public_key":"` + b64(k) + `","signature":"` + b64(sig) + `"}`
	}
	reg := `{"registration_id":77,"identity_key":"` + b64(ec) + `","signed_prekey":` + signed(1, ec) + `,"kyber_last_resort_prekey":` + signed(2, ky) +
		`,"one_time_prekeys":[{"key_id":5,"public_key":"` + b64(ec) + `"}],"kyber_prekeys":[` + signed(6, ky) + `]}`
	sp := func(id uint32, k []byte) *chatv1.SignedPreKey {
		return &chatv1.SignedPreKey{KeyId: id, PublicKey: k, Signature: sig}
	}
	c := "/v1/direct-messages/conversations/" + conversationID
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"POST", "/v1/me/e2e/device", reg, 200, &chatv1.RegisterDeviceRequest{
			RegistrationId: 77, IdentityKey: ec, SignedPrekey: sp(1, ec), KyberLastResortPrekey: sp(2, ky),
			OneTimePrekeys: []*chatv1.PreKey{{KeyId: 5, PublicKey: ec}}, KyberPrekeys: []*chatv1.SignedPreKey{sp(6, ky)},
		}},
		{"GET", "/v1/me/e2e/device", "", 200, &chatv1.GetMyDeviceRequest{}},
		{"DELETE", "/v1/me/e2e/device", "", 204, &chatv1.UnregisterDeviceRequest{}},
		{"POST", "/v1/me/e2e/device/prekeys", `{"signed_prekey":` + signed(3, ec) + `}`, 200, &chatv1.UploadPreKeysRequest{
			SignedPrekey: sp(3, ec), OneTimePrekeys: []*chatv1.PreKey{}, KyberPrekeys: []*chatv1.SignedPreKey{},
		}},
		{"GET", "/v1/users/" + userID + "/e2e/devices", "", 200, &chatv1.ListDevicesRequest{UserId: userID}},
		{"GET", "/v1/users/" + userID + "/e2e/key-events?page_size=3&page_token=P", "", 200, &chatv1.ListKeyEventsRequest{UserId: userID, PageSize: 3, PageToken: "P"}},
		{"POST", "/v1/users/" + userID + "/e2e/prekey-bundles", `{"device_ids":["` + deviceID + `"]}`, 200, &chatv1.GetPreKeyBundlesRequest{UserId: userID, DeviceIds: []string{deviceID}}},
		{"GET", "/v1/me/e2e/envelopes?page_size=10", "", 200, &chatv1.ListEnvelopesRequest{PageSize: 10}},
		{"POST", "/v1/me/e2e/envelopes/ack", `{"envelope_ids":["` + deviceID + `"]}`, 204, &chatv1.AckEnvelopesRequest{EnvelopeIds: []string{deviceID}}},
		{"POST", "/v1/me/e2e/archive", `{"history_key_version":2,"entries":[{"message_id":"` + messageID + `","content":"` + b64([]byte{4}) + `"}]}`, 200,
			&chatv1.PutArchiveEntriesRequest{HistoryKeyVersion: 2, Entries: []*chatv1.NewArchiveEntry{{MessageId: messageID, Content: []byte{4}}}}},
		{"DELETE", "/v1/me/e2e/archive", "", 204, &chatv1.DeleteArchiveRequest{}},
		{"GET", c + "/archive?after_message_id=" + messageID, "", 200, &chatv1.ListArchiveEntriesRequest{ConversationId: conversationID, AfterMessageId: messageID}},
		{"DELETE", c + "/archive", "", 204, &chatv1.DeleteArchiveRequest{ConversationId: conversationID}},
		{"POST", "/v1/me/e2e/history-key", "", 200, &chatv1.ClaimHistoryKeyRequest{}},
		{"POST", "/v1/me/e2e/history-key/rotate", `{"history_key_version":3,"shares":[{"device_id":"` + deviceID + `","bundle":"` + b64([]byte{1}) + `"}]}`, 204,
			&chatv1.RotateHistoryKeyRequest{HistoryKeyVersion: 3, Shares: []*chatv1.HistoryKeyShareInput{{DeviceId: deviceID, Bundle: []byte{1}}}}},
		{"GET", "/v1/me/e2e/history-key-shares", "", 200, &chatv1.ListHistoryKeySharesRequest{}},
		{"POST", "/v1/me/e2e/history-key-shares/ack", `{"share_ids":["` + deviceID + `"]}`, 204, &chatv1.AckHistoryKeySharesRequest{ShareIds: []string{deviceID}}},
		{"POST", "/v1/me/e2e/link-requests", "", 201, &chatv1.CreateLinkRequestRequest{}},
		{"GET", "/v1/me/e2e/link-requests/" + linkRequestID, "", 200, &chatv1.GetLinkRequestRequest{LinkRequestId: linkRequestID}},
		{"POST", "/v1/me/e2e/link-requests/" + linkRequestID + "/answer", `{"history_key_version":1,"answer":"` + b64([]byte{2}) + `"}`, 204,
			&chatv1.AnswerLinkRequestRequest{LinkRequestId: linkRequestID, HistoryKeyVersion: 1, Answer: []byte{2}}},
		{"POST", "/v1/me/e2e/link-requests/" + linkRequestID + "/complete", "", 200, &chatv1.CompleteLinkRequestRequest{LinkRequestId: linkRequestID}},
		{"POST", "/v1/direct-messages", `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `","envelopes":[{"device_id":"` + deviceID +
			`","registration_id":9,"type":"prekey","content":"` + b64([]byte{1, 2}) + `"}]}`, 201, &chatv1.SendDirectMessageRequest{
			RecipientUserId: userID, ClientRequestId: requestID,
			Envelopes: []*chatv1.OutgoingEnvelope{{DeviceId: deviceID, RegistrationId: 9, Type: chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY, Content: []byte{1, 2}}},
		}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; chat'e giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	env := decodeBody[struct {
		Envelopes []envelopeJSON `json:"envelopes"`
	}](t, as(h, "GET", "/v1/me/e2e/envelopes", ""))
	if len(env.Envelopes) != 1 || env.Envelopes[0].Type != "prekey" || !bytes.Equal(env.Envelopes[0].Content, []byte{0, 255, 7}) || env.Envelopes[0].SenderDeviceNumber != 3 {
		t.Fatalf("zarflar: %+v", env)
	}
	b := decodeBody[struct {
		Bundles []bundleJSON `json:"bundles"`
	}](t, as(h, "POST", "/v1/users/"+userID+"/e2e/prekey-bundles", `{}`))
	if len(b.Bundles) != 2 || b.Bundles[0].OneTimePreKey == nil || b.Bundles[1].OneTimePreKey != nil || !b.Bundles[1].KyberLastResort {
		t.Fatalf("paketler: %+v", b)
	}
	if w := as(h, "GET", "/v1/me/e2e/link-requests/"+linkRequestID, ""); strings.Contains(w.Body.String(), `"answer"`) {
		t.Fatalf("cevapsız istek cevap taşıyor: %s", w.Body)
	}
	if w := as(h, "GET", c+"/messages", ""); !strings.Contains(w.Body.String(), `"encryption":"unknown"`) {
		// The fake's messages leave encryption unset.
		t.Fatalf("şifreleme alanı yok: %s", w.Body)
	}
}

func TestE2EBadRequests(t *testing.T) {
	f := &fakeChat{}
	h := chatRouter(f)
	env := func(extra string) string {
		return `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `"` + extra + `}`
	}
	one := `[{"device_id":"` + deviceID + `","registration_id":1,"type":"prekey","content":"AQ=="}]`
	for name, tc := range map[string]struct{ method, path, body string }{
		"metin ve zarf":       {"POST", "/v1/direct-messages", env(`,"body":"x","envelopes":` + one)},
		"zarf türü":           {"POST", "/v1/direct-messages", env(`,"envelopes":[{"device_id":"` + deviceID + `","registration_id":1,"type":"plain","content":"AQ=="}]`)},
		"boş zarf":            {"POST", "/v1/direct-messages", env(`,"envelopes":[{"device_id":"` + deviceID + `","registration_id":1,"type":"prekey","content":""}]`)},
		"bozuk cihaz":         {"POST", "/v1/direct-messages", env(`,"envelopes":[{"device_id":"x","registration_id":1,"type":"prekey","content":"AQ=="}]`)},
		"büyük zarf":          {"POST", "/v1/direct-messages", env(`,"envelopes":[{"device_id":"` + deviceID + `","registration_id":1,"type":"prekey","content":"` + b64(make([]byte, 64<<10+1)) + `"}]`)},
		"kayıt eksik":         {"POST", "/v1/me/e2e/device", `{"registration_id":1}`},
		"bilinmeyen alan":     {"POST", "/v1/me/e2e/device/prekeys", `{"private_key":"AQ=="}`},
		"bozuk base64":        {"POST", "/v1/me/e2e/archive", `{"history_key_version":1,"entries":[{"message_id":"` + messageID + `","content":"%%%"}]}`},
		"boş arşiv":           {"POST", "/v1/me/e2e/archive", `{"history_key_version":1,"entries":[]}`},
		"boş onay":            {"POST", "/v1/me/e2e/envelopes/ack", `{"envelope_ids":[]}`},
		"bozuk onay":          {"POST", "/v1/me/e2e/envelopes/ack", `{"envelope_ids":["x"]}`},
		"çok cihaz":           {"POST", "/v1/users/" + userID + "/e2e/prekey-bundles", `{"device_ids":["` + strings.Repeat(deviceID+`","`, 5) + deviceID + `"]}`},
		"bozuk kullanıcı":     {"GET", "/v1/users/x/e2e/devices", ""},
		"bozuk eşleştirme":    {"GET", "/v1/me/e2e/link-requests/x", ""},
		"bozuk paylaşım":      {"POST", "/v1/me/e2e/history-key/rotate", `{"history_key_version":2,"shares":[{"device_id":"x","bundle":"AQ=="}]}`},
		"bozuk arşiv sonrası": {"GET", "/v1/direct-messages/conversations/" + conversationID + "/archive?after_message_id=x", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek chat'e gitti")
	}
	if w := do(h, "GET", "/v1/me/e2e/device", "", nil); w.Code != 401 {
		t.Fatalf("token'sız: %d", w.Code)
	}
	if w := do(h, "POST", "/v1/me/e2e/history-key", "", map[string]string{"Authorization": "Bearer USERTOKEN"}); w.Code != 400 {
		t.Fatalf("istemci başlığı olmadan: %d", w.Code)
	}
	// A message with envelopes may be larger than a text message.
	big := env(`,"envelopes":[` + strings.TrimSuffix(strings.Repeat(`{"device_id":"`+deviceID+`","registration_id":1,"type":"whisper","content":"`+b64(make([]byte, 60<<10))+`"},`, 9), ",") + `]`)
	if w := as(h, "POST", "/v1/direct-messages", big); w.Code != 201 {
		t.Fatalf("büyük zarflı gönderim: %d %s", w.Code, w.Body)
	}
}

func TestE2EErrors(t *testing.T) {
	for _, tc := range []struct {
		reason, typ string
		code        int
	}{
		{"ERROR_REASON_E2E_DEVICE_NOT_REGISTERED", "e2e-device-not-registered", 409},
		{"ERROR_REASON_E2E_DEVICE_ALREADY_REGISTERED", "e2e-device-already-registered", 409},
		{"ERROR_REASON_E2E_DEVICE_LIMIT", "e2e-device-limit", 409},
		{"ERROR_REASON_E2E_RECIPIENT_UNAVAILABLE", "e2e-recipient-unavailable", 409},
		{"ERROR_REASON_E2E_REQUIRED", "e2e-required", 409},
		{"ERROR_REASON_E2E_NOT_PAIRED", "e2e-not-paired", 409},
		{"ERROR_REASON_E2E_ALREADY_PAIRED", "e2e-already-paired", 409},
		{"ERROR_REASON_E2E_PAIRED_DEVICE_EXISTS", "e2e-paired-device-exists", 409},
		{"ERROR_REASON_E2E_HISTORY_KEY_STALE", "e2e-history-key-stale", 409},
		{"ERROR_REASON_E2E_LINK_REQUEST_NOT_FOUND", "e2e-link-request-not-found", 404},
		{"ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED", "direct-message-not-allowed", 403},
	} {
		f := &fakeChat{err: chatErr(codes.FailedPrecondition, tc.reason)}
		w := as(chatRouter(f), "POST", "/v1/me/e2e/history-key", "")
		if w.Code != tc.code || problemType(t, w) != tc.typ {
			t.Errorf("%s: %d %s", tc.reason, w.Code, problemType(t, w))
		}
	}

	// The device lists of e2e-devices-changed reach the client; anything
	// that is not a device ID is dropped.
	st, _ := status.New(codes.FailedPrecondition, "istek reddedildi").WithDetails(&errdetails.ErrorInfo{
		Reason: "ERROR_REASON_E2E_DEVICES_CHANGED", Domain: "chat.tayfam.app",
		Metadata: map[string]string{"missing_device_ids": deviceID + ",x", "stale_device_ids": ""},
	})
	f := &fakeChat{err: st.Err()}
	body := `{"recipient_user_id":"` + userID + `","client_request_id":"` + requestID + `","envelopes":[{"device_id":"` + deviceID + `","registration_id":1,"type":"whisper","content":"AQ=="}]}`
	w := as(chatRouter(f), "POST", "/v1/direct-messages", body)
	var p devicesChangedProblem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 409 || problemType(t, w) != "e2e-devices-changed" ||
		len(p.MissingDeviceIDs) != 1 || p.MissingDeviceIDs[0] != deviceID || p.StaleDeviceIDs == nil || len(p.StaleDeviceIDs) != 0 ||
		w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("cihaz listesi: %d %s", w.Code, w.Body)
	}
}

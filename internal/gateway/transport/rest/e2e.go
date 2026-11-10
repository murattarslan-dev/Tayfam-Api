package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

// E2EClient is chat's E2EService as the gateway uses it (issue #127,
// ADR-0018). The gateway checks shapes and sizes only; chat checks
// everything again, and nobody but the clients looks inside the bytes.
type E2EClient interface {
	RegisterDevice(ctx context.Context, in *chatv1.RegisterDeviceRequest, opts ...grpc.CallOption) (*chatv1.RegisterDeviceResponse, error)
	GetMyDevice(ctx context.Context, in *chatv1.GetMyDeviceRequest, opts ...grpc.CallOption) (*chatv1.GetMyDeviceResponse, error)
	UnregisterDevice(ctx context.Context, in *chatv1.UnregisterDeviceRequest, opts ...grpc.CallOption) (*chatv1.UnregisterDeviceResponse, error)
	UploadPreKeys(ctx context.Context, in *chatv1.UploadPreKeysRequest, opts ...grpc.CallOption) (*chatv1.UploadPreKeysResponse, error)
	ListDevices(ctx context.Context, in *chatv1.ListDevicesRequest, opts ...grpc.CallOption) (*chatv1.ListDevicesResponse, error)
	ListKeyEvents(ctx context.Context, in *chatv1.ListKeyEventsRequest, opts ...grpc.CallOption) (*chatv1.ListKeyEventsResponse, error)
	GetPreKeyBundles(ctx context.Context, in *chatv1.GetPreKeyBundlesRequest, opts ...grpc.CallOption) (*chatv1.GetPreKeyBundlesResponse, error)
	ListEnvelopes(ctx context.Context, in *chatv1.ListEnvelopesRequest, opts ...grpc.CallOption) (*chatv1.ListEnvelopesResponse, error)
	AckEnvelopes(ctx context.Context, in *chatv1.AckEnvelopesRequest, opts ...grpc.CallOption) (*chatv1.AckEnvelopesResponse, error)
	PutArchiveEntries(ctx context.Context, in *chatv1.PutArchiveEntriesRequest, opts ...grpc.CallOption) (*chatv1.PutArchiveEntriesResponse, error)
	ListArchiveEntries(ctx context.Context, in *chatv1.ListArchiveEntriesRequest, opts ...grpc.CallOption) (*chatv1.ListArchiveEntriesResponse, error)
	DeleteArchive(ctx context.Context, in *chatv1.DeleteArchiveRequest, opts ...grpc.CallOption) (*chatv1.DeleteArchiveResponse, error)
	ClaimHistoryKey(ctx context.Context, in *chatv1.ClaimHistoryKeyRequest, opts ...grpc.CallOption) (*chatv1.ClaimHistoryKeyResponse, error)
	CreateLinkRequest(ctx context.Context, in *chatv1.CreateLinkRequestRequest, opts ...grpc.CallOption) (*chatv1.CreateLinkRequestResponse, error)
	AnswerLinkRequest(ctx context.Context, in *chatv1.AnswerLinkRequestRequest, opts ...grpc.CallOption) (*chatv1.AnswerLinkRequestResponse, error)
	GetLinkRequest(ctx context.Context, in *chatv1.GetLinkRequestRequest, opts ...grpc.CallOption) (*chatv1.GetLinkRequestResponse, error)
	CompleteLinkRequest(ctx context.Context, in *chatv1.CompleteLinkRequestRequest, opts ...grpc.CallOption) (*chatv1.CompleteLinkRequestResponse, error)
	RotateHistoryKey(ctx context.Context, in *chatv1.RotateHistoryKeyRequest, opts ...grpc.CallOption) (*chatv1.RotateHistoryKeyResponse, error)
	ListHistoryKeyShares(ctx context.Context, in *chatv1.ListHistoryKeySharesRequest, opts ...grpc.CallOption) (*chatv1.ListHistoryKeySharesResponse, error)
	AckHistoryKeyShares(ctx context.Context, in *chatv1.AckHistoryKeySharesRequest, opts ...grpc.CallOption) (*chatv1.AckHistoryKeySharesResponse, error)
}

// Body limits of the end-to-end routes (ADR-0018 item 8; base64 grows
// bytes by a third).
const (
	// maxE2ESendBody: 16 envelopes of 64 KiB.
	maxE2ESendBody = 1536 << 10
	// maxE2EKeysBody: 100 EC and 100 Kyber pre-keys with signatures.
	maxE2EKeysBody = 512 << 10
	// maxE2EArchiveBody: 50 entries of 32 KiB.
	maxE2EArchiveBody = 2560 << 10
	// maxE2EShareBody: a pairing answer or five key shares of 16 KiB.
	maxE2EShareBody = 128 << 10
	// maxE2EIDsBody: 100 IDs.
	maxE2EIDsBody = 8 << 10
	maxE2EIDs     = 100
)

func registerE2E(mux *http.ServeMux, h chatHandlers) {
	a := func(next coreHandler) http.HandlerFunc { return authenticated(h.d.Verifier, h.d.Log, next) }
	mux.HandleFunc("GET /v1/me/e2e/device", a(h.getMyE2EDevice))
	mux.HandleFunc("POST /v1/me/e2e/device", a(h.registerE2EDevice))
	mux.HandleFunc("DELETE /v1/me/e2e/device", a(h.unregisterE2EDevice))
	mux.HandleFunc("POST /v1/me/e2e/device/prekeys", a(h.uploadE2EPreKeys))
	mux.HandleFunc("GET /v1/users/{user_id}/e2e/devices", a(h.listE2EDevices))
	mux.HandleFunc("GET /v1/users/{user_id}/e2e/key-events", a(h.listE2EKeyEvents))
	mux.HandleFunc("POST /v1/users/{user_id}/e2e/prekey-bundles", a(h.claimE2EPreKeyBundles))
	mux.HandleFunc("GET /v1/me/e2e/envelopes", a(h.listE2EEnvelopes))
	mux.HandleFunc("POST /v1/me/e2e/envelopes/ack", a(h.ackE2EEnvelopes))
	mux.HandleFunc("POST /v1/me/e2e/archive", a(h.putE2EArchive))
	mux.HandleFunc("DELETE /v1/me/e2e/archive", a(h.deleteE2EArchive))
	mux.HandleFunc("GET /v1/direct-messages/conversations/{conversation_id}/archive", a(h.listE2EArchive))
	mux.HandleFunc("DELETE /v1/direct-messages/conversations/{conversation_id}/archive", a(h.deleteE2EArchive))
	mux.HandleFunc("POST /v1/me/e2e/history-key", a(h.claimE2EHistoryKey))
	mux.HandleFunc("POST /v1/me/e2e/history-key/rotate", a(h.rotateE2EHistoryKey))
	mux.HandleFunc("GET /v1/me/e2e/history-key-shares", a(h.listE2EHistoryKeyShares))
	mux.HandleFunc("POST /v1/me/e2e/history-key-shares/ack", a(h.ackE2EHistoryKeyShares))
	mux.HandleFunc("POST /v1/me/e2e/link-requests", a(h.createE2ELinkRequest))
	mux.HandleFunc("GET /v1/me/e2e/link-requests/{link_request_id}", a(h.getE2ELinkRequest))
	mux.HandleFunc("POST /v1/me/e2e/link-requests/{link_request_id}/answer", a(h.answerE2ELinkRequest))
	mux.HandleFunc("POST /v1/me/e2e/link-requests/{link_request_id}/complete", a(h.completeE2ELinkRequest))
}

// devicesChangedProblem is e2e-devices-changed with the device lists
// (E2EDevicesChangedProblem). Only device IDs, never content.
type devicesChangedProblem struct {
	httpx.Problem
	MissingDeviceIDs []string `json:"missing_device_ids"`
	StaleDeviceIDs   []string `json:"stale_device_ids"`
}

// splitIDs splits chat's comma-separated device IDs, keeping well-formed
// ones only.
func splitIDs(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if v := s[start:i]; validUUID(v) {
				out = append(out, v)
			}
			start = i + 1
		}
	}
	return out
}

// failE2E answers a chat error; e2e-devices-changed also lists the
// devices.
func (h chatHandlers) failE2E(w http.ResponseWriter, r *http.Request, err error) {
	for _, d := range status.Convert(err).Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "chat.tayfam.app" || info.GetReason() != chatv1.ErrorReason_ERROR_REASON_E2E_DEVICES_CHANGED.String() {
			continue
		}
		p := devicesChangedProblem{
			Problem: httpx.Problem{
				Type: httpx.ProblemTypeBase + "e2e-devices-changed", Title: http.StatusText(http.StatusConflict),
				Status: http.StatusConflict, Instance: r.URL.Path, RequestID: httpx.RequestIDFrom(r.Context()),
			},
			MissingDeviceIDs: splitIDs(info.GetMetadata()["missing_device_ids"]),
			StaleDeviceIDs:   splitIDs(info.GetMetadata()["stale_device_ids"]),
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(p)
		return
	}
	h.fail(w, r, err)
}

// JSON shapes (OpenAPI E2E* schemas). []byte is standard base64 in JSON.

type preKeyJSON struct {
	KeyID     uint32 `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}

type signedPreKeyJSON struct {
	KeyID     uint32 `json:"key_id"`
	PublicKey []byte `json:"public_key"`
	Signature []byte `json:"signature"`
}

func (k *signedPreKeyJSON) proto() *chatv1.SignedPreKey {
	if k == nil {
		return nil
	}
	return &chatv1.SignedPreKey{KeyId: k.KeyID, PublicKey: k.PublicKey, Signature: k.Signature}
}

func signedFromProto(k *chatv1.SignedPreKey) signedPreKeyJSON {
	return signedPreKeyJSON{KeyID: k.GetKeyId(), PublicKey: k.GetPublicKey(), Signature: k.GetSignature()}
}

func preKeysProto(ks []preKeyJSON) []*chatv1.PreKey {
	out := make([]*chatv1.PreKey, 0, len(ks))
	for _, k := range ks {
		out = append(out, &chatv1.PreKey{KeyId: k.KeyID, PublicKey: k.PublicKey})
	}
	return out
}

func signedListProto(ks []signedPreKeyJSON) []*chatv1.SignedPreKey {
	out := make([]*chatv1.SignedPreKey, 0, len(ks))
	for i := range ks {
		out = append(out, ks[i].proto())
	}
	return out
}

type e2eDeviceJSON struct {
	ID             string    `json:"id"`
	DeviceNumber   uint32    `json:"device_number"`
	RegistrationID uint32    `json:"registration_id"`
	IdentityKey    []byte    `json:"identity_key"`
	Paired         bool      `json:"paired"`
	CreatedAt      time.Time `json:"created_at"`
}

func e2eDeviceFrom(d *chatv1.Device) e2eDeviceJSON {
	return e2eDeviceJSON{
		ID: d.GetId(), DeviceNumber: d.GetDeviceNumber(), RegistrationID: d.GetRegistrationId(),
		IdentityKey: d.GetIdentityKey(), Paired: d.GetPaired(), CreatedAt: d.GetCreateTime().AsTime(),
	}
}

type preKeyCountsJSON struct {
	EC    uint32 `json:"ec"`
	Kyber uint32 `json:"kyber"`
	Low   bool   `json:"low"`
}

func countsFrom(c *chatv1.PreKeyCounts) preKeyCountsJSON {
	return preKeyCountsJSON{EC: c.GetEc(), Kyber: c.GetKyber(), Low: c.GetLow()}
}

type e2eRegisterBody struct {
	RegistrationID        uint32             `json:"registration_id"`
	IdentityKey           []byte             `json:"identity_key"`
	SignedPreKey          *signedPreKeyJSON  `json:"signed_prekey"`
	KyberLastResortPreKey *signedPreKeyJSON  `json:"kyber_last_resort_prekey"`
	OneTimePreKeys        []preKeyJSON       `json:"one_time_prekeys"`
	KyberPreKeys          []signedPreKeyJSON `json:"kyber_prekeys"`
}

func (h chatHandlers) registerE2EDevice(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b e2eRegisterBody
	if !decodeLimit(w, r, &b, maxE2EKeysBody) {
		return
	}
	if b.SignedPreKey == nil || b.KyberLastResortPreKey == nil || len(b.OneTimePreKeys) > 100 || len(b.KyberPreKeys) > 100 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.E2E.RegisterDevice(ctx, &chatv1.RegisterDeviceRequest{
		RegistrationId: b.RegistrationID, IdentityKey: b.IdentityKey,
		SignedPrekey: b.SignedPreKey.proto(), KyberLastResortPrekey: b.KyberLastResortPreKey.proto(),
		OneTimePrekeys: preKeysProto(b.OneTimePreKeys), KyberPrekeys: signedListProto(b.KyberPreKeys),
	})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Device       e2eDeviceJSON    `json:"device"`
		PreKeyCounts preKeyCountsJSON `json:"prekey_counts"`
	}{e2eDeviceFrom(resp.GetDevice()), countsFrom(resp.GetPrekeyCounts())})
}

func (h chatHandlers) getMyE2EDevice(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.E2E.GetMyDevice(ctx, &chatv1.GetMyDeviceRequest{})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Device            e2eDeviceJSON    `json:"device"`
		PreKeyCounts      preKeyCountsJSON `json:"prekey_counts"`
		HistoryKeyVersion uint32           `json:"history_key_version"`
		RotationNeeded    bool             `json:"rotation_needed"`
		OtherPairedDevice bool             `json:"other_paired_device"`
	}{
		e2eDeviceFrom(resp.GetDevice()), countsFrom(resp.GetPrekeyCounts()),
		resp.GetHistoryKeyVersion(), resp.GetRotationNeeded(), resp.GetOtherPairedDevice(),
	})
}

func (h chatHandlers) unregisterE2EDevice(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.E2E.UnregisterDevice(ctx, &chatv1.UnregisterDeviceRequest{}); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type e2eUploadBody struct {
	SignedPreKey          *signedPreKeyJSON  `json:"signed_prekey"`
	KyberLastResortPreKey *signedPreKeyJSON  `json:"kyber_last_resort_prekey"`
	OneTimePreKeys        []preKeyJSON       `json:"one_time_prekeys"`
	KyberPreKeys          []signedPreKeyJSON `json:"kyber_prekeys"`
}

func (h chatHandlers) uploadE2EPreKeys(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b e2eUploadBody
	if !decodeLimit(w, r, &b, maxE2EKeysBody) {
		return
	}
	if len(b.OneTimePreKeys) > 100 || len(b.KyberPreKeys) > 100 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.E2E.UploadPreKeys(ctx, &chatv1.UploadPreKeysRequest{
		SignedPrekey: b.SignedPreKey.proto(), KyberLastResortPrekey: b.KyberLastResortPreKey.proto(),
		OneTimePrekeys: preKeysProto(b.OneTimePreKeys), KyberPrekeys: signedListProto(b.KyberPreKeys),
	})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, countsFrom(resp.GetPrekeyCounts()))
}

type publicDeviceJSON struct {
	ID             string    `json:"id"`
	DeviceNumber   uint32    `json:"device_number"`
	RegistrationID uint32    `json:"registration_id"`
	IdentityKey    []byte    `json:"identity_key"`
	CreatedAt      time.Time `json:"created_at"`
}

func (h chatHandlers) listE2EDevices(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.d.E2E.ListDevices(ctx, &chatv1.ListDevicesRequest{UserId: uid})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Devices []publicDeviceJSON `json:"devices"`
	}{Devices: make([]publicDeviceJSON, 0, len(resp.GetDevices()))}
	for _, d := range resp.GetDevices() {
		out.Devices = append(out.Devices, publicDeviceJSON{
			ID: d.GetId(), DeviceNumber: d.GetDeviceNumber(), RegistrationID: d.GetRegistrationId(),
			IdentityKey: d.GetIdentityKey(), CreatedAt: d.GetCreateTime().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

var keyEventTypeNames = map[chatv1.KeyEventType]string{
	chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_ADDED:     "device_added",
	chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_REMOVED:   "device_removed",
	chatv1.KeyEventType_KEY_EVENT_TYPE_IDENTITY_CHANGED: "identity_changed",
}

type keyEventJSON struct {
	ID           string    `json:"id"`
	DeviceID     string    `json:"device_id"`
	DeviceNumber uint32    `json:"device_number"`
	Type         string    `json:"type"`
	CreatedAt    time.Time `json:"created_at"`
}

func (h chatHandlers) listE2EKeyEvents(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.E2E.ListKeyEvents(ctx, &chatv1.ListKeyEventsRequest{UserId: uid, PageSize: size, PageToken: token})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Events        []keyEventJSON `json:"events"`
		NextPageToken string         `json:"next_page_token,omitempty"`
	}{Events: make([]keyEventJSON, 0, len(resp.GetEvents())), NextPageToken: resp.GetNextPageToken()}
	for _, e := range resp.GetEvents() {
		out.Events = append(out.Events, keyEventJSON{
			ID: e.GetId(), DeviceID: e.GetDeviceId(), DeviceNumber: e.GetDeviceNumber(),
			Type: nameOr(keyEventTypeNames, e.GetType()), CreatedAt: e.GetCreateTime().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type bundleJSON struct {
	DeviceID        string           `json:"device_id"`
	DeviceNumber    uint32           `json:"device_number"`
	RegistrationID  uint32           `json:"registration_id"`
	IdentityKey     []byte           `json:"identity_key"`
	SignedPreKey    signedPreKeyJSON `json:"signed_prekey"`
	OneTimePreKey   *preKeyJSON      `json:"one_time_prekey,omitempty"`
	KyberPreKey     signedPreKeyJSON `json:"kyber_prekey"`
	KyberLastResort bool             `json:"kyber_last_resort"`
}

func validUUIDs(vs []string, most int) bool {
	if len(vs) > most {
		return false
	}
	for _, v := range vs {
		if !validUUID(v) {
			return false
		}
	}
	return true
}

func (h chatHandlers) claimE2EPreKeyBundles(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	uid, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	var b struct {
		DeviceIDs []string `json:"device_ids"`
	}
	if !decodeLimit(w, r, &b, maxE2EIDsBody) {
		return
	}
	if !validUUIDs(b.DeviceIDs, 5) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.E2E.GetPreKeyBundles(ctx, &chatv1.GetPreKeyBundlesRequest{UserId: uid, DeviceIds: b.DeviceIDs})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Bundles []bundleJSON `json:"bundles"`
	}{Bundles: make([]bundleJSON, 0, len(resp.GetBundles()))}
	for _, x := range resp.GetBundles() {
		j := bundleJSON{
			DeviceID: x.GetDeviceId(), DeviceNumber: x.GetDeviceNumber(), RegistrationID: x.GetRegistrationId(),
			IdentityKey: x.GetIdentityKey(), SignedPreKey: signedFromProto(x.GetSignedPrekey()),
			KyberPreKey: signedFromProto(x.GetKyberPrekey()), KyberLastResort: x.GetKyberLastResort(),
		}
		if k := x.GetOneTimePrekey(); k != nil {
			j.OneTimePreKey = &preKeyJSON{KeyID: k.GetKeyId(), PublicKey: k.GetPublicKey()}
		}
		out.Bundles = append(out.Bundles, j)
	}
	writeJSON(w, http.StatusOK, out)
}

var envelopeTypeNames = map[chatv1.EnvelopeType]string{
	chatv1.EnvelopeType_ENVELOPE_TYPE_WHISPER: "whisper",
	chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY:  "prekey",
}

var envelopeTypes = map[string]chatv1.EnvelopeType{
	"whisper": chatv1.EnvelopeType_ENVELOPE_TYPE_WHISPER,
	"prekey":  chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY,
}

type envelopeJSON struct {
	ID                 string    `json:"id"`
	MessageID          string    `json:"message_id"`
	ConversationID     string    `json:"conversation_id"`
	SenderUserID       string    `json:"sender_user_id"`
	SenderDeviceID     string    `json:"sender_device_id"`
	SenderDeviceNumber uint32    `json:"sender_device_number"`
	Type               string    `json:"type"`
	Content            []byte    `json:"content"`
	CreatedAt          time.Time `json:"created_at"`
}

func (h chatHandlers) listE2EEnvelopes(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.E2E.ListEnvelopes(ctx, &chatv1.ListEnvelopesRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Envelopes     []envelopeJSON `json:"envelopes"`
		NextPageToken string         `json:"next_page_token,omitempty"`
	}{Envelopes: make([]envelopeJSON, 0, len(resp.GetEnvelopes())), NextPageToken: resp.GetNextPageToken()}
	for _, e := range resp.GetEnvelopes() {
		out.Envelopes = append(out.Envelopes, envelopeJSON{
			ID: e.GetId(), MessageID: e.GetMessageId(), ConversationID: e.GetConversationId(),
			SenderUserID: e.GetSenderUserId(), SenderDeviceID: e.GetSenderDeviceId(), SenderDeviceNumber: e.GetSenderDeviceNumber(),
			Type: nameOr(envelopeTypeNames, e.GetType()), Content: e.GetContent(), CreatedAt: e.GetCreateTime().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) ackE2EEnvelopes(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b struct {
		EnvelopeIDs []string `json:"envelope_ids"`
	}
	if !decodeLimit(w, r, &b, maxE2EIDsBody) {
		return
	}
	if len(b.EnvelopeIDs) == 0 || !validUUIDs(b.EnvelopeIDs, maxE2EIDs) {
		badRequest(w, r)
		return
	}
	if _, err := h.d.E2E.AckEnvelopes(ctx, &chatv1.AckEnvelopesRequest{EnvelopeIds: b.EnvelopeIDs}); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type archiveWriteBody struct {
	HistoryKeyVersion uint32 `json:"history_key_version"`
	Entries           []struct {
		MessageID string `json:"message_id"`
		Content   []byte `json:"content"`
	} `json:"entries"`
}

func (h chatHandlers) putE2EArchive(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b archiveWriteBody
	if !decodeLimit(w, r, &b, maxE2EArchiveBody) {
		return
	}
	if len(b.Entries) == 0 || len(b.Entries) > 50 {
		badRequest(w, r)
		return
	}
	req := &chatv1.PutArchiveEntriesRequest{HistoryKeyVersion: b.HistoryKeyVersion}
	for _, e := range b.Entries {
		if !validUUID(e.MessageID) {
			badRequest(w, r)
			return
		}
		req.Entries = append(req.Entries, &chatv1.NewArchiveEntry{MessageId: e.MessageID, Content: e.Content})
	}
	resp, err := h.d.E2E.PutArchiveEntries(ctx, req)
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AcceptedMessageIDs []string `json:"accepted_message_ids"`
	}{append([]string{}, resp.GetAcceptedMessageIds()...)})
}

type archiveEntryJSON struct {
	MessageID      string    `json:"message_id"`
	ConversationID string    `json:"conversation_id"`
	KeyVersion     uint32    `json:"key_version"`
	Content        []byte    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

func (h chatHandlers) listE2EArchive(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after_message_id")
	if after != "" && !validUUID(after) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.E2E.ListArchiveEntries(ctx, &chatv1.ListArchiveEntriesRequest{
		ConversationId: cid, PageSize: size, PageToken: token, AfterMessageId: after,
	})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Entries       []archiveEntryJSON `json:"entries"`
		NextPageToken string             `json:"next_page_token,omitempty"`
	}{Entries: make([]archiveEntryJSON, 0, len(resp.GetEntries())), NextPageToken: resp.GetNextPageToken()}
	for _, e := range resp.GetEntries() {
		out.Entries = append(out.Entries, archiveEntryJSON{
			MessageID: e.GetMessageId(), ConversationID: e.GetConversationId(), KeyVersion: e.GetKeyVersion(),
			Content: e.GetContent(), CreatedAt: e.GetCreateTime().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteE2EArchive deletes the caller's archive of one conversation (path
// with conversation_id) or all of it.
func (h chatHandlers) deleteE2EArchive(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	cid := ""
	if r.PathValue("conversation_id") != "" {
		var ok bool
		if cid, ok = pathUUID(w, r, "conversation_id"); !ok {
			return
		}
	}
	if _, err := h.d.E2E.DeleteArchive(ctx, &chatv1.DeleteArchiveRequest{ConversationId: cid}); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type historyKeyVersionJSON struct {
	HistoryKeyVersion uint32 `json:"history_key_version"`
}

func (h chatHandlers) claimE2EHistoryKey(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.E2E.ClaimHistoryKey(ctx, &chatv1.ClaimHistoryKeyRequest{})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, historyKeyVersionJSON{resp.GetHistoryKeyVersion()})
}

type rotateBody struct {
	HistoryKeyVersion uint32 `json:"history_key_version"`
	Shares            []struct {
		DeviceID string `json:"device_id"`
		Bundle   []byte `json:"bundle"`
	} `json:"shares"`
}

func (h chatHandlers) rotateE2EHistoryKey(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b rotateBody
	if !decodeLimit(w, r, &b, maxE2EShareBody) {
		return
	}
	if len(b.Shares) > 5 {
		badRequest(w, r)
		return
	}
	req := &chatv1.RotateHistoryKeyRequest{HistoryKeyVersion: b.HistoryKeyVersion}
	for _, s := range b.Shares {
		if !validUUID(s.DeviceID) {
			badRequest(w, r)
			return
		}
		req.Shares = append(req.Shares, &chatv1.HistoryKeyShareInput{DeviceId: s.DeviceID, Bundle: s.Bundle})
	}
	if _, err := h.d.E2E.RotateHistoryKey(ctx, req); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type shareJSON struct {
	ID                string    `json:"id"`
	SenderDeviceID    string    `json:"sender_device_id"`
	HistoryKeyVersion uint32    `json:"history_key_version"`
	Bundle            []byte    `json:"bundle"`
	CreatedAt         time.Time `json:"created_at"`
}

func (h chatHandlers) listE2EHistoryKeyShares(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.E2E.ListHistoryKeyShares(ctx, &chatv1.ListHistoryKeySharesRequest{})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		Shares []shareJSON `json:"shares"`
	}{Shares: make([]shareJSON, 0, len(resp.GetShares()))}
	for _, s := range resp.GetShares() {
		out.Shares = append(out.Shares, shareJSON{
			ID: s.GetId(), SenderDeviceID: s.GetSenderDeviceId(), HistoryKeyVersion: s.GetHistoryKeyVersion(),
			Bundle: s.GetBundle(), CreatedAt: s.GetCreateTime().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) ackE2EHistoryKeyShares(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b struct {
		ShareIDs []string `json:"share_ids"`
	}
	if !decodeLimit(w, r, &b, maxE2EIDsBody) {
		return
	}
	if len(b.ShareIDs) == 0 || !validUUIDs(b.ShareIDs, maxE2EIDs) {
		badRequest(w, r)
		return
	}
	if _, err := h.d.E2E.AckHistoryKeyShares(ctx, &chatv1.AckHistoryKeySharesRequest{ShareIds: b.ShareIDs}); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) createE2ELinkRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.E2E.CreateLinkRequest(ctx, &chatv1.CreateLinkRequestRequest{})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		LinkRequestID string    `json:"link_request_id"`
		ExpiresAt     time.Time `json:"expires_at"`
	}{resp.GetLinkRequestId(), resp.GetExpireTime().AsTime()})
}

func (h chatHandlers) getE2ELinkRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	lid, ok := pathUUID(w, r, "link_request_id")
	if !ok {
		return
	}
	resp, err := h.d.E2E.GetLinkRequest(ctx, &chatv1.GetLinkRequestRequest{LinkRequestId: lid})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	out := struct {
		LinkRequestID      string    `json:"link_request_id"`
		ExpiresAt          time.Time `json:"expires_at"`
		Answered           bool      `json:"answered"`
		Answer             []byte    `json:"answer,omitempty"`
		HistoryKeyVersion  uint32    `json:"history_key_version,omitempty"`
		AnsweredByDeviceID string    `json:"answered_by_device_id,omitempty"`
	}{
		LinkRequestID: resp.GetLinkRequestId(), ExpiresAt: resp.GetExpireTime().AsTime(), Answered: resp.GetAnswered(),
		Answer: resp.GetAnswer(), HistoryKeyVersion: resp.GetHistoryKeyVersion(), AnsweredByDeviceID: resp.GetAnsweredByDeviceId(),
	}
	writeJSON(w, http.StatusOK, out)
}

func (h chatHandlers) answerE2ELinkRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	lid, ok := pathUUID(w, r, "link_request_id")
	if !ok {
		return
	}
	var b struct {
		HistoryKeyVersion uint32 `json:"history_key_version"`
		Answer            []byte `json:"answer"`
	}
	if !decodeLimit(w, r, &b, maxE2EShareBody) {
		return
	}
	if _, err := h.d.E2E.AnswerLinkRequest(ctx, &chatv1.AnswerLinkRequestRequest{
		LinkRequestId: lid, HistoryKeyVersion: b.HistoryKeyVersion, Answer: b.Answer,
	}); err != nil {
		h.failE2E(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h chatHandlers) completeE2ELinkRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	lid, ok := pathUUID(w, r, "link_request_id")
	if !ok {
		return
	}
	resp, err := h.d.E2E.CompleteLinkRequest(ctx, &chatv1.CompleteLinkRequestRequest{LinkRequestId: lid})
	if err != nil {
		h.failE2E(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e2eDeviceFrom(resp.GetDevice()))
}

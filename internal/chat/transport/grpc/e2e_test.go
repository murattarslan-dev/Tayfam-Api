package grpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeKeys records what the transport passed and returns err.
type fakeKeys struct {
	caller   keys.Caller
	register keys.RegisterInput
	upload   keys.UploadInput
	user     id.ID
	ids      []id.ID
	content  []byte
	version  uint32
	err      error
}

func (f *fakeKeys) Register(_ context.Context, c keys.Caller, in keys.RegisterInput) (keys.Device, keys.Counts, error) {
	f.caller, f.register = c, in
	return keys.Device{ID: id.New(), Number: 3, RegistrationID: 7, IdentityKey: in.IdentityKey}, keys.Counts{EC: 4, Kyber: 20}, f.err
}

func (f *fakeKeys) MyDevice(_ context.Context, c keys.Caller) (keys.State, error) {
	f.caller = c
	return keys.State{Device: keys.Device{ID: id.New(), Paired: true}, Counts: keys.Counts{EC: 50, Kyber: 50}, HistoryKeyVersion: 2, RotationNeeded: true}, f.err
}

func (f *fakeKeys) Unregister(_ context.Context, c keys.Caller) error { f.caller = c; return f.err }

func (f *fakeKeys) Upload(_ context.Context, c keys.Caller, in keys.UploadInput) (keys.Counts, error) {
	f.caller, f.upload = c, in
	return keys.Counts{EC: 100, Kyber: 100}, f.err
}

func (f *fakeKeys) Devices(_ context.Context, caller, user id.ID) ([]keys.PublicDevice, error) {
	f.caller.User, f.user = caller, user
	return []keys.PublicDevice{{ID: id.New(), Number: 1, RegistrationID: 9, IdentityKey: []byte{5}}}, f.err
}

func (f *fakeKeys) KeyEvents(_ context.Context, caller, user id.ID, _ int32, _ string) ([]keys.KeyEvent, string, error) {
	f.caller.User, f.user = caller, user
	return []keys.KeyEvent{{ID: id.New(), DeviceID: id.New(), Number: 2, Type: keys.EventDeviceRemoved}}, "", f.err
}

func (f *fakeKeys) Bundles(_ context.Context, c keys.Caller, user id.ID, only []id.ID) ([]keys.Bundle, error) {
	f.caller, f.user, f.ids = c, user, only
	return []keys.Bundle{
		{DeviceID: id.New(), OneTime: &keys.PreKey{ID: 4, PublicKey: []byte{5}}, Kyber: keys.SignedPreKey{ID: 1}},
		{DeviceID: id.New(), Kyber: keys.SignedPreKey{ID: 2}, KyberLastResort: true},
	}, f.err
}

func (f *fakeKeys) Envelopes(_ context.Context, c keys.Caller, _ int32, _ string) ([]keys.Envelope, string, error) {
	f.caller = c
	return []keys.Envelope{{ID: id.New(), Type: keys.EnvelopePreKey, Content: []byte("zarf"), SenderNumber: 2}}, "x", f.err
}

func (f *fakeKeys) Ack(_ context.Context, c keys.Caller, ids []id.ID) error {
	f.caller, f.ids = c, ids
	return f.err
}

func (f *fakeKeys) PutArchive(_ context.Context, c keys.Caller, v uint32, es []keys.ArchiveInput) ([]id.ID, error) {
	f.caller, f.version = c, v
	if len(es) > 0 {
		f.content = es[0].Content
		return []id.ID{es[0].MessageID}, f.err
	}
	return nil, f.err
}

func (f *fakeKeys) Archive(_ context.Context, c keys.Caller, _ id.ID, _ keys.ArchiveListInput) ([]keys.ArchiveEntry, string, error) {
	f.caller = c
	return nil, "", f.err
}

func (f *fakeKeys) DeleteArchive(_ context.Context, c keys.Caller, conv id.ID) error {
	f.caller, f.ids = c, []id.ID{conv}
	return f.err
}

func (f *fakeKeys) ClaimHistoryKey(_ context.Context, c keys.Caller) (int, error) {
	f.caller = c
	return 1, f.err
}

func (f *fakeKeys) CreateLink(_ context.Context, c keys.Caller) (keys.LinkRequest, error) {
	f.caller = c
	return keys.LinkRequest{ID: id.New(), ExpiresAt: time.Now()}, f.err
}

func (f *fakeKeys) AnswerLink(_ context.Context, c keys.Caller, r id.ID, v uint32, answer []byte) error {
	f.caller, f.ids, f.version, f.content = c, []id.ID{r}, v, answer
	return f.err
}

func (f *fakeKeys) GetLink(_ context.Context, c keys.Caller, r id.ID) (keys.LinkRequest, error) {
	f.caller = c
	return keys.LinkRequest{ID: r, Answered: true, Answer: []byte("cevap"), Version: 1}, f.err
}

func (f *fakeKeys) CompleteLink(_ context.Context, c keys.Caller, _ id.ID) (keys.Device, error) {
	f.caller = c
	return keys.Device{ID: id.New(), Paired: true}, f.err
}

func (f *fakeKeys) Rotate(_ context.Context, c keys.Caller, v uint32, shares []keys.ShareInput) error {
	f.caller, f.version = c, v
	for _, s := range shares {
		f.ids = append(f.ids, s.Device)
	}
	return f.err
}

func (f *fakeKeys) Shares(_ context.Context, c keys.Caller) ([]keys.Share, error) {
	f.caller = c
	return []keys.Share{{ID: id.New(), Version: 2, Bundle: []byte{1}}}, f.err
}

func (f *fakeKeys) AckShares(_ context.Context, c keys.Caller, ids []id.ID) error {
	f.caller, f.ids = c, ids
	return f.err
}

// e2eConn serves E2EService (and DirectMessageService) behind auth.Guard
// and returns a context carrying a token of user in session.
func e2eConn(t *testing.T, app Keys, direct DirectMessages, logs *bytes.Buffer) (chatv1.E2EServiceClient, chatv1.DirectMessageServiceClient, context.Context, id.ID, id.ID) {
	t.Helper()
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(auth.NewGuard(uv).ServerOptions()...)
	log := slog.New(slog.NewJSONHandler(logs, nil))
	chatv1.RegisterE2EServiceServer(srv, NewE2EServer(app, log))
	chatv1.RegisterDirectMessageServiceServer(srv, NewDirectMessageServer(direct, log))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: "tayfam-api", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	user, session := id.New(), id.New()
	tok, _, err := signer.Issue(user.String(), session.String())
	if err != nil {
		t.Fatal(err)
	}
	return chatv1.NewE2EServiceClient(conn), chatv1.NewDirectMessageServiceClient(conn), auth.WithToken(context.Background(), tok), user, session
}

func signed(b byte) *chatv1.SignedPreKey {
	return &chatv1.SignedPreKey{KeyId: uint32(b), PublicKey: []byte{5, b}, Signature: []byte{b}}
}

// The device is the token's session: requests never name it.
func TestE2EServiceUsesTokenSession(t *testing.T) {
	app := &fakeKeys{}
	client, _, ctx, user, session := e2eConn(t, app, &fakeDirect{}, &bytes.Buffer{})
	if _, err := client.GetMyDevice(context.Background(), &chatv1.GetMyDeviceRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token yok: %v", err)
	}
	reg, err := client.RegisterDevice(ctx, &chatv1.RegisterDeviceRequest{
		RegistrationId: 7, IdentityKey: []byte{5, 1}, SignedPrekey: signed(1), KyberLastResortPrekey: signed(2),
		OneTimePrekeys: []*chatv1.PreKey{{KeyId: 3, PublicKey: []byte{5}}}, KyberPrekeys: []*chatv1.SignedPreKey{signed(4)},
	})
	if err != nil || app.caller.User != user || app.caller.Session != session || app.caller.IssuedAt.IsZero() ||
		app.register.RegistrationID != 7 || app.register.KyberLastResort.ID != 2 || len(app.register.OneTime) != 1 || len(app.register.Kyber) != 1 ||
		reg.GetDevice().GetDeviceNumber() != 3 || !reg.GetPrekeyCounts().GetLow() {
		t.Fatalf("kayıt = %v, %v (%+v)", reg, err, app.caller)
	}
	for name, r := range map[string]*chatv1.RegisterDeviceRequest{
		"signed_prekey":            {KyberLastResortPrekey: signed(1)},
		"kyber_last_resort_prekey": {SignedPrekey: signed(1)},
	} {
		_, err := client.RegisterDevice(ctx, r)
		if _, _, meta := reasonOf(err); meta["field"] != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	my, err := client.GetMyDevice(ctx, &chatv1.GetMyDeviceRequest{})
	if err != nil || my.GetHistoryKeyVersion() != 2 || !my.GetRotationNeeded() || my.GetPrekeyCounts().GetLow() || !my.GetDevice().GetPaired() {
		t.Fatalf("cihazım = %v, %v", my, err)
	}
	other := id.New()
	b, err := client.GetPreKeyBundles(ctx, &chatv1.GetPreKeyBundlesRequest{UserId: other.String(), DeviceIds: []string{other.String()}})
	if err != nil || app.user != other || app.caller.Session != session || len(app.ids) != 1 || len(b.GetBundles()) != 2 ||
		b.GetBundles()[0].GetOneTimePrekey().GetKeyId() != 4 || b.GetBundles()[1].GetOneTimePrekey() != nil || !b.GetBundles()[1].GetKyberLastResort() {
		t.Fatalf("paketler = %v, %v", b, err)
	}
	env, err := client.ListEnvelopes(ctx, &chatv1.ListEnvelopesRequest{})
	if err != nil || env.GetEnvelopes()[0].GetType() != chatv1.EnvelopeType_ENVELOPE_TYPE_PREKEY || string(env.GetEnvelopes()[0].GetContent()) != "zarf" {
		t.Fatalf("zarflar = %v, %v", env, err)
	}
	if _, err := client.AckEnvelopes(ctx, &chatv1.AckEnvelopesRequest{EnvelopeIds: []string{"x"}}); func() string { _, _, m := reasonOf(err); return m["field"] }() != "envelope_ids" {
		t.Fatalf("onay: %v", err)
	}
	ev, err := client.ListKeyEvents(ctx, &chatv1.ListKeyEventsRequest{UserId: other.String()})
	if err != nil || ev.GetEvents()[0].GetType() != chatv1.KeyEventType_KEY_EVENT_TYPE_DEVICE_REMOVED {
		t.Fatalf("olaylar = %v, %v", ev, err)
	}
	link, err := client.GetLinkRequest(ctx, &chatv1.GetLinkRequestRequest{LinkRequestId: other.String()})
	if err != nil || !link.GetAnswered() || string(link.GetAnswer()) != "cevap" || link.GetAnsweredByDeviceId() != "" {
		t.Fatalf("eşleştirme = %v, %v", link, err)
	}
	if _, err := client.RotateHistoryKey(ctx, &chatv1.RotateHistoryKeyRequest{HistoryKeyVersion: 3, Shares: []*chatv1.HistoryKeyShareInput{{DeviceId: other.String(), Bundle: []byte{1}}}}); err != nil || app.version != 3 {
		t.Fatalf("yenileme: %v", err)
	}
	if _, err := client.DeleteArchive(ctx, &chatv1.DeleteArchiveRequest{}); err != nil || app.ids[0] != (id.ID{}) {
		t.Fatalf("arşiv silme: %v", err)
	}

	// Sending end to end through DirectMessageService carries the session
	// and the envelopes as they are.
	fd := &fakeDirect{}
	_, direct, ctx, _, session := e2eConn(t, app, fd, &bytes.Buffer{})
	dev := id.New()
	_, err = direct.SendDirectMessage(ctx, &chatv1.SendDirectMessageRequest{
		RecipientUserId: other.String(), ClientRequestId: id.New().String(),
		Envelopes: []*chatv1.OutgoingEnvelope{{DeviceId: dev.String(), RegistrationId: 9, Type: chatv1.EnvelopeType_ENVELOPE_TYPE_WHISPER, Content: []byte("z")}},
	})
	if err != nil || fd.send.Session != session || len(fd.send.Envelopes) != 1 || fd.send.Envelopes[0].Device != dev ||
		fd.send.Envelopes[0].Type != keys.EnvelopeWhisper || fd.send.Envelopes[0].RegistrationID != 9 || string(fd.send.Envelopes[0].Content) != "z" {
		t.Fatalf("e2e gönderim = %+v, %v", fd.send, err)
	}
	_, err = direct.SendDirectMessage(ctx, &chatv1.SendDirectMessageRequest{
		RecipientUserId: other.String(), ClientRequestId: id.New().String(), Envelopes: []*chatv1.OutgoingEnvelope{{DeviceId: "x"}},
	})
	if _, _, meta := reasonOf(err); meta["field"] != "envelopes" {
		t.Fatalf("geçersiz zarf: %v", err)
	}
}

func TestE2EErrors(t *testing.T) {
	logs := &bytes.Buffer{}
	app := &fakeKeys{}
	client, _, ctx, _, _ := e2eConn(t, app, &fakeDirect{}, logs)
	for _, c := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{apperr.ErrE2EDeviceNotRegistered, codes.FailedPrecondition, "ERROR_REASON_E2E_DEVICE_NOT_REGISTERED"},
		{apperr.ErrE2EDeviceAlreadyRegistered, codes.AlreadyExists, "ERROR_REASON_E2E_DEVICE_ALREADY_REGISTERED"},
		{apperr.ErrE2EDeviceLimit, codes.ResourceExhausted, "ERROR_REASON_E2E_DEVICE_LIMIT"},
		{apperr.ErrE2ERecipientUnavailable, codes.FailedPrecondition, "ERROR_REASON_E2E_RECIPIENT_UNAVAILABLE"},
		{apperr.ErrE2ERequired, codes.FailedPrecondition, "ERROR_REASON_E2E_REQUIRED"},
		{apperr.ErrE2ENotPaired, codes.FailedPrecondition, "ERROR_REASON_E2E_NOT_PAIRED"},
		{apperr.ErrE2EAlreadyPaired, codes.FailedPrecondition, "ERROR_REASON_E2E_ALREADY_PAIRED"},
		{apperr.ErrE2EPairedDeviceExists, codes.FailedPrecondition, "ERROR_REASON_E2E_PAIRED_DEVICE_EXISTS"},
		{apperr.ErrE2EHistoryKeyStale, codes.FailedPrecondition, "ERROR_REASON_E2E_HISTORY_KEY_STALE"},
		{apperr.ErrE2ELinkRequestNotFound, codes.NotFound, "ERROR_REASON_E2E_LINK_REQUEST_NOT_FOUND"},
		{apperr.ErrDirectMessageNotAllowed, codes.PermissionDenied, "ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED"},
		{apperr.ErrRateLimited, codes.ResourceExhausted, "ERROR_REASON_RATE_LIMITED"},
	} {
		app.err = c.err
		_, err := client.ClaimHistoryKey(ctx, &chatv1.ClaimHistoryKeyRequest{})
		if code, reason, _ := reasonOf(err); code != c.code || reason != c.reason {
			t.Errorf("%v: %v", c.err, err)
		}
	}
	missing, stale := id.New().String(), id.New().String()
	app.err = &apperr.DevicesChangedError{Missing: []string{missing}, Stale: []string{stale}}
	_, err := client.RotateHistoryKey(ctx, &chatv1.RotateHistoryKeyRequest{HistoryKeyVersion: 2})
	if code, reason, meta := reasonOf(err); code != codes.FailedPrecondition || reason != "ERROR_REASON_E2E_DEVICES_CHANGED" ||
		meta["missing_device_ids"] != missing || meta["stale_device_ids"] != stale {
		t.Fatalf("cihaz listesi: %v %v", err, meta)
	}
	app.err = apperr.ErrSessionEnded
	if _, err := client.RegisterDevice(ctx, &chatv1.RegisterDeviceRequest{SignedPrekey: signed(1), KyberLastResortPrekey: signed(2)}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("biten oturum: %v", err)
	}
	// An unknown error is logged without the request's content and reaches
	// the caller without detail.
	app.err = errors.New("beklenmeyen")
	_, err = client.AnswerLinkRequest(ctx, &chatv1.AnswerLinkRequestRequest{LinkRequestId: id.New().String(), HistoryKeyVersion: 1, Answer: []byte("GIZLI-CEVAP")})
	if status.Code(err) != codes.Internal || string(app.content) != "GIZLI-CEVAP" {
		t.Fatalf("iç hata: %v", err)
	}
	_, _ = client.PutArchiveEntries(ctx, &chatv1.PutArchiveEntriesRequest{HistoryKeyVersion: 1, Entries: []*chatv1.NewArchiveEntry{{MessageId: id.New().String(), Content: []byte("GIZLI-ARSIV")}}})
	if strings.Contains(logs.String(), "GIZLI") || !strings.Contains(logs.String(), "beklenmeyen") {
		t.Fatalf("log: %s", logs.String())
	}
}

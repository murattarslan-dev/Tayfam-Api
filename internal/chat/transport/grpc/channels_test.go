package grpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeChannels records what the transport passed and returns err.
type fakeChannels struct {
	caller, tayfa, channel id.ID
	send                   channel.SendInput
	update                 channel.ChannelUpdate
	list                   channel.ListInput
	create                 channel.ChannelInput
	users                  []id.ID
	err                    error
}

func (f *fakeChannels) Members(_ context.Context, caller, tayfa, ch id.ID, _ int32, token string) (channel.MemberPage, error) {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	return channel.MemberPage{Users: []id.ID{caller}, Next: token + "+"}, f.err
}

func (f *fakeChannels) AddMembers(_ context.Context, caller, tayfa, ch id.ID, users []id.ID) (channel.Channel, error) {
	f.caller, f.tayfa, f.channel, f.users = caller, tayfa, ch, users
	return channel.Channel{ID: ch, TayfaID: tayfa, Name: "seçili", Visibility: channel.VisibilitySelected, MemberCount: 3}, f.err
}

func (f *fakeChannels) RemoveMember(_ context.Context, caller, tayfa, ch, user id.ID) error {
	f.caller, f.tayfa, f.channel, f.users = caller, tayfa, ch, []id.ID{user}
	return f.err
}

func (f *fakeChannels) List(_ context.Context, caller, tayfa id.ID) ([]channel.Channel, error) {
	f.caller, f.tayfa = caller, tayfa
	return []channel.Channel{{ID: id.New(), TayfaID: tayfa, Name: "genel", Visibility: channel.VisibilityStaff, Unread: 100}}, f.err
}

func (f *fakeChannels) Get(_ context.Context, caller, tayfa, ch id.ID) (channel.Channel, error) {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	return channel.Channel{ID: ch}, f.err
}

func (f *fakeChannels) Create(_ context.Context, caller, tayfa id.ID, in channel.ChannelInput) (channel.Channel, error) {
	f.caller, f.tayfa, f.create = caller, tayfa, in
	return channel.Channel{ID: id.New(), Name: in.Name, Visibility: in.Visibility}, f.err
}

func (f *fakeChannels) Update(_ context.Context, caller, tayfa, ch id.ID, in channel.ChannelUpdate) (channel.Channel, error) {
	f.caller, f.tayfa, f.channel, f.update = caller, tayfa, ch, in
	return channel.Channel{ID: ch}, f.err
}

func (f *fakeChannels) SetArchived(_ context.Context, caller, tayfa, ch id.ID, archived bool) (channel.Channel, error) {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	c := channel.Channel{ID: ch}
	if archived {
		c.ArchivedAt = time.Now()
	}
	return c, f.err
}

func (f *fakeChannels) Send(_ context.Context, caller, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error) {
	f.caller, f.tayfa, f.channel, f.send = caller, tayfa, ch, in
	return channel.Message{ID: id.New(), ChannelID: ch, AuthorID: caller, Body: in.Body}, f.err
}

func (f *fakeChannels) Messages(_ context.Context, caller, tayfa, ch id.ID, in channel.ListInput) ([]channel.Message, string, error) {
	f.caller, f.tayfa, f.channel, f.list = caller, tayfa, ch, in
	return []channel.Message{{ID: id.New(), ChannelID: ch, Deleted: true, Restorable: true}}, "", f.err
}

func (f *fakeChannels) Delete(_ context.Context, caller, tayfa, ch, _ id.ID) error {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	return f.err
}

func (f *fakeChannels) Restore(_ context.Context, caller, tayfa, ch, m id.ID) (channel.Message, error) {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	return channel.Message{ID: m, ChannelID: ch}, f.err
}

func (f *fakeChannels) MarkRead(_ context.Context, caller, tayfa, ch, _ id.ID) error {
	f.caller, f.tayfa, f.channel = caller, tayfa, ch
	return f.err
}

func channelConn(t *testing.T, app Channels, logs *bytes.Buffer) (chatv1.ChannelServiceClient, context.Context, id.ID) {
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
	chatv1.RegisterChannelServiceServer(srv, NewChannelServer(app, slog.New(slog.NewJSONHandler(logs, nil))))
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
	user := id.New()
	tok, _, err := signer.Issue(user.String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return chatv1.NewChannelServiceClient(conn), auth.WithToken(context.Background(), tok), user
}

func reasonOf(err error) (codes.Code, string, map[string]string) {
	st := status.Convert(err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return st.Code(), info.GetReason(), info.GetMetadata()
		}
	}
	return st.Code(), "", nil
}

func TestChannelServiceConverts(t *testing.T) {
	app := &fakeChannels{}
	client, ctx, user := channelConn(t, app, &bytes.Buffer{})
	tf, ch, req := id.New(), id.New(), id.New()

	if _, err := client.ListChannels(context.Background(), &chatv1.ListChannelsRequest{TayfaId: tf.String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token yok: %v", err)
	}
	list, err := client.ListChannels(ctx, &chatv1.ListChannelsRequest{TayfaId: tf.String()})
	if err != nil || app.caller != user || app.tayfa != tf ||
		list.GetChannels()[0].GetVisibility() != chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF || list.GetChannels()[0].GetUnreadCount() != 100 {
		t.Fatalf("liste = %v, %v", list, err)
	}

	sent, err := client.SendMessage(ctx, &chatv1.SendMessageRequest{TayfaId: tf.String(), ChannelId: ch.String(), Body: "merhaba", ClientRequestId: req.String()})
	if err != nil || app.send.Body != "merhaba" || app.send.ClientRequestID != req || app.channel != ch || sent.GetMessage().GetAuthorUserId() != user.String() {
		t.Fatalf("gönderim = %v, %v", sent, err)
	}
	for name, r := range map[string]*chatv1.SendMessageRequest{
		"tayfa_id":          {TayfaId: "x", ChannelId: ch.String(), ClientRequestId: req.String()},
		"channel_id":        {TayfaId: tf.String(), ChannelId: "", ClientRequestId: req.String()},
		"client_request_id": {TayfaId: tf.String(), ChannelId: ch.String()},
	} {
		_, err := client.SendMessage(ctx, r)
		if code, reason, meta := reasonOf(err); code != codes.InvalidArgument || reason != "ERROR_REASON_VALIDATION" || meta["field"] != name {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Create with UNSPECIFIED visibility means members.
	if _, err := client.CreateChannel(ctx, &chatv1.CreateChannelRequest{TayfaId: tf.String(), Name: "genel"}); err != nil || app.create.Visibility != channel.VisibilityMembers {
		t.Fatalf("oluşturma = %v %+v", err, app.create)
	}
	// Update follows the mask only.
	_, err = client.UpdateChannel(ctx, &chatv1.UpdateChannelRequest{
		TayfaId: tf.String(), ChannelId: ch.String(), Name: "yok sayılır", SlowModeSeconds: 30,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"slow_mode_seconds"}},
	})
	if err != nil || app.update.Name != nil || app.update.SlowModeSeconds == nil || *app.update.SlowModeSeconds != 30 {
		t.Fatalf("güncelleme = %v %+v", err, app.update)
	}
	for _, mask := range [][]string{nil, {"tayfa_id"}} {
		_, err := client.UpdateChannel(ctx, &chatv1.UpdateChannelRequest{TayfaId: tf.String(), ChannelId: ch.String(), UpdateMask: &fieldmaskpb.FieldMask{Paths: mask}})
		if _, _, meta := reasonOf(err); meta["field"] != "update_mask" {
			t.Errorf("maske %v: %v", mask, err)
		}
	}

	after := id.New()
	msgs, err := client.ListMessages(ctx, &chatv1.ListMessagesRequest{TayfaId: tf.String(), ChannelId: ch.String(), AfterMessageId: after.String(), PageSize: 7})
	if err != nil || app.list.After != after || app.list.PageSize != 7 || !msgs.GetMessages()[0].GetDeleted() || !msgs.GetMessages()[0].GetRestorable() {
		t.Fatalf("mesajlar = %v %v", msgs, err)
	}
	arch, err := client.ArchiveChannel(ctx, &chatv1.ArchiveChannelRequest{TayfaId: tf.String(), ChannelId: ch.String()})
	if err != nil || arch.GetChannel().GetArchiveTime() == nil {
		t.Fatalf("arşiv = %v %v", arch, err)
	}
}

func TestChannelErrors(t *testing.T) {
	logs := &bytes.Buffer{}
	app := &fakeChannels{}
	client, ctx, _ := channelConn(t, app, logs)
	r := &chatv1.SendMessageRequest{TayfaId: id.New().String(), ChannelId: id.New().String(), Body: "GizliGövde", ClientRequestId: id.New().String()}
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{apperr.ErrTayfaNotFound, codes.NotFound, "ERROR_REASON_TAYFA_NOT_FOUND"},
		{apperr.ErrChannelNotFound, codes.NotFound, "ERROR_REASON_CHANNEL_NOT_FOUND"},
		{apperr.ErrMessageNotFound, codes.NotFound, "ERROR_REASON_MESSAGE_NOT_FOUND"},
		{apperr.ErrPermissionDenied, codes.PermissionDenied, "ERROR_REASON_PERMISSION_DENIED"},
		{apperr.ErrMuted, codes.PermissionDenied, "ERROR_REASON_MUTED"},
		{apperr.ErrChannelArchived, codes.FailedPrecondition, "ERROR_REASON_CHANNEL_ARCHIVED"},
		{apperr.ErrChannelNameTaken, codes.AlreadyExists, "ERROR_REASON_CHANNEL_NAME_TAKEN"},
		{apperr.ErrTayfaReadOnly, codes.FailedPrecondition, "ERROR_REASON_TAYFA_READ_ONLY"},
		{apperr.ErrRoleHierarchy, codes.PermissionDenied, "ERROR_REASON_ROLE_HIERARCHY"},
		{apperr.ErrChannelsUnavailable, codes.Unavailable, ""},
		{apperr.ErrUnavailable, codes.Unavailable, ""},
	} {
		app.err = tc.err
		_, err := client.SendMessage(ctx, r)
		if code, reason, _ := reasonOf(err); code != tc.code || reason != tc.reason {
			t.Errorf("%v: %v", tc.err, err)
		}
	}
	app.err = &apperr.SlowModeError{RetryAfter: 1500 * time.Millisecond}
	_, err := client.SendMessage(ctx, r)
	if code, reason, meta := reasonOf(err); code != codes.ResourceExhausted || reason != "ERROR_REASON_SLOW_MODE" || meta["retry_after_seconds"] != "2" {
		t.Fatalf("yavaş mod: %v %v", err, meta)
	}
	// An internal error is logged without the request, and the caller sees
	// no detail.
	app.err = errInternalForTest
	_, err = client.SendMessage(ctx, r)
	if status.Code(err) != codes.Internal || strings.Contains(status.Convert(err).Message(), "test") {
		t.Fatalf("iç hata: %v", err)
	}
	if strings.Contains(logs.String(), "GizliGövde") {
		t.Fatal("log mesaj metni içeriyor")
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errInternalForTest = testErr("test iç hatası")

// TestChannelAudienceConverts (issue #183): the new kinds, their fields
// and the member methods.
func TestChannelAudienceConverts(t *testing.T) {
	app := &fakeChannels{}
	client, ctx, user := channelConn(t, app, &bytes.Buffer{})
	tf, ch, ev, other := id.New(), id.New(), id.New(), id.New()

	if _, err := client.CreateChannel(ctx, &chatv1.CreateChannelRequest{
		TayfaId: tf.String(), Name: "Etkinlik", Visibility: chatv1.ChannelVisibility_CHANNEL_VISIBILITY_EVENT, EventId: ev.String(),
	}); err != nil || app.create.Visibility != channel.VisibilityEvent || app.create.Event != ev {
		t.Fatalf("etkinlik kanalı: %v %+v", err, app.create)
	}
	if _, err := client.CreateChannel(ctx, &chatv1.CreateChannelRequest{
		TayfaId: tf.String(), Name: "Seçili", Visibility: chatv1.ChannelVisibility_CHANNEL_VISIBILITY_SELECTED, MemberUserIds: []string{other.String()},
	}); err != nil || app.create.Visibility != channel.VisibilitySelected || len(app.create.Members) != 1 || app.create.Members[0] != other {
		t.Fatalf("seçili kanal: %v %+v", err, app.create)
	}
	for name, req := range map[string]*chatv1.CreateChannelRequest{
		"bozuk etkinlik": {TayfaId: tf.String(), Name: "x", EventId: "x"},
		"bozuk üye":      {TayfaId: tf.String(), Name: "x", MemberUserIds: []string{"x"}},
		"101 üye":        {TayfaId: tf.String(), Name: "x", MemberUserIds: make([]string, 101)},
	} {
		if _, err := client.CreateChannel(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	page, err := client.ListChannelMembers(ctx, &chatv1.ListChannelMembersRequest{TayfaId: tf.String(), ChannelId: ch.String(), PageToken: "a"})
	if err != nil || len(page.GetUserIds()) != 1 || page.GetUserIds()[0] != user.String() || page.GetNextPageToken() != "a+" || app.channel != ch {
		t.Fatalf("üyeler: %v %v", page, err)
	}
	added, err := client.AddChannelMembers(ctx, &chatv1.AddChannelMembersRequest{TayfaId: tf.String(), ChannelId: ch.String(), UserIds: []string{other.String()}})
	if err != nil || len(app.users) != 1 || app.users[0] != other || added.GetChannel().GetMemberCount() != 3 ||
		added.GetChannel().GetVisibility() != chatv1.ChannelVisibility_CHANNEL_VISIBILITY_SELECTED {
		t.Fatalf("ekleme: %v %v", added, err)
	}
	if _, err := client.RemoveChannelMember(ctx, &chatv1.RemoveChannelMemberRequest{TayfaId: tf.String(), ChannelId: ch.String(), UserId: other.String()}); err != nil || app.users[0] != other {
		t.Fatalf("çıkarma: %v", err)
	}
	if _, err := client.RemoveChannelMember(ctx, &chatv1.RemoveChannelMemberRequest{TayfaId: tf.String(), ChannelId: ch.String(), UserId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bozuk kullanıcı: %v", err)
	}
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{apperr.ErrEventChannelExists, codes.FailedPrecondition, "ERROR_REASON_EVENT_CHANNEL_EXISTS"},
		{apperr.ErrEventNotFound, codes.NotFound, "ERROR_REASON_EVENT_NOT_FOUND"},
		{apperr.ErrNotTayfaMember, codes.InvalidArgument, "ERROR_REASON_NOT_TAYFA_MEMBER"},
		{apperr.ErrEventEnded, codes.FailedPrecondition, "ERROR_REASON_EVENT_ENDED"},
	} {
		app.err = tc.err
		_, err := client.AddChannelMembers(ctx, &chatv1.AddChannelMembersRequest{TayfaId: tf.String(), ChannelId: ch.String(), UserIds: []string{other.String()}})
		if code, reason, _ := reasonOf(err); code != tc.code || reason != tc.reason {
			t.Errorf("%v: %v %s", tc.err, code, reason)
		}
	}
}

func TestChannelToProtoAudience(t *testing.T) {
	ev, at := id.New(), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	p := channelToProto(channel.Channel{Visibility: channel.VisibilityEvent, EventID: ev, PlannedArchiveAt: at, MemberCount: 5})
	if p.GetVisibility() != chatv1.ChannelVisibility_CHANNEL_VISIBILITY_EVENT || p.GetEventId() != ev.String() ||
		!p.GetPlannedArchiveTime().AsTime().Equal(at) || p.GetMemberCount() != 5 {
		t.Fatalf("etkinlik kanalı = %v", p)
	}
	if p := channelToProto(channel.Channel{Visibility: channel.VisibilityMembers, MemberCount: 5}); p.GetMemberCount() != 0 || p.GetEventId() != "" {
		t.Fatalf("üyeler kanalı = %v", p)
	}
}

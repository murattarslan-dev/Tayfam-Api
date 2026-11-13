package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coreevents"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth/satest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const ns = "tayfam-test"

var (
	identitySA = auth.ServiceAccount{Namespace: ns, Name: "tayfam-identity"}
	coreSA     = auth.ServiceAccount{Namespace: ns, Name: "tayfam-core"}
)

type fakeApp struct {
	mu   sync.Mutex
	user []userevents.Event
	core []coreevents.Event
	err  error
}

func (f *fakeApp) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.user) + len(f.core)
}

type fakeUser struct{ *fakeApp }

func (f fakeUser) Apply(_ context.Context, e userevents.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.user = append(f.user, e)
	return f.err
}

type fakeCore struct{ *fakeApp }

func (f fakeCore) Apply(_ context.Context, e coreevents.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.core = append(f.core, e)
	return f.err
}

// eventConn serves both consumer services behind the same Guard policy as
// cmd/chat.
func eventConn(t *testing.T, c *satest.Cluster, app *fakeApp) (chatv1.UserEventServiceClient, chatv1.CoreEventServiceClient, string) {
	t.Helper()
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	uv, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := auth.NewGuard(uv).WithServices(auth.ServicePolicy{
		Verifier: c.Verifier(t, auth.ServiceAudience("chat"), nil),
		Services: []string{UserEventServiceName, CoreEventServiceName},
		Callers: map[string][]auth.ServiceAccount{
			chatv1.UserEventService_ApplyUserEvent_FullMethodName: {identitySA},
			chatv1.CoreEventService_ApplyCoreEvent_FullMethodName: {coreSA},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(g.ServerOptions()...)
	chatv1.RegisterUserEventServiceServer(srv, NewUserEventServer(fakeUser{app}, log))
	chatv1.RegisterCoreEventServiceServer(srv, NewCoreEventServer(fakeCore{app}, log))
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
	userTok, _, err := signer.Issue(id.New().String(), id.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return chatv1.NewUserEventServiceClient(conn), chatv1.NewCoreEventServiceClient(conn), userTok
}

func coreReq() *chatv1.ApplyCoreEventRequest {
	return &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: id.New().String(),
		Event: &chatv1.ApplyCoreEventRequest_TayfaPurged{TayfaPurged: &chatv1.TayfaPurged{}}}
}

func userReq() *chatv1.ApplyUserEventRequest {
	return &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: id.New().String(),
		Event: &chatv1.ApplyUserEventRequest_Deleted{Deleted: &chatv1.UserDeleted{}}}
}

// Each consumer service accepts only its own producer's ServiceAccount.
func TestEventServicesAuthentication(t *testing.T) {
	c := satest.New(t)
	app := &fakeApp{}
	users, cores, userTok := eventConn(t, c, app)
	callUser := func(ctx context.Context) error { _, err := users.ApplyUserEvent(ctx, userReq()); return err }
	callCore := func(ctx context.Context) error { _, err := cores.ApplyCoreEvent(ctx, coreReq()); return err }

	for _, tc := range []struct {
		name  string
		call  func(context.Context) error
		token string
		want  codes.Code
	}{
		{"identity → UserEventService", callUser, c.ServiceToken(t, identitySA, "chat.tayfam.internal"), codes.OK},
		{"core → UserEventService", callUser, c.ServiceToken(t, coreSA, "chat.tayfam.internal"), codes.PermissionDenied},
		{"core → CoreEventService", callCore, c.ServiceToken(t, coreSA, "chat.tayfam.internal"), codes.OK},
		{"identity → CoreEventService", callCore, c.ServiceToken(t, identitySA, "chat.tayfam.internal"), codes.PermissionDenied},
		{"başka namespace'teki core", callCore, c.ServiceToken(t, auth.ServiceAccount{Namespace: "baska", Name: "tayfam-core"}, "chat.tayfam.internal"), codes.PermissionDenied},
		{"core audience'lı token", callCore, c.ServiceToken(t, coreSA, "core.tayfam.internal"), codes.Unauthenticated},
		{"kullanıcı token'ı", callCore, userTok, codes.Unauthenticated},
		{"token yok", callUser, "", codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.token != "" {
				ctx = auth.WithToken(ctx, tc.token)
			}
			before := app.count()
			if got := status.Code(tc.call(ctx)); got != tc.want {
				t.Fatalf("kod = %v, beklenen %v", got, tc.want)
			}
			if tc.want != codes.OK && app.count() != before {
				t.Fatal("reddedilen çağrı uygulamaya ulaştı")
			}
		})
	}
}

func TestApplyCoreEventConversion(t *testing.T) {
	c := satest.New(t)
	app := &fakeApp{}
	_, cores, _ := eventConn(t, c, app)
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, coreSA, "chat.tayfam.internal"))

	tf, actor, u, ev, hiker := id.New(), id.New(), id.New(), id.New(), id.New()
	req := &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(),
		Event: &chatv1.ApplyCoreEventRequest_NotificationRequested{NotificationRequested: &chatv1.NotificationRequested{
			Type: chatv1.NotificationType_NOTIFICATION_TYPE_HIKE_OVERDUE, TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_EVENT,
			TargetId: ev.String(), RecipientIds: []string{u.String()}, SubjectUserId: hiker.String(),
		}}}
	if _, err := cores.ApplyCoreEvent(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := app.core[len(app.core)-1]
	n := got.Notification
	if got.Tayfa != tf || n == nil || n.Type != notification.TypeHikeOverdue || n.TargetType != notification.TargetEvent ||
		n.Target != ev || len(n.Recipients) != 1 || n.Recipients[0] != u || n.Subject != hiker || n.Actor != (id.ID{}) {
		t.Fatalf("olay = %+v %+v", got, n)
	}

	// Membership and tayfa state changes (issue #81).
	if _, err := cores.ApplyCoreEvent(ctx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(),
		Event: &chatv1.ApplyCoreEventRequest_MembershipChanged{MembershipChanged: &chatv1.MembershipChanged{UserId: u.String()}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.core[len(app.core)-1]; got.Tayfa != tf || got.MemberChanged != u || got.StatusChanged || got.Purged {
		t.Fatalf("üyelik = %+v", got)
	}
	if _, err := cores.ApplyCoreEvent(ctx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(),
		Event: &chatv1.ApplyCoreEventRequest_TayfaStatusChanged{TayfaStatusChanged: &chatv1.TayfaStatusChanged{}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.core[len(app.core)-1]; got.Tayfa != tf || !got.StatusChanged {
		t.Fatalf("durum = %+v", got)
	}

	// Event channels (issue #183).
	if _, err := cores.ApplyCoreEvent(ctx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(),
		Event: &chatv1.ApplyCoreEventRequest_MembershipChanged{MembershipChanged: &chatv1.MembershipChanged{UserId: u.String(), Ended: true}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.core[len(app.core)-1]; got.MemberChanged != u || !got.MemberEnded {
		t.Fatalf("biten üyelik = %+v", got)
	}
	if _, err := cores.ApplyCoreEvent(ctx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(),
		Event: &chatv1.ApplyCoreEventRequest_EventAttendanceChanged{EventAttendanceChanged: &chatv1.EventAttendanceChanged{EventId: ev.String(), UserId: u.String()}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.core[len(app.core)-1]; got.Attendance == nil || got.Attendance.Event != ev || got.Attendance.User != u {
		t.Fatalf("katılım = %+v", got)
	}
	end, occurred := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if _, err := cores.ApplyCoreEvent(ctx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(), OccurredAt: timestamppb.New(occurred),
		Event: &chatv1.ApplyCoreEventRequest_EventScheduleChanged{EventScheduleChanged: &chatv1.EventScheduleChanged{EventId: ev.String(), EndTime: timestamppb.New(end), Cancelled: true}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.core[len(app.core)-1]; got.Schedule == nil || got.Schedule.Event != ev || !got.Schedule.End.Equal(end) || !got.OccurredAt.Equal(occurred) {
		t.Fatalf("takvim = %+v", got)
	}

	notice := func(f func(n *chatv1.NotificationRequested)) *chatv1.ApplyCoreEventRequest {
		n := &chatv1.NotificationRequested{
			Type: chatv1.NotificationType_NOTIFICATION_TYPE_ANNOUNCEMENT, TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANNOUNCEMENT,
			TargetId: id.New().String(), RecipientIds: []string{u.String()},
		}
		f(n)
		return &chatv1.ApplyCoreEventRequest{EventId: id.New().String(), TayfaId: tf.String(), ActorId: actor.String(),
			Event: &chatv1.ApplyCoreEventRequest_NotificationRequested{NotificationRequested: n}}
	}
	for name, r := range map[string]*chatv1.ApplyCoreEventRequest{
		"olay kimliği":  {EventId: "x", TayfaId: tf.String(), Event: coreReq().Event},
		"tayfa":         {EventId: id.New().String(), TayfaId: "x", Event: coreReq().Event},
		"aktör":         {EventId: id.New().String(), TayfaId: tf.String(), ActorId: "x", Event: coreReq().Event},
		"tür yok":       {EventId: id.New().String(), TayfaId: tf.String()},
		"bildirim türü": notice(func(n *chatv1.NotificationRequested) { n.Type = 0 }),
		"hedef türü":    notice(func(n *chatv1.NotificationRequested) { n.TargetType = 0 }),
		"hedef":         notice(func(n *chatv1.NotificationRequested) { n.TargetId = "x" }),
		"bozuk alıcı":   notice(func(n *chatv1.NotificationRequested) { n.RecipientIds = []string{"x"} }),
		"çok alıcı":     notice(func(n *chatv1.NotificationRequested) { n.RecipientIds = make([]string, 201) }),
		"soru":          notice(func(n *chatv1.NotificationRequested) { n.QuestionId = "x" }),
		"uyarı nedeni":  notice(func(n *chatv1.NotificationRequested) { n.WarningReason = 9 }),
		"üye": {EventId: id.New().String(), TayfaId: tf.String(),
			Event: &chatv1.ApplyCoreEventRequest_MembershipChanged{MembershipChanged: &chatv1.MembershipChanged{UserId: "x"}}},
		"katılım etkinliği": {EventId: id.New().String(), TayfaId: tf.String(),
			Event: &chatv1.ApplyCoreEventRequest_EventAttendanceChanged{EventAttendanceChanged: &chatv1.EventAttendanceChanged{EventId: "x", UserId: u.String()}}},
		"katılım kullanıcısı": {EventId: id.New().String(), TayfaId: tf.String(),
			Event: &chatv1.ApplyCoreEventRequest_EventAttendanceChanged{EventAttendanceChanged: &chatv1.EventAttendanceChanged{EventId: ev.String()}}},
		"takvim bitişsiz": {EventId: id.New().String(), TayfaId: tf.String(),
			Event: &chatv1.ApplyCoreEventRequest_EventScheduleChanged{EventScheduleChanged: &chatv1.EventScheduleChanged{EventId: ev.String()}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cores.ApplyCoreEvent(ctx, r); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// identity's data_export_ready reaches the app with the request ID; core
// cannot ask for the data_export_ready type (it is not in core's list).
func TestApplyUserEventDataExportReady(t *testing.T) {
	c := satest.New(t)
	app := &fakeApp{}
	users, cores, _ := eventConn(t, c, app)
	ctx := auth.WithToken(context.Background(), c.ServiceToken(t, identitySA, "chat.tayfam.internal"))
	u, request := id.New(), id.New()
	if _, err := users.ApplyUserEvent(ctx, &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: u.String(),
		Event: &chatv1.ApplyUserEventRequest_DataExportReady{DataExportReady: &chatv1.DataExportReady{DataExportId: request.String()}}}); err != nil {
		t.Fatal(err)
	}
	if got := app.user[len(app.user)-1]; got.User != u || got.DataExportReady != request {
		t.Fatalf("olay = %+v", got)
	}
	if _, err := users.ApplyUserEvent(ctx, &chatv1.ApplyUserEventRequest{EventId: id.New().String(), UserId: u.String(),
		Event: &chatv1.ApplyUserEventRequest_DataExportReady{DataExportReady: &chatv1.DataExportReady{DataExportId: "x"}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bozuk talep kimliği: %v", err)
	}
	coreCtx := auth.WithToken(context.Background(), c.ServiceToken(t, coreSA, "chat.tayfam.internal"))
	if _, err := cores.ApplyCoreEvent(coreCtx, &chatv1.ApplyCoreEventRequest{EventId: id.New().String(),
		Event: &chatv1.ApplyCoreEventRequest_NotificationRequested{NotificationRequested: &chatv1.NotificationRequested{
			Type: chatv1.NotificationType_NOTIFICATION_TYPE_DATA_EXPORT_READY, TargetType: chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_DATA_EXPORT,
			TargetId: request.String(), RecipientIds: []string{u.String()},
		}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("core data_export_ready isteyememeli: %v", err)
	}
}

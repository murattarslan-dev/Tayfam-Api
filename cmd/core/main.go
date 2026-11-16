// Command core owns tayfas, memberships, events, Q&A, polls, social graph and
// media (ADR-0002, ADR-0009).
//
//	core           runs the service
//	core migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
	_ "time/tzdata" // series time zones: the same IANA database in every image (issue #51)

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/availability"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/channelaccess"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatdm"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatevents"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatrelay"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/directmessage"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/discover"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/export"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/identityadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/identityminor"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/instant"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/jobs"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/poll"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/userevents"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/core/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

// joinCodeAttemptsPerHour limits JoinByCode per user against code guessing
// (issue #32). In memory; core runs one replica.
const joinCodeAttemptsPerHour = 10

func main() {
	service.MainWithMigrations("core", coredb.Migrations(), setup)
}

func setup(rt *service.Runtime) error {
	c := rt.Config
	// Veritabanı parolası core-db secret'ından (deploy/apps/<ortam>/secrets,
	// SOPS). core yalnızca kendi veritabanına bağlanır (ADR-0002).
	dbCfg := db.LoadConfig(c)
	// Keşfet (issue #210, ADR-0033): kapalı gelir; açma kararı platform
	// sahibinindir (hukukçu görüşmesinden sonra). Kapalıyken akış, Keşfet'ten
	// katılım ve geri bildirim uçları feature-disabled döner; tayfa ayarı ve
	// panel onayı çalışır. Güvenlik kişisi ayrı bayraktır ve açık gelir
	// (platform sahibi, 2026-11-16): bütün yüz yüze etkinliklerde.
	discoverEnabled := c.OneOf("DISCOVER_ENABLED", "false", "true", "false") == "true"
	safetyContactEnabled := c.OneOf("SAFETY_CONTACT_ENABLED", "true", "true", "false") == "true"
	if err := c.Err(); err != nil {
		return err
	}

	pool, err := db.Open(context.Background(), dbCfg.URL())
	if err != nil {
		return err
	}
	rt.Health.Add("postgres", db.Ping(pool))

	// Her istekteki token, identity'nin açık anahtarlarıyla burada yeniden
	// doğrulanır (ADR-0011). Açık metod yoktur; health hariç her metod
	// geçerli token ister.
	v, err := rt.IdentityVerifier()
	if err != nil {
		return err
	}
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: rt.Log})
	if err != nil {
		return err
	}
	events, err := event.New(event.Config{Pool: pool, Log: rt.Log})
	if err != nil {
		return err
	}
	memberships, err := membership.New(membership.Config{
		Pool:            pool,
		JoinLimiter:     ratelimit.NewPer(joinCodeAttemptsPerHour, time.Hour),
		DiscoverEnabled: discoverEnabled,
	})
	if err != nil {
		return err
	}
	discovers, err := discover.New(discover.Config{Pool: pool, Enabled: discoverEnabled, SafetyContactEnabled: safetyContactEnabled})
	if err != nil {
		return err
	}
	rt.Log.Info("keşfet bayrağı", slog.Bool("açık", discoverEnabled), slog.Bool("güvenlik_kişisi", safetyContactEnabled))
	instants, err := instant.New(instant.Config{Pool: pool, Log: rt.Log, Tayfas: tayfas, Events: events, Memberships: memberships})
	if err != nil {
		return err
	}
	announcements, err := announcement.New(announcement.Config{Pool: pool})
	if err != nil {
		return err
	}
	questions, err := qa.New(qa.Config{Pool: pool})
	if err != nil {
		return err
	}
	polls, err := poll.New(poll.Config{Pool: pool})
	if err != nil {
		return err
	}
	socials, err := social.New(social.Config{Pool: pool})
	if err != nil {
		return err
	}
	// "Boşum" (issue #207, ADR-0029).
	availabilities, err := availability.New(availability.Config{Pool: pool})
	if err != nil {
		return err
	}
	profiles, err := profile.New(profile.Config{Pool: pool})
	if err != nil {
		return err
	}
	minors, err := identityMinors(rt)
	if err != nil {
		return err
	}
	chatConn, err := chatConnection(rt)
	if err != nil {
		return err
	}
	chatServiceConn, err := chatServiceConnection(rt)
	if err != nil {
		return err
	}
	reportCfg := report.Config{Pool: pool, Minors: minors}
	if chatConn != nil {
		reportCfg.DirectMessages = chatdm.New(chatv1.NewDirectMessageServiceClient(chatConn))
		// Kanal mesajı şikâyeti (issue #205, ADR-0031): şikâyet edenin
		// token'ıyla doğrulama; çözümde durum core'un servis token'ıyla.
		reportCfg.ChannelMessages = chatdm.NewChannelMessages(chatv1.NewChannelServiceClient(chatConn))
	}
	if chatServiceConn != nil {
		reportCfg.ChannelStatus = chatdm.NewChannelStatus(chatv1.NewReportedMessageServiceClient(chatServiceConn))
	}
	reports, err := report.New(reportCfg)
	if err != nil {
		return err
	}
	channelAccess, err := channelaccess.New(channelaccess.Config{Pool: pool})
	if err != nil {
		return err
	}
	directMessages, err := directmessage.New(directmessage.Config{Pool: pool})
	if err != nil {
		return err
	}

	medias, err := mediaService(rt, pool, chatConn, chatServiceConn)
	if err != nil {
		return err
	}
	// KVKK veri dışa aktarma (issue #144, ADR-0021): yalnızca çağıranın
	// kendi verisi; identity kullanıcının token'ıyla çağırır.
	exports, err := export.New(export.Config{Pool: pool})
	if err != nil {
		return err
	}

	// Arkadaş önerisi (issue #208, ADR-0030): 18 yaş sınırı identity'nin
	// IsMinor cevabıyla; identity yoksa (yalnızca yerel) öneri yapılmaz.
	background, err := jobs.New(jobs.Config{Pool: pool, Log: rt.Log, Minors: minors})
	if err != nil {
		return err
	}

	userEvents, err := userevents.New(userevents.Config{Pool: pool, Log: rt.Log})
	if err != nil {
		return err
	}
	// chat'in olayları (issue #205, ADR-0031): mesajın bıraktığı dosya.
	chatEvents, err := chatevents.New(pool)
	if err != nil {
		return err
	}

	// Kullanıcısız servis çağrıları (ADR-0013): UserEventService'in her
	// metodu yalnızca servis token'ı kabul eder; ApplyUserEvent'i yalnızca
	// identity'nin ServiceAccount'u çağırabilir. Yerelde (cluster yok)
	// doğrulayıcı yoksa bu metodlar kapalıdır.
	si, err := rt.ServiceIdentity()
	if err != nil {
		return err
	}
	// ChatEventService'i yalnızca chat'in ServiceAccount'u çağırabilir
	// (ADR-0031).
	policy := auth.ServicePolicy{Verifier: si.Verifier, Services: []string{grpcapi.UserEventServiceName, grpcapi.ChatEventServiceName}}
	if si.Verifier != nil {
		policy.Callers = map[string][]auth.ServiceAccount{
			corev1.UserEventService_ApplyUserEvent_FullMethodName: {si.Account("tayfam-identity")},
			corev1.ChatEventService_ApplyChatEvent_FullMethodName: {si.Account("tayfam-chat")},
		}
	}
	// Guard'a açık metod verilmez: kullanıcı metodları kullanıcının erişim
	// token'ını ister (ADR-0011).
	guard, err := auth.NewGuard(v).WithServices(policy)
	if err != nil {
		return err
	}
	// Platform yönetim paneli (ADR-0008, issue #104): AdminService yalnızca
	// admin token'ını kabul eder; her çağrıda identity'ye aynı token'la
	// yöneticilik sorulur. Diğer bütün metodlar admin token'ını reddeder.
	admins, adminV, err := platformAdmin(rt, pool, medias)
	if err != nil {
		return err
	}
	guard, err = guard.WithAdmin(auth.AdminPolicy{Verifier: adminV, Services: []string{grpcapi.AdminServiceName}})
	if err != nil {
		return err
	}
	srv := rt.NewGRPCServer(guard.ServerOptions()...)
	corev1.RegisterTayfaServiceServer(srv, grpcapi.NewTayfaServer(tayfas, rt.Log))
	corev1.RegisterMembershipServiceServer(srv, grpcapi.NewMembershipServer(memberships, rt.Log))
	corev1.RegisterUserEventServiceServer(srv, grpcapi.NewUserEventServer(userEvents, rt.Log))
	corev1.RegisterChatEventServiceServer(srv, grpcapi.NewChatEventServer(chatEvents, rt.Log))
	corev1.RegisterAnnouncementServiceServer(srv, grpcapi.NewAnnouncementServer(announcements, rt.Log))
	corev1.RegisterEventServiceServer(srv, grpcapi.NewEventServer(events, rt.Log).WithInstant(instants))
	corev1.RegisterQAServiceServer(srv, grpcapi.NewQAServer(questions, rt.Log))
	corev1.RegisterPollServiceServer(srv, grpcapi.NewPollServer(polls, rt.Log))
	corev1.RegisterSocialServiceServer(srv, grpcapi.NewSocialServer(socials, rt.Log))
	corev1.RegisterAvailabilityServiceServer(srv, grpcapi.NewAvailabilityServer(availabilities, rt.Log))
	corev1.RegisterDiscoverServiceServer(srv, grpcapi.NewDiscoverServer(discovers, memberships, rt.Log))
	corev1.RegisterProfileServiceServer(srv, grpcapi.NewProfileServer(profiles, rt.Log))
	corev1.RegisterReportServiceServer(srv, grpcapi.NewReportServer(reports, rt.Log))
	corev1.RegisterAdminServiceServer(srv, grpcapi.NewAdminServer(admins, rt.Log))
	// chat'in kanal izinleri (issue #81): kullanıcının kendi token'ıyla,
	// core'un tek izin tablosundan.
	corev1.RegisterChannelAccessServiceServer(srv, grpcapi.NewChannelAccessServer(channelAccess, rt.Log))
	// chat'in birebir mesaj kuralı (issue #95): gönderen çağıranın kendisi.
	corev1.RegisterDirectMessageServiceServer(srv, grpcapi.NewDirectMessageServer(directMessages, rt.Log))
	// Dosyalar (issue #135, ADR-0020): R2 yapılandırması yoksa uçlar
	// UNAVAILABLE döner, işçiler boşta bekler.
	corev1.RegisterMediaServiceServer(srv, grpcapi.NewMediaServer(medias, rt.Log))
	corev1.RegisterExportServiceServer(srv, grpcapi.NewExportServer(exports, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	// Süresi dolan ya da içeriği silinen dosyaların R2'den silinmesi
	// (birden fazla pod'da çalışabilir; her adım idempotent).
	rt.AddWorker("media-cleanup", medias.RunCleanup)
	// Hiç yazılmamış mesaja bağlı kalan sohbet dosyaları (ADR-0031).
	rt.AddWorker("media-chat-reconcile", medias.RunReconcile)
	// Tekrarlayan etkinliklerin önümüzdeki 8 haftalık örnekleri (issue #51).
	// Birden fazla pod'da çalışabilir: tayfa ve seri satırı kilitlenir,
	// örnekler (series_id, starts_at) ile tekildir.
	rt.AddWorker("event-series", events.RunSeriesGenerator)
	// Pasif tayfa temizliği, 72 saatlik tayfa silme, 30 günlük kalıcı silme,
	// yürüyüş dönmeme alarmı ve plaka temizliği (issue #66); şikâyetlerin
	// 7 gün sonra platforma yükselmesi ve 1 yıllık saklama süresi (#76);
	// arkadaşlık puanı ve haftalık arkadaş önerisi (#208); güvenlik kişisi
	// alarmı (#210; bayraktan bağımsız: seçilmiş kişiye alarm her zaman
	// gider). Her biri ayrı
	// bir worker; birden fazla pod'da her partiyi advisory kilidi alan tek
	// pod işler.
	for _, w := range background.Workers() {
		rt.AddWorker(w.Name, w.Run)
	}
	return addRelay(rt, pool)
}

// chatServiceConnection connects to chat with core's service token for chat
// (ADR-0013), for calls without a user: the reconciliation of chat
// attachments and a reported channel message's status (ADR-0031). Without
// the token (local runs only) it returns nil.
func chatServiceConnection(rt *service.Runtime) (*grpc.ClientConn, error) {
	tok, ok := rt.ServiceToken("chat")
	if !ok {
		return nil, rt.Config.Err()
	}
	addr := rt.Config.Required("CHAT_GRPC_ADDR")
	if err := rt.Config.Err(); err != nil {
		return nil, err
	}
	return grpcx.NewClient(addr, auth.ServiceTokenFrom(tok)...)
}

// mediaService returns the media service (issue #135, ADR-0020). The R2
// credentials come from the optional core-r2 secret; without them media is
// off and core still starts (and has no internet egress: the core-egress-r2
// NetworkPolicy is added to an environment together with its secret).
// Partial or malformed values stop the start.
//
// Who may see a chat message's file is chat's answer (ADR-0031), asked
// with the caller's own token over chatConn; without chat those files are
// visible to their owners only.
func mediaService(rt *service.Runtime, pool *pgxpool.Pool, chatConn, chatServiceConn *grpc.ClientConn) (*media.Service, error) {
	c := rt.Config
	perHour := c.Int("MEDIA_UPLOADS_PER_HOUR", media.DefaultUploadsPerHour)
	quotaMiB := c.Int("MEDIA_QUOTA_MIB", media.DefaultQuotaBytes>>20)
	r2, err := media.LoadR2(c, rt.Env)
	if err != nil {
		return nil, err
	}
	if err := c.Err(); err != nil {
		return nil, err
	}
	if perHour < 1 || quotaMiB < 1 {
		return nil, errors.New("MEDIA_UPLOADS_PER_HOUR ve MEDIA_QUOTA_MIB pozitif olmalı")
	}
	cfg := media.Config{Pool: pool, Log: rt.Log, UploadsPerHour: perHour, QuotaBytes: int64(quotaMiB) << 20}
	if chatConn != nil {
		cfg.Messages = chatdm.NewMessageMedia(chatv1.NewMessageMediaServiceClient(chatConn))
	}
	// Sohbet bağlamalarının uzlaştırılması (ADR-0031): kullanıcısız iş,
	// core'un chat için servis token'ıyla (ADR-0013). Token yoksa (yalnızca
	// yerel ortam) iş kapalıdır; onaylanmamış bağlama yine taşınabilir.
	if chatServiceConn != nil {
		cfg.Holders = chatdm.NewHolders(chatv1.NewMediaHolderServiceClient(chatServiceConn))
	}
	if r2 == nil {
		rt.Log.Info("R2 yapılandırması yok; dosya yükleme kapalı")
	} else {
		client, err := objstore.New(*r2)
		if err != nil {
			return nil, err
		}
		cfg.Store = client
		rt.Log.Info("dosya yükleme açık (R2)")
	}
	return media.New(cfg)
}

// platformAdmin returns the panel's service and the admin token verifier,
// both with identity: its public keys check the admin token, and a
// connection that forwards the caller's admin token asks whether the
// caller is still a platform administrator.
func platformAdmin(rt *service.Runtime, pool *pgxpool.Pool, medias *media.Service) (*admin.Service, *auth.AdminVerifier, error) {
	addr := rt.Config.Required("IDENTITY_GRPC_ADDR")
	issuer := rt.Config.Required("AUTH_ISSUER")
	if err := rt.Config.Err(); err != nil {
		return nil, nil, err
	}
	conn, err := grpcx.NewClient(addr, auth.ForwardToken()...)
	if err != nil {
		return nil, nil, err
	}
	av, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{
		Keys: auth.NewRemoteKeys(auth.FetchFromIdentity(identityv1.NewKeyServiceClient(conn))), Issuer: issuer,
	})
	if err != nil {
		return nil, nil, err
	}
	// medias signs the panel's short links: a reported album photo
	// (ADR-0028 §5) and a DM report's evidence photo on click (ADR-0031).
	svc, err := admin.New(admin.Config{Pool: pool, Log: rt.Log, Checker: identityadmin.NewChecker(identityv1.NewAdminAuthServiceClient(conn)), Media: medias})
	if err != nil {
		return nil, nil, err
	}
	return svc, av, nil
}

// identityMinors returns the client of identity's IsMinor with core's
// service token for identity (ADR-0012, ADR-0013). Only a local environment
// may run without the token; every report then counts as involving a minor
// (the safe side).
func identityMinors(rt *service.Runtime) (report.MinorChecker, error) {
	tok, ok := rt.ServiceToken("identity")
	if !ok {
		if err := rt.Config.Err(); err != nil {
			return nil, err
		}
		rt.Log.Warn("identity servis token'ı yok; şikâyetler 18 yaş altı sayılır (yalnızca yerel ortam)")
		return identityminor.Unavailable{}, nil
	}
	addr := rt.Config.Required("IDENTITY_GRPC_ADDR")
	if err := rt.Config.Err(); err != nil {
		return nil, err
	}
	conn, err := grpcx.NewClient(addr, auth.ServiceTokenFrom(tok)...)
	if err != nil {
		return nil, err
	}
	return identityminor.New(identityv1.NewMinorServiceClient(conn)), nil
}

// chatConnection returns the connection to chat that forwards the caller's
// own token (auth.ForwardToken; core gets no service identity for it,
// ADR-0013): reported direct messages (issue #95, ADR-0009) and who may
// see a chat message's file (issue #205, ADR-0031). Without CHAT_GRPC_ADDR
// (a local run without chat) direct message reports are UNAVAILABLE and
// chat files are visible to their owners only.
func chatConnection(rt *service.Runtime) (*grpc.ClientConn, error) {
	addr := rt.Config.String("CHAT_GRPC_ADDR", "")
	if addr == "" {
		rt.Log.Warn("CHAT_GRPC_ADDR yok; birebir mesaj şikâyetleri kapalı (UNAVAILABLE), sohbet dosyaları yalnızca sahibine")
		return nil, nil
	}
	return grpcx.NewClient(addr, auth.ForwardToken()...)
}

// addRelay starts the outbox relay that delivers core's events to chat with
// core's service token for chat (ADR-0002, ADR-0013). Only the event types
// in chatrelay.Types reach chat: notification.requested and tayfa.purged.
// Only a local environment may run without it; events then wait in the
// outbox.
func addRelay(rt *service.Runtime, pool *pgxpool.Pool) error {
	chatToken, ok := rt.ServiceToken("chat")
	if !ok {
		if err := rt.Config.Err(); err != nil {
			return err
		}
		rt.Log.Warn("chat servis token'ı yok; outbox relay kapalı (yalnızca yerel ortam)")
		return nil
	}
	addr := rt.Config.Required("CHAT_GRPC_ADDR")
	if err := rt.Config.Err(); err != nil {
		return err
	}
	conn, err := grpcx.NewClient(addr, auth.ServiceTokenFrom(chatToken)...)
	if err != nil {
		return err
	}
	consumers := []outbox.Consumer{chatrelay.Consumer(chatv1.NewCoreEventServiceClient(conn))}
	// Panel işlemlerinin denetim kaydı identity'ye (issue #104), core'un
	// identity için servis token'ıyla.
	if idToken, ok := rt.ServiceToken("identity"); ok {
		idConn, err := grpcx.NewClient(rt.Config.Required("IDENTITY_GRPC_ADDR"), auth.ServiceTokenFrom(idToken)...)
		if err != nil {
			return err
		}
		consumers = append(consumers, identityadmin.AuditConsumer(identityv1.NewPlatformAuditServiceClient(idConn)))
	} else {
		rt.Log.Warn("identity servis token'ı yok; platform denetim kayıtları outbox'ta bekler (yalnızca yerel ortam)")
	}
	relay, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Log: rt.Log, Consumers: consumers})
	if err != nil {
		return err
	}
	rt.AddWorker("outbox", relay.Run)
	return nil
}

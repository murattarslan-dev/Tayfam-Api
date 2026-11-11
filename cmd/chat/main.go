// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005, ADR-0009).
//
//	chat           runs the service
//	chat migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coreevents"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coremembers"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push/fcm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/chat/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/transport/ws"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

// channelAccessTTL is how long core's channel permission answer is reused.
const channelAccessTTL = 5 * time.Second

func main() {
	service.MainWithMigrations("chat", chatdb.Migrations(), setup)
}

func setup(rt *service.Runtime) error {
	c := rt.Config
	// Veritabanı parolası chat-db secret'ından (deploy/apps/<ortam>/secrets,
	// SOPS). chat yalnızca kendi veritabanına bağlanır (ADR-0002).
	dbCfg := db.LoadConfig(c)
	// Kanal mesajı şifreleme anahtarı (chat-message-key secret'ı, SOPS;
	// issue #81). prod'da zorunludur. local ve dev anahtarsız açılabilir:
	// o zaman kanal metodları UNAVAILABLE döner, mesaj yazılmaz.
	messageKeys := c.String("CHAT_MESSAGE_KEYS", "")
	// WebSocket (issue #82): gateway GET /v1/realtime'ı kullanıcının erişim
	// token'ıyla bu porta aktarır; chat token'ı yeniden doğrular. Origin
	// taşıyan (tarayıcıdan gelen) istek reddedilir (ADR-0017).
	// Bağlantılar bellektedir; chat tek kopya çalışır (ADR-0005 "Ölçekleme").
	httpAddr := c.String("HTTP_ADDR", ":8080")
	maxConns := c.Int("REALTIME_MAX_CONNECTIONS", 2000)
	// Push (FCM HTTP v1; issue #134, ADR-0019): servis hesabı JSON'u
	// chat-fcm secret'ından (SOPS). Yoksa push kapalıdır: servis başlar,
	// token kaydı çalışır, gönderim yapılmaz.
	fcmAccount := c.String("CHAT_FCM_SERVICE_ACCOUNT", "")
	// Firebase kurulunca prod'da push zorunludur (ADR-0019 madde 7): prod
	// overlay'i secret'la birlikte CHAT_FCM_REQUIRED=true verir; o zaman
	// değer yoksa servis başlamaz.
	fcmRequired := c.OneOf("CHAT_FCM_REQUIRED", "false", "true", "false") == "true"
	if err := c.Err(); err != nil {
		return err
	}
	cipher, err := channelcrypt.Load(rt.Env, messageKeys)
	if err != nil {
		return err
	}
	if cipher != nil {
		rt.Log.Info("kanal mesajı anahtarı yüklendi", slog.Int("active_version", int(cipher.ActiveVersion())))
	} else {
		rt.Log.Warn("CHAT_MESSAGE_KEYS yok; kanallar ve birebir mesajlar kapalı (UNAVAILABLE)")
	}
	var sender push.Sender
	if fcmAccount != "" {
		acct, err := fcm.ParseServiceAccount([]byte(fcmAccount))
		if err != nil {
			return err
		}
		client, err := fcm.New(fcm.Config{Account: acct})
		if err != nil {
			return err
		}
		sender = client
		rt.Log.Info("push açık (FCM)", slog.String("project_id", acct.ProjectID()))
	} else {
		if fcmRequired {
			return errors.New("CHAT_FCM_REQUIRED=true ama CHAT_FCM_SERVICE_ACCOUNT yok")
		}
		rt.Log.Info("CHAT_FCM_SERVICE_ACCOUNT yok; push kapalı")
	}
	if rt.Env == "prod" && !fcmRequired {
		rt.Log.Warn("prod'da CHAT_FCM_REQUIRED açık değil; Firebase kurulunca açılmalı (ADR-0019)")
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

	// Açık bağlantılar, abonelikler ve kapatılmış oturumlar (issue #82).
	hub, err := realtime.NewHub(realtime.HubConfig{MaxTotal: maxConns, MaxPerUser: 5, MaxSubscriptions: 100})
	if err != nil {
		return err
	}
	live, err := realtime.NewLive(realtime.LiveConfig{Hub: hub, Log: rt.Log})
	if err != nil {
		return err
	}

	// Kullanıcısız servis çağrıları (ADR-0013): olay tüketici servislerinin
	// her metodu yalnızca servis token'ı kabul eder. UserEventService'i
	// yalnızca identity'nin, CoreEventService'i yalnızca core'un
	// ServiceAccount'u çağırabilir. Yerelde (cluster yok) doğrulayıcı yoksa
	// bu metodlar kapalıdır.
	si, err := rt.ServiceIdentity()
	if err != nil {
		return err
	}
	policy := auth.ServicePolicy{Verifier: si.Verifier, Services: []string{grpcapi.UserEventServiceName, grpcapi.CoreEventServiceName}}
	if si.Verifier != nil {
		policy.Callers = map[string][]auth.ServiceAccount{
			chatv1.UserEventService_ApplyUserEvent_FullMethodName: {si.Account("tayfam-identity")},
			chatv1.CoreEventService_ApplyCoreEvent_FullMethodName: {si.Account("tayfam-core")},
		}
	}
	guard, err := auth.NewGuard(v).WithServices(policy)
	if err != nil {
		return err
	}
	// Sessize alma için üyeliği, kanal işlemleri için izni core söyler
	// (ADR-0009 senkron sorgular); çağrı kullanıcının kendi token'ını taşır.
	coreAddr := c.Required("CORE_GRPC_ADDR")
	if err := c.Err(); err != nil {
		return err
	}
	coreConn, err := grpcx.NewClient(coreAddr, auth.ForwardToken()...)
	if err != nil {
		return err
	}
	// Kanal izinlerini core'un tek izin tablosu verir (CLAUDE.md kural 5,
	// ChannelAccessService), kullanıcının kendi token'ıyla. Cevap kısa süre
	// önbellekte tutulur (ADR-0005); core'un membership.changed ve tayfa
	// durum olayları önbelleği hemen temizler, kaçan bir olay en geç bu
	// sürede telafi edilir.
	channelAccess := coremembers.NewChannelAccess(corev1.NewChannelAccessServiceClient(coreConn), channelAccessTTL)
	// Kaydedilen değişiklikler abonelere canlı iletilir (ADR-0005).
	// Spam koruması (issue #101): kullanıcı başına tek mesaj hızı sınırı;
	// kanal ve birebir mesaj, REST ve WebSocket aynı sayacı paylaşır
	// (dakikada 30, kısa patlama 10). Tek replika: bellek içi yeter
	// (ADR-0005). Yavaş mod ve DM istek sınırı ayrıca geçerlidir.
	sendLimit := ratelimit.New(30, 10)
	channels, err := channel.New(channel.Config{
		Pool: pool, Log: rt.Log, Access: channelAccess, SendLimit: sendLimit, Cipher: cipher, Publisher: live,
	})
	if err != nil {
		return err
	}
	live.Bind(channels)
	// Birebir mesajlar (issue #95, düz sürüm): kim kime yazabilir core'a
	// her mesajda, kullanıcının kendi token'ıyla sorulur (CheckDirectMessage).
	// Metin kanal mesajlarıyla aynı anahtarla şifreli saklanır; anahtar
	// yoksa DM metodları da UNAVAILABLE döner. REST ve WebSocket gönderimi
	// aynı fonksiyondan geçer.
	directMessages, err := dm.New(dm.Config{
		Pool: pool, Log: rt.Log, Rules: coremembers.NewDirectMessageRules(corev1.NewDirectMessageServiceClient(coreConn)),
		SendLimit: sendLimit, Cipher: cipher, Publisher: live.Direct(),
	})
	if err != nil {
		return err
	}
	live.BindDirect(directMessages)
	// Tayfa sessize alınırken üyeliği core söyler; birebir konuşma sessize
	// alınırken tarafı olup olmadığına chat kendi verisinden bakar (issue
	// #138).
	notifications, err := notification.New(notification.Config{
		Pool: pool, Log: rt.Log, Members: coremembers.New(corev1.NewMembershipServiceClient(coreConn)),
		Conversations: directMessages,
	})
	if err != nil {
		return err
	}
	// Birebir mesajda uçtan uca şifreleme, sunucu tarafı (issue #127,
	// ADR-0018): sunucu kriptografi yapmaz; açık anahtarları, zarfları,
	// arşivi ve eşleştirme mesajlarını saklar ve iletir. Ön anahtar paketini
	// yalnızca o an mesaj gönderebilecek kişi çeker (dm'in kuralları, core'a
	// kullanıcının token'ıyla). Kullanıcı başına hız sınırları: paket çekme
	// dakikada 20, anahtar işlemleri 30, arşiv yazma 120.
	e2e, err := keys.New(keys.Config{
		Pool: pool, Log: rt.Log, Contacts: directMessages, Publisher: live.Keys(),
		Bundles: ratelimit.New(20, 10), KeyOps: ratelimit.New(30, 10), Archive: ratelimit.New(120, 60),
	})
	if err != nil {
		return err
	}
	live.BindKeys(e2e)
	// Push token'ı oturuma bağlıdır (ADR-0019); kayıt kullanıcı başına
	// dakikada 10 (patlama 5). Bağlı kullanıcıya push gitmez.
	pushes, err := push.New(push.Config{
		Pool: pool, Log: rt.Log, Sender: sender, Presence: hub, Register: ratelimit.New(10, 5),
	})
	if err != nil {
		return err
	}
	// Hesap silinince bildirimler ve tercihler de silinir (ADR-0009).
	// Kanal mesajları kalır, yazar alanı boşalır; okunma kayıtları silinir.
	// Birebir konuşmalarda silinen kişinin tarafı ve gönderen alanı boşalır
	// (v14 "İlişkiler ve Silme Davranışı"); E2E cihazları, arşivi ve
	// bekleyen zarfları ve push token'ları silinir (ADR-0018, ADR-0019).
	// Oturum iptali, yaptırım ve silme açık bağlantıları kapatır (ADR-0004);
	// biten oturumun E2E cihazı ve push token'ı da silinir (cihaz = oturum).
	userEvents, err := userevents.New(userevents.Config{
		Pool:       pool,
		Cleanups:   []userevents.Cleanup{notification.Cleanup, channel.Cleanup, dm.Cleanup, keys.Cleanup, push.Cleanup},
		Sessions:   hub,
		Devices:    e2e,
		PushTokens: pushes,
	})
	if err != nil {
		return err
	}
	// Bildirimlerin alıcılarını core seçer (ADR-0009); chat kaydı yazar.
	// Üyelik ve tayfa durumu olayları yalnızca kanal izni önbelleğini
	// temizler; chat üyelik kopyası tutmaz.
	// Üyelik değişince açık abonelikler bağlantının kendi token'ıyla core'a
	// yeniden sorulur; yeni bildirimler bağlı alıcılara iletilir.
	coreEvents, err := coreevents.New(coreevents.Config{Pool: pool, Access: channelAccess, Live: live})
	if err != nil {
		return err
	}

	srv := rt.NewGRPCServer(guard.ServerOptions()...)
	chatv1.RegisterUserEventServiceServer(srv, grpcapi.NewUserEventServer(userEvents, rt.Log))
	chatv1.RegisterCoreEventServiceServer(srv, grpcapi.NewCoreEventServer(coreEvents, rt.Log))
	chatv1.RegisterNotificationServiceServer(srv, grpcapi.NewNotificationServer(notifications, rt.Log))
	chatv1.RegisterChannelServiceServer(srv, grpcapi.NewChannelServer(channels, rt.Log))
	chatv1.RegisterDirectMessageServiceServer(srv, grpcapi.NewDirectMessageServer(directMessages, rt.Log))
	chatv1.RegisterE2EServiceServer(srv, grpcapi.NewE2EServer(e2e, rt.Log))
	chatv1.RegisterPushServiceServer(srv, grpcapi.NewPushServer(pushes, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)

	wsHandler, err := ws.New(ws.Config{
		Verifier: v, Hub: hub, Live: live, Log: rt.Log,
		// Kullanıcı başına gelen çerçeve sınırı (ADR-0005 "gönderim hızı").
		Frames: ratelimit.New(60, 20),
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	wsHandler.Register(mux)
	rt.AddHTTPServer("http", httpAddr, mux)
	// Kapanışta bağlantılara 1001 gönderilir; abonelikler üyelik değişince
	// yeniden denetlenir.
	rt.AddWorker("realtime-hub", hub.Run)
	rt.AddWorker("realtime-recheck", live.Run)
	// 90 günden eski bildirimler (v14 "Saklama süreleri").
	rt.AddWorker("notification-retention", notifications.RunRetention)
	// Silinmiş kanal mesajları 30 gün sonra kalıcı silinir (v14).
	rt.AddWorker("channel-message-purge", channels.RunPurge)
	rt.AddWorker("direct-message-purge", directMessages.RunPurge)
	// Zarflar ve anahtar paylaşımları 30 gün, anahtar olayları 90 gün;
	// 90 gün görünmeyen cihazlar silinir (ADR-0018 madde 9).
	rt.AddWorker("e2e-retention", e2e.RunRetention)
	// Bildirim kaydıyla açılan push işleri (ADR-0019); push kapalıyken
	// çalışmaz. 60 gün yenilenmeyen token'lar ve 24 saatten eski işler
	// silinir.
	rt.AddWorker("push-sender", pushes.Run)
	rt.AddWorker("push-retention", pushes.RunRetention)
	return nil
}

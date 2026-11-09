// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005, ADR-0009).
//
//	chat           runs the service
//	chat migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"log/slog"
	"time"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coreevents"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/coremembers"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/chat/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

// membershipCacheTTL is how long core's membership answer is reused.
const membershipCacheTTL = 5 * time.Second

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
		rt.Log.Warn("CHAT_MESSAGE_KEYS yok; kanallar kapalı (UNAVAILABLE)")
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

	// Hesap silinince bildirimler ve tercihler de silinir (ADR-0009).
	// Kanal mesajları kalır, yazar alanı boşalır; okunma kayıtları silinir.
	userEvents, err := userevents.New(userevents.Config{Pool: pool, Cleanups: []userevents.Cleanup{notification.Cleanup, channel.Cleanup}})
	if err != nil {
		return err
	}
	// Bildirimlerin alıcılarını core seçer (ADR-0009); chat kaydı yazar.
	coreEvents, err := coreevents.New(coreevents.Config{Pool: pool})
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
	// Sessize alma için üyeliği core söyler (ADR-0009 senkron sorgular: chat
	// → core GetMembership); çağrı kullanıcının kendi token'ını taşır.
	coreAddr := c.Required("CORE_GRPC_ADDR")
	if err := c.Err(); err != nil {
		return err
	}
	coreConn, err := grpcx.NewClient(coreAddr, auth.ForwardToken()...)
	if err != nil {
		return err
	}
	// Kanal erişimi de core'un cevabıyla (rol, susturma). Cevap kısa süre
	// önbellekte tutulur (ADR-0005); core'un membership.changed olayı chat'e
	// henüz gelmediği için rol değişikliği ve susturma en geç bu sürede
	// geçerli olur.
	members := coremembers.NewCached(corev1.NewMembershipServiceClient(coreConn), membershipCacheTTL)
	notifications, err := notification.New(notification.Config{Pool: pool, Log: rt.Log, Members: members})
	if err != nil {
		return err
	}
	channels, err := channel.New(channel.Config{Pool: pool, Log: rt.Log, Members: members, Cipher: cipher})
	if err != nil {
		return err
	}

	srv := rt.NewGRPCServer(guard.ServerOptions()...)
	chatv1.RegisterUserEventServiceServer(srv, grpcapi.NewUserEventServer(userEvents, rt.Log))
	chatv1.RegisterCoreEventServiceServer(srv, grpcapi.NewCoreEventServer(coreEvents, rt.Log))
	chatv1.RegisterNotificationServiceServer(srv, grpcapi.NewNotificationServer(notifications, rt.Log))
	chatv1.RegisterChannelServiceServer(srv, grpcapi.NewChannelServer(channels, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	// 90 günden eski bildirimler (v14 "Saklama süreleri").
	rt.AddWorker("notification-retention", notifications.RunRetention)
	// Silinmiş kanal mesajları 30 gün sonra kalıcı silinir (v14).
	rt.AddWorker("channel-message-purge", channels.RunPurge)
	return nil
}

// Command core owns tayfas, memberships, events, Q&A, polls, social graph and
// media (ADR-0002, ADR-0009).
//
//	core           runs the service
//	core migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"time"
	_ "time/tzdata" // series time zones: the same IANA database in every image (issue #51)

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatrelay"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/jobs"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/poll"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/userevents"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/core/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
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
		Pool:        pool,
		JoinLimiter: ratelimit.NewPer(joinCodeAttemptsPerHour, time.Hour),
	})
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
	profiles, err := profile.New(profile.Config{Pool: pool})
	if err != nil {
		return err
	}

	background, err := jobs.New(jobs.Config{Pool: pool, Log: rt.Log})
	if err != nil {
		return err
	}

	userEvents, err := userevents.New(userevents.Config{Pool: pool, Log: rt.Log})
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
	policy := auth.ServicePolicy{Verifier: si.Verifier, Services: []string{grpcapi.UserEventServiceName}}
	if si.Verifier != nil {
		policy.Callers = map[string][]auth.ServiceAccount{
			corev1.UserEventService_ApplyUserEvent_FullMethodName: {si.Account("tayfam-identity")},
		}
	}
	// Guard'a açık metod verilmez: kullanıcı metodları kullanıcının erişim
	// token'ını ister (ADR-0011).
	guard, err := auth.NewGuard(v).WithServices(policy)
	if err != nil {
		return err
	}
	srv := rt.NewGRPCServer(guard.ServerOptions()...)
	corev1.RegisterTayfaServiceServer(srv, grpcapi.NewTayfaServer(tayfas, rt.Log))
	corev1.RegisterMembershipServiceServer(srv, grpcapi.NewMembershipServer(memberships, rt.Log))
	corev1.RegisterUserEventServiceServer(srv, grpcapi.NewUserEventServer(userEvents, rt.Log))
	corev1.RegisterAnnouncementServiceServer(srv, grpcapi.NewAnnouncementServer(announcements, rt.Log))
	corev1.RegisterEventServiceServer(srv, grpcapi.NewEventServer(events, rt.Log))
	corev1.RegisterQAServiceServer(srv, grpcapi.NewQAServer(questions, rt.Log))
	corev1.RegisterPollServiceServer(srv, grpcapi.NewPollServer(polls, rt.Log))
	corev1.RegisterSocialServiceServer(srv, grpcapi.NewSocialServer(socials, rt.Log))
	corev1.RegisterProfileServiceServer(srv, grpcapi.NewProfileServer(profiles, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	// Tekrarlayan etkinliklerin önümüzdeki 8 haftalık örnekleri (issue #51).
	// Birden fazla pod'da çalışabilir: tayfa ve seri satırı kilitlenir,
	// örnekler (series_id, starts_at) ile tekildir.
	rt.AddWorker("event-series", events.RunSeriesGenerator)
	// Pasif tayfa temizliği, 72 saatlik tayfa silme, 30 günlük kalıcı silme,
	// yürüyüş dönmeme alarmı ve plaka temizliği (issue #66). Her biri ayrı
	// bir worker; birden fazla pod'da her partiyi advisory kilidi alan tek
	// pod işler.
	for _, w := range background.Workers() {
		rt.AddWorker(w.Name, w.Run)
	}
	return addRelay(rt, pool)
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
	relay, err := outbox.NewRelay(outbox.RelayConfig{
		Pool: pool, Log: rt.Log,
		Consumers: []outbox.Consumer{chatrelay.Consumer(chatv1.NewCoreEventServiceClient(conn))},
	})
	if err != nil {
		return err
	}
	rt.AddWorker("outbox", relay.Run)
	return nil
}

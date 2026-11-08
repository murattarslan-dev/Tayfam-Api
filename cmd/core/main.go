// Command core owns tayfas, memberships, events, Q&A, polls, social graph and
// media (ADR-0002, ADR-0009).
//
//	core           runs the service
//	core migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"time"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/core/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
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
	memberships, err := membership.New(membership.Config{
		Pool:        pool,
		JoinLimiter: ratelimit.NewPer(joinCodeAttemptsPerHour, time.Hour),
	})
	if err != nil {
		return err
	}

	// Guard'a açık metod verilmez: her metod kullanıcının erişim token'ını
	// ister (ADR-0011).
	srv := rt.NewGRPCServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(srv, grpcapi.NewTayfaServer(tayfas, rt.Log))
	corev1.RegisterMembershipServiceServer(srv, grpcapi.NewMembershipServer(memberships, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	return nil
}

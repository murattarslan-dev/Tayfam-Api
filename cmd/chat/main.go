// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005, ADR-0009).
//
//	chat           runs the service
//	chat migrate   applies database migrations and exits (init container)
package main

import (
	"context"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

func main() {
	service.MainWithMigrations("chat", chatdb.Migrations(), setup)
}

func setup(rt *service.Runtime) error {
	c := rt.Config
	// Veritabanı parolası chat-db secret'ından (deploy/apps/<ortam>/secrets,
	// SOPS). chat yalnızca kendi veritabanına bağlanır (ADR-0002).
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
	// Servise özel metodlar sonraki iş paketlerinde bu sunucuya kaydedilir.
	srv := rt.NewGRPCServer(auth.NewGuard(v).ServerOptions()...)
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	return nil
}

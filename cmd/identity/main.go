// Command identity owns sign-in, sessions, tokens, KVKK requests and platform
// administration (ADR-0002, ADR-0004, ADR-0008).
package main

import (
	"log/slog"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/identity/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

func main() {
	service.Main("identity", func(rt *service.Runtime) error {
		// AUTH_SIGNING_KEY bir secret'tan gelir (deploy/apps/*/secrets, SOPS).
		// Emekli anahtarların yalnızca açık yarıları yapılandırılır.
		signing := rt.Config.Required("AUTH_SIGNING_KEY")
		retired := rt.Config.String("AUTH_RETIRED_KEYS", "")
		if err := rt.Config.Err(); err != nil {
			return err
		}
		ring, err := keys.Parse(signing, retired)
		if err != nil {
			return err
		}
		rt.Log.Info("imzalama anahtarları yüklendi",
			slog.String("active_kid", ring.Active().ID),
			slog.Int("public_keys", len(ring.PublicKeys())))

		// identity kendi token'larını kendi açık anahtarlarıyla doğrular.
		v, err := rt.NewVerifier(ring.Static())
		if err != nil {
			return err
		}
		srv := rt.NewGRPCServer(auth.NewGuard(v, grpcapi.PublicMethods...).ServerOptions()...)
		identityv1.RegisterKeyServiceServer(srv, grpcapi.NewKeyServer(ring))
		rt.AddGRPCServer("grpc", rt.Config.String("GRPC_ADDR", ":9090"), srv)
		return nil
	})
}

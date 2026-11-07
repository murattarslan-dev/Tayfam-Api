// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005).
package main

import (
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

func main() {
	service.Main("chat", func(rt *service.Runtime) error {
		// Her istekteki token, identity'nin açık anahtarlarıyla burada yeniden
		// doğrulanır (ADR-0011). Açık metod yoktur; health hariç her metod
		// geçerli token ister.
		v, err := rt.IdentityVerifier()
		if err != nil {
			return err
		}
		// Servise özel metodlar sonraki iş paketlerinde bu sunucuya kaydedilir.
		srv := rt.NewGRPCServer(auth.NewGuard(v).ServerOptions()...)
		rt.AddGRPCServer("grpc", rt.Config.String("GRPC_ADDR", ":9090"), srv)
		return nil
	})
}

// Command identity owns sign-in, sessions, tokens, KVKK requests and platform
// administration (ADR-0002, ADR-0004, ADR-0008).
package main

import "github.com/murattarslan-dev/tayfam-api/internal/platform/service"

func main() {
	service.Main("identity", func(rt *service.Runtime) error {
		// gRPC API'si: şimdilik yalnızca standart health servisi. Servise özel
		// metodlar sonraki iş paketlerinde bu sunucuya kaydedilir.
		rt.AddGRPCServer("grpc", rt.Config.String("GRPC_ADDR", ":9090"), rt.NewGRPCServer())
		return nil
	})
}

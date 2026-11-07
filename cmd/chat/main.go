// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005).
package main

import "github.com/murattarslan-dev/tayfam-api/internal/platform/service"

func main() {
	service.Main("chat", func(rt *service.Runtime) error {
		// gRPC API'si: şimdilik yalnızca standart health servisi. Servise özel
		// metodlar sonraki iş paketlerinde bu sunucuya kaydedilir.
		rt.AddGRPCServer("grpc", rt.Config.String("GRPC_ADDR", ":9090"), rt.NewGRPCServer())
		return nil
	})
}

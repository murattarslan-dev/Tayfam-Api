// Command chat owns channels, direct messages, real-time delivery and
// notifications (ADR-0005).
package main

import "github.com/murattarslan-dev/tayfam-api/internal/platform/service"

func main() {
	service.Main("chat", func(*service.Runtime) error {
		// The gRPC API, WebSocket server and database are added in the
		// following work packages.
		return nil
	})
}

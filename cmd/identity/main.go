// Command identity owns sign-in, sessions, tokens, KVKK requests and platform
// administration (ADR-0002, ADR-0004, ADR-0008).
package main

import "github.com/murattarslan-dev/tayfam-api/internal/platform/service"

func main() {
	service.Main("identity", func(*service.Runtime) error {
		// The gRPC API and database are added in the following work packages.
		return nil
	})
}

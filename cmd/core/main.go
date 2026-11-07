// Command core owns tayfas, memberships, events, Q&A, polls, social graph and
// media (ADR-0002, ADR-0009).
package main

import "github.com/murattarslan-dev/tayfam-api/internal/platform/service"

func main() {
	service.Main("core", func(*service.Runtime) error {
		// The gRPC API and database are added in the following work packages.
		return nil
	})
}

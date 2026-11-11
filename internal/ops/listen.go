// Package ops is Tayfam Ops: a small local web tool that runs the secret
// scripts (scripts/new-*.sh) for the platform owner without a terminal
// (issue #148).
//
// It runs only on the platform owner's computer (WSL), listens only on
// 127.0.0.1 and never makes an outbound network request. Plaintext values
// live only in memory and go to the scripts over stdin; the scripts hand them
// straight to sops (docs/runbooks/sops.md, ADR-0006). The tool does not push
// or open pull requests: it writes files under deploy/apps/<env>/ and opens
// that folder in Windows Explorer.
package ops

import (
	"context"
	"fmt"
	"net"
)

// Listen binds addr, which must be 127.0.0.1:<port> (port 0 picks a free
// one). Any other host (0.0.0.0, ::, a LAN address, a name) is refused so
// that the tool is never reachable from another machine.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host != "127.0.0.1" {
		return nil, fmt.Errorf("listen address %q: only 127.0.0.1 is allowed", addr)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("listen: %s is not a loopback address", ln.Addr())
	}
	return ln, nil
}

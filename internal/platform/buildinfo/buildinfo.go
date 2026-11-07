// Package buildinfo exposes the version stamped into binaries at build time.
package buildinfo

// Version is set with -ldflags "-X github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo.Version=…".
var Version = "dev"

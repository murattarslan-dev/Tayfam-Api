// Command gateway is Tayfam's only public entry point (ADR-0002).
package main

import (
	"net/http"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/gateway/transport/rest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/health"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

func main() {
	service.Main("gateway", setup)
}

func setup(rt *service.Runtime) error {
	httpAddr := rt.Config.String("HTTP_ADDR", ":8080")

	// Until the internal gRPC APIs exist, upstream availability is read from
	// each service's ops /readyz endpoint.
	client := &http.Client{Timeout: 3 * time.Second}
	upstreams := []rest.Upstream{
		{Name: "identity", Check: health.HTTPCheck(client, rt.Config.Required("IDENTITY_OPS_URL")+"/readyz")},
		{Name: "core", Check: health.HTTPCheck(client, rt.Config.Required("CORE_OPS_URL")+"/readyz")},
		{Name: "chat", Check: health.HTTPCheck(client, rt.Config.Required("CHAT_OPS_URL")+"/readyz")},
	}

	rt.AddHTTPServer("http", httpAddr, rest.NewRouter(rest.Deps{
		Version:   buildinfo.Version,
		Upstreams: upstreams,
	}))
	return nil
}

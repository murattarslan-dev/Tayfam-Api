// Command gateway is Tayfam's only public entry point (ADR-0002).
package main

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/transport/rest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

func main() {
	service.Main("gateway", setup)
}

func setup(rt *service.Runtime) error {
	httpAddr := rt.Config.String("HTTP_ADDR", ":8080")

	targets := []struct{ name, env string }{
		{"identity", "IDENTITY_GRPC_ADDR"},
		{"core", "CORE_GRPC_ADDR"},
		{"chat", "CHAT_GRPC_ADDR"},
	}
	upstreams := make([]rest.Upstream, 0, len(targets))
	conns := map[string]*grpc.ClientConn{}
	for _, t := range targets {
		addr := rt.Config.Required(t.env)
		if addr == "" {
			continue // the missing value is reported by the config check
		}
		conn, err := grpcx.NewClient(addr)
		if err != nil {
			return err
		}
		conns[t.name] = conn
		upstreams = append(upstreams, rest.Upstream{Name: t.name, Check: grpcx.HealthCheck(conn)})
	}

	// Web origins allowed to call the API with cookies (CORS), comma
	// separated. Local development adds its own origin through the env.
	origins := splitList(rt.Config.String("CORS_ALLOWED_ORIGINS", "https://tayfam.app"))
	// Cloudflare's client IP header is trusted only behind cloudflared.
	trustCF := rt.Config.OneOf("TRUST_CF_CONNECTING_IP", "false", "true", "false") == "true"
	if err := rt.Config.Err(); err != nil {
		return err
	}

	deps := rest.Deps{
		Version:        buildinfo.Version,
		Upstreams:      upstreams,
		AllowedOrigins: origins,
	}
	if conn := conns["identity"]; conn != nil {
		deps.Auth = rest.AuthDeps{
			Auth:                identityv1.NewAuthServiceClient(conn),
			Registration:        identityv1.NewRegistrationServiceClient(conn),
			Log:                 rt.Log,
			TrustCFConnectingIP: trustCF,
			// ADR-0004: IP limits only on low-volume actions (CGNAT).
			SignInLimit:  ratelimit.New(10, 5),
			RefreshLimit: ratelimit.New(60, 20),
		}
	}
	if conn := conns["core"]; conn != nil {
		// gateway checks the user's token before calling core, with
		// identity's public keys; core checks it again itself (ADR-0011).
		v, err := rt.IdentityVerifier()
		if err != nil {
			return err
		}
		deps.Core = rest.CoreDeps{
			Tayfas:              corev1.NewTayfaServiceClient(conn),
			Members:             corev1.NewMembershipServiceClient(conn),
			Announcements:       corev1.NewAnnouncementServiceClient(conn),
			Events:              corev1.NewEventServiceClient(conn),
			QA:                  corev1.NewQAServiceClient(conn),
			Polls:               corev1.NewPollServiceClient(conn),
			Social:              corev1.NewSocialServiceClient(conn),
			Invitations:         corev1.NewMembershipServiceClient(conn),
			Profiles:            corev1.NewProfileServiceClient(conn),
			Reports:             corev1.NewReportServiceClient(conn),
			Verifier:            v,
			Log:                 rt.Log,
			TrustCFConnectingIP: trustCF,
			// ADR-0004: opening a tayfa is low volume, one per IP per hour.
			CreateTayfaLimit: ratelimit.Every(time.Hour, 1),
			// Code guessing; core also limits attempts per user. Kept loose
			// enough for many users behind one CGNAT address.
			JoinCodeLimit: ratelimit.New(20, 10),
			InviteLimit:   ratelimit.New(60, 20),
			// Check-in at an event: many members may share one venue
			// network; core also allows five attempts per event and user.
			CheckInLimit: ratelimit.New(30, 30),
		}
	}
	// chat's WebSocket port; GET /v1/realtime is passed there (ADR-0005).
	var realtimeURL *url.URL
	if conns["chat"] != nil {
		if raw := rt.Config.Required("CHAT_REALTIME_URL"); raw != "" {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("CHAT_REALTIME_URL geçersiz")
			}
			realtimeURL = u
		}
		if err := rt.Config.Err(); err != nil {
			return err
		}
	}
	if conn := conns["chat"]; conn != nil {
		// Bildirim (issue #64), kanal (issue #81) ve birebir mesaj (issue #95) uçları: gateway token'ı
		// chat'e ulaşmadan doğrular; chat yeniden doğrular (ADR-0011).
		v, err := rt.IdentityVerifier()
		if err != nil {
			return err
		}
		deps.Chat = rest.ChatDeps{
			Notifications:  chatv1.NewNotificationServiceClient(conn),
			Channels:       chatv1.NewChannelServiceClient(conn),
			DirectMessages: chatv1.NewDirectMessageServiceClient(conn),
			RealtimeURL:    realtimeURL,
			Verifier:       v,
			Log:            rt.Log,
		}
	}
	// Account settings live in identity but take the same user token
	// check as core's routes (identity checks it again itself).
	if conn := conns["identity"]; conn != nil && deps.Core.Verifier != nil {
		deps.Core.Accounts = identityv1.NewAccountServiceClient(conn)
	}
	rt.AddHTTPServer("http", httpAddr, rest.NewRouter(deps))
	return nil
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

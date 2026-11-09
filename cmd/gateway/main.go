// Command gateway is Tayfam's only public entry point (ADR-0002).
package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/transport/rest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
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
	adminHost := rt.Config.String("ADMIN_HOST", "admin.tayfam.app")
	panel, err := adminRouter(rt, adminHost, conns["identity"], trustCF)
	if err != nil {
		return err
	}
	rt.AddHTTPServer("http", httpAddr, admin.Mux(rest.NewRouter(deps), adminHost, panel))
	return nil
}

// adminRouter builds the platform administration panel's router (ADR-0008,
// issue #104) from:
//
//	CF_ACCESS_TEAM_DOMAIN  Zero Trust team domain, <team>.cloudflareaccess.com
//	CF_ACCESS_AUD          AUD tag of the admin.tayfam.app Access application
//
// Neither is secret. Without both, there is no panel: it returns nil and
// the admin host answers 404, so the panel never runs without Cloudflare
// Access in front of it. One without the other stops the service.
func adminRouter(rt *service.Runtime, host string, identity *grpc.ClientConn, trustCF bool) (http.Handler, error) {
	team := rt.Config.String("CF_ACCESS_TEAM_DOMAIN", "")
	aud := rt.Config.String("CF_ACCESS_AUD", "")
	if team == "" && aud == "" {
		rt.Log.Info("Cloudflare Access yapılandırılmadı; yönetim paneli kapalı")
		return nil, nil
	}
	if team == "" || aud == "" {
		return nil, errors.New("CF_ACCESS_TEAM_DOMAIN ve CF_ACCESS_AUD birlikte verilmeli")
	}
	if identity == nil {
		return nil, errors.New("yönetim paneli IDENTITY_GRPC_ADDR gerektirir")
	}
	issuer := rt.Config.Required("AUTH_ISSUER")
	if err := rt.Config.Err(); err != nil {
		return nil, err
	}
	access, err := auth.NewAccessVerifier(auth.AccessVerifierConfig{
		TeamDomain: team, Audience: aud, Client: &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		return nil, err
	}
	// Admin tokens are checked with identity's public keys, by a verifier
	// that accepts nothing but admin tokens.
	adminV, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{
		Keys:   auth.NewRemoteKeys(auth.FetchFromIdentity(identityv1.NewKeyServiceClient(identity))),
		Issuer: issuer,
	})
	if err != nil {
		return nil, err
	}
	return admin.NewHandler(admin.Config{
		Host: host, Access: access, Admin: adminV,
		Identity:            identityv1.NewAdminAuthServiceClient(identity),
		Log:                 rt.Log,
		TrustCFConnectingIP: trustCF,
		// One person signs in a few times a day.
		SignInLimit: ratelimit.New(5, 5),
	})
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

// Command gateway is Tayfam's only public entry point (ADR-0002).
package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/transport/rest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
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

	// The app ships on Android and iOS only; its web build is a local
	// development aid and is served everywhere but prod (ADR-0017).
	webClient := rt.Env != "prod"
	origins, err := devOrigins(rt.Env, rt.Config.String("WEB_DEV_ORIGINS", ""))
	if err != nil {
		return err
	}
	// Cloudflare's client IP header is trusted only behind cloudflared.
	trustCF := rt.Config.OneOf("TRUST_CF_CONNECTING_IP", "false", "true", "false") == "true"
	if err := rt.Config.Err(); err != nil {
		return err
	}
	versions, err := clientVersions(rt.Config, webClient)
	if err != nil {
		return err
	}

	deps := rest.Deps{
		Version:    buildinfo.Version,
		Upstreams:  upstreams,
		DevOrigins: origins,
		WebClient:  webClient,
		Client: rest.ClientDeps{
			Versions: versions,
			// One call per app start; loose for many users behind one
			// CGNAT address (ADR-0004).
			ConfigLimit:         ratelimit.New(60, 30),
			TrustCFConnectingIP: trustCF,
		},
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
			Media:               corev1.NewMediaServiceClient(conn),
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
			// Uçtan uca şifreleme (issue #127, ADR-0018).
			E2E: chatv1.NewE2EServiceClient(conn),
			// Push token'ı (issue #134, ADR-0019).
			Push:        chatv1.NewPushServiceClient(conn),
			RealtimeURL: realtimeURL,
			Verifier:    v,
			Log:         rt.Log,
		}
	}
	// Account settings live in identity but take the same user token
	// check as core's routes (identity checks it again itself).
	if conn := conns["identity"]; conn != nil && deps.Core.Verifier != nil {
		deps.Core.Accounts = identityv1.NewAccountServiceClient(conn)
		deps.Core.Avatars = identityv1.NewAccountServiceClient(conn)
	}
	adminHost := rt.Config.String("ADMIN_HOST", "admin.tayfam.app")
	panel, err := adminRouter(rt, adminHost, conns["identity"], conns["core"], trustCF)
	if err != nil {
		return err
	}
	rt.AddHTTPServer("http", httpAddr, admin.Mux(rest.NewRouter(deps), adminHost, panel))
	return nil
}

// adminRouter builds the platform administration panel's router (ADR-0008,
// issue #104) from:
//
//	CF_ACCESS_TEAM_DOMAIN   Zero Trust team domain, <team>.cloudflareaccess.com
//	CF_ACCESS_AUD           AUD tag of the admin.tayfam.app Access application
//	ADMIN_GOOGLE_CLIENT_ID  Google OAuth client of the panel's sign-in; one of
//	                        identity's GOOGLE_CLIENT_IDS (required with the two above)
//
// None is secret. Without the Access pair, there is no panel: it returns
// nil and the admin host answers 404, so the panel never runs without
// Cloudflare Access in front of it. One without the other stops the
// service.
func adminRouter(rt *service.Runtime, host string, identity, core *grpc.ClientConn, trustCF bool) (http.Handler, error) {
	team := rt.Config.String("CF_ACCESS_TEAM_DOMAIN", "")
	aud := rt.Config.String("CF_ACCESS_AUD", "")
	if team == "" && aud == "" {
		rt.Log.Info("Cloudflare Access yapılandırılmadı; yönetim paneli kapalı")
		return nil, nil
	}
	if team == "" || aud == "" {
		return nil, errors.New("CF_ACCESS_TEAM_DOMAIN ve CF_ACCESS_AUD birlikte verilmeli")
	}
	if identity == nil || core == nil {
		return nil, errors.New("yönetim paneli IDENTITY_GRPC_ADDR ve CORE_GRPC_ADDR gerektirir")
	}
	issuer := rt.Config.Required("AUTH_ISSUER")
	googleClient := rt.Config.Required("ADMIN_GOOGLE_CLIENT_ID")
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
		Core:                corev1.NewAdminServiceClient(core),
		Users:               identityv1.NewAdminUserServiceClient(identity),
		Audit:               identityv1.NewAdminAuditServiceClient(identity),
		GoogleClientID:      googleClient,
		Log:                 rt.Log,
		TrustCFConnectingIP: trustCF,
		// One person signs in a few times a day.
		SignInLimit: ratelimit.New(5, 5),
	})
}

// clientVersions reads each platform's supported builds (issue #114,
// ADR-0016):
//
//	CLIENT_MIN_VERSION_<PLATFORM>     lowest build served; unset turns the
//	                                  check off for that platform
//	CLIENT_LATEST_VERSION_<PLATFORM>  newest build, reported to clients;
//	                                  unset takes the minimum
//
// for ANDROID, IOS and WEB. A value that is not a semantic version, or a
// latest below the minimum, stops the service. Without the web build
// (prod, ADR-0017) a WEB value stops it too: it would configure a platform
// that is not served.
func clientVersions(c *config.Loader, webClient bool) (clientversion.Policy, error) {
	raw := map[clientversion.Platform]clientversion.RawRange{}
	for _, p := range clientversion.Platforms() {
		suffix := strings.ToUpper(string(p))
		rr := clientversion.RawRange{
			Min:    c.String("CLIENT_MIN_VERSION_"+suffix, ""),
			Latest: c.String("CLIENT_LATEST_VERSION_"+suffix, ""),
		}
		if p == clientversion.Web && !webClient {
			if rr != (clientversion.RawRange{}) {
				return clientversion.Policy{}, errors.New("istemci sürümleri: prod'da web platformu yok; CLIENT_MIN_VERSION_WEB ve CLIENT_LATEST_VERSION_WEB boş olmalı (ADR-0017)")
			}
			continue
		}
		raw[p] = rr
	}
	p, err := clientversion.NewPolicy(raw)
	if err != nil {
		return clientversion.Policy{}, fmt.Errorf("istemci sürümleri: %w", err)
	}
	return p, nil
}

// devOrigins reads WEB_DEV_ORIGINS, the comma-separated origins the
// local web build of the app is served from (ADR-0017). Each must be
// exactly http://localhost:<port> or http://127.0.0.1:<port>, as a browser
// writes the Origin header: a remote origin, https, a path or a missing
// port stops the service rather than open CORS to it. prod serves no web
// build, so there the list must be empty.
func devOrigins(env, raw string) ([]string, error) {
	list := splitList(raw)
	if env == "prod" && len(list) > 0 {
		return nil, errors.New("WEB_DEV_ORIGINS prod'da boş olmalı (ADR-0017)")
	}
	for _, o := range list {
		if !isLocalOrigin(o) {
			return nil, fmt.Errorf("WEB_DEV_ORIGINS: %q yerel geliştirme kaynağı değil; yalnızca http://localhost:<port> ya da http://127.0.0.1:<port> (ADR-0017)", o)
		}
	}
	return list, nil
}

func isLocalOrigin(o string) bool {
	rest, ok := strings.CutPrefix(o, "http://")
	if !ok {
		return false
	}
	host, port, ok := strings.Cut(rest, ":")
	if !ok || (host != "localhost" && host != "127.0.0.1") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
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

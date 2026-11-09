package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// TayfaClient is the part of core's TayfaService the gateway uses.
type TayfaClient interface {
	CreateTayfa(ctx context.Context, in *corev1.CreateTayfaRequest, opts ...grpc.CallOption) (*corev1.CreateTayfaResponse, error)
	GetTayfa(ctx context.Context, in *corev1.GetTayfaRequest, opts ...grpc.CallOption) (*corev1.GetTayfaResponse, error)
	ListMyTayfas(ctx context.Context, in *corev1.ListMyTayfasRequest, opts ...grpc.CallOption) (*corev1.ListMyTayfasResponse, error)
	UpdateTayfa(ctx context.Context, in *corev1.UpdateTayfaRequest, opts ...grpc.CallOption) (*corev1.UpdateTayfaResponse, error)
	SetModule(ctx context.Context, in *corev1.SetModuleRequest, opts ...grpc.CallOption) (*corev1.SetModuleResponse, error)
	RotateJoinCode(ctx context.Context, in *corev1.RotateJoinCodeRequest, opts ...grpc.CallOption) (*corev1.RotateJoinCodeResponse, error)
	RequestDeletion(ctx context.Context, in *corev1.RequestDeletionRequest, opts ...grpc.CallOption) (*corev1.RequestDeletionResponse, error)
	CancelDeletion(ctx context.Context, in *corev1.CancelDeletionRequest, opts ...grpc.CallOption) (*corev1.CancelDeletionResponse, error)
}

// MembershipClient is the part of core's MembershipService the gateway
// uses. GetMembership is left out: it exists for chat, not for clients.
type MembershipClient interface {
	JoinByCode(ctx context.Context, in *corev1.JoinByCodeRequest, opts ...grpc.CallOption) (*corev1.JoinByCodeResponse, error)
	CreateInviteLink(ctx context.Context, in *corev1.CreateInviteLinkRequest, opts ...grpc.CallOption) (*corev1.CreateInviteLinkResponse, error)
	ListInviteLinks(ctx context.Context, in *corev1.ListInviteLinksRequest, opts ...grpc.CallOption) (*corev1.ListInviteLinksResponse, error)
	RevokeInviteLink(ctx context.Context, in *corev1.RevokeInviteLinkRequest, opts ...grpc.CallOption) (*corev1.RevokeInviteLinkResponse, error)
	PreviewInvite(ctx context.Context, in *corev1.PreviewInviteRequest, opts ...grpc.CallOption) (*corev1.PreviewInviteResponse, error)
	JoinByInvite(ctx context.Context, in *corev1.JoinByInviteRequest, opts ...grpc.CallOption) (*corev1.JoinByInviteResponse, error)
	ListMembers(ctx context.Context, in *corev1.ListMembersRequest, opts ...grpc.CallOption) (*corev1.ListMembersResponse, error)
	ListPending(ctx context.Context, in *corev1.ListPendingRequest, opts ...grpc.CallOption) (*corev1.ListPendingResponse, error)
	ApproveMember(ctx context.Context, in *corev1.ApproveMemberRequest, opts ...grpc.CallOption) (*corev1.ApproveMemberResponse, error)
	RejectMember(ctx context.Context, in *corev1.RejectMemberRequest, opts ...grpc.CallOption) (*corev1.RejectMemberResponse, error)
	ChangeRole(ctx context.Context, in *corev1.ChangeRoleRequest, opts ...grpc.CallOption) (*corev1.ChangeRoleResponse, error)
	MuteMember(ctx context.Context, in *corev1.MuteMemberRequest, opts ...grpc.CallOption) (*corev1.MuteMemberResponse, error)
	UnmuteMember(ctx context.Context, in *corev1.UnmuteMemberRequest, opts ...grpc.CallOption) (*corev1.UnmuteMemberResponse, error)
	BanMember(ctx context.Context, in *corev1.BanMemberRequest, opts ...grpc.CallOption) (*corev1.BanMemberResponse, error)
	RemoveMember(ctx context.Context, in *corev1.RemoveMemberRequest, opts ...grpc.CallOption) (*corev1.RemoveMemberResponse, error)
	LeaveTayfa(ctx context.Context, in *corev1.LeaveTayfaRequest, opts ...grpc.CallOption) (*corev1.LeaveTayfaResponse, error)
}

// TokenVerifier checks access tokens (auth.Verifier).
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (auth.Claims, error)
}

// CoreDeps configures the tayfa and membership routes.
type CoreDeps struct {
	Tayfas  TayfaClient
	Members MembershipClient
	// Announcements enables the announcement routes when set.
	Announcements AnnouncementClient
	// Events enables the event routes when set.
	Events EventClient
	// QA enables the question and answer routes when set.
	QA QAClient
	// Polls enables the poll routes when set.
	Polls PollClient
	// Verifier checks the user's token before anything reaches core. core
	// checks it again itself (ADR-0011); this only keeps junk out early.
	Verifier TokenVerifier
	Log      *slog.Logger
	// TrustCFConnectingIP: see AuthDeps.
	TrustCFConnectingIP bool
	// CreateTayfaLimit is per client IP (ADR-0004: one per hour).
	CreateTayfaLimit *ratelimit.Limiter
	// JoinCodeLimit is per client IP on join code attempts; core also limits
	// attempts per user.
	JoinCodeLimit *ratelimit.Limiter
	// InviteLimit is per client IP on invite previews and invite joins.
	InviteLimit *ratelimit.Limiter
	// CheckInLimit is per client IP on event check-ins; core also limits
	// attempts per event and user. Required with Events.
	CheckInLimit *ratelimit.Limiter
	Now          func() time.Time
}

const (
	maxAccessTokenBytes = 4096
	maxPageToken        = 1024
	maxJoinCode         = 32
	maxInviteToken      = 64
	createRetryWindow   = time.Hour
)

type coreHandlers struct {
	d       CoreDeps
	retries *createRetries
}

func registerCore(mux *http.ServeMux, d CoreDeps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	h := coreHandlers{d: d, retries: newCreateRetries(d.Now)}
	a := h.authed
	mux.HandleFunc("GET /v1/tayfas", a(h.listMyTayfas))
	mux.HandleFunc("POST /v1/tayfas", a(h.createTayfa))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}", a(h.getTayfa))
	mux.HandleFunc("PATCH /v1/tayfas/{tayfa_id}", a(h.updateTayfa))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/modules/{module}", a(h.setModule))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/join-code", a(h.rotateJoinCode))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/deletion", a(h.requestDeletion))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/deletion", a(h.cancelDeletion))

	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/membership", a(h.leave))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/members", a(h.listMembers))
	mux.HandleFunc("PATCH /v1/tayfas/{tayfa_id}/members/{user_id}", a(h.changeRole))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/members/{user_id}", a(h.removeMember))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/members/{user_id}/mute", a(h.mute))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/members/{user_id}/mute", a(h.unmute))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/members/{user_id}/ban", a(h.ban))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/join-requests", a(h.listJoinRequests))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/join-requests/{user_id}", a(h.rejectJoinRequest))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/join-requests/{user_id}/approval", a(h.approveJoinRequest))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/invites", a(h.listInvites))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/invites", a(h.createInvite))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/invites/{invite_id}", a(h.revokeInvite))
	mux.HandleFunc("POST /v1/tayfa-joins", a(h.joinByCode))
	mux.HandleFunc("POST /v1/invite-previews", a(h.previewInvite))
	mux.HandleFunc("POST /v1/invite-joins", a(h.joinByInvite))
	if d.Announcements != nil {
		registerAnnouncements(mux, h)
	}
	if d.Events != nil {
		registerEvents(mux, h)
	}
	if d.QA != nil {
		registerQA(mux, h)
	}
	if d.Polls != nil {
		registerPolls(mux, h)
	}
}

// coreHandler gets a context that carries the user's verified token for
// core and is bounded by upstreamWait.
type coreHandler func(ctx context.Context, w http.ResponseWriter, r *http.Request)

// authed verifies the access token (Bearer or the web cookie) and passes
// it on to core unchanged (auth.WithToken). core verifies it again.
func (h coreHandlers) authed(next coreHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, _ := bearer(r)
		if token == "" || len(token) > maxAccessTokenBytes {
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
		defer cancel()
		if _, err := h.d.Verifier.Verify(ctx, token); err != nil {
			if errors.Is(err, auth.ErrInvalidToken) {
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
				return
			}
			// The key source failed (identity unreachable); not the user's fault.
			h.d.Log.WarnContext(r.Context(), "token doğrulanamadı: anahtar alınamadı")
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
			return
		}
		next(auth.WithToken(ctx, token), w, r)
	}
}

func (h coreHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	writeUpstreamError(w, r, h.d.Log, "core", err)
}

func badRequest(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
}

func rateLimited(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
}

// --- Tayfa ---

type tayfaCreateBody struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Interests       []string `json:"interests"`
	JoinPolicy      string   `json:"join_policy"`
	ClientRequestID string   `json:"client_request_id"`
}

func (h coreHandlers) createTayfa(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b tayfaCreateBody
	if !decode(w, r, &b) {
		return
	}
	interests, ok := eventTypes.values(b.Interests)
	policy, okP := joinPolicies.valueOrUnspecified(b.JoinPolicy)
	if !ok || !okP || !validUUID(b.ClientRequestID) || !nameLengthOK(b.Name) ||
		utf8.RuneCountInString(strings.TrimSpace(b.Description)) > 500 {
		badRequest(w, r)
		return
	}
	// The IP limit comes after the cheap checks so that a typo does not
	// cost the hour; a retry of the same request passes (core dedups it).
	ip := clientIP(r, h.d.TrustCFConnectingIP)
	if !h.d.CreateTayfaLimit.Allow(ip) && !h.retries.isRetry(ip, b.ClientRequestID) {
		rateLimited(w, r)
		return
	}
	h.retries.remember(ip, b.ClientRequestID)
	resp, err := h.d.Tayfas.CreateTayfa(ctx, &corev1.CreateTayfaRequest{
		Name: b.Name, Description: b.Description, Interests: interests,
		JoinPolicy: policy, ClientRequestId: b.ClientRequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	t := tayfaFrom(resp.GetTayfa())
	w.Header().Set("Location", "/v1/tayfas/"+t.ID)
	writeJSON(w, http.StatusCreated, t)
}

type tayfaViewJSON struct {
	Tayfa      tayfaJSON `json:"tayfa"`
	CallerRole string    `json:"caller_role"`
}

func (h coreHandlers) getTayfa(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Tayfas.GetTayfa(ctx, &corev1.GetTayfaRequest{TayfaId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tayfaViewJSON{tayfaFrom(resp.GetTayfa()), roles.name(resp.GetCallerRole())})
}

type myTayfaJSON struct {
	TayfaID string     `json:"tayfa_id"`
	Name    string     `json:"name"`
	Role    string     `json:"role"`
	Status  string     `json:"status"`
	Tayfa   *tayfaJSON `json:"tayfa,omitempty"`
}

type myTayfaListJSON struct {
	Tayfas        []myTayfaJSON `json:"tayfas"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

func (h coreHandlers) listMyTayfas(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Tayfas.ListMyTayfas(ctx, &corev1.ListMyTayfasRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := myTayfaListJSON{Tayfas: make([]myTayfaJSON, 0, len(resp.GetTayfas())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetTayfas() {
		e := myTayfaJSON{TayfaID: m.GetTayfaId(), Name: m.GetName(), Role: roles.name(m.GetRole()), Status: membershipStatuses.name(m.GetStatus())}
		if m.GetTayfa() != nil {
			t := tayfaFrom(m.GetTayfa())
			e.Tayfa = &t
		}
		out.Tayfas = append(out.Tayfas, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// tayfaUpdateBody: a field present in the body is changed; null counts as
// absent.
type tayfaUpdateBody struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Interests   *[]string `json:"interests"`
	JoinPolicy  *string   `json:"join_policy"`
	// MembersCanCreateEvents lets every active member create events.
	MembersCanCreateEvents *bool `json:"members_can_create_events"`
}

func (h coreHandlers) updateTayfa(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b tayfaUpdateBody
	if !decode(w, r, &b) {
		return
	}
	req := &corev1.UpdateTayfaRequest{TayfaId: id, UpdateMask: &fieldmaskpb.FieldMask{}}
	if b.Name != nil {
		if !nameLengthOK(*b.Name) {
			badRequest(w, r)
			return
		}
		req.Name = *b.Name
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "name")
	}
	if b.Description != nil {
		req.Description = *b.Description
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "description")
	}
	if b.Interests != nil {
		v, ok := eventTypes.values(*b.Interests)
		if !ok {
			badRequest(w, r)
			return
		}
		req.Interests = v
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "interests")
	}
	if b.JoinPolicy != nil {
		v, ok := joinPolicies.value(*b.JoinPolicy)
		if !ok {
			badRequest(w, r)
			return
		}
		req.JoinPolicy = v
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "join_policy")
	}
	if b.MembersCanCreateEvents != nil {
		req.MembersCanCreateEvents = *b.MembersCanCreateEvents
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "members_can_create_events")
	}
	if len(req.UpdateMask.Paths) == 0 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Tayfas.UpdateTayfa(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tayfaFrom(resp.GetTayfa()))
}

type moduleUpdateBody struct {
	Enabled *bool `json:"enabled"`
}

func (h coreHandlers) setModule(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	module, ok := modules.value(r.PathValue("module"))
	if !ok {
		badRequest(w, r)
		return
	}
	var b moduleUpdateBody
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		badRequest(w, r)
		return
	}
	if _, err := h.d.Tayfas.SetModule(ctx, &corev1.SetModuleRequest{TayfaId: id, Module: module, Enabled: *b.Enabled}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) rotateJoinCode(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Tayfas.RotateJoinCode(ctx, &corev1.RotateJoinCodeRequest{TayfaId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		JoinCode string `json:"join_code"`
	}{resp.GetJoinCode()})
}

func (h coreHandlers) requestDeletion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Tayfas.RequestDeletion(ctx, &corev1.RequestDeletionRequest{TayfaId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		DeletionAt time.Time `json:"deletion_at"`
	}{resp.GetDeletionTime().AsTime()})
}

func (h coreHandlers) cancelDeletion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	if _, err := h.d.Tayfas.CancelDeletion(ctx, &corev1.CancelDeletionRequest{TayfaId: id}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Membership ---

func (h coreHandlers) leave(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	if _, err := h.d.Members.LeaveTayfa(ctx, &corev1.LeaveTayfaRequest{TayfaId: id}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type memberListJSON struct {
	Members       []membershipJSON `json:"members"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

func (h coreHandlers) listMembers(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	st := corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE
	switch r.URL.Query().Get("status") {
	case "", "active":
	case "banned":
		st = corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED
	default:
		badRequest(w, r)
		return
	}
	resp, err := h.d.Members.ListMembers(ctx, &corev1.ListMembersRequest{TayfaId: id, Status: st, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := memberListJSON{Members: make([]membershipJSON, 0, len(resp.GetMembers())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetMembers() {
		out.Members = append(out.Members, membershipFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

type roleUpdateBody struct {
	Role string `json:"role"`
}

func (h coreHandlers) changeRole(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	var b roleUpdateBody
	if !decode(w, r, &b) {
		return
	}
	role, ok := roles.value(b.Role)
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Members.ChangeRole(ctx, &corev1.ChangeRoleRequest{TayfaId: id, UserId: user, NewRole: role})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipFrom(resp.GetMembership()))
}

func (h coreHandlers) removeMember(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Members.RemoveMember(ctx, &corev1.RemoveMemberRequest{TayfaId: id, UserId: user}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type muteBody struct {
	MutedUntil time.Time `json:"muted_until"`
}

func (h coreHandlers) mute(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	var b muteBody
	if !decode(w, r, &b) {
		return
	}
	if b.MutedUntil.IsZero() {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Members.MuteMember(ctx, &corev1.MuteMemberRequest{TayfaId: id, UserId: user, ExpireTime: timestamppb.New(b.MutedUntil)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipFrom(resp.GetMembership()))
}

func (h coreHandlers) unmute(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Members.UnmuteMember(ctx, &corev1.UnmuteMemberRequest{TayfaId: id, UserId: user})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipFrom(resp.GetMembership()))
}

func (h coreHandlers) ban(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Members.BanMember(ctx, &corev1.BanMemberRequest{TayfaId: id, UserId: user}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type joinRequestJSON struct {
	UserID        string    `json:"user_id"`
	RequestedAt   time.Time `json:"requested_at"`
	Source        string    `json:"source"`
	InviterUserID string    `json:"inviter_user_id,omitempty"`
	InviteID      string    `json:"invite_id,omitempty"`
}

type joinRequestListJSON struct {
	JoinRequests  []joinRequestJSON `json:"join_requests"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

func (h coreHandlers) listJoinRequests(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Members.ListPending(ctx, &corev1.ListPendingRequest{TayfaId: id, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := joinRequestListJSON{JoinRequests: make([]joinRequestJSON, 0, len(resp.GetPending())), NextPageToken: resp.GetNextPageToken()}
	for _, p := range resp.GetPending() {
		out.JoinRequests = append(out.JoinRequests, joinRequestJSON{
			UserID: p.GetMembership().GetUserId(), RequestedAt: p.GetRequestTime().AsTime(),
			Source: joinSources.name(p.GetSource()), InviterUserID: p.GetInviterUserId(), InviteID: p.GetInviteId(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) approveJoinRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Members.ApproveMember(ctx, &corev1.ApproveMemberRequest{TayfaId: id, UserId: user})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipFrom(resp.GetMembership()))
}

func (h coreHandlers) rejectJoinRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, user, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Members.RejectMember(ctx, &corev1.RejectMemberRequest{TayfaId: id, UserId: user}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Invites ---

type inviteCreateBody struct {
	TTLSeconds int64 `json:"ttl_seconds"`
	MaxUses    int32 `json:"max_uses"`
}

type createdInviteJSON struct {
	Invite inviteJSON `json:"invite"`
	Token  string     `json:"token"`
}

type inviteListJSON struct {
	Invites []inviteJSON `json:"invites"`
}

func (h coreHandlers) createInvite(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b inviteCreateBody
	if !decode(w, r, &b) {
		return
	}
	ttl := time.Duration(b.TTLSeconds) * time.Second
	if ttl < time.Hour || ttl > 30*24*time.Hour || b.MaxUses < 1 || b.MaxUses > 100 {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Members.CreateInviteLink(ctx, &corev1.CreateInviteLinkRequest{TayfaId: id, Ttl: durationpb.New(ttl), MaxUses: b.MaxUses})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, createdInviteJSON{inviteFrom(resp.GetInvite()), resp.GetToken()})
}

func (h coreHandlers) listInvites(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Members.ListInviteLinks(ctx, &corev1.ListInviteLinksRequest{TayfaId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := inviteListJSON{Invites: make([]inviteJSON, 0, len(resp.GetInvites()))}
	for _, i := range resp.GetInvites() {
		out.Invites = append(out.Invites, inviteFrom(i))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) revokeInvite(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	invite, ok := pathUUID(w, r, "invite_id")
	if !ok {
		return
	}
	if _, err := h.d.Members.RevokeInviteLink(ctx, &corev1.RevokeInviteLinkRequest{TayfaId: id, InviteId: invite}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Joining (code and invite token travel in the body, never the URL) ---

type joinByCodeBody struct {
	Code string `json:"code"`
}

func (h coreHandlers) joinByCode(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Limited before the body is read, so malformed guesses count too.
	if !h.d.JoinCodeLimit.Allow(clientIP(r, h.d.TrustCFConnectingIP)) {
		rateLimited(w, r)
		return
	}
	var b joinByCodeBody
	if !decode(w, r, &b) {
		return
	}
	if b.Code == "" || len(b.Code) > maxJoinCode {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Members.JoinByCode(ctx, &corev1.JoinByCodeRequest{Code: b.Code})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipFrom(resp.GetMembership()))
}

type inviteTokenBody struct {
	Token string `json:"token"`
}

// inviteToken applies the IP limit and reads the token from the body.
func (h coreHandlers) inviteToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !h.d.InviteLimit.Allow(clientIP(r, h.d.TrustCFConnectingIP)) {
		rateLimited(w, r)
		return "", false
	}
	var b inviteTokenBody
	if !decode(w, r, &b) {
		return "", false
	}
	if b.Token == "" || len(b.Token) > maxInviteToken {
		badRequest(w, r)
		return "", false
	}
	return b.Token, true
}

type invitePreviewJSON struct {
	TayfaID       string `json:"tayfa_id"`
	Name          string `json:"name"`
	MemberCount   int32  `json:"member_count"`
	AlreadyMember bool   `json:"already_member"`
}

func (h coreHandlers) previewInvite(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	token, ok := h.inviteToken(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Members.PreviewInvite(ctx, &corev1.PreviewInviteRequest{Token: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitePreviewJSON{resp.GetTayfaId(), resp.GetName(), resp.GetMemberCount(), resp.GetAlreadyMember()})
}

func (h coreHandlers) joinByInvite(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	token, ok := h.inviteToken(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Members.JoinByInvite(ctx, &corev1.JoinByInviteRequest{Token: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipFrom(resp.GetMembership()))
}

// --- Input helpers ---

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(s string) bool { return uuidPattern.MatchString(s) }

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v := r.PathValue(name)
	if !validUUID(v) {
		badRequest(w, r)
		return "", false
	}
	return v, true
}

func pathTayfaUser(w http.ResponseWriter, r *http.Request) (tayfaID, userID string, ok bool) {
	if tayfaID, ok = pathUUID(w, r, "tayfa_id"); !ok {
		return "", "", false
	}
	if userID, ok = pathUUID(w, r, "user_id"); !ok {
		return "", "", false
	}
	return tayfaID, userID, true
}

// paging reads page_size and page_token. The token is passed to core as is;
// only its length is checked here.
func paging(w http.ResponseWriter, r *http.Request) (int32, string, bool) {
	q := r.URL.Query()
	var size int32
	if s := q.Get("page_size"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 0 || n > 100 {
			badRequest(w, r)
			return 0, "", false
		}
		size = int32(n)
	}
	token := q.Get("page_token")
	if len(token) > maxPageToken {
		badRequest(w, r)
		return 0, "", false
	}
	return size, token, true
}

// nameLengthOK mirrors core's length rule for tayfa names (3–50 characters
// after trimming). core checks the rest of the rules.
func nameLengthOK(s string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 3 && n <= 50
}

// createRetries remembers, per client IP, the client_request_id of the
// last tayfa creation for an hour, so that an offline retry of the same
// request is not refused by the IP limit. core returns the first result for
// it and creates nothing.
type createRetries struct {
	now func() time.Time

	mu      sync.Mutex
	last    map[string]createAttempt
	maxKeys int
}

type createAttempt struct {
	requestID string
	at        time.Time
}

func newCreateRetries(now func() time.Time) *createRetries {
	return &createRetries{now: now, last: map[string]createAttempt{}, maxKeys: 100_000}
}

func (c *createRetries) isRetry(ip, requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.last[ip]
	return ok && a.requestID == requestID && c.now().Sub(a.at) < createRetryWindow
}

func (c *createRetries) remember(ip, requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if a, ok := c.last[ip]; ok && a.requestID == requestID && now.Sub(a.at) < createRetryWindow {
		return // keep the first time, so retries cannot extend the window
	}
	if len(c.last) >= c.maxKeys {
		for k, a := range c.last {
			if now.Sub(a.at) >= createRetryWindow {
				delete(c.last, k)
			}
		}
		if len(c.last) >= c.maxKeys {
			return // full: retries from new IPs fall back to the plain limit
		}
	}
	c.last[ip] = createAttempt{requestID, now}
}

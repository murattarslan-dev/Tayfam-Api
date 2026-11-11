package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// Friends, blocks, direct invitations, profiles and the caller's account
// settings (issue #71). Every route needs the user's full access token
// (coreHandlers.authed); core and identity check it again and take the
// caller from it, so no route names the acting user. Referral codes travel
// in the body only, never in the path or query, so access logs cannot hold
// them.

// SocialClient is core's SocialService.
type SocialClient interface {
	SendFriendRequest(ctx context.Context, in *corev1.SendFriendRequestRequest, opts ...grpc.CallOption) (*corev1.SendFriendRequestResponse, error)
	AcceptFriendRequest(ctx context.Context, in *corev1.AcceptFriendRequestRequest, opts ...grpc.CallOption) (*corev1.AcceptFriendRequestResponse, error)
	RemoveFriendship(ctx context.Context, in *corev1.RemoveFriendshipRequest, opts ...grpc.CallOption) (*corev1.RemoveFriendshipResponse, error)
	ListFriends(ctx context.Context, in *corev1.ListFriendsRequest, opts ...grpc.CallOption) (*corev1.ListFriendsResponse, error)
	ListFriendRequests(ctx context.Context, in *corev1.ListFriendRequestsRequest, opts ...grpc.CallOption) (*corev1.ListFriendRequestsResponse, error)
	BlockUser(ctx context.Context, in *corev1.BlockUserRequest, opts ...grpc.CallOption) (*corev1.BlockUserResponse, error)
	UnblockUser(ctx context.Context, in *corev1.UnblockUserRequest, opts ...grpc.CallOption) (*corev1.UnblockUserResponse, error)
	ListBlockedUsers(ctx context.Context, in *corev1.ListBlockedUsersRequest, opts ...grpc.CallOption) (*corev1.ListBlockedUsersResponse, error)
}

// InvitationClient is the direct invitation part of core's
// MembershipService.
type InvitationClient interface {
	InviteFriend(ctx context.Context, in *corev1.InviteFriendRequest, opts ...grpc.CallOption) (*corev1.InviteFriendResponse, error)
	ListMyInvitations(ctx context.Context, in *corev1.ListMyInvitationsRequest, opts ...grpc.CallOption) (*corev1.ListMyInvitationsResponse, error)
	AcceptInvitation(ctx context.Context, in *corev1.AcceptInvitationRequest, opts ...grpc.CallOption) (*corev1.AcceptInvitationResponse, error)
	DeclineInvitation(ctx context.Context, in *corev1.DeclineInvitationRequest, opts ...grpc.CallOption) (*corev1.DeclineInvitationResponse, error)
	ListInvitations(ctx context.Context, in *corev1.ListInvitationsRequest, opts ...grpc.CallOption) (*corev1.ListInvitationsResponse, error)
	CancelInvitation(ctx context.Context, in *corev1.CancelInvitationRequest, opts ...grpc.CallOption) (*corev1.CancelInvitationResponse, error)
}

// ProfileClient is core's ProfileService.
type ProfileClient interface {
	GetUserProfile(ctx context.Context, in *corev1.GetUserProfileRequest, opts ...grpc.CallOption) (*corev1.GetUserProfileResponse, error)
	ListUserActivity(ctx context.Context, in *corev1.ListUserActivityRequest, opts ...grpc.CallOption) (*corev1.ListUserActivityResponse, error)
}

// AccountClient is identity's AccountService.
type AccountClient interface {
	GetAccount(ctx context.Context, in *identityv1.GetAccountRequest, opts ...grpc.CallOption) (*identityv1.GetAccountResponse, error)
	RotateReferralCode(ctx context.Context, in *identityv1.RotateReferralCodeRequest, opts ...grpc.CallOption) (*identityv1.RotateReferralCodeResponse, error)
	FindUserByReferralCode(ctx context.Context, in *identityv1.FindUserByReferralCodeRequest, opts ...grpc.CallOption) (*identityv1.FindUserByReferralCodeResponse, error)
	SetActivityVisibility(ctx context.Context, in *identityv1.SetActivityVisibilityRequest, opts ...grpc.CallOption) (*identityv1.SetActivityVisibilityResponse, error)
	// Sessions, consents and account deletion (issue #75, account.go).
	ListSessions(ctx context.Context, in *identityv1.ListSessionsRequest, opts ...grpc.CallOption) (*identityv1.ListSessionsResponse, error)
	RevokeSession(ctx context.Context, in *identityv1.RevokeSessionRequest, opts ...grpc.CallOption) (*identityv1.RevokeSessionResponse, error)
	RevokeOtherSessions(ctx context.Context, in *identityv1.RevokeOtherSessionsRequest, opts ...grpc.CallOption) (*identityv1.RevokeOtherSessionsResponse, error)
	GetConsents(ctx context.Context, in *identityv1.GetConsentsRequest, opts ...grpc.CallOption) (*identityv1.GetConsentsResponse, error)
	AcceptConsents(ctx context.Context, in *identityv1.AcceptConsentsRequest, opts ...grpc.CallOption) (*identityv1.AcceptConsentsResponse, error)
	DeleteAccount(ctx context.Context, in *identityv1.DeleteAccountRequest, opts ...grpc.CallOption) (*identityv1.DeleteAccountResponse, error)
}

// maxReferralCode bounds the code before it reaches identity.
const maxReferralCode = 32

func registerSocial(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/me/friends", a(h.listFriends))
	mux.HandleFunc("DELETE /v1/me/friends/{user_id}", a(h.removeFriendship))
	mux.HandleFunc("GET /v1/me/friend-requests", a(h.listFriendRequests))
	mux.HandleFunc("POST /v1/me/friend-requests", a(h.sendFriendRequest))
	mux.HandleFunc("POST /v1/me/friend-requests/{user_id}/acceptance", a(h.acceptFriendRequest))
	mux.HandleFunc("GET /v1/me/blocks", a(h.listBlocks))
	mux.HandleFunc("PUT /v1/me/blocks/{user_id}", a(h.blockUser))
	mux.HandleFunc("DELETE /v1/me/blocks/{user_id}", a(h.unblockUser))
}

func registerInvitations(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/invitations", a(h.listTayfaInvitations))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/invitations", a(h.inviteFriend))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/invitations/{user_id}", a(h.cancelInvitation))
	mux.HandleFunc("GET /v1/me/invitations", a(h.listMyInvitations))
	mux.HandleFunc("POST /v1/me/invitations/{tayfa_id}/acceptance", a(h.acceptInvitation))
	mux.HandleFunc("DELETE /v1/me/invitations/{tayfa_id}", a(h.declineInvitation))
}

func registerProfiles(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/users/{user_id}/profile", a(h.getProfile))
	mux.HandleFunc("GET /v1/users/{user_id}/activity", a(h.listActivity))
}

func registerAccount(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/me/account", a(h.getAccount))
	mux.HandleFunc("PATCH /v1/me/account", a(h.updateAccount))
	mux.HandleFunc("POST /v1/me/referral-code", a(h.rotateReferralCode))
	mux.HandleFunc("POST /v1/referral-lookups", a(h.lookupReferralCode))
}

// --- JSON ---

var friendStates = newEnum(map[string]corev1.FriendState{
	"friends": corev1.FriendState_FRIEND_STATE_FRIENDS, "incoming": corev1.FriendState_FRIEND_STATE_INCOMING,
	"outgoing": corev1.FriendState_FRIEND_STATE_OUTGOING,
})

type friendJSON struct {
	UserID         string     `json:"user_id"`
	DisplayName    string     `json:"display_name"`
	AvatarUploadID string     `json:"avatar_upload_id,omitempty"`
	State          string     `json:"state"`
	RequestedAt    time.Time  `json:"requested_at"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
}

func friendFrom(f *corev1.Friend) friendJSON {
	return friendJSON{
		UserID: f.GetUserId(), DisplayName: f.GetDisplayName(), AvatarUploadID: f.GetAvatarUploadId(),
		State: friendStates.name(f.GetState()), RequestedAt: f.GetRequestTime().AsTime(), AcceptedAt: optTime(f.GetAcceptTime()),
	}
}

type friendListJSON struct {
	Friends       []friendJSON `json:"friends"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

func friendList(items []*corev1.Friend, next string) friendListJSON {
	out := friendListJSON{Friends: make([]friendJSON, 0, len(items)), NextPageToken: next}
	for _, f := range items {
		out.Friends = append(out.Friends, friendFrom(f))
	}
	return out
}

type blockedJSON struct {
	UserID         string    `json:"user_id"`
	DisplayName    string    `json:"display_name,omitempty"`
	AvatarUploadID string    `json:"avatar_upload_id,omitempty"`
	BlockedAt      time.Time `json:"blocked_at"`
}

type blockListJSON struct {
	Users         []blockedJSON `json:"users"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

type invitationJSON struct {
	TayfaID   string `json:"tayfa_id"`
	TayfaName string `json:"tayfa_name"`
	UserID    string `json:"user_id"`
	// Absent when the inviter's account was deleted.
	InviterUserID string    `json:"inviter_user_id,omitempty"`
	InvitedAt     time.Time `json:"invited_at"`
}

func invitationFrom(i *corev1.Invitation) invitationJSON {
	return invitationJSON{
		TayfaID: i.GetTayfaId(), TayfaName: i.GetTayfaName(), UserID: i.GetUserId(),
		InviterUserID: i.GetInviterUserId(), InvitedAt: i.GetInviteTime().AsTime(),
	}
}

type invitationListJSON struct {
	Invitations   []invitationJSON `json:"invitations"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

func invitationList(items []*corev1.Invitation, next string) invitationListJSON {
	out := invitationListJSON{Invitations: make([]invitationJSON, 0, len(items)), NextPageToken: next}
	for _, i := range items {
		out.Invitations = append(out.Invitations, invitationFrom(i))
	}
	return out
}

var activityVisibilities = newEnum(map[string]corev1.ActivityVisibility{
	"private": corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE, "tayfa": corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
	"public": corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC,
})

var identityVisibilities = newEnum(map[string]identityv1.ActivityVisibility{
	"private": identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE, "tayfa": identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
	"public": identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC,
})

type mutualTayfaJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type profileJSON struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	AvatarUploadID string `json:"avatar_upload_id,omitempty"`
	Self           bool   `json:"self"`
	// Absent when there is no friendship or request between the two.
	FriendState     string            `json:"friend_state,omitempty"`
	MutualTayfas    []mutualTayfaJSON `json:"mutual_tayfas"`
	ActivityVisible bool              `json:"activity_visible"`
	// Only on one's own profile.
	ActivityVisibility string `json:"activity_visibility,omitempty"`
}

type activityJSON struct {
	EventType string    `json:"event_type"`
	Mode      string    `json:"mode"`
	City      string    `json:"city,omitempty"`
	District  string    `json:"district,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// The rest only for active members of the record's tayfa.
	TayfaMember     bool   `json:"tayfa_member"`
	TayfaID         string `json:"tayfa_id,omitempty"`
	TayfaName       string `json:"tayfa_name,omitempty"`
	EventID         string `json:"event_id,omitempty"`
	EventTitle      string `json:"event_title,omitempty"`
	DurationMinutes int32  `json:"duration_minutes,omitempty"`
}

type activityListJSON struct {
	Records       []activityJSON `json:"records"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

// --- Friends and blocks ---

type userRefBody struct {
	UserID string `json:"user_id"`
}

func (h coreHandlers) listFriends(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Social.ListFriends(ctx, &corev1.ListFriendsRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, friendList(resp.GetFriends(), resp.GetNextPageToken()))
}

func (h coreHandlers) listFriendRequests(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var dir corev1.RequestDirection
	switch r.URL.Query().Get("direction") {
	case "incoming", "":
		dir = corev1.RequestDirection_REQUEST_DIRECTION_INCOMING
	case "outgoing":
		dir = corev1.RequestDirection_REQUEST_DIRECTION_OUTGOING
	default:
		badRequest(w, r)
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Social.ListFriendRequests(ctx, &corev1.ListFriendRequestsRequest{Direction: dir, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, friendList(resp.GetRequests(), resp.GetNextPageToken()))
}

func (h coreHandlers) sendFriendRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b userRefBody
	if !decode(w, r, &b) {
		return
	}
	if !validUUID(b.UserID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Social.SendFriendRequest(ctx, &corev1.SendFriendRequestRequest{UserId: b.UserID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, friendFrom(resp.GetFriend()))
}

func (h coreHandlers) acceptFriendRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.d.Social.AcceptFriendRequest(ctx, &corev1.AcceptFriendRequestRequest{UserId: u})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, friendFrom(resp.GetFriend()))
}

func (h coreHandlers) removeFriendship(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if _, err := h.d.Social.RemoveFriendship(ctx, &corev1.RemoveFriendshipRequest{UserId: u}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) listBlocks(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Social.ListBlockedUsers(ctx, &corev1.ListBlockedUsersRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := blockListJSON{Users: make([]blockedJSON, 0, len(resp.GetUsers())), NextPageToken: resp.GetNextPageToken()}
	for _, b := range resp.GetUsers() {
		out.Users = append(out.Users, blockedJSON{UserID: b.GetUserId(), DisplayName: b.GetDisplayName(), AvatarUploadID: b.GetAvatarUploadId(), BlockedAt: b.GetBlockTime().AsTime()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) blockUser(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if _, err := h.d.Social.BlockUser(ctx, &corev1.BlockUserRequest{UserId: u}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) unblockUser(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if _, err := h.d.Social.UnblockUser(ctx, &corev1.UnblockUserRequest{UserId: u}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Direct invitations ---

func (h coreHandlers) inviteFriend(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b userRefBody
	if !decode(w, r, &b) {
		return
	}
	if !validUUID(b.UserID) {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Invitations.InviteFriend(ctx, &corev1.InviteFriendRequest{TayfaId: tf, UserId: b.UserID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, invitationFrom(resp.GetInvitation()))
}

func (h coreHandlers) listTayfaInvitations(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Invitations.ListInvitations(ctx, &corev1.ListInvitationsRequest{TayfaId: tf, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitationList(resp.GetInvitations(), resp.GetNextPageToken()))
}

func (h coreHandlers) cancelInvitation(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, u, ok := pathTayfaUser(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Invitations.CancelInvitation(ctx, &corev1.CancelInvitationRequest{TayfaId: tf, UserId: u}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) listMyInvitations(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Invitations.ListMyInvitations(ctx, &corev1.ListMyInvitationsRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitationList(resp.GetInvitations(), resp.GetNextPageToken()))
}

func (h coreHandlers) acceptInvitation(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Invitations.AcceptInvitation(ctx, &corev1.AcceptInvitationRequest{TayfaId: tf})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipFrom(resp.GetMembership()))
}

func (h coreHandlers) declineInvitation(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	if _, err := h.d.Invitations.DeclineInvitation(ctx, &corev1.DeclineInvitationRequest{TayfaId: tf}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Profiles ---

func (h coreHandlers) getProfile(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	resp, err := h.d.Profiles.GetUserProfile(ctx, &corev1.GetUserProfileRequest{UserId: u})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := resp.GetProfile()
	out := profileJSON{
		UserID: p.GetUserId(), DisplayName: p.GetDisplayName(), AvatarUploadID: p.GetAvatarUploadId(),
		Self: p.GetSelf(), ActivityVisible: p.GetActivityVisible(),
		MutualTayfas: make([]mutualTayfaJSON, 0, len(p.GetMutualTayfas())),
	}
	if p.GetFriendState() != corev1.FriendState_FRIEND_STATE_UNSPECIFIED {
		out.FriendState = friendStates.name(p.GetFriendState())
	}
	if p.GetActivityVisibility() != corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED {
		out.ActivityVisibility = activityVisibilities.name(p.GetActivityVisibility())
	}
	for _, t := range p.GetMutualTayfas() {
		out.MutualTayfas = append(out.MutualTayfas, mutualTayfaJSON{ID: t.GetTayfaId(), Name: t.GetName()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) listActivity(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	u, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Profiles.ListUserActivity(ctx, &corev1.ListUserActivityRequest{UserId: u, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := activityListJSON{Records: make([]activityJSON, 0, len(resp.GetRecords())), NextPageToken: resp.GetNextPageToken()}
	for _, a := range resp.GetRecords() {
		out.Records = append(out.Records, activityJSON{
			EventType: eventTypes.name(a.GetEventType()), Mode: eventModes.name(a.GetMode()),
			City: a.GetCity(), District: a.GetDistrict(), StartedAt: a.GetStartTime().AsTime(),
			TayfaMember: a.GetTayfaMember(), TayfaID: a.GetTayfaId(), TayfaName: a.GetTayfaName(),
			EventID: a.GetEventId(), EventTitle: a.GetEventTitle(), DurationMinutes: a.GetDurationMinutes(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Account (identity) ---

type accountJSON struct {
	ReferralCode       string `json:"referral_code"`
	ActivityVisibility string `json:"activity_visibility"`
	AvatarUploadID     string `json:"avatar_upload_id,omitempty"`
}

type accountPatchBody struct {
	ActivityVisibility string `json:"activity_visibility"`
}

type referralCodeJSON struct {
	ReferralCode string `json:"referral_code"`
}

type referralLookupBody struct {
	ReferralCode string `json:"referral_code"`
}

type referredUserJSON struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	AvatarUploadID string `json:"avatar_upload_id,omitempty"`
}

func (h coreHandlers) identityFail(w http.ResponseWriter, r *http.Request, err error) {
	writeUpstreamError(w, r, h.d.Log, "identity", err)
}

func (h coreHandlers) getAccount(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Accounts.GetAccount(ctx, &identityv1.GetAccountRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, accountJSON{
		ReferralCode: resp.GetReferralCode(), ActivityVisibility: identityVisibilities.name(resp.GetActivityVisibility()),
		AvatarUploadID: resp.GetAvatarUploadId(),
	})
}

func (h coreHandlers) updateAccount(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b accountPatchBody
	if !decode(w, r, &b) {
		return
	}
	v, ok := identityVisibilities.value(b.ActivityVisibility)
	if !ok {
		badRequest(w, r)
		return
	}
	if _, err := h.d.Accounts.SetActivityVisibility(ctx, &identityv1.SetActivityVisibilityRequest{ActivityVisibility: v}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	h.getAccount(ctx, w, r)
}

func (h coreHandlers) rotateReferralCode(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Accounts.RotateReferralCode(ctx, &identityv1.RotateReferralCodeRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, referralCodeJSON{ReferralCode: resp.GetReferralCode()})
}

func (h coreHandlers) lookupReferralCode(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b referralLookupBody
	if !decode(w, r, &b) {
		return
	}
	if b.ReferralCode == "" || len(b.ReferralCode) > maxReferralCode {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Accounts.FindUserByReferralCode(ctx, &identityv1.FindUserByReferralCodeRequest{ReferralCode: b.ReferralCode})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, referredUserJSON{UserID: resp.GetUserId(), DisplayName: resp.GetDisplayName(), AvatarUploadID: resp.GetAvatarUploadId()})
}

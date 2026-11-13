package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// --- fakeCore: SocialService, invitations, ProfileService, identity's
// AccountService ---

func testFriend() *corev1.Friend {
	return &corev1.Friend{UserId: userID, DisplayName: "Ece", State: corev1.FriendState_FRIEND_STATE_OUTGOING, RequestTime: timestamppb.New(time.Unix(1, 0))}
}

func (f *fakeCore) SendFriendRequest(ctx context.Context, in *corev1.SendFriendRequestRequest, _ ...grpc.CallOption) (*corev1.SendFriendRequestResponse, error) {
	return &corev1.SendFriendRequestResponse{Friend: testFriend()}, f.record(ctx, in)
}
func (f *fakeCore) AcceptFriendRequest(ctx context.Context, in *corev1.AcceptFriendRequestRequest, _ ...grpc.CallOption) (*corev1.AcceptFriendRequestResponse, error) {
	fr := testFriend()
	fr.State, fr.AcceptTime = corev1.FriendState_FRIEND_STATE_FRIENDS, timestamppb.New(time.Unix(2, 0))
	return &corev1.AcceptFriendRequestResponse{Friend: fr}, f.record(ctx, in)
}
func (f *fakeCore) RemoveFriendship(ctx context.Context, in *corev1.RemoveFriendshipRequest, _ ...grpc.CallOption) (*corev1.RemoveFriendshipResponse, error) {
	return &corev1.RemoveFriendshipResponse{}, f.record(ctx, in)
}
func (f *fakeCore) ListFriends(ctx context.Context, in *corev1.ListFriendsRequest, _ ...grpc.CallOption) (*corev1.ListFriendsResponse, error) {
	return &corev1.ListFriendsResponse{Friends: []*corev1.Friend{testFriend()}, NextPageToken: "sonraki"}, f.record(ctx, in)
}
func (f *fakeCore) ListFriendRequests(ctx context.Context, in *corev1.ListFriendRequestsRequest, _ ...grpc.CallOption) (*corev1.ListFriendRequestsResponse, error) {
	return &corev1.ListFriendRequestsResponse{}, f.record(ctx, in)
}
func (f *fakeCore) BlockUser(ctx context.Context, in *corev1.BlockUserRequest, _ ...grpc.CallOption) (*corev1.BlockUserResponse, error) {
	return &corev1.BlockUserResponse{}, f.record(ctx, in)
}
func (f *fakeCore) UnblockUser(ctx context.Context, in *corev1.UnblockUserRequest, _ ...grpc.CallOption) (*corev1.UnblockUserResponse, error) {
	return &corev1.UnblockUserResponse{}, f.record(ctx, in)
}
func (f *fakeCore) ListBlockedUsers(ctx context.Context, in *corev1.ListBlockedUsersRequest, _ ...grpc.CallOption) (*corev1.ListBlockedUsersResponse, error) {
	return &corev1.ListBlockedUsersResponse{Users: []*corev1.BlockedUser{{UserId: userID, BlockTime: timestamppb.New(time.Unix(1, 0))}}}, f.record(ctx, in)
}

func testInvitation() *corev1.Invitation {
	return &corev1.Invitation{TayfaId: tayfaID, TayfaName: "Klasikçiler", UserId: userID, InviteTime: timestamppb.New(time.Unix(1, 0))}
}

func (f *fakeCore) InviteFriend(ctx context.Context, in *corev1.InviteFriendRequest, _ ...grpc.CallOption) (*corev1.InviteFriendResponse, error) {
	return &corev1.InviteFriendResponse{Invitation: testInvitation()}, f.record(ctx, in)
}
func (f *fakeCore) ListMyInvitations(ctx context.Context, in *corev1.ListMyInvitationsRequest, _ ...grpc.CallOption) (*corev1.ListMyInvitationsResponse, error) {
	return &corev1.ListMyInvitationsResponse{Invitations: []*corev1.Invitation{testInvitation()}}, f.record(ctx, in)
}
func (f *fakeCore) AcceptInvitation(ctx context.Context, in *corev1.AcceptInvitationRequest, _ ...grpc.CallOption) (*corev1.AcceptInvitationResponse, error) {
	return &corev1.AcceptInvitationResponse{Membership: testMembership()}, f.record(ctx, in)
}
func (f *fakeCore) DeclineInvitation(ctx context.Context, in *corev1.DeclineInvitationRequest, _ ...grpc.CallOption) (*corev1.DeclineInvitationResponse, error) {
	return &corev1.DeclineInvitationResponse{}, f.record(ctx, in)
}
func (f *fakeCore) ListInvitations(ctx context.Context, in *corev1.ListInvitationsRequest, _ ...grpc.CallOption) (*corev1.ListInvitationsResponse, error) {
	return &corev1.ListInvitationsResponse{}, f.record(ctx, in)
}
func (f *fakeCore) CancelInvitation(ctx context.Context, in *corev1.CancelInvitationRequest, _ ...grpc.CallOption) (*corev1.CancelInvitationResponse, error) {
	return &corev1.CancelInvitationResponse{}, f.record(ctx, in)
}

func (f *fakeCore) GetUserProfile(ctx context.Context, in *corev1.GetUserProfileRequest, _ ...grpc.CallOption) (*corev1.GetUserProfileResponse, error) {
	return &corev1.GetUserProfileResponse{Profile: &corev1.UserProfile{
		UserId: userID, DisplayName: "Ece", ActivityVisible: true,
		MutualTayfas: []*corev1.MutualTayfa{{TayfaId: tayfaID, Name: "Klasikçiler"}},
	}}, f.record(ctx, in)
}
func (f *fakeCore) ListUserActivity(ctx context.Context, in *corev1.ListUserActivityRequest, _ ...grpc.CallOption) (*corev1.ListUserActivityResponse, error) {
	return &corev1.ListUserActivityResponse{Records: []*corev1.ActivityRecord{
		{EventType: corev1.EventType_EVENT_TYPE_HIKE, Mode: corev1.EventMode_EVENT_MODE_IN_PERSON, City: "Bursa", StartTime: timestamppb.New(time.Unix(1, 0))},
	}}, f.record(ctx, in)
}

func (f *fakeCore) GetAccount(ctx context.Context, in *identityv1.GetAccountRequest, _ ...grpc.CallOption) (*identityv1.GetAccountResponse, error) {
	return &identityv1.GetAccountResponse{
		UserId: userID, DisplayName: "Deniz",
		ReferralCode: "TYF-8K2M4Q", ActivityVisibility: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
	}, f.record(ctx, in)
}
func (f *fakeCore) RotateReferralCode(ctx context.Context, in *identityv1.RotateReferralCodeRequest, _ ...grpc.CallOption) (*identityv1.RotateReferralCodeResponse, error) {
	return &identityv1.RotateReferralCodeResponse{ReferralCode: "TYF-2222AB"}, f.record(ctx, in)
}
func (f *fakeCore) FindUserByReferralCode(ctx context.Context, in *identityv1.FindUserByReferralCodeRequest, _ ...grpc.CallOption) (*identityv1.FindUserByReferralCodeResponse, error) {
	return &identityv1.FindUserByReferralCodeResponse{UserId: userID, DisplayName: "Ece"}, f.record(ctx, in)
}
func (f *fakeCore) SetActivityVisibility(ctx context.Context, in *identityv1.SetActivityVisibilityRequest, _ ...grpc.CallOption) (*identityv1.SetActivityVisibilityResponse, error) {
	return &identityv1.SetActivityVisibilityResponse{ActivityVisibility: in.GetActivityVisibility()}, f.record(ctx, in)
}

func (f *fakeCore) UpdateAccount(ctx context.Context, in *identityv1.UpdateAccountRequest, _ ...grpc.CallOption) (*identityv1.UpdateAccountResponse, error) {
	a := &identityv1.GetAccountResponse{
		UserId: userID, DisplayName: "Deniz",
		ReferralCode: "TYF-8K2M4Q", ActivityVisibility: identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
		BirthdayVisibility: identityv1.BirthdayVisibility_BIRTHDAY_VISIBILITY_BIRTHDAY,
	}
	if in.GetBirthdayVisibility() != identityv1.BirthdayVisibility_BIRTHDAY_VISIBILITY_UNSPECIFIED {
		a.BirthdayVisibility = in.GetBirthdayVisibility()
	}
	if in.DisplayName != nil {
		a.DisplayName = in.GetDisplayName()
	}
	if in.GetActivityVisibility() != identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED {
		a.ActivityVisibility = in.GetActivityVisibility()
	}
	return &identityv1.UpdateAccountResponse{Account: a}, f.record(ctx, in)
}

func (f *fakeCore) SnoozeProfilePrompt(ctx context.Context, in *identityv1.SnoozeProfilePromptRequest, _ ...grpc.CallOption) (*identityv1.SnoozeProfilePromptResponse, error) {
	return &identityv1.SnoozeProfilePromptResponse{}, f.record(ctx, in)
}

// --- tests ---

func TestFriendRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "POST", "/v1/me/friend-requests", `{"user_id":"`+userID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("istek: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	if got.(*corev1.SendFriendRequestRequest).GetUserId() != userID || tok != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", got, tok)
	}
	if v := decodeBody[friendJSON](t, w); v.State != "outgoing" || v.AcceptedAt != nil {
		t.Fatalf("cevap: %+v", v)
	}
	w = as(h, "POST", "/v1/me/friend-requests/"+userID+"/acceptance", "")
	if v := decodeBody[friendJSON](t, w); w.Code != 200 || v.State != "friends" || v.AcceptedAt == nil {
		t.Fatalf("kabul: %d %+v", w.Code, v)
	}
	w = as(h, "GET", "/v1/me/friend-requests?direction=outgoing", "")
	got, _, _ = f.last()
	if w.Code != 200 || got.(*corev1.ListFriendRequestsRequest).GetDirection() != corev1.RequestDirection_REQUEST_DIRECTION_OUTGOING {
		t.Fatalf("giden: %d %v", w.Code, got)
	}
	as(h, "GET", "/v1/me/friend-requests", "")
	if got, _, _ = f.last(); got.(*corev1.ListFriendRequestsRequest).GetDirection() != corev1.RequestDirection_REQUEST_DIRECTION_INCOMING {
		t.Fatalf("varsayılan yön: %v", got)
	}
	w = as(h, "GET", "/v1/me/friends", "")
	if v := decodeBody[friendListJSON](t, w); w.Code != 200 || len(v.Friends) != 1 || v.NextPageToken != "sonraki" {
		t.Fatalf("arkadaşlar: %d %+v", w.Code, v)
	}
	for _, c := range []struct{ method, path string }{
		{"DELETE", "/v1/me/friends/" + userID}, {"PUT", "/v1/me/blocks/" + userID}, {"DELETE", "/v1/me/blocks/" + userID},
	} {
		if w := as(h, c.method, c.path, ""); w.Code != http.StatusNoContent {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	w = as(h, "GET", "/v1/me/blocks", "")
	if !strings.Contains(w.Body.String(), `"users":[{"user_id":"`+userID) || strings.Contains(w.Body.String(), "display_name") {
		t.Fatalf("engeller: %s", w.Body)
	}
}

func TestSocialRoutesValidateInput(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/me/friend-requests", `{"user_id":"x"}`},
		{"POST", "/v1/me/friend-requests", `{"user_id":"` + userID + `","extra":1}`},
		{"GET", "/v1/me/friend-requests?direction=sideways", ""},
		{"PUT", "/v1/me/blocks/x", ""},
		{"POST", "/v1/tayfas/" + tayfaID + "/invitations", `{}`},
		{"POST", "/v1/me/invitations/x/acceptance", ""},
		{"GET", "/v1/users/x/profile", ""},
		{"GET", "/v1/users/" + userID + "/activity?page_size=101", ""},
		{"PATCH", "/v1/me/account", `{"activity_visibility":"friends"}`},
		{"PATCH", "/v1/me/account", `{}`},
		{"PATCH", "/v1/me/account", `{"birthday_visibility":"day_month"}`},
		{"PATCH", "/v1/me/account", `{"activity_visibility":null}`},
		{"PATCH", "/v1/me/account", `{"display_name":"` + strings.Repeat("a", 513) + `"}`},
		{"PATCH", "/v1/me/account", `{"display_name":"Ece","nickname":"x"}`},
		{"POST", "/v1/referral-lookups", `{"referral_code":""}`},
		{"POST", "/v1/referral-lookups", `{"referral_code":"` + strings.Repeat("A", 33) + `"}`},
	} {
		if w := as(h, c.method, c.path, c.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("geçersiz istekler core'a ulaştı: %d", n)
	}
	// No token, no call.
	w := do(h, "GET", "/v1/me/friends", "", map[string]string{clientHeader: "android"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("token'sız: %d", w.Code)
	}
}

func TestInvitationRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "POST", "/v1/tayfas/"+tayfaID+"/invitations", `{"user_id":"`+userID+`"}`)
	got, _, _ := f.last()
	if w.Code != http.StatusCreated || got.(*corev1.InviteFriendRequest).GetTayfaId() != tayfaID || got.(*corev1.InviteFriendRequest).GetUserId() != userID {
		t.Fatalf("davet: %d %v", w.Code, got)
	}
	if v := decodeBody[invitationJSON](t, w); v.TayfaName != "Klasikçiler" || v.InviterUserID != "" {
		t.Fatalf("davet cevabı: %+v", v)
	}
	w = as(h, "POST", "/v1/me/invitations/"+tayfaID+"/acceptance", "")
	if v := decodeBody[membershipJSON](t, w); w.Code != 200 || v.TayfaID == "" {
		t.Fatalf("kabul: %d %+v", w.Code, v)
	}
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"DELETE", "/v1/me/invitations/" + tayfaID, 204},
		{"GET", "/v1/me/invitations", 200},
		{"GET", "/v1/tayfas/" + tayfaID + "/invitations", 200},
		{"DELETE", "/v1/tayfas/" + tayfaID + "/invitations/" + userID, 204},
	} {
		if w := as(h, c.method, c.path, ""); w.Code != c.code {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	w = as(coreRouter(&fakeCore{err: coreErr(codes.ResourceExhausted, "ERROR_REASON_DIRECT_INVITE_LIMIT")}), "POST", "/v1/tayfas/"+tayfaID+"/invitations", `{"user_id":"`+userID+`"}`)
	if w.Code != http.StatusTooManyRequests || problemType(t, w) != "direct-invite-limit" {
		t.Fatalf("sınır: %d %s", w.Code, problemType(t, w))
	}
}

func TestProfileRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/users/"+userID+"/profile", "")
	v := decodeBody[profileJSON](t, w)
	if w.Code != 200 || v.FriendState != "" || v.ActivityVisibility != "" || len(v.MutualTayfas) != 1 || !v.ActivityVisible {
		t.Fatalf("profil: %d %+v", w.Code, v)
	}
	w = as(h, "GET", "/v1/users/"+userID+"/activity?page_size=10", "")
	a := decodeBody[activityListJSON](t, w)
	if w.Code != 200 || len(a.Records) != 1 || a.Records[0].EventType != "hike" || a.Records[0].Mode != "in_person" || a.Records[0].TayfaMember || strings.Contains(w.Body.String(), "tayfa_name") {
		t.Fatalf("geçmiş: %d %s", w.Code, w.Body)
	}
	w = as(coreRouter(&fakeCore{err: coreErr(codes.NotFound, "ERROR_REASON_USER_NOT_FOUND")}), "GET", "/v1/users/"+userID+"/profile", "")
	if w.Code != http.StatusNotFound || problemType(t, w) != "user-not-found" {
		t.Fatalf("engel: %d %s", w.Code, problemType(t, w))
	}
}

type fakeDetails struct {
	resp  *identityv1.GetProfileDetailsResponse
	err   error
	got   string
	token string
}

func (f *fakeDetails) GetProfileDetails(ctx context.Context, in *identityv1.GetProfileDetailsRequest, _ ...grpc.CallOption) (*identityv1.GetProfileDetailsResponse, error) {
	f.got, f.token = in.GetUserId(), tokenFrom(ctx)
	return f.resp, f.err
}

// TestProfileDetails (issue #185): identity's details are added as
// identity returns them; gateway passes only the profile's user and the
// caller's token. Without identity the profile is shown without them.
func TestProfileDetails(t *testing.T) {
	with := func(d *fakeDetails) http.Handler {
		deps := testCoreDeps(&fakeCore{}, 100)
		deps.ProfileDetails = d
		return coreRouterWith(deps)
	}
	d := &fakeDetails{resp: &identityv1.GetProfileDetailsResponse{Birthday: &identityv1.Birthday{Month: 3, Day: 4}}}
	w := as(with(d), "GET", "/v1/users/"+userID+"/profile", "")
	v := decodeBody[profileJSON](t, w)
	if w.Code != 200 || v.Birthday == nil || *v.Birthday != (birthdayJSON{Month: 3, Day: 4}) || d.got != userID || d.token != "USERTOKEN" ||
		strings.Contains(w.Body.String(), `"year"`) || strings.Contains(w.Body.String(), `"age"`) {
		t.Fatalf("gün/ay: %d %s", w.Code, w.Body)
	}
	d = &fakeDetails{resp: &identityv1.GetProfileDetailsResponse{
		Birthday: &identityv1.Birthday{Month: 3, Day: 4, Year: 2008, Age: 18},
		About: &identityv1.ProfileFields{Team: "Bursaspor", SocialAccounts: []*identityv1.SocialAccount{
			{Platform: identityv1.SocialPlatform_SOCIAL_PLATFORM_INSTAGRAM, Username: "ece.su"},
		}},
	}}
	w = as(with(d), "GET", "/v1/users/"+userID+"/profile", "")
	if !strings.Contains(w.Body.String(), `"team":"Bursaspor"`) || !strings.Contains(w.Body.String(), `"social_accounts":[{"platform":"instagram","username":"ece.su"}]`) ||
		strings.Contains(w.Body.String(), `"interests"`) {
		t.Fatalf("hakkında: %s", w.Body)
	}
	if v := decodeBody[profileJSON](t, w); w.Code != 200 || v.Birthday == nil || v.Birthday.Year != 2008 || v.Birthday.Age != 18 {
		t.Fatalf("tam: %d %s", w.Code, w.Body)
	}
	for name, d := range map[string]*fakeDetails{
		"gizli":        {resp: &identityv1.GetProfileDetailsResponse{}},
		"identity yok": {err: status.Error(codes.Unavailable, "x")},
	} {
		w := as(with(d), "GET", "/v1/users/"+userID+"/profile", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "birthday") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// core refuses (block, unknown user): identity is not asked.
	d = &fakeDetails{resp: &identityv1.GetProfileDetailsResponse{Birthday: &identityv1.Birthday{Month: 3, Day: 4}}}
	deps := testCoreDeps(&fakeCore{err: coreErr(codes.NotFound, "ERROR_REASON_USER_NOT_FOUND")}, 100)
	deps.ProfileDetails = d
	if w := as(coreRouterWith(deps), "GET", "/v1/users/"+userID+"/profile", ""); w.Code != http.StatusNotFound || d.got != "" {
		t.Fatalf("engel: %d %q", w.Code, d.got)
	}
}

// TestProfileFieldRoutes (issue #186): names map to identity's enums,
// unknown names and oversized input never leave gateway; identity's
// refusal is invalid-request.
func TestProfileFieldRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "PATCH", "/v1/me/account", `{"interests":["hike","camp"],"team":"Bursaspor","languages":[],
		"social_accounts":[{"platform":"x","username":"ece"}],"declined_profile_fields":["about"],"about_visibility":"friends"}`)
	got, _, _ := f.last()
	req := got.(*identityv1.UpdateAccountRequest)
	p := req.GetProfile()
	if w.Code != 200 || len(p.GetInterests().GetValues()) != 2 || p.GetTeam() != "Bursaspor" || p.Languages == nil ||
		p.GetSocialAccounts().GetValues()[0].GetPlatform() != identityv1.SocialPlatform_SOCIAL_PLATFORM_X ||
		p.GetDeclined().GetValues()[0] != identityv1.ProfileField_PROFILE_FIELD_ABOUT || p.About != nil ||
		req.GetAboutVisibility() != identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS || req.DisplayName != nil {
		t.Fatalf("profil alanları: %d %v", w.Code, req)
	}
	// Visibility alone sends no profile update.
	_ = as(h, "PATCH", "/v1/me/account", `{"about_visibility":"none"}`)
	got, _, _ = f.last()
	if got.(*identityv1.UpdateAccountRequest).Profile != nil {
		t.Fatal("yalnız görünürlük profil güncellemesi gönderdi")
	}
	n0 := func() int { _, _, n := f.last(); return n }()
	for _, body := range []string{
		`{"interests":["chess"]}`,
		`{"interests":["hike","hike"]}`,
		`{"social_accounts":[{"platform":"facebook","username":"ece"}]}`,
		`{"declined_profile_fields":["birth_date"]}`,
		`{"about_visibility":"public"}`,
		`{"about":"` + strings.Repeat("a", 2049) + `"}`,
		`{"languages":["` + strings.Repeat(`tr","`, 16) + `tr"]}`,
	} {
		if w := as(h, "PATCH", "/v1/me/account", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if _, _, n := f.last(); n != n0 {
		t.Fatal("geçersiz istek identity'ye ulaştı")
	}
	w = as(coreRouter(&fakeCore{err: identityErr(codes.InvalidArgument, "ERROR_REASON_VALIDATION")}), "PATCH", "/v1/me/account", `{"social_accounts":[{"platform":"x","username":"https://evil.example"}]}`)
	if w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
		t.Fatalf("identity reddi: %d %s", w.Code, problemType(t, w))
	}
	w = as(h, "POST", "/v1/me/profile-prompt/snooze", "")
	got, tok, _ := f.last()
	if _, ok := got.(*identityv1.SnoozeProfilePromptRequest); w.Code != http.StatusNoContent || !ok || tok != "USERTOKEN" {
		t.Fatalf("erteleme: %d %v", w.Code, got)
	}
}

func TestAccountProfileJSON(t *testing.T) {
	a := accountFrom(&identityv1.GetAccountResponse{
		Profile:               &identityv1.ProfileFields{Team: "Bursaspor", Interests: []identityv1.ProfileInterest{identityv1.ProfileInterest_PROFILE_INTEREST_HIKE}},
		DeclinedProfileFields: []identityv1.ProfileField{identityv1.ProfileField_PROFILE_FIELD_ABOUT},
		AboutVisibility:       identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS_AND_MUTUAL, ProfileCompletionPending: true,
	})
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"team":"Bursaspor"`, `"interests":["hike"]`, `"languages":[]`, `"social_accounts":[]`,
		`"declined_profile_fields":["about"]`, `"about_visibility":"friends_and_mutual"`, `"profile_completion_pending":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s yok: %s", want, b)
		}
	}
}

func TestAccountRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/me/account", "")
	if v := decodeBody[accountJSON](t, w); w.Code != 200 || v.ReferralCode != "TYF-8K2M4Q" || v.ActivityVisibility != "tayfa" ||
		v.UserID != userID || v.DisplayName != "Deniz" {
		t.Fatalf("hesap: %d %+v", w.Code, v)
	}
	w = as(h, "PATCH", "/v1/me/account", `{"activity_visibility":"private"}`)
	got, _, _ := f.last()
	if v := decodeBody[accountJSON](t, w); w.Code != 200 || v.ActivityVisibility != "private" || v.BirthdayVisibility != "birthday" ||
		got.(*identityv1.UpdateAccountRequest).DisplayName != nil {
		t.Fatalf("güncelleme: %d %+v", w.Code, v)
	}
	// Display name (issue #184): passed as typed, identity cleans it.
	w = as(h, "PATCH", "/v1/me/account", `{"display_name":" Ece  Su "}`)
	got, _, _ = f.last()
	if v := decodeBody[accountJSON](t, w); w.Code != 200 || v.DisplayName != " Ece  Su " ||
		got.(*identityv1.UpdateAccountRequest).GetDisplayName() != " Ece  Su " ||
		got.(*identityv1.UpdateAccountRequest).GetActivityVisibility() != identityv1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED {
		t.Fatalf("ad: %d %+v", w.Code, v)
	}
	w = as(h, "PATCH", "/v1/me/account", `{"birthday_visibility":"full"}`)
	got, _, _ = f.last()
	if v := decodeBody[accountJSON](t, w); w.Code != 200 || v.BirthdayVisibility != "full" ||
		got.(*identityv1.UpdateAccountRequest).GetBirthdayVisibility() != identityv1.BirthdayVisibility_BIRTHDAY_VISIBILITY_FULL {
		t.Fatalf("doğum günü: %d %+v", w.Code, v)
	}
	for reason, want := range map[string]struct {
		code int
		typ  string
	}{
		"ERROR_REASON_INVALID_DISPLAY_NAME": {http.StatusBadRequest, "invalid-display-name"},
		"ERROR_REASON_RATE_LIMITED":         {http.StatusTooManyRequests, "rate-limited"},
	} {
		w = as(coreRouter(&fakeCore{err: identityErr(codes.InvalidArgument, reason)}), "PATCH", "/v1/me/account", `{"display_name":"Ece"}`)
		if w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, problemType(t, w))
		}
	}
	w = as(h, "POST", "/v1/me/referral-code", "")
	if v := decodeBody[referralCodeJSON](t, w); w.Code != 200 || v.ReferralCode != "TYF-2222AB" {
		t.Fatalf("yenileme: %d %+v", w.Code, v)
	}
	w = as(h, "POST", "/v1/referral-lookups", `{"referral_code":"tyf-8k2m4q"}`)
	got, tok, _ := f.last()
	if w.Code != 200 || got.(*identityv1.FindUserByReferralCodeRequest).GetReferralCode() != "tyf-8k2m4q" || tok != "USERTOKEN" {
		t.Fatalf("arama: %d %v", w.Code, got)
	}
	// identity's reasons are trusted only from identity's domain.
	w = as(coreRouter(&fakeCore{err: identityErr(codes.ResourceExhausted, "ERROR_REASON_RATE_LIMITED")}), "POST", "/v1/referral-lookups", `{"referral_code":"TYF-8K2M4Q"}`)
	if w.Code != http.StatusTooManyRequests || problemType(t, w) != "rate-limited" {
		t.Fatalf("sınır: %d %s", w.Code, problemType(t, w))
	}
	w = as(coreRouter(&fakeCore{err: identityErr(codes.NotFound, "ERROR_REASON_REFERRAL_CODE_NOT_FOUND")}), "POST", "/v1/referral-lookups", `{"referral_code":"TYF-8K2M4Q"}`)
	if w.Code != http.StatusNotFound || problemType(t, w) != "referral-code-not-found" {
		t.Fatalf("bulunamadı: %d %s", w.Code, problemType(t, w))
	}
}

func TestRegistrationCarriesReferralCode(t *testing.T) {
	f := &fakeIdentity{}
	body := `{"birth_date":"1990-05-19","accepted_privacy_notice_version":"v","accepted_terms_version":"v","referral_code":"TYF-8K2M4Q"}`
	w := do(newTestRouter(f), "POST", "/v1/registration", body, map[string]string{"Authorization": "Bearer REGTOKEN", clientHeader: "ios"})
	if w.Code != http.StatusNoContent || f.gotReg.GetReferralCode() != "TYF-8K2M4Q" {
		t.Fatalf("kayıt: %d %v", w.Code, f.gotReg)
	}
}

package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Friend suggestions and settings (issue #208, ADR-0030).

func (f *fakeSocial) ListFriendSuggestions(_ context.Context, c id.ID) ([]social.Suggestion, error) {
	f.caller, f.called = c, true
	return []social.Suggestion{{UserID: f.other, DisplayName: "Ece", SuggestedAt: time.Unix(3, 0).UTC()}}, f.err
}
func (f *fakeSocial) DismissFriendSuggestion(_ context.Context, c, o id.ID) error {
	f.caller, f.other, f.called = c, o, true
	return f.err
}
func (f *fakeSocial) GetSocialSettings(_ context.Context, c id.ID) (social.Settings, error) {
	f.caller, f.called = c, true
	return social.DefaultSettings, f.err
}
func (f *fakeSocial) UpdateSocialSettings(_ context.Context, c id.ID, u social.SettingsUpdate) (social.Settings, error) {
	f.caller, f.called, f.update = c, true, u
	st := social.DefaultSettings
	if u.ProfilingEnabled != nil {
		st.ProfilingEnabled = *u.ProfilingEnabled
	}
	if u.HideFromSuggestions != nil {
		st.HideFromSuggestions = *u.HideFromSuggestions
	}
	return st, f.err
}

func TestFriendSuggestionMethods(t *testing.T) {
	u, other := id.New(), id.New()
	f := &fakeSocial{other: other}
	srv := newSocialServer(f)
	ctx := callerCtx(u)

	l, err := srv.ListFriendSuggestions(ctx, &corev1.ListFriendSuggestionsRequest{})
	if err != nil || f.caller != u || len(l.GetSuggestions()) != 1 || l.GetSuggestions()[0].GetUserId() != other.String() ||
		l.GetSuggestions()[0].GetAvatarUploadId() != "" || !l.GetSuggestions()[0].GetSuggestTime().AsTime().Equal(time.Unix(3, 0)) {
		t.Fatalf("liste: %v %v", l, err)
	}
	if _, err := srv.DismissFriendSuggestion(ctx, &corev1.DismissFriendSuggestionRequest{UserId: other.String()}); err != nil || f.other != other {
		t.Fatalf("kapat: %v", err)
	}
	g, err := srv.GetSocialSettings(ctx, &corev1.GetSocialSettingsRequest{})
	if err != nil || !g.GetSettings().GetProfilingEnabled() || g.GetSettings().GetHideFromSuggestions() {
		t.Fatalf("ayarlar: %v %v", g, err)
	}
	off := false
	up, err := srv.UpdateSocialSettings(ctx, &corev1.UpdateSocialSettingsRequest{ProfilingEnabled: &off})
	if err != nil || up.GetSettings().GetProfilingEnabled() || f.update.HideFromSuggestions != nil || f.update.ProfilingEnabled == nil {
		t.Fatalf("güncelleme: %v %v", up, err)
	}
}

func TestFriendSuggestionValidationAndAuth(t *testing.T) {
	f := &fakeSocial{}
	srv := newSocialServer(f)
	if _, err := srv.ListFriendSuggestions(context.Background(), &corev1.ListFriendSuggestionsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	if _, err := srv.UpdateSocialSettings(context.Background(), &corev1.UpdateSocialSettingsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız ayar: %v", err)
	}
	if _, err := srv.DismissFriendSuggestion(callerCtx(id.New()), &corev1.DismissFriendSuggestionRequest{UserId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("kimlik: %v", err)
	}
	if f.called {
		t.Fatal("uygulama katmanı çağrıldı")
	}
	srv = newSocialServer(&fakeSocial{err: apperr.Validation("settings")})
	if _, err := srv.UpdateSocialSettings(callerCtx(id.New()), &corev1.UpdateSocialSettingsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("boş güncelleme: %v", err)
	}
}

// TestNoScoreInSocialResponses pins ADR-0030: no response message of
// SocialService has a field that could carry the friendship score or the
// number of shared events.
func TestNoScoreInSocialResponses(t *testing.T) {
	svc := corev1.File_tayfam_core_v1_social_proto.Services().ByName("SocialService")
	if svc == nil {
		t.Fatal("SocialService yok")
	}
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		fields := m.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			name := strings.ToLower(string(fd.Name()))
			for _, bad := range []string{"score", "shared", "weight", "rank", "count"} {
				if strings.Contains(name, bad) {
					t.Errorf("%s.%s puanı ya da ortak katılımı taşıyabilir", m.FullName(), fd.Name())
				}
			}
			if fd.Message() != nil {
				walk(fd.Message())
			}
		}
	}
	methods := svc.Methods()
	for i := range methods.Len() {
		walk(methods.Get(i).Output())
	}
	if !seen["tayfam.core.v1.FriendSuggestion"] {
		t.Fatal("FriendSuggestion denetlenmedi")
	}
}

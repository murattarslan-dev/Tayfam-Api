package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const celebrationComponentService = "tayfam.core.v1.CelebrationComponentService"

// fakeCelebrations is core's CelebrationComponentService for the
// celebration tests.
type fakeCelebrations struct {
	get      *corev1.GetCelebrationRequest
	announce *corev1.AnnounceCelebrationRequest
	token    string
	out      *corev1.EventCelebration
	err      error
	calls    int
}

func (f *fakeCelebrations) GetCelebration(ctx context.Context, in *corev1.GetCelebrationRequest, _ ...grpc.CallOption) (*corev1.GetCelebrationResponse, error) {
	f.get, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.GetCelebrationResponse{Celebration: f.out}, f.err
}

func (f *fakeCelebrations) AnnounceCelebration(ctx context.Context, in *corev1.AnnounceCelebrationRequest, _ ...grpc.CallOption) (*corev1.AnnounceCelebrationResponse, error) {
	f.announce, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
	return &corev1.AnnounceCelebrationResponse{Celebration: f.out}, f.err
}

const celebrationPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/cup/celebration"

func TestCelebrationComponentRoutes(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeCelebrations{out: &corev1.EventCelebration{
		Source: "games", SourceKind: "scoreboard", Announced: true, AnnounceTime: timestamppb.New(at), CallerCanAnnounce: true,
		Winners: []*corev1.CelebrationWinner{{UserId: userID, TableId: tayfaID}, {GuestId: eventID}},
	}}
	h := componentRouter(fakeServices{celebrationComponentService: f})

	w := as(h, "GET", celebrationPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("okuma: %d %s", w.Code, w.Body)
	}
	if ref := f.get.GetRef(); ref.GetTayfaId() != tayfaID || ref.GetEventId() != eventID || ref.GetComponentKey() != "cup" || f.token != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", f.get, f.token)
	}
	got := decodeBody[eventCelebrationJSON](t, w)
	if got.Source != "games" || got.SourceKind != "scoreboard" || !got.Announced || got.AnnouncedAt == nil || !got.AnnouncedAt.Equal(at) ||
		!got.CallerCanAnnounce || got.Hidden || len(got.Winners) != 2 || got.Winners[0].UserID != userID || got.Winners[0].TableID != tayfaID ||
		got.Winners[1].GuestID != eventID || got.Winners[1].UserID != "" {
		t.Fatalf("kutlama: %+v", got)
	}

	if w := as(h, "PUT", celebrationPath, `{}`); w.Code != http.StatusOK || len(f.announce.GetWinnerIds()) != 0 ||
		f.announce.GetRef().GetComponentKey() != "cup" {
		t.Fatalf("kaynaklı ilan: %d %s %v", w.Code, w.Body, f.announce)
	}
	if w := as(h, "PUT", celebrationPath, `{"winner_ids":["`+userID+`"]}`); w.Code != http.StatusOK ||
		len(f.announce.GetWinnerIds()) != 1 || f.announce.GetWinnerIds()[0] != userID {
		t.Fatalf("elle ilan: %d %s %v", w.Code, w.Body, f.announce)
	}

	// Not announced, nothing to show: the optional fields are left out.
	f.out = &corev1.EventCelebration{}
	w = as(h, "GET", celebrationPath, "")
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, `"winners":[]`) || strings.Contains(body, `"announced_at"`) ||
		strings.Contains(body, `"source"`) {
		t.Fatalf("ilansız: %d %s", w.Code, body)
	}
}

func TestCelebrationComponentRoutesValidateInput(t *testing.T) {
	f := &fakeCelebrations{out: &corev1.EventCelebration{}}
	h := componentRouter(fakeServices{celebrationComponentService: f})
	eleven := `"` + strings.TrimSuffix(strings.Repeat(userID+`","`, 11), `","`) + `"`

	for name, body := range map[string]string{
		"bozuk kazanan": `{"winner_ids":["x"]}`,
		"metin":         `{"winner_ids":"` + userID + `"}`,
		"fazla kazanan": `{"winner_ids":[` + eleven + `]}`,
		"fazla alan":    `{"winner_ids":[],"source":"x"}`,
		"bozuk JSON":    `{"winner_ids":`,
	} {
		if w := as(h, "PUT", celebrationPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The path is checked before core is asked.
	for _, method := range []string{"GET", "PUT"} {
		path := "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Cup/celebration"
		if w := as(h, method, path, `{}`); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", method, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// No token, no call.
	for _, method := range []string{"GET", "PUT"} {
		if w := do(h, method, celebrationPath, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s: %d", method, w.Code)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestCelebrationComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_CELEBRATION_NOT_STARTED":     {codes.FailedPrecondition, http.StatusConflict, "celebration-not-started"},
		"ERROR_REASON_CELEBRATION_NO_WINNER":       {codes.FailedPrecondition, http.StatusConflict, "celebration-no-winner"},
		"ERROR_REASON_CELEBRATION_NOT_PARTICIPANT": {codes.FailedPrecondition, http.StatusConflict, "celebration-not-participant"},
		"ERROR_REASON_COMPONENT_FROZEN":            {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_PERMISSION_DENIED":           {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{celebrationComponentService: &fakeCelebrations{err: coreErr(want.grpc, reason)}})
		if w := as(h, "PUT", celebrationPath, `{}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}

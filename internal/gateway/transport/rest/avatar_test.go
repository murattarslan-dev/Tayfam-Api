package rest

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

func (f *fakeCore) SetAvatar(ctx context.Context, in *identityv1.SetAvatarRequest, _ ...grpc.CallOption) (*identityv1.SetAvatarResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.SetAvatarResponse{AvatarUploadId: in.GetUploadId()}, nil
}

func (f *fakeCore) ClearAvatar(ctx context.Context, in *identityv1.ClearAvatarRequest, _ ...grpc.CallOption) (*identityv1.ClearAvatarResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.ClearAvatarResponse{}, nil
}

func TestAvatarRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "PUT", "/v1/me/avatar", `{"upload_id":"`+uploadID+`"}`)
	if w.Code != http.StatusOK || decodeBody[avatarJSON](t, w).AvatarUploadID != uploadID {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if in, token, _ := f.last(); in.(*identityv1.SetAvatarRequest).GetUploadId() != uploadID || token != "USERTOKEN" {
		t.Fatal("identity'ye giden istek")
	}
	if w := as(h, "PUT", "/v1/me/avatar", `{"upload_id":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bozuk kimlik: %d", w.Code)
	}
	if w := as(h, "DELETE", "/v1/me/avatar", ""); w.Code != http.StatusNoContent {
		t.Fatalf("kaldırma: %d", w.Code)
	}
	st, _ := status.New(codes.FailedPrecondition, "x").WithDetails(&errdetails.ErrorInfo{Domain: "identity.tayfam.app", Reason: "ERROR_REASON_UPLOAD_NOT_READY"})
	w = as(coreRouter(&fakeCore{err: st.Err()}), "PUT", "/v1/me/avatar", `{"upload_id":"`+uploadID+`"}`)
	if w.Code != http.StatusConflict || problemType(t, w) != "upload-not-ready" {
		t.Fatalf("hazır değil: %d %s", w.Code, problemType(t, w))
	}
}

// Covers and catch photos are passed to core with their mask path; "" in a
// PATCH removes the cover (issue #135).
func TestCoverAndPhotoFields(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	if w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"cover_upload_id":"`+uploadID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("tayfa kapağı: %d %s", w.Code, w.Body)
	}
	in, _, _ := f.last()
	if r := in.(*corev1.UpdateTayfaRequest); r.GetCoverUploadId() != uploadID || r.GetUpdateMask().GetPaths()[0] != "cover_upload_id" {
		t.Fatalf("tayfa isteği: %v", r)
	}
	if w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"cover_upload_id":""}`); w.Code != http.StatusOK {
		t.Fatalf("kapak kaldırma: %d", w.Code)
	}
	if w := as(h, "PATCH", "/v1/tayfas/"+tayfaID, `{"cover_upload_id":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bozuk kapak: %d", w.Code)
	}
	if w := as(h, "PATCH", "/v1/tayfas/"+tayfaID+"/events/"+eventID, `{"cover_upload_id":"`+uploadID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("etkinlik kapağı: %d %s", w.Code, w.Body)
	}
	in, _, _ = f.last()
	if r := in.(*corev1.UpdateEventRequest); r.GetCoverUploadId() != uploadID || r.GetUpdateMask().GetPaths()[0] != "cover_upload_id" {
		t.Fatalf("etkinlik isteği: %v", r)
	}
	body := `{"species":"Levrek","length_cm":40,"client_request_id":"` + uploadID + `","photo_upload_id":"` + uploadID + `"}`
	if w := as(h, "POST", "/v1/tayfas/"+tayfaID+"/events/"+eventID+"/fishing-catches", body); w.Code != http.StatusCreated {
		t.Fatalf("av fotoğrafı: %d %s", w.Code, w.Body)
	}
	in, _, _ = f.last()
	if in.(*corev1.AddFishingCatchRequest).GetPhotoUploadId() != uploadID {
		t.Fatal("av fotoğrafı iletilmeli")
	}
}

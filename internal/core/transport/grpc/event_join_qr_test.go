package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/joinqr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeJoinQRs records what transport passed on.
type fakeJoinQRs struct {
	err   error
	calls int
	user  id.ID
	tayfa id.ID
	event id.ID
	token string
	res   joinqr.Result
}

func (f *fakeJoinQRs) Create(_ context.Context, u, t, e id.ID) (event.JoinQR, error) {
	f.calls++
	f.user, f.tayfa, f.event = u, t, e
	return event.JoinQR{Token: "QRTOKEN", ExpiresAt: time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)}, f.err
}

func (f *fakeJoinQRs) Join(_ context.Context, u id.ID, token string) (joinqr.Result, error) {
	f.calls++
	f.user, f.token = u, token
	return f.res, f.err
}

// QR ile katılım (issue #264): the IDs and the token reach the app, the
// result is converted, errors map to their reasons, and without the
// service the methods are unimplemented.
func TestEventServerJoinQR(t *testing.T) {
	user, tayfaID, eventID := id.New(), id.New(), id.New()
	f := &fakeJoinQRs{res: joinqr.Result{
		TayfaID: tayfaID, EventID: eventID, RSVP: event.RSVPGoing,
		Attendance: event.Attendance{Status: event.AttendanceAttended, Method: event.MethodQR},
	}}
	srv := NewEventServer(&fakeEvents{}, discard).WithJoinQR(f)
	ctx := callerCtx(user)

	c, err := srv.CreateEventJoinQr(ctx, &corev1.CreateEventJoinQrRequest{TayfaId: tayfaID.String(), EventId: eventID.String()})
	if err != nil || f.user != user || f.tayfa != tayfaID || f.event != eventID || c.GetToken() != "QRTOKEN" ||
		!c.GetExpireTime().AsTime().Equal(time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("token: %v %v", c, err)
	}
	j, err := srv.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{Token: "AbCdEfGhIjKlMnOpQrStUv"})
	if err != nil || f.token != "AbCdEfGhIjKlMnOpQrStUv" || j.GetTayfaId() != tayfaID.String() || j.GetEventId() != eventID.String() ||
		j.GetPending() || j.GetRsvp() != corev1.RsvpStatus_RSVP_STATUS_GOING || j.GetAttendance() != corev1.AttendanceStatus_ATTENDANCE_STATUS_ATTENDED {
		t.Fatalf("katılım: %v %v", j, err)
	}
	f.res = joinqr.Result{TayfaID: tayfaID, EventID: eventID, Pending: true}
	j, err = srv.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{Token: "AbCdEfGhIjKlMnOpQrStUv"})
	if err != nil || !j.GetPending() || j.GetRsvp() != corev1.RsvpStatus_RSVP_STATUS_UNSPECIFIED {
		t.Fatalf("istek: %v %v", j, err)
	}

	calls := f.calls
	if _, err := srv.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("boş token: %v", err)
	}
	if _, err := srv.CreateEventJoinQr(ctx, &corev1.CreateEventJoinQrRequest{TayfaId: tayfaID.String(), EventId: "x"}); err == nil {
		t.Error("geçersiz etkinlik kimliği kabul edildi")
	}
	if _, err := srv.JoinEventByQr(context.Background(), &corev1.JoinEventByQrRequest{Token: "x"}); codeOf(t, err) != codes.Unauthenticated {
		t.Errorf("kimliksiz: %v", err)
	}
	if f.calls != calls {
		t.Error("hatalı istek uygulamaya gitti")
	}

	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason corev1.ErrorReason
	}{
		{apperr.ErrInvalidJoinQR, codes.NotFound, corev1.ErrorReason_ERROR_REASON_INVALID_JOIN_QR},
		{apperr.ErrJoinQRLimit, codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_JOIN_QR_LIMIT_REACHED},
	} {
		f.err = tc.err
		_, err := srv.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{Token: "AbCdEfGhIjKlMnOpQrStUv"})
		if code, info := infoOf(t, err); code != tc.code || info.GetReason() != tc.reason.String() {
			t.Errorf("%v: %v", tc.err, err)
		}
	}

	bare := NewEventServer(&fakeEvents{}, discard)
	if _, err := bare.JoinEventByQr(ctx, &corev1.JoinEventByQrRequest{Token: "x"}); codeOf(t, err) != codes.Unimplemented {
		t.Errorf("servissiz: %v", err)
	}
}

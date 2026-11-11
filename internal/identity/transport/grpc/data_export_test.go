package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/dataexport"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeExports struct {
	caller dataexport.Caller
	user   id.ID
	called int
	err    error
}

func (f *fakeExports) Create(_ context.Context, c dataexport.Caller, _ id.ID) (dataexport.Request, error) {
	f.caller, f.called = c, f.called+1
	return dataexport.Request{ID: id.New(), Status: dataexport.StatusPending, RequestedAt: time.Now()}, f.err
}

func (f *fakeExports) List(_ context.Context, u id.ID) ([]dataexport.Request, time.Time, error) {
	f.user, f.called = u, f.called+1
	return []dataexport.Request{{ID: id.New(), Status: dataexport.StatusFailed, FailureReason: dataexport.FailTooLarge}}, time.Now().Add(time.Hour), f.err
}

func (f *fakeExports) Get(_ context.Context, u, _ id.ID) (dataexport.Request, error) {
	f.user, f.called = u, f.called+1
	return dataexport.Request{Status: dataexport.StatusReady, ReadyAt: time.Now(), ExpiresAt: time.Now(), SizeBytes: 9}, f.err
}

func (f *fakeExports) Download(_ context.Context, c dataexport.Caller, _ id.ID) (dataexport.Download, error) {
	f.caller, f.called = c, f.called+1
	return dataexport.Download{URL: "https://r2.example/x", Key: make([]byte, 32), Size: 9, Format: dataexport.Format, Expires: time.Now()}, f.err
}

func TestDataExportServer(t *testing.T) {
	f := &fakeExports{}
	srv := NewDataExportServer(f, testAudience, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u, sid := id.New(), id.New()
	iat := time.Now().Add(-time.Minute)
	exp := iat.Add(15 * time.Minute)
	ctx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: sid.String(), Audience: testAudience, IssuedAt: iat, ExpiresAt: exp})

	// A registration-only token or none reaches nothing.
	for name, c := range map[string]context.Context{
		"token yok":     context.Background(),
		"kayıt token'ı": auth.WithCallerForTest(context.Background(), auth.Claims{Subject: u.String(), SessionID: sid.String(), Audience: "tayfam-registration"}),
	} {
		if _, err := srv.CreateDataExport(c, &identityv1.CreateDataExportRequest{ClientRequestId: id.New().String()}); codeOfErr(err) != codes.Unauthenticated {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.called != 0 {
		t.Fatal("uygulama katmanı çağrıldı")
	}

	r, err := srv.CreateDataExport(ctx, &identityv1.CreateDataExportRequest{ClientRequestId: id.New().String()})
	if err != nil || r.GetDataExport().GetStatus() != identityv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING ||
		f.caller != (dataexport.Caller{User: u, Session: sid, IssuedAt: iat, ExpiresAt: exp}) {
		t.Fatalf("talep: %v %v %+v", r, err, f.caller)
	}
	if _, err := srv.CreateDataExport(ctx, &identityv1.CreateDataExportRequest{ClientRequestId: "x"}); codeOfErr(err) != codes.InvalidArgument {
		t.Fatalf("bozuk client_request_id: %v", err)
	}
	l, err := srv.ListDataExports(ctx, &identityv1.ListDataExportsRequest{})
	if err != nil || f.user != u || l.GetNextRequestTime() == nil ||
		l.GetDataExports()[0].GetFailureReason() != identityv1.DataExportFailureReason_DATA_EXPORT_FAILURE_REASON_TOO_LARGE {
		t.Fatalf("liste: %v %v", l, err)
	}
	g, err := srv.GetDataExport(ctx, &identityv1.GetDataExportRequest{DataExportId: id.New().String()})
	if err != nil || g.GetDataExport().GetSizeBytes() != 9 || g.GetDataExport().GetDownloadTime() != nil {
		t.Fatalf("okuma: %v %v", g, err)
	}
	d, err := srv.GetDataExportDownload(ctx, &identityv1.GetDataExportDownloadRequest{DataExportId: id.New().String()})
	if err != nil || len(d.GetKey()) != 32 || d.GetFormat() != "aes-256-gcm-v1" {
		t.Fatalf("indirme: %v %v", d, err)
	}
	for _, bad := range []string{"", "x"} {
		if _, err := srv.GetDataExportDownload(ctx, &identityv1.GetDataExportDownloadRequest{DataExportId: bad}); codeOfErr(err) != codes.NotFound {
			t.Errorf("%q: %v", bad, err)
		}
	}

	for appErr, want := range map[error]struct {
		code   codes.Code
		reason string
	}{
		dataexport.ErrNotFound:                 {codes.NotFound, "ERROR_REASON_DATA_EXPORT_NOT_FOUND"},
		dataexport.ErrInProgress:               {codes.FailedPrecondition, "ERROR_REASON_DATA_EXPORT_IN_PROGRESS"},
		dataexport.ErrLimit:                    {codes.ResourceExhausted, "ERROR_REASON_DATA_EXPORT_LIMIT"},
		dataexport.ErrNotReady:                 {codes.FailedPrecondition, "ERROR_REASON_DATA_EXPORT_NOT_READY"},
		dataexport.ErrExpired:                  {codes.FailedPrecondition, "ERROR_REASON_DATA_EXPORT_EXPIRED"},
		dataexport.ErrReauthenticationRequired: {codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED"},
		dataexport.ErrUnavailable:              {codes.Unavailable, ""},
		dataexport.ErrTokenExpiring:            {codes.Unauthenticated, ""},
		errors.New("db"):                       {codes.Internal, ""},
	} {
		f.err = appErr
		_, err := srv.GetDataExportDownload(ctx, &identityv1.GetDataExportDownloadRequest{DataExportId: id.New().String()})
		code, reason := reasonOf(t, err)
		if code != want.code || reason != want.reason {
			t.Errorf("%v: %v %q", appErr, code, reason)
		}
	}
}

func codeOfErr(err error) codes.Code { return status.Code(err) }

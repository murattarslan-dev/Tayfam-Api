package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeUsers struct {
	err      error
	sanction platformadmin.SanctionInput
}

func (f *fakeUsers) SearchUsers(context.Context, auth.AdminClaims, string) ([]platformadmin.UserSummary, error) {
	return []platformadmin.UserSummary{{ID: id.New(), ActiveSanction: platformadmin.SanctionBan}}, f.err
}
func (f *fakeUsers) GetUser(_ context.Context, _ auth.AdminClaims, u id.ID) (platformadmin.UserDetail, error) {
	return platformadmin.UserDetail{UserSummary: platformadmin.UserSummary{ID: u}, ActiveSessions: 2}, f.err
}
func (f *fakeUsers) RevokeUserSessions(context.Context, auth.AdminClaims, id.ID, string) (int, error) {
	return 3, f.err
}
func (f *fakeUsers) SanctionUser(_ context.Context, _ auth.AdminClaims, in platformadmin.SanctionInput) (platformadmin.Sanction, error) {
	f.sanction = in
	return platformadmin.Sanction{ID: id.New(), Type: in.Type, StartsAt: time.Now()}, f.err
}
func (f *fakeUsers) LiftSanction(context.Context, auth.AdminClaims, id.ID, string) error {
	return f.err
}
func (f *fakeUsers) UserStats(context.Context, auth.AdminClaims) (platformadmin.UserStats, error) {
	return platformadmin.UserStats{Total: 10, Active: 4, Suspended: 1, Banned: 2, DeletionsPending: 3}, f.err
}

func adminCtx() context.Context {
	return auth.WithAdminCallerForTest(context.Background(), auth.AdminClaims{Subject: id.New().String()})
}

func TestAdminUserServerNeedsAdminCaller(t *testing.T) {
	srv := NewAdminUserServer(&fakeUsers{}, discard())
	userCtx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()})
	if _, err := srv.SearchUsers(userCtx, &identityv1.SearchUsersRequest{Query: "ayşe"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("uygulama çağıranı: %v", err)
	}
	resp, err := srv.SearchUsers(adminCtx(), &identityv1.SearchUsersRequest{Query: "ayşe"})
	if err != nil || len(resp.GetUsers()) != 1 || resp.GetUsers()[0].GetActiveSanction() != identityv1.SanctionType_SANCTION_TYPE_BAN {
		t.Fatalf("arama = %v %v", resp, err)
	}
}

func TestSanctionUserMapsRequest(t *testing.T) {
	f := &fakeUsers{}
	srv := NewAdminUserServer(f, discard())
	u, rep := id.New(), id.New()
	end := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	_, err := srv.SanctionUser(adminCtx(), &identityv1.SanctionUserRequest{UserId: u.String(), Type: identityv1.SanctionType_SANCTION_TYPE_SUSPEND,
		Reason: "Tekrarlanan kural ihlali", EndTime: timestamppb.New(end), ReportId: rep.String()})
	if err != nil || f.sanction.User != u || f.sanction.Type != platformadmin.SanctionSuspend || !f.sanction.EndsAt.Equal(end) || f.sanction.Report != rep {
		t.Fatalf("girdi = %+v %v", f.sanction, err)
	}
	for _, req := range []*identityv1.SanctionUserRequest{
		{UserId: "x", Type: identityv1.SanctionType_SANCTION_TYPE_BAN},
		{UserId: u.String()},
		{UserId: u.String(), Type: identityv1.SanctionType_SANCTION_TYPE_BAN, ReportId: "x"},
	} {
		if _, err := srv.SanctionUser(adminCtx(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", req, err)
		}
	}
}

func TestAdminUserErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{platformadmin.ErrNotAdmin, codes.Unauthenticated, ""},
		{platformadmin.ErrReauthenticationRequired, codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED"},
		{platformadmin.ErrReasonRequired, codes.InvalidArgument, "ERROR_REASON_REASON_REQUIRED"},
		{platformadmin.ErrUserNotFound, codes.NotFound, "ERROR_REASON_USER_NOT_FOUND"},
		{platformadmin.ErrSanctionConflict, codes.FailedPrecondition, "ERROR_REASON_SANCTION_CONFLICT"},
		{errors.New("veritabanı ayrıntısı"), codes.Internal, ""},
	} {
		srv := NewAdminUserServer(&fakeUsers{err: tc.err}, discard())
		_, err := srv.LiftSanction(adminCtx(), &identityv1.LiftSanctionRequest{UserId: id.New().String(), Reason: "Yeterince uzun gerekçe"})
		if code, reason := reasonOf(t, err); code != tc.code || reason != tc.reason {
			t.Errorf("%v: %v %q", tc.err, code, reason)
		}
	}
}

type fakeAudits struct{ got platformadmin.CoreEntry }

func (f *fakeAudits) RecordFromCore(_ context.Context, e platformadmin.CoreEntry) error {
	f.got = e
	return nil
}

func TestRecordPlatformAudit(t *testing.T) {
	f := &fakeAudits{}
	srv := NewPlatformAuditServer(f, discard())
	ev, actor, tf := id.New(), id.New(), id.New()
	req := &identityv1.RecordPlatformAuditRequest{EventId: ev.String(), ActorId: actor.String(), OccurTime: timestamppb.Now(),
		Action: "tayfa.suspend", TargetType: "tayfa", TargetId: tf.String(), Reason: "Kurallara aykırı içerik", TayfaId: tf.String()}
	if _, err := srv.RecordPlatformAudit(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("servis çağıranı olmadan: %v", err)
	}
	ctx := auth.WithServiceCallerForTest(context.Background(), auth.ServiceClaims{})
	if _, err := srv.RecordPlatformAudit(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.got.EventID != ev || f.got.Actor != actor || f.got.TargetID != tf || f.got.Metadata["tayfa_id"] != tf.String() {
		t.Fatalf("kayıt = %+v", f.got)
	}
	req.UserId = "x"
	if _, err := srv.RecordPlatformAudit(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bozuk kimlik: %v", err)
	}
}

package grpc

import (
	"context"
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

type fakeAuditLog struct {
	err    error
	filter platformadmin.AuditFilter
	size   int
	token  string
}

func (f *fakeAuditLog) ListAuditLogs(_ context.Context, _ auth.AdminClaims, flt platformadmin.AuditFilter, size int, token string) (platformadmin.AuditPage, error) {
	f.filter, f.size, f.token = flt, size, token
	return platformadmin.AuditPage{
		Entries: []platformadmin.AuditEntry{{ID: id.New(), Actor: id.New(), Action: "tayfa.suspend", TargetType: "tayfa", TargetID: id.New(), Reason: "kural ihlali tekrarlandı", CreatedAt: time.Now()}},
		Next:    "sonraki",
	}, f.err
}

func TestListPlatformAuditLogsMapsRequest(t *testing.T) {
	f := &fakeAuditLog{}
	srv := NewAdminAuditServer(f, discard())
	userCtx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()})
	if _, err := srv.ListPlatformAuditLogs(userCtx, &identityv1.ListPlatformAuditLogsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("uygulama çağıranı: %v", err)
	}
	rel := id.New()
	from, to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	resp, err := srv.ListPlatformAuditLogs(adminCtx(), &identityv1.ListPlatformAuditLogsRequest{
		PageSize: 20, PageCursor: "t", Action: "user.ban", TargetType: "user", RelatedId: rel.String(),
		StartTime: timestamppb.New(from), EndTime: timestamppb.New(to),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := platformadmin.AuditFilter{Action: "user.ban", TargetType: "user", Related: rel, Since: from, Until: to}
	if f.filter != want || f.size != 20 || f.token != "t" {
		t.Fatalf("filtre = %+v %d %q", f.filter, f.size, f.token)
	}
	e := resp.GetEntries()[0]
	if resp.GetNextPageCursor() != "sonraki" || e.GetAction() != "tayfa.suspend" || e.GetTargetId() == "" || e.GetUserId() != "" {
		t.Fatalf("yanıt = %v", resp)
	}
	if _, err := srv.ListPlatformAuditLogs(adminCtx(), &identityv1.ListPlatformAuditLogsRequest{RelatedId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("geçersiz kimlik: %v", err)
	}
	f.err = platformadmin.ErrValidation
	if _, err := srv.ListPlatformAuditLogs(adminCtx(), &identityv1.ListPlatformAuditLogsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("doğrulama: %v", err)
	}
	f.err = platformadmin.ErrNotAdmin
	if _, err := srv.ListPlatformAuditLogs(adminCtx(), &identityv1.ListPlatformAuditLogsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("yönetici değil: %v", err)
	}
}

func TestGetUserStats(t *testing.T) {
	f := &fakeUsers{}
	srv := NewAdminUserServer(f, discard())
	userCtx := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()})
	if _, err := srv.GetUserStats(userCtx, &identityv1.GetUserStatsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("uygulama çağıranı: %v", err)
	}
	resp, err := srv.GetUserStats(adminCtx(), &identityv1.GetUserStatsRequest{})
	if err != nil || resp.GetTotalUsers() != 10 || resp.GetActiveUsers() != 4 || resp.GetBannedUsers() != 2 || resp.GetDeletionsPending() != 3 {
		t.Fatalf("sayılar = %v %v", resp, err)
	}
}

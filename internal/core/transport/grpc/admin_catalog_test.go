package grpc

import (
	"context"
	"log/slog"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/admin"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestCatalogMethodsNeedAdminCaller: every catalog method answers
// Unauthenticated without the admin caller the guard sets, before the app
// layer (issue #262).
func TestCatalogMethodsNeedAdminCaller(t *testing.T) {
	srv := NewAdminServer(nil, slog.New(slog.DiscardHandler))
	for _, c := range []context.Context{
		context.Background(),
		auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()}),
	} {
		calls := map[string]func() error{
			"GetCatalogAdmin": func() error { _, err := srv.GetCatalogAdmin(c, &corev1.GetCatalogAdminRequest{}); return err },
			"SaveCatalogInterest": func() error {
				_, err := srv.SaveCatalogInterest(c, &corev1.SaveCatalogInterestRequest{ValidateOnly: true})
				return err
			},
			"SaveCatalogEventType": func() error {
				_, err := srv.SaveCatalogEventType(c, &corev1.SaveCatalogEventTypeRequest{ValidateOnly: true})
				return err
			},
			"SaveCatalogBadge": func() error {
				_, err := srv.SaveCatalogBadge(c, &corev1.SaveCatalogBadgeRequest{ValidateOnly: true})
				return err
			},
			"SaveCatalogStudyTag": func() error {
				_, err := srv.SaveCatalogStudyTag(c, &corev1.SaveCatalogStudyTagRequest{ValidateOnly: true})
				return err
			},
			"SaveCatalogScorePreset": func() error {
				_, err := srv.SaveCatalogScorePreset(c, &corev1.SaveCatalogScorePresetRequest{ValidateOnly: true})
				return err
			},
			"BlockDomain":   func() error { _, err := srv.BlockDomain(c, &corev1.BlockDomainRequest{}); return err },
			"UnblockDomain": func() error { _, err := srv.UnblockDomain(c, &corev1.UnblockDomainRequest{}); return err },
		}
		for name, call := range calls {
			if err := call(); status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}

// TestCatalogProblemStatus: a failed check is ERROR_REASON_VALIDATION with
// the field and the problem for the panel.
func TestCatalogProblemStatus(t *testing.T) {
	srv := NewAdminServer(nil, slog.New(slog.DiscardHandler))
	err := srv.catalogError(context.Background(), &admin.ProblemError{Field: "definition", Problem: "tanım: components[0].kind: bilinmeyen değer"})
	st := status.Convert(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("kod %v", st.Code())
	}
	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if i, ok := d.(*errdetails.ErrorInfo); ok {
			info = i
		}
	}
	if info == nil || info.GetReason() != "ERROR_REASON_VALIDATION" || info.GetMetadata()["field"] != "definition" ||
		info.GetMetadata()["problem"] == "" {
		t.Fatalf("ayrıntı = %v", info)
	}
}

func TestCatalogConversions(t *testing.T) {
	if catalogStatusOf(corev1.CatalogStatus_CATALOG_STATUS_RETIRED) != catalog.StatusRetired ||
		catalogStatusOf(corev1.CatalogStatus_CATALOG_STATUS_UNSPECIFIED) != "" || catalogStatusOf(corev1.CatalogStatus(99)) != "" {
		t.Fatal("durum")
	}
	// An unknown mode stays in the list as "", which the app layer refuses.
	if m := eventModesOf([]corev1.EventMode{corev1.EventMode_EVENT_MODE_ONLINE, corev1.EventMode(9)}); len(m) != 2 || m[0] != catalog.ModeOnline || m[1] != "" {
		t.Fatalf("modlar = %v", m)
	}
	if reverse(badgeScopesPB, corev1.BadgeScope_BADGE_SCOPE_TAYFA) != catalog.BadgeScopeTayfa ||
		reverse(badgeScopesPB, corev1.BadgeScope_BADGE_SCOPE_UNSPECIFIED) != "" ||
		reverse(badgePeriodsPB, corev1.BadgePeriod_BADGE_PERIOD_YEAR) != catalog.PeriodYear {
		t.Fatal("rozet dönüşümleri")
	}
	off := false
	pb := catalogCheckPB(admin.CheckResult{
		MinClientVersion: "0.3.0", Version: 2, NewVersion: true,
		Components: []definition.Component{
			{Kind: definition.CompRoute, Key: "route"},
			{Kind: definition.CompTimer, Key: "timer", Optional: &off},
		},
	})
	if len(pb.GetComponents()) != 2 || pb.GetComponents()[0].GetOptional() || !pb.GetComponents()[0].GetDefaultOn() ||
		!pb.GetComponents()[1].GetOptional() || pb.GetComponents()[1].GetDefaultOn() ||
		pb.GetComponents()[1].GetMinClientVersion() != definition.MinClientVersions[definition.CompTimer] || pb.GetVersion() != 2 {
		t.Fatalf("önizleme = %v", pb)
	}
}

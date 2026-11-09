package grpc

import (
	"context"
	"log/slog"
	"regexp"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// forbidden are field names the panel's contracts must never carry (Notion
// "Panelin göremedikleri"): locations, poll votes, birth dates, e-mail
// addresses, IPs, devices and tokens; and no way to act as a user.
var forbidden = regexp.MustCompile(`(^|_)(lat|lng|latitude|longitude|location|coordinates?|meeting|votes?|voters?|birth|email|ip|device|token|password|impersonat\w*|act_as|on_behalf)($|_)`)

// TestPanelContractsCannotSee walks every request and response message of
// core's AdminService and identity's AdminUserService and AdminAuditService,
// nested ones too.
func TestPanelContractsCannotSee(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] || m.ParentFile().Package() == "google.protobuf" {
			return
		}
		seen[m.FullName()] = true
		fs := m.Fields()
		for i := range fs.Len() {
			f := fs.Get(i)
			if forbidden.MatchString(string(f.Name())) {
				t.Errorf("%s.%s panel sözleşmesinde olmamalı", m.FullName(), f.Name())
			}
			if f.Message() != nil {
				walk(f.Message())
			}
		}
	}
	services := []protoreflect.ServiceDescriptor{
		corev1.File_tayfam_core_v1_admin_proto.Services().ByName("AdminService"),
		identityv1.File_tayfam_identity_v1_admin_proto.Services().ByName("AdminUserService"),
		identityv1.File_tayfam_identity_v1_admin_proto.Services().ByName("AdminAuditService"),
	}
	n := 0
	for _, s := range services {
		ms := s.Methods()
		for i := range ms.Len() {
			walk(ms.Get(i).Input())
			walk(ms.Get(i).Output())
			n++
		}
	}
	if n < 18 {
		t.Fatalf("yalnızca %d metod bulundu", n)
	}
}

// TestGetStatsNeedsAdminCaller: without the admin caller the guard sets
// (an app user, or nothing), GetStats answers Unauthenticated before
// reaching the app layer.
func TestGetStatsNeedsAdminCaller(t *testing.T) {
	srv := NewAdminServer(nil, slog.New(slog.DiscardHandler))
	for _, c := range []context.Context{
		context.Background(),
		auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()}),
	} {
		if _, err := srv.GetStats(c, &corev1.GetStatsRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("GetStats = %v", err)
		}
	}
}

package grpc

import (
	"regexp"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// forbidden are field names the panel's contracts must never carry (Notion
// "Panelin göremedikleri"): locations, poll votes, birth dates, e-mail
// addresses, IPs, devices and tokens; and no way to act as a user.
var forbidden = regexp.MustCompile(`(^|_)(lat|lng|latitude|longitude|location|coordinates?|meeting|votes?|voters?|birth|email|ip|device|token|password|impersonat\w*|act_as|on_behalf)($|_)`)

// TestPanelContractsCannotSee walks every request and response message of
// core's AdminService and identity's AdminUserService, nested ones too.
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
	if n < 15 {
		t.Fatalf("yalnızca %d metod bulundu", n)
	}
}

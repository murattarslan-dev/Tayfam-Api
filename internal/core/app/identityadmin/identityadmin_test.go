package identityadmin

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

type fakeAdmin struct{ err error }

func (f fakeAdmin) GetAdmin(context.Context, *identityv1.GetAdminRequest, ...grpc.CallOption) (*identityv1.GetAdminResponse, error) {
	return &identityv1.GetAdminResponse{}, f.err
}

func TestChecker(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{nil, nil},
		{status.Error(codes.Unauthenticated, "x"), apperr.ErrNotPlatformAdmin},
		{status.Error(codes.PermissionDenied, "x"), apperr.ErrNotPlatformAdmin},
		{status.Error(codes.Unavailable, "x"), apperr.ErrUnavailable},
	} {
		if got := NewChecker(fakeAdmin{tc.err}).Check(context.Background()); !errors.Is(got, tc.want) {
			t.Errorf("%v: %v", tc.err, got)
		}
	}
}

func TestRequest(t *testing.T) {
	admin, tf := id.New(), id.New()
	e := outbox.Event{ID: id.New(), Type: EventAudit, Actor: admin, OccurredAt: time.Now(),
		Payload: []byte(`{"action":"tayfa.suspend","target_type":"tayfa","target_id":"` + tf.String() + `","reason":"Kurallara aykırı içerik","admin_session_id":"` + admin.String() + `","tayfa_id":"` + tf.String() + `"}`)}
	req, err := Request(e)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetEventId() != e.ID.String() || req.GetActorId() != admin.String() || req.GetAction() != "tayfa.suspend" ||
		req.GetTargetId() != tf.String() || req.GetTayfaId() != tf.String() || req.GetReason() == "" {
		t.Fatalf("istek = %v", req)
	}
	e.Actor = id.ID{}
	if _, err := Request(e); err == nil {
		t.Fatal("aktörsüz denetim olayı kabul edildi")
	}
	if c := AuditConsumer(nil); c.Name != "identity" || len(c.Types) != 1 || c.Types[0] != EventAudit {
		t.Fatalf("tüketici = %v", c)
	}
}

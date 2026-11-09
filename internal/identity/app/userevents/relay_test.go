package userevents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

type fakeCore struct {
	got  []*corev1.ApplyUserEventRequest
	fail bool
}

func (f *fakeCore) ApplyUserEvent(_ context.Context, req *corev1.ApplyUserEventRequest, _ ...grpc.CallOption) (*corev1.ApplyUserEventResponse, error) {
	if f.fail {
		return nil, errors.New("core kapalı")
	}
	f.got = append(f.got, req)
	return &corev1.ApplyUserEventResponse{}, nil
}

func TestCoreConsumerDelivers(t *testing.T) {
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	ctx := context.Background()
	user := id.New()
	avatar := id.New().String()
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, user, Profile{DisplayName: "Deniz", AvatarUploadID: avatar})
	})
	// An event of another type is not sent to core.
	inTx(t, pool, func(tx pgx.Tx) error {
		_, err := outbox.Write(ctx, tx, outbox.NewEvent{Type: "device.removed"})
		return err
	})
	inTx(t, pool, func(tx pgx.Tx) error { return Deleted(ctx, tx, user) })

	core := &fakeCore{fail: true}
	consumer := CoreConsumer(core)
	r, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Consumers: []outbox.Consumer{consumer}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Step(ctx, consumer); err == nil {
		t.Fatal("core kapalıyken hata bekleniyordu")
	}
	core.fail = false
	if n, err := r.Step(ctx, consumer); err != nil || n != 1 {
		// Deleted removed the profile event, so only user.deleted is left.
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if len(core.got) != 1 || core.got[0].GetDeleted() == nil || core.got[0].GetUserId() != user.String() || core.got[0].GetOccurredAt() == nil {
		t.Fatalf("core'a giden = %v", core.got)
	}

	// A live profile event carries the name and avatar only.
	other := id.New()
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, other, Profile{DisplayName: "Ece", AvatarUploadID: avatar})
	})
	if n, err := r.Step(ctx, consumer); err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	p := core.got[1].GetProfileUpdated()
	if p.GetDisplayName() != "Ece" || p.GetAvatarUploadId() != avatar || core.got[1].GetUserId() != other.String() {
		t.Fatalf("profil = %v", core.got[1])
	}
	if _, ok := id.Parse(core.got[1].GetEventId()); !ok {
		t.Fatal("olay kimliği geçersiz")
	}
}

func TestCoreRequestRejectsUnknown(t *testing.T) {
	for name, e := range map[string]outbox.Event{
		"konu yok":       {ID: id.New(), Type: TypeDeleted},
		"bilinmeyen tür": {ID: id.New(), Type: "user.renamed", SubjectType: "user", SubjectID: id.New()},
		"bozuk içerik":   {ID: id.New(), Type: TypeProfileUpdated, SubjectType: "user", SubjectID: id.New(), Payload: []byte("{")},
		"bilinmeyen görünürlük": {ID: id.New(), Type: TypeProfileUpdated, SubjectType: "user", SubjectID: id.New(),
			Payload: []byte(`{"display_name": "Ece", "activity_visibility": "friends"}`)},
	} {
		if _, err := coreRequest(e); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
}

func TestCoreRequestCarriesVisibility(t *testing.T) {
	for payload, want := range map[string]corev1.ActivityVisibility{
		`{"display_name": "Ece"}`:                                   corev1.ActivityVisibility_ACTIVITY_VISIBILITY_UNSPECIFIED,
		`{"display_name": "Ece", "activity_visibility": "private"}`: corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PRIVATE,
		`{"display_name": "Ece", "activity_visibility": "tayfa"}`:   corev1.ActivityVisibility_ACTIVITY_VISIBILITY_TAYFA,
		`{"display_name": "Ece", "activity_visibility": "public"}`:  corev1.ActivityVisibility_ACTIVITY_VISIBILITY_PUBLIC,
	} {
		req, err := coreRequest(outbox.Event{ID: id.New(), Type: TypeProfileUpdated, SubjectType: "user", SubjectID: id.New(), Payload: []byte(payload)})
		if err != nil || req.GetProfileUpdated().GetActivityVisibility() != want {
			t.Errorf("%s: %v %v", payload, req, err)
		}
	}
}

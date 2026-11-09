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
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

type fakeChat struct {
	got  []*chatv1.ApplyUserEventRequest
	fail bool
}

func (f *fakeChat) ApplyUserEvent(_ context.Context, req *chatv1.ApplyUserEventRequest, _ ...grpc.CallOption) (*chatv1.ApplyUserEventResponse, error) {
	if f.fail {
		return nil, errors.New("chat kapalı")
	}
	f.got = append(f.got, req)
	return &chatv1.ApplyUserEventResponse{}, nil
}

// chat and core are separate consumers: chat being down does not hold up
// core, and chat gets the same events once it is back (issue #64).
func TestChatConsumerIsIndependent(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	ctx := context.Background()
	user := id.New()
	inTx(t, pool, func(tx pgx.Tx) error {
		return ProfileUpdated(ctx, tx, user, Profile{DisplayName: "Deniz"})
	})

	core, chat := &fakeCore{}, &fakeChat{fail: true}
	cc, hc := CoreConsumer(core), ChatConsumer(chat)
	r, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Consumers: []outbox.Consumer{cc, hc}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Step(ctx, hc); err == nil {
		t.Fatal("chat kapalıyken hata bekleniyordu")
	}
	if n, err := r.Step(ctx, cc); err != nil || n != 1 {
		t.Fatalf("core: n = %d, err = %v", n, err)
	}
	chat.fail = false
	inTx(t, pool, func(tx pgx.Tx) error { return Deleted(ctx, tx, user) })
	if n, err := r.Step(ctx, hc); err != nil || n != 1 {
		// Deleted removed the profile event, so only user.deleted is left.
		t.Fatalf("chat: n = %d, err = %v", n, err)
	}
	if len(chat.got) != 1 || chat.got[0].GetDeleted() == nil || chat.got[0].GetUserId() != user.String() {
		t.Fatalf("chat'e giden = %v", chat.got)
	}
}

func TestChatRequestCarriesProfile(t *testing.T) {
	user, avatar := id.New(), id.New()
	e := outbox.Event{ID: id.New(), Type: TypeProfileUpdated, SubjectType: subjectUser, SubjectID: user,
		Payload: []byte(`{"display_name":"Ece","avatar_upload_id":"` + avatar.String() + `"}`)}
	r, err := chatRequest(e)
	if err != nil {
		t.Fatal(err)
	}
	p := r.GetProfileUpdated()
	if r.GetEventId() != e.ID.String() || r.GetUserId() != user.String() || p.GetDisplayName() != "Ece" || p.GetAvatarUploadId() != avatar.String() {
		t.Fatalf("istek = %v", r)
	}
	if _, err := chatRequest(outbox.Event{ID: id.New(), Type: "device.removed", SubjectType: subjectUser, SubjectID: user, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("bilinmeyen tür kabul edildi")
	}
}

package grpc

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeReported struct {
	tayfa, message id.ID
	removed        bool
}

func (f *fakeReported) MessageRemoved(_ context.Context, tayfa, message id.ID) (bool, error) {
	f.tayfa, f.message = tayfa, message
	return f.removed, nil
}

// core asks whether a reported channel message is removed with its service
// token (issue #205, ADR-0031); the answer is one flag.
func TestGetChannelMessageStatus(t *testing.T) {
	f := &fakeReported{removed: true}
	s := NewReportedMessageServer(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := auth.WithServiceCallerForTest(context.Background(), auth.ServiceClaims{})
	tf, msg := id.New(), id.New()
	resp, err := s.GetChannelMessageStatus(ctx, &chatv1.GetChannelMessageStatusRequest{TayfaId: tf.String(), MessageId: msg.String()})
	if err != nil || !resp.GetRemoved() || f.tayfa != tf || f.message != msg {
		t.Fatalf("cevap = %v, %v", resp, err)
	}
	for name, r := range map[string]*chatv1.GetChannelMessageStatusRequest{
		"tayfa_id":   {TayfaId: "x", MessageId: msg.String()},
		"message_id": {TayfaId: tf.String()},
	} {
		if _, err := s.GetChannelMessageStatus(ctx, r); func() string { _, _, m := reasonOf(err); return m["field"] }() != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A user token never reaches it (the Guard refuses first; this is the
	// handler's own check).
	user := auth.WithCallerForTest(context.Background(), auth.Claims{Subject: id.New().String()})
	if _, err := s.GetChannelMessageStatus(user, &chatv1.GetChannelMessageStatusRequest{TayfaId: tf.String(), MessageId: msg.String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("kullanıcı token'ı: %v", err)
	}
}

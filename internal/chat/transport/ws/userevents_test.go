package ws

import (
	"context"
	"net/http"
	"testing"
	"time"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// identity's session.revoked and user.sanctioned, applied as the
// UserEventService applies them, close the live connections they name and
// no others (issue #92).
func TestUserEventsCloseConnections(t *testing.T) {
	e := newEnv(t, nil)
	svc, err := userevents.New(userevents.Config{Pool: dbtest.New(t, "chat", chatdb.Migrations()), Sessions: e.hub})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, other := id.New(), id.New()
	revoked, kept := e.token(user, time.Hour), e.token(user, time.Hour)
	bystander := e.token(other, time.Hour)
	cRevoked, cKept := e.connect(t, revoked), e.connect(t, kept)
	e.connect(t, bystander)

	// session.revoked: only that session's connection closes.
	if err := svc.Apply(ctx, userevents.Event{ID: id.New(), User: user, SessionRevoked: revoked.session}); err != nil {
		t.Fatal(err)
	}
	if code := closedWith(t, cRevoked); code != CloseSessionEnded {
		t.Fatalf("kapanış kodu %d", code)
	}
	waitConnections(t, e.hub, 2)
	if _, code, err := e.dial(t, revoked.token, ""); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("iptal edilen oturum yeniden bağlandı: %d %v", code, err)
	}

	// user.sanctioned: every connection of the user closes, and a new
	// session of the user cannot connect; another user's stays.
	if err := svc.Apply(ctx, userevents.Event{ID: id.New(), User: user, Sanctioned: true}); err != nil {
		t.Fatal(err)
	}
	if code := closedWith(t, cKept); code != CloseSessionEnded {
		t.Fatalf("kapanış kodu %d", code)
	}
	waitConnections(t, e.hub, 1)
	if _, code, err := e.dial(t, e.token(user, time.Hour).token, ""); err == nil || code != http.StatusUnauthorized {
		t.Fatalf("yaptırımlı kullanıcı bağlandı: %d %v", code, err)
	}
}

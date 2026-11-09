package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestSanctionedCannotSignInOrRefresh: an account under an active platform
// suspension or ban cannot open or refresh a session (ADR-0004 "Platform
// yaptırımları", issue #104); a lifted or expired one can again.
func TestSanctionedCannotSignInOrRefresh(t *testing.T) {
	for _, typ := range []string{"suspend", "ban"} {
		t.Run(typ, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			first := e.signIn(t, "sub-1")
			sid := id.New()
			var ends any
			if typ == "suspend" {
				ends = time.Now().Add(time.Hour)
			}
			if _, err := e.pool.Exec(ctx, `INSERT INTO platform_sanctions (id, user_id, type, reason, ends_at, created_by)
				VALUES ($1, $2, $3, 'Tekrarlanan kural ihlali', $4, $5)`, sid.PG(), first.UserID.PG(), typ, ends, id.New().PG()); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.SignIn(ctx, SignInInput{Provider: "google", IDToken: "ok:sub-1", Nonce: "n", Platform: "ios"}); !errors.Is(err, ErrSanctioned) {
				t.Fatalf("giriş: %v", err)
			}
			if _, err := e.svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrSanctioned) {
				t.Fatalf("yenileme: %v", err)
			}
			if _, err := e.pool.Exec(ctx, `UPDATE platform_sanctions SET lifted_at = now(), lifted_by = $2 WHERE id = $1`, sid.PG(), id.New().PG()); err != nil {
				t.Fatal(err)
			}
			e.signIn(t, "sub-1")
		})
	}
}

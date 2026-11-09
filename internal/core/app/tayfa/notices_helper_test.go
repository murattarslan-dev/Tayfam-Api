package tayfa

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// notice is one notification.requested event (ADR-0009, issue #64).
type notice struct {
	actor, target id.ID
	recipients    []id.ID
	payload       map[string]any
}

// noticesOf returns the notification.requested events of type typ, oldest
// first.
func noticesOf(t *testing.T, pool *pgxpool.Pool, typ string) []notice {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT coalesce(actor_id, '00000000-0000-0000-0000-000000000000'), subject_id, payload
		FROM domain_events WHERE type = $1 AND payload->>'type' = $2 ORDER BY id`, notify.EventType, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		var actor, target pgtype.UUID
		var raw []byte
		if err := rows.Scan(&actor, &target, &raw); err != nil {
			t.Fatal(err)
		}
		n.actor, n.target = id.ID(actor.Bytes), id.ID(target.Bytes)
		if err := json.Unmarshal(raw, &n.payload); err != nil {
			t.Fatal(err)
		}
		for _, v := range n.payload["recipient_ids"].([]any) {
			u, _ := id.Parse(v.(string))
			n.recipients = append(n.recipients, u)
		}
		out = append(out, n)
	}
	return out
}

// sameUsers compares recipient sets regardless of order.
func sameUsers(got []id.ID, want ...id.ID) bool {
	g, w := slices.Clone(got), slices.Clone(want)
	cmp := func(a, b id.ID) int { return slices.Compare(a[:], b[:]) }
	slices.SortFunc(g, cmp)
	slices.SortFunc(w, cmp)
	return slices.Equal(g, w)
}

package channel

import (
	"sync"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// messageIDs hands out strictly increasing message IDs in this process.
// Messages are paged and counted as unread by ID (UUIDv7, v14 "Okunma
// bilgisi"); id.New alone does not order two IDs made in the same
// millisecond. chat runs as one replica (ADR-0005), so this keeps the order
// in which messages were accepted.
type messageIDs struct {
	mu   sync.Mutex
	last id.ID
}

func (g *messageIDs) next() id.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = id.After(g.last)
	return g.last
}

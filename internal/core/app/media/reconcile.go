package media

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Reconciliation of chat attachments (issue #205, ADR-0031). chat has core
// attach a message's file just before it writes the message, outside its
// transaction, so no lock is held during the call. If the message is never
// written (chat rolled back, the client went away, a timeout after core
// committed) the attachment points at nothing. Such an attachment is not
// confirmed: it may move to the owner's next message (Attach), and after
// ReconcileAfter this job asks chat which message holds the file. A holder
// confirms the attachment (and corrects its message ID); no holder undoes
// it, so the file expires like any unattached upload instead of being
// stuck "in use" and counted against the quota.
const (
	// ReconcileAfter is how old an unconfirmed attachment must be before
	// chat is asked: well past any send in flight.
	ReconcileAfter = 10 * time.Minute
	reconcileBatch = 100
)

// HolderItem asks chat which message holds an upload.
type HolderItem struct {
	Upload id.ID
	// Direct is true for a direct message attachment.
	Direct bool
}

// MediaHolders asks chat with core's service token (MediaHolderService;
// ADR-0013): upload → the message that holds it, for the items some
// message holds. An error leaves every attachment as it is.
type MediaHolders interface {
	Holders(ctx context.Context, items []HolderItem) (map[id.ID]id.ID, error)
}

// Reconcile handles one batch of unconfirmed chat attachments older than
// ReconcileAfter and returns how many it confirmed and detached. Without a
// chat client it does nothing (a local run without the service token).
func (s *Service) Reconcile(ctx context.Context) (confirmed, detached int, err error) {
	if s.cfg.Holders == nil {
		return 0, 0, nil
	}
	now := s.now()
	rows, err := s.q.ListUnconfirmedChatAttachments(ctx, store.ListUnconfirmedChatAttachmentsParams{
		Before: ts(now.Add(-ReconcileAfter)), N: reconcileBatch,
	})
	if err != nil || len(rows) == 0 {
		return 0, 0, err
	}
	items := make([]HolderItem, len(rows))
	for i, r := range rows {
		items[i] = HolderItem{Upload: id.ID(r.ID.Bytes), Direct: r.AttachedType == TargetDMMessage}
	}
	// No database lock is held while chat answers; each update below is
	// conditional on the attachment being unchanged.
	holders, err := s.cfg.Holders.Holders(ctx, items)
	if err != nil {
		return 0, 0, fmt.Errorf("chat'e soru: %w", err)
	}
	for _, r := range rows {
		u := id.ID(r.ID.Bytes)
		if h, ok := holders[u]; ok {
			n, err := s.q.ConfirmChatAttachment(ctx, store.ConfirmChatAttachmentParams{
				HolderID: h.PG(), Now: ts(now), ID: r.ID, AttachedType: r.AttachedType, AskedID: r.AttachedID,
			})
			if err != nil {
				return confirmed, detached, fmt.Errorf("onay: %w", err)
			}
			confirmed += int(n)
			continue
		}
		n, err := s.q.DetachChatAttachment(ctx, store.DetachChatAttachmentParams{ID: r.ID, AttachedType: r.AttachedType, AskedID: r.AttachedID})
		if err != nil {
			return confirmed, detached, fmt.Errorf("bağlamayı kaldırma: %w", err)
		}
		detached += int(n)
	}
	return confirmed, detached, nil
}

// RunReconcile runs Reconcile every CleanupInterval until ctx is done.
// Running it in more than one pod is harmless: every update is
// conditional.
func (s *Service) RunReconcile(ctx context.Context) error {
	t := time.NewTicker(s.cfg.CleanupInterval)
	defer t.Stop()
	for {
		c, d, err := s.Reconcile(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.cfg.Log.WarnContext(ctx, "sohbet bağlamaları uzlaştırılamadı", slog.String("err", err.Error()))
		case c+d > 0:
			s.cfg.Log.InfoContext(ctx, "sohbet bağlamaları uzlaştırıldı", slog.Int("onaylanan", c), slog.Int("kaldırılan", d))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

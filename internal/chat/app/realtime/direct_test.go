package realtime

import (
	"io"
	"log/slog"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Direct message frames go to the two parties' connections only; the read
// marker goes to the other party only.
func TestDirectDelivery(t *testing.T) {
	h, _ := newTestHub(t, 10, 5)
	live, err := NewLive(LiveConfig{Hub: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	a, b, x := id.New(), id.New(), id.New()
	ca, ca2, cb, cx := &fakeConn{}, &fakeConn{}, &fakeConn{}, &fakeConn{}
	mustAdmit(t, h, a, id.New(), ca)
	mustAdmit(t, h, a, id.New(), ca2)
	mustAdmit(t, h, b, id.New(), cb)
	mustAdmit(t, h, x, id.New(), cx)

	p := live.Direct()
	conv := id.New()
	p.MessageCreated([2]id.ID{a, b}, dm.Message{ID: id.New(), ConversationID: conv, SenderID: a, Encryption: dm.EncryptionE2E})
	p.MessageDeleted([2]id.ID{a, b}, conv, id.New())
	p.ConversationAccepted([2]id.ID{a, b}, conv)
	// A deleted party (zero ID) is skipped.
	p.MessageCreated([2]id.ID{{}, b}, dm.Message{ID: id.New(), ConversationID: conv, Encryption: dm.EncryptionE2E})
	p.ConversationRead(a, b, conv, id.New())
	p.Notified(notification.Request{Type: notification.TypeDMMessage, TargetType: notification.TargetDMConversation, Target: conv, Recipients: []id.ID{b}})

	types := func(c *fakeConn) []string {
		var out []string
		for _, e := range c.got() {
			out = append(out, e.Type)
		}
		return out
	}
	want := map[*fakeConn][]string{
		ca:  {EventDMCreated, EventDMDeleted, EventDMAccepted, EventDMRead},
		ca2: {EventDMCreated, EventDMDeleted, EventDMAccepted, EventDMRead},
		cb:  {EventDMCreated, EventDMDeleted, EventDMAccepted, EventDMCreated, EventNotification},
		cx:  nil,
	}
	for c, w := range want {
		if got := types(c); len(got) != len(w) {
			t.Errorf("çerçeveler = %v, beklenen %v", got, w)
		} else {
			for i := range w {
				if got[i] != w[i] {
					t.Errorf("çerçeveler = %v, beklenen %v", got, w)
					break
				}
			}
		}
	}
}

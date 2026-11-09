package realtime

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDirect struct {
	token string
	in    dm.SendInput
}

func (f *fakeDirect) Send(ctx context.Context, caller id.ID, in dm.SendInput) (dm.Message, dm.Conversation, error) {
	f.token, f.in = tokenOf(ctx), in
	return dm.Message{ID: id.New(), SenderID: caller, Body: in.Body}, dm.Conversation{}, nil
}

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
	ma := mustAdmit(t, h, a, id.New(), ca)
	mustAdmit(t, h, a, id.New(), ca2)
	mustAdmit(t, h, b, id.New(), cb)
	mustAdmit(t, h, x, id.New(), cx)

	// Sending goes through dm.Service with the connection's own token.
	fd := &fakeDirect{}
	live.BindDirect(fd)
	if _, _, err := live.SendDirect(context.Background(), ma, dm.SendInput{Recipient: b, Body: "selam"}); err != nil {
		t.Fatal(err)
	}
	if fd.token != "TOKEN-"+a.String() || fd.in.Recipient != b {
		t.Fatalf("gönderim = %q %+v", fd.token, fd.in)
	}

	p := live.Direct()
	conv := id.New()
	p.MessageCreated([2]id.ID{a, b}, dm.Message{ID: id.New(), ConversationID: conv, SenderID: a, Body: "selam"})
	p.MessageDeleted([2]id.ID{a, b}, conv, id.New())
	p.ConversationAccepted([2]id.ID{a, b}, conv)
	// A deleted party (zero ID) is skipped.
	p.MessageCreated([2]id.ID{{}, b}, dm.Message{ID: id.New(), ConversationID: conv, Body: "x"})
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

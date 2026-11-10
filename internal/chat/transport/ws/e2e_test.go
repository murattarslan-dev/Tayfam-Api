package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/realtime"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeAcker stands in for keys.Service's Ack.
type fakeAcker struct {
	mu     sync.Mutex
	caller keys.Caller
	ids    []id.ID
	token  string
	err    error
}

func (f *fakeAcker) Ack(ctx context.Context, c keys.Caller, ids []id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caller, f.ids, f.token = c, ids, tokenOf(ctx)
	return f.err
}

// End-to-end envelopes reach only the connections of their device (a
// session), and the device acknowledges them over the WebSocket (issue
// #127).
func TestEnvelopeDeliveryAndAck(t *testing.T) {
	e := newEnv(t, nil)
	ack := &fakeAcker{}
	e.live.BindKeys(ack)
	u := id.New()
	dev, other := e.token(u, time.Hour), e.token(u, time.Hour)
	cd, co := e.connect(t, dev), e.connect(t, other)
	waitConnections(t, e.hub, 2)

	env := keys.Envelope{
		ID: id.New(), MessageID: id.New(), ConversationID: id.New(), SenderID: id.New(), SenderDevice: id.New(),
		SenderNumber: 2, Type: keys.EnvelopePreKey, Content: []byte{0, 1, 2, 250}, CreatedAt: time.Now().UTC(),
	}
	e.live.Direct().Envelopes([]keys.Delivery{{Session: dev.session, Envelope: env}})
	f := readFrame(t, cd)
	var got realtime.EnvelopeData
	if err := json.Unmarshal(f.Data, &got); err != nil || f.Type != realtime.EventDMEnvelope || got.ID != env.ID.String() ||
		got.Type != "prekey" || !bytes.Equal(got.Content, env.Content) || got.SenderDeviceNumber != 2 {
		t.Fatalf("zarf: %+v %s", f, f.Data)
	}
	// The user's other device got nothing: its next frame is its own answer.
	write(t, co, `{"type":"nope","id":"z"}`)
	if f := readFrame(t, co); f.Type != "error" || f.ID != "z" {
		t.Fatalf("diğer cihaz: %+v %s", f, f.Data)
	}

	write(t, cd, `{"type":"dm.envelope_ack","id":"a1","data":{"envelope_ids":["`+env.ID.String()+`"]}}`)
	if f := readFrame(t, cd); f.Type != "dm.envelope_acked" || f.ID != "a1" {
		t.Fatalf("onay: %+v %s", f, f.Data)
	}
	ack.mu.Lock()
	if ack.caller.User != u || ack.caller.Session != dev.session || len(ack.ids) != 1 || ack.ids[0] != env.ID || ack.token != dev.token {
		t.Fatalf("onay çağrısı: %+v", ack)
	}
	ack.err = apperr.ErrE2EDeviceNotRegistered
	ack.mu.Unlock()
	write(t, cd, `{"type":"dm.envelope_ack","id":"a2","data":{"envelope_ids":["`+env.ID.String()+`"]}}`)
	if d := errorOf(t, readFrame(t, cd)); d.Code != "e2e_device_not_registered" {
		t.Fatalf("cihazsız onay: %+v", d)
	}
	write(t, cd, `{"type":"dm.envelope_ack","id":"a3","data":{"envelope_ids":["kötü"]}}`)
	if d := errorOf(t, readFrame(t, cd)); d.Code != "invalid_request" || d.Field != "envelope_ids" {
		t.Fatalf("geçersiz onay: %+v", d)
	}

	// Pairing and key signals go to the one session too.
	pub := e.live.Keys()
	pub.PreKeysLow(dev.session, keys.Counts{EC: 3, Kyber: 40})
	if f := readFrame(t, cd); f.Type != realtime.EventPreKeysLow {
		t.Fatalf("azalma: %+v", f)
	}
	req := id.New()
	pub.LinkAnswered(dev.session, req)
	var la realtime.LinkAnsweredData
	if f := readFrame(t, cd); f.Type != realtime.EventLinkAnswered || json.Unmarshal(f.Data, &la) != nil || la.LinkRequestID != req.String() {
		t.Fatalf("eşleştirme: %+v", f)
	}
	pub.HistoryKeyShared(dev.session)
	if f := readFrame(t, cd); f.Type != realtime.EventHistoryKeyShared {
		t.Fatalf("paylaşım: %+v", f)
	}
}

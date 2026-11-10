package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// End-to-end frame types (issue #127, ADR-0018). They go to the
// connections of one session only: a device is a session.
const (
	// EventDMEnvelope: an envelope for this device. The client stores it,
	// then acknowledges it (dm.envelope_ack); delivery is at least once.
	EventDMEnvelope = "dm.envelope"
	// EventPreKeysLow: fewer than keys.LowPreKeys one-time pre-keys of a
	// kind are left; upload more.
	EventPreKeysLow = "e2e.prekeys_low"
	// EventLinkAnswered: the device's pairing request was answered.
	EventLinkAnswered = "e2e.link_answered"
	// EventHistoryKeyShared: a new history key share waits for the device.
	EventHistoryKeyShared = "e2e.history_key_shared"
)

// EnvelopeData is an envelope in a frame. Content is the opaque Signal
// message; it goes only to its device.
type EnvelopeData struct {
	ID                 string    `json:"id"`
	MessageID          string    `json:"message_id"`
	ConversationID     string    `json:"conversation_id"`
	SenderUserID       string    `json:"sender_user_id"`
	SenderDeviceID     string    `json:"sender_device_id"`
	SenderDeviceNumber int       `json:"sender_device_number"`
	Type               string    `json:"type"`
	Content            []byte    `json:"content"` // base64 in JSON
	CreatedAt          time.Time `json:"created_at"`
}

// EnvelopeTypes are the envelope type names in frames and REST.
var EnvelopeTypes = map[int]string{keys.EnvelopeWhisper: "whisper", keys.EnvelopePreKey: "prekey"}

// EnvelopeFrom converts an envelope for a frame.
func EnvelopeFrom(e keys.Envelope) EnvelopeData {
	return EnvelopeData{
		ID: e.ID.String(), MessageID: e.MessageID.String(), ConversationID: e.ConversationID.String(),
		SenderUserID: e.SenderID.String(), SenderDeviceID: e.SenderDevice.String(), SenderDeviceNumber: e.SenderNumber,
		Type: EnvelopeTypes[e.Type], Content: e.Content, CreatedAt: e.CreatedAt,
	}
}

// PreKeysLowData carries the device's one-time pre-key counts.
type PreKeysLowData struct {
	EC    int `json:"ec"`
	Kyber int `json:"kyber"`
}

// LinkAnsweredData names the answered pairing request.
type LinkAnsweredData struct {
	LinkRequestID string `json:"link_request_id"`
}

// Acker acknowledges envelopes (keys.Service).
type Acker interface {
	Ack(ctx context.Context, c keys.Caller, ids []id.ID) error
}

// BindKeys sets the end-to-end service; call it once, before serving.
func (l *Live) BindKeys(a Acker) { l.cfg.Keys = a }

// AckEnvelopes acknowledges envelopes of m's device (dm.envelope_ack).
func (l *Live) AckEnvelopes(ctx context.Context, m *Member, ids []id.ID) error {
	if l.cfg.Keys == nil {
		return errors.New("e2e servisi bağlı değil")
	}
	if err := l.cfg.Keys.Ack(m.asUser(ctx), keys.Caller{User: m.user, Session: m.session}, ids); err != nil {
		return fmt.Errorf("zarf onayı: %w", err)
	}
	return nil
}

// Keys returns Live as keys.Publisher.
func (l *Live) Keys() keys.Publisher { return keysPublisher{l} }

type keysPublisher struct{ l *Live }

func (p keysPublisher) PreKeysLow(session id.ID, c keys.Counts) {
	p.l.cfg.Hub.sendToSession(session, Event{Type: EventPreKeysLow, Data: PreKeysLowData{EC: c.EC, Kyber: c.Kyber}})
}

func (p keysPublisher) LinkAnswered(session, request id.ID) {
	p.l.cfg.Hub.sendToSession(session, Event{Type: EventLinkAnswered, Data: LinkAnsweredData{LinkRequestID: request.String()}})
}

func (p keysPublisher) HistoryKeyShared(session id.ID) {
	p.l.cfg.Hub.sendToSession(session, Event{Type: EventHistoryKeyShared, Data: struct{}{}})
}

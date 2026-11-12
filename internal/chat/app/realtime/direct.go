package realtime

import (
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/dm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Direct message frame types. Direct messages need no subscription: they
// go to every connection of the conversation's two parties.
const (
	EventDMCreated  = "dm.message_created"
	EventDMDeleted  = "dm.message_deleted"
	EventDMAccepted = "dm.conversation_accepted"
	EventDMRead     = "dm.read"
)

// DMData is a direct message in a frame, for the two parties only. It has
// no content: that arrives as a dm.envelope to each device (ADR-0018).
type DMData struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SenderID       string    `json:"sender_user_id,omitempty"`
	Encryption     string    `json:"encryption"`
	CreatedAt      time.Time `json:"created_at"`
}

// DMDeletedData is an erased direct message.
type DMDeletedData struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
}

// DMConversationData names a conversation.
type DMConversationData struct {
	ConversationID string `json:"conversation_id"`
}

// DMReadData is the other party's read marker (read receipts, active
// conversations only).
type DMReadData struct {
	ConversationID    string `json:"conversation_id"`
	UserID            string `json:"user_id"`
	LastReadMessageID string `json:"last_read_message_id"`
}

// DMFrom converts a direct message for a frame.
func DMFrom(m dm.Message) DMData {
	d := DMData{ID: m.ID.String(), ConversationID: m.ConversationID.String(), Encryption: m.Encryption, CreatedAt: m.CreatedAt}
	if m.SenderID != (id.ID{}) {
		d.SenderID = m.SenderID.String()
	}
	return d
}

// Direct returns Live as dm.Publisher.
func (l *Live) Direct() dm.Publisher { return directPublisher{l} }

type directPublisher struct{ l *Live }

func (p directPublisher) toParties(parties [2]id.ID, e Event) {
	for _, u := range parties {
		if u != (id.ID{}) {
			p.l.cfg.Hub.sendToUser(u, e)
		}
	}
}

func (p directPublisher) MessageCreated(parties [2]id.ID, m dm.Message) {
	p.toParties(parties, Event{Type: EventDMCreated, Data: DMFrom(m)})
}

func (p directPublisher) MessageDeleted(parties [2]id.ID, conversation, message id.ID) {
	p.toParties(parties, Event{Type: EventDMDeleted, Data: DMDeletedData{ConversationID: conversation.String(), MessageID: message.String()}})
}

func (p directPublisher) ConversationAccepted(parties [2]id.ID, conversation id.ID) {
	p.toParties(parties, Event{Type: EventDMAccepted, Data: DMConversationData{ConversationID: conversation.String()}})
}

func (p directPublisher) ConversationRead(to, reader, conversation, message id.ID) {
	p.l.cfg.Hub.sendToUser(to, Event{Type: EventDMRead, Data: DMReadData{
		ConversationID: conversation.String(), UserID: reader.String(), LastReadMessageID: message.String(),
	}})
}

func (p directPublisher) Notified(r notification.Request) { p.l.Notified(r) }

// Envelopes delivers each end-to-end envelope to its device's connections
// only (issue #127).
func (p directPublisher) Envelopes(ds []keys.Delivery) {
	for _, d := range ds {
		p.l.cfg.Hub.sendToSession(d.Session, Event{Type: EventDMEnvelope, Data: EnvelopeFrom(d.Envelope)})
	}
}

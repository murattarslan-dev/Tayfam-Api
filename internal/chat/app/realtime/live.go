package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Channels is what Live needs from channel.Service: the same permission
// checks and the same send path as REST.
type Channels interface {
	Get(ctx context.Context, caller, tayfa, ch id.ID) (channel.Channel, error)
	Send(ctx context.Context, caller, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error)
}

// Server frame types (ADR-0005 "Mesaj protokolü").
const (
	EventMessageCreated  = "message.created"
	EventMessageDeleted  = "message.deleted"
	EventMessageRestored = "message.restored"
	EventUnsubscribed    = "unsubscribed"
	EventNotification    = "notification"
)

// Reasons in an unsubscribed frame.
const (
	// ReasonRequested: the client asked for it.
	ReasonRequested = "requested"
	// ReasonAccessLost: core says the user may no longer see the channel
	// (left, banned, demoted from a staff channel, channel made staff,
	// tayfa deleted).
	ReasonAccessLost = "access_lost"
	// ReasonRecheckFailed: the permission could not be checked again
	// (core unreachable); the default answer is no. The client may
	// subscribe again.
	ReasonRecheckFailed = "recheck_failed"
)

const (
	// maxSubscribeAttempts bounds the retries when the tayfa keeps changing
	// during a subscription check.
	maxSubscribeAttempts = 3
	recheckTimeout       = 5 * time.Second
	recheckParallel      = 8
	recheckQueue         = 1024
)

// MessageData is a channel message in a frame. Body is the decrypted text;
// it goes only to subscribers whose permission core confirmed.
type MessageData struct {
	ID        string    `json:"id"`
	TayfaID   string    `json:"tayfa_id"`
	ChannelID string    `json:"channel_id"`
	AuthorID  string    `json:"author_user_id,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// DeletedData is a deleted message in a frame.
type DeletedData struct {
	TayfaID   string `json:"tayfa_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}

// SubscriptionData names a subscription.
type SubscriptionData struct {
	TayfaID   string `json:"tayfa_id"`
	ChannelID string `json:"channel_id"`
	Reason    string `json:"reason,omitempty"`
}

// NotificationData tells a user that a notification was written; IDs and
// classification only, as in the notification itself. The client reads the
// list over REST.
type NotificationData struct {
	Type       string `json:"type"`
	TayfaID    string `json:"tayfa_id,omitempty"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

// MessageFrom converts a message for a frame.
func MessageFrom(tayfa id.ID, m channel.Message) MessageData {
	d := MessageData{ID: m.ID.String(), TayfaID: tayfa.String(), ChannelID: m.ChannelID.String(), Body: m.Body, CreatedAt: m.CreatedAt}
	if m.AuthorID != (id.ID{}) {
		d.AuthorID = m.AuthorID.String()
	}
	return d
}

// LiveConfig wires Live.
type LiveConfig struct {
	Hub      *Hub
	Channels Channels
	// DirectMessages may be left nil and given with BindDirect.
	DirectMessages DirectMessages
	Log            *slog.Logger
}

// Live subscribes connections to channels, sends from them and delivers
// committed changes. It implements channel.Publisher.
type Live struct {
	cfg     LiveConfig
	recheck chan []sub
}

// NewLive returns Live. Channels may be left nil and given with Bind,
// because channel.Service takes Live as its Publisher.
func NewLive(cfg LiveConfig) (*Live, error) {
	if cfg.Hub == nil || cfg.Log == nil {
		return nil, errors.New("realtime: eksik yapılandırma")
	}
	return &Live{cfg: cfg, recheck: make(chan []sub, recheckQueue)}, nil
}

// Bind sets the channel service; call it once, before serving.
func (l *Live) Bind(c Channels) { l.cfg.Channels = c }

// asUser returns ctx carrying the connection's own access token for calls
// to core (auth.ForwardToken passes it on unchanged).
func (m *Member) asUser(ctx context.Context) context.Context {
	return auth.WithToken(ctx, m.token)
}

// Subscribe follows a channel for m after core confirms, with m's own
// token, that the user may see it (staff channels only for moderators and
// admins). A tayfa change during the check repeats it.
func (l *Live) Subscribe(ctx context.Context, m *Member, tayfa, ch id.ID) error {
	for range maxSubscribeAttempts {
		g := l.cfg.Hub.generation(tayfa)
		if _, err := l.cfg.Channels.Get(m.asUser(ctx), m.user, tayfa, ch); err != nil {
			return err
		}
		err := l.cfg.Hub.subscribe(m, tayfa, ch, g)
		if !errors.Is(err, errChanged) {
			return err
		}
	}
	return apperr.ErrUnavailable
}

// Unsubscribe stops following a channel; it reports whether m followed it.
func (l *Live) Unsubscribe(m *Member, tayfa, ch id.ID) bool {
	return l.cfg.Hub.unsubscribe(m, tayfa, ch)
}

// Send writes a message through channel.Service.Send, the single send path
// (ADR-0005; mute, slow mode, archive and permission in one place), with
// m's own token. Subscribers, the sender's other connections included,
// get it through MessageCreated.
func (l *Live) Send(ctx context.Context, m *Member, tayfa, ch id.ID, in channel.SendInput) (channel.Message, error) {
	msg, err := l.cfg.Channels.Send(m.asUser(ctx), m.user, tayfa, ch, in)
	if err != nil {
		return channel.Message{}, fmt.Errorf("gönderim: %w", err)
	}
	return msg, nil
}

// MessageCreated implements channel.Publisher.
func (l *Live) MessageCreated(tayfa id.ID, m channel.Message) {
	l.cfg.Hub.publish(tayfa, m.ChannelID, Event{Type: EventMessageCreated, Data: MessageFrom(tayfa, m)})
}

// MessageDeleted implements channel.Publisher.
func (l *Live) MessageDeleted(tayfa, ch, message id.ID) {
	l.cfg.Hub.publish(tayfa, ch, Event{Type: EventMessageDeleted, Data: DeletedData{
		TayfaID: tayfa.String(), ChannelID: ch.String(), MessageID: message.String(),
	}})
}

// MessageRestored implements channel.Publisher.
func (l *Live) MessageRestored(tayfa id.ID, m channel.Message) {
	l.cfg.Hub.publish(tayfa, m.ChannelID, Event{Type: EventMessageRestored, Data: MessageFrom(tayfa, m)})
}

// ChannelChanged implements channel.Publisher: the channel's subscribers
// are checked again (a channel made staff drops plain members).
func (l *Live) ChannelChanged(tayfa, ch id.ID) {
	l.cfg.Hub.bump(tayfa)
	l.enqueue(l.cfg.Hub.channelSubs(tayfa, ch))
}

// MembershipChanged: a member's role, mute or status changed in the tayfa
// (core's membership.changed, after the permission cache was cleared).
// Their subscriptions in the tayfa are checked again with each
// connection's own token; what core no longer allows is cut.
func (l *Live) MembershipChanged(tayfa, user id.ID) {
	l.cfg.Hub.bump(tayfa)
	l.enqueue(l.cfg.Hub.userSubs(tayfa, user))
}

// TayfaChanged: the tayfa became read-only or writable, or was deleted.
// Every subscription in it is checked again.
func (l *Live) TayfaChanged(tayfa id.ID) {
	l.cfg.Hub.bump(tayfa)
	l.enqueue(l.cfg.Hub.subsWhere(tayfa, func(*Member, id.ID) bool { return true }))
}

// TayfaPurged: the tayfa is gone for good; its subscriptions end without a
// question.
func (l *Live) TayfaPurged(tayfa id.ID) {
	l.cfg.Hub.bump(tayfa)
	for _, s := range l.cfg.Hub.subsWhere(tayfa, func(*Member, id.ID) bool { return true }) {
		l.cut(s, ReasonAccessLost)
	}
}

// Notified tells the recipients' open connections that a notification was
// written (after the transaction committed).
func (l *Live) Notified(r notification.Request) {
	d := NotificationData{Type: r.Type, TargetType: r.TargetType, TargetID: r.Target.String()}
	if r.Tayfa != (id.ID{}) {
		d.TayfaID = r.Tayfa.String()
	}
	for _, u := range r.Recipients {
		l.cfg.Hub.sendToUser(u, Event{Type: EventNotification, Data: d})
	}
}

// enqueue hands subscriptions to the recheck worker. If the queue is full
// they are cut at once: the default answer is no.
func (l *Live) enqueue(subs []sub) {
	if len(subs) == 0 {
		return
	}
	select {
	case l.recheck <- subs:
	default:
		for _, s := range subs {
			l.cut(s, ReasonRecheckFailed)
		}
	}
}

func (l *Live) cut(s sub, reason string) {
	if l.cfg.Hub.unsubscribe(s.m, s.tayfa, s.channel) {
		s.m.conn.Deliver(Event{Type: EventUnsubscribed, Data: SubscriptionData{
			TayfaID: s.tayfa.String(), ChannelID: s.channel.String(), Reason: reason,
		}})
	}
}

// check asks core again, with the connection's own token, whether the
// subscription still holds; if not it is cut.
func (l *Live) check(ctx context.Context, s sub) {
	if !l.cfg.Hub.subscribed(s.m, s.tayfa, s.channel) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, recheckTimeout)
	defer cancel()
	_, err := l.cfg.Channels.Get(s.m.asUser(ctx), s.m.user, s.tayfa, s.channel)
	switch {
	case err == nil:
	case errors.Is(err, apperr.ErrChannelNotFound), errors.Is(err, apperr.ErrTayfaNotFound),
		errors.Is(err, apperr.ErrPermissionDenied):
		l.cut(s, ReasonAccessLost)
	default:
		if !errors.Is(err, context.Canceled) {
			l.cfg.Log.WarnContext(ctx, "abonelik yeniden denetlenemedi", slog.String("hata", err.Error()))
		}
		l.cut(s, ReasonRecheckFailed)
	}
}

// Run checks queued subscriptions again until ctx is done.
func (l *Live) Run(ctx context.Context) error {
	sem := make(chan struct{}, recheckParallel)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		case subs := <-l.recheck:
			for _, s := range subs {
				select {
				case <-ctx.Done():
					return nil
				case sem <- struct{}{}:
				}
				wg.Go(func() {
					defer func() { <-sem }()
					l.check(ctx, s)
				})
			}
		}
	}
}

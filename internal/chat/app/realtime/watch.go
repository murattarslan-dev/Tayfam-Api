package realtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Watching an event for component changes (issue #238, ADR-0025 §6).
//
// core's event.component_changed carries the event and the component key
// only. chat keeps nothing about events: a connection watches an event
// after core confirmed, with the connection's own token, that its user may
// read the event's components (the model of ADR-0024 §1), and the question
// is asked again whenever the user's standing in the tayfa may have
// changed. A connection that does not watch the event, or whose watch core
// no longer allows, gets no frame. Clients read the changed component from
// core over REST, where access is checked once more.

// Server frame types of event watches.
const (
	EventComponentChanged = "event.component_changed"
	EventUnwatched        = "event.unwatched"
)

// EventAccess asks core whether the user of ctx's token may watch an event
// of a tayfa: nil, apperr.ErrTayfaNotFound (not an active member),
// apperr.ErrEventNotFound, or apperr.ErrUnavailable.
type EventAccess interface {
	CheckEventAccess(ctx context.Context, tayfa, event id.ID) error
}

// WatchData names a watched event; Reason is set in an event.unwatched
// frame (the reasons of unsubscribed).
type WatchData struct {
	TayfaID string `json:"tayfa_id"`
	EventID string `json:"event_id"`
	Reason  string `json:"reason,omitempty"`
}

// ComponentChangedData is an event.component_changed frame: IDs only.
// ComponentKey is empty when the event's selections or referees changed.
type ComponentChangedData struct {
	TayfaID      string `json:"tayfa_id"`
	EventID      string `json:"event_id"`
	ComponentKey string `json:"component_key,omitempty"`
}

// watch adds a watch checked at generation g; errChanged if the tayfa
// changed since. Watching again is a no-op.
func (h *Hub) watch(m *Member, tayfa, event id.ID, g uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case m.watches == nil:
		return ErrServerFull // released meanwhile
	case h.gen[tayfa] != g:
		return errChanged
	}
	if t, ok := m.watches[event]; ok && t == tayfa {
		return nil
	}
	if len(m.watches) >= h.cfg.MaxWatches {
		return ErrTooManyWatches
	}
	m.watches[event] = tayfa
	add(h.byEvent, event, m)
	return nil
}

// unwatch removes a watch; it reports whether there was one.
func (h *Hub) unwatch(m *Member, tayfa, event id.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := m.watches[event]; !ok || t != tayfa {
		return false
	}
	delete(m.watches, event)
	remove(h.byEvent, event, m)
	return true
}

// watching reports whether m watches the event.
func (h *Hub) watching(m *Member, tayfa, event id.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := m.watches[event]
	return ok && t == tayfa
}

// userWatches returns user's watches in tayfa.
func (h *Hub) userWatches(tayfa, user id.ID) []sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sub
	for m := range h.byUser[user] {
		for ev, t := range m.watches {
			if t == tayfa {
				out = append(out, sub{m: m, tayfa: t, channel: ev, watch: true})
			}
		}
	}
	return out
}

// tayfaWatches returns every watch in tayfa.
func (h *Hub) tayfaWatches(tayfa id.ID) []sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sub
	for _, set := range h.byUser {
		for m := range set {
			for ev, t := range m.watches {
				if t == tayfa {
					out = append(out, sub{m: m, tayfa: t, channel: ev, watch: true})
				}
			}
		}
	}
	return out
}

// publishEvent delivers e to the connections that watch the event in this
// tayfa.
func (h *Hub) publishEvent(tayfa, event id.ID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.byEvent[event] {
		if m.watches[event] == tayfa {
			m.conn.Deliver(e)
		}
	}
}

// Watches returns how many event watches the connections hold.
func (h *Hub) Watches() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.byEvent {
		n += len(set)
	}
	return n
}

// BindEvents sets the answer to "may this user watch this event"; call it
// once, before serving.
func (l *Live) BindEvents(a EventAccess) { l.cfg.Events = a }

// Watch follows an event's component changes for m after core confirms,
// with m's own token, that the user may read the event's components. A
// tayfa change during the check repeats it.
func (l *Live) Watch(ctx context.Context, m *Member, tayfa, event id.ID) error {
	if l.cfg.Events == nil {
		return apperr.ErrUnavailable
	}
	for range maxSubscribeAttempts {
		g := l.cfg.Hub.generation(tayfa)
		if err := l.cfg.Events.CheckEventAccess(m.asUser(ctx), tayfa, event); err != nil {
			return err
		}
		err := l.cfg.Hub.watch(m, tayfa, event, g)
		if !errors.Is(err, errChanged) {
			return err
		}
	}
	return apperr.ErrUnavailable
}

// Unwatch stops watching an event; it reports whether m watched it.
func (l *Live) Unwatch(m *Member, tayfa, event id.ID) bool {
	return l.cfg.Hub.unwatch(m, tayfa, event)
}

// ComponentChanged tells the connections that watch the event that one of
// its components changed (core's event.component_changed). The frame
// holds the IDs the event carried and nothing else.
func (l *Live) ComponentChanged(tayfa, event id.ID, key string) {
	l.cfg.Hub.publishEvent(tayfa, event, Event{Type: EventComponentChanged, Data: ComponentChangedData{
		TayfaID: tayfa.String(), EventID: event.String(), ComponentKey: key,
	}})
}

func (l *Live) cutWatch(s sub, reason string) {
	if l.cfg.Hub.unwatch(s.m, s.tayfa, s.channel) {
		s.m.conn.Deliver(Event{Type: EventUnwatched, Data: WatchData{
			TayfaID: s.tayfa.String(), EventID: s.channel.String(), Reason: reason,
		}})
	}
}

// checkWatch asks core again, with the connection's own token, whether
// the watch still holds; if not it is cut. The default answer is no.
func (l *Live) checkWatch(ctx context.Context, s sub) {
	if !l.cfg.Hub.watching(s.m, s.tayfa, s.channel) {
		return
	}
	if l.cfg.Events == nil {
		l.cutWatch(s, ReasonRecheckFailed)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, recheckTimeout)
	defer cancel()
	err := l.cfg.Events.CheckEventAccess(s.m.asUser(ctx), s.tayfa, s.channel)
	switch {
	case err == nil:
	case errors.Is(err, apperr.ErrTayfaNotFound), errors.Is(err, apperr.ErrEventNotFound),
		errors.Is(err, apperr.ErrPermissionDenied):
		l.cutWatch(s, ReasonAccessLost)
	default:
		if !errors.Is(err, context.Canceled) {
			l.cfg.Log.WarnContext(ctx, "etkinlik izleme yeniden denetlenemedi", slog.String("hata", err.Error()))
		}
		l.cutWatch(s, ReasonRecheckFailed)
	}
}

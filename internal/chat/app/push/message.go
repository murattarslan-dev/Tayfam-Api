package push

import (
	"encoding/json"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Android notification channels the client creates (ADR-0019 item 4).
const (
	ChannelMessages = "tayfam_messages"
	ChannelAlerts   = "tayfam_alerts"
	ChannelGeneral  = "tayfam_general"
)

// Message is one push to one token. It holds translation keys and IDs
// only: no text, no names, no places (ADR-0019 item 4). Sender
// implementations must not log any field of it.
type Message struct {
	Token    string
	Platform string
	// TitleKey and BodyKey are the client's translation keys
	// (push_<type>_title, push_<type>_body). There are no arguments.
	TitleKey, BodyKey string
	// Data: string IDs and classification (notification_id, type,
	// target_type, target_id, tayfa_id, …).
	Data map[string]string
	// Group collapses notifications about the same target (Android tag,
	// APNs thread-id and collapse id).
	Group string
	// Channel is the Android notification channel.
	Channel string
	// High: deliver now (direct messages, hike_overdue).
	High bool
	// TimeSensitive: iOS time-sensitive interruption level (hike_overdue).
	TimeSensitive bool
	TTL           time.Duration
}

// note is a queued notification as the worker reads it.
type note struct {
	ID         id.ID
	User       id.ID
	Tayfa      id.ID // zero: not about a tayfa
	Type       string
	TargetType string
	Target     id.ID
	Payload    []byte
}

// content builds the platform independent part of the push of n.
func content(n note) Message {
	m := Message{
		TitleKey: "push_" + n.Type + "_title",
		BodyKey:  "push_" + n.Type + "_body",
		Data: map[string]string{
			"notification_id": n.ID.String(),
			"type":            n.Type,
			"target_type":     n.TargetType,
			"target_id":       n.Target.String(),
		},
		Group:   n.Target.String(),
		Channel: ChannelGeneral,
		TTL:     TTL,
	}
	switch {
	case notification.IsDirect(n.Type):
		// "Yeni mesaj" / "Yeni mesaj isteği": the conversation's ID only;
		// no sender, no content, no unread count (ADR-0005).
		m.Channel, m.High = ChannelMessages, true
		return m
	case n.Type == notification.TypeHikeOverdue:
		m.Channel, m.High, m.TimeSensitive = ChannelAlerts, true, true
	}
	if n.Tayfa != (id.ID{}) {
		m.Data["tayfa_id"] = n.Tayfa.String()
	}
	// The payload holds IDs and a classification only (00002); unknown
	// keys are not copied.
	var p struct {
		QuestionID string `json:"question_id"`
		UserID     string `json:"user_id"`
		Reason     string `json:"reason"`
	}
	if len(n.Payload) > 0 && json.Unmarshal(n.Payload, &p) == nil {
		if v, ok := id.Parse(p.QuestionID); ok {
			m.Data["question_id"] = v.String()
		}
		if v, ok := id.Parse(p.UserID); ok {
			m.Data["subject_user_id"] = v.String()
		}
		if p.Reason == notification.ReasonInactive || p.Reason == notification.ReasonFewMembers {
			m.Data["reason"] = p.Reason
		}
	}
	return m
}

// personal are the notifications level "mentions" still pushes: about the
// recipient personally (ADR-0019 item 3, a proposal).
var personal = map[string]bool{
	"mention":                        true,
	notification.TypeAnswerReceived:  true,
	notification.TypeRSVPPromoted:    true,
	notification.TypeJoinApproved:    true,
	notification.TypeTayfaInvitation: true,
	notification.TypeReportResolved:  true,
	notification.TypeDMMessage:       true,
	notification.TypeDMRequest:       true,
	// The user's own data export (ADR-0021, ADR-0019 item 3).
	notification.TypeDataExportReady: true,
}

// scope returns the preference scope of a notification, or ok false when
// no preference applies.
func scope(n note) (typ string, scopeID id.ID, ok bool) {
	switch {
	case notification.IsDirect(n.Type):
		return "dm_conversation", n.Target, true
	case n.Tayfa != (id.ID{}):
		return notification.ScopeTayfa, n.Tayfa, true
	}
	return "", id.ID{}, false
}

// allowed decides from the effective preference whether n is pushed. A
// level with a muted_until in the past is back to all; hike_overdue is
// never muted (v14).
func allowed(typ, level string, until, now time.Time) bool {
	if typ == notification.TypeHikeOverdue {
		return true
	}
	if !until.IsZero() && !now.Before(until) {
		return true
	}
	switch level {
	case notification.LevelNone:
		return false
	case notification.LevelMentions:
		return personal[typ]
	}
	return true
}

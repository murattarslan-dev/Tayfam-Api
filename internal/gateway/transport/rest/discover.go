package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Keşfet, first stage (issue #210, ADR-0033). Every route needs the user's
// full access token; core checks it again and decides everything else:
// whether Keşfet is on (a closed feature is 404 feature-disabled), who may
// see what, and the friendship score, which never reaches a response.
// There is no location anywhere here: the feed has no meeting point and a
// safety share only the meeting point's name.

// DiscoverClient is core's DiscoverService.
type DiscoverClient interface {
	ListDiscoverFeed(ctx context.Context, in *corev1.ListDiscoverFeedRequest, opts ...grpc.CallOption) (*corev1.ListDiscoverFeedResponse, error)
	JoinFromDiscover(ctx context.Context, in *corev1.JoinFromDiscoverRequest, opts ...grpc.CallOption) (*corev1.JoinFromDiscoverResponse, error)
	GetTayfaDiscover(ctx context.Context, in *corev1.GetTayfaDiscoverRequest, opts ...grpc.CallOption) (*corev1.GetTayfaDiscoverResponse, error)
	SetTayfaDiscover(ctx context.Context, in *corev1.SetTayfaDiscoverRequest, opts ...grpc.CallOption) (*corev1.SetTayfaDiscoverResponse, error)
	GetEventFeedback(ctx context.Context, in *corev1.GetEventFeedbackRequest, opts ...grpc.CallOption) (*corev1.GetEventFeedbackResponse, error)
	SetEventFeedback(ctx context.Context, in *corev1.SetEventFeedbackRequest, opts ...grpc.CallOption) (*corev1.SetEventFeedbackResponse, error)
	GetSafetyContact(ctx context.Context, in *corev1.GetSafetyContactRequest, opts ...grpc.CallOption) (*corev1.GetSafetyContactResponse, error)
	SetSafetyContact(ctx context.Context, in *corev1.SetSafetyContactRequest, opts ...grpc.CallOption) (*corev1.SetSafetyContactResponse, error)
	ClearSafetyContact(ctx context.Context, in *corev1.ClearSafetyContactRequest, opts ...grpc.CallOption) (*corev1.ClearSafetyContactResponse, error)
	MarkReturned(ctx context.Context, in *corev1.MarkReturnedRequest, opts ...grpc.CallOption) (*corev1.MarkReturnedResponse, error)
	ListSafetyShares(ctx context.Context, in *corev1.ListSafetySharesRequest, opts ...grpc.CallOption) (*corev1.ListSafetySharesResponse, error)
}

func registerDiscover(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("GET /v1/discover/events", a(h.listDiscoverFeed))
	mux.HandleFunc("POST /v1/discover/tayfas/{tayfa_id}/join", a(h.joinFromDiscover))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/discover", a(h.getTayfaDiscover))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/discover", a(h.setTayfaDiscover))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/events/{event_id}/feedback", a(h.getEventFeedback))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/events/{event_id}/feedback", a(h.setEventFeedback))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact", a(h.getSafetyContact))
	mux.HandleFunc("PUT /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact", a(h.setSafetyContact))
	mux.HandleFunc("DELETE /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact", a(h.clearSafetyContact))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact/return", a(h.markReturned))
	mux.HandleFunc("GET /v1/me/safety-shares", a(h.listSafetyShares))
}

var (
	tayfaDiscoverStates = newEnum(map[string]corev1.TayfaDiscoverState{
		"off": corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_OFF, "pending": corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_PENDING,
		"listed": corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_LISTED, "under_review": corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_UNDER_REVIEW,
		"rejected": corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_REJECTED,
	})
	feedbackAnswers = newEnum(map[string]corev1.FeedbackAnswer{
		"ok": corev1.FeedbackAnswer_FEEDBACK_ANSWER_OK, "not_ok": corev1.FeedbackAnswer_FEEDBACK_ANSWER_NOT_OK,
		"serious": corev1.FeedbackAnswer_FEEDBACK_ANSWER_SERIOUS,
	})
)

type discoverPersonJSON struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	AvatarUploadID string `json:"avatar_upload_id,omitempty"`
}

func discoverPersonFrom(p *corev1.DiscoverPerson) *discoverPersonJSON {
	if p.GetUserId() == "" {
		return nil
	}
	return &discoverPersonJSON{UserID: p.GetUserId(), DisplayName: p.GetDisplayName(), AvatarUploadID: p.GetAvatarUploadId()}
}

type discoverEventJSON struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Title    string    `json:"title"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	City     string    `json:"city,omitempty"`
	District string    `json:"district,omitempty"`
}

type discoverTayfaJSON struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	City            string    `json:"city,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	EventCount      int32     `json:"event_count"`
	AttendanceCount int32     `json:"attendance_count"`
}

// discoverItemJSON is one feed entry; it has no score or position field.
type discoverItemJSON struct {
	Event       discoverEventJSON    `json:"event"`
	Tayfa       discoverTayfaJSON    `json:"tayfa"`
	Friends     []discoverPersonJSON `json:"friends"`
	FriendCount int32                `json:"friend_count"`
}

type discoverFeedJSON struct {
	Items []discoverItemJSON `json:"items"`
}

func (h coreHandlers) listDiscoverFeed(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Discover.ListDiscoverFeed(ctx, &corev1.ListDiscoverFeedRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := discoverFeedJSON{Items: make([]discoverItemJSON, 0, len(resp.GetItems()))}
	for _, it := range resp.GetItems() {
		e, t := it.GetEvent(), it.GetTayfa()
		item := discoverItemJSON{
			Event: discoverEventJSON{
				ID: e.GetEventId(), Type: eventTypes.name(e.GetType()), Title: e.GetTitle(),
				StartsAt: e.GetStartTime().AsTime(), EndsAt: e.GetEndTime().AsTime(), City: e.GetCity(), District: e.GetDistrict(),
			},
			Tayfa: discoverTayfaJSON{
				ID: t.GetTayfaId(), Name: t.GetName(), Description: t.GetDescription(), City: t.GetCity(),
				CreatedAt: t.GetCreateTime().AsTime(), EventCount: t.GetEventCount(), AttendanceCount: t.GetAttendanceCount(),
			},
			Friends: make([]discoverPersonJSON, 0, len(it.GetFriends())), FriendCount: it.GetFriendCount(),
		}
		for _, f := range it.GetFriends() {
			if p := discoverPersonFrom(f); p != nil {
				item.Friends = append(item.Friends, *p)
			}
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) joinFromDiscover(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !h.d.JoinCodeLimit.Allow(clientIP(r, h.d.TrustCFConnectingIP)) {
		rateLimited(w, r)
		return
	}
	t, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Discover.JoinFromDiscover(ctx, &corev1.JoinFromDiscoverRequest{TayfaId: t})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipFrom(resp.GetMembership()))
}

type tayfaDiscoverJSON struct {
	State   string `json:"state"`
	Enabled bool   `json:"enabled"`
}

type tayfaDiscoverBody struct {
	Enabled *bool `json:"enabled"`
}

func (h coreHandlers) getTayfaDiscover(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.d.Discover.GetTayfaDiscover(ctx, &corev1.GetTayfaDiscoverRequest{TayfaId: t})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tayfaDiscoverJSON{State: tayfaDiscoverStates.name(resp.GetState()), Enabled: resp.GetEnabled()})
}

func (h coreHandlers) setTayfaDiscover(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b tayfaDiscoverBody
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Discover.SetTayfaDiscover(ctx, &corev1.SetTayfaDiscoverRequest{TayfaId: t, Enabled: *b.Enabled})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tayfaDiscoverJSON{State: tayfaDiscoverStates.name(resp.GetState()), Enabled: resp.GetEnabled()})
}

type eventFeedbackJSON struct {
	Answer    string     `json:"answer,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type eventFeedbackBody struct {
	Answer string `json:"answer"`
}

func feedbackFrom(a corev1.FeedbackAnswer, at *timestamppb.Timestamp) eventFeedbackJSON {
	if a == corev1.FeedbackAnswer_FEEDBACK_ANSWER_UNSPECIFIED {
		return eventFeedbackJSON{}
	}
	return eventFeedbackJSON{Answer: feedbackAnswers.name(a), UpdatedAt: optTime(at)}
}

func (h coreHandlers) getEventFeedback(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Discover.GetEventFeedback(ctx, &corev1.GetEventFeedbackRequest{TayfaId: t, EventId: e})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, feedbackFrom(resp.GetAnswer(), resp.GetUpdateTime()))
}

func (h coreHandlers) setEventFeedback(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b eventFeedbackBody
	if !decode(w, r, &b) {
		return
	}
	a, ok := feedbackAnswers.value(b.Answer)
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Discover.SetEventFeedback(ctx, &corev1.SetEventFeedbackRequest{TayfaId: t, EventId: e, Answer: a})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, feedbackFrom(resp.GetAnswer(), resp.GetUpdateTime()))
}

type safetyContactJSON struct {
	Contact      *discoverPersonJSON `json:"contact,omitempty"`
	GraceMinutes int32               `json:"grace_minutes"`
	ReturnedAt   *time.Time          `json:"returned_at,omitempty"`
	AlertSentAt  *time.Time          `json:"alert_sent_at,omitempty"`
}

type safetyContactBody struct {
	ContactUserID string `json:"contact_user_id"`
	GraceMinutes  *int32 `json:"grace_minutes"`
}

func safetyContactFrom(c *corev1.SafetyContact) safetyContactJSON {
	return safetyContactJSON{
		Contact: discoverPersonFrom(c.GetContact()), GraceMinutes: c.GetGraceMinutes(),
		ReturnedAt: optTime(c.GetReturnTime()), AlertSentAt: optTime(c.GetAlertTime()),
	}
}

func (h coreHandlers) getSafetyContact(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Discover.GetSafetyContact(ctx, &corev1.GetSafetyContactRequest{TayfaId: t, EventId: e})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, safetyContactFrom(resp.GetSafetyContact()))
}

func (h coreHandlers) setSafetyContact(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	var b safetyContactBody
	if !decode(w, r, &b) {
		return
	}
	if !uuidPattern.MatchString(b.ContactUserID) {
		badRequest(w, r)
		return
	}
	req := &corev1.SetSafetyContactRequest{TayfaId: t, EventId: e, ContactUserId: b.ContactUserID}
	if b.GraceMinutes != nil {
		// 0 would mean "default" to core; the contract asks for 15–1440.
		if *b.GraceMinutes <= 0 {
			badRequest(w, r)
			return
		}
		req.GraceMinutes = *b.GraceMinutes
	}
	resp, err := h.d.Discover.SetSafetyContact(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, safetyContactFrom(resp.GetSafetyContact()))
}

func (h coreHandlers) clearSafetyContact(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	if _, err := h.d.Discover.ClearSafetyContact(ctx, &corev1.ClearSafetyContactRequest{TayfaId: t, EventId: e}); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) markReturned(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	t, e, ok := pathTayfaEvent(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Discover.MarkReturned(ctx, &corev1.MarkReturnedRequest{TayfaId: t, EventId: e})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, safetyContactFrom(resp.GetSafetyContact()))
}

// safetyShareJSON is what a participant shares with their safety contact:
// who, the time range and the state; no tayfa, event or place (ADR-0033
// §10).
type safetyShareJSON struct {
	Participant  *discoverPersonJSON `json:"participant"`
	StartsAt     time.Time           `json:"starts_at"`
	EndsAt       time.Time           `json:"ends_at"`
	GraceMinutes int32               `json:"grace_minutes"`
	ReturnedAt   *time.Time          `json:"returned_at,omitempty"`
	AlertSentAt  *time.Time          `json:"alert_sent_at,omitempty"`
	State        string              `json:"state"`
}

var shareStateNames = map[corev1.SafetyShareState]string{
	corev1.SafetyShareState_SAFETY_SHARE_STATE_NOT_RETURNED: "not_returned",
	corev1.SafetyShareState_SAFETY_SHARE_STATE_RETURNED:     "returned",
	corev1.SafetyShareState_SAFETY_SHARE_STATE_ALERT_SENT:   "alert_sent",
}

type safetyShareListJSON struct {
	Shares []safetyShareJSON `json:"shares"`
}

func (h coreHandlers) listSafetyShares(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Discover.ListSafetyShares(ctx, &corev1.ListSafetySharesRequest{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := safetyShareListJSON{Shares: make([]safetyShareJSON, 0, len(resp.GetShares()))}
	for _, s := range resp.GetShares() {
		out.Shares = append(out.Shares, safetyShareJSON{
			Participant: discoverPersonFrom(s.GetParticipant()),
			StartsAt:    s.GetStartTime().AsTime(), EndsAt: s.GetEndTime().AsTime(),
			GraceMinutes: s.GetGraceMinutes(), ReturnedAt: optTime(s.GetReturnTime()), AlertSentAt: optTime(s.GetAlertTime()),
			State: shareStateNames[s.GetState()],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

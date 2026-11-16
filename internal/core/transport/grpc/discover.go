package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/discover"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Discovers is what DiscoverServer needs from the app layer
// (discover.Service).
type Discovers interface {
	Feed(ctx context.Context, viewer id.ID) ([]discover.Item, error)
	GetTayfa(ctx context.Context, caller, tayfa id.ID) (discover.TayfaState, error)
	SetTayfa(ctx context.Context, caller, tayfa id.ID, enabled bool) (discover.TayfaState, error)
	GetFeedback(ctx context.Context, caller, tayfa, event id.ID) (discover.Feedback, error)
	SetFeedback(ctx context.Context, caller, tayfa, event id.ID, answer string) (discover.Feedback, error)
	GetSafetyContact(ctx context.Context, caller, tayfa, event id.ID) (discover.SafetyContact, error)
	SetSafetyContact(ctx context.Context, caller, tayfa, event, contact id.ID, grace int32) (discover.SafetyContact, error)
	ClearSafetyContact(ctx context.Context, caller, tayfa, event id.ID) error
	MarkReturned(ctx context.Context, caller, tayfa, event id.ID) (discover.SafetyContact, error)
	ListSafetyShares(ctx context.Context, caller id.ID) ([]discover.Share, error)
}

// DiscoverJoiner is membership.Service's Keşfet join.
type DiscoverJoiner interface {
	JoinFromDiscover(ctx context.Context, caller, tayfa id.ID) (membership.Member, error)
}

// DiscoverServer implements corev1.DiscoverServiceServer (issue #210,
// ADR-0033). Every method needs a verified token (auth.Guard); the caller
// always comes from it. The friendship score never reaches a response.
type DiscoverServer struct {
	corev1.UnimplementedDiscoverServiceServer
	app  Discovers
	join DiscoverJoiner
	log  *slog.Logger
}

// NewDiscoverServer returns a DiscoverServer.
func NewDiscoverServer(app Discovers, join DiscoverJoiner, log *slog.Logger) *DiscoverServer {
	return &DiscoverServer{app: app, join: join, log: log}
}

var tayfaDiscoverStates = map[string]corev1.TayfaDiscoverState{
	discover.StateOff:         corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_OFF,
	discover.StatePending:     corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_PENDING,
	discover.StateListed:      corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_LISTED,
	discover.StateUnderReview: corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_UNDER_REVIEW,
	discover.StateRejected:    corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_REJECTED,
}

var feedbackAnswers = map[string]corev1.FeedbackAnswer{
	discover.AnswerOK:      corev1.FeedbackAnswer_FEEDBACK_ANSWER_OK,
	discover.AnswerNotOK:   corev1.FeedbackAnswer_FEEDBACK_ANSWER_NOT_OK,
	discover.AnswerSerious: corev1.FeedbackAnswer_FEEDBACK_ANSWER_SERIOUS,
}

func personPB(p discover.Person) *corev1.DiscoverPerson {
	if p.ID == (id.ID{}) {
		return nil
	}
	return &corev1.DiscoverPerson{UserId: p.ID.String(), DisplayName: p.DisplayName, AvatarUploadId: optionalID(p.AvatarID)}
}

// ListDiscoverFeed implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) ListDiscoverFeed(ctx context.Context, _ *corev1.ListDiscoverFeedRequest) (*corev1.ListDiscoverFeedResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.app.Feed(ctx, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListDiscoverFeedResponse{Items: make([]*corev1.DiscoverItem, 0, len(items))}
	for _, it := range items {
		pb := &corev1.DiscoverItem{
			Event: &corev1.DiscoverEvent{
				EventId: it.EventID.String(), Type: interestToProto(it.Type), Title: it.Title,
				StartTime: timestamppb.New(it.StartsAt), EndTime: timestamppb.New(it.EndsAt),
				City: it.City, District: it.District,
			},
			Tayfa: &corev1.DiscoverTayfa{
				TayfaId: it.TayfaID.String(), Name: it.TayfaName, Description: it.TayfaDescription, City: it.TayfaCity,
				CreateTime: timestamppb.New(it.TayfaCreatedAt), EventCount: it.EventCount, AttendanceCount: it.AttendanceCount,
			},
			FriendCount: it.FriendCount,
		}
		for _, f := range it.Friends {
			pb.Friends = append(pb.Friends, personPB(f))
		}
		out.Items = append(out.Items, pb)
	}
	return out, nil
}

// JoinFromDiscover implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) JoinFromDiscover(ctx context.Context, req *corev1.JoinFromDiscoverRequest) (*corev1.JoinFromDiscoverResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	m, err := s.join.JoinFromDiscover(ctx, u, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.JoinFromDiscoverResponse{Membership: memberPB(m)}, nil
}

// GetTayfaDiscover implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) GetTayfaDiscover(ctx context.Context, req *corev1.GetTayfaDiscoverRequest) (*corev1.GetTayfaDiscoverResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	st, err := s.app.GetTayfa(ctx, u, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetTayfaDiscoverResponse{State: tayfaDiscoverStates[st.State], Enabled: st.Enabled}, nil
}

// SetTayfaDiscover implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) SetTayfaDiscover(ctx context.Context, req *corev1.SetTayfaDiscoverRequest) (*corev1.SetTayfaDiscoverResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := tayfaID(req.GetTayfaId())
	if err != nil {
		return nil, err
	}
	st, err := s.app.SetTayfa(ctx, u, t, req.GetEnabled())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetTayfaDiscoverResponse{State: tayfaDiscoverStates[st.State], Enabled: st.Enabled}, nil
}

// eventArgs reads the caller and the tayfa_id and event_id fields.
func eventArgs(ctx context.Context, rawTayfa, rawEvent string) (u, tayfa, event id.ID, err error) {
	if u, err = callerID(ctx); err != nil {
		return
	}
	if tayfa, err = tayfaID(rawTayfa); err != nil {
		return
	}
	event, err = parseID(rawEvent, "event_id")
	return
}

func feedbackPB(f discover.Feedback) (corev1.FeedbackAnswer, *timestamppb.Timestamp) {
	return feedbackAnswers[f.Answer], optionalTime(f.UpdatedAt)
}

// GetEventFeedback implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) GetEventFeedback(ctx context.Context, req *corev1.GetEventFeedbackRequest) (*corev1.GetEventFeedbackResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	f, err := s.app.GetFeedback(ctx, u, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	a, at := feedbackPB(f)
	return &corev1.GetEventFeedbackResponse{Answer: a, UpdateTime: at}, nil
}

// SetEventFeedback implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) SetEventFeedback(ctx context.Context, req *corev1.SetEventFeedbackRequest) (*corev1.SetEventFeedbackResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	answer := ""
	for k, v := range feedbackAnswers {
		if v == req.GetAnswer() {
			answer = k
		}
	}
	if answer == "" {
		return nil, validation("answer")
	}
	f, err := s.app.SetFeedback(ctx, u, t, e, answer)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	a, at := feedbackPB(f)
	return &corev1.SetEventFeedbackResponse{Answer: a, UpdateTime: at}, nil
}

func safetyContactPB(c discover.SafetyContact) *corev1.SafetyContact {
	return &corev1.SafetyContact{
		Contact: personPB(c.Contact), GraceMinutes: c.GraceMinutes,
		ReturnTime: optionalTime(c.ReturnedAt), AlertTime: optionalTime(c.AlertSentAt),
	}
}

// GetSafetyContact implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) GetSafetyContact(ctx context.Context, req *corev1.GetSafetyContactRequest) (*corev1.GetSafetyContactResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	c, err := s.app.GetSafetyContact(ctx, u, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetSafetyContactResponse{SafetyContact: safetyContactPB(c)}, nil
}

// SetSafetyContact implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) SetSafetyContact(ctx context.Context, req *corev1.SetSafetyContactRequest) (*corev1.SetSafetyContactResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	contact, err := parseID(req.GetContactUserId(), "contact_user_id")
	if err != nil {
		return nil, err
	}
	c, err := s.app.SetSafetyContact(ctx, u, t, e, contact, req.GetGraceMinutes())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetSafetyContactResponse{SafetyContact: safetyContactPB(c)}, nil
}

// ClearSafetyContact implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) ClearSafetyContact(ctx context.Context, req *corev1.ClearSafetyContactRequest) (*corev1.ClearSafetyContactResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	if err := s.app.ClearSafetyContact(ctx, u, t, e); err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.ClearSafetyContactResponse{}, nil
}

// MarkReturned implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) MarkReturned(ctx context.Context, req *corev1.MarkReturnedRequest) (*corev1.MarkReturnedResponse, error) {
	u, t, e, err := eventArgs(ctx, req.GetTayfaId(), req.GetEventId())
	if err != nil {
		return nil, err
	}
	c, err := s.app.MarkReturned(ctx, u, t, e)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.MarkReturnedResponse{SafetyContact: safetyContactPB(c)}, nil
}

// ListSafetyShares implements corev1.DiscoverServiceServer.
func (s *DiscoverServer) ListSafetyShares(ctx context.Context, _ *corev1.ListSafetySharesRequest) (*corev1.ListSafetySharesResponse, error) {
	u, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	shares, err := s.app.ListSafetyShares(ctx, u)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	out := &corev1.ListSafetySharesResponse{Shares: make([]*corev1.SafetyShare, 0, len(shares))}
	for _, sh := range shares {
		out.Shares = append(out.Shares, &corev1.SafetyShare{
			Participant: personPB(sh.Participant), TayfaName: sh.TayfaName, EventId: sh.EventID.String(), EventTitle: sh.EventTitle,
			StartTime: timestamppb.New(sh.StartsAt), EndTime: timestamppb.New(sh.EndsAt), MeetingPointName: sh.MeetingPointName,
			City: sh.City, District: sh.District, GraceMinutes: sh.GraceMinutes,
			ReturnTime: optionalTime(sh.ReturnedAt), AlertTime: optionalTime(sh.AlertSentAt),
		})
	}
	return out, nil
}

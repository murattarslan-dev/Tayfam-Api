package demo

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeTokens issues "tok-<user>" tokens; fakeServices reads the caller back
// from the outgoing metadata, as core and chat read the verified subject.
type fakeTokens struct{}

func (fakeTokens) Issue(user, session string) (string, time.Time, error) {
	if _, ok := id.Parse(user); !ok {
		return "", time.Time{}, fmt.Errorf("kullanıcı kimliği %q", user)
	}
	if _, ok := id.Parse(session); !ok {
		return "", time.Time{}, fmt.Errorf("oturum kimliği %q", session)
	}
	return "tok-" + user, time.Now().Add(5 * time.Minute), nil
}

// fakeServices stands in for core and chat with the rules the seeder
// depends on: active membership for every tayfa call, roles for staff
// actions, join requests and approvals, client_request_id replays, unique
// channel names, read-only tayfas.
type fakeServices struct {
	mu       sync.Mutex
	next     int
	tayfas   map[string]*fTayfa
	replays  map[string]string // caller|kind|crid → object ID
	objects  map[string]int    // kind → objects created
	calls    []fCall
	events   map[string]*fEvent
	notices  map[string]*corev1.Announcement
	answers  map[string]string          // answer ID → author
	ansVotes map[string]map[string]bool // answer ID → voters
	polls    map[string]*fPoll
	channels map[string][]*chatv1.Channel // tayfa ID → channels
	plates   int
}

type fCall struct{ Caller, Method string }

type fTayfa struct {
	id, name, code string
	roles          map[string]corev1.Role // active members
	pending        map[string]bool
	readOnly       bool
}

type fEvent struct {
	ev       *corev1.Event
	rsvps    map[string]corev1.RsvpStatus
	vehicles map[string]*corev1.ConvoyVehicle
	items    []*corev1.CampItem
}

type fPoll struct {
	poll  *corev1.Poll
	votes map[string][]string
}

func newFake() *fakeServices {
	return &fakeServices{
		tayfas: map[string]*fTayfa{}, replays: map[string]string{}, objects: map[string]int{},
		events: map[string]*fEvent{}, notices: map[string]*corev1.Announcement{},
		answers: map[string]string{}, ansVotes: map[string]map[string]bool{},
		polls: map[string]*fPoll{}, channels: map[string][]*chatv1.Channel{},
	}
}

func (f *fakeServices) core() Core {
	return Core{Tayfas: f, Memberships: f, Events: f, Announcements: f, QA: f, Polls: f}
}

func fail(code codes.Code, reason string) error {
	st, _ := status.New(code, reason).WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "test"})
	return st.Err()
}

func (f *fakeServices) newID() string {
	f.next++
	return id.New().String()
}

// begin records the call and returns the caller.
func (f *fakeServices) begin(ctx context.Context, method string) (string, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	v := md.Get("authorization")
	if len(v) != 1 || !strings.HasPrefix(v[0], "Bearer tok-") {
		return "", status.Error(codes.Unauthenticated, "token yok")
	}
	caller := strings.TrimPrefix(v[0], "Bearer tok-")
	f.calls = append(f.calls, fCall{Caller: caller, Method: method})
	return caller, nil
}

// member returns the tayfa when caller is an active member with at least
// min; write refuses read-only tayfas.
func (f *fakeServices) member(tayfaID, caller string, min corev1.Role, write bool) (*fTayfa, error) {
	t, ok := f.tayfas[tayfaID]
	if !ok {
		return nil, fail(codes.NotFound, "ERROR_REASON_TAYFA_NOT_FOUND")
	}
	role, ok := t.roles[caller]
	if !ok {
		return nil, fail(codes.NotFound, "ERROR_REASON_TAYFA_NOT_FOUND")
	}
	if role < min {
		return nil, fail(codes.PermissionDenied, "ERROR_REASON_PERMISSION_DENIED")
	}
	if write && t.readOnly {
		return nil, fail(codes.FailedPrecondition, "ERROR_REASON_TAYFA_READ_ONLY")
	}
	return t, nil
}

// replay returns the earlier object of the same create, or "" after
// counting a new one.
func (f *fakeServices) replay(caller, kind, crid string) (string, bool) {
	if crid == "" {
		panic(kind + ": client_request_id yok")
	}
	if prev, ok := f.replays[caller+"|"+kind+"|"+crid]; ok {
		return prev, true
	}
	return "", false
}

func (f *fakeServices) created(caller, kind, crid, objectID string) {
	f.replays[caller+"|"+kind+"|"+crid] = objectID
	f.objects[kind]++
}

// snapshot sums every object and every answer, vote and vehicle.
func (f *fakeServices) snapshot() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.objects {
		out[k] = v
	}
	for _, t := range f.tayfas {
		out["members"] += len(t.roles)
		out["pending"] += len(t.pending)
		for _, r := range t.roles {
			out["role:"+r.String()]++
		}
	}
	for _, e := range f.events {
		out["rsvps"] += len(e.rsvps)
		out["vehicles"] += len(e.vehicles)
		out["items"] += len(e.items)
	}
	for _, v := range f.ansVotes {
		out["answer_votes"] += len(v)
	}
	for _, p := range f.polls {
		out["poll_votes"] += len(p.votes)
	}
	for _, n := range f.notices {
		if n.GetPinned() {
			out["pinned"]++
		}
	}
	return out
}

// TayfaService

func (f *fakeServices) CreateTayfa(ctx context.Context, in *corev1.CreateTayfaRequest, _ ...grpc.CallOption) (*corev1.CreateTayfaResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateTayfa")
	if err != nil {
		return nil, err
	}
	if prev, ok := f.replay(caller, "tayfa", in.GetClientRequestId()); ok {
		return &corev1.CreateTayfaResponse{Tayfa: &corev1.Tayfa{Id: prev, Name: f.tayfas[prev].name}}, nil
	}
	t := &fTayfa{id: f.newID(), name: in.GetName(), code: fmt.Sprintf("KOD%05d", f.next),
		roles: map[string]corev1.Role{caller: corev1.Role_ROLE_ADMIN}, pending: map[string]bool{}}
	f.tayfas[t.id] = t
	f.created(caller, "tayfa", in.GetClientRequestId(), t.id)
	return &corev1.CreateTayfaResponse{Tayfa: &corev1.Tayfa{Id: t.id, Name: t.name}}, nil
}

func (f *fakeServices) GetTayfa(ctx context.Context, in *corev1.GetTayfaRequest, _ ...grpc.CallOption) (*corev1.GetTayfaResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "GetTayfa")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, false)
	if err != nil {
		return nil, err
	}
	out := &corev1.Tayfa{Id: t.id, Name: t.name}
	if t.roles[caller] == corev1.Role_ROLE_ADMIN {
		out.JoinCode = t.code
	}
	return &corev1.GetTayfaResponse{Tayfa: out, CallerRole: t.roles[caller]}, nil
}

// MembershipService

func (f *fakeServices) JoinByCode(ctx context.Context, in *corev1.JoinByCodeRequest, _ ...grpc.CallOption) (*corev1.JoinByCodeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "JoinByCode")
	if err != nil {
		return nil, err
	}
	for _, t := range f.tayfas {
		if t.code != in.GetCode() || t.readOnly {
			continue
		}
		if _, ok := t.roles[caller]; ok {
			return nil, fail(codes.AlreadyExists, "ERROR_REASON_ALREADY_MEMBER")
		}
		if t.pending[caller] {
			return nil, fail(codes.AlreadyExists, "ERROR_REASON_ALREADY_PENDING")
		}
		t.pending[caller] = true
		return &corev1.JoinByCodeResponse{}, nil
	}
	return nil, fail(codes.InvalidArgument, "ERROR_REASON_INVALID_JOIN_CODE")
}

func (f *fakeServices) ListMembers(ctx context.Context, in *corev1.ListMembersRequest, _ ...grpc.CallOption) (*corev1.ListMembersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "ListMembers")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, false)
	if err != nil {
		return nil, err
	}
	out := &corev1.ListMembersResponse{}
	for u, r := range t.roles {
		out.Members = append(out.Members, &corev1.Membership{TayfaId: t.id, UserId: u, Role: r, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE})
	}
	return out, nil
}

func (f *fakeServices) ApproveMember(ctx context.Context, in *corev1.ApproveMemberRequest, _ ...grpc.CallOption) (*corev1.ApproveMemberResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "ApproveMember")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true)
	if err != nil {
		return nil, err
	}
	if !t.pending[in.GetUserId()] {
		return nil, fail(codes.NotFound, "ERROR_REASON_MEMBER_NOT_FOUND")
	}
	delete(t.pending, in.GetUserId())
	t.roles[in.GetUserId()] = corev1.Role_ROLE_MEMBER
	return &corev1.ApproveMemberResponse{}, nil
}

func (f *fakeServices) ChangeRole(ctx context.Context, in *corev1.ChangeRoleRequest, _ ...grpc.CallOption) (*corev1.ChangeRoleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "ChangeRole")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_ADMIN, true)
	if err != nil {
		return nil, err
	}
	cur, ok := t.roles[in.GetUserId()]
	switch {
	case !ok:
		return nil, fail(codes.NotFound, "ERROR_REASON_MEMBER_NOT_FOUND")
	case cur >= t.roles[caller]:
		return nil, fail(codes.PermissionDenied, "ERROR_REASON_ROLE_HIERARCHY")
	case cur == in.GetNewRole():
		return nil, fail(codes.InvalidArgument, "ERROR_REASON_VALIDATION")
	}
	t.roles[in.GetUserId()] = in.GetNewRole()
	return &corev1.ChangeRoleResponse{}, nil
}

// EventService

func (f *fakeServices) CreateEvent(ctx context.Context, in *corev1.CreateEventRequest, _ ...grpc.CallOption) (*corev1.CreateEventResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateEvent")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true); err != nil {
		return nil, err
	}
	if prev, ok := f.replay(caller, "event", in.GetClientRequestId()); ok {
		return &corev1.CreateEventResponse{Event: f.events[prev].ev}, nil
	}
	if !in.GetEndTime().AsTime().After(in.GetStartTime().AsTime()) {
		return nil, fail(codes.InvalidArgument, "ERROR_REASON_VALIDATION")
	}
	e := &fEvent{
		ev:    &corev1.Event{Id: f.newID(), TayfaId: in.GetTayfaId(), Type: in.GetType(), Mode: in.GetMode(), StartTime: in.GetStartTime()},
		rsvps: map[string]corev1.RsvpStatus{}, vehicles: map[string]*corev1.ConvoyVehicle{},
	}
	f.events[e.ev.Id] = e
	f.created(caller, "event", in.GetClientRequestId(), e.ev.Id)
	return &corev1.CreateEventResponse{Event: e.ev}, nil
}

func (f *fakeServices) event(tayfaID, eventID string) (*fEvent, error) {
	e, ok := f.events[eventID]
	if !ok || e.ev.GetTayfaId() != tayfaID {
		return nil, fail(codes.NotFound, "ERROR_REASON_EVENT_NOT_FOUND")
	}
	return e, nil
}

func (f *fakeServices) GetEvent(ctx context.Context, in *corev1.GetEventRequest, _ ...grpc.CallOption) (*corev1.GetEventResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "GetEvent")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, false); err != nil {
		return nil, err
	}
	e, err := f.event(in.GetTayfaId(), in.GetEventId())
	if err != nil {
		return nil, err
	}
	out := &corev1.Event{Id: e.ev.Id, TayfaId: e.ev.TayfaId, Type: e.ev.Type}
	if e.ev.Type == corev1.EventType_EVENT_TYPE_CAMP {
		out.Details = &corev1.Event_Camp{Camp: &corev1.CampDetails{Items: e.items}}
	}
	return &corev1.GetEventResponse{Event: out}, nil
}

func (f *fakeServices) SetRsvp(ctx context.Context, in *corev1.SetRsvpRequest, _ ...grpc.CallOption) (*corev1.SetRsvpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "SetRsvp")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	e, err := f.event(in.GetTayfaId(), in.GetEventId())
	if err != nil {
		return nil, err
	}
	e.rsvps[caller] = in.GetStatus()
	return &corev1.SetRsvpResponse{Status: in.GetStatus()}, nil
}

func (f *fakeServices) AddCampItem(ctx context.Context, in *corev1.AddCampItemRequest, _ ...grpc.CallOption) (*corev1.AddCampItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "AddCampItem")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true)
	if err != nil {
		return nil, err
	}
	e, err := f.event(in.GetTayfaId(), in.GetEventId())
	if err != nil {
		return nil, err
	}
	if e.ev.Type != corev1.EventType_EVENT_TYPE_CAMP {
		return nil, fail(codes.FailedPrecondition, "ERROR_REASON_EVENT_TYPE_MISMATCH")
	}
	if a := in.GetAssignedUserId(); a != "" {
		if _, ok := t.roles[a]; !ok {
			return nil, fail(codes.InvalidArgument, "ERROR_REASON_VALIDATION")
		}
	}
	it := &corev1.CampItem{Id: f.newID(), Name: in.GetName(), Quantity: in.GetQuantity(), AssignedUserId: in.GetAssignedUserId()}
	e.items = append(e.items, it)
	return &corev1.AddCampItemResponse{Item: it}, nil
}

func (f *fakeServices) AddConvoyVehicle(ctx context.Context, in *corev1.AddConvoyVehicleRequest, _ ...grpc.CallOption) (*corev1.AddConvoyVehicleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "AddConvoyVehicle")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	e, err := f.event(in.GetTayfaId(), in.GetEventId())
	if err != nil {
		return nil, err
	}
	if e.rsvps[caller] != corev1.RsvpStatus_RSVP_STATUS_GOING {
		return nil, fail(codes.FailedPrecondition, "ERROR_REASON_EVENT_NOT_GOING")
	}
	if in.GetPlate() != "" {
		f.plates++
	}
	v := &corev1.ConvoyVehicle{Id: f.newID(), UserId: caller, Make: in.GetMake(), Model: in.GetModel(), ModelYear: in.GetModelYear(), Color: in.GetColor()}
	e.vehicles[caller] = v
	return &corev1.AddConvoyVehicleResponse{Vehicle: v}, nil
}

// AnnouncementService

func (f *fakeServices) CreateAnnouncement(ctx context.Context, in *corev1.CreateAnnouncementRequest, _ ...grpc.CallOption) (*corev1.CreateAnnouncementResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateAnnouncement")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true); err != nil {
		return nil, err
	}
	if prev, ok := f.replay(caller, "announcement", in.GetClientRequestId()); ok {
		return &corev1.CreateAnnouncementResponse{Announcement: f.notices[prev]}, nil
	}
	a := &corev1.Announcement{Id: f.newID(), TayfaId: in.GetTayfaId(), Title: in.GetTitle()}
	f.notices[a.Id] = a
	f.created(caller, "announcement", in.GetClientRequestId(), a.Id)
	return &corev1.CreateAnnouncementResponse{Announcement: a}, nil
}

func (f *fakeServices) SetAnnouncementPinned(ctx context.Context, in *corev1.SetAnnouncementPinnedRequest, _ ...grpc.CallOption) (*corev1.SetAnnouncementPinnedResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "SetAnnouncementPinned")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true); err != nil {
		return nil, err
	}
	a, ok := f.notices[in.GetAnnouncementId()]
	if !ok {
		return nil, fail(codes.NotFound, "ERROR_REASON_ANNOUNCEMENT_NOT_FOUND")
	}
	a.Pinned = in.GetPinned()
	return &corev1.SetAnnouncementPinnedResponse{}, nil
}

// QAService

func (f *fakeServices) CreateQuestion(ctx context.Context, in *corev1.CreateQuestionRequest, _ ...grpc.CallOption) (*corev1.CreateQuestionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateQuestion")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	if n := len([]rune(in.GetTitle())); n < 10 || n > 150 {
		return nil, fail(codes.InvalidArgument, "ERROR_REASON_VALIDATION")
	}
	if prev, ok := f.replay(caller, "question", in.GetClientRequestId()); ok {
		return &corev1.CreateQuestionResponse{Question: &corev1.Question{Id: prev}}, nil
	}
	q := f.newID()
	f.created(caller, "question", in.GetClientRequestId(), q)
	return &corev1.CreateQuestionResponse{Question: &corev1.Question{Id: q}}, nil
}

func (f *fakeServices) CreateAnswer(ctx context.Context, in *corev1.CreateAnswerRequest, _ ...grpc.CallOption) (*corev1.CreateAnswerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateAnswer")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	if prev, ok := f.replay(caller, "answer", in.GetClientRequestId()); ok {
		return &corev1.CreateAnswerResponse{Answer: &corev1.Answer{Id: prev}}, nil
	}
	a := f.newID()
	f.answers[a] = caller
	f.created(caller, "answer", in.GetClientRequestId(), a)
	return &corev1.CreateAnswerResponse{Answer: &corev1.Answer{Id: a}}, nil
}

func (f *fakeServices) SetAnswerVote(ctx context.Context, in *corev1.SetAnswerVoteRequest, _ ...grpc.CallOption) (*corev1.SetAnswerVoteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "SetAnswerVote")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	author, ok := f.answers[in.GetAnswerId()]
	if !ok {
		return nil, fail(codes.NotFound, "ERROR_REASON_ANSWER_NOT_FOUND")
	}
	if author == caller {
		return nil, fail(codes.FailedPrecondition, "ERROR_REASON_OWN_ANSWER")
	}
	if f.ansVotes[in.GetAnswerId()] == nil {
		f.ansVotes[in.GetAnswerId()] = map[string]bool{}
	}
	f.ansVotes[in.GetAnswerId()][caller] = true
	return &corev1.SetAnswerVoteResponse{}, nil
}

// PollService

func (f *fakeServices) CreatePoll(ctx context.Context, in *corev1.CreatePollRequest, _ ...grpc.CallOption) (*corev1.CreatePollResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreatePoll")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	if prev, ok := f.replay(caller, "poll", in.GetClientRequestId()); ok {
		return &corev1.CreatePollResponse{Poll: f.polls[prev].poll}, nil
	}
	p := &corev1.Poll{Id: f.newID(), TayfaId: in.GetTayfaId(), Question: in.GetQuestion(), Anonymous: in.GetAnonymous(), MultipleChoice: in.GetMultipleChoice()}
	for i, o := range in.GetOptions() {
		p.Options = append(p.Options, &corev1.PollOption{Id: f.newID(), Text: o, Position: int32(i)}) // #nosec G115 -- at most 10 options
	}
	f.polls[p.Id] = &fPoll{poll: p, votes: map[string][]string{}}
	f.created(caller, "poll", in.GetClientRequestId(), p.Id)
	return &corev1.CreatePollResponse{Poll: p}, nil
}

func (f *fakeServices) VotePoll(ctx context.Context, in *corev1.VotePollRequest, _ ...grpc.CallOption) (*corev1.VotePollResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "VotePoll")
	if err != nil {
		return nil, err
	}
	if _, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true); err != nil {
		return nil, err
	}
	p, ok := f.polls[in.GetPollId()]
	if !ok {
		return nil, fail(codes.NotFound, "ERROR_REASON_POLL_NOT_FOUND")
	}
	if len(in.GetOptionIds()) == 0 || (!p.poll.GetMultipleChoice() && len(in.GetOptionIds()) != 1) {
		return nil, fail(codes.InvalidArgument, "ERROR_REASON_VALIDATION")
	}
	p.votes[caller] = in.GetOptionIds()
	return &corev1.VotePollResponse{Poll: p.poll}, nil
}

// ChannelService

func (f *fakeServices) ListChannels(ctx context.Context, in *chatv1.ListChannelsRequest, _ ...grpc.CallOption) (*chatv1.ListChannelsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "ListChannels")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, false)
	if err != nil {
		return nil, err
	}
	out := &chatv1.ListChannelsResponse{}
	for _, c := range f.channels[t.id] {
		if c.GetVisibility() == chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF && t.roles[caller] < corev1.Role_ROLE_MODERATOR {
			continue
		}
		out.Channels = append(out.Channels, c)
	}
	return out, nil
}

func (f *fakeServices) CreateChannel(ctx context.Context, in *chatv1.CreateChannelRequest, _ ...grpc.CallOption) (*chatv1.CreateChannelResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "CreateChannel")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MODERATOR, true)
	if err != nil {
		return nil, err
	}
	for _, c := range f.channels[t.id] {
		if c.GetName() == in.GetName() {
			return nil, fail(codes.AlreadyExists, "ERROR_REASON_CHANNEL_NAME_TAKEN")
		}
	}
	vis := in.GetVisibility()
	if vis == chatv1.ChannelVisibility_CHANNEL_VISIBILITY_UNSPECIFIED {
		vis = chatv1.ChannelVisibility_CHANNEL_VISIBILITY_MEMBERS
	}
	c := &chatv1.Channel{Id: f.newID(), TayfaId: t.id, Name: in.GetName(), Visibility: vis}
	f.channels[t.id] = append(f.channels[t.id], c)
	f.objects["channel"]++
	return &chatv1.CreateChannelResponse{Channel: c}, nil
}

func (f *fakeServices) SendMessage(ctx context.Context, in *chatv1.SendMessageRequest, _ ...grpc.CallOption) (*chatv1.SendMessageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller, err := f.begin(ctx, "SendMessage")
	if err != nil {
		return nil, err
	}
	t, err := f.member(in.GetTayfaId(), caller, corev1.Role_ROLE_MEMBER, true)
	if err != nil {
		return nil, err
	}
	var ch *chatv1.Channel
	for _, c := range f.channels[t.id] {
		if c.GetId() == in.GetChannelId() {
			ch = c
		}
	}
	if ch == nil || (ch.GetVisibility() == chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF && t.roles[caller] < corev1.Role_ROLE_MODERATOR) {
		return nil, fail(codes.NotFound, "ERROR_REASON_CHANNEL_NOT_FOUND")
	}
	if prev, ok := f.replay(caller, "message", in.GetClientRequestId()); ok {
		return &chatv1.SendMessageResponse{Message: &chatv1.Message{Id: prev}}, nil
	}
	m := f.newID()
	f.created(caller, "message", in.GetClientRequestId(), m)
	return &chatv1.SendMessageResponse{Message: &chatv1.Message{Id: m}}, nil
}

package demo

import (
	"context"

	"google.golang.org/grpc"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The narrow parts of core's and chat's generated gRPC clients this package
// calls. The generated clients satisfy them; tests use fakes.

type TayfaAPI interface {
	CreateTayfa(ctx context.Context, in *corev1.CreateTayfaRequest, opts ...grpc.CallOption) (*corev1.CreateTayfaResponse, error)
	GetTayfa(ctx context.Context, in *corev1.GetTayfaRequest, opts ...grpc.CallOption) (*corev1.GetTayfaResponse, error)
}

type MembershipAPI interface {
	JoinByCode(ctx context.Context, in *corev1.JoinByCodeRequest, opts ...grpc.CallOption) (*corev1.JoinByCodeResponse, error)
	ListMembers(ctx context.Context, in *corev1.ListMembersRequest, opts ...grpc.CallOption) (*corev1.ListMembersResponse, error)
	ApproveMember(ctx context.Context, in *corev1.ApproveMemberRequest, opts ...grpc.CallOption) (*corev1.ApproveMemberResponse, error)
	ChangeRole(ctx context.Context, in *corev1.ChangeRoleRequest, opts ...grpc.CallOption) (*corev1.ChangeRoleResponse, error)
}

type EventAPI interface {
	CreateEvent(ctx context.Context, in *corev1.CreateEventRequest, opts ...grpc.CallOption) (*corev1.CreateEventResponse, error)
	GetEvent(ctx context.Context, in *corev1.GetEventRequest, opts ...grpc.CallOption) (*corev1.GetEventResponse, error)
	SetRsvp(ctx context.Context, in *corev1.SetRsvpRequest, opts ...grpc.CallOption) (*corev1.SetRsvpResponse, error)
	AddCampItem(ctx context.Context, in *corev1.AddCampItemRequest, opts ...grpc.CallOption) (*corev1.AddCampItemResponse, error)
	AddConvoyVehicle(ctx context.Context, in *corev1.AddConvoyVehicleRequest, opts ...grpc.CallOption) (*corev1.AddConvoyVehicleResponse, error)
}

type AnnouncementAPI interface {
	CreateAnnouncement(ctx context.Context, in *corev1.CreateAnnouncementRequest, opts ...grpc.CallOption) (*corev1.CreateAnnouncementResponse, error)
	SetAnnouncementPinned(ctx context.Context, in *corev1.SetAnnouncementPinnedRequest, opts ...grpc.CallOption) (*corev1.SetAnnouncementPinnedResponse, error)
}

type QAAPI interface {
	CreateQuestion(ctx context.Context, in *corev1.CreateQuestionRequest, opts ...grpc.CallOption) (*corev1.CreateQuestionResponse, error)
	CreateAnswer(ctx context.Context, in *corev1.CreateAnswerRequest, opts ...grpc.CallOption) (*corev1.CreateAnswerResponse, error)
	SetAnswerVote(ctx context.Context, in *corev1.SetAnswerVoteRequest, opts ...grpc.CallOption) (*corev1.SetAnswerVoteResponse, error)
}

type PollAPI interface {
	CreatePoll(ctx context.Context, in *corev1.CreatePollRequest, opts ...grpc.CallOption) (*corev1.CreatePollResponse, error)
	VotePoll(ctx context.Context, in *corev1.VotePollRequest, opts ...grpc.CallOption) (*corev1.VotePollResponse, error)
}

type ChannelAPI interface {
	ListChannels(ctx context.Context, in *chatv1.ListChannelsRequest, opts ...grpc.CallOption) (*chatv1.ListChannelsResponse, error)
	CreateChannel(ctx context.Context, in *chatv1.CreateChannelRequest, opts ...grpc.CallOption) (*chatv1.CreateChannelResponse, error)
	SendMessage(ctx context.Context, in *chatv1.SendMessageRequest, opts ...grpc.CallOption) (*chatv1.SendMessageResponse, error)
}

// Core is core's services the seeder calls.
type Core struct {
	Tayfas        TayfaAPI
	Memberships   MembershipAPI
	Events        EventAPI
	Announcements AnnouncementAPI
	QA            QAAPI
	Polls         PollAPI
}

func (c Core) complete() bool {
	return c.Tayfas != nil && c.Memberships != nil && c.Events != nil &&
		c.Announcements != nil && c.QA != nil && c.Polls != nil
}

// NewCore returns the Core for a connection to core.
func NewCore(conn grpc.ClientConnInterface) Core {
	return Core{
		Tayfas:        corev1.NewTayfaServiceClient(conn),
		Memberships:   corev1.NewMembershipServiceClient(conn),
		Events:        corev1.NewEventServiceClient(conn),
		Announcements: corev1.NewAnnouncementServiceClient(conn),
		QA:            corev1.NewQAServiceClient(conn),
		Polls:         corev1.NewPollServiceClient(conn),
	}
}

// NewChannelClient returns chat's ChannelService client for a connection to
// chat.
func NewChannelClient(conn grpc.ClientConnInterface) ChannelAPI {
	return chatv1.NewChannelServiceClient(conn)
}

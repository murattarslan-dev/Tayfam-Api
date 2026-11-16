package chatdm

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Channel message reports (issue #205, ADR-0031).

// ChannelClient is the part of chat's ChannelService core uses.
type ChannelClient interface {
	VerifyChannelMessageForReport(ctx context.Context, in *chatv1.VerifyChannelMessageForReportRequest, opts ...grpc.CallOption) (*chatv1.VerifyChannelMessageForReportResponse, error)
}

// ChannelMessages implements report.ChannelMessages with chat. The
// connection forwards the reporter's own token: chat answers only about
// messages the reporter can read.
type ChannelMessages struct {
	chat ChannelClient
}

// NewChannelMessages returns a ChannelMessages.
func NewChannelMessages(chat ChannelClient) *ChannelMessages { return &ChannelMessages{chat: chat} }

var reportKinds = map[chatv1.MessageKind]string{
	chatv1.MessageKind_MESSAGE_KIND_TEXT:  "text",
	chatv1.MessageKind_MESSAGE_KIND_PHOTO: "photo",
	chatv1.MessageKind_MESSAGE_KIND_PLACE: "place",
}

// ReportTarget implements report.ChannelMessages.
func (c *ChannelMessages) ReportTarget(ctx context.Context, tayfa, message id.ID) (report.ChannelMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.chat.VerifyChannelMessageForReport(ctx, &chatv1.VerifyChannelMessageForReportRequest{
		TayfaId: tayfa.String(), MessageId: message.String(),
	})
	if err != nil {
		return report.ChannelMessage{}, chatErr(err)
	}
	ch, okC := id.Parse(resp.GetChannelId())
	author, okA := id.Parse(resp.GetAuthorUserId())
	kind, okK := reportKinds[resp.GetKind()]
	if !okC || !okA || !okK {
		return report.ChannelMessage{}, apperr.ErrReportTargetNotFound
	}
	m := report.ChannelMessage{Channel: ch, Author: author, Kind: kind, Body: resp.GetBody(), CreatedAt: resp.GetCreateTime().AsTime()}
	if p := resp.GetPhotoUploadId(); p != "" {
		if m.Photo, okA = id.Parse(p); !okA {
			return report.ChannelMessage{}, apperr.ErrReportTargetNotFound
		}
	}
	if p := resp.GetPlace(); p != nil {
		m.Place = &report.Place{Latitude: p.GetLatitude(), Longitude: p.GetLongitude(), Name: p.GetName()}
	}
	return m, nil
}

// chatErr maps chat's refusal to the report's: not found (no reason), or
// unavailable.
func chatErr(err error) error {
	switch c := status.Code(err); c {
	case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied:
		return apperr.ErrReportTargetNotFound
	case codes.Unavailable, codes.DeadlineExceeded:
		return apperr.ErrUnavailable
	default:
		return fmt.Errorf("chat şikâyet doğrulaması: %s", c)
	}
}

// StatusClient is the part of chat's ReportedMessageService core uses.
type StatusClient interface {
	GetChannelMessageStatus(ctx context.Context, in *chatv1.GetChannelMessageStatusRequest, opts ...grpc.CallOption) (*chatv1.GetChannelMessageStatusResponse, error)
}

// ChannelStatus implements report.ChannelMessageStatus with chat. The
// connection carries core's service token for chat (ADR-0013).
type ChannelStatus struct {
	chat StatusClient
}

// NewChannelStatus returns a ChannelStatus.
func NewChannelStatus(chat StatusClient) *ChannelStatus { return &ChannelStatus{chat: chat} }

// Removed implements report.ChannelMessageStatus.
func (c *ChannelStatus) Removed(ctx context.Context, tayfa, message id.ID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.chat.GetChannelMessageStatus(ctx, &chatv1.GetChannelMessageStatusRequest{
		TayfaId: tayfa.String(), MessageId: message.String(),
	})
	if err != nil {
		switch c := status.Code(err); c {
		case codes.Unavailable, codes.DeadlineExceeded:
			return false, apperr.ErrUnavailable
		default:
			return false, fmt.Errorf("chat mesaj durumu: %s", c)
		}
	}
	return resp.GetRemoved(), nil
}

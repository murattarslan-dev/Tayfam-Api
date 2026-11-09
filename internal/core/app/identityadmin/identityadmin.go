// Package identityadmin connects core's platform panel methods to identity
// (issue #104 stage 2):
//
//   - Checker asks identity, with the caller's own admin token
//     (auth.ForwardToken), whether the caller is still a platform
//     administrator. platform_admins is identity's table; core never keeps
//     a copy of it.
//   - AuditConsumer is the outbox consumer that delivers platform.audit
//     events to identity's PlatformAuditService with core's service token
//     for identity (ADR-0013). identity records each entry once, keyed by
//     the event ID.
package identityadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// EventAudit is admin.EventAudit, named here so this package does not
// depend on the admin package.
const EventAudit = "platform.audit"

// ConsumerName is identity's position in core's event_consumers.
const ConsumerName = "identity"

const callTimeout = 5 * time.Second

// AdminClient is the part of identity's AdminAuthService core uses.
type AdminClient interface {
	GetAdmin(ctx context.Context, in *identityv1.GetAdminRequest, opts ...grpc.CallOption) (*identityv1.GetAdminResponse, error)
}

// Checker implements admin.Checker. Its client must forward the caller's
// token (auth.ForwardToken).
type Checker struct {
	client AdminClient
}

// NewChecker returns a Checker.
func NewChecker(c AdminClient) *Checker { return &Checker{client: c} }

// Check returns nil when identity confirms the admin token's user is a
// platform administrator.
func (c *Checker) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := c.client.GetAdmin(ctx, &identityv1.GetAdminRequest{})
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.Unauthenticated, codes.PermissionDenied:
		return apperr.ErrNotPlatformAdmin
	default:
		return fmt.Errorf("%w: identity: %w", apperr.ErrUnavailable, err)
	}
}

// AuditClient is identity's PlatformAuditService.
type AuditClient interface {
	RecordPlatformAudit(ctx context.Context, in *identityv1.RecordPlatformAuditRequest, opts ...grpc.CallOption) (*identityv1.RecordPlatformAuditResponse, error)
}

type payload struct {
	Action         string `json:"action"`
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	Reason         string `json:"reason"`
	AdminSessionID string `json:"admin_session_id"`
	TayfaID        string `json:"tayfa_id"`
	UserID         string `json:"user_id"`
	ReportID       string `json:"report_id"`
}

// Request converts a platform.audit event to identity's request.
func Request(e outbox.Event) (*identityv1.RecordPlatformAuditRequest, error) {
	if e.Type != EventAudit {
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	if e.Actor == (id.ID{}) {
		return nil, errors.New("denetim olayında platform yöneticisi yok")
	}
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, errors.New("denetim olayı çözümlenemedi")
	}
	return &identityv1.RecordPlatformAuditRequest{
		EventId: e.ID.String(), OccurTime: timestamppb.New(e.OccurredAt), ActorId: e.Actor.String(),
		Action: p.Action, TargetType: p.TargetType, TargetId: p.TargetID, Reason: p.Reason,
		AdminSessionId: p.AdminSessionID, TayfaId: p.TayfaID, UserId: p.UserID, ReportId: p.ReportID,
	}, nil
}

// AuditConsumer returns the outbox consumer for identity. client must carry
// core's service token for identity (auth.ServiceTokenFrom).
func AuditConsumer(client AuditClient) outbox.Consumer {
	return outbox.Consumer{
		Name:  ConsumerName,
		Types: []string{EventAudit},
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := Request(e)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			if _, err := client.RecordPlatformAudit(ctx, req); err != nil {
				return fmt.Errorf("identity: %w", err)
			}
			return nil
		},
	}
}

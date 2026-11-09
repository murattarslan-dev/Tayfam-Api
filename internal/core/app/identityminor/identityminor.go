// Package identityminor asks identity whether users are under 18 (ADR-0012
// "Şikâyetlerde yaş önceliği"; identity's MinorService.IsMinor). The
// connection carries core's service token for identity (ADR-0013,
// auth.ServiceTokenFrom); identity accepts it only from core's
// ServiceAccount.
//
// The answer is a yes/no per user and is used once, for a new report's
// priority (package report). It is never stored, returned or logged.
package identityminor

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// callTimeout bounds one call: filing a report waits for it, and the safe
// answer is ready when identity is slow.
const callTimeout = 3 * time.Second

// Client is the part of identity's MinorService core uses.
type Client interface {
	IsMinor(ctx context.Context, in *identityv1.IsMinorRequest, opts ...grpc.CallOption) (*identityv1.IsMinorResponse, error)
}

// Checker implements report.MinorChecker with identity.
type Checker struct {
	identity Client
}

// New returns a Checker. identity's connection must carry core's service
// token for identity.
func New(identity Client) *Checker { return &Checker{identity: identity} }

// IsMinor returns identity's answers. Users identity left out, and answers
// about users that were not asked, are not in the map. Any failure is an
// error; the caller then takes the safe side.
func (c *Checker) IsMinor(ctx context.Context, users []id.ID) (map[id.ID]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := &identityv1.IsMinorRequest{UserIds: make([]string, len(users))}
	asked := make(map[id.ID]bool, len(users))
	for i, u := range users {
		req.UserIds[i] = u.String()
		asked[u] = true
	}
	resp, err := c.identity.IsMinor(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("identity IsMinor: %s", status.Code(err))
	}
	out := make(map[id.ID]bool, len(resp.GetAnswers()))
	for _, a := range resp.GetAnswers() {
		u, ok := id.Parse(a.GetUserId())
		if !ok || !asked[u] {
			continue
		}
		out[u] = a.GetMinor()
	}
	return out, nil
}

// Unavailable is a MinorChecker for an environment without identity's
// service API (local only): every answer is "unknown", so every report is
// treated as involving a minor.
type Unavailable struct{}

// IsMinor implements report.MinorChecker.
func (Unavailable) IsMinor(context.Context, []id.ID) (map[id.ID]bool, error) {
	return nil, fmt.Errorf("identity IsMinor yapılandırılmadı")
}

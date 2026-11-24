// Package catalogrelay is the outbox consumer that delivers core's
// catalog.changed events to identity's CatalogCopyService (issue #280,
// ADR-0042, ADR-0002). identity keeps a copy of the catalog's interests and
// checks profile interests against it.
//
// It is its own consumer (identity.catalog), apart from the platform audit
// consumer (identity, package identityadmin): one stuck target does not
// hold up the other. The request carries the catalog version and every
// interest's key and status, nothing else; identity applies it only when
// the version is newer, so repeated delivery is harmless.
package catalogrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// ConsumerName is the catalog copy's position in core's event_consumers.
const ConsumerName = "identity.catalog"

const callTimeout = 10 * time.Second

// Client is identity's CatalogCopyService.
type Client interface {
	ApplyCatalogChange(ctx context.Context, in *identityv1.ApplyCatalogChangeRequest, opts ...grpc.CallOption) (*identityv1.ApplyCatalogChangeResponse, error)
}

var statuses = map[string]identityv1.CatalogInterestStatus{
	catalog.StatusActive:  identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_ACTIVE,
	catalog.StatusRetired: identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_RETIRED,
}

// Request converts a catalog.changed event to identity's request. A payload
// that does not decode, or has an unknown status, is an error: the relay
// retries and the event does not move on (a broken payload must not leave
// identity with a partial copy).
func Request(e outbox.Event) (*identityv1.ApplyCatalogChangeRequest, error) {
	if e.Type != catalog.EventChanged {
		return nil, fmt.Errorf("bilinmeyen olay türü %q", e.Type)
	}
	var p catalog.Changed
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, errors.New("katalog olayı çözümlenemedi")
	}
	if p.CatalogVersion < 1 {
		return nil, errors.New("katalog olayında sürüm yok")
	}
	req := &identityv1.ApplyCatalogChangeRequest{
		EventId: e.ID.String(), CatalogVersion: p.CatalogVersion,
		Interests: make([]*identityv1.CatalogInterestState, 0, len(p.Interests)),
	}
	for _, i := range p.Interests {
		st, ok := statuses[i.Status]
		if !ok {
			return nil, errors.New("katalog olayında bilinmeyen ilgi alanı durumu")
		}
		req.Interests = append(req.Interests, &identityv1.CatalogInterestState{Key: i.Key, Status: st})
	}
	return req, nil
}

// Consumer returns the outbox consumer for identity's catalog copy. client
// must carry core's service token for identity (auth.ServiceTokenFrom,
// ADR-0013).
func Consumer(client Client) outbox.Consumer {
	return outbox.Consumer{
		Name:  ConsumerName,
		Types: []string{catalog.EventChanged},
		Deliver: func(ctx context.Context, e outbox.Event) error {
			req, err := Request(e)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			if _, err := client.ApplyCatalogChange(ctx, req); err != nil {
				return fmt.Errorf("identity: %w", err)
			}
			return nil
		},
	}
}

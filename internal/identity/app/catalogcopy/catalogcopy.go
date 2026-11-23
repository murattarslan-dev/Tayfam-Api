// Package catalogcopy applies core's catalog.changed events to identity's
// copy of the event type catalog (CatalogCopyService,
// proto/tayfam/identity/v1/catalog_copy.proto; issue #280, ADR-0042,
// ADR-0025 §10).
//
// The copy is catalog_copy (the catalog version it reflects) and
// catalog_interests (interest keys and their status); package profilefields
// checks profile interests against it. A change is applied in one
// transaction and only when its version is newer than the copy's, so a
// repeated or late event changes nothing (the relay delivers at least
// once). The change carries the catalog's complete list: a key it lacks is
// retired, never deleted, because profiles keep showing it.
//
// Every value is checked again here (ADR-0011): the request comes from
// core's relay, but a taken-over caller must not be able to write
// arbitrary keys.
package catalogcopy

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
)

// Stored values of catalog_interests.status.
const (
	StatusActive  = "active"
	StatusRetired = "retired"
)

// MaxInterests bounds one change (the catalog has 16 interests; core's
// event payload is limited to 8 KiB, about 130).
const MaxInterests = 200

// ErrInvalid: the change does not pass validation; nothing was applied.
var ErrInvalid = errors.New("geçersiz katalog değişikliği")

// keyPattern matches the catalog_interests_key CHECK.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// Interest is one interest of the catalog.
type Interest struct {
	Key    string
	Status string // StatusActive or StatusRetired
}

// Change is the content of one catalog.changed event.
type Change struct {
	Version   int64
	Interests []Interest
}

// Validate checks c: version at least 1, 1–MaxInterests interests, valid
// and unique keys, known statuses.
func (c Change) Validate() error {
	if c.Version < 1 {
		return fmt.Errorf("%w: sürüm", ErrInvalid)
	}
	if len(c.Interests) == 0 || len(c.Interests) > MaxInterests {
		return fmt.Errorf("%w: ilgi alanı sayısı", ErrInvalid)
	}
	seen := make(map[string]bool, len(c.Interests))
	for _, i := range c.Interests {
		if !keyPattern.MatchString(i.Key) || seen[i.Key] {
			return fmt.Errorf("%w: ilgi alanı anahtarı", ErrInvalid)
		}
		seen[i.Key] = true
		if i.Status != StatusActive && i.Status != StatusRetired {
			return fmt.Errorf("%w: ilgi alanı durumu", ErrInvalid)
		}
	}
	return nil
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
}

// Service applies catalog changes.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("catalogcopy: eksik yapılandırma")
	}
	return &Service{pool: cfg.Pool}, nil
}

// Apply applies c in one transaction and reports whether the copy changed:
// false when c's version is not newer than the copy's. An invalid c is
// ErrInvalid.
func (s *Service) Apply(ctx context.Context, c Change) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	applied := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// The version first: the row lock it takes orders concurrent
		// deliveries, and an older change stops here.
		n, err := q.AdvanceCatalogCopyVersion(ctx, c.Version)
		if err != nil {
			return fmt.Errorf("kopya sürümü: %w", err)
		}
		if n == 0 {
			return nil
		}
		keys := make([]string, 0, len(c.Interests))
		for _, i := range c.Interests {
			if err := q.UpsertCatalogInterest(ctx, store.UpsertCatalogInterestParams{Key: i.Key, Status: i.Status}); err != nil {
				return fmt.Errorf("kopya ilgi alanı: %w", err)
			}
			keys = append(keys, i.Key)
		}
		if _, err := q.RetireMissingCatalogInterests(ctx, keys); err != nil {
			return fmt.Errorf("kopyadan kalkan ilgi alanları: %w", err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

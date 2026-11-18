// Package catalog serves the event type catalog (CatalogService,
// proto/tayfam/core/v1/catalog.proto; issue #237, ADR-0025 §2 and §8,
// ADR-0037).
//
// Event types are data in core's database: catalog_interests,
// catalog_event_types (the current version of each type),
// catalog_event_type_versions (every version, immutable) and catalog_meta
// (the version of the catalog as a whole). The service's database user can
// only read them.
//
// What is read is checked again before it leaves (ADR-0011, "assume
// breach"): keys, labels and every definition go through the closed schema
// of package definition. A row that does not pass fails the request as an
// internal error instead of reaching a client that would draw it.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// Stored values of catalog_interests.status and catalog_event_types.status.
const (
	StatusActive  = "active"
	StatusRetired = "retired"
)

// Stored values of catalog_event_types.modes (the same as events.mode).
const (
	ModeInPerson = "in_person"
	ModeOnline   = "online"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
}

// Service implements CatalogService's business logic.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("catalog: eksik yapılandırma")
	}
	return &Service{pool: cfg.Pool}, nil
}

// Interest groups event types.
type Interest struct {
	Key      string
	Position int32
	// Labels maps a language to the interest's name.
	Labels         map[string]string
	Icon           string
	Status         string
	DefaultWidgets []string
}

// EventType is one version of an event type.
type EventType struct {
	Key string
	// InterestKeys has one to three keys; the first is the main interest.
	InterestKeys []string
	Labels       map[string]string
	Icon         string
	Modes        []string
	Status       string
	Version      int32
	// Definition is the validated definition as compact JSON.
	Definition []byte
	// Parsed is Definition as package definition read it.
	Parsed *definition.Definition
}

// Catalog is the catalog as clients get it.
type Catalog struct {
	// Version grows with every change to the catalog.
	Version int64
	// Interests holds every interest, retired ones included, by position.
	Interests []Interest
	// Types holds the current version of every active type, by position.
	Types []EventType
}

// Get returns the catalog. When known is the catalog's current version the
// lists are not read: notModified is true and only Version is set.
func (s *Service) Get(ctx context.Context, known int64) (c Catalog, notModified bool, err error) {
	// One snapshot: the version and the lists belong together.
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		q := store.New(tx)
		v, err := q.GetCatalogVersion(ctx)
		if err != nil {
			return fmt.Errorf("katalog sürümü: %w", err)
		}
		c = Catalog{Version: v}
		if known == v {
			notModified = true
			return nil
		}
		interests, err := q.ListCatalogInterests(ctx)
		if err != nil {
			return fmt.Errorf("ilgi alanları: %w", err)
		}
		c.Interests = make([]Interest, 0, len(interests))
		for _, r := range interests {
			in := Interest{Key: r.Key, Position: r.Position, Icon: r.Icon, Status: r.Status, DefaultWidgets: r.DefaultWidgets}
			if in.Labels, err = entry(r.Key, r.Icon, r.Status, r.Labels); err != nil {
				return fmt.Errorf("ilgi alanı %q: %w", r.Key, err)
			}
			for _, w := range r.DefaultWidgets {
				if !definition.ValidKey(w) {
					return fmt.Errorf("ilgi alanı %q: geçersiz ana sayfa bileşeni", r.Key)
				}
			}
			c.Interests = append(c.Interests, in)
		}
		types, err := q.ListActiveCatalogEventTypes(ctx)
		if err != nil {
			return fmt.Errorf("türler: %w", err)
		}
		c.Types = make([]EventType, 0, len(types))
		for _, r := range types {
			t, err := eventType(r.Key, r.Version, r.InterestKeys, r.Modes, r.Icon, StatusActive, r.Labels, r.Definition)
			if err != nil {
				return err
			}
			c.Types = append(c.Types, t)
		}
		return nil
	})
	if err != nil {
		return Catalog{}, false, err
	}
	return c, notModified, nil
}

// TypeVersion returns one version of an event type; retired types and old
// versions too. An unknown key or version is apperr.ErrCatalogTypeNotFound.
func (s *Service) TypeVersion(ctx context.Context, key string, version int32) (EventType, error) {
	return TypeVersion(ctx, store.New(s.pool), key, version)
}

// TypeVersion is Service.TypeVersion inside the caller's transaction, for
// the services that validate against a type version (events).
func TypeVersion(ctx context.Context, q *store.Queries, key string, version int32) (EventType, error) {
	if !definition.ValidKey(key) {
		return EventType{}, apperr.Validation("key")
	}
	if version < 1 {
		return EventType{}, apperr.Validation("version")
	}
	r, err := q.GetCatalogEventTypeVersion(ctx, store.GetCatalogEventTypeVersionParams{TypeKey: key, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventType{}, apperr.ErrCatalogTypeNotFound
	}
	if err != nil {
		return EventType{}, fmt.Errorf("tür sürümü: %w", err)
	}
	return eventType(r.TypeKey, r.Version, r.InterestKeys, r.Modes, r.Icon, r.Status, r.Labels, r.Definition)
}

// CurrentType returns the current version of an event type inside the
// caller's transaction: the version a new event is created with. An
// unknown key is apperr.ErrCatalogTypeNotFound; a retired type is returned
// with StatusRetired, and the caller refuses it.
func CurrentType(ctx context.Context, q *store.Queries, key string) (EventType, error) {
	if !definition.ValidKey(key) {
		return EventType{}, apperr.ErrCatalogTypeNotFound
	}
	r, err := q.GetCatalogEventType(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventType{}, apperr.ErrCatalogTypeNotFound
	}
	if err != nil {
		return EventType{}, fmt.Errorf("tür: %w", err)
	}
	return eventType(r.Key, r.Version, r.InterestKeys, r.Modes, r.Icon, r.Status, r.Labels, r.Definition)
}

// eventType checks a type row and parses its definition.
func eventType(key string, version int32, interests, modes []string, icon, status string, labels, def []byte) (EventType, error) {
	t := EventType{Key: key, InterestKeys: interests, Icon: icon, Modes: modes, Status: status, Version: version}
	var err error
	if t.Labels, err = entry(key, icon, status, labels); err != nil {
		return EventType{}, fmt.Errorf("tür %q: %w", key, err)
	}
	if version < 1 || len(interests) < 1 || len(interests) > 3 || len(modes) < 1 || len(modes) > 2 {
		return EventType{}, fmt.Errorf("tür %q: geçersiz kayıt", key)
	}
	for _, k := range interests {
		if !definition.ValidKey(k) {
			return EventType{}, fmt.Errorf("tür %q: geçersiz ilgi alanı", key)
		}
	}
	for _, m := range modes {
		if m != ModeInPerson && m != ModeOnline {
			return EventType{}, fmt.Errorf("tür %q: geçersiz mod", key)
		}
	}
	// jsonb text has spaces after separators; the size limit is on the
	// compact form.
	var compact bytes.Buffer
	if err := json.Compact(&compact, def); err != nil {
		return EventType{}, fmt.Errorf("tür %q sürüm %d: tanım okunamadı: %w", key, version, err)
	}
	t.Definition = compact.Bytes()
	if t.Parsed, err = definition.Parse(t.Definition); err != nil {
		return EventType{}, fmt.Errorf("tür %q sürüm %d: %w", key, version, err)
	}
	return t, nil
}

// entry checks the columns interests and types share and decodes the
// labels.
func entry(key, icon, status string, labels []byte) (map[string]string, error) {
	if !definition.ValidKey(key) || !definition.ValidKey(icon) {
		return nil, errors.New("geçersiz anahtar ya da ikon")
	}
	if status != StatusActive && status != StatusRetired {
		return nil, errors.New("geçersiz durum")
	}
	var m map[string]string
	if err := json.Unmarshal(labels, &m); err != nil {
		return nil, fmt.Errorf("adlar okunamadı: %w", err)
	}
	if _, ok := m["tr"]; !ok || len(m) > definition.MaxLanguages {
		return nil, errors.New("tr adı yok ya da çok fazla dil")
	}
	for lang, text := range m {
		if len(lang) != 2 || !definition.ValidLabel(text) {
			return nil, errors.New("geçersiz ad")
		}
	}
	return m, nil
}

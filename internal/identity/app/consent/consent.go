// Package consent holds the versions of the texts a user accepts (privacy
// notice and terms of use) and records acceptances in consent_records
// (data model v14 "Onay kaydı", ADR-0008).
//
// Records are only added. When a text is republished its version changes in
// configuration; a registered user who has not accepted the new version is
// asked again at the next sign-in (Required), and Record adds the new
// acceptance next to the old ones.
package consent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Document types (consent_records_document_type CHECK).
const (
	TypePrivacyNotice = "privacy_notice"
	TypeTerms         = "terms"
)

// Documents are the current versions of the texts a user must accept, such
// as "2026-11-08". They come from configuration and change when a text is
// republished.
type Documents struct {
	PrivacyNotice string
	Terms         string
}

// Valid reports whether both versions are set.
func (d Documents) Valid() bool { return d.PrivacyNotice != "" && d.Terms != "" }

// Matches reports whether the versions a client sent are the current ones.
func (d Documents) Matches(privacyNotice, terms string) bool {
	return privacyNotice == d.PrivacyNotice && terms == d.Terms
}

func (d Documents) each() [2]struct{ typ, version string } {
	return [2]struct{ typ, version string }{{TypePrivacyNotice, d.PrivacyNotice}, {TypeTerms, d.Terms}}
}

// Required reports whether user has not accepted, or has withdrawn, the
// current version of any document.
func Required(ctx context.Context, q *store.Queries, user id.ID, d Documents) (bool, error) {
	for _, doc := range d.each() {
		ok, err := q.HasActiveConsent(ctx, store.HasActiveConsentParams{UserID: user.PG(), DocumentType: doc.typ, DocumentVersion: doc.version})
		if err != nil {
			return false, fmt.Errorf("onay kaydı: %w", err)
		}
		if !ok {
			return true, nil
		}
	}
	return false, nil
}

// Record stores that user accepted the versions in d. Accepting a version
// again adds nothing.
func Record(ctx context.Context, q *store.Queries, user id.ID, d Documents) error {
	for _, doc := range d.each() {
		if err := q.RecordConsent(ctx, store.RecordConsentParams{ID: id.New().PG(), UserID: user.PG(), DocumentType: doc.typ, DocumentVersion: doc.version}); err != nil {
			return fmt.Errorf("onay kaydı: %w", err)
		}
	}
	return nil
}

var (
	// ErrOutdated: the versions the client accepted are not the current
	// ones; it fetches the status again and shows the new texts.
	ErrOutdated = errors.New("onaylanan belge sürümü güncel değil")
	// ErrUnknownUser: the caller has no registered, live account.
	ErrUnknownUser = errors.New("kullanıcı bulunamadı")
)

// Service lets a registered user see and accept the current versions.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	docs Documents
}

// NewService returns a Service for the current versions docs.
func NewService(pool *pgxpool.Pool, docs Documents) (*Service, error) {
	if pool == nil || !docs.Valid() {
		return nil, errors.New("consent: eksik yapılandırma")
	}
	return &Service{pool: pool, q: store.New(pool), docs: docs}, nil
}

// Status is the current versions and whether the caller must accept them.
type Status struct {
	Documents Documents
	Required  bool
}

// Status returns the caller's consent status.
func (s *Service) Status(ctx context.Context, user id.ID) (Status, error) {
	if err := s.registered(ctx, s.q, user); err != nil {
		return Status{}, err
	}
	req, err := Required(ctx, s.q, user, s.docs)
	if err != nil {
		return Status{}, err
	}
	return Status{Documents: s.docs, Required: req}, nil
}

// Accept records that the caller accepted the current versions. The
// versions the client showed are sent back and must be the current ones,
// so a text published while the screen was open is not accepted unseen.
func (s *Service) Accept(ctx context.Context, user id.ID, privacyNotice, terms string) error {
	if !s.docs.Matches(privacyNotice, terms) {
		return ErrOutdated
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := s.registered(ctx, q, user); err != nil {
			return err
		}
		return Record(ctx, q, user, s.docs)
	})
}

func (s *Service) registered(ctx context.Context, q *store.Queries, user id.ID) error {
	u, err := q.GetUser(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownUser
	}
	if err != nil {
		return fmt.Errorf("kullanıcı: %w", err)
	}
	if !u.RegisteredAt.Valid {
		return ErrUnknownUser
	}
	return nil
}

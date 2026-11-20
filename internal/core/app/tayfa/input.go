package tayfa

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/home"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Length limits (veri modeli v14; the same CHECKs are in the database).
const (
	nameMin        = 3
	nameMax        = 50
	descriptionMax = 500
	// maxRuneBytes bounds the byte length before counting characters, so an
	// oversized request is refused without scanning all of it.
	maxRuneBytes = 4
)

// MaxInterests bounds the interests of one tayfa. The catalog decides which
// keys exist (catalog_interests); this only bounds a request.
const MaxInterests = 20

// Modules are the modules a tayfa can turn on or off, in Module order
// (tayfa_modules.module).
var Modules = []string{"announcements", "events", "qa", "polls"}

// Join policies (tayfas.join_policy).
const (
	JoinApproval = "approval"
	JoinOpen     = "open"
)

// Update mask paths (UpdateTayfaRequest.update_mask).
const (
	FieldName        = "name"
	FieldDescription = "description"
	FieldInterests   = "interests"
	FieldJoinPolicy  = "join_policy"
	// FieldMembersCanCreateEvents is the event creation setting
	// (tayfas.members_can_create_events).
	FieldMembersCanCreateEvents = "members_can_create_events"
	// FieldNowCreateMinRole is the Şimdi başlat setting
	// (tayfas.now_create_min_role, ADR-0038 §2).
	FieldNowCreateMinRole = "now_create_min_role"
	// FieldCover is the cover image (tayfas.cover_upload_id, issue #135).
	FieldCover = "cover_upload_id"
)

// cleanName trims surrounding spaces and returns the name and its
// normalized form (CLAUDE.md "Türkçe metin").
func cleanName(s string) (name, normalized string, err error) {
	if len(s) > (nameMax+64)*maxRuneBytes {
		return "", "", apperr.Validation(FieldName)
	}
	name = strings.TrimSpace(s)
	if !validText(name, false) {
		return "", "", apperr.Validation(FieldName)
	}
	normalized = strings.ToLowerSpecial(unicode.TurkishCase, name)
	for _, v := range []string{name, normalized} {
		if n := utf8.RuneCountInString(v); n < nameMin || n > nameMax {
			return "", "", apperr.Validation(FieldName)
		}
	}
	return name, normalized, nil
}

// cleanDescription trims surrounding spaces; an empty description is stored
// as NULL.
func cleanDescription(s string) (string, error) {
	if len(s) > (descriptionMax+64)*maxRuneBytes {
		return "", apperr.Validation(FieldDescription)
	}
	d := strings.TrimSpace(s)
	if !validText(d, true) || utf8.RuneCountInString(d) > descriptionMax {
		return "", apperr.Validation(FieldDescription)
	}
	return d, nil
}

// validText refuses invalid UTF-8 and control characters; multiline text
// may contain line breaks and tabs.
func validText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// cleanInterests checks the shape of a request's interests: at most
// MaxInterests distinct catalog keys. Whether the keys are in the catalog
// is checkInterests' question, asked inside the transaction.
func cleanInterests(in []string) ([]string, error) {
	if len(in) > MaxInterests {
		return nil, apperr.Validation(FieldInterests)
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !definition.ValidKey(v) || slices.Contains(out, v) {
			return nil, apperr.Validation(FieldInterests)
		}
		out = append(out, v)
	}
	return out, nil
}

// checkInterests checks keys against the catalog (ADR-0025 §2): every key
// is an interest of the catalog, and a retired interest can only be kept,
// not chosen: it passes only when the tayfa already lists it (current).
func checkInterests(ctx context.Context, q *store.Queries, keys, current []string) error {
	if len(keys) == 0 {
		return nil
	}
	rows, err := q.ListCatalogInterestStatuses(ctx)
	if err != nil {
		return fmt.Errorf("katalog ilgi alanları: %w", err)
	}
	status := make(map[string]string, len(rows))
	for _, r := range rows {
		status[r.Key] = r.Status
	}
	for _, k := range keys {
		switch status[k] {
		case catalog.StatusActive:
		case catalog.StatusRetired:
			if !slices.Contains(current, k) {
				return apperr.Validation(FieldInterests)
			}
		default:
			return apperr.Validation(FieldInterests)
		}
	}
	return nil
}

// setInterests replaces the tayfa's interests with keys after checking
// them against the catalog, and switches on the home page widgets: with
// sections the former fixed sections (a new or converted tayfa), and the
// default widgets of the interests added (ADR-0027 §3; home.AddDefaults).
func setInterests(ctx context.Context, q *store.Queries, tayfa id.ID, keys []string, sections bool, now time.Time) error {
	current, err := q.ListTayfaInterests(ctx, tayfa.PG())
	if err != nil {
		return fmt.Errorf("ilgi alanları: %w", err)
	}
	if err := checkInterests(ctx, q, keys, current); err != nil {
		return err
	}
	if err := q.DeleteTayfaInterests(ctx, tayfa.PG()); err != nil {
		return fmt.Errorf("ilgi alanları: %w", err)
	}
	var added []string
	for _, k := range keys {
		if err := q.AddTayfaInterest(ctx, store.AddTayfaInterestParams{TayfaID: tayfa.PG(), InterestKey: k}); err != nil {
			return fmt.Errorf("ilgi alanı: %w", err)
		}
		if !slices.Contains(current, k) {
			added = append(added, k)
		}
	}
	return home.AddDefaults(ctx, q, tayfa, added, sections, now)
}

func validJoinPolicy(p string) bool { return p == JoinApproval || p == JoinOpen }

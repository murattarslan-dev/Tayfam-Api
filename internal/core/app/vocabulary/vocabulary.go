// Package vocabulary is the tayfa vocabulary (issue #244; ADR-0037 §4): the
// tayfa's own list of values in a namespace (fish_species, book …), so that
// the same species or book written two ways does not split the statistics.
// Tayfam provides no content; the values come from the members' entries.
//
// A text field of a type definition is bound to a vocabulary by its
// `vocabulary.namespace` (package definition). A component that stores such
// a field (the log, package logbook; later the library) takes either an
// existing value's ID (Lookup) or a text (Ensure): a known text is reused by
// its normalized form (Normalize: Turkish lower case, single spaces), a new
// one is added with its writer. The component keeps the value's ID in a
// table of its own, with a foreign key; the text shown is the merge
// target's.
//
// Who may do what (the permission table, package authz):
//
//	read          every active member, muted ones included (TayfaView)
//	merge, undo   moderators and admins (TayfaVocabularyMerge), not on a
//	              read-only tayfa
//
// Merges stay one level deep: a value is merged into a value that is not
// merged itself, and the values merged into it move along to the target.
// Entries keep the value they chose, so undoing a merge gives them back
// their own. A merge and its undo write an audit record and the
// TypeMerged / TypeUnmerged outbox event, which names the years of the
// affected entries for the statistics to recompute (ADR-0026 §2; issue
// #266 consumes it). Values never reach a log, an audit record or an event;
// IDs and the namespace do.
//
// Account deletion keeps the values (they are the tayfa's) and clears who
// added them; export lists a person's entries with their values (package
// export).
package vocabulary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// MaxValueLength bounds a value, in characters (tayfa_vocabulary_value).
const MaxValueLength = 60

// MaxResults bounds a list.
const MaxResults = 20

// Audit actions; the records name the values by ID only.
const (
	AuditMerged   = "tayfa.vocabulary_merged"
	AuditUnmerged = "tayfa.vocabulary_unmerged"
)

// Outbox events of a merge and its undo: the hook of the statistics
// (ADR-0037 §4, ADR-0026 §2). The payload names the tayfa, the namespace,
// the values and the years (Europe/Istanbul) of the events whose entries
// chose the merged value; the statistics recompute those years, which is
// idempotent.
const (
	TypeMerged   = "tayfa.vocabulary_merged"
	TypeUnmerged = "tayfa.vocabulary_unmerged"
)

// targetType is the audit record's and event's subject type.
const targetType = "vocabulary"

// Value is a value of a tayfa's vocabulary.
type Value struct {
	ID    id.ID
	Value string
	// MergedInto and MergedIntoValue are set only on a merged value.
	MergedInto      id.ID
	MergedIntoValue string
}

// List is a list of values.
type List struct {
	Values []Value
	// CallerCanMerge reports whether the caller may merge and undo now.
	CallerCanMerge bool
}

// Clean returns the shown form of a value: trimmed, with single spaces. ok
// is false for an empty value, one longer than max (at most
// MaxValueLength) characters, or one with a control character.
func Clean(s string, maxLen int) (clean string, ok bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return "", false
		}
	}
	clean = strings.Join(strings.Fields(s), " ")
	if maxLen <= 0 || maxLen > MaxValueLength {
		maxLen = MaxValueLength
	}
	if n := utf8.RuneCountInString(clean); n < 1 || n > maxLen {
		return "", false
	}
	return clean, true
}

// Normalize returns the form values are told apart by: Turkish lower case
// and single spaces (ADR-0037 §4; "Kırlangıç" and " kırlangıç " are the
// same value).
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLowerSpecial(unicode.TurkishCase, s)), " ")
}

// Ensure returns the ID of the value text in the tayfa's namespace, adding
// it with user as its writer if the vocabulary does not know it. maxLen is
// the field's bound (MaxValueLength at most). It runs in the caller's
// transaction (q); the caller has checked the tayfa and the caller's
// standing. A bad text is a validation error on field.
//
// The new value passes the same checks as every free text of the tayfa
// (Clean); the ADR-0012 term check is not in core yet.
func Ensure(ctx context.Context, q *store.Queries, tayfa, user id.ID, namespace, text string, maxLen int, now time.Time, field string) (id.ID, error) {
	value, ok := Clean(text, maxLen)
	if !ok || !definition.ValidKey(namespace) {
		return id.ID{}, apperr.Validation(field)
	}
	norm := Normalize(value)
	find := func() (id.ID, bool, error) {
		v, err := q.FindVocabularyValue(ctx, store.FindVocabularyValueParams{TayfaID: tayfa.PG(), Namespace: namespace, ValueNormalized: norm})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return id.ID{}, false, nil
		case err != nil:
			return id.ID{}, false, fmt.Errorf("sözlük: %w", err)
		}
		return id.ID(v.Bytes), true, nil
	}
	if v, ok, err := find(); err != nil || ok {
		return v, err
	}
	created, err := q.CreateVocabularyValue(ctx, store.CreateVocabularyValueParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), Namespace: namespace, Value: value, ValueNormalized: norm,
		CreatedBy: user.PG(), CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	switch {
	case err == nil:
		return id.ID(created.Bytes), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return id.ID{}, fmt.Errorf("sözlük değeri: %w", err)
	}
	// Added at the same time by someone else.
	v, ok, err := find()
	if err != nil {
		return id.ID{}, err
	}
	if !ok {
		return id.ID{}, errors.New("sözlük değeri eklenemedi")
	}
	return v, nil
}

// Lookup checks that value is a value of the tayfa's namespace, in the
// caller's transaction; an unknown one is a validation error on field.
func Lookup(ctx context.Context, q *store.Queries, tayfa id.ID, namespace string, value id.ID, field string) error {
	_, err := q.GetVocabularyValue(ctx, store.GetVocabularyValueParams{TayfaID: tayfa.PG(), Namespace: namespace, ID: value.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.Validation(field)
	case err != nil:
		return fmt.Errorf("sözlük değeri: %w", err)
	}
	return nil
}

// Service serves the vocabulary to members and moderators, built on the
// component framework's tayfa scope.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func checkNamespace(namespace string) error {
	if !definition.ValidKey(namespace) {
		return apperr.Validation("namespace")
	}
	return nil
}

// List returns up to MaxResults values of the namespace whose text, or the
// text of a value merged into them, contains query (normalized); those
// that start with it first. With merged, it returns the merged values with
// their targets instead. Refused: anyone but an active member
// (apperr.ErrTayfaNotFound), a bad namespace or query (validation error).
func (s *Service) List(ctx context.Context, user, tayfa id.ID, namespace, query string, merged bool) (List, error) {
	if err := checkNamespace(namespace); err != nil {
		return List{}, err
	}
	q := Normalize(query)
	if !utf8.ValidString(query) || utf8.RuneCountInString(q) > MaxValueLength {
		return List{}, apperr.Validation("query")
	}
	var out List
	err := s.fw.Tayfa(ctx, user, tayfa, false, func(ts *component.TayfaScope) error {
		if err := ts.Authorize(authz.TayfaView); err != nil {
			return err
		}
		out = List{Values: []Value{}, CallerCanMerge: ts.Authorize(authz.TayfaVocabularyMerge) == nil && !ts.ReadOnly()}
		if merged {
			rows, err := ts.Q().SearchMergedVocabulary(ctx, store.SearchMergedVocabularyParams{
				TayfaID: tayfa.PG(), Namespace: namespace, Query: q, N: MaxResults,
			})
			if err != nil {
				return fmt.Errorf("sözlük: %w", err)
			}
			for _, r := range rows {
				out.Values = append(out.Values, Value{
					ID: id.ID(r.ID.Bytes), Value: r.Value, MergedInto: id.ID(r.TargetID.Bytes), MergedIntoValue: r.TargetValue,
				})
			}
			return nil
		}
		rows, err := ts.Q().SearchVocabulary(ctx, store.SearchVocabularyParams{
			TayfaID: tayfa.PG(), Namespace: namespace, Query: q, N: MaxResults,
		})
		if err != nil {
			return fmt.Errorf("sözlük: %w", err)
		}
		for _, r := range rows {
			out.Values = append(out.Values, Value{ID: id.ID(r.ID.Bytes), Value: r.Value})
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// locked is a value read for a merge.
type locked struct {
	value  string
	merged bool
}

func lock(ctx context.Context, ts *component.TayfaScope, namespace string, v id.ID) (locked, error) {
	r, err := ts.Q().GetVocabularyValueForUpdate(ctx, store.GetVocabularyValueForUpdateParams{
		TayfaID: ts.Tayfa.PG(), Namespace: namespace, ID: v.PG(),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return locked{}, apperr.ErrVocabularyNotFound
	case err != nil:
		return locked{}, fmt.Errorf("sözlük değeri: %w", err)
	}
	return locked{value: r.Value, merged: r.MergedInto.Valid}, nil
}

// years returns the years of the events whose live entries chose v or a
// value merged into it.
func years(ctx context.Context, ts *component.TayfaScope, namespace string, v id.ID) ([]int32, error) {
	ys, err := ts.Q().VocabularyEntryYears(ctx, store.VocabularyEntryYearsParams{TayfaID: ts.Tayfa.PG(), Namespace: namespace, ID: v.PG()})
	if err != nil {
		return nil, fmt.Errorf("etkilenen yıllar: %w", err)
	}
	if ys == nil {
		ys = []int32{}
	}
	return ys, nil
}

// Merge merges source into target, two values of the namespace. Refused:
// source and target the same (validation error on target_id), an unknown
// value (apperr.ErrVocabularyNotFound), a merged source or target
// (apperr.ErrVocabularyMerged), the permission (apperr.ErrPermissionDenied,
// apperr.ErrMuted), a read-only tayfa (apperr.ErrTayfaReadOnly) and anyone
// but an active member (apperr.ErrTayfaNotFound).
func (s *Service) Merge(ctx context.Context, user, tayfa id.ID, namespace string, source, target id.ID) (Value, error) {
	if err := checkNamespace(namespace); err != nil {
		return Value{}, err
	}
	if source == target {
		return Value{}, apperr.Validation("target_id")
	}
	var out Value
	err := s.fw.Tayfa(ctx, user, tayfa, true, func(ts *component.TayfaScope) error {
		if err := ts.Authorize(authz.TayfaVocabularyMerge); err != nil {
			return err
		}
		// Locked in ID order, so that two merges of the same pair cannot
		// deadlock.
		first, second := source, target
		if second.String() < first.String() {
			first, second = second, first
		}
		got := map[id.ID]locked{}
		for _, v := range []id.ID{first, second} {
			l, err := lock(ctx, ts, namespace, v)
			if err != nil {
				return err
			}
			got[v] = l
		}
		if got[source].merged || got[target].merged {
			return apperr.ErrVocabularyMerged
		}
		ys, err := years(ctx, ts, namespace, source)
		if err != nil {
			return err
		}
		if err := ts.Q().MoveVocabularyMerges(ctx, store.MoveVocabularyMergesParams{
			TargetID: target.PG(), TayfaID: tayfa.PG(), Namespace: namespace, SourceID: source.PG(),
		}); err != nil {
			return fmt.Errorf("birleştirilmiş değerler: %w", err)
		}
		if _, err := ts.Q().SetVocabularyMergedInto(ctx, store.SetVocabularyMergedIntoParams{
			MergedInto: target.PG(), TayfaID: tayfa.PG(), Namespace: namespace, ID: source.PG(),
		}); err != nil {
			return fmt.Errorf("birleştirme: %w", err)
		}
		subject := txn.Target{Type: targetType, ID: source}
		if err := ts.Audit(ctx, AuditMerged, subject, map[string]any{
			"namespace": namespace, "target_id": target.String(),
		}); err != nil {
			return err
		}
		if err := ts.Event(ctx, TypeMerged, subject, map[string]any{
			"tayfa_id": tayfa.String(), "namespace": namespace, "vocabulary_id": source.String(),
			"target_id": target.String(), "years": ys,
		}); err != nil {
			return err
		}
		out = Value{ID: source, Value: got[source].value, MergedInto: target, MergedIntoValue: got[target].value}
		return nil
	})
	if err != nil {
		return Value{}, err
	}
	return out, nil
}

// Unmerge undoes the merge of value. Refused: an unknown value
// (apperr.ErrVocabularyNotFound), a value that is not merged
// (apperr.ErrVocabularyNotMerged) and the refusals of Merge.
func (s *Service) Unmerge(ctx context.Context, user, tayfa id.ID, namespace string, value id.ID) error {
	if err := checkNamespace(namespace); err != nil {
		return err
	}
	return s.fw.Tayfa(ctx, user, tayfa, true, func(ts *component.TayfaScope) error {
		if err := ts.Authorize(authz.TayfaVocabularyMerge); err != nil {
			return err
		}
		l, err := lock(ctx, ts, namespace, value)
		if err != nil {
			return err
		}
		if !l.merged {
			return apperr.ErrVocabularyNotMerged
		}
		ys, err := years(ctx, ts, namespace, value)
		if err != nil {
			return err
		}
		if _, err := ts.Q().SetVocabularyMergedInto(ctx, store.SetVocabularyMergedIntoParams{
			TayfaID: tayfa.PG(), Namespace: namespace, ID: value.PG(),
		}); err != nil {
			return fmt.Errorf("birleştirmeyi geri alma: %w", err)
		}
		subject := txn.Target{Type: targetType, ID: value}
		if err := ts.Audit(ctx, AuditUnmerged, subject, map[string]any{"namespace": namespace}); err != nil {
			return err
		}
		return ts.Event(ctx, TypeUnmerged, subject, map[string]any{
			"tayfa_id": tayfa.String(), "namespace": namespace, "vocabulary_id": value.String(), "years": ys,
		})
	})
}

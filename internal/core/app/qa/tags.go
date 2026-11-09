package qa

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ListTags returns all tags of the tayfa by name, with the number of
// visible questions carrying each.
func (s *Service) ListTags(ctx context.Context, user, tayfa id.ID) ([]Tag, error) {
	if err := s.read(ctx, user, tayfa); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTags(ctx, tayfa.PG())
	if err != nil {
		return nil, fmt.Errorf("etiketler: %w", err)
	}
	out := make([]Tag, 0, len(rows))
	for _, r := range rows {
		out = append(out, Tag{ID: id.ID(r.ID.Bytes), Name: r.Name, QuestionCount: r.QuestionCount})
	}
	return out, nil
}

// DeleteTag removes a tag from the tayfa and from every question.
// Moderator and above.
func (s *Service) DeleteTag(ctx context.Context, user, tayfa, tag id.ID) error {
	return s.write(ctx, user, tayfa, authz.TagDelete, func(w writer) error {
		n, err := w.q.DeleteTag(ctx, store.DeleteTagParams{TayfaID: tayfa.PG(), ID: tag.PG()})
		if err != nil {
			return fmt.Errorf("etiket silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrTagNotFound
		}
		return record(ctx, w.q, tayfa, user, txn.Target{Type: subjectTag, ID: tag}, EventTagDeleted,
			map[string]any{"tag_id": tag.String()}, "tag.deleted", nil)
	})
}

// requireTag checks that tag belongs to tayfa.
func requireTag(ctx context.Context, q *store.Queries, tayfa, tag id.ID) error {
	_, err := q.GetTag(ctx, store.GetTagParams{TayfaID: tayfa.PG(), ID: tag.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.ErrTagNotFound
	case err != nil:
		return fmt.Errorf("etiket: %w", err)
	}
	return nil
}

// ensureTags returns the IDs of names in tayfa, in order, creating the
// missing ones. The tayfa row is locked (write), so two questions cannot
// create the same tag or pass the limit together.
func ensureTags(ctx context.Context, q *store.Queries, tayfa id.ID, names []tagName) ([]id.ID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	norms := make([]string, 0, len(names))
	for _, n := range names {
		norms = append(norms, n.normalized)
	}
	rows, err := q.GetTagsByNormalized(ctx, store.GetTagsByNormalizedParams{TayfaID: tayfa.PG(), Names: norms})
	if err != nil {
		return nil, fmt.Errorf("etiketler: %w", err)
	}
	have := map[string]id.ID{}
	for _, r := range rows {
		have[r.NameNormalized] = id.ID(r.ID.Bytes)
	}
	missing := 0
	for _, n := range names {
		if _, ok := have[n.normalized]; !ok {
			missing++
		}
	}
	if missing > 0 {
		count, err := q.CountTags(ctx, tayfa.PG())
		if err != nil {
			return nil, fmt.Errorf("etiket sayısı: %w", err)
		}
		if int(count)+missing > MaxTayfaTags {
			return nil, apperr.ErrTagLimit
		}
	}
	out := make([]id.ID, 0, len(names))
	for _, n := range names {
		tid, ok := have[n.normalized]
		if !ok {
			tid = id.New()
			if err := q.CreateTag(ctx, store.CreateTagParams{ID: tid.PG(), TayfaID: tayfa.PG(), Name: n.name, NameNormalized: n.normalized}); err != nil {
				return nil, fmt.Errorf("etiket: %w", err)
			}
		}
		out = append(out, tid)
	}
	return out, nil
}

// setTags adds tags to a question that has none.
func setTags(ctx context.Context, q *store.Queries, tayfa, question id.ID, tags []id.ID) error {
	for _, t := range tags {
		if err := q.AddQuestionTag(ctx, store.AddQuestionTagParams{QuestionID: question.PG(), TagID: t.PG(), TayfaID: tayfa.PG()}); err != nil {
			return fmt.Errorf("soru etiketi: %w", err)
		}
	}
	return nil
}

// replaceTags makes names the question's tags; changed is false when they
// already were.
func replaceTags(ctx context.Context, q *store.Queries, tayfa, question id.ID, names []tagName) (changed bool, err error) {
	cur, err := q.ListQuestionTags(ctx, store.ListQuestionTagsParams{TayfaID: tayfa.PG(), QuestionIds: []pgtype.UUID{question.PG()}})
	if err != nil {
		return false, fmt.Errorf("soru etiketleri: %w", err)
	}
	ids, err := ensureTags(ctx, q, tayfa, names)
	if err != nil {
		return false, err
	}
	if len(cur) == len(ids) && !slices.ContainsFunc(cur, func(r store.ListQuestionTagsRow) bool {
		return !slices.Contains(ids, id.ID(r.ID.Bytes))
	}) {
		return false, nil
	}
	if err := q.ClearQuestionTags(ctx, store.ClearQuestionTagsParams{TayfaID: tayfa.PG(), QuestionID: question.PG()}); err != nil {
		return false, fmt.Errorf("soru etiketleri: %w", err)
	}
	return true, setTags(ctx, q, tayfa, question, ids)
}

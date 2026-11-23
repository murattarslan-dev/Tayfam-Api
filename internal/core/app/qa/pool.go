package qa

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The question pool (issue #271; ADR-0040 §2, Notion "Faz 3 — Katalog
// ayrıntıları ve şema ekleri (2026-11-18)" §E.3). Eğitim tayfas send each
// other the questions they are stuck on and get them solved:
//
//   - A pool question has no title and no free tags. Its subject is
//     required and its topic optional, both keys of Tayfam's study tag
//     list (catalog.CheckStudySelection); Tayfam provides only the list,
//     the content is the users'.
//   - A question and each answer are text and/or up to MaxPhotos photos of
//     the author's own (upload purpose media.PurposeQuestionPool,
//     ADR-0020); no files or PDFs.
//   - There is no "solved" mark: answers are ordered by helpful votes, as
//     in Soru-Cevap.
//   - The members are often 16–17 years old. Nothing here opens a new way
//     to reach someone: no direct message, no contact field, no link
//     preview. Reports go through the usual flow (questions and answers),
//     where a minor moves a report up the queue, with paid_tutoring among
//     the reasons.

// Pools (questions.pool).
const (
	PoolQA           = "qa"
	PoolQuestionPool = "question_pool"
)

// MaxPhotos is how many photos a pool question or answer has at most.
const MaxPhotos = 2

// Validation fields of the question pool.
const (
	FieldPool       = "pool"
	FieldSubjectKey = catalog.FieldSubjectKey
	FieldTopicKey   = catalog.FieldTopicKey
	FieldPhotoIDs   = "photo_upload_ids"
)

// moduleOf is the module a pool belongs to.
func moduleOf(pool string) string {
	if pool == PoolQuestionPool {
		return access.ModuleQuestionPool
	}
	return access.ModuleQA
}

// cleanPool reads a requested pool; empty is PoolQA.
func cleanPool(p string) (string, error) {
	switch p {
	case "", PoolQA:
		return PoolQA, nil
	case PoolQuestionPool:
		return PoolQuestionPool, nil
	}
	return "", apperr.Validation(FieldPool)
}

// poolOf finds the pool a method works in, inside its transaction (or with
// the service's queries for reads). An unknown question or answer is
// not found in any pool.
type poolOf func(ctx context.Context, q *store.Queries) (string, error)

// inPool is a method that names its pool.
func inPool(pool string) poolOf {
	return func(context.Context, *store.Queries) (string, error) { return pool, nil }
}

// questionPool is the pool of a question of tayfa, deleted or not.
func questionPool(tayfa, question id.ID) poolOf {
	return func(ctx context.Context, q *store.Queries) (string, error) {
		pool, err := q.GetQuestionPool(ctx, store.GetQuestionPoolParams{TayfaID: tayfa.PG(), ID: question.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return "", apperr.ErrQuestionNotFound
		case err != nil:
			return "", fmt.Errorf("sorunun havuzu: %w", err)
		}
		return pool, nil
	}
}

// answerPool is the pool of an answer of tayfa, deleted or not.
func answerPool(tayfa, answer id.ID) poolOf {
	return func(ctx context.Context, q *store.Queries) (string, error) {
		pool, err := q.GetAnswerPool(ctx, store.GetAnswerPoolParams{TayfaID: tayfa.PG(), ID: answer.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return "", apperr.ErrAnswerNotFound
		case err != nil:
			return "", fmt.Errorf("cevabın havuzu: %w", err)
		}
		return pool, nil
	}
}

// checkTerms is the one place pool text meets the abuse term check
// (ADR-0012). The term list and its check are not in core yet; when they
// come, the question and answer texts are checked here and nowhere else.
func checkTerms(...string) error { return nil }

// cleanPhotos parses photo upload IDs: at most MaxPhotos, UUIDs, no
// repeats; the order is kept.
func cleanPhotos(raw []string) ([]id.ID, error) {
	if len(raw) > MaxPhotos {
		return nil, apperr.Validation(FieldPhotoIDs)
	}
	out := make([]id.ID, 0, len(raw))
	for _, r := range raw {
		u, ok := id.Parse(r)
		if !ok {
			return nil, apperr.Validation(FieldPhotoIDs)
		}
		for _, x := range out {
			if x == u {
				return nil, apperr.Validation(FieldPhotoIDs)
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// cleanStudyKey checks the form of a subject or topic key in a filter;
// whether it exists does not matter there.
func cleanStudyKey(k, field string) error {
	if k != "" && !definition.ValidKey(k) {
		return apperr.Validation(field)
	}
	return nil
}

// attachPhotos attaches the user's uploads to a pool question or answer
// (target) before its row is written (the photo columns' triggers check it
// again). The photos the row already has (first, second) are kept as they
// are: a moderator may drop someone else's photo, but only attaches their
// own uploads.
func attachPhotos(ctx context.Context, q *store.Queries, user, tayfa id.ID, target media.Target, photos []id.ID, first, second pgtype.UUID, now time.Time) error {
	have := photosOf(first, second)
	for _, p := range photos {
		if slices.Contains(have, p) {
			continue
		}
		if err := media.Attach(ctx, q, user, p, media.PurposeQuestionPool, tayfa, target, now); err != nil {
			return err
		}
	}
	return nil
}

// photoColumns spreads photos over the two columns.
func photoColumns(photos []id.ID) (first, second pgtype.UUID) {
	if len(photos) > 0 {
		first = photos[0].PG()
	}
	if len(photos) > 1 {
		second = photos[1].PG()
	}
	return first, second
}

// photosOf reads the two columns back, in order.
func photosOf(first, second pgtype.UUID) []id.ID {
	out := []id.ID{}
	for _, u := range []pgtype.UUID{first, second} {
		if u.Valid {
			out = append(out, id.ID(u.Bytes))
		}
	}
	return out
}

// samePhotos reports whether photos are the photos in the two columns.
func samePhotos(photos []id.ID, first, second pgtype.UUID) bool {
	have := photosOf(first, second)
	if len(have) != len(photos) {
		return false
	}
	for i := range have {
		if have[i] != photos[i] {
			return false
		}
	}
	return true
}

// optText is a nullable text column: empty is NULL.
func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// poolFields are the fields of a question or answer that belong to one
// pool only.
type poolFields struct {
	title    string
	tags     []string
	subject  string
	topic    string
	photos   []id.ID
	hasTitle bool
}

// check refuses a field of the other pool with a validation error on that
// field.
func (f poolFields) check(pool string) error {
	if pool == PoolQuestionPool {
		switch {
		case f.hasTitle && f.title != "":
			return apperr.Validation(FieldTitle)
		case len(f.tags) > 0:
			return apperr.Validation(FieldTags)
		}
		return nil
	}
	switch {
	case f.subject != "":
		return apperr.Validation(FieldSubjectKey)
	case f.topic != "":
		return apperr.Validation(FieldTopicKey)
	case len(f.photos) > 0:
		return apperr.Validation(FieldPhotoIDs)
	}
	return nil
}

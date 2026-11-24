package qa

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// CreateAnswerInput is a new answer.
type CreateAnswerInput struct {
	Body string
	// Photos are the question pool's upload IDs (0–MaxPhotos).
	Photos []string
	// ClientRequestID is required: a UUID the client reuses on retry.
	ClientRequestID string
}

// cleanAnswer checks an answer's body and photos against its pool: in
// Soru-Cevap the text is required and there are no photos; in the
// question pool the text and/or a photo is.
func cleanAnswer(pool, rawBody string, rawPhotos []string) (string, []id.ID, error) {
	photos, err := cleanPhotos(rawPhotos)
	if err != nil {
		return "", nil, err
	}
	if err := (poolFields{photos: photos}).check(pool); err != nil {
		return "", nil, err
	}
	if pool == PoolQA {
		body, err := cleanBody(rawBody, FieldBody, 1, AnswerMax)
		return body, photos, err
	}
	body, err := cleanBody(rawBody, FieldBody, 0, AnswerMax)
	if err != nil {
		return "", nil, err
	}
	if body == "" && len(photos) == 0 {
		return "", nil, apperr.Validation(FieldBody)
	}
	if err := checkTerms(body); err != nil {
		return "", nil, err
	}
	return body, photos, nil
}

// CreateAnswer answers a visible question. A repeat with the same
// ClientRequestID by the same user returns the first answer and records
// nothing. The answer.created event names the question's author for the
// answer_received notification.
func (s *Service) CreateAnswer(ctx context.Context, user, tayfa, question id.ID, in CreateAnswerInput) (Answer, error) {
	// The pool is known inside the write; this is the widest check.
	if _, err := cleanBody(in.Body, FieldBody, 0, AnswerMax); err != nil {
		return Answer{}, err
	}
	if _, err := cleanPhotos(in.Photos); err != nil {
		return Answer{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Answer{}, apperr.Validation("client_request_id")
	}

	var v Answer
	err := s.write(ctx, user, tayfa, questionPool(tayfa, question), authz.AnswerCreate, func(w writer) error {
		q := w.q
		body, photos, err := cleanAnswer(w.pool, in.Body, in.Photos)
		if err != nil {
			return err
		}
		prev, err := q.GetAnswerByClientRequest(ctx, store.GetAnswerByClientRequestParams{AuthorID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if id.ID(prev.TayfaID.Bytes) != tayfa || id.ID(prev.QuestionID.Bytes) != question || prev.DeletedAt.Valid {
				return apperr.Validation("client_request_id")
			}
			v, err = getAnswer(ctx, q, tayfa, id.ID(prev.ID.Bytes), user)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		qrow, err := lockQuestion(ctx, q, tayfa, question)
		if err != nil {
			return err
		}
		aid := id.New()
		if err := attachPhotos(ctx, q, user, tayfa, media.Target{Type: media.TargetAnswer, ID: aid}, photos, pgtype.UUID{}, pgtype.UUID{}, w.now); err != nil {
			return err
		}
		p := store.CreateAnswerParams{
			ID: aid.PG(), QuestionID: question.PG(), TayfaID: tayfa.PG(), AuthorID: user.PG(), Body: body, ClientRequestID: crid.PG(),
		}
		p.Photo1UploadID, p.Photo2UploadID = photoColumns(photos)
		if err := q.CreateAnswer(ctx, p); err != nil {
			return fmt.Errorf("cevap: %w", err)
		}
		if v, err = getAnswer(ctx, q, tayfa, aid, user); err != nil {
			return err
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, w.now); err != nil {
			return err
		}
		if err := record(ctx, q, tayfa, user, txn.Target{Type: subjectAnswer, ID: aid}, EventAnswerCreated,
			map[string]any{"question_id": question.String(), "answer_id": aid.String(), "question_author_id": idString(qrow.AuthorID)},
			"", nil); err != nil {
			return err
		}
		// The question's author, if still an active member and not the one
		// answering (ADR-0009).
		to, err := notify.Only(ctx, q, tayfa, []id.ID{optional(qrow.AuthorID)}, user)
		if err != nil {
			return err
		}
		return notify.Write(ctx, q, notify.Request{
			Type: notify.TypeAnswerReceived, Tayfa: tayfa, Actor: user, TargetType: notify.TargetAnswer, Target: aid,
			Extra: map[string]string{notify.ExtraQuestionID: question.String()},
		}, to)
	})
	if err != nil {
		return Answer{}, err
	}
	return v, nil
}

// UpdateAnswer replaces an answer's body and, in the question pool, its
// photos (rawPhotos after the change; empty removes them): the author, or a
// moderator or admin above the author's current role. The same values
// record nothing.
func (s *Service) UpdateAnswer(ctx context.Context, user, tayfa, answer id.ID, rawBody string, rawPhotos []string) (Answer, error) {
	if _, err := cleanBody(rawBody, FieldBody, 0, AnswerMax); err != nil {
		return Answer{}, err
	}
	if _, err := cleanPhotos(rawPhotos); err != nil {
		return Answer{}, err
	}
	var v Answer
	err := s.write(ctx, user, tayfa, answerPool(tayfa, answer), 0, func(w writer) error {
		q := w.q
		body, photos, err := cleanAnswer(w.pool, rawBody, rawPhotos)
		if err != nil {
			return err
		}
		row, err := lockAnswer(ctx, q, tayfa, answer)
		if err != nil {
			return err
		}
		if err := w.authorizeOn(ctx, tayfa, user, row.AuthorID, authz.AnswerEditOwn, authz.AnswerEditAny); err != nil {
			return err
		}
		same := body == row.Body && samePhotos(photos, row.Photo1UploadID, row.Photo2UploadID)
		if !same {
			if err := attachPhotos(ctx, q, user, tayfa, media.Target{Type: media.TargetAnswer, ID: answer}, photos, row.Photo1UploadID, row.Photo2UploadID, w.now); err != nil {
				return err
			}
			p := store.UpdateAnswerBodyParams{TayfaID: tayfa.PG(), ID: answer.PG(), Body: body}
			p.Photo1UploadID, p.Photo2UploadID = photoColumns(photos)
			n, err := q.UpdateAnswerBody(ctx, p)
			if err != nil {
				return fmt.Errorf("cevap güncelleme: %w", err)
			}
			if n != 1 {
				return apperr.ErrAnswerNotFound
			}
		}
		if v, err = getAnswer(ctx, q, tayfa, answer, user); err != nil || same {
			return err
		}
		audit := ""
		if !isAuthor(row.AuthorID, user) {
			audit = "answer." + changeEdited
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectAnswer, ID: answer}, EventAnswerUpdated,
			map[string]any{"question_id": idString(row.QuestionID), "answer_id": answer.String(), "change": changeEdited},
			audit, map[string]any{"author_id": idString(row.AuthorID)})
	})
	if err != nil {
		return Answer{}, err
	}
	return v, nil
}

// DeleteAnswer removes an answer. The author's own deletion empties the
// body and removes its votes at once; it cannot be undone. Someone else's
// deletion hides it (votes kept); an admin can restore it within
// RestoreWindow.
func (s *Service) DeleteAnswer(ctx context.Context, user, tayfa, answer id.ID) error {
	return s.write(ctx, user, tayfa, answerPool(tayfa, answer), 0, func(w writer) error {
		q := w.q
		row, err := lockAnswer(ctx, q, tayfa, answer)
		if err != nil {
			return err
		}
		if err := w.authorizeOn(ctx, tayfa, user, row.AuthorID, authz.AnswerDeleteOwn, authz.AnswerDeleteAny); err != nil {
			return err
		}
		own := isAuthor(row.AuthorID, user)
		var n int64
		if own {
			if err := q.DeleteAnswerVotes(ctx, answer.PG()); err != nil {
				return fmt.Errorf("cevap oyları: %w", err)
			}
			n, err = q.EraseAnswer(ctx, store.EraseAnswerParams{TayfaID: tayfa.PG(), ID: answer.PG(), DeletedAt: ts(w.now), DeletedBy: user.PG()})
		} else {
			n, err = q.HideAnswer(ctx, store.HideAnswerParams{TayfaID: tayfa.PG(), ID: answer.PG(), DeletedAt: ts(w.now), DeletedBy: user.PG()})
		}
		if err != nil {
			return fmt.Errorf("cevap silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrAnswerNotFound
		}
		audit := ""
		if !own {
			audit = "answer.deleted"
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectAnswer, ID: answer}, EventAnswerDeleted,
			map[string]any{"question_id": idString(row.QuestionID), "answer_id": answer.String(), "restorable": !own},
			audit, map[string]any{"author_id": idString(row.AuthorID), "restorable": !own})
	})
}

// RestoreAnswer undoes a moderator's or admin's deletion within
// RestoreWindow, as RestoreQuestion. The question must be visible.
func (s *Service) RestoreAnswer(ctx context.Context, user, tayfa, answer id.ID) (Answer, error) {
	var v Answer
	err := s.write(ctx, user, tayfa, answerPool(tayfa, answer), authz.AnswerRestore, func(w writer) error {
		q := w.q
		row, err := q.GetAnswerForUpdate(ctx, store.GetAnswerForUpdateParams{TayfaID: tayfa.PG(), ID: answer.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrAnswerNotFound
		case err != nil:
			return fmt.Errorf("cevap: %w", err)
		case row.QuestionDeletedAt.Valid:
			return apperr.ErrAnswerNotFound
		}
		if !row.DeletedAt.Valid {
			v, err = getAnswer(ctx, q, tayfa, answer, user)
			return err
		}
		// Erased by its author: neither text nor photo.
		erased := row.Body == "" && !row.Photo1UploadID.Valid && !row.Photo2UploadID.Valid
		if erased || w.now.Sub(row.DeletedAt.Time) > RestoreWindow {
			return apperr.ErrAnswerNotFound
		}
		if err := mayChange(ctx, q, tayfa, w.m, row.AuthorID); err != nil {
			return err
		}
		n, err := q.RestoreAnswer(ctx, store.RestoreAnswerParams{TayfaID: tayfa.PG(), ID: answer.PG()})
		if err != nil {
			return fmt.Errorf("geri alma: %w", err)
		}
		if n != 1 {
			return apperr.ErrAnswerNotFound
		}
		if v, err = getAnswer(ctx, q, tayfa, answer, user); err != nil {
			return err
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectAnswer, ID: answer}, EventAnswerUpdated,
			map[string]any{"question_id": idString(row.QuestionID), "answer_id": answer.String(), "change": changeRestored},
			"answer."+changeRestored, map[string]any{"author_id": idString(row.AuthorID), "deleted_by": idString(row.DeletedBy)})
	})
	if err != nil {
		return Answer{}, err
	}
	return v, nil
}

// SetVote gives (voted) or takes back the caller's helpful vote on a
// visible answer: one per person, never on one's own answer. The vote and
// vote_count change in the same transaction; setting the current value
// changes nothing.
func (s *Service) SetVote(ctx context.Context, user, tayfa, answer id.ID, voted bool) (Answer, error) {
	var v Answer
	err := s.write(ctx, user, tayfa, answerPool(tayfa, answer), authz.AnswerVote, func(w writer) error {
		q := w.q
		row, err := lockAnswer(ctx, q, tayfa, answer)
		if err != nil {
			return err
		}
		if isAuthor(row.AuthorID, user) {
			return apperr.ErrOwnAnswerVote
		}
		var (
			n     int64
			delta int32
			kind  string
		)
		if voted {
			n, err = q.AddAnswerVote(ctx, store.AddAnswerVoteParams{AnswerID: answer.PG(), UserID: user.PG()})
			delta, kind = 1, changeAdded
		} else {
			n, err = q.RemoveAnswerVote(ctx, store.RemoveAnswerVoteParams{AnswerID: answer.PG(), UserID: user.PG()})
			delta, kind = -1, changeRemoved
		}
		if err != nil {
			return fmt.Errorf("oy: %w", err)
		}
		if n == 1 {
			m, err := q.AdjustAnswerVoteCount(ctx, store.AdjustAnswerVoteCountParams{TayfaID: tayfa.PG(), ID: answer.PG(), Delta: delta})
			if err != nil {
				return fmt.Errorf("oy sayısı: %w", err)
			}
			if m != 1 {
				return apperr.ErrAnswerNotFound
			}
		}
		if v, err = getAnswer(ctx, q, tayfa, answer, user); err != nil || n == 0 {
			return err
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectAnswer, ID: answer}, EventAnswerVoted,
			map[string]any{"question_id": idString(row.QuestionID), "answer_id": answer.String(), "change": kind},
			"", nil)
	})
	if err != nil {
		return Answer{}, err
	}
	return v, nil
}

// lockAnswer locks a visible answer of a visible question of tayfa.
func lockAnswer(ctx context.Context, q *store.Queries, tayfa, answer id.ID) (store.GetAnswerForUpdateRow, error) {
	row, err := q.GetAnswerForUpdate(ctx, store.GetAnswerForUpdateParams{TayfaID: tayfa.PG(), ID: answer.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return row, apperr.ErrAnswerNotFound
	case err != nil:
		return row, fmt.Errorf("cevap: %w", err)
	case row.DeletedAt.Valid, row.QuestionDeletedAt.Valid:
		return row, apperr.ErrAnswerNotFound
	}
	return row, nil
}

func getAnswer(ctx context.Context, q *store.Queries, tayfa, answer, user id.ID) (Answer, error) {
	r, err := q.GetAnswer(ctx, store.GetAnswerParams{TayfaID: tayfa.PG(), ID: answer.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Answer{}, apperr.ErrAnswerNotFound
	case err != nil:
		return Answer{}, fmt.Errorf("cevap: %w", err)
	}
	return answerView(store.ListAnswersPageRow(r)), nil
}

func answerView(r store.ListAnswersPageRow) Answer {
	return Answer{
		ID: id.ID(r.ID.Bytes), QuestionID: id.ID(r.QuestionID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes),
		AuthorID: optional(r.AuthorID), Body: r.Body, VoteCount: r.VoteCount, Voted: r.Voted,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
		Pool: r.Pool, Photos: photosOf(r.Photo1UploadID, r.Photo2UploadID),
	}
}

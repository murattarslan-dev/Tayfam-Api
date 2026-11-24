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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// CreateQuestionInput is a new question.
type CreateQuestionInput struct {
	// Pool is PoolQA (also when empty) or PoolQuestionPool.
	Pool  string
	Title string
	Body  string
	// Tags are names; existing tags are matched with Turkish case folding,
	// missing ones are created.
	Tags []string
	// SubjectKey (required) and TopicKey (optional) are the question
	// pool's study tags.
	SubjectKey string
	TopicKey   string
	// Photos are the question pool's upload IDs (0–MaxPhotos).
	Photos []string
	// ClientRequestID is required: a UUID the client reuses on retry.
	ClientRequestID string
}

// CreateQuestion asks a question by user. A repeat with the same
// ClientRequestID by the same user returns the first question and records
// nothing. In the question pool the subject must be selectable and the
// text and/or a photo is required.
func (s *Service) CreateQuestion(ctx context.Context, user, tayfa id.ID, in CreateQuestionInput) (Question, error) {
	pool, err := cleanPool(in.Pool)
	if err != nil {
		return Question{}, err
	}
	photos, err := cleanPhotos(in.Photos)
	if err != nil {
		return Question{}, err
	}
	if err := (poolFields{title: in.Title, hasTitle: true, tags: in.Tags, subject: in.SubjectKey, topic: in.TopicKey, photos: photos}).check(pool); err != nil {
		return Question{}, err
	}
	var title string
	if pool == PoolQA {
		if title, err = cleanTitle(in.Title); err != nil {
			return Question{}, err
		}
	}
	body, err := cleanBody(in.Body, FieldBody, 0, BodyMax)
	if err != nil {
		return Question{}, err
	}
	if pool == PoolQuestionPool {
		if body == "" && len(photos) == 0 {
			return Question{}, apperr.Validation(FieldBody)
		}
		if err := checkTerms(body); err != nil {
			return Question{}, err
		}
	}
	tags, err := cleanTags(in.Tags)
	if err != nil {
		return Question{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Question{}, apperr.Validation("client_request_id")
	}

	var v Question
	err = s.write(ctx, user, tayfa, inPool(pool), authz.QuestionCreate, func(w writer) error {
		q := w.q
		prev, err := q.GetQuestionByClientRequest(ctx, store.GetQuestionByClientRequestParams{AuthorID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			// The same request again: only valid for the same, still
			// visible question in the same tayfa.
			if id.ID(prev.TayfaID.Bytes) != tayfa || prev.DeletedAt.Valid {
				return apperr.Validation("client_request_id")
			}
			v, err = getQuestion(ctx, q, tayfa, id.ID(prev.ID.Bytes))
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}

		if pool == PoolQuestionPool {
			if err := catalog.CheckStudySelection(ctx, q, in.SubjectKey, in.TopicKey); err != nil {
				return err
			}
		}
		tagIDs, err := ensureTags(ctx, q, tayfa, tags)
		if err != nil {
			return err
		}
		qid := id.New()
		if err := attachPhotos(ctx, q, user, tayfa, media.Target{Type: media.TargetQuestion, ID: qid}, photos, pgtype.UUID{}, pgtype.UUID{}, w.now); err != nil {
			return err
		}
		p := store.CreateQuestionParams{
			ID: qid.PG(), TayfaID: tayfa.PG(), AuthorID: user.PG(), Title: title, Body: body, ClientRequestID: crid.PG(),
		}
		if pool == PoolQuestionPool {
			p.Pool, p.SubjectKey, p.TopicKey = optText(pool), optText(in.SubjectKey), optText(in.TopicKey)
			p.Photo1UploadID, p.Photo2UploadID = photoColumns(photos)
		}
		if err := q.CreateQuestion(ctx, p); err != nil {
			return fmt.Errorf("soru: %w", err)
		}
		if err := setTags(ctx, q, tayfa, qid, tagIDs); err != nil {
			return err
		}
		if v, err = getQuestion(ctx, q, tayfa, qid); err != nil {
			return err
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, w.now); err != nil {
			return err
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectQuestion, ID: qid}, EventQuestionCreated,
			map[string]any{"question_id": qid.String()}, "", nil)
	})
	if err != nil {
		return Question{}, err
	}
	return v, nil
}

// GetQuestion returns one visible question of either pool.
func (s *Service) GetQuestion(ctx context.Context, user, tayfa, question id.ID) (Question, error) {
	if err := s.read(ctx, user, tayfa, questionPool(tayfa, question)); err != nil {
		return Question{}, err
	}
	return getQuestion(ctx, s.q, tayfa, question)
}

// UpdateQuestionInput is an edit. Only the fields named in Fields change;
// FieldTags replaces the tags. FieldSubject (SubjectKey and TopicKey) and
// FieldPhotos (Photos replaces the photos) are the question pool's;
// FieldTitle and FieldTags are Soru-Cevap's.
type UpdateQuestionInput struct {
	Fields     []string
	Title      string
	Body       string
	Tags       []string
	SubjectKey string
	TopicKey   string
	Photos     []string
}

// UpdateQuestion edits a question: the author, or a moderator or admin
// above the author's current role. Setting the current values changes
// nothing and records nothing.
func (s *Service) UpdateQuestion(ctx context.Context, user, tayfa, question id.ID, in UpdateQuestionInput) (Question, error) {
	if len(in.Fields) == 0 {
		return Question{}, apperr.Validation("update_mask")
	}
	set := map[string]bool{}
	for _, f := range in.Fields {
		if !slices.Contains([]string{FieldTitle, FieldBody, FieldTags, FieldSubject, FieldPhotos}, f) {
			return Question{}, apperr.Validation("update_mask")
		}
		set[f] = true
	}
	var (
		title, body string
		tags        []tagName
		photos      []id.ID
		err         error
	)
	if set[FieldPhotos] {
		if photos, err = cleanPhotos(in.Photos); err != nil {
			return Question{}, err
		}
	}
	if set[FieldSubject] {
		if in.SubjectKey == "" {
			return Question{}, apperr.Validation(FieldSubjectKey)
		}
	}
	if set[FieldTitle] {
		if title, err = cleanTitle(in.Title); err != nil {
			return Question{}, err
		}
	}
	if set[FieldBody] {
		if body, err = cleanBody(in.Body, FieldBody, 0, BodyMax); err != nil {
			return Question{}, err
		}
	}
	if set[FieldTags] {
		if tags, err = cleanTags(in.Tags); err != nil {
			return Question{}, err
		}
	}

	var v Question
	err = s.write(ctx, user, tayfa, questionPool(tayfa, question), 0, func(w writer) error {
		q := w.q
		row, err := lockQuestion(ctx, q, tayfa, question)
		if err != nil {
			return err
		}
		// The paths of the other pool.
		if (row.Pool == PoolQA && (set[FieldSubject] || set[FieldPhotos])) ||
			(row.Pool == PoolQuestionPool && (set[FieldTitle] || set[FieldTags])) {
			return apperr.Validation("update_mask")
		}
		if err := w.authorizeOn(ctx, tayfa, user, row.AuthorID, authz.QuestionEditOwn, authz.QuestionEditAny); err != nil {
			return err
		}
		if !set[FieldTitle] {
			title = row.Title
		}
		if !set[FieldBody] {
			body = row.Body
		}
		subject, topic := text(row.SubjectKey), text(row.TopicKey)
		if set[FieldSubject] {
			subject, topic = in.SubjectKey, in.TopicKey
		}
		if !set[FieldPhotos] {
			photos = photosOf(row.Photo1UploadID, row.Photo2UploadID)
		}
		var changed []string
		if title != row.Title {
			changed = append(changed, FieldTitle)
		}
		if body != row.Body {
			changed = append(changed, FieldBody)
		}
		if subject != text(row.SubjectKey) || topic != text(row.TopicKey) {
			// Only a new choice must be selectable: a retired subject or
			// topic the question already has stays.
			if err := catalog.CheckStudySelection(ctx, q, subject, topic); err != nil {
				return err
			}
			changed = append(changed, FieldSubject)
		}
		if !samePhotos(photos, row.Photo1UploadID, row.Photo2UploadID) {
			if err := attachPhotos(ctx, q, user, tayfa, media.Target{Type: media.TargetQuestion, ID: question}, photos, row.Photo1UploadID, row.Photo2UploadID, w.now); err != nil {
				return err
			}
			changed = append(changed, FieldPhotos)
		}
		if row.Pool == PoolQuestionPool {
			if body == "" && len(photos) == 0 {
				return apperr.Validation(FieldBody)
			}
			if err := checkTerms(body); err != nil {
				return err
			}
		}
		if len(changed) > 0 {
			p := store.UpdateQuestionContentParams{
				TayfaID: tayfa.PG(), ID: question.PG(), Title: title, Body: body,
				SubjectKey: optText(subject), TopicKey: optText(topic),
			}
			p.Photo1UploadID, p.Photo2UploadID = photoColumns(photos)
			n, err := q.UpdateQuestionContent(ctx, p)
			if err != nil {
				return fmt.Errorf("soru güncelleme: %w", err)
			}
			if n != 1 {
				return apperr.ErrQuestionNotFound
			}
		}
		if set[FieldTags] {
			ok, err := replaceTags(ctx, q, tayfa, question, tags)
			if err != nil {
				return err
			}
			if ok {
				changed = append(changed, FieldTags)
			}
		}
		if v, err = getQuestion(ctx, q, tayfa, question); err != nil || len(changed) == 0 {
			return err
		}
		own := isAuthor(row.AuthorID, user)
		audit := ""
		if !own {
			audit = "question." + changeEdited
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectQuestion, ID: question}, EventQuestionUpdated,
			map[string]any{"question_id": question.String(), "change": changeEdited, "fields": changed},
			audit, map[string]any{"fields": changed, "author_id": idString(row.AuthorID)})
	})
	if err != nil {
		return Question{}, err
	}
	return v, nil
}

// DeleteQuestion removes a question; its answers are no longer visible.
// The author's own deletion empties the title and the body and drops the
// tags at once; it cannot be undone. Someone else's deletion (moderator and
// above, hierarchy as in UpdateQuestion) hides it; an admin can restore it
// within RestoreWindow.
func (s *Service) DeleteQuestion(ctx context.Context, user, tayfa, question id.ID) error {
	return s.write(ctx, user, tayfa, questionPool(tayfa, question), 0, func(w writer) error {
		q := w.q
		row, err := lockQuestion(ctx, q, tayfa, question)
		if err != nil {
			return err
		}
		if err := w.authorizeOn(ctx, tayfa, user, row.AuthorID, authz.QuestionDeleteOwn, authz.QuestionDeleteAny); err != nil {
			return err
		}
		own := isAuthor(row.AuthorID, user)
		var n int64
		if own {
			n, err = q.EraseQuestion(ctx, store.EraseQuestionParams{TayfaID: tayfa.PG(), ID: question.PG(), DeletedAt: ts(w.now), DeletedBy: user.PG()})
			if err == nil {
				err = q.ClearQuestionTags(ctx, store.ClearQuestionTagsParams{TayfaID: tayfa.PG(), QuestionID: question.PG()})
			}
		} else {
			n, err = q.HideQuestion(ctx, store.HideQuestionParams{TayfaID: tayfa.PG(), ID: question.PG(), DeletedAt: ts(w.now), DeletedBy: user.PG()})
		}
		if err != nil {
			return fmt.Errorf("soru silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrQuestionNotFound
		}
		audit := ""
		if !own {
			audit = "question.deleted"
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectQuestion, ID: question}, EventQuestionDeleted,
			map[string]any{"question_id": question.String(), "restorable": !own},
			audit, map[string]any{"author_id": idString(row.AuthorID), "restorable": !own})
	})
}

// RestoreQuestion undoes a moderator's or admin's deletion within
// RestoreWindow. Admin only, above the author's current role. Restoring a
// visible question changes nothing.
func (s *Service) RestoreQuestion(ctx context.Context, user, tayfa, question id.ID) (Question, error) {
	var v Question
	err := s.write(ctx, user, tayfa, questionPool(tayfa, question), authz.QuestionRestore, func(w writer) error {
		q := w.q
		row, err := q.GetQuestionForUpdate(ctx, store.GetQuestionForUpdateParams{TayfaID: tayfa.PG(), ID: question.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrQuestionNotFound
		case err != nil:
			return fmt.Errorf("soru: %w", err)
		}
		if !row.DeletedAt.Valid {
			v, err = getQuestion(ctx, q, tayfa, question)
			return err
		}
		// Erased by its author (no title in Soru-Cevap; neither text nor
		// photo in the question pool), or past the window: the same answer
		// as for an unknown question.
		erased := row.Title == ""
		if row.Pool == PoolQuestionPool {
			erased = row.Body == "" && !row.Photo1UploadID.Valid && !row.Photo2UploadID.Valid
		}
		if erased || w.now.Sub(row.DeletedAt.Time) > RestoreWindow {
			return apperr.ErrQuestionNotFound
		}
		if err := mayChange(ctx, q, tayfa, w.m, row.AuthorID); err != nil {
			return err
		}
		n, err := q.RestoreQuestion(ctx, store.RestoreQuestionParams{TayfaID: tayfa.PG(), ID: question.PG()})
		if err != nil {
			return fmt.Errorf("geri alma: %w", err)
		}
		if n != 1 {
			return apperr.ErrQuestionNotFound
		}
		if v, err = getQuestion(ctx, q, tayfa, question); err != nil {
			return err
		}
		return record(ctx, q, tayfa, user, txn.Target{Type: subjectQuestion, ID: question}, EventQuestionUpdated,
			map[string]any{"question_id": question.String(), "change": changeRestored},
			"question."+changeRestored, map[string]any{"author_id": idString(row.AuthorID), "deleted_by": idString(row.DeletedBy)})
	})
	if err != nil {
		return Question{}, err
	}
	return v, nil
}

// lockQuestion locks a visible question of tayfa.
func lockQuestion(ctx context.Context, q *store.Queries, tayfa, question id.ID) (store.GetQuestionForUpdateRow, error) {
	row, err := q.GetQuestionForUpdate(ctx, store.GetQuestionForUpdateParams{TayfaID: tayfa.PG(), ID: question.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return row, apperr.ErrQuestionNotFound
	case err != nil:
		return row, fmt.Errorf("soru: %w", err)
	case row.DeletedAt.Valid:
		return row, apperr.ErrQuestionNotFound
	}
	return row, nil
}

func getQuestion(ctx context.Context, q *store.Queries, tayfa, question id.ID) (Question, error) {
	r, err := q.GetQuestion(ctx, store.GetQuestionParams{TayfaID: tayfa.PG(), ID: question.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Question{}, apperr.ErrQuestionNotFound
	case err != nil:
		return Question{}, fmt.Errorf("soru: %w", err)
	}
	out := []Question{questionView(store.ListQuestionsPageRow(r))}
	if err := attachTags(ctx, q, tayfa, out); err != nil {
		return Question{}, err
	}
	return out[0], nil
}

func questionView(r store.ListQuestionsPageRow) Question {
	return Question{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), AuthorID: optional(r.AuthorID),
		Title: r.Title, Body: r.Body, Tags: []Tag{}, AnswerCount: r.AnswerCount,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
		Pool: r.Pool, SubjectKey: text(r.SubjectKey), TopicKey: text(r.TopicKey),
		Photos: photosOf(r.Photo1UploadID, r.Photo2UploadID),
	}
}

// attachTags fills the tags of questions of tayfa with one query.
func attachTags(ctx context.Context, q *store.Queries, tayfa id.ID, qs []Question) error {
	if len(qs) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(qs))
	for _, x := range qs {
		ids = append(ids, x.ID.PG())
	}
	rows, err := q.ListQuestionTags(ctx, store.ListQuestionTagsParams{TayfaID: tayfa.PG(), QuestionIds: ids})
	if err != nil {
		return fmt.Errorf("soru etiketleri: %w", err)
	}
	for _, r := range rows {
		i := slices.IndexFunc(qs, func(x Question) bool { return x.ID == id.ID(r.QuestionID.Bytes) })
		if i >= 0 {
			qs[i].Tags = append(qs[i].Tags, Tag{ID: id.ID(r.ID.Bytes), Name: r.Name})
		}
	}
	return nil
}

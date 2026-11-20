package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// The study tag list (issue #259; ADR-0040 §5): the levels, subjects and
// topics the Eğitim modules label their content with (material library
// #261, question pool #271). Tayfam provides only the list; the content
// is the users'.
//
// The list is catalog data in catalog_study_tags: level (exam or grade) →
// subject → topic, at most three deep. A key never changes and is unique
// in the whole list, so content stores the key alone; a removed tag is
// retired, never deleted. Every level and subject has a child "Diğer"
// whose key is its parent's key with OtherSuffix.

// Depths of a study tag.
const (
	StudyLevel   = 1
	StudySubject = 2
	StudyTopic   = 3
)

// OtherSuffix ends the key of the "Diğer" child of a level or a subject.
const OtherSuffix = "_other"

// Validation fields of CheckStudySelection. Content that is labelled with
// a study tag names its fields so.
const (
	FieldSubjectKey = "subject_key"
	FieldTopicKey   = "topic_key"
)

// StudyTag is one entry of the study tag list.
type StudyTag struct {
	Key string
	// ParentKey is empty for a level.
	ParentKey string
	// Position orders the tags under the same parent.
	Position int32
	Labels   map[string]string
	Status   string
}

// studyTags reads the list, retired tags included, and checks its shape
// (ADR-0011): a row that does not pass fails the request.
func studyTags(ctx context.Context, q *store.Queries) ([]StudyTag, error) {
	rows, err := q.ListCatalogStudyTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("ders ve konu etiketleri: %w", err)
	}
	tags := make([]StudyTag, 0, len(rows))
	for _, r := range rows {
		t := StudyTag{Key: r.Key, ParentKey: r.ParentKey, Position: r.Position, Status: r.Status}
		if t.ParentKey != "" && !definition.ValidKey(t.ParentKey) {
			return nil, fmt.Errorf("etiket %q: geçersiz üst etiket", r.Key)
		}
		// The key stands in for the icon, which tags do not have.
		if t.Labels, err = entry(r.Key, r.Key, r.Status, r.Labels); err != nil {
			return nil, fmt.Errorf("etiket %q: %w", r.Key, err)
		}
		tags = append(tags, t)
	}
	if _, err := studyDepths(tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// studyDepths returns the depth of every tag; a tag whose parent is not in
// the list, or that is more than three deep, is an error.
func studyDepths(tags []StudyTag) (map[string]int, error) {
	parent := make(map[string]string, len(tags))
	for _, t := range tags {
		if _, dup := parent[t.Key]; dup {
			return nil, fmt.Errorf("etiket %q: tekrar ediyor", t.Key)
		}
		parent[t.Key] = t.ParentKey
	}
	depths := make(map[string]int, len(tags))
	for _, t := range tags {
		d, p := 1, t.ParentKey
		for p != "" {
			next, ok := parent[p]
			if !ok {
				return nil, fmt.Errorf("etiket %q: üst etiket yok", t.Key)
			}
			if d++; d > StudyTopic {
				return nil, fmt.Errorf("etiket %q: üç seviyeden derin", t.Key)
			}
			p = next
		}
		depths[t.Key] = d
	}
	return depths, nil
}

// CheckStudyTags checks the content rules of a whole list (ADR-0040 §5)
// beyond its shape: every active level and subject has an active "Diğer"
// child (key + OtherSuffix), a "Diğer" tag sits under the tag its key
// names and has no children. The seed (#260) and the platform panel
// (#262) check a list with it before it is published; the catalog does not
// refuse to serve a list that misses one.
func CheckStudyTags(tags []StudyTag) error {
	depths, err := studyDepths(tags)
	if err != nil {
		return err
	}
	byKey := make(map[string]StudyTag, len(tags))
	hasChildren := make(map[string]bool, len(tags))
	for _, t := range tags {
		byKey[t.Key] = t
		if t.ParentKey != "" {
			hasChildren[t.ParentKey] = true
		}
	}
	for _, t := range tags {
		if strings.HasSuffix(t.Key, OtherSuffix) {
			if t.Key != t.ParentKey+OtherSuffix {
				return fmt.Errorf("etiket %q: Diğer etiketi üst etiketinin altında değil", t.Key)
			}
			if hasChildren[t.Key] {
				return fmt.Errorf("etiket %q: Diğer etiketinin alt etiketi var", t.Key)
			}
			continue
		}
		if t.Status != StatusActive || depths[t.Key] == StudyTopic {
			continue
		}
		other, ok := byKey[t.Key+OtherSuffix]
		if !ok || other.ParentKey != t.Key || other.Status != StatusActive {
			return fmt.Errorf("etiket %q: aktif Diğer etiketi yok", t.Key)
		}
	}
	return nil
}

// CheckStudySelection checks the study tags chosen for new content inside
// the caller's transaction: the subject is required and must be a subject
// (second level), the topic is optional and must be a topic under that
// subject. A retired tag, or one under a retired tag, cannot be chosen.
// Errors are apperr.Validation(FieldSubjectKey) or
// apperr.Validation(FieldTopicKey).
//
// Content that already carries a tag keeps it when the tag is retired: an
// edit that leaves the tags as they were does not call this.
func CheckStudySelection(ctx context.Context, q *store.Queries, subject, topic string) error {
	s, err := studyTag(ctx, q, subject, FieldSubjectKey)
	if err != nil {
		return err
	}
	if s.Depth != StudySubject || !s.Selectable {
		return apperr.Validation(FieldSubjectKey)
	}
	if topic == "" {
		return nil
	}
	t, err := studyTag(ctx, q, topic, FieldTopicKey)
	if err != nil {
		return err
	}
	if t.Depth != StudyTopic || t.ParentKey != subject || !t.Selectable {
		return apperr.Validation(FieldTopicKey)
	}
	return nil
}

func studyTag(ctx context.Context, q *store.Queries, key, field string) (store.GetCatalogStudyTagRow, error) {
	if !definition.ValidKey(key) {
		return store.GetCatalogStudyTagRow{}, apperr.Validation(field)
	}
	r, err := q.GetCatalogStudyTag(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetCatalogStudyTagRow{}, apperr.Validation(field)
	}
	if err != nil {
		return store.GetCatalogStudyTagRow{}, fmt.Errorf("ders/konu etiketi: %w", err)
	}
	return r, nil
}

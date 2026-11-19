package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/vocabulary"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeVocabulary struct {
	err              error
	method           string
	user, tayfa      id.ID
	namespace, query string
	merged           bool
	source, target   id.ID
	list             vocabulary.List
	value            vocabulary.Value
	calls            int
}

func (f *fakeVocabulary) rec(method string, user, tayfa id.ID, namespace string) {
	f.method, f.user, f.tayfa, f.namespace, f.calls = method, user, tayfa, namespace, f.calls+1
}

func (f *fakeVocabulary) List(_ context.Context, user, tayfa id.ID, namespace, query string, merged bool) (vocabulary.List, error) {
	f.rec("List", user, tayfa, namespace)
	f.query, f.merged = query, merged
	return f.list, f.err
}

func (f *fakeVocabulary) Merge(_ context.Context, user, tayfa id.ID, namespace string, source, target id.ID) (vocabulary.Value, error) {
	f.rec("Merge", user, tayfa, namespace)
	f.source, f.target = source, target
	return f.value, f.err
}

func (f *fakeVocabulary) Unmerge(_ context.Context, user, tayfa id.ID, namespace string, value id.ID) error {
	f.rec("Unmerge", user, tayfa, namespace)
	f.source = value
	return f.err
}

func TestTayfaVocabularyMethods(t *testing.T) {
	a, b := id.New(), id.New()
	f := &fakeVocabulary{
		list:  vocabulary.List{Values: []vocabulary.Value{{ID: a, Value: "Levrek"}, {ID: b, Value: "lavrak", MergedInto: a, MergedIntoValue: "Levrek"}}, CallerCanMerge: true},
		value: vocabulary.Value{ID: b, Value: "lavrak", MergedInto: a, MergedIntoValue: "Levrek"},
	}
	s := NewTayfaVocabularyServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	list, err := s.ListVocabulary(ctx, &corev1.ListVocabularyRequest{TayfaId: tf.String(), Namespace: "fish_species", Query: "lev", Merged: true})
	if err != nil || f.method != "List" || f.user != user || f.tayfa != tf || f.namespace != "fish_species" || f.query != "lev" || !f.merged {
		t.Fatalf("liste: %v %+v", err, f)
	}
	if len(list.GetValues()) != 2 || !list.GetCallerCanMerge() {
		t.Fatalf("liste yanıtı: %v", list)
	}
	if v := list.GetValues()[0]; v.GetId() != a.String() || v.GetValue() != "Levrek" || v.GetMergedIntoId() != "" || v.GetMergedIntoValue() != "" {
		t.Fatalf("değer: %v", v)
	}
	if v := list.GetValues()[1]; v.GetMergedIntoId() != a.String() || v.GetMergedIntoValue() != "Levrek" {
		t.Fatalf("birleştirilmiş değer: %v", v)
	}

	merged, err := s.MergeVocabulary(ctx, &corev1.MergeVocabularyRequest{
		TayfaId: tf.String(), Namespace: "fish_species", VocabularyId: b.String(), TargetId: a.String(),
	})
	if err != nil || f.method != "Merge" || f.source != b || f.target != a || merged.GetValue().GetMergedIntoId() != a.String() {
		t.Fatalf("birleştirme: %v %v", merged, err)
	}
	if _, err := s.UnmergeVocabulary(ctx, &corev1.UnmergeVocabularyRequest{
		TayfaId: tf.String(), Namespace: "fish_species", VocabularyId: b.String(),
	}); err != nil || f.method != "Unmerge" || f.source != b {
		t.Fatalf("geri alma: %v", err)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrVocabularyNotFound:  {codes.NotFound, corev1.ErrorReason_ERROR_REASON_VOCABULARY_NOT_FOUND},
		apperr.ErrVocabularyMerged:    {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOCABULARY_MERGED},
		apperr.ErrVocabularyNotMerged: {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_VOCABULARY_NOT_MERGED},
		apperr.ErrPermissionDenied:    {codes.PermissionDenied, corev1.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
		apperr.ErrTayfaReadOnly:       {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_TAYFA_READ_ONLY},
	} {
		f.err = appErr
		_, err := s.MergeVocabulary(ctx, &corev1.MergeVocabularyRequest{
			TayfaId: tf.String(), Namespace: "fish_species", VocabularyId: b.String(), TargetId: a.String(),
		})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestTayfaVocabularyValidation(t *testing.T) {
	f := &fakeVocabulary{}
	s := NewTayfaVocabularyServer(f, discard)
	ctx := callerCtx(id.New())
	tf, good := id.New().String(), id.New().String()
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	_, err := s.ListVocabulary(ctx, &corev1.ListVocabularyRequest{TayfaId: "x", Namespace: "fish_species"})
	if field(err) != "tayfa_id" {
		t.Error("bozuk tayfa")
	}
	_, err = s.MergeVocabulary(ctx, &corev1.MergeVocabularyRequest{TayfaId: tf, Namespace: "fish_species", VocabularyId: "x", TargetId: good})
	if field(err) != "vocabulary_id" {
		t.Error("bozuk değer")
	}
	_, err = s.MergeVocabulary(ctx, &corev1.MergeVocabularyRequest{TayfaId: tf, Namespace: "fish_species", VocabularyId: good, TargetId: "x"})
	if field(err) != "target_id" {
		t.Error("bozuk hedef")
	}
	_, err = s.UnmergeVocabulary(ctx, &corev1.UnmergeVocabularyRequest{TayfaId: tf, Namespace: "fish_species", VocabularyId: ""})
	if field(err) != "vocabulary_id" {
		t.Error("değersiz geri alma")
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.ListVocabulary(context.Background(), &corev1.ListVocabularyRequest{TayfaId: tf, Namespace: "fish_species"}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the tayfa vocabulary.
func TestTayfaVocabularyRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.TayfaVocabularyService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("sözlük servisi: %v", info)
	}
}

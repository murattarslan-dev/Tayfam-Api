package grpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/logbook"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeLogbook struct {
	err    error
	method string
	ref    component.Ref
	in     logbook.Input
	entry  id.ID
	size   int32
	token  string
	page   logbook.Page
	calls  int
}

func (f *fakeLogbook) rec(method string, ref component.Ref) {
	f.method, f.ref, f.calls = method, ref, f.calls+1
}

func (f *fakeLogbook) List(_ context.Context, ref component.Ref, size int32, token string) (logbook.Page, error) {
	f.rec("List", ref)
	f.size, f.token = size, token
	return f.page, f.err
}

func (f *fakeLogbook) Add(_ context.Context, ref component.Ref, in logbook.Input) (logbook.Entry, error) {
	f.rec("Add", ref)
	f.in = in
	if len(f.page.Entries) == 0 {
		return logbook.Entry{}, f.err
	}
	return f.page.Entries[0], f.err
}

func (f *fakeLogbook) Remove(_ context.Context, ref component.Ref, entry id.ID) error {
	f.rec("Remove", ref)
	f.entry = entry
	return f.err
}

func TestLogComponentMethods(t *testing.T) {
	author, mine, theirs, photo := id.New(), id.New(), id.New(), id.New()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeLogbook{page: logbook.Page{
		Entries: []logbook.Entry{
			{ID: mine, UserID: author, Values: map[string]json.RawMessage{"species": json.RawMessage(`"Levrek"`), "weight": json.RawMessage(`1.25`)},
				Location: &logbook.Point{Lat: 38.35, Lng: 26.77}, LocationVisibility: logbook.LocationParticipants,
				PhotoUploadID: photo, CreatedAt: at, CallerCanRemove: true},
			{ID: theirs, UserID: id.New(), Values: map[string]json.RawMessage{"species": json.RawMessage(`"Lüfer"`)}, CreatedAt: at},
		},
		NextPageToken: "SONRAKI", LocationEnabled: true, LocationDefault: logbook.LocationSelf, MaxEntries: 100, CallerCanAdd: true,
	}}
	s := NewLogComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "catches"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "catches"}

	list, err := s.ListLogEntries(ctx, &corev1.ListLogEntriesRequest{Ref: ref, PageSize: 20, PageToken: "P"})
	if err != nil || f.method != "List" || f.ref != want || f.size != 20 || f.token != "P" {
		t.Fatalf("liste: %v %s %+v", err, f.method, f.ref)
	}
	if len(list.GetEntries()) != 2 || list.GetNextPageToken() != "SONRAKI" || !list.GetLocationEnabled() ||
		list.GetLocationDefault() != corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_SELF || list.GetMaxEntries() != 100 || !list.GetCallerCanAdd() {
		t.Fatalf("liste yanıtı: %v", list)
	}
	e := list.GetEntries()[0]
	if e.GetId() != mine.String() || e.GetUserId() != author.String() || e.GetValues()["species"] != `"Levrek"` || e.GetValues()["weight"] != `1.25` ||
		e.GetLocation().GetLat() != 38.35 || e.GetLocationVisibility() != corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS ||
		e.GetPhotoUploadId() != photo.String() || !e.GetCreateTime().AsTime().Equal(at) || !e.GetCallerCanRemove() {
		t.Fatalf("kayıt: %v", e)
	}
	if o := list.GetEntries()[1]; o.GetLocation() != nil || o.GetLocationVisibility() != corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_UNSPECIFIED ||
		o.GetPhotoUploadId() != "" || o.GetCallerCanRemove() {
		t.Fatalf("başkasının kaydı: %v", o)
	}

	added, err := s.AddLogEntry(ctx, &corev1.AddLogEntryRequest{
		Ref: ref, Values: map[string]string{"species": `"Levrek"`, "weight": `1.25`}, Location: &corev1.LatLng{Lat: 38.35, Lng: 26.77},
		LocationVisibility: corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_TAYFA, PhotoUploadId: photo.String(), ClientRequestId: "istek",
	})
	if err != nil || f.method != "Add" || f.ref != want || added.GetEntry().GetId() != mine.String() {
		t.Fatalf("ekleme: %v %v", added, err)
	}
	// The values reach the app layer as sent; it checks them.
	if string(f.in.Values["species"]) != `"Levrek"` || string(f.in.Values["weight"]) != `1.25` || f.in.Location == nil || f.in.Location.Lng != 26.77 ||
		f.in.LocationVisibility != logbook.LocationTayfa || f.in.PhotoUploadID != photo || f.in.ClientRequestID != "istek" {
		t.Fatalf("girdi: %+v", f.in)
	}
	if _, err := s.AddLogEntry(ctx, &corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"species": `"Lüfer"`}}); err != nil ||
		f.in.Location != nil || f.in.LocationVisibility != "" || f.in.PhotoUploadID != (id.ID{}) {
		t.Fatalf("konumsuz kayıt: %+v %v", f.in, err)
	}

	if _, err := s.RemoveLogEntry(ctx, &corev1.RemoveLogEntryRequest{Ref: ref, EntryId: theirs.String()}); err != nil || f.method != "Remove" || f.entry != theirs {
		t.Fatalf("silme: %v %s", err, f.entry)
	}

	// The app's errors keep their reasons.
	for appErr, want := range map[error]struct {
		code   codes.Code
		reason corev1.ErrorReason
	}{
		apperr.ErrNotAttended:       {codes.FailedPrecondition, corev1.ErrorReason_ERROR_REASON_NOT_ATTENDED},
		apperr.ErrEventLimit:        {codes.ResourceExhausted, corev1.ErrorReason_ERROR_REASON_EVENT_LIMIT},
		apperr.ErrEventItemNotFound: {codes.NotFound, corev1.ErrorReason_ERROR_REASON_EVENT_ITEM_NOT_FOUND},
		apperr.ErrUploadNotFound:    {codes.NotFound, corev1.ErrorReason_ERROR_REASON_UPLOAD_NOT_FOUND},
	} {
		f.err = appErr
		_, err := s.AddLogEntry(ctx, &corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"species": `"Levrek"`}})
		if code, info := infoOf(t, err); code != want.code || info.GetReason() != want.reason.String() {
			t.Errorf("%v: %v", appErr, err)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestLogComponentValidation(t *testing.T) {
	f := &fakeLogbook{}
	s := NewLogComponentServer(f, discard)
	tf, ev, good := id.New().String(), id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "catches"}
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}

	for name, bad := range map[string]*corev1.ComponentRef{
		"başvurusuz":     nil,
		"bozuk tayfa":    {TayfaId: "x", EventId: ev, ComponentKey: "catches"},
		"bozuk etkinlik": {TayfaId: tf, EventId: "x", ComponentKey: "catches"},
		"bozuk anahtar":  {TayfaId: tf, EventId: ev, ComponentKey: "Catches"},
	} {
		_, e1 := s.ListLogEntries(ctx, &corev1.ListLogEntriesRequest{Ref: bad})
		_, e2 := s.AddLogEntry(ctx, &corev1.AddLogEntryRequest{Ref: bad})
		_, e3 := s.RemoveLogEntry(ctx, &corev1.RemoveLogEntryRequest{Ref: bad, EntryId: good})
		for i, err := range []error{e1, e2, e3} {
			if codeOf(t, err) != codes.InvalidArgument {
				t.Errorf("%s / %d: %v", name, i, err)
			}
		}
	}
	tooMany := map[string]string{}
	for i := range 9 {
		tooMany["f"+strings.Repeat("x", i+1)] = `1`
	}
	for name, tc := range map[string]struct {
		req   *corev1.AddLogEntryRequest
		field string
	}{
		"çok alan":            {&corev1.AddLogEntryRequest{Ref: ref, Values: tooMany}, "values"},
		"bozuk alan anahtarı": {&corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"Tür": `"x"`}}, "values"},
		"boş değer":           {&corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"species": ""}}, "values"},
		"uzun değer":          {&corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"species": `"` + strings.Repeat("a", 9000) + `"`}}, "values"},
		"bilinmeyen gizlilik": {&corev1.AddLogEntryRequest{Ref: ref, LocationVisibility: 9}, "location_visibility"},
		"bozuk fotoğraf":      {&corev1.AddLogEntryRequest{Ref: ref, PhotoUploadId: "x"}, "photo_upload_id"},
	} {
		_, err := s.AddLogEntry(ctx, tc.req)
		if got := field(err); got != tc.field {
			t.Errorf("%s: %s", name, got)
		}
	}
	_, err := s.RemoveLogEntry(ctx, &corev1.RemoveLogEntryRequest{Ref: ref, EntryId: "x"})
	if field(err) != "entry_id" {
		t.Error("bozuk kayıt kimliği")
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.AddLogEntry(context.Background(), &corev1.AddLogEntryRequest{Ref: ref}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the log component.
func TestLogComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.LogComponentService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("günlük servisi: %v", info)
	}
}

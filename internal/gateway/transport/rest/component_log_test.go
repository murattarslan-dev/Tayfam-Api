package rest

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const logComponentService = "tayfam.core.v1.LogComponentService"

// fakeLog is core's LogComponentService for the log tests.
type fakeLog struct {
	got   proto.Message
	token string
	err   error
	calls int
}

func (f *fakeLog) rec(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func testLogEntry() *corev1.LogEntry {
	return &corev1.LogEntry{
		Id: entryID, UserId: userID, Values: map[string]string{"species": `"Levrek"`, "weight": `1.25`},
		Location: &corev1.LatLng{Lat: 38.35, Lng: 26.77}, LocationVisibility: corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS,
		PhotoUploadId: uploadID, CreateTime: timestamppb.New(t0), CallerCanRemove: true,
		VocabularyIds: map[string]string{"species": itemID},
	}
}

func (f *fakeLog) ListLogEntries(ctx context.Context, in *corev1.ListLogEntriesRequest, _ ...grpc.CallOption) (*corev1.ListLogEntriesResponse, error) {
	f.rec(ctx, in)
	return &corev1.ListLogEntriesResponse{
		Entries:       []*corev1.LogEntry{testLogEntry(), {Id: eventID, UserId: tayfaID, Values: map[string]string{"species": `"Lüfer"`}, CreateTime: timestamppb.New(t0)}},
		NextPageToken: "SONRAKI", LocationEnabled: true,
		LocationDefault: corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_SELF, MaxEntries: 100, CallerCanAdd: true,
	}, f.err
}

func (f *fakeLog) AddLogEntry(ctx context.Context, in *corev1.AddLogEntryRequest, _ ...grpc.CallOption) (*corev1.AddLogEntryResponse, error) {
	f.rec(ctx, in)
	return &corev1.AddLogEntryResponse{Entry: testLogEntry()}, f.err
}

func (f *fakeLog) RemoveLogEntry(ctx context.Context, in *corev1.RemoveLogEntryRequest, _ ...grpc.CallOption) (*corev1.RemoveLogEntryResponse, error) {
	f.rec(ctx, in)
	return &corev1.RemoveLogEntryResponse{}, f.err
}

const (
	logPath      = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/catches/entries"
	logEntryPath = logPath + "/" + entryID
)

func TestLogComponentRoutes(t *testing.T) {
	f := &fakeLog{}
	h := componentRouter(fakeServices{logComponentService: f})
	ref := &corev1.ComponentRef{TayfaId: tayfaID, EventId: eventID, ComponentKey: "catches"}

	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"GET", logPath, "", http.StatusOK, &corev1.ListLogEntriesRequest{Ref: ref}},
		{"GET", logPath + "?page_size=20&page_token=P", "", http.StatusOK, &corev1.ListLogEntriesRequest{Ref: ref, PageSize: 20, PageToken: "P"}},
		{"POST", logPath, `{"values":{"species":"Levrek","weight":1.25},"location":{"lat":38.35,"lng":26.77},"location_visibility":"participants","photo_upload_id":"` + uploadID + `","client_request_id":"` + reqID + `"}`,
			http.StatusCreated, &corev1.AddLogEntryRequest{
				Ref: ref, Values: map[string]string{"species": `"Levrek"`, "weight": `1.25`}, Location: &corev1.LatLng{Lat: 38.35, Lng: 26.77},
				LocationVisibility: corev1.LogLocationVisibility_LOG_LOCATION_VISIBILITY_PARTICIPANTS, PhotoUploadId: uploadID, ClientRequestId: reqID,
			}},
		{"POST", logPath, `{"values":{"species":"Lüfer"},"client_request_id":"` + reqID + `"}`, http.StatusCreated,
			&corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"species": `"Lüfer"`}, ClientRequestId: reqID}},
		{"POST", logPath, `{"values":{"weight":1.25},"vocabulary_ids":{"species":"` + itemID + `"},"client_request_id":"` + reqID + `"}`, http.StatusCreated,
			&corev1.AddLogEntryRequest{Ref: ref, Values: map[string]string{"weight": `1.25`}, VocabularyIds: map[string]string{"species": itemID}, ClientRequestId: reqID}},
		{"DELETE", logEntryPath, "", http.StatusNoContent, &corev1.RemoveLogEntryRequest{Ref: ref, EntryId: entryID}},
	} {
		f.got, f.token = nil, ""
		w := as(h, tc.method, tc.path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if f.got == nil || reflect.TypeOf(f.got) != reflect.TypeOf(tc.want) || !proto.Equal(f.got, tc.want) || f.token != "USERTOKEN" {
			t.Fatalf("%s %s: core'a giden %v (%q), beklenen %v", tc.method, tc.path, f.got, f.token, tc.want)
		}
	}

	w := as(h, "GET", logPath, "")
	l := decodeBody[logEntryListJSON](t, w)
	if len(l.Entries) != 2 || l.NextPageToken != "SONRAKI" || !l.LocationEnabled || l.LocationDefault != "self" || l.MaxEntries != 100 || !l.CallerCanAdd {
		t.Fatalf("liste: %+v", l)
	}
	e := l.Entries[0]
	if e.ID != entryID || e.UserID != userID || string(e.Values["species"]) != `"Levrek"` || string(e.Values["weight"]) != `1.25` ||
		e.Location == nil || e.Location.Lat != 38.35 || e.LocationVisibility != "participants" || e.PhotoUploadID != uploadID ||
		!e.CreatedAt.Equal(t0) || !e.CallerCanRemove || e.VocabularyIDs["species"] != itemID {
		t.Fatalf("kayıt: %+v", e)
	}
	// Someone else's entry without a location: no location keys, no photo.
	if want := `{"id":"` + eventID + `","user_id":"` + tayfaID + `","values":{"species":"Lüfer"},"created_at":`; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("konumsuz kayıt: %s", w.Body)
	}
	w = as(h, "POST", logPath, `{"values":{"species":"Levrek"},"client_request_id":"`+reqID+`"}`)
	if a := decodeBody[logEntryJSON](t, w); a.ID != entryID {
		t.Fatalf("ekleme yanıtı: %+v", a)
	}
}

func TestLogComponentRoutesValidateInput(t *testing.T) {
	f := &fakeLog{}
	h := componentRouter(fakeServices{logComponentService: f})

	tooMany := `{"values":{"a1":1,"a2":1,"a3":1,"a4":1,"a5":1,"a6":1,"a7":1,"a8":1,"a9":1},"client_request_id":"` + reqID + `"}`
	for name, body := range map[string]string{
		"istek kimliği yok":    `{"values":{"species":"Levrek"}}`,
		"bozuk istek kimliği":  `{"values":{"species":"Levrek"},"client_request_id":"x"}`,
		"null değer":           `{"values":{"species":null},"client_request_id":"` + reqID + `"}`,
		"bozuk alan anahtarı":  `{"values":{"Tür":"Levrek"},"client_request_id":"` + reqID + `"}`,
		"çok alan":             tooMany,
		"konum aralık dışı":    `{"values":{"species":"Levrek"},"location":{"lat":0,"lng":200},"client_request_id":"` + reqID + `"}`,
		"konum fazla alan":     `{"values":{"species":"Levrek"},"location":{"lat":1,"lng":2,"accuracy":5},"client_request_id":"` + reqID + `"}`,
		"bilinmeyen gizlilik":  `{"values":{"species":"Levrek"},"location":{"lat":1,"lng":2},"location_visibility":"public","client_request_id":"` + reqID + `"}`,
		"bozuk fotoğraf":       `{"values":{"species":"Levrek"},"photo_upload_id":"x","client_request_id":"` + reqID + `"}`,
		"eski av gövdesi":      `{"species":"Levrek","length_cm":40,"client_request_id":"` + reqID + `"}`,
		"bozuk sözlük kimliği": `{"values":{},"vocabulary_ids":{"species":"x"},"client_request_id":"` + reqID + `"}`,
		"bozuk sözlük alanı":   `{"values":{},"vocabulary_ids":{"Tür":"` + itemID + `"},"client_request_id":"` + reqID + `"}`,
		"bozuk JSON":           `{"values":`,
	} {
		if w := as(h, "POST", logPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", logPath + "?page_size=-1"},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components/Catches/entries"},
		{"DELETE", logPath + "/x"},
	} {
		if w := as(h, rt.method, rt.path, ""); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls)
	}
	// The Faz 1 routes are gone (#243).
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/fishing-catches"},
		{"POST", "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/fishing-catches"},
	} {
		if w := as(h, rt.method, rt.path, `{}`); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("eski uç %s %s: %d", rt.method, rt.path, w.Code)
		}
	}
	// No token, no call; a write without the client header is refused.
	for _, rt := range []struct{ method, path string }{
		{"GET", logPath}, {"POST", logPath}, {"DELETE", logEntryPath},
	} {
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{clientHeader: "android", "Content-Type": "application/json"}); w.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("token'sız %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method == "GET" {
			continue
		}
		if w := do(h, rt.method, rt.path, `{}`, map[string]string{"Authorization": "Bearer T"}); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || f.calls != 0 {
			t.Fatalf("istemci başlıksız %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// core's refusals reach the client as their problem types.
func TestLogComponentErrors(t *testing.T) {
	for reason, want := range map[string]struct {
		grpc codes.Code
		code int
		typ  string
	}{
		"ERROR_REASON_COMPONENT_NOT_FOUND":  {codes.NotFound, http.StatusNotFound, "component-not-found"},
		"ERROR_REASON_COMPONENT_FROZEN":     {codes.FailedPrecondition, http.StatusConflict, "component-frozen"},
		"ERROR_REASON_NOT_ATTENDED":         {codes.FailedPrecondition, http.StatusConflict, "not-attended"},
		"ERROR_REASON_EVENT_LIMIT":          {codes.ResourceExhausted, http.StatusConflict, "event-limit"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND": {codes.NotFound, http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_PERMISSION_DENIED":    {codes.PermissionDenied, http.StatusForbidden, "permission-denied"},
	} {
		h := componentRouter(fakeServices{logComponentService: &fakeLog{err: coreErr(want.grpc, reason)}})
		if w := as(h, "POST", logPath, `{"values":{"species":"Levrek"},"client_request_id":"`+reqID+`"}`); w.Code != want.code || problemType(t, w) != want.typ {
			t.Errorf("%s: %d %s", reason, w.Code, w.Body)
		}
	}
}

// An entry's location never reaches a log line or a problem response, also
// when core's error message carries it.
func TestLogLocationNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := testCoreDeps(&fakeCore{}, 100)
	d.Log = log
	leaky := &fakeLog{err: status.Error(codes.Internal, "konum "+secretLat+","+secretLng+" uzak")}
	d.Components = fakeServices{logComponentService: leaky}
	h := coreRouterWith(d)
	loc := `{"lat":` + secretLat + `,"lng":` + secretLng + `}`
	for _, body := range []string{
		`{"values":{"species":"Levrek"},"location":` + loc + `,"client_request_id":"` + reqID + `"}`,
		`{"values":{"species":"Levrek"},"location":{"lat":` + secretLat + `,"lng":"` + secretLng + `"},"client_request_id":"` + reqID + `"}`,
	} {
		w := as(h, "POST", logPath, body)
		if strings.Contains(w.Body.String(), secretLat) || strings.Contains(w.Body.String(), secretLng) {
			t.Errorf("konum yanıta girdi: %s", w.Body)
		}
	}
	if strings.Contains(logs.String(), secretLat) || strings.Contains(logs.String(), secretLng) {
		t.Fatalf("konum log'a girdi: %s", logs.String())
	}
}

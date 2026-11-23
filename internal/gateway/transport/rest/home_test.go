package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const homeService = "tayfam.core.v1.TayfaHomeService"

// fakeHome is core's TayfaHomeService for the home page tests.
type fakeHome struct {
	got   proto.Message
	token string
	resp  proto.Message
	err   error
	calls int
}

func (f *fakeHome) record(ctx context.Context, in proto.Message) {
	f.got, f.token, f.calls = in, tokenFrom(ctx), f.calls+1
}

func (f *fakeHome) GetHomeWidgets(ctx context.Context, in *corev1.GetHomeWidgetsRequest, _ ...grpc.CallOption) (*corev1.GetHomeWidgetsResponse, error) {
	f.record(ctx, in)
	r, _ := f.resp.(*corev1.GetHomeWidgetsResponse)
	if r == nil {
		r = &corev1.GetHomeWidgetsResponse{}
	}
	return r, f.err
}

func (f *fakeHome) SetHomeWidgets(ctx context.Context, in *corev1.SetHomeWidgetsRequest, _ ...grpc.CallOption) (*corev1.SetHomeWidgetsResponse, error) {
	f.record(ctx, in)
	r, _ := f.resp.(*corev1.SetHomeWidgetsResponse)
	if r == nil {
		r = &corev1.SetHomeWidgetsResponse{}
	}
	return r, f.err
}

func (f *fakeHome) GetHomeWidgetData(ctx context.Context, in *corev1.GetHomeWidgetDataRequest, _ ...grpc.CallOption) (*corev1.GetHomeWidgetDataResponse, error) {
	f.record(ctx, in)
	r, _ := f.resp.(*corev1.GetHomeWidgetDataResponse)
	if r == nil {
		r = &corev1.GetHomeWidgetDataResponse{}
	}
	return r, f.err
}

const homeWidgetsTestPath = "/v1/tayfas/" + tayfaID + "/home-widgets"

func TestHomeWidgetsRoutes(t *testing.T) {
	at := time.Date(2027, 5, 19, 9, 0, 0, 0, time.UTC)
	widgets := []*corev1.HomeWidget{
		{Key: "upcoming", Kind: corev1.HomeWidgetKind_HOME_WIDGET_KIND_SECTION, Module: "events", Enabled: true, Position: 0},
		{Key: "countdown", Kind: corev1.HomeWidgetKind_HOME_WIDGET_KIND_DISPLAY, Enabled: true, Position: 1, Config: &corev1.HomeWidgetConfig{
			Countdown: &corev1.CountdownWidgetConfig{Title: "Bayram", TargetTime: timestamppb.New(at)},
		}},
		{Key: "goal", Kind: corev1.HomeWidgetKind_HOME_WIDGET_KIND_DISPLAY, Position: 2, Config: &corev1.HomeWidgetConfig{
			Goal: &corev1.GoalWidgetConfig{MetricKey: "trash_pickup.weight", Target: 500},
		}},
	}
	f := &fakeHome{resp: &corev1.GetHomeWidgetsResponse{Widgets: widgets}}
	h := componentRouter(fakeServices{homeService: f})

	w := as(h, "GET", homeWidgetsTestPath, "")
	if w.Code != http.StatusOK || !proto.Equal(f.got, &corev1.GetHomeWidgetsRequest{TayfaId: tayfaID}) || f.token != "USERTOKEN" {
		t.Fatalf("düzen: %d %v %q %s", w.Code, f.got, f.token, w.Body)
	}
	v := decodeBody[homeWidgetsJSON](t, w)
	if len(v.Widgets) != 3 || v.Widgets[0].Kind != "section" || v.Widgets[0].Module != "events" || v.Widgets[0].Config != nil {
		t.Fatalf("yanıt: %+v", v)
	}
	if c := v.Widgets[1].Config; c == nil || c.Countdown == nil || c.Countdown.Title != "Bayram" || !c.Countdown.At.Equal(at) {
		t.Fatalf("geri sayım: %+v", v.Widgets[1])
	}
	if g := v.Widgets[2].Config.Goal; g == nil || g.MetricKey != "trash_pickup.weight" || g.Target != 500 || g.Year != nil || v.Widgets[2].Enabled {
		t.Fatalf("hedef: %+v", v.Widgets[2])
	}
	if body := w.Body.String(); strings.Contains(body, `"year"`) || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("gövde: %s", body)
	}

	// The whole list goes to core as is.
	f.resp = &corev1.SetHomeWidgetsResponse{Widgets: widgets[:1]}
	body := `{"widgets":[{"key":"upcoming","enabled":true},
		{"key":"goal","enabled":true,"config":{"goal":{"metric_key":"events","target":12,"year":2027}}},
		{"key":"countdown","enabled":false,"config":{"countdown":{"title":"Bayram","at":"2027-05-19T09:00:00Z"}}}]}`
	w = as(h, "PUT", homeWidgetsTestPath, body)
	want := &corev1.SetHomeWidgetsRequest{TayfaId: tayfaID, Widgets: []*corev1.HomeWidgetInput{
		{Key: "upcoming", Enabled: true},
		{Key: "goal", Enabled: true, Config: &corev1.HomeWidgetConfig{Goal: &corev1.GoalWidgetConfig{MetricKey: "events", Target: 12, Year: 2027}}},
		{Key: "countdown", Config: &corev1.HomeWidgetConfig{Countdown: &corev1.CountdownWidgetConfig{Title: "Bayram", TargetTime: timestamppb.New(at)}}},
	}}
	if w.Code != http.StatusOK || !proto.Equal(f.got, want) {
		t.Fatalf("değiştirme: %d %v %s", w.Code, f.got, w.Body)
	}
	if v := decodeBody[homeWidgetsJSON](t, w); len(v.Widgets) != 1 {
		t.Fatalf("yeni düzen: %+v", v)
	}

	calls := f.calls
	for name, b := range map[string]string{
		"liste yok":         `{}`,
		"bilinmeyen alan":   `{"widgets":[],"extra":1}`,
		"bozuk anahtar":     `{"widgets":[{"key":"Stats","enabled":true}]}`,
		"bilinmeyen ayar":   `{"widgets":[{"key":"stats","enabled":true,"config":{"weather":{}}}]}`,
		"bozuk tarih":       `{"widgets":[{"key":"countdown","enabled":true,"config":{"countdown":{"title":"A","at":"yarın"}}}]}`,
		"çok uzun liste":    `{"widgets":[` + strings.Repeat(`{"key":"stats","enabled":true},`, 64) + `{"key":"stats","enabled":true}]}`,
		"ayarda fazla alan": `{"widgets":[{"key":"goal","enabled":true,"config":{"goal":{"metric_key":"events","target":1,"note":"x"}}}]}`,
	} {
		if w := as(h, "PUT", homeWidgetsTestPath, b); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := as(h, "GET", "/v1/tayfas/x/home-widgets", ""); w.Code != http.StatusBadRequest {
		t.Errorf("geçersiz tayfa: %d", w.Code)
	}
	if w := as(h, "GET", homeWidgetsTestPath+"/Stats/data", ""); w.Code != http.StatusBadRequest {
		t.Errorf("geçersiz anahtar: %d", w.Code)
	}
	if f.calls != calls {
		t.Fatalf("bozuk istek core'a ulaştı: %d", f.calls-calls)
	}
	if w := do(h, "GET", homeWidgetsTestPath, "", map[string]string{clientHeader: "android"}); w.Code != http.StatusUnauthorized || f.calls != calls {
		t.Fatalf("token'sız: %d", w.Code)
	}

	h = componentRouter(fakeServices{homeService: &fakeHome{err: coreErr(codes.PermissionDenied, "ERROR_REASON_PERMISSION_DENIED")}})
	if w := as(h, "PUT", homeWidgetsTestPath, `{"widgets":[]}`); w.Code != http.StatusForbidden || problemType(t, w) != "permission-denied" {
		t.Fatalf("yönetici olmayan: %d %s", w.Code, w.Body)
	}
}

func TestHomeWidgetDataRoute(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	f := &fakeHome{resp: &corev1.GetHomeWidgetDataResponse{
		Key: "champions",
		Champions: &corev1.ChampionsWidgetData{Period: "this_year", Champions: []*corev1.HomeChampion{{
			EventId: eventID, EventTitle: "Turnuva", EventType: "chess", StartTime: timestamppb.New(start),
			ComponentKey: "celebration", AnnounceTime: timestamppb.New(start.Add(time.Hour)),
			Winners: []*corev1.ChampionWinner{{UserId: userID}, {TeamId: itemID, TeamName: "Kartallar", TeamPosition: 2}, {GuestId: itemID, GuestNickname: "Misafir"}},
		}}},
	}}
	h := componentRouter(fakeServices{homeService: f})
	w := as(h, "GET", homeWidgetsTestPath+"/champions/data", "")
	if w.Code != http.StatusOK || !proto.Equal(f.got, &corev1.GetHomeWidgetDataRequest{TayfaId: tayfaID, Key: "champions"}) {
		t.Fatalf("istek: %d %v %s", w.Code, f.got, w.Body)
	}
	v := decodeBody[homeWidgetDataJSON](t, w)
	if v.Key != "champions" || v.Champions == nil || len(v.Champions.Champions) != 1 || v.Stats != nil {
		t.Fatalf("yanıt: %+v", v)
	}
	c := v.Champions.Champions[0]
	if c.EventID != eventID || c.EventTitle != "Turnuva" || c.StartsAt == nil || !c.StartsAt.Equal(start) || len(c.Winners) != 3 {
		t.Fatalf("şampiyon: %+v", c)
	}
	if c.Winners[0] != (championWinnerJSON{UserID: userID}) || c.Winners[1] != (championWinnerJSON{TeamID: itemID, TeamName: "Kartallar", TeamPosition: 2}) ||
		c.Winners[2] != (championWinnerJSON{GuestID: itemID, GuestNickname: "Misafir"}) {
		t.Fatalf("kazananlar: %+v", c.Winners)
	}

	// Stats of all time: no year.
	f.resp = &corev1.GetHomeWidgetDataResponse{Key: "stats", Stats: &corev1.StatsWidgetData{
		Period: "all_time", Values: []*corev1.HomeMetricValue{{MetricKey: "events", Value: 7, EventCount: 7}},
	}}
	w = as(h, "GET", homeWidgetsTestPath+"/stats/data", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"year"`) || strings.Contains(w.Body.String(), "computed_at") {
		t.Fatalf("istatistik: %d %s", w.Code, w.Body)
	}
	if v := decodeBody[homeWidgetDataJSON](t, w); v.Stats == nil || len(v.Stats.Values) != 1 || v.Stats.Values[0] != (homeMetricValueJSON{MetricKey: "events", Value: 7, EventCount: 7}) {
		t.Fatalf("istatistik: %+v", v)
	}

	h = componentRouter(fakeServices{homeService: &fakeHome{err: coreErr(codes.NotFound, "ERROR_REASON_HOME_WIDGET_NOT_FOUND")}})
	if w := as(h, "GET", homeWidgetsTestPath+"/goal/data", ""); w.Code != http.StatusNotFound || problemType(t, w) != "home-widget-not-found" {
		t.Fatalf("gösterilmeyen: %d %s", w.Code, w.Body)
	}
}

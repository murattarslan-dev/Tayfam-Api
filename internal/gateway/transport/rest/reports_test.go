package rest

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const (
	reportID = "01965a3c-0000-7000-8000-000000000030"
	targetID = "01965a3c-0000-7000-8000-000000000031"
)

func testMyReport() *corev1.MyReport {
	return &corev1.MyReport{
		Id: reportID, TayfaId: tayfaID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT, TargetId: targetID,
		Reason: corev1.ReportReason_REPORT_REASON_SPAM, Description: "Reklam", Status: corev1.ReportStatus_REPORT_STATUS_RESOLVED,
		Outcome: corev1.ReportOutcome_REPORT_OUTCOME_ACTION_TAKEN, CreateTime: timestamppb.New(t0), ResolveTime: timestamppb.New(t0),
	}
}

func testModReport() *corev1.ModerationReport {
	return &corev1.ModerationReport{
		Id: reportID, TayfaId: tayfaID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_USER, TargetId: targetID,
		SubjectUserId: targetID, Reason: corev1.ReportReason_REPORT_REASON_CHILD_SAFETY,
		Priority: corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED, Status: corev1.ReportStatus_REPORT_STATUS_OPEN,
		Escalated: true, CreateTime: timestamppb.New(t0),
	}
}

func (f *fakeCore) CreateReport(ctx context.Context, in *corev1.CreateReportRequest, _ ...grpc.CallOption) (*corev1.CreateReportResponse, error) {
	// An existing report comes back for the "tayfa" target in these tests.
	created := in.GetTargetType() != corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA
	return &corev1.CreateReportResponse{Report: testMyReport(), Created: created}, f.record(ctx, in)
}
func (f *fakeCore) GetMyReport(ctx context.Context, in *corev1.GetMyReportRequest, _ ...grpc.CallOption) (*corev1.GetMyReportResponse, error) {
	return &corev1.GetMyReportResponse{Report: testMyReport()}, f.record(ctx, in)
}
func (f *fakeCore) ListMyReports(ctx context.Context, in *corev1.ListMyReportsRequest, _ ...grpc.CallOption) (*corev1.ListMyReportsResponse, error) {
	open := testMyReport()
	open.Status, open.Outcome, open.ResolveTime = corev1.ReportStatus_REPORT_STATUS_OPEN, 0, nil
	return &corev1.ListMyReportsResponse{Reports: []*corev1.MyReport{testMyReport(), open}, NextPageToken: "SONRAKI"}, f.record(ctx, in)
}
func (f *fakeCore) ListTayfaReports(ctx context.Context, in *corev1.ListTayfaReportsRequest, _ ...grpc.CallOption) (*corev1.ListTayfaReportsResponse, error) {
	return &corev1.ListTayfaReportsResponse{Reports: []*corev1.ModerationReport{testModReport()}}, f.record(ctx, in)
}
func (f *fakeCore) ResolveReport(ctx context.Context, in *corev1.ResolveReportRequest, _ ...grpc.CallOption) (*corev1.ResolveReportResponse, error) {
	m := testModReport()
	m.Status, m.Resolution, m.HandledByUserId, m.ResolveTime = corev1.ReportStatus_REPORT_STATUS_RESOLVED,
		corev1.ReportResolution_REPORT_RESOLUTION_DISMISSED, userID, timestamppb.New(t0)
	return &corev1.ResolveReportResponse{Report: m}, f.record(ctx, in)
}

func TestReportRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/reports"
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               proto.Message
	}{
		{"POST", base, `{"target_type":"fishing_catch","target_id":"` + targetID + `","reason":"child_safety","description":"Açıklama\nsatırı"}`, 201,
			&corev1.CreateReportRequest{TayfaId: tayfaID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_FISHING_CATCH, TargetId: targetID,
				Reason: corev1.ReportReason_REPORT_REASON_CHILD_SAFETY, Description: "Açıklama\nsatırı"}},
		{"POST", base, `{"target_type":"tayfa","target_id":"` + tayfaID + `","reason":"gambling"}`, 200,
			&corev1.CreateReportRequest{TayfaId: tayfaID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA, TargetId: tayfaID,
				Reason: corev1.ReportReason_REPORT_REASON_GAMBLING}},
		{"GET", base, "", 200, &corev1.ListTayfaReportsRequest{TayfaId: tayfaID}},
		{"GET", base + "?status=resolved&page_size=5&page_token=P", "", 200, &corev1.ListTayfaReportsRequest{TayfaId: tayfaID,
			Status: corev1.ReportStatus_REPORT_STATUS_RESOLVED, PageSize: 5, PageToken: "P"}},
		{"POST", base + "/" + reportID + "/resolution", `{"resolution":"member_sanctioned"}`, 200, &corev1.ResolveReportRequest{
			TayfaId: tayfaID, ReportId: reportID, Resolution: corev1.ReportResolution_REPORT_RESOLUTION_MEMBER_SANCTIONED}},
		{"GET", "/v1/me/reports?page_size=2", "", 200, &corev1.ListMyReportsRequest{PageSize: 2}},
		{"GET", "/v1/me/reports/" + reportID, "", 200, &corev1.GetMyReportRequest{ReportId: reportID}},
	} {
		w := as(h, tc.method, tc.path, tc.body)
		got, tok, _ := f.last()
		if w.Code != tc.code || !proto.Equal(got, tc.want) || tok != "USERTOKEN" {
			t.Errorf("%s %s: %d %s; core'a giden %v", tc.method, tc.path, w.Code, w.Body, got)
		}
	}

	w := as(h, "POST", base, `{"target_type":"user","target_id":"`+targetID+`","reason":"spam"}`)
	m := decodeBody[myReportJSON](t, w)
	if w.Header().Get("Location") != "/v1/me/reports/"+reportID || m.TargetType != "announcement" || m.Reason != "spam" ||
		m.Status != "resolved" || m.Outcome != "action_taken" || m.ResolvedAt == nil || !m.CreatedAt.Equal(t0) {
		t.Fatalf("oluşturma yanıtı: %v %+v", w.Header(), m)
	}
	if b := w.Body.String(); strings.Contains(b, "handled_by") || strings.Contains(b, "subject") {
		t.Fatalf("şikâyet edene çözen ya da şikâyet edilen döndü: %s", b)
	}
	w = as(h, "GET", "/v1/me/reports", "")
	if l := decodeBody[myReportListJSON](t, w); len(l.Reports) != 2 || l.Reports[1].Outcome != "" || l.Reports[1].ResolvedAt != nil || l.NextPageToken != "SONRAKI" {
		t.Fatalf("kendi şikâyetleri: %+v", l)
	}
	w = as(h, "GET", base, "")
	q := decodeBody[modReportListJSON](t, w)
	if len(q.Reports) != 1 || q.Reports[0].Priority != "minor_involved" || q.Reports[0].Reason != "child_safety" ||
		q.Reports[0].SubjectUserID != targetID || !q.Reports[0].Escalated || q.Reports[0].Resolution != "" || q.Reports[0].HandledByUserID != "" {
		t.Fatalf("kuyruk: %+v", q)
	}
	if strings.Contains(w.Body.String(), "reporter") {
		t.Fatalf("kuyrukta şikâyet eden: %s", w.Body)
	}
	w = as(h, "POST", base+"/"+reportID+"/resolution", `{"resolution":"dismissed"}`)
	if r := decodeBody[modReportJSON](t, w); r.Resolution != "dismissed" || r.HandledByUserID != userID || r.ResolvedAt == nil {
		t.Fatalf("çözüm: %+v", r)
	}
}

func TestReportBadRequests(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	base := "/v1/tayfas/" + tayfaID + "/reports"
	ok := `"target_id":"` + targetID + `"`
	for name, tc := range map[string]struct{ method, path, body string }{
		"bilinmeyen tür":        {"POST", base, `{"target_type":"channel_message",` + ok + `,"reason":"spam"}`},
		"tür yok":               {"POST", base, `{` + ok + `,"reason":"spam"}`},
		"bilinmeyen sebep":      {"POST", base, `{"target_type":"user",` + ok + `,"reason":"boring"}`},
		"bozuk hedef":           {"POST", base, `{"target_type":"user","target_id":"x","reason":"spam"}`},
		"uzun açıklama":         {"POST", base, `{"target_type":"user",` + ok + `,"reason":"spam","description":"` + strings.Repeat("ş", 1001) + `"}`},
		"başkası adına":         {"POST", base, `{"target_type":"user",` + ok + `,"reason":"spam","reporter_id":"` + userID + `"}`},
		"bozuk tayfa":           {"POST", "/v1/tayfas/x/reports", `{"target_type":"user",` + ok + `,"reason":"spam"}`},
		"bozuk durum":           {"GET", base + "?status=closed", ""},
		"bozuk sayfa":           {"GET", base + "?page_size=101", ""},
		"bilinmeyen çözüm":      {"POST", base + "/" + reportID + "/resolution", `{"resolution":"ignored"}`},
		"çözüm yok":             {"POST", base + "/" + reportID + "/resolution", `{}`},
		"bozuk şikâyet":         {"POST", base + "/x/resolution", `{"resolution":"dismissed"}`},
		"bozuk kendi şikâyetim": {"GET", "/v1/me/reports/x", ""},
	} {
		if w := as(h, tc.method, tc.path, tc.body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, problemType(t, w))
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
	// 1000 characters after trimming are accepted.
	if w := as(h, "POST", base, `{"target_type":"user",`+ok+`,"reason":"spam","description":"  `+strings.Repeat("ş", 1000)+`  "}`); w.Code != 201 {
		t.Fatalf("1000 karakter: %d %s", w.Code, w.Body)
	}
}

func TestReportErrors(t *testing.T) {
	for _, tc := range []struct {
		reason corev1.ErrorReason
		code   codes.Code
		status int
		typ    string
	}{
		{corev1.ErrorReason_ERROR_REASON_REPORT_NOT_FOUND, codes.NotFound, 404, "report-not-found"},
		{corev1.ErrorReason_ERROR_REASON_REPORT_TARGET_NOT_FOUND, codes.NotFound, 404, "report-target-not-found"},
		{corev1.ErrorReason_ERROR_REASON_REPORT_LIMIT, codes.ResourceExhausted, 429, "report-limit"},
		{corev1.ErrorReason_ERROR_REASON_REPORT_ALREADY_RESOLVED, codes.FailedPrecondition, 409, "report-already-resolved"},
		{corev1.ErrorReason_ERROR_REASON_REPORT_ACTION_MISSING, codes.FailedPrecondition, 409, "report-action-missing"},
	} {
		f := &fakeCore{err: coreErr(tc.code, tc.reason.String())}
		w := as(coreRouter(f), "POST", "/v1/tayfas/"+tayfaID+"/reports/"+reportID+"/resolution", `{"resolution":"dismissed"}`)
		if w.Code != tc.status || problemType(t, w) != tc.typ {
			t.Errorf("%s: %d %s", tc.reason, w.Code, problemType(t, w))
		}
	}
}

package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/report"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeReports records what transport passed on.
type fakeReports struct {
	user, tayfa, r id.ID
	create         report.CreateInput
	status         report.Status
	resolution     report.Resolution
	mine           report.MyView
	mod            report.ModView
}

func (f *fakeReports) Create(_ context.Context, u, t id.ID, in report.CreateInput) (report.MyView, bool, error) {
	f.user, f.tayfa, f.create = u, t, in
	return f.mine, true, nil
}
func (f *fakeReports) GetMine(_ context.Context, u, r id.ID) (report.MyView, error) {
	f.user, f.r = u, r
	return f.mine, nil
}
func (f *fakeReports) ListMine(_ context.Context, u id.ID, _ int32, _ string) ([]report.MyView, string, error) {
	f.user = u
	return []report.MyView{f.mine}, "", nil
}
func (f *fakeReports) ListQueue(_ context.Context, u, t id.ID, st report.Status, _ int32, _ string) ([]report.ModView, string, error) {
	f.user, f.tayfa, f.status = u, t, st
	return []report.ModView{f.mod}, "sonraki", nil
}
func (f *fakeReports) Resolve(_ context.Context, u, t, r id.ID, res report.Resolution) (report.ModView, error) {
	f.user, f.tayfa, f.r, f.resolution = u, t, r, res
	return f.mod, nil
}

// TestModerationReportHasNoReporter: the queue's contract cannot carry the
// reporter (issue #76), the escalation or anyone's age (ADR-0012 as updated
// on 2026-11-09: the priority is shown, nothing else about age).
func TestModerationReportHasNoReporter(t *testing.T) {
	fields := (&corev1.ModerationReport{}).ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		n := string(fields.Get(i).Name())
		for _, banned := range []string{"reporter", "minor", "escalat", "age", "birth"} {
			if strings.Contains(n, banned) {
				t.Fatalf("ModerationReport.%s", n)
			}
		}
	}
	mine := (&corev1.MyReport{}).ProtoReflect().Descriptor().Fields()
	for i := range mine.Len() {
		if n := string(mine.Get(i).Name()); strings.Contains(n, "handled_by") || strings.Contains(n, "subject") {
			t.Fatalf("MyReport.%s", n)
		}
	}
}

func TestReportServer(t *testing.T) {
	user, tid, rid, subject := id.New(), id.New(), id.New(), id.New()
	now := time.Now().UTC()
	f := &fakeReports{
		mine: report.MyView{ID: rid, TayfaID: tid, TargetType: report.TargetFishingCatch, TargetID: subject, Reason: report.ReasonChildSafety,
			Status: report.StatusResolved, Outcome: report.OutcomeDismissed, CreatedAt: now, ResolvedAt: now},
		mod: report.ModView{ID: rid, TayfaID: tid, TargetType: report.TargetUser, TargetID: subject, SubjectID: subject,
			Reason: report.ReasonStaffConduct, Priority: report.PriorityMinorInvolved, Status: report.StatusOpen, CreatedAt: now},
	}
	srv := NewReportServer(f, discard)

	for method, err := range map[string]error{
		"Create": func() error {
			_, err := srv.CreateReport(context.Background(), &corev1.CreateReportRequest{})
			return err
		}(),
		"List": func() error {
			_, err := srv.ListTayfaReports(context.Background(), &corev1.ListTayfaReportsRequest{TayfaId: tid.String()})
			return err
		}(),
	} {
		if code, _ := infoOf(t, err); code != codes.Unauthenticated {
			t.Errorf("%s token'sız: %v", method, err)
		}
	}

	ctx := callerCtx(user)
	resp, err := srv.CreateReport(ctx, &corev1.CreateReportRequest{
		TayfaId: tid.String(), TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_ANSWER, TargetId: subject.String(),
		Reason: corev1.ReportReason_REPORT_REASON_HATE_SPEECH, Description: "Açıklama",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != tid || f.create != (report.CreateInput{TargetType: report.TargetAnswer, TargetID: subject.String(), Reason: report.ReasonHateSpeech, Description: "Açıklama"}) {
		t.Fatalf("app girdisi = %+v", f.create)
	}
	m := resp.GetReport()
	if !resp.GetCreated() || m.GetTargetType() != corev1.ReportTargetType_REPORT_TARGET_TYPE_FISHING_CATCH || m.GetReason() != corev1.ReportReason_REPORT_REASON_CHILD_SAFETY ||
		m.GetStatus() != corev1.ReportStatus_REPORT_STATUS_RESOLVED || m.GetOutcome() != corev1.ReportOutcome_REPORT_OUTCOME_DISMISSED || m.GetResolveTime() == nil {
		t.Fatalf("yanıt = %v", resp)
	}
	for name, req := range map[string]*corev1.CreateReportRequest{
		"target_type": {TayfaId: tid.String(), TargetId: subject.String(), Reason: corev1.ReportReason_REPORT_REASON_SPAM},
		"reason":      {TayfaId: tid.String(), TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_USER, TargetId: subject.String()},
		"tayfa_id":    {TayfaId: "x", TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_USER, Reason: corev1.ReportReason_REPORT_REASON_SPAM},
	} {
		_, err := srv.CreateReport(ctx, req)
		if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != name {
			t.Errorf("%s: %v", name, err)
		}
	}

	list, err := srv.ListTayfaReports(ctx, &corev1.ListTayfaReportsRequest{TayfaId: tid.String()})
	if err != nil {
		t.Fatal(err)
	}
	q := list.GetReports()[0]
	if f.status != report.StatusOpen || list.GetNextPageToken() != "sonraki" || q.GetPriority() != corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED ||
		q.GetSubjectUserId() != subject.String() || q.GetHandledByUserId() != "" || q.GetResolveTime() != nil ||
		q.GetReason() != corev1.ReportReason_REPORT_REASON_STAFF_CONDUCT {
		t.Fatalf("kuyruk = %v", list)
	}
	if _, err := srv.ListTayfaReports(ctx, &corev1.ListTayfaReportsRequest{TayfaId: tid.String(), Status: corev1.ReportStatus_REPORT_STATUS_RESOLVED}); err != nil || f.status != report.StatusResolved {
		t.Fatalf("çözülenler: %v %q", err, f.status)
	}

	if _, err := srv.ResolveReport(ctx, &corev1.ResolveReportRequest{TayfaId: tid.String(), ReportId: rid.String(),
		Resolution: corev1.ReportResolution_REPORT_RESOLUTION_MEMBER_SANCTIONED}); err != nil || f.r != rid || f.resolution != report.ResolutionMemberSanctioned {
		t.Fatalf("çözüm: %v %+v", err, f)
	}
	_, err = srv.ResolveReport(ctx, &corev1.ResolveReportRequest{TayfaId: tid.String(), ReportId: rid.String()})
	if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != "resolution" {
		t.Errorf("çözümsüz: %v", err)
	}
	_, err = srv.GetMyReport(ctx, &corev1.GetMyReportRequest{ReportId: "x"})
	if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != "report_id" {
		t.Errorf("bozuk kimlik: %v", err)
	}
}

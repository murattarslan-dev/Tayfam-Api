package rest

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// ReportClient is core's ReportService as the gateway uses it (issue #76).
type ReportClient interface {
	CreateReport(ctx context.Context, in *corev1.CreateReportRequest, opts ...grpc.CallOption) (*corev1.CreateReportResponse, error)
	GetMyReport(ctx context.Context, in *corev1.GetMyReportRequest, opts ...grpc.CallOption) (*corev1.GetMyReportResponse, error)
	ListMyReports(ctx context.Context, in *corev1.ListMyReportsRequest, opts ...grpc.CallOption) (*corev1.ListMyReportsResponse, error)
	ListTayfaReports(ctx context.Context, in *corev1.ListTayfaReportsRequest, opts ...grpc.CallOption) (*corev1.ListTayfaReportsResponse, error)
	ResolveReport(ctx context.Context, in *corev1.ResolveReportRequest, opts ...grpc.CallOption) (*corev1.ResolveReportResponse, error)
}

const (
	// maxReportBody fits a 1000-character description and a 2000-character
	// evidence text in the worst JSON escaping (\uXXXX surrogate pairs).
	maxReportBody        = 48 << 10
	reportDescriptionMax = 1000
	reportEvidenceMax    = 2000
)

// registerReports serves the report routes. The reporter is always the
// caller of the verified token; no request names them, and no response of
// the moderation queue carries them (core's contract has no such field).
func registerReports(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/reports", a(h.createReport))
	mux.HandleFunc("GET /v1/tayfas/{tayfa_id}/reports", a(h.listTayfaReports))
	mux.HandleFunc("POST /v1/tayfas/{tayfa_id}/reports/{report_id}/resolution", a(h.resolveReport))
	mux.HandleFunc("GET /v1/me/reports", a(h.listMyReports))
	// Reports that are not about a tayfa: direct messages (issue #95).
	mux.HandleFunc("POST /v1/me/reports", a(h.createDirectReport))
	mux.HandleFunc("GET /v1/me/reports/{report_id}", a(h.getMyReport))
}

var (
	reportTargets = newEnum(map[string]corev1.ReportTargetType{
		"user": corev1.ReportTargetType_REPORT_TARGET_TYPE_USER, "tayfa": corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA,
		"announcement": corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT, "event": corev1.ReportTargetType_REPORT_TARGET_TYPE_EVENT,
		"question": corev1.ReportTargetType_REPORT_TARGET_TYPE_QUESTION, "answer": corev1.ReportTargetType_REPORT_TARGET_TYPE_ANSWER,
		"poll": corev1.ReportTargetType_REPORT_TARGET_TYPE_POLL, "fishing_catch": corev1.ReportTargetType_REPORT_TARGET_TYPE_FISHING_CATCH,
		"dm_message": corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE,
	})
	reportReasons = newEnum(map[string]corev1.ReportReason{
		"harassment":         corev1.ReportReason_REPORT_REASON_HARASSMENT,
		"hate_speech":        corev1.ReportReason_REPORT_REASON_HATE_SPEECH,
		"violence":           corev1.ReportReason_REPORT_REASON_VIOLENCE,
		"privacy":            corev1.ReportReason_REPORT_REASON_PRIVACY,
		"impersonation":      corev1.ReportReason_REPORT_REASON_IMPERSONATION,
		"illegal_trade":      corev1.ReportReason_REPORT_REASON_ILLEGAL_TRADE,
		"drugs":              corev1.ReportReason_REPORT_REASON_DRUGS,
		"gambling":           corev1.ReportReason_REPORT_REASON_GAMBLING,
		"dangerous_activity": corev1.ReportReason_REPORT_REASON_DANGEROUS_ACTIVITY,
		"fake_event":         corev1.ReportReason_REPORT_REASON_FAKE_EVENT,
		"sexual_content":     corev1.ReportReason_REPORT_REASON_SEXUAL_CONTENT,
		"child_safety":       corev1.ReportReason_REPORT_REASON_CHILD_SAFETY,
		"spam":               corev1.ReportReason_REPORT_REASON_SPAM,
		"copyright":          corev1.ReportReason_REPORT_REASON_COPYRIGHT,
		"staff_conduct":      corev1.ReportReason_REPORT_REASON_STAFF_CONDUCT,
		"other":              corev1.ReportReason_REPORT_REASON_OTHER,
	})
	reportStatuses = newEnum(map[string]corev1.ReportStatus{
		"open": corev1.ReportStatus_REPORT_STATUS_OPEN, "resolved": corev1.ReportStatus_REPORT_STATUS_RESOLVED,
	})
	reportResolutions = newEnum(map[string]corev1.ReportResolution{
		"content_removed":   corev1.ReportResolution_REPORT_RESOLUTION_CONTENT_REMOVED,
		"member_sanctioned": corev1.ReportResolution_REPORT_RESOLUTION_MEMBER_SANCTIONED,
		"dismissed":         corev1.ReportResolution_REPORT_RESOLUTION_DISMISSED,
	})
	reportPriorities = newEnum(map[string]corev1.ReportPriority{
		"normal": corev1.ReportPriority_REPORT_PRIORITY_NORMAL, "minor_involved": corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED,
	})
	reportOutcomes = newEnum(map[string]corev1.ReportOutcome{
		"action_taken": corev1.ReportOutcome_REPORT_OUTCOME_ACTION_TAKEN, "dismissed": corev1.ReportOutcome_REPORT_OUTCOME_DISMISSED,
	})
)

type myReportJSON struct {
	ID string `json:"id"`
	// Absent for a direct message report.
	TayfaID     string `json:"tayfa_id,omitempty"`
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	Reason      string `json:"reason"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	// Resolved reports only.
	Outcome    string     `json:"outcome,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func myReportFrom(m *corev1.MyReport) myReportJSON {
	out := myReportJSON{
		ID: m.GetId(), TayfaID: m.GetTayfaId(), TargetType: reportTargets.name(m.GetTargetType()), TargetID: m.GetTargetId(),
		Reason: reportReasons.name(m.GetReason()), Description: m.GetDescription(), Status: reportStatuses.name(m.GetStatus()),
		CreatedAt: m.GetCreateTime().AsTime(), ResolvedAt: optTime(m.GetResolveTime()),
	}
	if m.GetOutcome() != corev1.ReportOutcome_REPORT_OUTCOME_UNSPECIFIED {
		out.Outcome = reportOutcomes.name(m.GetOutcome())
	}
	return out
}

// modReportJSON has no field for the reporter, the escalation or anyone's
// age. The priority is shown (ADR-0012 as updated on 2026-11-09).
type modReportJSON struct {
	ID            string `json:"id"`
	TayfaID       string `json:"tayfa_id"`
	TargetType    string `json:"target_type"`
	TargetID      string `json:"target_id"`
	SubjectUserID string `json:"subject_user_id,omitempty"`
	Reason        string `json:"reason"`
	Description   string `json:"description,omitempty"`
	Priority      string `json:"priority"`
	Status        string `json:"status"`
	// Resolved reports only.
	Resolution      string     `json:"resolution,omitempty"`
	HandledByUserID string     `json:"handled_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

func modReportFrom(m *corev1.ModerationReport) modReportJSON {
	out := modReportJSON{
		ID: m.GetId(), TayfaID: m.GetTayfaId(), TargetType: reportTargets.name(m.GetTargetType()), TargetID: m.GetTargetId(),
		SubjectUserID: m.GetSubjectUserId(), Reason: reportReasons.name(m.GetReason()), Description: m.GetDescription(),
		Priority: reportPriorities.name(m.GetPriority()), Status: reportStatuses.name(m.GetStatus()),
		HandledByUserID: m.GetHandledByUserId(),
		CreatedAt:       m.GetCreateTime().AsTime(), ResolvedAt: optTime(m.GetResolveTime()),
	}
	if m.GetResolution() != corev1.ReportResolution_REPORT_RESOLUTION_UNSPECIFIED {
		out.Resolution = reportResolutions.name(m.GetResolution())
	}
	return out
}

type myReportListJSON struct {
	Reports       []myReportJSON `json:"reports"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

type modReportListJSON struct {
	Reports       []modReportJSON `json:"reports"`
	NextPageToken string          `json:"next_page_token,omitempty"`
}

type reportCreateBody struct {
	TargetType   string `json:"target_type"`
	TargetID     string `json:"target_id"`
	Reason       string `json:"reason"`
	Description  string `json:"description"`
	EvidenceText string `json:"evidence_text"`
}

// createReport files a report about something in a tayfa.
func (h coreHandlers) createReport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	h.fileReport(ctx, w, r, id)
}

// createDirectReport files a report that is not about a tayfa: a direct
// message (issue #95) or a user from their profile (issue #187). It goes to
// the platform only.
func (h coreHandlers) createDirectReport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	h.fileReport(ctx, w, r, "")
}

// fileReport checks the body; tayfaID is empty for direct message reports,
// which alone carry evidence_text, and for user reports from a profile.
func (h coreHandlers) fileReport(ctx context.Context, w http.ResponseWriter, r *http.Request, tayfaID string) {
	var b reportCreateBody
	if !decodeLimit(w, r, &b, maxReportBody) {
		return
	}
	tt, okT := reportTargets.value(b.TargetType)
	reason, okR := reportReasons.value(b.Reason)
	direct := tt == corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE
	// Without a tayfa: a direct message or a user from their profile
	// (issue #187); a user may also be reported from a tayfa.
	noTayfaOK := direct || tt == corev1.ReportTargetType_REPORT_TARGET_TYPE_USER
	evidence := utf8.RuneCountInString(strings.TrimSpace(b.EvidenceText))
	if !okT || !okR || !validUUID(b.TargetID) || utf8.RuneCountInString(strings.TrimSpace(b.Description)) > reportDescriptionMax ||
		(tayfaID == "" && !noTayfaOK) || (direct && tayfaID != "") ||
		(direct && (evidence < 1 || evidence > reportEvidenceMax)) || (!direct && b.EvidenceText != "") {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Reports.CreateReport(ctx, &corev1.CreateReportRequest{
		TayfaId: tayfaID, TargetType: tt, TargetId: b.TargetID, Reason: reason, Description: b.Description, EvidenceText: b.EvidenceText,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rep := myReportFrom(resp.GetReport())
	if !resp.GetCreated() {
		writeJSON(w, http.StatusOK, rep)
		return
	}
	w.Header().Set("Location", "/v1/me/reports/"+rep.ID)
	writeJSON(w, http.StatusCreated, rep)
}

func (h coreHandlers) listTayfaReports(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	status, ok := reportStatuses.valueOrUnspecified(r.URL.Query().Get("status"))
	if !ok {
		badRequest(w, r)
		return
	}
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Reports.ListTayfaReports(ctx, &corev1.ListTayfaReportsRequest{TayfaId: id, Status: status, PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := modReportListJSON{Reports: make([]modReportJSON, 0, len(resp.GetReports())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetReports() {
		out.Reports = append(out.Reports, modReportFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

type reportResolveBody struct {
	Resolution string `json:"resolution"`
}

func (h coreHandlers) resolveReport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tayfa_id", "report_id")
	if !ok {
		return
	}
	var b reportResolveBody
	if !decode(w, r, &b) {
		return
	}
	res, ok := reportResolutions.value(b.Resolution)
	if !ok {
		badRequest(w, r)
		return
	}
	resp, err := h.d.Reports.ResolveReport(ctx, &corev1.ResolveReportRequest{TayfaId: ids[0], ReportId: ids[1], Resolution: res})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, modReportFrom(resp.GetReport()))
}

func (h coreHandlers) listMyReports(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	size, token, ok := paging(w, r)
	if !ok {
		return
	}
	resp, err := h.d.Reports.ListMyReports(ctx, &corev1.ListMyReportsRequest{PageSize: size, PageToken: token})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := myReportListJSON{Reports: make([]myReportJSON, 0, len(resp.GetReports())), NextPageToken: resp.GetNextPageToken()}
	for _, m := range resp.GetReports() {
		out.Reports = append(out.Reports, myReportFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) getMyReport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "report_id")
	if !ok {
		return
	}
	resp, err := h.d.Reports.GetMyReport(ctx, &corev1.GetMyReportRequest{ReportId: id})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, myReportFrom(resp.GetReport()))
}

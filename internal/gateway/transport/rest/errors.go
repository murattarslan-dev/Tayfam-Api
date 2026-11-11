package rest

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
)

type problem struct {
	status int
	code   string
}

// problemsByDomain maps each service's ErrorInfo reasons to problem types.
// A reason is only trusted together with its service's domain, so one
// service cannot produce another's problems.
var problemsByDomain = map[string]map[string]problem{
	"identity.tayfam.app": {
		"ERROR_REASON_INVALID_CREDENTIALS":     {http.StatusUnauthorized, "invalid-credentials"},
		"ERROR_REASON_UNSUPPORTED_PROVIDER":    {http.StatusBadRequest, "unsupported-provider"},
		"ERROR_REASON_AGE_BLOCKED":             {http.StatusForbidden, "age-blocked"},
		"ERROR_REASON_INVALID_REFRESH":         {http.StatusUnauthorized, "invalid-refresh"},
		"ERROR_REASON_REFRESH_CONFLICT":        {http.StatusConflict, "refresh-conflict"},
		"ERROR_REASON_INVALID_BIRTH_DATE":      {http.StatusBadRequest, "invalid-birth-date"},
		"ERROR_REASON_CONSENT_OUTDATED":        {http.StatusConflict, "consent-outdated"},
		"ERROR_REASON_ALREADY_REGISTERED":      {http.StatusConflict, "already-registered"},
		"ERROR_REASON_INVALID_DISPLAY_NAME":    {http.StatusBadRequest, "invalid-display-name"},
		"ERROR_REASON_RATE_LIMITED":            {http.StatusTooManyRequests, "rate-limited"},
		"ERROR_REASON_REFERRAL_CODE_NOT_FOUND": {http.StatusNotFound, "referral-code-not-found"},
		// Account management (issue #75).
		"ERROR_REASON_REAUTHENTICATION_REQUIRED": {http.StatusForbidden, "reauthentication-required"},
		"ERROR_REASON_SESSION_NOT_FOUND":         {http.StatusNotFound, "session-not-found"},
		// Platform sanction (issue #104).
		"ERROR_REASON_ACCOUNT_SANCTIONED": {http.StatusForbidden, "account-sanctioned"},
		"ERROR_REASON_REASON_REQUIRED":    {http.StatusBadRequest, "reason-required"},
		"ERROR_REASON_USER_NOT_FOUND":     {http.StatusNotFound, "user-not-found"},
		"ERROR_REASON_SANCTION_CONFLICT":  {http.StatusConflict, "sanction-conflict"},
		"ERROR_REASON_VALIDATION":         {http.StatusBadRequest, "invalid-request"},
	},
	// proto/tayfam/core/v1/errors.proto
	"core.tayfam.app": {
		"ERROR_REASON_VALIDATION":                  {http.StatusBadRequest, "invalid-request"},
		"ERROR_REASON_TAYFA_NOT_FOUND":             {http.StatusNotFound, "tayfa-not-found"},
		"ERROR_REASON_PERMISSION_DENIED":           {http.StatusForbidden, "permission-denied"},
		"ERROR_REASON_MUTED":                       {http.StatusForbidden, "muted"},
		"ERROR_REASON_TAYFA_READ_ONLY":             {http.StatusConflict, "tayfa-read-only"},
		"ERROR_REASON_TAYFA_CREATE_LIMIT":          {http.StatusTooManyRequests, "tayfa-create-limit"},
		"ERROR_REASON_RATE_LIMITED":                {http.StatusTooManyRequests, "rate-limited"},
		"ERROR_REASON_INVALID_JOIN_CODE":           {http.StatusNotFound, "invalid-join-code"},
		"ERROR_REASON_INVITE_INVALID":              {http.StatusNotFound, "invite-invalid"},
		"ERROR_REASON_INVITE_EXPIRED":              {http.StatusGone, "invite-expired"},
		"ERROR_REASON_INVITE_REVOKED":              {http.StatusGone, "invite-revoked"},
		"ERROR_REASON_INVITE_EXHAUSTED":            {http.StatusGone, "invite-exhausted"},
		"ERROR_REASON_INVITE_NOT_FOUND":            {http.StatusNotFound, "invite-not-found"},
		"ERROR_REASON_ALREADY_MEMBER":              {http.StatusConflict, "already-member"},
		"ERROR_REASON_ALREADY_PENDING":             {http.StatusConflict, "already-pending"},
		"ERROR_REASON_BANNED":                      {http.StatusForbidden, "banned"},
		"ERROR_REASON_MEMBER_NOT_FOUND":            {http.StatusNotFound, "member-not-found"},
		"ERROR_REASON_ROLE_HIERARCHY":              {http.StatusForbidden, "role-hierarchy"},
		"ERROR_REASON_LAST_ADMIN":                  {http.StatusConflict, "last-admin"},
		"ERROR_REASON_DELETION_ALREADY_REQUESTED":  {http.StatusConflict, "deletion-already-requested"},
		"ERROR_REASON_DELETION_NOT_REQUESTED":      {http.StatusConflict, "deletion-not-requested"},
		"ERROR_REASON_MODULE_DISABLED":             {http.StatusConflict, "module-disabled"},
		"ERROR_REASON_ANNOUNCEMENT_NOT_FOUND":      {http.StatusNotFound, "announcement-not-found"},
		"ERROR_REASON_EVENT_NOT_FOUND":             {http.StatusNotFound, "event-not-found"},
		"ERROR_REASON_EVENT_CANCELLED":             {http.StatusConflict, "event-cancelled"},
		"ERROR_REASON_EVENT_ENDED":                 {http.StatusConflict, "event-ended"},
		"ERROR_REASON_EVENT_NOT_GOING":             {http.StatusConflict, "event-not-going"},
		"ERROR_REASON_EVENT_ITEM_NOT_FOUND":        {http.StatusNotFound, "event-item-not-found"},
		"ERROR_REASON_EVENT_LIMIT":                 {http.StatusConflict, "event-limit"},
		"ERROR_REASON_EVENT_TYPE_MISMATCH":         {http.StatusConflict, "event-type-mismatch"},
		"ERROR_REASON_CHECK_IN_CLOSED":             {http.StatusConflict, "check-in-closed"},
		"ERROR_REASON_CHECK_IN_TOO_FAR":            {http.StatusConflict, "check-in-too-far"},
		"ERROR_REASON_CHECK_IN_ATTEMPTS_EXHAUSTED": {http.StatusTooManyRequests, "check-in-attempts-exhausted"},
		"ERROR_REASON_CORRECTION_CLOSED":           {http.StatusConflict, "correction-closed"},
		"ERROR_REASON_HIKE_NOT_STARTED":            {http.StatusConflict, "hike-not-started"},
		"ERROR_REASON_NOT_ATTENDED":                {http.StatusConflict, "not-attended"},
		"ERROR_REASON_EVENT_SERIES_NOT_FOUND":      {http.StatusNotFound, "event-series-not-found"},
		"ERROR_REASON_EVENT_SERIES_ENDED":          {http.StatusConflict, "event-series-ended"},
		"ERROR_REASON_QUESTION_NOT_FOUND":          {http.StatusNotFound, "question-not-found"},
		"ERROR_REASON_ANSWER_NOT_FOUND":            {http.StatusNotFound, "answer-not-found"},
		"ERROR_REASON_TAG_NOT_FOUND":               {http.StatusNotFound, "tag-not-found"},
		"ERROR_REASON_OWN_ANSWER_VOTE":             {http.StatusConflict, "own-answer-vote"},
		"ERROR_REASON_TAG_LIMIT":                   {http.StatusConflict, "tag-limit"},
		"ERROR_REASON_POLL_NOT_FOUND":              {http.StatusNotFound, "poll-not-found"},
		"ERROR_REASON_POLL_CLOSED":                 {http.StatusConflict, "poll-closed"},
		"ERROR_REASON_POLL_ANONYMOUS":              {http.StatusForbidden, "poll-anonymous"},
		"ERROR_REASON_USER_NOT_FOUND":              {http.StatusNotFound, "user-not-found"},
		"ERROR_REASON_FRIEND_REQUEST_LIMIT":        {http.StatusTooManyRequests, "friend-request-limit"},
		"ERROR_REASON_FRIEND_REQUEST_NOT_FOUND":    {http.StatusNotFound, "friend-request-not-found"},
		"ERROR_REASON_ALREADY_FRIENDS":             {http.StatusConflict, "already-friends"},
		"ERROR_REASON_FRIEND_REQUEST_PENDING":      {http.StatusConflict, "friend-request-pending"},
		"ERROR_REASON_NOT_FRIENDS":                 {http.StatusConflict, "not-friends"},
		"ERROR_REASON_DIRECT_INVITE_LIMIT":         {http.StatusTooManyRequests, "direct-invite-limit"},
		"ERROR_REASON_INVITATION_NOT_FOUND":        {http.StatusNotFound, "invitation-not-found"},
		"ERROR_REASON_ALREADY_INVITED":             {http.StatusConflict, "already-invited"},
		"ERROR_REASON_INVITE_NOT_ALLOWED":          {http.StatusConflict, "invite-not-allowed"},
		"ERROR_REASON_REPORT_NOT_FOUND":            {http.StatusNotFound, "report-not-found"},
		"ERROR_REASON_REPORT_TARGET_NOT_FOUND":     {http.StatusNotFound, "report-target-not-found"},
		"ERROR_REASON_REPORT_LIMIT":                {http.StatusTooManyRequests, "report-limit"},
		"ERROR_REASON_REPORT_ALREADY_RESOLVED":     {http.StatusConflict, "report-already-resolved"},
		"ERROR_REASON_REPORT_ACTION_MISSING":       {http.StatusConflict, "report-action-missing"},
		// Platform panel (AdminService, issue #104).
		"ERROR_REASON_REAUTHENTICATION_REQUIRED": {http.StatusForbidden, "reauthentication-required"},
		"ERROR_REASON_REASON_REQUIRED":           {http.StatusBadRequest, "reason-required"},
		"ERROR_REASON_DECISION_NOT_APPLICABLE":   {http.StatusConflict, "decision-not-applicable"},
		"ERROR_REASON_TAYFA_STATE_CONFLICT":      {http.StatusConflict, "tayfa-state-conflict"},
		// Media (MediaService, issue #135).
		"ERROR_REASON_UPLOAD_NOT_FOUND":      {http.StatusNotFound, "upload-not-found"},
		"ERROR_REASON_UPLOAD_NOT_READY":      {http.StatusConflict, "upload-not-ready"},
		"ERROR_REASON_UPLOAD_IN_USE":         {http.StatusConflict, "upload-in-use"},
		"ERROR_REASON_UPLOAD_NOT_RECEIVED":   {http.StatusConflict, "upload-not-received"},
		"ERROR_REASON_UPLOAD_RATE_LIMITED":   {http.StatusTooManyRequests, "upload-rate-limited"},
		"ERROR_REASON_UPLOAD_QUOTA_EXCEEDED": {http.StatusConflict, "upload-quota-exceeded"},
	},
	// proto/tayfam/chat/v1/errors.proto
	"chat.tayfam.app": {
		"ERROR_REASON_VALIDATION":             {http.StatusBadRequest, "invalid-request"},
		"ERROR_REASON_NOTIFICATION_NOT_FOUND": {http.StatusNotFound, "notification-not-found"},
		"ERROR_REASON_TAYFA_NOT_FOUND":        {http.StatusNotFound, "tayfa-not-found"},
		// Channels (issue #81).
		"ERROR_REASON_CHANNEL_NOT_FOUND":  {http.StatusNotFound, "channel-not-found"},
		"ERROR_REASON_MESSAGE_NOT_FOUND":  {http.StatusNotFound, "message-not-found"},
		"ERROR_REASON_PERMISSION_DENIED":  {http.StatusForbidden, "permission-denied"},
		"ERROR_REASON_MUTED":              {http.StatusForbidden, "muted"},
		"ERROR_REASON_CHANNEL_ARCHIVED":   {http.StatusConflict, "channel-archived"},
		"ERROR_REASON_CHANNEL_NAME_TAKEN": {http.StatusConflict, "channel-name-taken"},
		"ERROR_REASON_SLOW_MODE":          {http.StatusTooManyRequests, "slow-mode"},
		"ERROR_REASON_TAYFA_READ_ONLY":    {http.StatusConflict, "tayfa-read-only"},
		"ERROR_REASON_ROLE_HIERARCHY":     {http.StatusForbidden, "role-hierarchy"},
		// Direct messages (issue #95).
		"ERROR_REASON_CONVERSATION_NOT_FOUND":     {http.StatusNotFound, "conversation-not-found"},
		"ERROR_REASON_DIRECT_MESSAGE_NOT_ALLOWED": {http.StatusForbidden, "direct-message-not-allowed"},
		"ERROR_REASON_MESSAGE_REQUEST_PENDING":    {http.StatusConflict, "message-request-pending"},
		"ERROR_REASON_MESSAGE_REQUEST_LIMIT":      {http.StatusTooManyRequests, "message-request-limit"},
		"ERROR_REASON_MESSAGE_REQUEST_NOT_FOUND":  {http.StatusNotFound, "message-request-not-found"},
		// Per-user message rate, channel and direct messages (issue #101).
		"ERROR_REASON_RATE_LIMITED": {http.StatusTooManyRequests, "rate-limited"},
		// End-to-end encryption (issue #127, ADR-0018).
		"ERROR_REASON_E2E_DEVICE_NOT_REGISTERED":     {http.StatusConflict, "e2e-device-not-registered"},
		"ERROR_REASON_E2E_DEVICE_ALREADY_REGISTERED": {http.StatusConflict, "e2e-device-already-registered"},
		"ERROR_REASON_E2E_DEVICE_LIMIT":              {http.StatusConflict, "e2e-device-limit"},
		"ERROR_REASON_E2E_DEVICES_CHANGED":           {http.StatusConflict, "e2e-devices-changed"},
		"ERROR_REASON_E2E_RECIPIENT_UNAVAILABLE":     {http.StatusConflict, "e2e-recipient-unavailable"},
		"ERROR_REASON_E2E_REQUIRED":                  {http.StatusConflict, "e2e-required"},
		"ERROR_REASON_E2E_NOT_PAIRED":                {http.StatusConflict, "e2e-not-paired"},
		"ERROR_REASON_E2E_ALREADY_PAIRED":            {http.StatusConflict, "e2e-already-paired"},
		"ERROR_REASON_E2E_PAIRED_DEVICE_EXISTS":      {http.StatusConflict, "e2e-paired-device-exists"},
		"ERROR_REASON_E2E_HISTORY_KEY_STALE":         {http.StatusConflict, "e2e-history-key-stale"},
		"ERROR_REASON_E2E_LINK_REQUEST_NOT_FOUND":    {http.StatusNotFound, "e2e-link-request-not-found"},
	},
}

// writeUpstreamError turns a gRPC error from service (identity, core, chat) into
// a problem response without passing on any internal detail: only a known
// ErrorInfo reason from that service's own domain, or the bare status code,
// decides the answer.
func writeUpstreamError(w http.ResponseWriter, r *http.Request, log *slog.Logger, service string, err error) {
	st := status.Convert(err)
	domain := service + ".tayfam.app"
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == domain {
			if p, ok := problemsByDomain[domain][info.GetReason()]; ok {
				httpx.WriteProblem(w, r, p.status, p.code, http.StatusText(p.status), "")
				return
			}
		}
	}
	switch st.Code() {
	case codes.Unauthenticated:
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
	case codes.InvalidArgument:
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
	// Unimplemented: the service is older than gateway during a rollout.
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unimplemented:
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
	default:
		log.ErrorContext(r.Context(), "servis çağrısı başarısız", slog.String("service", service), slog.String("code", st.Code().String()))
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal Server Error", "")
	}
}

// clientIP returns Cloudflare's client IP when trusted, else the peer
// address. Used only as a rate limit key, never logged.
func clientIP(r *http.Request, trustCF bool) string {
	if trustCF {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

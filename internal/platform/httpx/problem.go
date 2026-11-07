// Package httpx holds HTTP building blocks shared by services: RFC 9457 error
// responses and middleware.
package httpx

import (
	"encoding/json"
	"net/http"
)

// ProblemTypeBase prefixes machine-readable problem types.
const ProblemTypeBase = "https://tayfam.app/problems/"

// Problem is an RFC 9457 problem details object.
//
// Type is a stable machine-readable code the client maps to its own
// translated text; Title is a short English summary for developers. Detail
// must never contain internal information (stack traces, SQL, other users' data).
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem writes p as application/problem+json. code is appended to ProblemTypeBase.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	p := Problem{
		Type:      ProblemTypeBase + code,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: RequestIDFrom(r.Context()),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// NotFound answers unknown routes with a problem response.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, http.StatusNotFound, "not-found", "Not Found", "")
}

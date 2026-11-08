package rest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRoutesMatchContract keeps the router and api/openapi/gateway.yaml in
// step (CLAUDE.md, rule 7): every operation in the contract is served, and
// unknown paths are not.
func TestRoutesMatchContract(t *testing.T) {
	spec, err := os.ReadFile("../../../../api/openapi/gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/\S+):$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	h := newTestRouter(&fakeIdentity{})
	var path string
	ops := 0
	for line := range strings.SplitSeq(string(spec), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		m := opRe.FindStringSubmatch(line)
		if m == nil || path == "" {
			continue
		}
		ops++
		method := strings.ToUpper(m[1])
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set(clientHeader, "android")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("sözleşmedeki %s %s sunulmuyor (%d)", method, path, w.Code)
		}
	}
	if ops < 6 {
		t.Fatalf("sözleşmede yalnızca %d işlem bulundu; ayrıştırma bozuk olabilir", ops)
	}
}

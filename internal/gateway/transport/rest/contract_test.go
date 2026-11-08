package rest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const specPath = "../../../../api/openapi/gateway.yaml"

// pathParams fill the path templates of the contract with valid values.
var pathParams = strings.NewReplacer(
	"{tayfa_id}", tayfaID, "{user_id}", userID, "{invite_id}", inviteID, "{module}", "events",
)

// TestRoutesMatchContract keeps the router and api/openapi/gateway.yaml in
// step (CLAUDE.md, rule 7): every operation in the contract is served, and
// unknown paths are not.
func TestRoutesMatchContract(t *testing.T) {
	spec, err := os.ReadFile(specPath)
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
		r := httptest.NewRequest(method, pathParams.Replace(path), nil)
		r.Header.Set(clientHeader, "android")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("sözleşmedeki %s %s sunulmuyor (%d)", method, path, w.Code)
		}
	}
	if ops < 30 {
		t.Fatalf("sözleşmede yalnızca %d işlem bulundu; ayrıştırma bozuk olabilir", ops)
	}
}

// TestProblemTypesDocumented checks that every problem type gateway can
// answer with is listed under x-problem-types in the contract.
func TestProblemTypesDocumented(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	inList := false
	itemRe := regexp.MustCompile(`^\s+- ([a-z][a-z-]*)$`)
	for line := range strings.SplitSeq(string(spec), "\n") {
		if strings.TrimSpace(line) == "x-problem-types:" {
			inList = true
			continue
		}
		if !inList || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := itemRe.FindStringSubmatch(line)
		if m == nil {
			break
		}
		documented[m[1]] = true
	}
	if len(documented) < 10 {
		t.Fatalf("x-problem-types ayrıştırılamadı: %v", documented)
	}

	// Codes written directly in this package and in httpx (not-found, internal).
	used := map[string]bool{}
	callRe := regexp.MustCompile(`WriteProblem\(w, r, [^,]+, "([a-z-]+)"`)
	for _, dir := range []string{".", "../../../platform/httpx"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range callRe.FindAllStringSubmatch(string(src), -1) {
				used[m[1]] = true
			}
		}
	}
	for _, reasons := range problemsByDomain {
		for _, p := range reasons {
			used[p.code] = true
		}
	}
	if !used["unauthenticated"] || !used["not-found"] || !used["tayfa-not-found"] {
		t.Fatalf("kullanılan kodlar eksik toplandı: %v", used)
	}
	for code := range used {
		if !documented[code] {
			t.Errorf("problem type %q sözleşmede (x-problem-types) belgelenmemiş", code)
		}
	}
	for code := range documented {
		if !used[code] {
			t.Errorf("sözleşmedeki %q hiçbir yerde kullanılmıyor", code)
		}
	}
}

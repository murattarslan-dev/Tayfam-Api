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
	"{announcement_id}", annID, "{event_id}", eventID, "{item_id}", itemID,
	"{vehicle_id}", vehicleID, "{catch_id}", catchID, "{series_id}", seriesID,
	"{question_id}", questionID, "{answer_id}", answerID, "{tag_id}", tagID,
	"{poll_id}", pollID, "{option_id}", optionID, "{notification_id}", notificationID,
	"{session_id}", sessionID, "{channel_id}", channelID, "{message_id}", messageID,
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

// TestRefsResolve checks that every $ref in the contract points at a
// component that exists under components.<section>. A schema written under
// another top-level key (it happened once under security:) is not a
// component, so its $refs would not resolve.
func TestRefsResolve(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	topRe := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*):`)
	sectionRe := regexp.MustCompile(`^  ([A-Za-z][A-Za-z0-9_]*):\s*$`)
	nameRe := regexp.MustCompile(`^    ([A-Za-z][A-Za-z0-9_]*):`)
	defined := map[string]bool{}
	top, section := "", ""
	for line := range strings.SplitSeq(string(spec), "\n") {
		if m := topRe.FindStringSubmatch(line); m != nil {
			top, section = m[1], ""
			continue
		}
		if top != "components" {
			continue
		}
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if m := nameRe.FindStringSubmatch(line); m != nil && section != "" {
			defined[section+"/"+m[1]] = true
		}
	}
	if !defined["schemas/Problem"] || !defined["parameters/TayfaID"] {
		t.Fatalf("bileşenler ayrıştırılamadı: %d", len(defined))
	}
	refRe := regexp.MustCompile(`\$ref: "#/components/([A-Za-z]+)/([A-Za-z0-9_]+)"`)
	refs := 0
	for _, m := range refRe.FindAllStringSubmatch(string(spec), -1) {
		refs++
		if !defined[m[1]+"/"+m[2]] {
			t.Errorf("çözülemeyen $ref: #/components/%s/%s", m[1], m[2])
		}
	}
	if refs < 100 {
		t.Fatalf("yalnızca %d $ref bulundu; ayrıştırma bozuk olabilir", refs)
	}
	// Every $ref has the quoted form checked above.
	if n := strings.Count(string(spec), "$ref:"); n != refs {
		t.Errorf("%d $ref'ten %d tanesi denetlenebilir biçimde", n, refs)
	}
}

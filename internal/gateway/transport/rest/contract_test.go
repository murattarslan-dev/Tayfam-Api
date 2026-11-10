package rest

import (
	"fmt"
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
	"{conversation_id}", conversationID, "{link_request_id}", linkRequestID,
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
	itemRe := regexp.MustCompile(`^\s+- ([a-z][a-z0-9-]*)$`)
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

// TestSpecWellFormed reads the contract strictly: a key whose value is null,
// an unquoted value holding ": " or a key a Schema Object does not know fails
// the build. "{ description: Gelecekte, en fazla iki yıl ileri. }" is valid
// YAML but reads as the description "Gelecekte" plus a key "en fazla iki
// yıl ileri." whose value is null (#128).
func TestSpecWellFormed(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	c := checkSpec(string(spec))
	for _, e := range c.errs {
		t.Error(e)
	}
	if c.schemas < 500 || c.flows < 500 {
		t.Fatalf("yalnızca %d şema ve %d akış koleksiyonu okundu; okuma bozuk olabilir", c.schemas, c.flows)
	}
}

// TestCheckSpec pins what the strict reader rejects and accepts.
func TestCheckSpec(t *testing.T) {
	const head = "components:\n  schemas:\n    S:\n      type: object\n      properties:\n"
	tests := []struct {
		name, body string
		bad        bool
	}{
		{"tırnaklı virgül", "        a: { type: string, description: \"Bir, iki.\" }\n", false},
		{"tırnaksız virgül", "        a: { type: string, description: Bir, iki. }\n", true},
		{"akışta tırnaksız iki nokta", "        a: { type: string, description: Not: iki }\n", true},
		{"blokta tırnaksız iki nokta", "        a:\n          type: string\n          description: Not: iki\n", true},
		{"blokta virgül", "        a:\n          type: string\n          description: Bir, iki.\n", false},
		{"blokta boş değer", "        a:\n          type: string\n          description:\n", true},
		{"akışta boş değer", "        a: { type: string, format: }\n", true},
		{"bilinmeyen anahtar (blok)", "        a:\n          type: string\n          maxlength: 3\n", true},
		{"bilinmeyen anahtar (akış)", "        a: { type: string, maxlenght: 3 }\n", true},
		{"x- uzantısı", "        a: { type: string, x-note: ok }\n", false},
		{"iç içe şema", "        a: { type: array, items: { type: string, fromat: uuid } }\n", true},
		{"allOf dizisi", "        a:\n          allOf:\n            - $ref: \"#/components/schemas/T\"\n            - type: object\n              requried: [b]\n", true},
		{"özellik adı serbest", "        maxlength: { type: string }\n", false},
		{"blok metin", "        a:\n          type: string\n          description: |\n            Not: iki, üç\n            foo: { bar\n", false},
		{"enum dizisi", "        a: { type: string, enum: [a, \"b, c\"] }\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := checkSpec(head + tt.body)
			if bad := len(c.errs) > 0; bad != tt.bad {
				t.Errorf("hata bekleniyor=%v, bulunan: %v", tt.bad, c.errs)
			}
		})
	}
	paths := "paths:\n  /v1/x:\n    get:\n      parameters:\n        - name: q\n          in: query\n          schema: { type: string, maxLenght: 5 }\n"
	if c := checkSpec(paths); len(c.errs) != 1 {
		t.Errorf("yol parametresindeki şema denetlenmedi: %v", c.errs)
	}
}

// The repository has no YAML library (go.mod) and the contract tests stay
// dependency-free, so the checks below read the spec with a small reader
// for the subset of YAML the contract uses: block mappings and sequences,
// block scalars (| and >), and single-line flow collections. Anything it
// cannot read is reported rather than skipped.

// schemaKeywords are the keys a Schema Object may carry (OpenAPI 3.1, JSON
// Schema 2020-12). x- extensions are accepted separately.
var schemaKeywords = map[string]bool{
	"$ref": true, "$comment": true, "$id": true, "$schema": true, "$defs": true,
	"type": true, "format": true, "title": true, "description": true,
	"enum": true, "const": true, "default": true, "examples": true, "example": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
	"properties": true, "patternProperties": true, "additionalProperties": true,
	"unevaluatedProperties": true, "propertyNames": true, "required": true,
	"minProperties": true, "maxProperties": true, "dependentRequired": true,
	"dependentSchemas": true,
	"items":            true, "prefixItems": true, "unevaluatedItems": true, "contains": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
	"minContains": true, "maxContains": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"contentEncoding": true, "contentMediaType": true, "contentSchema": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true,
	"exclusiveMaximum": true, "multipleOf": true,
	"allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"if": true, "then": true, "else": true,
	"discriminator": true, "xml": true, "externalDocs": true,
}

// childKind says what the value of key is, given the kind of the mapping
// that holds it. Only "schema" mappings have their keys checked; the other
// kinds exist to find the schemas. "seq:<kind>" is a sequence of <kind>.
func childKind(kind, key string) string {
	switch kind {
	case "doc":
		switch key {
		case "paths":
			return "paths"
		case "components":
			return "components"
		}
	case "components":
		switch key {
		case "schemas":
			return "schemaMap"
		case "parameters":
			return "paramMap"
		case "responses":
			return "responseMap"
		case "requestBodies":
			return "requestBodyMap"
		case "headers":
			return "headerMap"
		}
	case "schemaMap":
		return "schema"
	case "paramMap":
		return "param"
	case "responseMap":
		return "response"
	case "requestBodyMap":
		return "requestBody"
	case "headerMap":
		return "header"
	case "paths":
		return "pathItem"
	case "pathItem", "operation":
		switch key {
		case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			if kind == "pathItem" {
				return "operation"
			}
		case "parameters":
			return "seq:param"
		case "requestBody":
			return "requestBody"
		case "responses":
			return "responseMap"
		}
	case "param", "header":
		switch key {
		case "schema":
			return "schema"
		case "content":
			return "content"
		}
	case "requestBody":
		if key == "content" {
			return "content"
		}
	case "response":
		switch key {
		case "content":
			return "content"
		case "headers":
			return "headerMap"
		}
	case "content":
		return "media"
	case "media":
		if key == "schema" {
			return "schema"
		}
	case "schema":
		switch key {
		case "properties", "patternProperties", "$defs", "dependentSchemas":
			return "schemaMap"
		case "items", "additionalProperties", "unevaluatedProperties",
			"propertyNames", "contains", "not", "if", "then", "else",
			"contentSchema", "unevaluatedItems":
			return "schema"
		case "allOf", "anyOf", "oneOf", "prefixItems":
			return "seq:schema"
		}
	}
	return "any"
}

// itemKind is the kind of the items of a sequence of the given kind.
func itemKind(kind string) string {
	if k, ok := strings.CutPrefix(kind, "seq:"); ok {
		return k
	}
	return "any"
}

// specChecker collects problems found in the contract.
type specChecker struct {
	errs    []string
	schemas int // schema mappings whose keys were checked
	flows   int // flow mappings read
}

func (c *specChecker) errorf(line int, format string, args ...any) {
	c.errs = append(c.errs, fmt.Sprintf("gateway.yaml:%d: ", line)+fmt.Sprintf(format, args...))
}

// checkKey checks one key of a mapping of the given kind.
func (c *specChecker) checkKey(line int, kind, key string) {
	if kind != "schema" || schemaKeywords[key] || strings.HasPrefix(key, "x-") {
		return
	}
	c.errorf(line, "şemada tanınmayan anahtar %q", key)
}

type specFrame struct {
	col  int    // column of the key (or of the "- " of a sequence item)
	kind string // kind of the value under this key or item
	item bool
}

// checkSpec reads the contract line by line.
func checkSpec(spec string) *specChecker {
	c := &specChecker{}
	stack := []specFrame{{col: -1, kind: "doc"}}
	blockCol := -1 // inside a block scalar whose key is at this column
	type pending struct {
		line, col int
		key       string
	}
	var empty *pending // key with no inline value, waiting for children
	for i, raw := range strings.Split(spec, "\n") {
		no := i + 1
		text := strings.TrimRight(raw, " \r")
		trimmed := strings.TrimLeft(text, " ")
		indent := len(text) - len(trimmed)
		if strings.HasPrefix(trimmed, "\t") {
			c.errorf(no, "girintide sekme karakteri")
			continue
		}
		if blockCol >= 0 {
			if trimmed == "" || indent > blockCol {
				continue
			}
			blockCol = -1
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		col := indent
		rest := trimmed
		dash := false
		if rest == "-" || strings.HasPrefix(rest, "- ") {
			dash = true
		}
		if empty != nil {
			if col < empty.col || (col == empty.col && !dash) {
				c.errorf(empty.line, "%q anahtarının değeri boş (null)", empty.key)
			}
			empty = nil
		}
		if dash {
			for len(stack) > 1 {
				top := stack[len(stack)-1]
				if top.col > col || (top.col == col && top.item) {
					stack = stack[:len(stack)-1]
					continue
				}
				break
			}
			parent := stack[len(stack)-1]
			ik := itemKind(parent.kind)
			if ik == "schema" {
				c.schemas++
			}
			stack = append(stack, specFrame{col: col, kind: ik, item: true})
			rest = strings.TrimLeft(rest[1:], " ")
			col += len(trimmed) - len(rest)
			if rest == "" {
				c.errorf(no, "boş sıra öğesi")
				continue
			}
			if !looksLikeKey(rest) {
				c.checkValue(no, ik, rest)
				continue
			}
		} else {
			for len(stack) > 1 && stack[len(stack)-1].col >= col {
				stack = stack[:len(stack)-1]
			}
		}
		key, value, ok := splitKey(rest)
		if !ok {
			// Continuation of a multi-line plain scalar; the contract
			// writes those as block scalars.
			c.errorf(no, "anahtar olarak okunamayan satır: %q", trimmed)
			continue
		}
		parent := stack[len(stack)-1]
		c.checkKey(no, parent.kind, key)
		kind := childKind(parent.kind, key)
		stack = append(stack, specFrame{col: col, kind: kind})
		switch {
		case value == "":
			empty = &pending{line: no, col: col, key: key}
			if kind == "schema" {
				c.schemas++
			}
		case value[0] == '|' || value[0] == '>':
			blockCol = col
		default:
			c.checkValue(no, kind, value)
		}
	}
	if empty != nil {
		c.errorf(empty.line, "%q anahtarının değeri boş (null)", empty.key)
	}
	return c
}

// looksLikeKey reports whether s starts a "key: value" entry.
func looksLikeKey(s string) bool {
	if s[0] == '{' || s[0] == '[' {
		return false
	}
	_, _, ok := splitKey(s)
	return ok
}

// splitKey splits "key: value" (or "key:") in block context.
func splitKey(s string) (key, value string, ok bool) {
	if s[0] == '"' || s[0] == '\'' {
		end := quotedEnd(s, 0)
		if end < 0 || end >= len(s) || s[end] != ':' {
			return "", "", false
		}
		key, rest := unquote(s[:end]), s[end+1:]
		if rest != "" && rest[0] != ' ' {
			return "", "", false
		}
		return key, strings.TrimSpace(stripComment(rest)), true
	}
	for j := 0; j < len(s); j++ {
		if s[j] == '#' && j > 0 && s[j-1] == ' ' {
			return "", "", false
		}
		if s[j] == ':' && (j+1 == len(s) || s[j+1] == ' ') {
			return strings.TrimSpace(s[:j]), strings.TrimSpace(stripComment(s[j+1:])), true
		}
	}
	return "", "", false
}

// stripComment drops a trailing " #comment" outside quotes.
func stripComment(s string) string {
	var q byte
	for j := 0; j < len(s); j++ {
		switch {
		case q != 0:
			if s[j] == q {
				if q == '\'' && j+1 < len(s) && s[j+1] == '\'' {
					j++
				} else {
					q = 0
				}
			} else if q == '"' && s[j] == '\\' {
				j++
			}
		case (s[j] == '"' || s[j] == '\'') && (j == 0 || strings.ContainsRune(" [{,:", rune(s[j-1]))):
			q = s[j]
		case s[j] == '#' && (j == 0 || s[j-1] == ' '):
			return s[:j]
		}
	}
	return s
}

// quotedEnd returns the index just past the quoted scalar starting at i,
// or -1 when it is not closed on the line.
func quotedEnd(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch {
		case q == '"' && s[j] == '\\':
			j++
		case s[j] == q:
			if q == '\'' && j+1 < len(s) && s[j+1] == '\'' {
				j++
				continue
			}
			return j + 1
		}
	}
	return -1
}

func unquote(s string) string { return s[1 : len(s)-1] }

// checkValue checks an inline value in block context.
func (c *specChecker) checkValue(line int, kind, v string) {
	switch v[0] {
	case '{', '[':
		p := &flowReader{c: c, line: line, s: v}
		if v[0] == '{' {
			p.mapping(kind)
		} else {
			p.sequence(kind)
		}
		if p.err {
			return
		}
		if rest := strings.TrimSpace(p.s[p.i:]); rest != "" {
			c.errorf(line, "akış koleksiyonundan sonra beklenmeyen metin: %q", rest)
		}
	case '"', '\'':
		if end := quotedEnd(v, 0); end < 0 {
			c.errorf(line, "kapanmayan tırnak: %s", v)
		} else if rest := strings.TrimSpace(v[end:]); rest != "" {
			c.errorf(line, "tırnaktan sonra beklenmeyen metin: %q", rest)
		}
	case '&', '*', '!', '%', '@', '`':
		c.errorf(line, "desteklenmeyen YAML göstergesiyle başlayan değer: %s", v)
	default:
		if strings.Contains(v, ": ") || strings.HasSuffix(v, ":") {
			c.errorf(line, "tırnaksız değerde \": \" var; değeri tırnak içine alın: %s", v)
		}
	}
}

// flowReader reads one single-line flow collection.
type flowReader struct {
	c    *specChecker
	line int
	s    string
	i    int
	err  bool
}

func (p *flowReader) fail(format string, args ...any) {
	if !p.err {
		p.c.errorf(p.line, format, args...)
	}
	p.err = true
}

func (p *flowReader) skip() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *flowReader) peek() byte {
	p.skip()
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

// scalar reads a quoted or plain scalar in flow context. A plain scalar ends
// at , [ ] { } or at a ':' followed by a space or a flow indicator.
func (p *flowReader) scalar() (string, bool) {
	p.skip()
	if p.i >= len(p.s) {
		p.fail("akış koleksiyonu kapanmadan satır bitti")
		return "", false
	}
	if c := p.s[p.i]; c == '"' || c == '\'' {
		end := quotedEnd(p.s, p.i)
		if end < 0 {
			p.fail("kapanmayan tırnak")
			return "", false
		}
		v := unquote(p.s[p.i:end])
		p.i = end
		return v, true
	}
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if strings.IndexByte(",[]{}", c) >= 0 {
			break
		}
		if c == ':' && (p.i+1 == len(p.s) || strings.IndexByte(" ,[]{}", p.s[p.i+1]) >= 0) {
			break
		}
		if c == '#' && p.i > start && p.s[p.i-1] == ' ' {
			p.fail("akış koleksiyonu içinde yorum")
			return "", false
		}
		p.i++
	}
	return strings.TrimSpace(p.s[start:p.i]), true
}

func (p *flowReader) mapping(kind string) {
	p.c.flows++
	if kind == "schema" {
		p.c.schemas++
	}
	p.i++ // {
	if p.peek() == '}' {
		p.i++
		return
	}
	for !p.err {
		key, ok := p.scalar()
		if !ok {
			return
		}
		if key == "" {
			p.fail("akış eşlemesinde boş anahtar")
			return
		}
		if p.peek() != ':' {
			// "{ description: a, b }" reads as description: a and a key
			// "b" whose value is null.
			p.fail("akış eşlemesinde %q anahtarının değeri yok (null); tırnaksız bir değerde virgül olabilir", key)
			return
		}
		p.i++ // :
		p.c.checkKey(p.line, kind, key)
		ck := childKind(kind, key)
		switch p.peek() {
		case '{':
			p.mapping(ck)
		case '[':
			p.sequence(ck)
		case ',', '}', 0:
			p.fail("akış eşlemesinde %q anahtarının değeri boş (null)", key)
			return
		default:
			v, ok := p.scalar()
			if !ok {
				return
			}
			if p.peek() == ':' {
				p.fail("akış eşlemesinde %q değerinde \": \" var; değeri tırnak içine alın", key)
				return
			}
			if v == "" {
				p.fail("akış eşlemesinde %q anahtarının değeri boş (null)", key)
				return
			}
		}
		if p.err {
			return
		}
		switch p.peek() {
		case ',':
			p.i++
			if p.peek() == '}' {
				p.i++
				return
			}
		case '}':
			p.i++
			return
		default:
			p.fail("akış eşlemesinde beklenmeyen karakter %q (sütun %d)", p.peek(), p.i+1)
			return
		}
	}
}

func (p *flowReader) sequence(kind string) {
	p.c.flows++
	ik := itemKind(kind)
	p.i++ // [
	if p.peek() == ']' {
		p.i++
		return
	}
	for !p.err {
		switch p.peek() {
		case '{':
			p.mapping(ik)
		case '[':
			p.sequence("any")
		default:
			if _, ok := p.scalar(); !ok {
				return
			}
			if p.peek() == ':' {
				p.fail("akış dizisinde \": \" içeren tırnaksız öğe; tırnak içine alın")
				return
			}
		}
		if p.err {
			return
		}
		switch p.peek() {
		case ',':
			p.i++
			if p.peek() == ']' {
				p.i++
				return
			}
		case ']':
			p.i++
			return
		default:
			p.fail("akış dizisinde beklenmeyen karakter %q (sütun %d)", p.peek(), p.i+1)
			return
		}
	}
}

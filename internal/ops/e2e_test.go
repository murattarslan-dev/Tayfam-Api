package ops

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The end-to-end test runs the real scripts with throwaway age keys against
// a copy of deploy/ and scripts/. It needs bash, sops, age-keygen, jq and
// kubectl or kustomize; without them it is skipped (the script-only parts
// are covered by scripts/secrets_test.sh in CI).

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "sops", "age-keygen", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	_, k1 := exec.LookPath("kubectl")
	_, k2 := exec.LookPath("kustomize")
	if k1 != nil && k2 != nil {
		t.Skip("kubectl or kustomize not installed")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ageKey creates a throwaway age identity file and returns its public key.
func ageKey(t *testing.T, file string) string {
	t.Helper()
	if out, err := exec.Command("age-keygen", "-o", file).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v %s", err, out)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if pub, ok := strings.CutPrefix(l, "# public key: "); ok {
			return pub
		}
	}
	t.Fatal("no public key")
	return ""
}

// runbookYAML returns the yaml block in docs/runbooks/sops.md whose
// top-level metadata name is name (the same extraction as
// scripts/secrets_test.sh).
func runbookYAML(t *testing.T, name string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), "docs", "runbooks", "sops.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	in, ind, hit := false, 0, false
	var buf strings.Builder
	for sc.Scan() {
		l := sc.Text()
		tr := strings.TrimLeft(l, " ")
		switch {
		case !in && tr == "```yaml":
			in, ind, hit = true, len(l)-len(tr), false
			buf.Reset()
		case in && tr == "```":
			if hit {
				return buf.String()
			}
			in = false
		case in:
			line := l
			if len(line) >= ind {
				line = line[ind:]
			}
			buf.WriteString(line + "\n")
			if line == "  name: "+name {
				hit = true
			}
		}
	}
	t.Fatalf("runbook block %s not found", name)
	return ""
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func decrypt(t *testing.T, file, key string) string {
	t.Helper()
	cmd := exec.Command("sops", "--decrypt", "--extract", `["stringData"]["`+key+`"]`, file)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sops -d %s: %v", file, err)
	}
	return string(out)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEndToEndWithSops(t *testing.T) {
	requireTools(t)
	root := repoRoot(t)
	repo := t.TempDir()
	copyTree(t, filepath.Join(root, "deploy"), filepath.Join(repo, "deploy"))
	copyTree(t, filepath.Join(root, "scripts"), filepath.Join(repo, "scripts"))

	// Throwaway keys: dev and prod each get their own (ADR-0006). The real
	// dev secrets in the copy stay encrypted to the real dev key.
	keys := t.TempDir()
	devPub := ageKey(t, filepath.Join(keys, "dev.txt"))
	prodPub := ageKey(t, filepath.Join(keys, "prod.txt"))
	sopsYAML := "creation_rules:\n" +
		"  - path_regex: ^deploy/apps/prod/secrets/.*\\.sops\\.yaml$\n    encrypted_regex: ^(data|stringData)$\n    age: " + prodPub + "\n" +
		"  - path_regex: ^deploy/apps/dev/secrets/.*\\.sops\\.yaml$\n    encrypted_regex: ^(data|stringData)$\n    age: " + devPub + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte(sopsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	both := filepath.Join(keys, "both.txt")
	if err := os.WriteFile(both, []byte(readFile(t, filepath.Join(keys, "dev.txt"))+readFile(t, filepath.Join(keys, "prod.txt"))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", both)
	// Anything the scripts might spill to a temporary file lands here and
	// is scanned below.
	tmpdir := t.TempDir()
	t.Setenv("TMPDIR", tmpdir)

	e := newTestEnv(t, repo)
	e.login()
	var bodies strings.Builder

	// --- R2 (dev) ---
	account := randHex(t, 16)
	keyID := "KEYID" + randHex(t, 14)
	secret := "SeCrEt" + base64.StdEncoding.EncodeToString([]byte(randHex(t, 24)))
	r2 := url.Values{
		"csrf": {e.csrf()}, "env": {"dev"},
		"account": {account}, "bucket": {"tayfam-dev-private"},
		"key_id": {keyID}, "secret": {secret},
	}
	rec := e.postForm("/r2", r2)
	bodies.WriteString(rec.Body.String())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Tamam") {
		t.Fatalf("r2: %d\n%s", rec.Code, rec.Body.String())
	}
	dev := filepath.Join(repo, "deploy", "apps", "dev")
	f := filepath.Join(dev, "secrets", "core-r2.sops.yaml")
	for k, want := range map[string]string{"account-id": account, "bucket": "tayfam-dev-private", "access-key-id": keyID, "secret-access-key": secret} {
		if got := decrypt(t, f, k); got != want {
			t.Errorf("core-r2 %s mismatch", k)
		}
	}
	if got := readFile(t, filepath.Join(dev, "core-egress-r2.yaml")); got != runbookYAML(t, "core-egress-r2") {
		t.Errorf("core-egress-r2.yaml differs from the runbook:\n%s", got)
	}
	kust := readFile(t, filepath.Join(dev, "kustomization.yaml"))
	for _, l := range []string{"  - secrets/core-r2.sops.yaml\n", "  - core-egress-r2.yaml\n"} {
		if strings.Count(kust, l) != 1 {
			t.Errorf("dev kustomization: %q", l)
		}
	}
	for _, p := range []string{"deploy/apps/dev/secrets/core-r2.sops.yaml", "deploy/apps/dev/core-egress-r2.yaml", "deploy/apps/dev/kustomization.yaml", "dev: başvurulan"} {
		if !strings.Contains(rec.Body.String(), p) {
			t.Errorf("result page lacks %q", p)
		}
	}

	// Running again without rotation is refused; with rotation it
	// re-encrypts and leaves the rest untouched (idempotent).
	rec = e.postForm("/r2", r2)
	bodies.WriteString(rec.Body.String())
	if !strings.Contains(rec.Body.String(), "zaten var") {
		t.Errorf("second run without rotation:\n%s", rec.Body.String())
	}
	r2.Set("rotate", "1")
	rec = e.postForm("/r2", r2)
	bodies.WriteString(rec.Body.String())
	if !strings.Contains(rec.Body.String(), "Tamam") {
		t.Fatalf("rotation:\n%s", rec.Body.String())
	}
	if readFile(t, filepath.Join(dev, "kustomization.yaml")) != kust {
		t.Error("kustomization changed on rotation")
	}

	// --- Push (prod): secret list, egress and the required patch ---
	marker := "FAKEKEY" + randHex(t, 20)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "tayfam-test", "client_email": "push@tayfam-test.iam.gserviceaccount.com",
		"private_key": "-----BEGIN PRIVATE KEY-----\n" + marker + "\n-----END PRIVATE KEY-----\n",
	})
	pretty := append(bytes.ReplaceAll(sa, []byte(","), []byte(",\n  ")), '\n')
	rec = e.postKey("prod", pretty)
	bodies.WriteString(rec.Body.String())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Tamam") {
		t.Fatalf("fcm: %d\n%s", rec.Code, rec.Body.String())
	}
	prod := filepath.Join(repo, "deploy", "apps", "prod")
	got := decrypt(t, filepath.Join(prod, "secrets", "chat-fcm.sops.yaml"), "service-account.json")
	var gotSA, wantSA map[string]string
	if json.Unmarshal([]byte(got), &gotSA) != nil || json.Unmarshal(sa, &wantSA) != nil || gotSA["private_key"] != wantSA["private_key"] || strings.Contains(got, "\n  ") {
		t.Error("chat-fcm: decrypted key differs or is not compact JSON")
	}
	if got := readFile(t, filepath.Join(prod, "chat-egress-internet.yaml")); got != runbookYAML(t, "chat-egress-internet") {
		t.Errorf("chat-egress-internet.yaml differs from the runbook:\n%s", got)
	}
	if got := readFile(t, filepath.Join(prod, "chat-fcm-required.yaml")); got != runbookYAML(t, "chat") {
		t.Errorf("chat-fcm-required.yaml differs from the runbook:\n%s", got)
	}
	pk := readFile(t, filepath.Join(prod, "kustomization.yaml"))
	if !strings.Contains(pk, "  - chat-egress-internet.yaml\n") || !strings.Contains(pk, "  - path: chat-fcm-required.yaml\n") {
		t.Errorf("prod kustomization:\n%s", pk)
	}
	if !strings.Contains(readFile(t, filepath.Join(prod, "secrets", "kustomization.yaml")), "  - chat-fcm.sops.yaml\n") {
		t.Error("prod secret list")
	}
	// prod has no other secrets in the copy, so the check reports them
	// (chat-fcm is now required there).
	if !strings.Contains(rec.Body.String(), "overlay&#39;de tanımlı olmayan") || !strings.Contains(rec.Body.String(), "Windows'ta şimdi silin") {
		t.Errorf("fcm result page:\n%s", rec.Body.String())
	}

	// --- Export key (dev) ---
	rec = e.postForm("/export", url.Values{"csrf": {e.csrf()}, "env": {"dev"}})
	bodies.WriteString(rec.Body.String())
	if !strings.Contains(rec.Body.String(), "Tamam") {
		t.Fatalf("export:\n%s", rec.Body.String())
	}
	v := decrypt(t, filepath.Join(dev, "secrets", "identity-export-key.sops.yaml"), "IDENTITY_DATA_EXPORT_KEYS")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "1:"))
	if !strings.HasPrefix(v, "1:") || err != nil || len(raw) != 32 {
		t.Error("export key format")
	}
	if !strings.Contains(readFile(t, filepath.Join(dev, "kustomization.yaml")), "  - secrets/identity-export-key.sops.yaml\n") {
		t.Error("export key not listed")
	}

	// --- No plaintext anywhere: files, temp dir, log, pages ---
	plain := []string{keyID, secret, marker, strings.TrimPrefix(v, "1:")}
	for _, dir := range []string{repo, tmpdir} {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, _ := os.ReadFile(p)
			for _, s := range plain {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("plaintext found in %s", p)
				}
			}
			return nil
		})
	}
	for _, s := range plain {
		if strings.Contains(e.log.String(), s) || strings.Contains(bodies.String(), s) {
			t.Error("plaintext in log or page")
		}
	}
	if !strings.Contains(e.log.String(), `"op":"r2"`) {
		t.Errorf("operation not logged: %s", e.log.String())
	}
}

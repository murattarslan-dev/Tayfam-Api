package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// scriptTimeout bounds one script run (sops, jq and kustomize are local).
const scriptTimeout = 2 * time.Minute

// maxOutput caps the script output kept for the result page.
const maxOutput = 64 << 10

// Result is what the result page shows after an operation.
type Result struct {
	Op         string
	Env        string
	OK         bool
	Output     string   // script output, plaintext values removed
	Changed    []string // files under deploy/apps/<env> created or modified, repo-relative
	RefsRun    bool
	RefsOK     bool
	RefsOutput string
}

// job is one script run.
type job struct {
	op      string
	env     string
	script  string   // repo-relative path
	args    []string // after the environment
	stdin   []byte   // wiped by run
	extra   []string // extra environment variables (non-secret)
	secrets [][]byte // values removed from the output; wiped by the caller
}

// run executes the job's script in the repository and then
// check-secret-refs.sh for the environment. Plaintext only travels over the
// child's stdin pipe; nothing is written to a temporary file.
func (s *Server) run(ctx context.Context, j job) Result {
	defer wipe(j.stdin)
	res := Result{Op: j.op, Env: j.env}
	dir := filepath.Join(s.cfg.Repo, "deploy", "apps", j.env)

	before, err := snapshot(dir)
	if err != nil {
		res.Output = "deploy/apps/" + j.env + " okunamadı."
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	args := append([]string{j.script, j.env}, j.args...)
	out, runErr := s.exec(ctx, j.stdin, j.extra, args...)
	res.OK = runErr == nil
	res.Output = redact(out, j.secrets)
	if !res.OK && res.Output == "" {
		res.Output = "Betik başarısız oldu."
	}

	after, err := snapshot(dir)
	if err == nil {
		res.Changed = changed(before, after, "deploy/apps/"+j.env)
	}

	if res.OK {
		refs := []string{"scripts/check-secret-refs.sh", j.env}
		if j.env == "dev" {
			// dev's tunnel token is created by hand in the cluster
			// (docs/runbooks/sops.md).
			refs = append(refs, "cloudflared-token")
		}
		out, err := s.exec(ctx, nil, nil, refs...)
		res.RefsRun = true
		res.RefsOK = err == nil
		res.RefsOutput = redact(out, j.secrets)
	}
	s.cfg.Logger.Info("ops işlemi",
		"op", j.op, "env", j.env, "ok", res.OK,
		"changed", len(res.Changed), "refs_ok", res.RefsOK)
	return res
}

// exec runs bash with args in the repository and returns combined output.
// The environment is inherited (PATH, HOME for sops) except FORCE, which
// only the caller may set.
func (s *Server) exec(ctx context.Context, stdin []byte, extra []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bash", args...) // #nosec G204 -- fixed scripts, validated env
	cmd.Dir = s.cfg.Repo
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "FORCE=")
	})
	env = append(env, extra...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%s: %w", args[0], err)
	}
	return out.Bytes(), err
}

// limitedBuffer keeps the first maxOutput bytes written to it.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// redact removes every occurrence of the given values and any line that
// looks like key material from script output. The scripts never print
// values; this is a second line of defence.
func redact(out []byte, secrets [][]byte) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			out = bytes.ReplaceAll(out, v, []byte("[gizlendi]"))
		}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool {
		return strings.Contains(l, "PRIVATE KEY") || strings.Contains(l, "AGE-SECRET-KEY")
	})
	return strings.Join(lines, "\n")
}

// snapshot hashes every regular file under dir (relative path → sha256).
func snapshot(dir string) (map[string][32]byte, error) {
	m := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 -- under deploy/apps/<env>
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		m[filepath.ToSlash(rel)] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", dir, err)
	}
	return m, nil
}

func changed(before, after map[string][32]byte, prefix string) []string {
	var out []string
	for p, h := range after {
		if old, ok := before[p]; !ok || old != h {
			out = append(out, prefix+"/"+p)
		}
	}
	slices.Sort(out)
	return out
}

// validators hold the formats the scripts accept (scripts/new-r2-secret.sh);
// checking them here gives a readable error before anything runs.
type validators struct {
	account, bucket, keyID, secret *regexp.Regexp
}

func newValidators() validators {
	return validators{
		account: regexp.MustCompile(`^[0-9a-f]{32}$`),
		bucket:  regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`),
		keyID:   regexp.MustCompile(`^[A-Za-z0-9]{20,128}$`),
		secret:  regexp.MustCompile(`^[A-Za-z0-9/+=]{32,256}$`),
	}
}

// envReady reports whether sops can encrypt for env: the prod rule in
// .sops.yaml carries a placeholder until docs/runbooks/vds-kurulumu.md
// step e.
func (s *Server) envReady(env string) (bool, string) {
	if _, err := os.Stat(filepath.Join(s.cfg.Repo, "deploy", "apps", env)); err != nil {
		return false, "deploy/apps/" + env + " bulunamadı."
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.Repo, ".sops.yaml"))
	if err != nil {
		return false, ".sops.yaml okunamadı."
	}
	if env == "prod" && bytes.Contains(b, []byte("YER_TUTUCU_PROD_AGE_ACIK_ANAHTARI")) {
		return false, "prod'un age açık anahtarı .sops.yaml'a henüz yazılmamış (docs/runbooks/vds-kurulumu.md, adım e). O adıma kadar prod secret'ı üretilemez."
	}
	return true, ""
}

// missingTools lists required programs that are not on PATH.
func missingTools() []string {
	var miss []string
	for _, t := range []string{"bash", "sops", "jq"} {
		if _, err := exec.LookPath(t); err != nil {
			miss = append(miss, t)
		}
	}
	_, k1 := exec.LookPath("kubectl")
	_, k2 := exec.LookPath("kustomize")
	if k1 != nil && k2 != nil {
		miss = append(miss, "kubectl (ya da kustomize)")
	}
	return miss
}

// ExplorerOpener opens a WSL directory in Windows Explorer
// (wslpath -w + explorer.exe). It makes no network request.
func ExplorerOpener(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	win, err := exec.CommandContext(ctx, "wslpath", "-w", dir).Output() // #nosec G204 -- fixed program, path built by the tool
	if err != nil {
		return fmt.Errorf("wslpath: %w", err)
	}
	return WindowsOpen(ctx, strings.TrimSpace(string(win)))
}

// WindowsOpen hands target (a Windows path or an http://127.0.0.1 link) to
// explorer.exe, which opens it with its default program.
func WindowsOpen(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "explorer.exe", target).Run() // #nosec G204 -- fixed program
	// explorer.exe exits with 1 even on success; only a failure to start
	// it is an error.
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("explorer.exe: %w", err)
	}
	return nil
}

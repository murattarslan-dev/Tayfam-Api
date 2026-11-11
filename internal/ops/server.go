package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed templates/*.html static/style.css
var assets embed.FS

const (
	cookieName = "tayfam_ops"
	// Form bodies: the R2 form is a few hundred bytes; a Google service
	// account key is about 2.4 KB.
	maxFormBody = 8 << 10
	maxKeyFile  = 32 << 10
)

// Config holds the server's dependencies.
type Config struct {
	Repo   string // repository root (absolute)
	Port   int    // the port Listen picked
	Logger *slog.Logger
	// OpenDir opens a directory for the user (ExplorerOpener on WSL).
	OpenDir func(ctx context.Context, dir string) error
}

// Server is the Tayfam Ops web UI.
type Server struct {
	cfg     Config
	tmpl    *template.Template
	valid   validators
	launch  []byte // one-time token from the start-up link; nil once used
	session []byte // cookie value
	csrf    []byte // form token
	mu      sync.Mutex
	op      sync.Mutex // one script run at a time
	last    atomic.Int64
}

// New creates a server with fresh random tokens.
func New(cfg Config) (*Server, error) {
	if !filepath.IsAbs(cfg.Repo) {
		return nil, errors.New("ops: repo path must be absolute")
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{"nextData": nextData}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("ops: templates: %w", err)
	}
	s := &Server{cfg: cfg, tmpl: tmpl, valid: newValidators()}
	for _, p := range []*[]byte{&s.launch, &s.session, &s.csrf} {
		*p = make([]byte, 32)
		if _, err := rand.Read(*p); err != nil {
			return nil, fmt.Errorf("ops: random: %w", err)
		}
	}
	s.touch()
	return s, nil
}

// LaunchURL is the start-up link. Its token works once and is exchanged for
// an HttpOnly cookie; a copy left in browser history is useless.
func (s *Server) LaunchURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", s.cfg.Port, hex.EncodeToString(s.launch))
}

// Idle reports how long ago the last request arrived.
func (s *Server) Idle() time.Duration {
	return time.Since(time.Unix(0, s.last.Load()))
}

func (s *Server) touch() { s.last.Store(time.Now().UnixNano()) }

// Handler returns the HTTP handler with every protection applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/style.css", s.style)
	mux.Handle("GET /{$}", s.auth(http.HandlerFunc(s.home)))
	mux.Handle("GET /r2", s.auth(s.page("r2")))
	mux.Handle("GET /fcm", s.auth(s.page("fcm")))
	mux.Handle("GET /export", s.auth(s.page("export")))
	mux.Handle("POST /r2", s.auth(s.post(http.HandlerFunc(s.postR2))))
	mux.Handle("POST /fcm", s.auth(s.post(http.HandlerFunc(s.postFCM))))
	mux.Handle("POST /export", s.auth(s.post(http.HandlerFunc(s.postExport))))
	mux.Handle("POST /open", s.auth(s.post(http.HandlerFunc(s.postOpen))))
	return s.headers(s.host(mux))
}

// headers sets security headers on every response.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// host refuses requests whose Host is not this tool's loopback address
// (DNS rebinding).
func (s *Server) host(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "geçersiz Host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedHost(h string) bool {
	p := strconv.Itoa(s.cfg.Port)
	return h == "127.0.0.1:"+p || h == "localhost:"+p
}

// auth accepts the session cookie, or exchanges the one-time launch token
// for it.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(cookieName); err == nil && equalHex(c.Value, s.session) {
			next.ServeHTTP(w, r)
			return
		}
		if t := r.URL.Query().Get("t"); r.Method == http.MethodGet && r.URL.Path == "/" && t != "" {
			s.mu.Lock()
			ok := s.launch != nil && equalHex(t, s.launch)
			if ok {
				s.launch = nil
			}
			s.mu.Unlock()
			if ok {
				http.SetCookie(w, &http.Cookie{
					Name: cookieName, Value: hex.EncodeToString(s.session), Path: "/",
					// Browsers treat http://127.0.0.1 and http://localhost as
					// secure contexts, so Secure cookies work there.
					HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
				})
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		s.render(w, http.StatusForbidden, "denied", nil)
	})
}

// post checks that a state-changing request comes from this tool's own page.
func (s *Server) post(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		p := strconv.Itoa(s.cfg.Port)
		if o != "http://127.0.0.1:"+p && o != "http://localhost:"+p {
			s.render(w, http.StatusForbidden, "denied", nil)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			s.render(w, http.StatusForbidden, "denied", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func equalHex(got string, want []byte) bool {
	b, err := hex.DecodeString(got)
	return err == nil && len(want) > 0 && subtle.ConstantTimeCompare(b, want) == 1
}

func (s *Server) csrfOK(v []byte) bool {
	return equalHex(string(v), s.csrf)
}

// view is the data every page gets.
type view struct {
	CSRF    string
	Env     string
	Ready   bool
	Note    string
	Missing []string
	Error   string
	Result  *Result
}

func (s *Server) view(env string) view {
	v := view{CSRF: hex.EncodeToString(s.csrf), Env: env, Missing: missingTools()}
	v.Ready, v.Note = s.envReady(env)
	return v
}

// next is the data of the "next step" block on a result page.
type next struct{ Env, Deploy, LogLine string }

func nextData(env, deploy, logLine string) next { return next{env, deploy, logLine} }

func envParam(v string) (string, bool) {
	switch v {
	case "", "dev":
		return "dev", true
	case "prod":
		return "prod", true
	}
	return "", false
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	env, ok := envParam(r.URL.Query().Get("env"))
	if !ok {
		http.Error(w, "geçersiz ortam", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, "home", s.view(env))
}

func (s *Server) page(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env, ok := envParam(r.URL.Query().Get("env"))
		if !ok {
			http.Error(w, "geçersiz ortam", http.StatusBadRequest)
			return
		}
		s.render(w, http.StatusOK, name, s.view(env))
	})
}

func (s *Server) style(w http.ResponseWriter, _ *http.Request) {
	b, err := assets.ReadFile("static/style.css")
	if err != nil {
		http.Error(w, "yok", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) render(w http.ResponseWriter, code int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.cfg.Logger.Error("şablon", "name", name, "err", err)
		http.Error(w, "iç hata", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// readForm reads a urlencoded body into wipeable byte slices and checks the
// CSRF token and the environment.
func (s *Server) readForm(w http.ResponseWriter, r *http.Request) (map[string][]byte, string, bool) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/x-www-form-urlencoded" {
		http.Error(w, "geçersiz form", http.StatusUnsupportedMediaType)
		return nil, "", false
	}
	body, err := readBody(http.MaxBytesReader(w, r.Body, maxFormBody), maxFormBody)
	if err != nil {
		http.Error(w, "geçersiz form", http.StatusBadRequest)
		return nil, "", false
	}
	f, err := parseForm(body)
	wipe(body)
	if err != nil {
		http.Error(w, "geçersiz form", http.StatusBadRequest)
		return nil, "", false
	}
	if !s.csrfOK(f["csrf"]) {
		wipeForm(f)
		s.render(w, http.StatusForbidden, "denied", nil)
		return nil, "", false
	}
	env, ok := envParam(string(f["env"]))
	if !ok || len(f["env"]) == 0 {
		wipeForm(f)
		http.Error(w, "geçersiz ortam", http.StatusBadRequest)
		return nil, "", false
	}
	return f, env, true
}

// start takes the operation lock and checks the environment; it renders the
// form again with an error when the operation cannot run.
func (s *Server) start(w http.ResponseWriter, page, env string) bool {
	if ok, note := s.envReady(env); !ok {
		v := s.view(env)
		v.Error = note
		s.render(w, http.StatusConflict, page, v)
		return false
	}
	if !s.op.TryLock() {
		v := s.view(env)
		v.Error = "Başka bir işlem sürüyor; bitmesini bekleyin."
		s.render(w, http.StatusConflict, page, v)
		return false
	}
	return true
}

func (s *Server) formError(w http.ResponseWriter, page, env, msg string) {
	v := s.view(env)
	v.Error = msg
	s.render(w, http.StatusUnprocessableEntity, page, v)
}

func (s *Server) result(w http.ResponseWriter, page string, res Result) {
	v := s.view(res.Env)
	v.Result = &res
	s.render(w, http.StatusOK, page, v)
}

// postR2 runs scripts/new-r2-secret.sh with the four values on stdin.
func (s *Server) postR2(w http.ResponseWriter, r *http.Request) {
	f, env, ok := s.readForm(w, r)
	if !ok {
		return
	}
	defer wipeForm(f)
	fields := []struct {
		name, label string
		re          interface{ Match([]byte) bool }
	}{
		{"account", "Account ID (32 karakter, 0-9 a-f)", s.valid.account},
		{"bucket", "Bucket adı", s.valid.bucket},
		{"key_id", "Access Key ID", s.valid.keyID},
		{"secret", "Secret Access Key", s.valid.secret},
	}
	var stdin []byte
	var secrets [][]byte
	for _, fl := range fields {
		v := bytes.TrimSpace(f[fl.name])
		if !fl.re.Match(v) {
			wipe(stdin)
			s.formError(w, "r2", env, fl.label+" biçimi beklenmedik (boş, eksik kopyalanmış ya da fazladan karakter var).")
			return
		}
		stdin = append(stdin, v...)
		stdin = append(stdin, '\n')
		secrets = append(secrets, v)
	}
	var extra []string
	if string(f["rotate"]) == "1" {
		extra = append(extra, "FORCE=1")
	}
	if !s.start(w, "r2", env) {
		wipe(stdin)
		return
	}
	defer s.op.Unlock()
	res := s.run(r.Context(), job{
		op: "r2", env: env, script: "scripts/new-r2-secret.sh",
		stdin: stdin, extra: extra, secrets: secrets,
	})
	s.result(w, "r2", res)
}

// postFCM reads the uploaded service account key into memory (never to a
// temporary file: multipart.Reader is read part by part instead of
// ParseMultipartForm) and runs scripts/new-chat-fcm-secret.sh with "-".
func (s *Server) postFCM(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxKeyFile+4<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "geçersiz form", http.StatusBadRequest)
		return
	}
	// The CSRF token must come first so that nothing else is read for a
	// forged request.
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "csrf" {
		s.render(w, http.StatusForbidden, "denied", nil)
		return
	}
	tok, err := readBody(part, 128)
	if err != nil || !s.csrfOK(tok) {
		s.render(w, http.StatusForbidden, "denied", nil)
		return
	}
	var env string
	var key []byte
	defer func() { wipe(key) }()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, "geçersiz form", http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "env":
			b, err := readBody(part, 16)
			if err != nil {
				http.Error(w, "geçersiz form", http.StatusBadRequest)
				return
			}
			env = string(b)
		case "key":
			if key != nil {
				http.Error(w, "geçersiz form", http.StatusBadRequest)
				return
			}
			key, err = readBody(part, maxKeyFile)
			if err != nil {
				http.Error(w, "dosya çok büyük ya da okunamadı", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "geçersiz form", http.StatusBadRequest)
			return
		}
	}
	env, ok := envParam(env)
	if !ok || env == "" {
		http.Error(w, "geçersiz ortam", http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(key)) == 0 {
		s.formError(w, "fcm", env, "Servis hesabı JSON dosyasını seçin.")
		return
	}
	if !s.start(w, "fcm", env) {
		return
	}
	defer s.op.Unlock()
	stdin := bytes.Clone(key)
	res := s.run(r.Context(), job{
		op: "fcm", env: env, script: "scripts/new-chat-fcm-secret.sh", args: []string{"-"},
		stdin: stdin, secrets: [][]byte{key},
	})
	s.result(w, "fcm", res)
}

// postExport runs scripts/new-export-key.sh; the key is generated by the
// script and never reaches this process.
func (s *Server) postExport(w http.ResponseWriter, r *http.Request) {
	f, env, ok := s.readForm(w, r)
	if !ok {
		return
	}
	wipeForm(f)
	if !s.start(w, "export", env) {
		return
	}
	defer s.op.Unlock()
	res := s.run(r.Context(), job{op: "export", env: env, script: "scripts/new-export-key.sh"})
	s.result(w, "export", res)
}

// postOpen opens deploy/apps/<env> in Windows Explorer. It answers 204 so
// that the browser stays on the result page.
func (s *Server) postOpen(w http.ResponseWriter, r *http.Request) {
	f, env, ok := s.readForm(w, r)
	if !ok {
		return
	}
	wipeForm(f)
	dir := filepath.Join(s.cfg.Repo, "deploy", "apps", env)
	if s.cfg.OpenDir == nil {
		http.Error(w, "klasör açma yalnızca WSL'de çalışır", http.StatusNotImplemented)
		return
	}
	if err := s.cfg.OpenDir(r.Context(), dir); err != nil {
		s.cfg.Logger.Warn("klasör açılamadı", "env", env, "err", err)
		http.Error(w, "Klasör açılamadı (yalnızca WSL'de çalışır). Yol: deploy/apps/"+env, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

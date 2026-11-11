// Package dataexport is the user's KVKK data export (KVKK md. 11; issue
// #144; ADR-0008, ADR-0021; data model v14 "Veri dışa aktarma"; file format
// docs/data-export-format.md).
//
// identity owns the request (data_export_requests). Collecting starts at
// once, in this process, with the requesting user's own access token
// (ADR-0021 item 3): core's and chat's ExportMyData return only the
// verified caller's data, so no service gets a "read any user" method. The
// job is bounded by the token's lifetime; a job that cannot finish fails
// and does not count toward the one-in-30-days limit.
//
// The archive (JSON files, zip) is built in memory up to MaxArchiveBytes,
// sealed in place with a fresh 32-byte data key (AES-256-GCM, standard
// library) and PUT, encrypted, to core's object storage with a link core
// signs (identity has no storage credentials). The data key is kept only
// wrapped with identity's wrapping key (Keys); the client gets it together
// with a 5-minute download link and decrypts on the device. After 7 days
// the key and the file are deleted (retention; core's cleanup).
//
// Nothing of the content, the data key, the wrapped key, the links or the
// storage keys is ever logged; logs carry request IDs, statuses, failure
// reasons, section counts and gRPC codes only.
package dataexport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Statuses (data_export_requests.status).
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusExpired    = "expired"
	StatusFailed     = "failed"
)

// Failure reasons (data_export_requests.failure_reason).
const (
	FailUnavailable = "unavailable"
	FailTooLarge    = "too_large"
	FailTimeout     = "timeout"
	FailFailed      = "failed"
)

// Rules (ADR-0008, ADR-0021).
const (
	// Interval: one counted request in this long.
	Interval = 30 * 24 * time.Hour
	// Lifetime: a READY export can be downloaded this long (core's
	// expiry is used when it gives one).
	Lifetime = 7 * 24 * time.Hour
	// RowRetention: the request row stays this long (the limit reads it).
	RowRetention = Interval
	// StaleAfter: a request still pending or processing this long after it
	// was made is failed by retention (identity restarted mid-job).
	StaleAfter = 10 * time.Minute
	// ReauthWindow: the download needs a provider sign-in this recent, as
	// account deletion does.
	ReauthWindow = 10 * time.Minute
	// MaxJob bounds one job; the token's remaining lifetime may cut it
	// shorter.
	MaxJob = 3 * time.Minute
	// MinTokenLeft: a request whose access token expires sooner is refused
	// (ErrTokenExpiring); the client refreshes and asks again.
	MinTokenLeft = time.Minute
	// tokenMargin is left before the token expires.
	tokenMargin = 10 * time.Second
	// queueSize bounds the jobs waiting behind the running one.
	queueSize = 16
	// putTimeout bounds the upload to object storage.
	putTimeout = 30 * time.Second
)

var (
	// ErrUnavailable: export is off (no key, no storage or a service is
	// unreachable).
	ErrUnavailable = errors.New("veri dışa aktarma kullanılamıyor")
	// ErrInProgress: another request of the caller is pending or processing.
	ErrInProgress = errors.New("başka bir dışa aktarma hazırlanıyor")
	// ErrLimit: a counted request in the last 30 days.
	ErrLimit = errors.New("30 günde bir dışa aktarma")
	// ErrNotFound: no such request of the caller.
	ErrNotFound = errors.New("dışa aktarma bulunamadı")
	// ErrNotReady: not READY (pending, processing or failed).
	ErrNotReady = errors.New("dışa aktarma hazır değil")
	// ErrExpired: the 7 days are over.
	ErrExpired = errors.New("dışa aktarmanın süresi doldu")
	// ErrReauthenticationRequired: the download needs a recent sign-in.
	ErrReauthenticationRequired = errors.New("yeniden kimlik doğrulama gerekli")
	// ErrTokenExpiring: the access token would expire during the job.
	ErrTokenExpiring = errors.New("erişim token'ının süresi dolmak üzere")
	// ErrUnknownUser: the account is deleted or not registered, or the
	// caller's session is ended.
	ErrUnknownUser = errors.New("kullanıcı bulunamadı")
	// errStorageURL: core gave an upload link outside object storage.
	errStorageURL = errors.New("yükleme adresi kabul edilmedi")
)

// r2Host is the only upload address: Cloudflare R2's S3 endpoint of an
// account (ADR-0020 item 7).
var r2Host = regexp.MustCompile(`^[0-9a-f]{32}\.r2\.cloudflarestorage\.com$`)

// Caller is the verified access token of the request.
type Caller struct {
	User      id.ID
	Session   id.ID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Request is a data export request as its owner sees it.
type Request struct {
	ID            id.ID
	Status        string
	FailureReason string
	RequestedAt   time.Time
	ReadyAt       time.Time
	ExpiresAt     time.Time
	DownloadedAt  time.Time
	SizeBytes     int64
}

// Download is what the client needs to fetch and decrypt the file.
type Download struct {
	URL     string
	Expires time.Time
	Key     []byte
	Size    int64
	Format  string
}

// Config wires a Service. Keys, Storage and the sources may be nil: export
// is then off (ErrUnavailable) and identity runs anyway.
type Config struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Keys    *Keys
	Birth   *birthdate.Cipher
	Storage Storage
	Core    Source
	Chat    Source
	// HTTP uploads the file; nil: a client with putTimeout that follows
	// no redirect.
	HTTP *http.Client
	// AnyStorageHost accepts an upload link to any http(s) host. Only the
	// local environment (MinIO) and tests set it.
	AnyStorageHost bool
	Now            func() time.Time
}

type job struct {
	request, user id.ID
	// ctx carries the caller's verified token for auth.ForwardToken; it is
	// detached from the request's cancellation.
	ctx      context.Context
	deadline time.Time
}

// Service runs data export requests.
type Service struct {
	cfg  Config
	q    *store.Queries
	jobs chan job
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Birth == nil {
		return nil, errors.New("dataexport: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{
			Timeout:       putTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool), jobs: make(chan job, queueSize)}, nil
}

// Enabled reports whether export is configured.
func (s *Service) Enabled() bool {
	return s.cfg.Keys != nil && s.cfg.Storage != nil && s.cfg.Core != nil && s.cfg.Chat != nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func requestOf(r store.DataExportRequest) Request {
	return Request{
		ID: id.ID(r.ID.Bytes), Status: r.Status, FailureReason: r.FailureReason.String,
		RequestedAt: r.RequestedAt.Time.UTC(), ReadyAt: r.ReadyAt.Time.UTC(), ExpiresAt: r.ExpiresAt.Time.UTC(),
		DownloadedAt: r.DownloadedAt.Time.UTC(), SizeBytes: r.SizeBytes.Int64,
	}
}

// unavailable reports errors of core or chat that mean "not now".
func unavailable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unimplemented, codes.Unauthenticated:
		return true
	}
	return false
}

// Create opens the caller's request and starts it (DataExportService.
// CreateDataExport). ctx must carry the caller's verified token: the job
// uses it with core and chat.
func (s *Service) Create(ctx context.Context, c Caller, clientRequestID id.ID) (Request, error) {
	if !s.Enabled() {
		return Request{}, ErrUnavailable
	}
	now := s.now()
	if prev, err := s.q.GetDataExportByClientRequest(ctx, store.GetDataExportByClientRequestParams{UserID: c.User.PG(), ClientRequestID: clientRequestID.PG()}); err == nil {
		return requestOf(prev), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Request{}, fmt.Errorf("tekrar gönderim: %w", err)
	}
	if err := s.check(ctx, s.q, c.User, now); err != nil {
		return Request{}, err
	}
	deadline := now.Add(MaxJob)
	if t := c.ExpiresAt.Add(-tokenMargin); t.Before(deadline) {
		deadline = t
	}
	if deadline.Sub(now) < MinTokenLeft-tokenMargin {
		return Request{}, ErrTokenExpiring
	}
	reqID := id.New()
	upload, err := s.cfg.Storage.Reserve(ctx, reqID)
	if err != nil {
		s.cfg.Log.WarnContext(ctx, "dışa aktarma yuvası açılamadı", slog.String("code", grpcCode(err)))
		return Request{}, ErrUnavailable
	}
	var row store.DataExportRequest
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		u, err := q.LockLiveUser(ctx, c.User.PG())
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !u.RegisteredAt.Valid) {
			return ErrUnknownUser
		}
		if err != nil {
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		// Again under the lock: a concurrent request of the same user.
		if prev, err := q.GetDataExportByClientRequest(ctx, store.GetDataExportByClientRequestParams{UserID: c.User.PG(), ClientRequestID: clientRequestID.PG()}); err == nil {
			row = prev
			return nil
		}
		if err := s.check(ctx, q, c.User, now); err != nil {
			return err
		}
		row, err = q.CreateDataExportRequest(ctx, store.CreateDataExportRequestParams{
			ID: reqID.PG(), UserID: c.User.PG(), UploadID: upload.PG(), ClientRequestID: clientRequestID.PG(), Now: ts(now),
		})
		if err != nil {
			return fmt.Errorf("talep: %w", err)
		}
		return nil
	})
	if err != nil {
		return Request{}, err
	}
	if id.ID(row.ID.Bytes) != reqID {
		return requestOf(row), nil
	}
	select {
	case s.jobs <- job{request: reqID, user: c.User, ctx: context.WithoutCancel(ctx), deadline: deadline}:
	default:
		// Too many waiting: fail it now; it does not count.
		if _, err := s.q.MarkDataExportFailed(ctx, store.MarkDataExportFailedParams{ID: row.ID, FailureReason: pgtype.Text{String: FailUnavailable, Valid: true}, Now: ts(now)}); err != nil {
			return Request{}, fmt.Errorf("talep: %w", err)
		}
		s.cfg.Log.WarnContext(ctx, "dışa aktarma kuyruğu dolu", slog.String("data_export_id", reqID.String()))
		return Request{}, ErrUnavailable
	}
	s.cfg.Log.InfoContext(ctx, "dışa aktarma talebi açıldı", slog.String("data_export_id", reqID.String()))
	return requestOf(row), nil
}

// check refuses a new request while another is active or a counted one is
// younger than Interval.
func (s *Service) check(ctx context.Context, q *store.Queries, user id.ID, now time.Time) error {
	active, err := q.HasActiveDataExport(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("etkin talep: %w", err)
	}
	if active {
		return ErrInProgress
	}
	last, err := q.LastCountedDataExport(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("son talep: %w", err)
	}
	if last.Valid && now.Before(last.Time.Add(Interval)) {
		return ErrLimit
	}
	return nil
}

// List returns the caller's requests of the last 30 days and when the next
// one may be made (zero: now, unless one is active).
func (s *Service) List(ctx context.Context, user id.ID) ([]Request, time.Time, error) {
	now := s.now()
	rows, err := s.q.ListOwnDataExports(ctx, store.ListOwnDataExportsParams{UserID: user.PG(), Since: ts(now.Add(-Interval))})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("talepler: %w", err)
	}
	out := make([]Request, 0, len(rows))
	var next time.Time
	for _, r := range rows {
		out = append(out, requestOf(r))
		if r.Status != StatusFailed {
			if t := r.RequestedAt.Time.Add(Interval); t.After(next) {
				next = t.UTC()
			}
		}
	}
	if !next.After(now) {
		next = time.Time{}
	}
	return out, next, nil
}

// Get returns one of the caller's requests.
func (s *Service) Get(ctx context.Context, user, request id.ID) (Request, error) {
	r, err := s.q.GetOwnDataExport(ctx, store.GetOwnDataExportParams{ID: request.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("talep: %w", err)
	}
	return requestOf(r), nil
}

// Download returns the link and the key of the caller's READY export. ctx
// must carry the caller's token (core signs the link for its owner only).
func (s *Service) Download(ctx context.Context, c Caller, request id.ID) (Download, error) {
	if !s.Enabled() {
		return Download{}, ErrUnavailable
	}
	now := s.now()
	r, err := s.q.GetOwnDataExport(ctx, store.GetOwnDataExportParams{ID: request.PG(), UserID: c.User.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	if err != nil {
		return Download{}, fmt.Errorf("talep: %w", err)
	}
	switch {
	case r.Status == StatusExpired, r.Status == StatusReady && !now.Before(r.ExpiresAt.Time):
		return Download{}, ErrExpired
	case r.Status != StatusReady:
		return Download{}, ErrNotReady
	}
	// A stolen session must not take the whole account's data away: the
	// download needs a fresh sign-in with the provider (ADR-0021 item 6).
	since := now.Add(-ReauthWindow)
	if c.IssuedAt.Before(since) {
		return Download{}, ErrReauthenticationRequired
	}
	sess, err := s.q.GetCallerSession(ctx, store.GetCallerSessionParams{ID: c.Session.PG(), UserID: c.User.PG(), Now: ts(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Download{}, ErrUnknownUser
	}
	if err != nil {
		return Download{}, fmt.Errorf("oturum: %w", err)
	}
	if sess.CreatedAt.Time.Before(since) {
		return Download{}, ErrReauthenticationRequired
	}
	key, err := s.cfg.Keys.Unwrap(request, c.User, r.WrappedKey)
	if err != nil {
		s.cfg.Log.ErrorContext(ctx, "dışa aktarma anahtarı açılamadı", slog.String("data_export_id", request.String()))
		return Download{}, err
	}
	link, expires, err := s.cfg.Storage.DownloadURL(ctx, id.ID(r.UploadID.Bytes))
	switch {
	case errors.Is(err, ErrStorageNotFound):
		return Download{}, ErrExpired
	case err != nil:
		s.cfg.Log.WarnContext(ctx, "dışa aktarma linki alınamadı", slog.String("code", grpcCode(err)))
		return Download{}, ErrUnavailable
	}
	if err := s.q.MarkDataExportDownloaded(ctx, store.MarkDataExportDownloadedParams{ID: r.ID, UserID: c.User.PG(), Now: ts(now)}); err != nil {
		return Download{}, fmt.Errorf("indirme: %w", err)
	}
	s.cfg.Log.InfoContext(ctx, "dışa aktarma indirme linki verildi", slog.String("data_export_id", request.String()))
	return Download{URL: link, Expires: expires.UTC(), Key: key, Size: r.SizeBytes.Int64, Format: Format}, nil
}

// Run runs the queued jobs one at a time until ctx is done (the
// data-export worker). One at a time keeps identity's memory bounded: an
// archive is at most MaxArchiveBytes.
func (s *Service) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case j := <-s.jobs:
			s.process(ctx, j)
		}
	}
}

// process runs one job. Stopping the worker cancels it; the request then
// fails as timeout (by retention if the update below cannot run).
func (s *Service) process(worker context.Context, j job) {
	ctx, cancel := context.WithDeadline(j.ctx, j.deadline)
	defer cancel()
	stop := context.AfterFunc(worker, cancel)
	defer stop()
	log := s.cfg.Log.With(slog.String("data_export_id", j.request.String()))

	n, err := s.q.MarkDataExportProcessing(ctx, store.MarkDataExportProcessingParams{ID: j.request.PG(), Now: ts(s.now())})
	if err != nil || n != 1 {
		// Failed by retention meanwhile, or the account was deleted.
		return
	}
	start := time.Now()
	size, counts, err := s.build(ctx, j)
	if err == nil {
		log.InfoContext(ctx, "dışa aktarma hazır", slog.Int64("size_bytes", size), slog.Any("records", counts),
			slog.Duration("took", time.Since(start)))
		return
	}
	reason := FailFailed
	switch {
	case errors.Is(err, ErrTooLarge):
		reason = FailTooLarge
	case ctx.Err() != nil:
		reason = FailTimeout
	case unavailable(err), errors.Is(err, ErrUnavailable):
		reason = FailUnavailable
	}
	// The request's own context may be over; the update must still run.
	upd, cancelUpd := context.WithTimeout(context.WithoutCancel(worker), 5*time.Second)
	defer cancelUpd()
	if _, uerr := s.q.MarkDataExportFailed(upd, store.MarkDataExportFailedParams{
		ID: j.request.PG(), FailureReason: pgtype.Text{String: reason, Valid: true}, Now: ts(s.now()),
	}); uerr != nil {
		log.ErrorContext(upd, "dışa aktarma durumu yazılamadı", slog.String("err", uerr.Error()))
	}
	// Only the reason and the gRPC code: the error may name a URL.
	level := slog.LevelWarn
	if reason == FailTooLarge {
		// The platform answers this one by hand within KVKK's 30 days.
		level = slog.LevelError
	}
	log.Log(upd, level, "dışa aktarma başarısız", slog.String("reason", reason), slog.String("code", grpcCode(err)))
}

// build collects, seals, uploads and completes one export and marks it
// READY. It returns the file size and the records per file.
func (s *Service) build(ctx context.Context, j job) (int64, map[string]int, error) {
	a := newArchive()
	if err := s.identitySections(ctx, a, j.user); err != nil {
		return 0, nil, err
	}
	for _, src := range []Source{s.cfg.Core, s.cfg.Chat} {
		if err := src.Export(ctx, a.add); err != nil {
			return 0, nil, err
		}
	}
	expected := slices.Concat(identityFiles, s.cfg.Core.Files(), s.cfg.Chat.Files())
	buf, err := a.finish(j.request, j.user, s.now(), expected)
	if err != nil {
		return 0, nil, err
	}
	key := make([]byte, KeySize)
	_, _ = rand.Read(key) // crypto/rand.Read never returns an error
	defer clear(key)
	file, err := seal(buf, key, j.request)
	if err != nil {
		return 0, nil, err
	}
	defer clear(file)
	if err := s.upload(ctx, j, file); err != nil {
		return 0, nil, err
	}
	r, err := s.q.GetOwnDataExport(ctx, store.GetOwnDataExportParams{ID: j.request.PG(), UserID: j.user.PG()})
	if err != nil {
		return 0, nil, fmt.Errorf("talep: %w", err)
	}
	upload := id.ID(r.UploadID.Bytes)
	expires, err := s.cfg.Storage.Complete(ctx, upload)
	if err != nil {
		return 0, nil, err
	}
	now := s.now()
	if expires.IsZero() || expires.After(now.Add(Lifetime)) {
		expires = now.Add(Lifetime)
	}
	wrapped := s.cfg.Keys.Wrap(j.request, j.user, key)
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.MarkDataExportReady(ctx, store.MarkDataExportReadyParams{
			ID: j.request.PG(), WrappedKey: wrapped, SizeBytes: pgtype.Int8{Int64: int64(len(file)), Valid: true},
			Now: ts(now), ExpiresAt: ts(expires.UTC().Truncate(time.Microsecond)),
		})
		if err != nil {
			return fmt.Errorf("hazır: %w", err)
		}
		if n != 1 {
			// The account was deleted or the request failed meanwhile;
			// core deletes the file with the account or at its expiry.
			return errors.New("talep artık hazırlanmıyor")
		}
		return userevents.DataExportReady(ctx, tx, j.user, j.request)
	})
	if err != nil {
		return 0, nil, err
	}
	return int64(len(file)), a.counts, nil
}

// upload PUTs the encrypted file to the link core signed, at most twice.
func (s *Service) upload(ctx context.Context, j job, file []byte) error {
	r, err := s.q.GetOwnDataExport(ctx, store.GetOwnDataExportParams{ID: j.request.PG(), UserID: j.user.PG()})
	if err != nil {
		return fmt.Errorf("talep: %w", err)
	}
	link, headers, err := s.cfg.Storage.UploadURL(ctx, id.ID(r.UploadID.Bytes), int64(len(file)))
	if err != nil {
		return err
	}
	u, err := url.Parse(link)
	switch {
	case err != nil:
		return errStorageURL
	case s.cfg.AnyStorageHost:
		if u.Scheme != "https" && u.Scheme != "http" {
			return errStorageURL
		}
	case u.Scheme != "https" || !r2Host.MatchString(u.Host):
		// A compromised core cannot send the file anywhere else (it is
		// encrypted anyway).
		return errStorageURL
	}
	var last error
	for range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, link, bytes.NewReader(file))
		if err != nil {
			return errStorageURL
		}
		req.ContentLength = int64(len(file))
		for k, v := range headers {
			if http.CanonicalHeaderKey(k) != "Content-Length" {
				req.Header.Set(k, v)
			}
		}
		resp, err := s.cfg.HTTP.Do(req)
		if err != nil {
			// The transport error names the URL: never returned as is.
			last = fmt.Errorf("%w: yükleme bağlantısı", ErrUnavailable)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		last = fmt.Errorf("%w: yükleme HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return last
}

// identityFiles are identity's files of the archive, in order.
var identityFiles = []string{
	"identity/profile.json", "identity/sign_in_identities.json", "identity/devices.json",
	"identity/consents.json", "identity/data_exports.json",
}

func jsonRecord(v any) ([]byte, error) { return json.Marshal(v) }

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	v := id.ID(u.Bytes).String()
	return &v
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// identitySections writes identity's own data (docs/data-export-format.md).
func (s *Service) identitySections(ctx context.Context, a *archive, user id.ID) error {
	u, err := s.q.ExportUserProfile(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("profil: %w", err)
	}
	var birth *string
	if u.BirthDateEnc != nil {
		d, err := s.cfg.Birth.Decrypt(user, u.BirthDateEnc)
		if err != nil {
			return fmt.Errorf("doğum tarihi: %w", err)
		}
		v := d.String()
		birth = &v
	}
	profile, err := jsonRecord(struct {
		UserID              string     `json:"user_id"`
		DisplayName         *string    `json:"display_name"`
		AvatarUploadID      *string    `json:"avatar_upload_id"`
		Locale              string     `json:"locale"`
		BirthDate           *string    `json:"birth_date"`
		BirthDateVisibility string     `json:"birth_date_visibility"`
		BirthDateAudience   string     `json:"birth_date_audience"`
		ActivityVisibility  string     `json:"activity_visibility"`
		RefCode             *string    `json:"ref_code"`
		ReferredByUserID    *string    `json:"referred_by_user_id"`
		ReferredAt          *time.Time `json:"referred_at"`
		RegisteredAt        *time.Time `json:"registered_at"`
		CreatedAt           time.Time  `json:"created_at"`
	}{
		UserID: user.String(), DisplayName: textPtr(u.DisplayName), AvatarUploadID: uuidPtr(u.AvatarUploadID),
		Locale: u.Locale, BirthDate: birth, BirthDateVisibility: u.BirthDateVisibility, BirthDateAudience: u.BirthDateAudience,
		ActivityVisibility: u.ActivityVisibility, RefCode: textPtr(u.RefCode), ReferredByUserID: uuidPtr(u.ReferredByUserID),
		ReferredAt: tsPtr(u.ReferredAt), RegisteredAt: tsPtr(u.RegisteredAt), CreatedAt: u.CreatedAt.Time.UTC(),
	})
	if err != nil {
		return err
	}
	if err := a.add(identityFiles[0], [][]byte{profile}); err != nil {
		return err
	}

	ids, err := s.q.ExportAuthIdentities(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("giriş kimlikleri: %w", err)
	}
	var recs [][]byte
	for _, r := range ids {
		b, err := jsonRecord(struct {
			Provider   string    `json:"provider"`
			Email      *string   `json:"email"`
			CreatedAt  time.Time `json:"created_at"`
			LastUsedAt time.Time `json:"last_used_at"`
		}{r.Provider, textPtr(r.Email), r.CreatedAt.Time.UTC(), r.LastUsedAt.Time.UTC()})
		if err != nil {
			return err
		}
		recs = append(recs, b)
	}
	if err := a.add(identityFiles[1], recs); err != nil {
		return err
	}

	devices, err := s.q.ExportDevices(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("cihazlar: %w", err)
	}
	recs = recs[:0]
	for _, r := range devices {
		b, err := jsonRecord(struct {
			DeviceID   string    `json:"device_id"`
			Platform   string    `json:"platform"`
			CreatedAt  time.Time `json:"created_at"`
			LastSeenAt time.Time `json:"last_seen_at"`
		}{id.ID(r.ID.Bytes).String(), r.Platform, r.CreatedAt.Time.UTC(), r.LastSeenAt.Time.UTC()})
		if err != nil {
			return err
		}
		recs = append(recs, b)
	}
	if err := a.add(identityFiles[2], recs); err != nil {
		return err
	}

	consents, err := s.q.ExportConsents(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("onaylar: %w", err)
	}
	recs = recs[:0]
	for _, r := range consents {
		b, err := jsonRecord(struct {
			DocumentType    string     `json:"document_type"`
			DocumentVersion string     `json:"document_version"`
			AcceptedAt      time.Time  `json:"accepted_at"`
			WithdrawnAt     *time.Time `json:"withdrawn_at"`
		}{r.DocumentType, r.DocumentVersion, r.AcceptedAt.Time.UTC(), tsPtr(r.WithdrawnAt)})
		if err != nil {
			return err
		}
		recs = append(recs, b)
	}
	if err := a.add(identityFiles[3], recs); err != nil {
		return err
	}

	history, err := s.q.ListOwnDataExports(ctx, store.ListOwnDataExportsParams{UserID: user.PG(), Since: ts(s.now().Add(-Interval))})
	if err != nil {
		return fmt.Errorf("talepler: %w", err)
	}
	recs = recs[:0]
	for _, r := range history {
		b, err := jsonRecord(struct {
			DataExportID string     `json:"data_export_id"`
			Status       string     `json:"status"`
			RequestedAt  time.Time  `json:"requested_at"`
			ReadyAt      *time.Time `json:"ready_at"`
			ExpiresAt    *time.Time `json:"expires_at"`
			DownloadedAt *time.Time `json:"downloaded_at"`
		}{id.ID(r.ID.Bytes).String(), r.Status, r.RequestedAt.Time.UTC(), tsPtr(r.ReadyAt), tsPtr(r.ExpiresAt), tsPtr(r.DownloadedAt)})
		if err != nil {
			return err
		}
		recs = append(recs, b)
	}
	return a.add(identityFiles[4], recs)
}

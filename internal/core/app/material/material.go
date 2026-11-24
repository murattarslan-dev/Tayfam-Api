// Package material is the tayfa's material library — materyal kütüphanesi
// (issue #261; ADR-0040 §3): study material of Eğitim tayfas. A material is
// an https link or photos of the member's own notes; no file or PDF is ever
// uploaded. Each carries a subject (required) and a topic (optional) from
// Tayfam's study tag list (catalog.CheckStudySelection); only the keys are
// kept. It is a module (tayfa_modules "materials"); its home widget
// "materials" (package home) is the module's showcase. With the module off
// every method gets apperr.ErrModuleDisabled.
//
// Who may do what (the permission table, package authz):
//
//	read the materials           every active member (TayfaView)
//	add a material               a member (MaterialAdd)
//	edit one's own material      its author (MaterialEditOwn)
//	delete one's own material    its author (MaterialDeleteOwn)
//	remove anyone's material     moderators and admins (MaterialRemoveAny)
//
// # Links
//
// The server never opens a link: no preview, title or favicon is fetched,
// and nothing here imports a network package (TestNoNetwork). A link is
// parsed and normalized by package link (https only, no user info, a host
// name and never an IP address, at most 500 characters) and its domain,
// with every domain above it, must not be on the platform's blocked domain
// list (blocked_domains, managed from the platform panel): otherwise
// apperr.ErrBlockedLink. A domain added to the list later hides the
// materials linking to it, for everyone and everywhere (list, get, report,
// edit); they are not deleted and come back if the domain leaves the list.
// The SQL function material_domain_blocked is the one place the list is
// read.
//
// # Deleting
//
// Deleting is a soft delete; the retention job deletes for good 30 days
// later. The author's own delete drops the photos at once (their files are
// deleted); a moderator's removal only hides the material. There is no
// undo. On account deletion the material stays without its author and the
// person's photo files are deleted.
//
// Titles and links are content: they never reach a log, an audit record or
// an event; IDs do. Export lists the materials one added (package export).
package material

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/material/link"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (ADR-0040 §3; the same CHECKs are in the database).
const (
	// TitleMax bounds a material's title, in characters.
	TitleMax = 80
	// URLMax bounds a normalized link, in characters.
	URLMax = link.MaxLength
	// MaxPhotos is how many note photos a material has at most.
	MaxPhotos = 3
	// MaxPerAuthor bounds the live materials of one member in a tayfa.
	MaxPerAuthor = 200
	// DefaultPageSize and MaxPageSize bound a list page.
	DefaultPageSize = 20
	MaxPageSize     = 50
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
	// maxPageToken bounds a page token before decoding it.
	maxPageToken = 512
)

// Material kinds (materials.kind).
const (
	KindLink      = "link"
	KindNotePhoto = "note_photo"
)

// Kinds are the material kinds, in their order in the contract.
var Kinds = []string{KindLink, KindNotePhoto}

// Validation fields.
const (
	FieldKind            = "kind"
	FieldTitle           = "title"
	FieldURL             = "url"
	FieldPhotoIDs        = "photo_upload_ids"
	FieldSubjectKey      = catalog.FieldSubjectKey
	FieldTopicKey        = catalog.FieldTopicKey
	FieldMaterialID      = "material_id"
	FieldPageSize        = "page_size"
	FieldPageToken       = "page_token"
	FieldClientRequestID = "client_request_id"
)

// Audit actions. The records name the material by ID only.
const (
	AuditCreated = "material.created"
	AuditUpdated = "material.updated"
	AuditDeleted = "material.deleted"
	AuditRemoved = "material.removed"
)

// targetType is the audit record's subject type (and the report target).
const targetType = "material"

// Material is a material as the caller sees it.
type Material struct {
	ID    id.ID
	Kind  string
	Title string
	// URL and Domain: a link only.
	URL    string
	Domain string
	// Photos: note photos only, in order.
	Photos     []id.ID
	SubjectKey string
	TopicKey   string
	// Author is zero once the account is deleted.
	Author    id.ID
	CreatedAt time.Time
	UpdatedAt time.Time
	// CallerCanEdit and CallerCanDelete report what the caller may do now.
	CallerCanEdit   bool
	CallerCanDelete bool
}

// Input is a material as its author writes it. Kind is read on create
// only.
type Input struct {
	Kind       string
	Title      string
	URL        string
	Photos     []id.ID
	SubjectKey string
	TopicKey   string
}

// Filter narrows a list. Zero values are no filter.
type Filter struct {
	Kind       string
	SubjectKey string
	TopicKey   string
	// Mine: only the caller's materials.
	Mine      bool
	PageSize  int32
	PageToken string
}

// Page is a page of the material library.
type Page struct {
	Materials []Material
	// NextPageToken is "" on the last page.
	NextPageToken string
	// CallerCanAdd reports whether the caller may add a material now.
	CallerCanAdd bool
}

// Service serves the material library, built on the component framework's
// tayfa scope (component.Service.Tayfa).
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// CleanTitle returns a title as stored: one line, trimmed, whitespace (line
// breaks and tabs too) folded to single spaces. Refused: empty, invalid
// UTF-8, any other control or format character, more than TitleMax
// characters.
func CleanTitle(s string) (string, bool) {
	if len(s) > (TitleMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, func(r rune) bool {
		if r == '\n' || r == '\r' || r == '\t' {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if t == "" || utf8.RuneCountInString(t) > TitleMax {
		return "", false
	}
	return t, true
}

// checkTerms is the one place a material meets the abuse term check
// (ADR-0012; ADR-0040 §3: "kötüye kullanım filtresi", the link too). The
// term list and its check are not in core yet; when they come, the title
// and the link are checked here and nowhere else.
func checkTerms(...string) error { return nil }

// cleanPhotos checks note photos: 1–MaxPhotos, no repeats; the order is
// kept.
func cleanPhotos(photos []id.ID) ([]id.ID, error) {
	if len(photos) == 0 || len(photos) > MaxPhotos {
		return nil, apperr.Validation(FieldPhotoIDs)
	}
	out := make([]id.ID, 0, len(photos))
	for _, p := range photos {
		if p == (id.ID{}) || slices.Contains(out, p) {
			return nil, apperr.Validation(FieldPhotoIDs)
		}
		out = append(out, p)
	}
	return out, nil
}

// clean checks an input for kind (the material's kind); the error names
// the field. The tags' existence is checked in the transaction.
func clean(kind string, in Input) (Input, link.Link, error) {
	if !slices.Contains(Kinds, kind) {
		return Input{}, link.Link{}, apperr.Validation(FieldKind)
	}
	title, ok := CleanTitle(in.Title)
	if !ok {
		return Input{}, link.Link{}, apperr.Validation(FieldTitle)
	}
	out := Input{Kind: kind, Title: title, SubjectKey: in.SubjectKey, TopicKey: in.TopicKey}
	var l link.Link
	switch kind {
	case KindLink:
		if len(in.Photos) > 0 {
			return Input{}, link.Link{}, apperr.Validation(FieldPhotoIDs)
		}
		if l, ok = link.Parse(in.URL); !ok {
			return Input{}, link.Link{}, apperr.Validation(FieldURL)
		}
		out.URL = l.URL
	case KindNotePhoto:
		if in.URL != "" {
			return Input{}, link.Link{}, apperr.Validation(FieldURL)
		}
		photos, err := cleanPhotos(in.Photos)
		if err != nil {
			return Input{}, link.Link{}, err
		}
		out.Photos = photos
	}
	if err := checkTerms(out.Title, out.URL); err != nil {
		return Input{}, link.Link{}, err
	}
	return out, l, nil
}

// checkTags checks a new choice of tags: the subject is required, the topic
// optional and under that subject; neither retired (catalog).
func checkTags(ctx context.Context, q *store.Queries, subject, topic string) error {
	return catalog.CheckStudySelection(ctx, q, subject, topic)
}

// checkLink refuses a link whose domain, or a domain above it, is blocked.
func checkLink(ctx context.Context, q *store.Queries, l link.Link) error {
	if l.Domain == "" {
		return nil
	}
	blocked, err := q.MaterialDomainBlocked(ctx, l.Domain)
	if err != nil {
		return fmt.Errorf("engelli alan adı: %w", err)
	}
	if blocked {
		return apperr.ErrBlockedLink
	}
	return nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func idOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// photoColumns spreads photos over the three columns.
func photoColumns(photos []id.ID) (p [MaxPhotos]pgtype.UUID) {
	for i, x := range photos {
		if i < MaxPhotos {
			p[i] = x.PG()
		}
	}
	return p
}

// photosOf reads the three columns back, in order.
func photosOf(cols ...pgtype.UUID) []id.ID {
	var out []id.ID
	for _, c := range cols {
		if c.Valid {
			out = append(out, id.ID(c.Bytes))
		}
	}
	return out
}

// attach attaches the author's uploads to the material before its row is
// written (the photo columns' triggers check it again); the photos it
// already has are left as they are.
func attach(ctx context.Context, sc *component.TayfaScope, photos, have []id.ID, material id.ID) error {
	for _, p := range photos {
		if slices.Contains(have, p) {
			continue
		}
		if err := media.Attach(ctx, sc.Q(), sc.User, p, media.PurposeMaterialNote, sc.Tayfa,
			media.Target{Type: media.TargetMaterial, ID: material}, sc.Now); err != nil {
			return err
		}
	}
	return nil
}

// read runs fn for a reader: an active member (anyone else gets
// apperr.ErrTayfaNotFound), the module on, TayfaView.
func (s *Service) read(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleMaterials); err != nil {
			return err
		}
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		return fn(sc)
	})
}

// write runs fn in a transaction with the tayfa locked: an active member,
// the module on, not a read-only tayfa. fn asks for its own permission.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleMaterials); err != nil {
			return err
		}
		return fn(sc)
	})
}

// cursor is a page token's content: the list it belongs to and the last
// material it returned. Clients treat it as opaque.
type cursor struct {
	Tayfa  string    `json:"t"`
	Filter string    `json:"f"`
	At     time.Time `json:"a"`
	ID     string    `json:"i"`
}

// filterKey names a filter in a page token, so a token is only good for
// the list it came from.
func filterKey(f Filter) string {
	return strings.Join([]string{f.Kind, f.SubjectKey, f.TopicKey, fmt.Sprint(f.Mine)}, "|")
}

func encodeCursor(tayfa id.ID, f Filter, at time.Time, last id.ID) string {
	b, _ := json.Marshal(cursor{Tayfa: tayfa.String(), Filter: filterKey(f), At: at, ID: last.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(token string, tayfa id.ID, f Filter) (time.Time, id.ID, bool) {
	if len(token) > maxPageToken {
		return time.Time{}, id.ID{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return time.Time{}, id.ID{}, false
	}
	last, ok := id.Parse(c.ID)
	if !ok || c.Tayfa != tayfa.String() || c.Filter != filterKey(f) || c.At.IsZero() {
		return time.Time{}, id.ID{}, false
	}
	return c.At, last, true
}

// view is a stored material as user sees it in sc.
func view(sc *component.TayfaScope, r store.ListMaterialsPageRow) Material {
	m := Material{
		ID: id.ID(r.ID.Bytes), Kind: r.Kind, Title: r.Title, URL: r.Link.String, Domain: r.Domain.String,
		Photos:     photosOf(r.Photo1UploadID, r.Photo2UploadID, r.Photo3UploadID),
		SubjectKey: r.SubjectKey, TopicKey: r.TopicKey.String, Author: idOf(r.AuthorID),
		CreatedAt: timeOf(r.CreatedAt), UpdatedAt: timeOf(r.UpdatedAt),
	}
	if !sc.ReadOnly() {
		own := m.Author != (id.ID{}) && m.Author == sc.User
		m.CallerCanEdit = own && sc.Authorize(authz.MaterialEditOwn) == nil
		m.CallerCanDelete = (own && sc.Authorize(authz.MaterialDeleteOwn) == nil) || sc.Authorize(authz.MaterialRemoveAny) == nil
	}
	return m
}

// List returns a page of the tayfa's visible materials, the newest first.
// Refused: a bad filter (validation error on kind, subject_key, topic_key,
// page_size, page_token), anyone but an active member, the module off.
func (s *Service) List(ctx context.Context, user, tayfa id.ID, f Filter) (Page, error) {
	if f.Kind != "" && !slices.Contains(Kinds, f.Kind) {
		return Page{}, apperr.Validation(FieldKind)
	}
	if f.SubjectKey != "" && !definition.ValidKey(f.SubjectKey) {
		return Page{}, apperr.Validation(FieldSubjectKey)
	}
	if f.TopicKey != "" && !definition.ValidKey(f.TopicKey) {
		return Page{}, apperr.Validation(FieldTopicKey)
	}
	size := f.PageSize
	switch {
	case size == 0:
		size = DefaultPageSize
	case size < 0 || size > MaxPageSize:
		return Page{}, apperr.Validation(FieldPageSize)
	}
	p := store.ListMaterialsPageParams{
		TayfaID: tayfa.PG(), Kind: text(f.Kind), SubjectKey: text(f.SubjectKey), TopicKey: text(f.TopicKey),
		PageSize: size + 1,
	}
	if f.Mine {
		p.AuthorID = user.PG()
	}
	if f.PageToken != "" {
		at, last, ok := decodeCursor(f.PageToken, tayfa, f)
		if !ok {
			return Page{}, apperr.Validation(FieldPageToken)
		}
		p.HasCursor, p.AfterCreatedAt, p.AfterID = true, ts(at), last.PG()
	}
	var out Page
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		rows, err := sc.Q().ListMaterialsPage(ctx, p)
		if err != nil {
			return fmt.Errorf("materyaller: %w", err)
		}
		more := len(rows) > int(size)
		if more {
			rows = rows[:size]
		}
		out.Materials = make([]Material, 0, len(rows))
		for _, r := range rows {
			out.Materials = append(out.Materials, view(sc, r))
		}
		if more {
			last := rows[len(rows)-1]
			out.NextPageToken = encodeCursor(tayfa, f, timeOf(last.CreatedAt), id.ID(last.ID.Bytes))
		}
		out.CallerCanAdd = sc.Authorize(authz.MaterialAdd) == nil && !sc.ReadOnly()
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return out, nil
}

// load reads one visible material as the scope's user sees it.
func load(ctx context.Context, sc *component.TayfaScope, material id.ID) (Material, error) {
	rows, err := sc.Q().ListMaterialsPage(ctx, store.ListMaterialsPageParams{
		TayfaID: sc.Tayfa.PG(), MaterialID: material.PG(), PageSize: 1,
	})
	if err != nil {
		return Material{}, fmt.Errorf("materyal: %w", err)
	}
	if len(rows) != 1 {
		return Material{}, apperr.ErrMaterialNotFound
	}
	return view(sc, rows[0]), nil
}

// Get returns a visible material. Refused: a material not in the library,
// deleted or linking to a blocked domain (apperr.ErrMaterialNotFound), and
// the refusals of every read.
func (s *Service) Get(ctx context.Context, user, tayfa, material id.ID) (Material, error) {
	var out Material
	err := s.read(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		var err error
		out, err = load(ctx, sc, material)
		return err
	})
	if err != nil {
		return Material{}, err
	}
	return out, nil
}

// Create adds a material of the caller's. A repeated client request ID
// returns the material it created. Refused: a bad input (validation error
// on kind, title, url, photo_upload_ids, subject_key, topic_key,
// client_request_id), a blocked link (apperr.ErrBlockedLink), a photo that
// is not the caller's ready material_note upload of the tayfa (the media
// errors), too many materials (apperr.ErrMaterialLimit), MaterialAdd, a
// read-only tayfa.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in Input, clientRequestID string) (Material, error) {
	crid, ok := id.Parse(clientRequestID)
	if !ok {
		return Material{}, apperr.Validation(FieldClientRequestID)
	}
	c, l, err := clean(in.Kind, in)
	if err != nil {
		return Material{}, err
	}
	var material id.ID
	err = s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.MaterialAdd); err != nil {
			return err
		}
		q := sc.Q()
		prev, err := q.GetMaterialByClientRequest(ctx, store.GetMaterialByClientRequestParams{TayfaID: tayfa.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			material = id.ID(prev.Bytes)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("materyal: %w", err)
		}
		if err := checkTags(ctx, q, c.SubjectKey, c.TopicKey); err != nil {
			return err
		}
		if err := checkLink(ctx, q, l); err != nil {
			return err
		}
		n, err := q.CountAuthorMaterials(ctx, store.CountAuthorMaterialsParams{TayfaID: tayfa.PG(), AuthorID: user.PG()})
		if err != nil {
			return fmt.Errorf("materyaller: %w", err)
		}
		if n >= MaxPerAuthor {
			return apperr.ErrMaterialLimit
		}
		material = id.New()
		if err := attach(ctx, sc, c.Photos, nil, material); err != nil {
			return err
		}
		photos := photoColumns(c.Photos)
		if err := q.CreateMaterial(ctx, store.CreateMaterialParams{
			ID: material.PG(), TayfaID: tayfa.PG(), AuthorID: user.PG(), Kind: c.Kind, Title: c.Title,
			Link: text(l.URL), Domain: text(l.Domain),
			Photo1UploadID: photos[0], Photo2UploadID: photos[1], Photo3UploadID: photos[2],
			SubjectKey: c.SubjectKey, TopicKey: text(c.TopicKey), ClientRequestID: crid.PG(), Now: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("materyal ekleme: %w", err)
		}
		return sc.Audit(ctx, AuditCreated, txn.Target{Type: targetType, ID: material}, map[string]any{"kind": c.Kind})
	})
	if err != nil {
		return Material{}, err
	}
	return s.Get(ctx, user, tayfa, material)
}

// lock reads a visible material of the tayfa for a change.
func lock(ctx context.Context, q *store.Queries, tayfa, material id.ID) (store.GetMaterialForUpdateRow, error) {
	r, err := q.GetMaterialForUpdate(ctx, store.GetMaterialForUpdateParams{TayfaID: tayfa.PG(), ID: material.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.GetMaterialForUpdateRow{}, apperr.ErrMaterialNotFound
	case err != nil:
		return store.GetMaterialForUpdateRow{}, fmt.Errorf("materyal: %w", err)
	}
	if r.DeletedAt.Valid || r.Blocked {
		return store.GetMaterialForUpdateRow{}, apperr.ErrMaterialNotFound
	}
	return r, nil
}

func authoredBy(r store.GetMaterialForUpdateRow, user id.ID) bool {
	return r.AuthorID.Valid && id.ID(r.AuthorID.Bytes) == user
}

// Update replaces the title, the link or the photos, and the tags of the
// caller's material; its kind stays. Photos left out are released (their
// files are deleted). Tags left as they were are kept even if retired.
// Refused: a bad input, a blocked link, a material that is not the
// caller's (apperr.ErrMaterialNotFound), the media errors,
// MaterialEditOwn, a read-only tayfa.
func (s *Service) Update(ctx context.Context, user, tayfa, material id.ID, in Input) (Material, error) {
	err := s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.MaterialEditOwn); err != nil {
			return err
		}
		q := sc.Q()
		r, err := lock(ctx, q, tayfa, material)
		if err != nil {
			return err
		}
		if !authoredBy(r, user) {
			return apperr.ErrMaterialNotFound
		}
		c, l, err := clean(r.Kind, in)
		if err != nil {
			return err
		}
		if c.SubjectKey != r.SubjectKey || c.TopicKey != r.TopicKey.String {
			if err := checkTags(ctx, q, c.SubjectKey, c.TopicKey); err != nil {
				return err
			}
		}
		if err := checkLink(ctx, q, l); err != nil {
			return err
		}
		if err := attach(ctx, sc, c.Photos, photosOf(r.Photo1UploadID, r.Photo2UploadID, r.Photo3UploadID), material); err != nil {
			return err
		}
		photos := photoColumns(c.Photos)
		n, err := q.UpdateMaterial(ctx, store.UpdateMaterialParams{
			Title: c.Title, Link: text(l.URL), Domain: text(l.Domain),
			Photo1UploadID: photos[0], Photo2UploadID: photos[1], Photo3UploadID: photos[2],
			SubjectKey: c.SubjectKey, TopicKey: text(c.TopicKey), Now: ts(sc.Now), TayfaID: tayfa.PG(), ID: material.PG(),
		})
		if err != nil {
			return fmt.Errorf("materyal düzenleme: %w", err)
		}
		if n != 1 {
			return apperr.ErrMaterialNotFound
		}
		return sc.Audit(ctx, AuditUpdated, txn.Target{Type: targetType, ID: material}, nil)
	})
	if err != nil {
		return Material{}, err
	}
	return s.Get(ctx, user, tayfa, material)
}

// Delete deletes the caller's own material (MaterialDeleteOwn; its photos
// are released at once) or removes anyone's for a moderator
// (MaterialRemoveAny; hidden, photos kept until the retention job). Refused:
// a material not visible in the library (apperr.ErrMaterialNotFound), a
// read-only tayfa.
func (s *Service) Delete(ctx context.Context, user, tayfa, material id.ID) error {
	return s.write(ctx, user, tayfa, func(sc *component.TayfaScope) error {
		q := sc.Q()
		r, err := lock(ctx, q, tayfa, material)
		if err != nil {
			return err
		}
		own := authoredBy(r, user)
		action := AuditDeleted
		if own {
			err = sc.Authorize(authz.MaterialDeleteOwn)
		} else {
			err = sc.Authorize(authz.MaterialRemoveAny)
			action = AuditRemoved
		}
		if err != nil {
			return err
		}
		n, err := q.DeleteMaterial(ctx, store.DeleteMaterialParams{
			Now: ts(sc.Now), DeletedBy: user.PG(), ReleasePhotos: own, TayfaID: tayfa.PG(), ID: material.PG(),
		})
		if err != nil {
			return fmt.Errorf("materyal silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrMaterialNotFound
		}
		return sc.Audit(ctx, action, txn.Target{Type: targetType, ID: material}, nil)
	})
}

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/scoreboard"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/home"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/material/link"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Catalog management (issue #262; ADR-0025 §7, ADR-0039 §1, ADR-0040 §3
// and §5, ADR-0042 §5). The platform owner manages the interests, event
// types, badges, study tags, score presets and blocked domains here
// without changing code.
//
// Every save runs in one transaction that first locks catalog_meta, so
// saves happen one after another and every check sees the catalog as the
// save leaves it:
//
//  1. the entry is checked on its own (keys, names, the closed definition
//     schema, the badge rule schema, the preset rules);
//  2. it is written, and what depends on the catalog as a whole is checked
//     against the written state (the interests a type names, the presets a
//     scoreboard offers, the badges that count a type, the study tag list's
//     "Diğer" rule, the database's own constraints and triggers);
//  3. with validate_only the transaction is rolled back here and the result
//     is the check; otherwise the catalog's version is raised (when the
//     entry is part of GET /v1/catalog), the platform audit entry is
//     written and catalog.changed is published (catalog.PublishChanged,
//     last).
//
// A failed check is a *ProblemError: the request field and a Turkish
// sentence that names the place and never repeats a value. Keys never
// change and nothing is deleted (ADR-0025 §2): an entry that should go is
// retired. The blocked domain list is the only list whose entries are
// added and removed.

// Platform audit actions of catalog management. The entry carries the key
// (catalog_key), never a target.
const (
	ActionInterestCreate = "catalog.interest_create"
	ActionInterestUpdate = "catalog.interest_update"
	ActionTypeCreate     = "catalog.type_create"
	ActionTypeUpdate     = "catalog.type_update"
	ActionBadgeCreate    = "catalog.badge_create"
	ActionBadgeUpdate    = "catalog.badge_update"
	ActionStudyTagCreate = "catalog.study_tag_create"
	ActionStudyTagUpdate = "catalog.study_tag_update"
	ActionPresetCreate   = "catalog.preset_create"
	ActionPresetUpdate   = "catalog.preset_update"
	ActionDomainBlock    = "catalog.domain_block"
	ActionDomainUnblock  = "catalog.domain_unblock"
)

// Limits of catalog entries, as the database checks them.
const (
	maxPosition       = 9999
	maxDefaultWidgets = 20
	maxInterestKeys   = 3
	// maxPresetParams bounds a preset's rules (a handful of short keys).
	maxPresetParams = 1024
	// otherPosition is the position of a "Diğer" study tag: last.
	otherPosition = 999
)

// otherLabels are the names of a "Diğer" study tag the panel adds.
var otherLabels = map[string]string{"tr": "Diğer"}

// ProblemError is a catalog entry that failed a check.
type ProblemError struct {
	// Field is the request field the problem is about.
	Field string
	// Problem says what is wrong, in Turkish, without the value.
	Problem string
}

func (e *ProblemError) Error() string { return "katalog: " + e.Field + ": " + e.Problem }

func problem(field, msg string) error { return &ProblemError{Field: field, Problem: msg} }

// dbProblem turns the database's refusal of a write (a constraint or a
// trigger, the second line of defence) into a problem; other errors are
// returned as they are.
func dbProblem(err error, field string) error {
	switch store.SQLState(err) {
	case store.CodeCheckViolation, store.CodeUniqueViolation, "23503", "23502", "22P02":
		return problem(field, "veritabanı kuralı kaydı reddetti")
	}
	return err
}

// errValidateOnly rolls back a validate_only save after its checks.
var errValidateOnly = errors.New("yalnızca doğrulama")

// CheckResult is what a save's checks found. Fields, Components,
// MinClientVersion, Version and NewVersion are set for event types.
type CheckResult struct {
	Problem          string
	Field            string
	Fields           []definition.Field
	Components       []definition.Component
	MinClientVersion string
	Version          int32
	NewVersion       bool
}

// SaveResult is the result of a save.
type SaveResult struct {
	Check CheckResult
	// CatalogVersion is the catalog's version after the save.
	CatalogVersion int64
}

// change is what a save's write step did.
type change struct {
	action string
	key    string
	// changed is false when the entry was saved as it already was: then
	// nothing is recorded or published.
	changed bool
	// published: the entry is part of GET /v1/catalog, so the save raises
	// catalog_version and publishes catalog.changed.
	published bool
}

// save runs a catalog save (see the package comment above).
func (s *Service) save(ctx context.Context, c auth.AdminClaims, validateOnly bool, reason string,
	write func(q *store.Queries, cl Caller, res *SaveResult) (change, error)) (SaveResult, error) {
	var cl Caller
	var err error
	if validateOnly {
		cl, err = s.caller(ctx, c)
	} else {
		cl, err = s.destructive(ctx, c, reason)
	}
	if err != nil {
		return SaveResult{}, err
	}
	var res SaveResult
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		v, err := q.AdminLockCatalog(ctx)
		if err != nil {
			return fmt.Errorf("katalog kilidi: %w", err)
		}
		res.CatalogVersion = v
		ch, err := write(q, cl, &res)
		if err != nil {
			return err
		}
		if validateOnly {
			return errValidateOnly
		}
		if !ch.changed {
			return nil
		}
		if ch.published {
			if res.CatalogVersion, err = q.AdminBumpCatalogVersion(ctx); err != nil {
				return fmt.Errorf("katalog sürümü: %w", err)
			}
		}
		if err := catalogAudit(ctx, q, cl, ch.action, ch.key, reason); err != nil {
			return err
		}
		if ch.published {
			return catalog.PublishChanged(ctx, q)
		}
		return nil
	})
	var p *ProblemError
	switch {
	case errors.Is(err, errValidateOnly):
		return res, nil
	case validateOnly && errors.As(err, &p):
		res.Check.Problem, res.Check.Field = p.Problem, p.Field
		return res, nil
	case err != nil:
		return SaveResult{}, err
	}
	return res, nil
}

// catalogAudit writes the platform audit entry of a catalog change. It has
// no target and no tayfa: the key is the entry's catalog_key.
func catalogAudit(ctx context.Context, q *store.Queries, c Caller, action, key, reason string) error {
	return txn.Event(ctx, q, EventAudit, id.ID{}, c.User, txn.Target{}, map[string]any{
		"action": action, "reason": reason, "admin_session_id": c.Session.String(), "catalog_key": key,
	})
}

// --- Reading ---

// ScorePreset is a scoreboard's game rule preset (catalog_score_presets).
type ScorePreset struct {
	Key      string
	Position int32
	Labels   map[string]string
	// Params is the rules as a JSON object.
	Params []byte
	Status string
}

// BlockedDomain is an entry of the blocked domain list.
type BlockedDomain struct {
	Domain    string
	CreatedAt time.Time
	// Initial: the entry came with the migration, not from the panel.
	Initial bool
}

// CatalogView is the catalog as the panel manages it.
type CatalogView struct {
	catalog.Panel
	Presets []ScorePreset
	Domains []BlockedDomain
}

// GetCatalog returns the whole catalog, retired entries included, with the
// score presets and the blocked domain list.
func (s *Service) GetCatalog(ctx context.Context, c auth.AdminClaims) (CatalogView, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return CatalogView{}, err
	}
	var out CatalogView
	err := pgx.BeginTxFunc(ctx, s.cfg.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if out.Panel, err = catalog.ReadPanel(ctx, q); err != nil {
			return err
		}
		presets, err := q.AdminListScorePresets(ctx)
		if err != nil {
			return fmt.Errorf("ön ayarlar: %w", err)
		}
		out.Presets = make([]ScorePreset, 0, len(presets))
		for _, r := range presets {
			p := ScorePreset{Key: r.Key, Position: int32(r.Position), Params: compactJSON(r.Params), Status: r.Status}
			if err := json.Unmarshal(r.Labels, &p.Labels); err != nil {
				return fmt.Errorf("ön ayar %q: adlar okunamadı: %w", r.Key, err)
			}
			out.Presets = append(out.Presets, p)
		}
		domains, err := q.AdminListBlockedDomains(ctx)
		if err != nil {
			return fmt.Errorf("engelli alan adları: %w", err)
		}
		out.Domains = make([]BlockedDomain, 0, len(domains))
		for _, r := range domains {
			out.Domains = append(out.Domains, BlockedDomain{Domain: r.Domain, CreatedAt: timeOf(r.CreatedAt), Initial: r.Initial})
		}
		return nil
	})
	if err != nil {
		return CatalogView{}, err
	}
	return out, nil
}

// --- Shared checks ---

func checkKey(key string) error {
	if !definition.ValidKey(key) {
		return problem("key", "anahtar küçük harfle başlamalı; yalnızca küçük harf, rakam ve alt çizgi, 2–40 karakter")
	}
	return nil
}

func checkIcon(icon string) error {
	if !definition.ValidKey(icon) {
		return problem("icon", "ikon, istemcinin ikon setinden bir anahtar olmalı")
	}
	return nil
}

func checkStatus(status string) error {
	if status != catalog.StatusActive && status != catalog.StatusRetired {
		return problem("status", "durum aktif ya da emekli olmalı")
	}
	return nil
}

func checkPosition(position int32) error {
	if position < 0 || position > maxPosition {
		return problem("position", "sıra 0–9999 arasında olmalı")
	}
	return nil
}

func checkLabels(labels map[string]string) error {
	if err := catalog.CheckLabels(labels); err != nil {
		return problem("labels", "adlar: Türkçe ad zorunlu, en fazla 5 dil, iki harfli küçük dil kodu, her ad tek satır ve en fazla 60 karakter")
	}
	return nil
}

// labelsJSON is the stored form of labels.
func labelsJSON(labels map[string]string) []byte {
	b, _ := json.Marshal(labels) // map of strings; cannot fail
	return b
}

func readLabels(raw []byte) map[string]string {
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	return m
}

func compactJSON(raw []byte) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

// found reads the error of a row read for update: false when there is no
// row.
func found(err error) (bool, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// checkCreate refuses a create of an existing key and a change of a
// missing one.
func checkCreate(create, found bool) error {
	switch {
	case create && found:
		return problem("key", "bu anahtarla bir kayıt zaten var")
	case !create && !found:
		return problem("key", "bu anahtarla bir kayıt yok")
	}
	return nil
}

// --- Interests ---

// SaveInterest creates or changes an interest.
func (s *Service) SaveInterest(ctx context.Context, c auth.AdminClaims, in catalog.Interest, create, validateOnly bool, reason string) (SaveResult, error) {
	return s.save(ctx, c, validateOnly, reason, func(q *store.Queries, _ Caller, _ *SaveResult) (change, error) {
		if err := checkKey(in.Key); err != nil {
			return change{}, err
		}
		if err := errors.Join(checkPosition(in.Position), checkLabels(in.Labels), checkIcon(in.Icon), checkStatus(in.Status)); err != nil {
			return change{}, firstProblem(err)
		}
		widgets := in.DefaultWidgets
		if widgets == nil {
			widgets = []string{}
		}
		if len(widgets) > maxDefaultWidgets {
			return change{}, problem("default_widgets", "en fazla 20 varsayılan bileşen")
		}
		for i, w := range widgets {
			hw, ok := home.Lookup(w)
			if !ok || hw.NeedsConfig || slices.Contains(widgets[:i], w) {
				return change{}, problem("default_widgets", "varsayılan bileşenler, ayar istemeyen ana sayfa bileşenleri olmalı ve tekrar etmemeli")
			}
		}
		cur, err := q.AdminGetCatalogInterestForUpdate(ctx, in.Key)
		ok, err := found(err)
		if err != nil {
			return change{}, fmt.Errorf("ilgi alanı: %w", err)
		}
		if err := checkCreate(create, ok); err != nil {
			return change{}, err
		}
		ch := change{action: ActionInterestCreate, key: in.Key, changed: true, published: true}
		if create {
			err = q.AdminInsertCatalogInterest(ctx, store.AdminInsertCatalogInterestParams{
				Key: in.Key, Position: in.Position, Labels: labelsJSON(in.Labels), Icon: in.Icon, Status: in.Status, DefaultWidgets: widgets,
			})
		} else {
			ch.action = ActionInterestUpdate
			ch.changed = cur.Position != in.Position || cur.Icon != in.Icon || cur.Status != in.Status ||
				!maps.Equal(readLabels(cur.Labels), in.Labels) || !slices.Equal(cur.DefaultWidgets, widgets)
			if !ch.changed {
				return ch, nil
			}
			err = q.AdminUpdateCatalogInterest(ctx, store.AdminUpdateCatalogInterestParams{
				Key: in.Key, Position: in.Position, Labels: labelsJSON(in.Labels), Icon: in.Icon, Status: in.Status, DefaultWidgets: widgets,
			})
		}
		if err != nil {
			return change{}, dbProblem(fmt.Errorf("ilgi alanı yazılamadı: %w", err), "key")
		}
		return ch, nil
	})
}

// firstProblem returns the first problem of errors.Join's result.
func firstProblem(err error) error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		if errs := j.Unwrap(); len(errs) > 0 {
			return errs[0]
		}
	}
	return err
}

// --- Event types ---

// TypeInput is an event type as the panel saves it.
type TypeInput struct {
	Key          string
	InterestKeys []string
	Position     int32
	Labels       map[string]string
	Icon         string
	Modes        []string
	Status       string
	// BaseVersion is the version the change was made on; 0 creates the
	// type.
	BaseVersion int32
	// Definition is the definition as JSON.
	Definition []byte
}

// SaveEventType creates or changes an event type. A change of the names,
// icon, modes or definition makes a new version (ADR-0025 §2): old events
// keep theirs. The interests, position and status are not part of a
// version.
func (s *Service) SaveEventType(ctx context.Context, c auth.AdminClaims, in TypeInput, validateOnly bool, reason string) (SaveResult, error) {
	return s.save(ctx, c, validateOnly, reason, func(q *store.Queries, _ Caller, res *SaveResult) (change, error) {
		def, raw, err := checkType(in)
		if err != nil {
			return change{}, err
		}
		res.Check.Fields, res.Check.Components = def.Fields, def.Components
		res.Check.MinClientVersion = def.MinClientVersion()
		cur, err := q.AdminGetCatalogEventTypeForUpdate(ctx, in.Key)
		ok, err := found(err)
		if err != nil {
			return change{}, fmt.Errorf("tür: %w", err)
		}
		create := in.BaseVersion == 0
		if err := checkCreate(create, ok); err != nil {
			return change{}, err
		}
		if ok && cur.Version != in.BaseVersion {
			return change{}, problem("version", "tür bu arada değişti; sayfayı yenileyip yeniden düzenle")
		}
		if err := checkTypeInterests(ctx, q, in.InterestKeys, cur.InterestKeys); err != nil {
			return change{}, err
		}
		if err := checkTypePresets(ctx, q, def); err != nil {
			return change{}, err
		}
		labels := labelsJSON(in.Labels)
		ch := change{action: ActionTypeCreate, key: in.Key, changed: true, published: true}
		version := int32(1)
		newVersion := true
		if ok {
			ch.action = ActionTypeUpdate
			version = cur.Version
			newVersion = cur.Icon != in.Icon || !slices.Equal(cur.Modes, in.Modes) ||
				!maps.Equal(readLabels(cur.Labels), in.Labels) || !sameJSON(cur.Definition, raw)
			if newVersion {
				version = cur.Version + 1
			}
			ch.changed = newVersion || cur.Position != in.Position || cur.Status != in.Status || !slices.Equal(cur.InterestKeys, in.InterestKeys)
		}
		res.Check.Version, res.Check.NewVersion = version, newVersion
		if !ch.changed {
			return ch, nil
		}
		if create {
			err = q.AdminInsertCatalogEventType(ctx, store.AdminInsertCatalogEventTypeParams{
				Key: in.Key, InterestKeys: in.InterestKeys, Position: in.Position, Labels: labels, Icon: in.Icon,
				Modes: in.Modes, Status: in.Status, Version: version, Definition: raw,
			})
		} else {
			err = q.AdminUpdateCatalogEventType(ctx, store.AdminUpdateCatalogEventTypeParams{
				Key: in.Key, InterestKeys: in.InterestKeys, Position: in.Position, Labels: labels, Icon: in.Icon,
				Modes: in.Modes, Status: in.Status, Version: version, Definition: raw,
			})
		}
		if err != nil {
			return change{}, dbProblem(fmt.Errorf("tür yazılamadı: %w", err), "key")
		}
		if newVersion {
			if err := q.AdminInsertCatalogEventTypeVersion(ctx, store.AdminInsertCatalogEventTypeVersionParams{
				TypeKey: in.Key, Version: version, Labels: labels, Icon: in.Icon, Modes: in.Modes, Definition: raw,
			}); err != nil {
				return change{}, dbProblem(fmt.Errorf("tür sürümü yazılamadı: %w", err), "definition")
			}
			// The badges that count this type are checked against every
			// version of it, the new one included (ADR-0039 §2: nothing
			// sensitive earns a badge).
			if err := checkTypeBadges(ctx, q, in.Key); err != nil {
				return change{}, err
			}
		}
		return ch, nil
	})
}

// checkType checks a type on its own and parses its definition; raw is the
// definition as compact JSON.
func checkType(in TypeInput) (*definition.Definition, []byte, error) {
	if err := checkKey(in.Key); err != nil {
		return nil, nil, err
	}
	if err := errors.Join(checkPosition(in.Position), checkLabels(in.Labels), checkIcon(in.Icon), checkStatus(in.Status)); err != nil {
		return nil, nil, firstProblem(err)
	}
	if in.BaseVersion < 0 {
		return nil, nil, problem("version", "sürüm geçersiz")
	}
	if n := len(in.InterestKeys); n < 1 || n > maxInterestKeys {
		return nil, nil, problem("interest_keys", "bir ile üç arasında ilgi alanı gerekli; ilki ana ilgi alanıdır")
	}
	for i, k := range in.InterestKeys {
		if !definition.ValidKey(k) || slices.Contains(in.InterestKeys[:i], k) {
			return nil, nil, problem("interest_keys", "ilgi alanı anahtarları geçerli olmalı ve tekrar etmemeli")
		}
	}
	if n := len(in.Modes); n < 1 || n > 2 {
		return nil, nil, problem("modes", "en az bir mod gerekli: yüz yüze, online ya da ikisi")
	}
	for i, m := range in.Modes {
		if (m != catalog.ModeInPerson && m != catalog.ModeOnline) || slices.Contains(in.Modes[:i], m) {
			return nil, nil, problem("modes", "mod yüz yüze ya da online olmalı ve tekrar etmemeli")
		}
	}
	if len(in.Definition) > definition.MaxBytes {
		return nil, nil, problem("definition", "tanım en fazla 16 KiB olabilir")
	}
	var b bytes.Buffer
	if err := json.Compact(&b, in.Definition); err != nil {
		return nil, nil, problem("definition", "tanım geçerli bir JSON değil")
	}
	raw := b.Bytes()
	def, err := definition.Parse(raw)
	if err != nil {
		return nil, nil, problem("definition", err.Error())
	}
	return def, raw, nil
}

// checkTypeInterests: every interest must be in the catalog, and one the
// type did not have before must be active.
func checkTypeInterests(ctx context.Context, q *store.Queries, keys, before []string) error {
	rows, err := q.AdminListCatalogInterestStatuses(ctx, keys)
	if err != nil {
		return fmt.Errorf("ilgi alanları: %w", err)
	}
	status := make(map[string]string, len(rows))
	for _, r := range rows {
		status[r.Key] = r.Status
	}
	for _, k := range keys {
		st, ok := status[k]
		if !ok {
			return problem("interest_keys", "ilgi alanı katalogda yok")
		}
		if st != catalog.StatusActive && !slices.Contains(before, k) {
			return problem("interest_keys", "emekli bir ilgi alanı türe yeni eklenemez")
		}
	}
	return nil
}

// checkTypePresets: the presets a scoreboard offers must be in the catalog.
func checkTypePresets(ctx context.Context, q *store.Queries, def *definition.Definition) error {
	var keys []string
	for _, c := range def.Components {
		if c.Kind != definition.CompScoreboard {
			continue
		}
		if ks, ok := c.Params[scoreboard.ParamPresets].Value.([]string); ok {
			for _, k := range ks {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	found, err := q.AdminListScorePresetKeys(ctx, keys)
	if err != nil {
		return fmt.Errorf("ön ayarlar: %w", err)
	}
	if len(found) != len(keys) {
		return problem("definition", "yazbozun sunduğu ön ayarlardan biri katalogda yok")
	}
	return nil
}

// checkTypeBadges checks again every active badge whose rule names the
// type.
func checkTypeBadges(ctx context.Context, q *store.Queries, key string) error {
	badges, err := catalog.Badges(ctx, q)
	if err != nil {
		return err
	}
	for _, b := range badges {
		if b.Status != catalog.StatusActive || b.Rule.RuleType() != key {
			continue
		}
		if err := catalog.CheckBadge(ctx, q, b); err != nil {
			return problem("definition", "bu türü sayan "+b.Key+" rozeti artık geçersiz olur: "+err.Error())
		}
	}
	return nil
}

// sameJSON reports whether two JSON texts are the same document (jsonb
// stores keys in its own order and with spaces).
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x) // maps are marshalled with sorted keys
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// --- Badges ---

// SaveBadge creates or changes a badge definition (ADR-0039 §1). Its
// scope, rule kind and period never change.
func (s *Service) SaveBadge(ctx context.Context, c auth.AdminClaims, b catalog.Badge, create, validateOnly bool, reason string) (SaveResult, error) {
	return s.save(ctx, c, validateOnly, reason, func(q *store.Queries, _ Caller, _ *SaveResult) (change, error) {
		if err := checkKey(b.Key); err != nil {
			return change{}, err
		}
		if err := errors.Join(checkPosition(b.Position), checkLabels(b.Labels), checkIcon(b.Icon), checkStatus(b.Status)); err != nil {
			return change{}, firstProblem(err)
		}
		if b.Scope != catalog.BadgeScopeUser && b.Scope != catalog.BadgeScopeTayfa {
			return change{}, problem("scope", "kapsam kişi ya da tayfa olmalı")
		}
		rule, err := badgeRuleJSON(b.Rule)
		if err != nil {
			return change{}, err
		}
		if b.Rule, err = catalog.ParseBadgeRule(rule); err != nil {
			return change{}, problem("rule", err.Error())
		}
		if err := catalog.CheckBadge(ctx, q, b); err != nil {
			return change{}, problem("rule", err.Error())
		}
		cur, err := q.AdminGetCatalogBadgeForUpdate(ctx, b.Key)
		ok, err := found(err)
		if err != nil {
			return change{}, fmt.Errorf("rozet: %w", err)
		}
		if err := checkCreate(create, ok); err != nil {
			return change{}, err
		}
		ch := change{action: ActionBadgeCreate, key: b.Key, changed: true, published: true}
		if create {
			err = q.AdminInsertCatalogBadge(ctx, store.AdminInsertCatalogBadgeParams{
				Key: b.Key, Scope: b.Scope, Position: b.Position, Labels: labelsJSON(b.Labels), Icon: b.Icon, Rule: rule, Status: b.Status,
			})
		} else {
			ch.action = ActionBadgeUpdate
			old, perr := catalog.ParseBadgeRule(compactJSON(cur.Rule))
			if perr != nil {
				return change{}, fmt.Errorf("rozet %q: %w", b.Key, perr)
			}
			if cur.Scope != b.Scope || old.Kind != b.Rule.Kind || old.Period != b.Rule.Period {
				return change{}, problem("rule", "rozetin kapsamı, kural türü ve dönemi değişmez; yeni bir rozet ekle")
			}
			ch.changed = cur.Position != b.Position || cur.Icon != b.Icon || cur.Status != b.Status ||
				!maps.Equal(readLabels(cur.Labels), b.Labels) || old != b.Rule
			if !ch.changed {
				return ch, nil
			}
			err = q.AdminUpdateCatalogBadge(ctx, store.AdminUpdateCatalogBadgeParams{
				Key: b.Key, Position: b.Position, Labels: labelsJSON(b.Labels), Icon: b.Icon, Rule: rule, Status: b.Status,
			})
		}
		if err != nil {
			return change{}, dbProblem(fmt.Errorf("rozet yazılamadı: %w", err), "rule")
		}
		return ch, nil
	})
}

// badgeRuleJSON is the stored form of a rule: only the keys of its kind.
func badgeRuleJSON(r catalog.BadgeRule) ([]byte, error) {
	m := map[string]any{"kind": r.Kind}
	switch r.Kind {
	case catalog.RuleThreshold:
		m["metric"], m["value"], m["period"] = r.Metric, r.Value, r.Period
	case catalog.RuleFirst, catalog.RuleChampion:
		if r.EventType != "" {
			m["event_type"] = r.EventType
		}
	default:
		return nil, problem("rule", "kural türü eşik, ilk katılım ya da şampiyonluk olmalı")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, problem("rule", "kural değeri geçersiz")
	}
	return b, nil
}

// --- Study tags ---

// SaveStudyTag creates or changes a study tag (ADR-0040 §5). A tag's parent
// never changes. A new level or subject gets its "Diğer" child in the same
// save; afterwards the list must pass catalog.CheckStudyTags.
func (s *Service) SaveStudyTag(ctx context.Context, c auth.AdminClaims, t catalog.StudyTag, create, validateOnly bool, reason string) (SaveResult, error) {
	return s.save(ctx, c, validateOnly, reason, func(q *store.Queries, _ Caller, _ *SaveResult) (change, error) {
		if err := checkKey(t.Key); err != nil {
			return change{}, err
		}
		if t.ParentKey != "" && !definition.ValidKey(t.ParentKey) {
			return change{}, problem("parent_key", "üst etiket anahtarı geçersiz")
		}
		if err := errors.Join(checkPosition(t.Position), checkLabels(t.Labels), checkStatus(t.Status)); err != nil {
			return change{}, firstProblem(err)
		}
		cur, err := q.AdminGetCatalogStudyTagForUpdate(ctx, t.Key)
		ok, err := found(err)
		if err != nil {
			return change{}, fmt.Errorf("ders/konu etiketi: %w", err)
		}
		if err := checkCreate(create, ok); err != nil {
			return change{}, err
		}
		ch := change{action: ActionStudyTagCreate, key: t.Key, changed: true, published: true}
		if create {
			if err := s.insertStudyTag(ctx, q, t); err != nil {
				return change{}, err
			}
		} else {
			ch.action = ActionStudyTagUpdate
			if cur.ParentKey != t.ParentKey {
				return change{}, problem("parent_key", "etiketin üst etiketi değişmez")
			}
			ch.changed = cur.Position != t.Position || cur.Status != t.Status || !maps.Equal(readLabels(cur.Labels), t.Labels)
			if !ch.changed {
				return ch, nil
			}
			if err := q.AdminUpdateCatalogStudyTag(ctx, store.AdminUpdateCatalogStudyTagParams{
				Key: t.Key, Position: t.Position, Labels: labelsJSON(t.Labels), Status: t.Status,
			}); err != nil {
				return change{}, dbProblem(fmt.Errorf("ders/konu etiketi yazılamadı: %w", err), "key")
			}
		}
		tags, err := catalog.StudyTags(ctx, q)
		if err != nil {
			return change{}, err
		}
		if err := catalog.CheckStudyTags(tags); err != nil {
			return change{}, problem("key", "liste kuralı: "+err.Error())
		}
		return ch, nil
	})
}

// insertStudyTag adds a new tag and, for a level or a subject that is not
// itself a "Diğer", its "Diğer" child.
func (s *Service) insertStudyTag(ctx context.Context, q *store.Queries, t catalog.StudyTag) error {
	depth := catalog.StudyLevel
	if t.ParentKey != "" {
		p, err := q.GetCatalogStudyTag(ctx, t.ParentKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem("parent_key", "üst etiket listede yok")
		}
		if err != nil {
			return fmt.Errorf("üst etiket: %w", err)
		}
		depth = int(p.Depth) + 1
		if depth > catalog.StudyTopic {
			return problem("parent_key", "liste en fazla üç seviyedir: düzey, ders, konu")
		}
	}
	other := !strings.HasSuffix(t.Key, catalog.OtherSuffix) && depth < catalog.StudyTopic
	if other && len(t.Key)+len(catalog.OtherSuffix) > 40 {
		return problem("key", "düzey ve ders anahtarı en fazla 34 karakter olabilir (Diğer etiketi için)")
	}
	parent := pgtype.Text{String: t.ParentKey, Valid: t.ParentKey != ""}
	if err := q.AdminInsertCatalogStudyTag(ctx, store.AdminInsertCatalogStudyTagParams{
		Key: t.Key, ParentKey: parent, Position: t.Position, Labels: labelsJSON(t.Labels), Status: t.Status,
	}); err != nil {
		return dbProblem(fmt.Errorf("ders/konu etiketi yazılamadı: %w", err), "key")
	}
	if !other {
		return nil
	}
	if err := q.AdminInsertCatalogStudyTag(ctx, store.AdminInsertCatalogStudyTagParams{
		Key: t.Key + catalog.OtherSuffix, ParentKey: pgtype.Text{String: t.Key, Valid: true}, Position: otherPosition,
		Labels: labelsJSON(otherLabels), Status: catalog.StatusActive,
	}); err != nil {
		return dbProblem(fmt.Errorf("diğer etiketi yazılamadı: %w", err), "key")
	}
	return nil
}

// --- Score presets ---

// SaveScorePreset creates or changes a score preset. Tables already set up
// keep the rules they were set up with. Presets are not part of GET
// /v1/catalog: the catalog's version stays as it is.
func (s *Service) SaveScorePreset(ctx context.Context, c auth.AdminClaims, p ScorePreset, create, validateOnly bool, reason string) (SaveResult, error) {
	return s.save(ctx, c, validateOnly, reason, func(q *store.Queries, _ Caller, _ *SaveResult) (change, error) {
		if err := checkKey(p.Key); err != nil {
			return change{}, err
		}
		if err := errors.Join(checkPosition(p.Position), checkLabels(p.Labels), checkStatus(p.Status)); err != nil {
			return change{}, firstProblem(err)
		}
		if len(p.Params) > maxPresetParams {
			return change{}, problem("params", "kurallar en fazla 1 KiB olabilir")
		}
		params := compactJSON(p.Params)
		if len(params) == 0 || params[0] != '{' || !json.Valid(params) {
			return change{}, problem("params", "kurallar bir JSON nesnesi olmalı")
		}
		labels := labelsJSON(p.Labels)
		if _, err := scoreboard.ParsePreset(p.Key, labels, params, p.Status); err != nil {
			return change{}, problem("params", "kurallar geçersiz: yalnızca players_min, players_max, side, rounds, target, winner, result ve best_of, izin verilen değerlerle")
		}
		cur, err := q.AdminGetScorePresetForUpdate(ctx, p.Key)
		ok, err := found(err)
		if err != nil {
			return change{}, fmt.Errorf("ön ayar: %w", err)
		}
		if err := checkCreate(create, ok); err != nil {
			return change{}, err
		}
		ch := change{action: ActionPresetCreate, key: p.Key, changed: true}
		pos := int16(p.Position) // #nosec G115 -- checkPosition bounds it to 0–9999
		if create {
			err = q.AdminInsertScorePreset(ctx, store.AdminInsertScorePresetParams{
				Key: p.Key, Position: pos, Labels: labels, Params: params, Status: p.Status,
			})
		} else {
			ch.action = ActionPresetUpdate
			ch.changed = cur.Position != pos || cur.Status != p.Status ||
				!maps.Equal(readLabels(cur.Labels), p.Labels) || !sameJSON(cur.Params, params)
			if !ch.changed {
				return ch, nil
			}
			err = q.AdminUpdateScorePreset(ctx, store.AdminUpdateScorePresetParams{
				Key: p.Key, Position: pos, Labels: labels, Params: params, Status: p.Status,
			})
		}
		if err != nil {
			return change{}, dbProblem(fmt.Errorf("ön ayar yazılamadı: %w", err), "params")
		}
		return ch, nil
	})
}

// --- Blocked domains ---

// BlockDomain adds a domain to the blocked domain list (ADR-0040 §3). The
// list is not part of GET /v1/catalog.
func (s *Service) BlockDomain(ctx context.Context, c auth.AdminClaims, domain, reason string) error {
	_, err := s.save(ctx, c, false, reason, func(q *store.Queries, cl Caller, _ *SaveResult) (change, error) {
		d, ok := link.Domain(strings.TrimSpace(domain))
		if !ok {
			return change{}, problem("domain", "geçerli bir alan adı değil")
		}
		n, err := q.AdminInsertBlockedDomain(ctx, store.AdminInsertBlockedDomainParams{Domain: d, CreatedBy: cl.User.PG()})
		if err != nil {
			return change{}, dbProblem(fmt.Errorf("engelli alan adı yazılamadı: %w", err), "domain")
		}
		if n != 1 {
			return change{}, problem("domain", "alan adı zaten listede")
		}
		return change{action: ActionDomainBlock, key: d, changed: true}, nil
	})
	return err
}

// UnblockDomain removes a domain from the list; its materials show again.
func (s *Service) UnblockDomain(ctx context.Context, c auth.AdminClaims, domain, reason string) error {
	_, err := s.save(ctx, c, false, reason, func(q *store.Queries, _ Caller, _ *SaveResult) (change, error) {
		d, ok := link.Domain(strings.TrimSpace(domain))
		if !ok {
			return change{}, problem("domain", "geçerli bir alan adı değil")
		}
		n, err := q.AdminDeleteBlockedDomain(ctx, d)
		if err != nil {
			return change{}, fmt.Errorf("engelli alan adı silinemedi: %w", err)
		}
		if n != 1 {
			return change{}, problem("domain", "alan adı listede yok")
		}
		return change{action: ActionDomainUnblock, key: d, changed: true}, nil
	})
	return err
}

// Package definition is the closed schema of an event type's definition
// (catalog_event_types.definition; ADR-0025 §3–§4, ADR-0037 §1–§3).
//
// A definition is purely declarative: which fields an event of the type
// has, which components it is built from and with which parameters. Parse
// accepts only the keys, field kinds, component kinds, parameters and
// units listed here; anything else is refused, so that a taken-over
// catalog can at worst hold a meaningless type (ADR-0011, ADR-0025
// "Gerekçe"). There are no expressions, scripts, templates, HTML, URLs or
// money fields, and no way to write one.
//
// The package imports only the standard library: the validator is the same
// for the catalog's seed, the platform panel and the tests.
package definition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBytes bounds a definition's JSON text (ADR-0025 §3: 16 KiB).
const MaxBytes = 16 << 10

// Limits of a definition.
const (
	MaxFields     = 20
	MaxComponents = 20
	// MaxLogFields bounds the fields of one log record.
	MaxLogFields = 8
	// MaxLogStats bounds a log's statistics (ADR-0037 §3).
	MaxLogStats = 3
	// MaxTimerPresets bounds a timer's presets (ADR-0037 §3).
	MaxTimerPresets = 6
	// MaxScorePresets bounds the presets a scoreboard offers.
	MaxScorePresets = 20
	// MaxStudySelected bounds a study_tags value (ADR-0043).
	MaxStudySelected = 10
	MaxOptions       = 50
	MaxLanguages     = 5
	labelMax         = 60
	textMax          = 120
	longTextMax      = 1000
	// numberMax bounds every numeric limit and value.
	numberMax = 1_000_000_000
	// durationMax is 30 days in minutes, an event's longest duration.
	durationMax = 30 * 24 * 60
	decimalsMax = 3
)

// Field kinds (ADR-0025 §3, ADR-0043). The list is closed: there is no
// money, URL, file, location or person field.
const (
	KindText        = "text"
	KindLongText    = "long_text"
	KindInteger     = "integer"
	KindDecimal     = "decimal"
	KindChoice      = "choice"
	KindMultiChoice = "multi_choice"
	KindBoolean     = "boolean"
	KindDate        = "date"
	KindDuration    = "duration"
	// KindStudyTags is a choice from Tayfam's study tag list
	// (catalog_study_tags, ADR-0040 §5; ADR-0043). Its value is a list of
	// tag keys; the list itself is not in the definition, so the value is
	// checked against it by catalog.CheckStudyValues.
	KindStudyTags = "study_tags"
)

// FieldKinds are the field kinds, in the order of ADR-0025 §3.
var FieldKinds = []string{
	KindText, KindLongText, KindInteger, KindDecimal, KindChoice,
	KindMultiChoice, KindBoolean, KindDate, KindDuration, KindStudyTags,
}

// Depths of the study tag list (ADR-0040 §5): level → subject → topic.
// They match catalog.StudyLevel, StudySubject and StudyTopic.
const (
	StudyDepthLevel   = 1
	StudyDepthSubject = 2
	StudyDepthTopic   = 3
)

// studyDepths names the depths in a definition, by depth − 1.
var studyDepths = []string{"level", "subject", "topic"}

// Component kinds (ADR-0025 §4, ADR-0037 §3).
const (
	CompRoute        = "route"
	CompMeetingPoint = "meeting_point"
	CompSchedule     = "schedule"
	CompChecklist    = "checklist"
	CompRollCall     = "roll_call"
	CompVehicles     = "vehicles"
	CompTeams        = "teams"
	CompDraw         = "draw"
	CompBracket      = "bracket"
	CompScoreboard   = "scoreboard"
	CompRanking      = "ranking"
	CompVote         = "vote"
	CompCelebration  = "celebration"
	CompMeasure      = "measure"
	CompGoal         = "goal"
	CompLog          = "log"
	CompQuestions    = "questions"
	CompTimer        = "timer"
	CompPoolLink     = "pool_link"
)

// ComponentKinds are the component kinds, in the order of ADR-0037 §3.
// Attendance, capacity, check-in and the album are the base of every event
// and are not components.
var ComponentKinds = []string{
	CompRoute, CompMeetingPoint, CompSchedule, CompChecklist, CompRollCall,
	CompVehicles, CompTeams, CompDraw, CompBracket, CompScoreboard,
	CompRanking, CompVote, CompCelebration, CompMeasure, CompGoal, CompLog,
	CompQuestions, CompTimer, CompPoolLink,
}

// Units is the closed list of measurement units (ADR-0025 §4, ADR-0037
// §3). Their names are in the client's translation file.
var Units = []string{
	"kg", "piece", "bag", "km", "m", "altitude_m", "page", "minute", "hour",
	"unit", "goal", "point", "night", "nm", "box", "section", "net",
}

// unitMinute is the unit of a duration field.
const unitMinute = "minute"

// keyPattern is the form of every key: type, interest, field, component,
// option, preset and vocabulary namespace (ADR-0025 §2).
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// optionPattern is the form of a choice option; unlike a key it may be a
// single character or start with a digit ("1" … "5").
var optionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,39}$`)

var langPattern = regexp.MustCompile(`^[a-z]{2}$`)

// ValidKey reports whether s has the form of a catalog key.
func ValidKey(s string) bool { return keyPattern.MatchString(s) }

// Error reports the first place a definition breaks the schema. Path names
// the place ("components[2].params.unit"); the rejected value is not kept.
type Error struct {
	Path   string
	Reason string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "tanım: " + e.Reason
	}
	return "tanım: " + e.Path + ": " + e.Reason
}

// Definition is a validated definition.
type Definition struct {
	Fields     []Field
	Components []Component
	Flags      Flags
}

// Flags are the type-level flags (ADR-0037 §1).
type Flags struct {
	// SensitiveAttendance: attending is kept out of the personal history,
	// statistics and the friendship score; only the organizer sees who
	// came.
	SensitiveAttendance bool
	// AlbumNotice: an album upload needs the uploader's consent
	// acknowledgement.
	AlbumNotice bool
	// PersonalScopeForbidden: no per-person value may be defined (ADR-0025
	// §4, health data): no measure with scope personal and no log.
	PersonalScopeForbidden bool
}

// Field is one field of a type, or of a log record.
type Field struct {
	Key      string
	Kind     string
	Required bool
	// Labels maps a language to the field's name.
	Labels map[string]string
	// MaxLength is set for text and long_text.
	MaxLength int
	// Min and Max bound integer, decimal and duration (minutes) values.
	Min, Max float64
	// Decimals is set for decimal.
	Decimals int
	// Unit is the optional unit of an integer or decimal field; a duration
	// field is in minutes.
	Unit string
	// Options are the values of choice and multi_choice.
	Options []string
	// OptionLabels maps an option to its names by language; options
	// without an entry are shown as they are ("1" … "5").
	OptionLabels map[string]map[string]string
	// MaxSelected bounds a multi_choice or study_tags value.
	MaxSelected int
	// MinDepth and MaxDepth bound the depth of a study_tags field's tags
	// (StudyDepthLevel … StudyDepthTopic).
	MinDepth, MaxDepth int
	// Vocabulary is the tayfa vocabulary namespace of a text field, or ""
	// (ADR-0037 §4).
	Vocabulary string
}

// Component is one component of a type.
type Component struct {
	Kind string
	// Key is the component's key within the type; the kind when the
	// definition gives none.
	Key string
	// Labels is the component's own name (a measure's "Mesafe"); may be
	// empty for kinds that need none.
	Labels map[string]string
	// Optional is nil for a component that is always on; otherwise it
	// points at the default the organizer may change (ADR-0037 §2).
	Optional *bool
	// Params holds the parameters by name, except a log's fields.
	Params map[string]Param
	// LogFields are the record fields of a log.
	LogFields []Field
}

// Param is a parameter's value. Value is a bool, int64, float64, string,
// []string or []int64, depending on the parameter. For a parameter the
// organizer chooses (ADR-0037 §2) Choices lists the allowed values and
// Value is the default.
type Param struct {
	Value   any
	Choices []any
}

// String returns the parameter's value when it is a string.
func (p Param) String() string {
	s, _ := p.Value.(string)
	return s
}

// Bool returns the parameter's value when it is a bool.
func (p Param) Bool() bool {
	b, _ := p.Value.(bool)
	return b
}

// Field returns the type field with this key.
func (d *Definition) Field(key string) (Field, bool) {
	for _, f := range d.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Component returns the component with this key.
func (d *Definition) Component(key string) (Component, bool) {
	for _, c := range d.Components {
		if c.Key == key {
			return c, true
		}
	}
	return Component{}, false
}

// ---------------------------------------------------------------------------
// Parameter schemas

type paramKind int

const (
	pBool paramKind = iota
	// pInt is an integer within lo..hi.
	pInt
	// pNumber is a finite number within lo..hi.
	pNumber
	// pEnum is one of enum.
	pEnum
	// pIntSet is one of ints.
	pIntSet
	// pKey is a key (a reference, checked by the kind's own rule).
	pKey
	// pKeyList is a list of distinct keys, at most hi.
	pKeyList
	// pIntList is a list of distinct integers within lo..hi, at most n.
	pIntList
	// pStats is a log's statistics list.
	pStats
	// pFields is a log's record fields.
	pFields
)

type paramSpec struct {
	name     string
	kind     paramKind
	required bool
	// choosable: the definition may offer a list for the organizer to
	// choose from instead of a value (organizer_choice).
	choosable bool
	lo, hi    float64
	n         int
	enum      []string
	ints      []int64
}

type kindSpec struct {
	// multi: a type may hold the kind more than once (with distinct keys).
	multi bool
	// keyed: the definition must give the component a key and labels (the
	// key names its data and statistics).
	keyed  bool
	params []paramSpec
}

// Enumerated parameter values.
var (
	ChecklistLabels = []string{"gear", "task", "section", "duty", "item"}
	TeamModes       = []string{"random", "balanced", "manual"}
	BracketFormats  = []string{"elimination", "league", "swiss"}
	ScoreSides      = []string{"player", "team", "pair"}
	ScoreRounds     = []string{"round", "single", "first_to"}
	ScoreWinners    = []string{"high", "low", "first"}
	ScoreResults    = []string{"points", "wdl"}
	RankingMetrics  = []string{"duration", "rank"}
	Directions      = []string{"asc", "desc"}
	// VoteSources are where a vote's options come from (ADR-0037 §3; the
	// platform owner dropped route_stops on 2026-11-23, issue #254).
	VoteSources   = []string{"free", "participants", "vehicles", "album"}
	MeasureScopes = []string{"personal", "total"}
	EnteredBy     = []string{"self", "organizer"}
	// LocationLevels are who sees a log record's location (ADR-0025 §4).
	LocationLevels = []string{"self", "participants", "tayfa"}
	TimerModes     = []string{"countdown", "stopwatch", "chess_clock"}
	bestOf         = []int64{1, 3, 5}
)

// kinds is the parameter schema of every component kind. No kind has a
// money, stake or prize parameter (ADR-0025 §4).
var kinds = map[string]kindSpec{
	CompRoute:        {},
	CompMeetingPoint: {},
	CompSchedule: {params: []paramSpec{
		{name: "max_items", kind: pInt, lo: 1, hi: 100},
	}},
	CompChecklist: {multi: true, params: []paramSpec{
		{name: "label", kind: pEnum, enum: ChecklistLabels},
		{name: "assignment", kind: pBool},
		{name: "quantity", kind: pBool},
	}},
	CompRollCall: {params: []paramSpec{
		{name: "alarm", kind: pBool},
		{name: "return_grace_minutes", kind: pInt, lo: 1, hi: 1440},
	}},
	CompVehicles: {params: []paramSpec{
		{name: "plate", kind: pBool},
		{name: "seats", kind: pBool},
	}},
	CompTeams: {params: []paramSpec{
		{name: "mode", kind: pEnum, enum: TeamModes, choosable: true},
		{name: "team_size", kind: pInt, lo: 1, hi: 50},
		{name: "team_count", kind: pInt, lo: 2, hi: 50},
	}},
	CompDraw: {},
	CompBracket: {params: []paramSpec{
		{name: "format", kind: pEnum, enum: BracketFormats, choosable: true},
		{name: "best_of", kind: pIntSet, ints: bestOf, choosable: true},
	}},
	CompScoreboard: {multi: true, params: []paramSpec{
		{name: "side", kind: pEnum, enum: ScoreSides},
		{name: "tables", kind: pBool},
		{name: "presets", kind: pKeyList, hi: MaxScorePresets},
		{name: "rounds", kind: pEnum, enum: ScoreRounds, choosable: true},
		{name: "target", kind: pInt, lo: 1, hi: 100000},
		{name: "winner", kind: pEnum, enum: ScoreWinners, choosable: true},
		{name: "result", kind: pEnum, enum: ScoreResults},
		{name: "best_of", kind: pIntSet, ints: bestOf, choosable: true},
		{name: "guests", kind: pBool},
	}},
	CompRanking: {multi: true, params: []paramSpec{
		{name: "source", kind: pKey},
		{name: "metric", kind: pEnum, enum: RankingMetrics},
		{name: "direction", kind: pEnum, enum: Directions},
	}},
	CompVote: {multi: true, params: []paramSpec{
		{name: "source", kind: pEnum, enum: VoteSources, required: true},
	}},
	CompCelebration: {multi: true, params: []paramSpec{
		{name: "source", kind: pKey},
	}},
	CompMeasure: {multi: true, keyed: true, params: []paramSpec{
		{name: "unit", kind: pEnum, enum: Units, required: true},
		{name: "scope", kind: pEnum, enum: MeasureScopes, required: true},
		{name: "entered_by", kind: pEnum, enum: EnteredBy, required: true},
		{name: "min", kind: pNumber, lo: -numberMax, hi: numberMax},
		{name: "max", kind: pNumber, lo: -numberMax, hi: numberMax},
		{name: "decimals", kind: pInt, lo: 0, hi: decimalsMax},
		{name: "showcase", kind: pBool},
		{name: "sensitive", kind: pBool},
		{name: "prefill_from", kind: pKey},
	}},
	CompGoal: {multi: true, params: []paramSpec{
		{name: "measure", kind: pKey, required: true},
		{name: "default_target", kind: pNumber, lo: 0, hi: numberMax},
	}},
	CompLog: {multi: true, keyed: true, params: []paramSpec{
		{name: "fields", kind: pFields, required: true},
		{name: "location", kind: pBool},
		{name: "location_default", kind: pEnum, enum: LocationLevels},
		{name: "stat", kind: pStats},
		{name: "max_entries", kind: pInt, lo: 1, hi: 500},
	}},
	CompQuestions: {params: []paramSpec{
		{name: "anonymous", kind: pBool},
	}},
	CompTimer: {params: []paramSpec{
		{name: "mode", kind: pEnum, enum: TimerModes, choosable: true},
		{name: "presets", kind: pIntList, lo: 1, hi: 86400, n: MaxTimerPresets},
	}},
	CompPoolLink: {params: []paramSpec{
		{name: "max_links", kind: pInt, lo: 1, hi: 50},
	}},
}

// celebrationSources are the kinds a celebration may announce the winner
// of.
var celebrationSources = []string{CompScoreboard, CompBracket, CompRanking, CompVote}

// ---------------------------------------------------------------------------
// Parse

// Parse validates a definition's JSON text and returns it. The text must
// be one JSON object of at most MaxBytes; a definition read back from the
// database should be compacted first (jsonb adds spaces).
func Parse(raw []byte) (*Definition, error) {
	if len(raw) > MaxBytes {
		return nil, &Error{Reason: "16 KiB sınırını aşıyor"}
	}
	root, err := decode(raw)
	if err != nil {
		return nil, &Error{Reason: "geçerli bir JSON nesnesi değil"}
	}
	p := &parser{}
	d := p.definition(root)
	if p.err != nil {
		return nil, p.err
	}
	return d, nil
}

// decode reads exactly one JSON value, keeping numbers as written.
func decode(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("utf-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("fazladan veri")
	}
	return v, nil
}

// parser keeps the first error; every method is a no-op after one.
type parser struct {
	err *Error
}

func (p *parser) fail(path, reason string) {
	if p.err == nil {
		p.err = &Error{Path: path, Reason: reason}
	}
}

func (p *parser) failed() bool { return p.err != nil }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func index(path string, i int) string { return path + "[" + strconv.Itoa(i) + "]" }

// object checks that v is an object with only the allowed keys.
func (p *parser) object(v any, path string, allowed ...string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(path, "nesne olmalı")
		return nil
	}
	// Sorted, so that the reported key does not depend on map order.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			p.fail(join(path, printable(k)), "bilinmeyen anahtar")
			return nil
		}
	}
	return m
}

// printable keeps an unknown key readable and short in an error path.
func printable(k string) string {
	if len(k) > 40 || !keyPattern.MatchString(k) {
		return "?"
	}
	return k
}

func (p *parser) array(v any, path string, lo, hi int) []any {
	a, ok := v.([]any)
	if !ok {
		p.fail(path, "dizi olmalı")
		return nil
	}
	if len(a) < lo || len(a) > hi {
		p.fail(path, fmt.Sprintf("%d–%d öğe olmalı", lo, hi))
		return nil
	}
	return a
}

func (p *parser) str(v any, path string) string {
	s, ok := v.(string)
	if !ok {
		p.fail(path, "metin olmalı")
	}
	return s
}

func (p *parser) boolean(v any, path string) bool {
	b, ok := v.(bool)
	if !ok {
		p.fail(path, "true ya da false olmalı")
	}
	return b
}

// integer accepts only an integer literal ("3", not "3.0" or "3e0").
func (p *parser) integer(v any, path string, lo, hi int64) int64 {
	n, ok := v.(json.Number)
	if !ok {
		p.fail(path, "tam sayı olmalı")
		return 0
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		p.fail(path, "tam sayı olmalı")
		return 0
	}
	if i < lo || i > hi {
		p.fail(path, fmt.Sprintf("%d–%d aralığında olmalı", lo, hi))
		return 0
	}
	return i
}

// small is integer for a value that is kept as an int.
func (p *parser) small(v any, path string, lo, hi int) int {
	n, ok := v.(json.Number)
	if !ok {
		p.fail(path, "tam sayı olmalı")
		return 0
	}
	i, err := strconv.Atoi(n.String())
	if err != nil {
		p.fail(path, "tam sayı olmalı")
		return 0
	}
	if i < lo || i > hi {
		p.fail(path, fmt.Sprintf("%d–%d aralığında olmalı", lo, hi))
		return 0
	}
	return i
}

func (p *parser) number(v any, path string, lo, hi float64) float64 {
	n, ok := v.(json.Number)
	if !ok {
		p.fail(path, "sayı olmalı")
		return 0
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		p.fail(path, "sayı olmalı")
		return 0
	}
	if f < lo || f > hi {
		p.fail(path, "aralık dışında")
		return 0
	}
	return f
}

func (p *parser) key(v any, path string) string {
	s := p.str(v, path)
	if p.failed() {
		return ""
	}
	if !keyPattern.MatchString(s) {
		p.fail(path, "anahtar biçiminde değil")
		return ""
	}
	return s
}

func (p *parser) enum(v any, path string, allowed []string) string {
	s := p.str(v, path)
	if p.failed() {
		return ""
	}
	if !slices.Contains(allowed, s) {
		p.fail(path, "bilinmeyen değer")
		return ""
	}
	return s
}

// labels reads a language → text map. required asks for a Turkish entry.
func (p *parser) labels(v any, path string, required bool) map[string]string {
	if v == nil {
		if required {
			p.fail(path, "gerekli")
		}
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(path, "nesne olmalı")
		return nil
	}
	if len(m) == 0 || len(m) > MaxLanguages {
		p.fail(path, fmt.Sprintf("1–%d dil olmalı", MaxLanguages))
		return nil
	}
	langs := make([]string, 0, len(m))
	for l := range m {
		langs = append(langs, l)
	}
	slices.Sort(langs)
	out := make(map[string]string, len(m))
	for _, l := range langs {
		if !langPattern.MatchString(l) {
			p.fail(path, "dil kodu iki küçük harf olmalı")
			return nil
		}
		s := p.str(m[l], join(path, l))
		if p.failed() {
			return nil
		}
		if !ValidLabel(s) {
			p.fail(join(path, l), fmt.Sprintf("1–%d karakter, baştaki ve sondaki boşluk ve kontrol karakteri olmadan", labelMax))
			return nil
		}
		out[l] = s
	}
	if _, ok := out["tr"]; !ok {
		p.fail(path, "Türkçe (tr) metin gerekli")
		return nil
	}
	return out
}

// ValidLabel reports whether s can be a catalog label: 1–60 characters,
// trimmed, one line, no control or invisible formatting characters.
func ValidLabel(s string) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > labelMax || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func (p *parser) definition(root any) *Definition {
	m := p.object(root, "", "fields", "components", "flags")
	if p.failed() {
		return nil
	}
	d := &Definition{}
	if v, ok := m["flags"]; ok {
		f := p.object(v, "flags", "sensitive_attendance", "album_notice", "personal_scope_forbidden")
		if p.failed() {
			return nil
		}
		for name, dst := range map[string]*bool{
			"sensitive_attendance":     &d.Flags.SensitiveAttendance,
			"album_notice":             &d.Flags.AlbumNotice,
			"personal_scope_forbidden": &d.Flags.PersonalScopeForbidden,
		} {
			if fv, ok := f[name]; ok {
				*dst = p.boolean(fv, join("flags", name))
			}
		}
	}
	if v, ok := m["fields"]; ok {
		d.Fields = p.fields(v, "fields", MaxFields)
	}
	if v, ok := m["components"]; ok {
		d.Components = p.components(v)
	}
	if p.failed() {
		return nil
	}
	p.crossCheck(d)
	return d
}

// fields reads a field list; keys are distinct.
func (p *parser) fields(v any, path string, limit int) []Field {
	a := p.array(v, path, 0, limit)
	out := make([]Field, 0, len(a))
	seen := map[string]bool{}
	for i, item := range a {
		f := p.field(item, index(path, i))
		if p.failed() {
			return nil
		}
		if seen[f.Key] {
			p.fail(join(index(path, i), "key"), "anahtar tekrar ediyor")
			return nil
		}
		seen[f.Key] = true
		out = append(out, f)
	}
	return out
}

// fieldKeys are the keys a field of each kind may have, besides the common
// ones.
var fieldKeys = map[string][]string{
	KindText:        {"max_length", "vocabulary"},
	KindLongText:    {"max_length"},
	KindInteger:     {"min", "max", "unit"},
	KindDecimal:     {"min", "max", "decimals", "unit"},
	KindChoice:      {"options", "option_labels"},
	KindMultiChoice: {"options", "option_labels", "max_selected"},
	KindBoolean:     {},
	KindDate:        {},
	KindDuration:    {"min", "max"},
	KindStudyTags:   {"min_depth", "max_depth", "max_selected"},
}

func (p *parser) field(v any, path string) Field {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(path, "nesne olmalı")
		return Field{}
	}
	var f Field
	f.Kind = p.enum(m["kind"], join(path, "kind"), FieldKinds)
	if p.failed() {
		return Field{}
	}
	allowed := append([]string{"key", "kind", "required", "labels"}, fieldKeys[f.Kind]...)
	if p.object(v, path, allowed...); p.failed() {
		return Field{}
	}
	f.Key = p.key(m["key"], join(path, "key"))
	f.Labels = p.labels(m["labels"], join(path, "labels"), true)
	if rv, ok := m["required"]; ok {
		f.Required = p.boolean(rv, join(path, "required"))
	}
	if p.failed() {
		return Field{}
	}
	switch f.Kind {
	case KindText, KindLongText:
		limit := textMax
		if f.Kind == KindLongText {
			limit = longTextMax
		}
		f.MaxLength = limit
		if lv, ok := m["max_length"]; ok {
			f.MaxLength = p.small(lv, join(path, "max_length"), 1, limit)
		}
		if vv, ok := m["vocabulary"]; ok {
			vm := p.object(vv, join(path, "vocabulary"), "namespace")
			if p.failed() {
				return Field{}
			}
			f.Vocabulary = p.key(vm["namespace"], join(path, "vocabulary.namespace"))
		}
	case KindInteger, KindDecimal, KindDuration:
		f.Min, f.Max = 0, numberMax
		lo, hi := float64(-numberMax), float64(numberMax)
		if f.Kind == KindDuration {
			f.Min, f.Max = 1, durationMax
			lo, hi = 1, durationMax
		}
		for name, dst := range map[string]*float64{"min": &f.Min, "max": &f.Max} {
			bv, ok := m[name]
			if !ok {
				continue
			}
			if f.Kind == KindDecimal {
				*dst = p.number(bv, join(path, name), lo, hi)
			} else {
				*dst = float64(p.integer(bv, join(path, name), int64(lo), int64(hi)))
			}
		}
		if !p.failed() && f.Min > f.Max {
			p.fail(join(path, "min"), "max değerinden büyük")
		}
		if f.Kind == KindDecimal {
			f.Decimals = 2
			if dv, ok := m["decimals"]; ok {
				f.Decimals = p.small(dv, join(path, "decimals"), 1, decimalsMax)
			}
		}
		if uv, ok := m["unit"]; ok {
			f.Unit = p.enum(uv, join(path, "unit"), Units)
		}
		if f.Kind == KindDuration {
			f.Unit = unitMinute
		}
	case KindChoice, KindMultiChoice:
		a := p.array(m["options"], join(path, "options"), 1, MaxOptions)
		for i, ov := range a {
			o := p.str(ov, index(join(path, "options"), i))
			if p.failed() {
				return Field{}
			}
			if !optionPattern.MatchString(o) {
				p.fail(index(join(path, "options"), i), "seçenek biçiminde değil")
				return Field{}
			}
			if slices.Contains(f.Options, o) {
				p.fail(index(join(path, "options"), i), "seçenek tekrar ediyor")
				return Field{}
			}
			f.Options = append(f.Options, o)
		}
		if lv, ok := m["option_labels"]; ok && !p.failed() {
			lm, ok := lv.(map[string]any)
			if !ok {
				p.fail(join(path, "option_labels"), "nesne olmalı")
				return Field{}
			}
			f.OptionLabels = make(map[string]map[string]string, len(lm))
			opts := make([]string, 0, len(lm))
			for o := range lm {
				opts = append(opts, o)
			}
			slices.Sort(opts)
			for _, o := range opts {
				if !slices.Contains(f.Options, o) {
					p.fail(join(path, "option_labels"), "seçeneklerde olmayan bir anahtar")
					return Field{}
				}
				f.OptionLabels[o] = p.labels(lm[o], join(join(path, "option_labels"), o), true)
			}
		}
		if f.Kind == KindMultiChoice && !p.failed() {
			f.MaxSelected = len(f.Options)
			if sv, ok := m["max_selected"]; ok {
				f.MaxSelected = p.small(sv, join(path, "max_selected"), 1, len(f.Options))
			}
		}
	case KindStudyTags:
		f.MinDepth, f.MaxDepth, f.MaxSelected = StudyDepthSubject, StudyDepthTopic, 1
		if dv, ok := m["min_depth"]; ok {
			f.MinDepth = slices.Index(studyDepths, p.enum(dv, join(path, "min_depth"), studyDepths[:StudyDepthSubject])) + 1
		}
		if dv, ok := m["max_depth"]; ok && !p.failed() {
			f.MaxDepth = slices.Index(studyDepths, p.enum(dv, join(path, "max_depth"), studyDepths)) + 1
		}
		if !p.failed() && f.MinDepth > f.MaxDepth {
			p.fail(join(path, "max_depth"), "min_depth değerinden sığ")
		}
		if sv, ok := m["max_selected"]; ok && !p.failed() {
			f.MaxSelected = p.small(sv, join(path, "max_selected"), 1, MaxStudySelected)
		}
	}
	if p.failed() {
		return Field{}
	}
	return f
}

func (p *parser) components(v any) []Component {
	const path = "components"
	a := p.array(v, path, 0, MaxComponents)
	out := make([]Component, 0, len(a))
	keys := map[string]bool{}
	kindSeen := map[string]bool{}
	for i, item := range a {
		ip := index(path, i)
		m := p.object(item, ip, "kind", "key", "labels", "optional", "params")
		if p.failed() {
			return nil
		}
		c := Component{Params: map[string]Param{}}
		c.Kind = p.enum(m["kind"], join(ip, "kind"), ComponentKinds)
		if p.failed() {
			return nil
		}
		spec := kinds[c.Kind]
		if kindSeen[c.Kind] && !spec.multi {
			p.fail(join(ip, "kind"), "bu bileşen bir türde yalnızca bir kez kullanılabilir")
			return nil
		}
		kindSeen[c.Kind] = true
		c.Key = c.Kind
		if kv, ok := m["key"]; ok {
			c.Key = p.key(kv, join(ip, "key"))
		} else if spec.keyed {
			p.fail(join(ip, "key"), "gerekli")
		}
		c.Labels = p.labels(m["labels"], join(ip, "labels"), spec.keyed)
		if p.failed() {
			return nil
		}
		if keys[c.Key] {
			p.fail(join(ip, "key"), "anahtar tekrar ediyor")
			return nil
		}
		keys[c.Key] = true
		if ov, ok := m["optional"]; ok {
			om := p.object(ov, join(ip, "optional"), "default")
			if p.failed() {
				return nil
			}
			def := p.boolean(om["default"], join(ip, "optional.default"))
			c.Optional = &def
		}
		var pm map[string]any
		if pv, ok := m["params"]; ok {
			names := make([]string, 0, len(spec.params))
			for _, s := range spec.params {
				names = append(names, s.name)
			}
			pm = p.object(pv, join(ip, "params"), names...)
		}
		if p.failed() {
			return nil
		}
		for _, s := range spec.params {
			pp := join(join(ip, "params"), s.name)
			pv, ok := pm[s.name]
			if !ok {
				if s.required {
					p.fail(pp, "gerekli")
					return nil
				}
				continue
			}
			if s.kind == pFields {
				c.LogFields = p.fields(pv, pp, MaxLogFields)
				if !p.failed() && len(c.LogFields) == 0 {
					p.fail(pp, "en az bir alan olmalı")
				}
			} else {
				c.Params[s.name] = p.param(pv, pp, s)
			}
			if p.failed() {
				return nil
			}
		}
		out = append(out, c)
	}
	return out
}

// param reads one parameter: a value, or a list of choices with a default
// for a parameter the organizer chooses.
func (p *parser) param(v any, path string, s paramSpec) Param {
	if m, ok := v.(map[string]any); ok {
		if !s.choosable {
			p.fail(path, "bu parametre organizatöre bırakılamaz")
			return Param{}
		}
		if p.object(v, path, "organizer_choice", "default"); p.failed() {
			return Param{}
		}
		a := p.array(m["organizer_choice"], join(path, "organizer_choice"), 2, 10)
		var out Param
		for i, cv := range a {
			val := p.scalar(cv, index(join(path, "organizer_choice"), i), s)
			if p.failed() {
				return Param{}
			}
			if slices.Contains(out.Choices, val) {
				p.fail(index(join(path, "organizer_choice"), i), "seçenek tekrar ediyor")
				return Param{}
			}
			out.Choices = append(out.Choices, val)
		}
		dv, ok := m["default"]
		if !ok {
			p.fail(join(path, "default"), "gerekli")
			return Param{}
		}
		out.Value = p.scalar(dv, join(path, "default"), s)
		if !p.failed() && !slices.Contains(out.Choices, out.Value) {
			p.fail(join(path, "default"), "seçenekler arasında değil")
		}
		return out
	}
	switch s.kind {
	case pKeyList:
		a := p.array(v, path, 1, int(s.hi))
		keys := make([]string, 0, len(a))
		for i, kv := range a {
			k := p.key(kv, index(path, i))
			if p.failed() {
				return Param{}
			}
			if slices.Contains(keys, k) {
				p.fail(index(path, i), "anahtar tekrar ediyor")
				return Param{}
			}
			keys = append(keys, k)
		}
		return Param{Value: keys}
	case pIntList:
		a := p.array(v, path, 1, s.n)
		ints := make([]int64, 0, len(a))
		for i, iv := range a {
			n := p.integer(iv, index(path, i), int64(s.lo), int64(s.hi))
			if p.failed() {
				return Param{}
			}
			if slices.Contains(ints, n) {
				p.fail(index(path, i), "değer tekrar ediyor")
				return Param{}
			}
			ints = append(ints, n)
		}
		return Param{Value: ints}
	case pStats:
		a := p.array(v, path, 1, MaxLogStats)
		stats := make([]string, 0, len(a))
		for i, sv := range a {
			st := p.str(sv, index(path, i))
			if p.failed() {
				return Param{}
			}
			if slices.Contains(stats, st) {
				p.fail(index(path, i), "değer tekrar ediyor")
				return Param{}
			}
			stats = append(stats, st)
		}
		return Param{Value: stats}
	}
	return Param{Value: p.scalar(v, path, s)}
}

func (p *parser) scalar(v any, path string, s paramSpec) any {
	switch s.kind {
	case pBool:
		return p.boolean(v, path)
	case pInt:
		return p.integer(v, path, int64(s.lo), int64(s.hi))
	case pNumber:
		return p.number(v, path, s.lo, s.hi)
	case pEnum:
		return p.enum(v, path, s.enum)
	case pIntSet:
		n := p.integer(v, path, math.MinInt32, math.MaxInt32)
		if !p.failed() && !slices.Contains(s.ints, n) {
			p.fail(path, "bilinmeyen değer")
		}
		return n
	case pKey:
		return p.key(v, path)
	}
	p.fail(path, "bu parametre tek değer alamaz")
	return nil
}

// crossCheck applies the rules that involve more than one part of the
// definition.
func (p *parser) crossCheck(d *Definition) {
	for i, c := range d.Components {
		pp := join(index("components", i), "params")
		switch c.Kind {
		case CompMeasure:
			p.checkMeasure(d, c, pp)
		case CompLog:
			p.checkLog(d, c, pp)
		case CompGoal:
			if t, ok := d.Component(c.Params["measure"].String()); !ok || t.Kind != CompMeasure {
				p.fail(join(pp, "measure"), "bu anahtarla bir ölçüm yok")
			}
		case CompRanking:
			p.checkRanking(d, c, pp)
		case CompCelebration:
			if src, ok := c.Params["source"]; ok {
				if t, ok := d.Component(src.String()); !ok || !slices.Contains(celebrationSources, t.Kind) {
					p.fail(join(pp, "source"), "kazananı olan bir bileşen değil")
				}
			}
		case CompVote:
			switch c.Params["source"].String() {
			case "vehicles":
				if !slices.ContainsFunc(d.Components, func(o Component) bool { return o.Kind == CompVehicles }) {
					p.fail(join(pp, "source"), "kaynak bileşen bu türde yok")
				}
			case "participants":
				// The options would list who came (issue #254; ADR-0037 §1).
				if d.Flags.SensitiveAttendance {
					p.fail(join(pp, "source"), "katılımı hassas türde katılımcı oylaması olmaz")
				}
			}
		}
		if p.failed() {
			return
		}
	}
}

// checkRanking checks a ranking (issue #253; ADR-0037 §3): either computed
// from a personal measure of the same definition (source) or entered by
// hand with a metric, never both. A total measure has a single value and
// ranks nobody.
func (p *parser) checkRanking(d *Definition, c Component, pp string) {
	src, hasSrc := c.Params["source"]
	_, hasMetric := c.Params["metric"]
	switch {
	case hasSrc && hasMetric:
		p.fail(join(pp, "metric"), "ölçümden hesaplanan derecenin ölçütü yoktur")
	case !hasSrc && !hasMetric:
		p.fail(join(pp, "metric"), "source ya da metric gerekli")
	case hasSrc:
		t, ok := d.Component(src.String())
		if !ok || t.Kind != CompMeasure {
			p.fail(join(pp, "source"), "bu anahtarla bir ölçüm yok")
			return
		}
		if t.Params["scope"].String() != "personal" {
			p.fail(join(pp, "source"), "kişisel bir ölçüm değil")
		}
	}
}

func (p *parser) checkMeasure(d *Definition, c Component, pp string) {
	scope, by := c.Params["scope"].String(), c.Params["entered_by"].String()
	if scope == "total" && by != "organizer" {
		p.fail(join(pp, "entered_by"), "toplam ölçümü organizatör girer")
		return
	}
	if scope == "personal" && d.Flags.PersonalScopeForbidden {
		p.fail(join(pp, "scope"), "bu türde kişi bazında değer tutulamaz")
		return
	}
	lo, hasLo := c.Params["min"].Value.(float64)
	hi, hasHi := c.Params["max"].Value.(float64)
	if hasLo && hasHi && lo > hi {
		p.fail(join(pp, "min"), "max değerinden büyük")
		return
	}
	if c.Params["sensitive"].Bool() {
		if scope != "personal" {
			p.fail(join(pp, "sensitive"), "yalnızca kişisel ölçüm hassas olabilir")
			return
		}
		if c.Params["showcase"].Bool() {
			p.fail(join(pp, "showcase"), "hassas ölçüm vitrine çıkamaz")
			return
		}
	}
	if pf, ok := c.Params["prefill_from"]; ok {
		f, ok := d.Field(pf.String())
		if !ok {
			p.fail(join(pp, "prefill_from"), "bu anahtarla bir alan yok")
			return
		}
		if f.Kind != KindInteger && f.Kind != KindDecimal && f.Kind != KindDuration {
			p.fail(join(pp, "prefill_from"), "alan sayısal değil")
			return
		}
		if f.Unit != c.Params["unit"].String() {
			p.fail(join(pp, "prefill_from"), "alan ile ölçüm aynı birimde değil")
		}
	}
}

func (p *parser) checkLog(d *Definition, c Component, pp string) {
	if d.Flags.PersonalScopeForbidden {
		p.fail(pp, "bu türde kişi bazında kayıt tutulamaz")
		return
	}
	// A record's fields are checked without the database (logbook), so a
	// record has no study tag field (ADR-0043 §1).
	for j, f := range c.LogFields {
		if f.Kind == KindStudyTags {
			p.fail(join(index(join(pp, "fields"), j), "kind"), "kayıt alanı ders/konu seçimi olamaz")
			return
		}
	}
	if _, ok := c.Params["location_default"]; ok && !c.Params["location"].Bool() {
		p.fail(join(pp, "location_default"), "konum kapalıyken verilemez")
		return
	}
	stats, _ := c.Params["stat"].Value.([]string)
	for i, st := range stats {
		sp := index(join(pp, "stat"), i)
		if st == "count" {
			continue
		}
		op, key, ok := strings.Cut(st, ":")
		if !ok || (op != "distinct" && op != "sum") {
			p.fail(sp, "count, distinct:<alan> ya da sum:<alan> olmalı")
			return
		}
		var field *Field
		for j := range c.LogFields {
			if c.LogFields[j].Key == key {
				field = &c.LogFields[j]
			}
		}
		switch {
		case field == nil:
			p.fail(sp, "bu anahtarla bir kayıt alanı yok")
		case op == "sum" && field.Kind != KindInteger && field.Kind != KindDecimal && field.Kind != KindDuration:
			p.fail(sp, "toplam yalnızca sayısal alanda alınır")
		case op == "distinct" && field.Kind != KindText && field.Kind != KindChoice:
			p.fail(sp, "farklı değer sayısı yalnızca metin ya da seçim alanında alınır")
		}
		if p.failed() {
			return
		}
	}
}

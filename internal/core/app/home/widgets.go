// Package home is the tayfa home page (issue #268; ADR-0027 §1–§4,
// ADR-0040): which widgets a tayfa shows, in what order and with what
// settings, and the data of the display widgets.
//
// The widget list is code (Widgets): a closed list, the same for every
// tayfa. Every tayfa may use every widget whatever its interests; the
// interests only decide which widgets are switched on by default
// (catalog_interests.default_widgets, AddDefaults). Tayfam puts no content
// of its own on the page.
//
// The layout is tayfa_home_widgets: one row per widget the tayfa has seen.
// "Sıradaki" (the next event) is not a widget: the client always shows it
// first. A widget without a row is off. A widget whose module
// (tayfa_modules) is off is not shown, even when its row is on.
//
// Who may do what (the permission table, ADR-0004):
//
//	read the layout and the data  every active member (authz.TayfaView)
//	change the layout             the admins (authz.TayfaHomeUpdate)
//
// # Adding a widget
//
// A widget of another issue (günün içeriği #269, ortak kütüphane #270, soru
// havuzu #271, materyal kütüphanesi #261, üye garajı #272) adds ONE line
// under its own heading in Widgets: its key, kind and the module it needs.
// Nothing else here changes: the layout, the defaults, the settings screen
// and GET /v1/tayfas/{id} pick it up. A module widget's card reads its
// module's own endpoints; GetHomeWidgetData serves the display widgets of
// this package only. A widget that needs settings (config) adds its schema
// to Config, cleanConfig and the contract in the same issue.
package home

import "slices"

// Kind is what a widget is (ADR-0027 §1).
type Kind string

// Widget kinds.
const (
	// KindSection is one of the home page's former fixed sections
	// (Yaklaşan, Duyurular, Açık anket, Son sorular): its module's list.
	KindSection Kind = "section"
	// KindDisplay shows data kept elsewhere (statistics, measures,
	// celebrations); it has no data of its own, or very little.
	KindDisplay Kind = "display"
	// KindModule is the showcase of a tayfa module with its own data and
	// endpoints (ADR-0040).
	KindModule Kind = "module"
)

// Modules a widget may need (tayfa_modules.module).
const (
	ModuleAnnouncements = "announcements"
	ModuleEvents        = "events"
	ModuleQA            = "qa"
	ModulePolls         = "polls"
)

// Widget keys of this issue.
const (
	KeyUpcoming      = "upcoming"
	KeyAnnouncements = "announcements"
	KeyPolls         = "polls"
	KeyQuestions     = "questions"

	KeyStats      = "stats"
	KeyGoal       = "goal"
	KeyCountdown  = "countdown"
	KeyChampions  = "champions"
	KeyLogSummary = "log_summary"
)

// Widget is one entry of the widget list.
type Widget struct {
	Key  string
	Kind Kind
	// Module is the tayfa module the widget belongs to; "" when it needs
	// none. While the module is off the widget is not shown.
	Module string
	// NeedsConfig: the widget cannot be switched on without its settings
	// (config), so it is never a default.
	NeedsConfig bool
}

// Widgets is the widget list, in the order the settings screen lists the
// widgets a tayfa has not placed yet. One line per widget, under its own
// heading, so that widgets developed side by side do not touch the same
// lines. Keep the headings and the empty lines between them.
func Widgets() []Widget {
	return []Widget{
		// Bugünkü sabit bölümler (ADR-0027 §3; Tayfam-App ADR-0015), bugünkü
		// sırasıyla.
		{Key: KeyUpcoming, Kind: KindSection, Module: ModuleEvents},
		{Key: KeyAnnouncements, Kind: KindSection, Module: ModuleAnnouncements},
		{Key: KeyPolls, Kind: KindSection, Module: ModulePolls},
		{Key: KeyQuestions, Kind: KindSection, Module: ModuleQA},

		// Gösterim bileşenleri (#268; ADR-0027 §4).
		{Key: KeyStats, Kind: KindDisplay},
		{Key: KeyGoal, Kind: KindDisplay, NeedsConfig: true},
		{Key: KeyCountdown, Kind: KindDisplay, NeedsConfig: true},
		{Key: KeyChampions, Kind: KindDisplay, Module: ModuleEvents},
		{Key: KeyLogSummary, Kind: KindDisplay, NeedsConfig: true},

		// daily (#269)
		{Key: "daily", Kind: KindModule, Module: "daily"},

		// library (#270)
		{Key: "library", Kind: KindModule, Module: "library"},

		// question_pool (#271)
		{Key: "question_pool", Kind: KindModule, Module: "question_pool"},

		// materials (#261)
		{Key: "materials", Kind: KindModule, Module: "materials"},

		// garage (#272)
	}
}

// Lookup returns the widget with key.
func Lookup(key string) (Widget, bool) {
	ws := Widgets()
	i := slices.IndexFunc(ws, func(w Widget) bool { return w.Key == key })
	if i < 0 {
		return Widget{}, false
	}
	return ws[i], true
}

// Sections are the keys of the former fixed sections, in their order: every
// new tayfa starts with them on.
func Sections() []string {
	var out []string
	for _, w := range Widgets() {
		if w.Kind == KindSection {
			out = append(out, w.Key)
		}
	}
	return out
}

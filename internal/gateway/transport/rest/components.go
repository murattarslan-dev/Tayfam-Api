package rest

import (
	"net/http"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// Event components (issue #238; ADR-0025 §4, §8). A component's routes
// live under
//
//	/v1/tayfas/{tayfa_id}/events/{event_id}/components/{component_key}/…
//
// in a file of its own (component_<kind>.go) that builds its gRPC client
// from the connection to core (componentHandlers.conn) and adds one line to
// componentRoutes. The first path segment after {component_key} belongs to
// one component kind (its name or its resource: "route", "items",
// "tables" …): two kinds must not register the same pattern.

// componentHandlers serves the component routes.
type componentHandlers struct {
	coreHandlers
	// conn is the connection to core; every component builds its own
	// client from it.
	conn grpc.ClientConnInterface
}

// componentPath returns the route pattern of a component resource:
// componentPath("GET", "items/{item_id}").
func componentPath(method, suffix string) string {
	return method + " /v1/tayfas/{tayfa_id}/events/{event_id}/components/{component_key}/" + suffix
}

// componentRef reads and checks the path's tayfa, event and component key;
// on a bad one it answers invalid-request and ok is false.
func componentRef(w http.ResponseWriter, r *http.Request) (ref *corev1.ComponentRef, ok bool) {
	id, eid, ok := pathTayfaEvent(w, r)
	if !ok {
		return nil, false
	}
	key := r.PathValue("component_key")
	if !catalogKeyPattern.MatchString(key) {
		badRequest(w, r)
		return nil, false
	}
	return &corev1.ComponentRef{TayfaId: id, EventId: eid, ComponentKey: key}, true
}

// componentRoutes is the gateway's component registry: one line per
// component, under its own heading, so that components developed side by
// side do not touch the same lines. Keep the headings and the empty lines
// between them.
func componentRoutes() []func(mux *http.ServeMux, h componentHandlers) {
	return []func(mux *http.ServeMux, h componentHandlers){
		// The framework's own routes: selections and referees.
		registerEventComponents,

		// route (#239)
		registerRouteComponent,

		// vehicles (#240)
		registerVehiclesComponent,

		// checklist (#241)
		registerChecklistComponent,

		// roll_call (#242)
		registerRollCallComponent,

		// log (#243)
		registerLogComponent,

		// tayfa vocabulary (#244)
		registerTayfaVocabulary,

		// measure (#245)
		registerMeasureComponent,

		// goal (#246)
		registerGoalComponent,

		// schedule (#247)
		registerScheduleComponent,

		// teams (#248)
		registerTeamsComponent,

		// draw (#249)
		registerDrawComponent,

		// scoreboard (#250)
		registerScoreboardComponent,

		// bracket (#251)
		registerBracketComponent,

		// guest players (#252)
		registerGuestPlayers,

		// ranking (#253)
		registerRankingComponent,

		// vote (#254)

		// celebration (#255)
		registerCelebrationComponent,

		// questions (#256)
		registerQuestionsComponent,

		// pool_link (#271)
		registerPoolLinkComponent,

		// tayfa statistics (#266)
		registerTayfaStats,

		// tayfa home widgets (#268)
		registerTayfaHome,

		// daily content (#269)
		registerTayfaDaily,

		// shared library (#270)
		registerTayfaLibrary,
	}
}

func registerComponents(mux *http.ServeMux, h coreHandlers) {
	ch := componentHandlers{coreHandlers: h, conn: h.d.Components}
	for _, register := range componentRoutes() {
		register(mux, ch)
	}
}

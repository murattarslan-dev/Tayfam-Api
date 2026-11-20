package grpc

import (
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
)

// componentRegistrar registers one component's gRPC service on srv. It
// builds the component's app service on the framework fw; the function
// lives in the component's own file (component_<kind>.go).
type componentRegistrar func(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error

// componentRegistrars is core's component registry (issue #238): one line
// per component, under its own heading, so that components developed side
// by side do not touch the same lines. Keep the headings and the empty
// lines between them.
func componentRegistrars() []componentRegistrar {
	return []componentRegistrar{
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

		// draw (#249)

		// scoreboard (#250)

		// bracket (#251)

		// guest players (#252)

		// ranking (#253)

		// vote (#254)

		// celebration (#255)

		// questions (#256)

		// pool_link (#271)
	}
}

// RegisterComponents registers EventComponentService (the framework's own
// methods) and the service of every component in the registry.
func RegisterComponents(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterEventComponentServiceServer(srv, NewEventComponentServer(fw, log))
	for _, register := range componentRegistrars() {
		if err := register(srv, fw, log); err != nil {
			return err
		}
	}
	return nil
}

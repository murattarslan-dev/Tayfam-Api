// Package component is the framework every event component is built on
// (issue #238; ADR-0025 §4–§6, ADR-0037 §2).
//
// A component (route, checklist, scoreboard …) is code; an event type is
// data that lists the components an event of the type has (package
// definition). This package holds what the components share, so that each
// of them is only its own data and rules:
//
//   - Resolving a component of an event: the caller's standing in the
//     tayfa (access.Load, events module on), the event loaded by tayfa and
//     event ID together (IDOR), the type version the event was created
//     with, the component with the given key and kind in that version
//     (apperr.ErrComponentNotFound), and whether it is switched on
//     (apperr.ErrComponentDisabled). View and Change do all of it and hand
//     a Scope to the component.
//   - The organizer's selections (event_components): optional components
//     on or off and the parameters the definition leaves to the organizer
//     (List, Set, Apply). They are locked once the event has started
//     (apperr.ErrComponentLocked).
//   - The write window: no change to a cancelled event
//     (apperr.ErrEventCancelled), to a read-only tayfa
//     (apperr.ErrTayfaReadOnly) or from seven days after the event's end
//     (apperr.ErrComponentFrozen; ADR-0025 §5).
//   - Roles: the permission table's component actions (authz) asked in one
//     place. A change names who may make it: ByParticipant, ByManager (the
//     organizer, and moderators as for every event), ByReferee (a manager
//     or a referee the organizer assigned, event_referees) or an action
//     of the component's own (ByAction).
//   - Live updates: a change that calls Scope.Changed writes
//     event.component_changed (event ID and component key only) to the
//     outbox in the same transaction; the relay passes it to chat, which
//     tells the connections that watch the event (ADR-0025 §6).
//
// # Adding a component
//
// A component of kind K (issue #N) adds only files of its own:
//
//	proto/tayfam/core/v1/component_K.proto       service KComponentService; requests carry ComponentRef
//	db/core/migrations/000NN_component_K.sql     tables keyed by (event_id, component_key) with the
//	                                             event_component_guard('K') trigger
//	db/core/queries/component_K.sql
//	internal/core/app/component/K/               business logic on View and Change
//	internal/core/transport/grpc/component_K.go  the gRPC server and registerKComponent
//	internal/gateway/transport/rest/component_K.go  the routes and registerKComponent
//
// and one line under its own heading in each of the two registries:
// transport/grpc/components.go (core) and rest/components.go (gateway).
// Shared files a component still touches, each with only its own entries:
// api/openapi/gateway.yaml (its own section), errors (apperr, errors.proto
// with numbers N*10…N*10+9, the two error maps), the privileges test
// (store) and account deletion and export for the personal data it keeps.
//
// The test component in component_test.go is the pattern: a table with the
// guard trigger, a service that reads with View and writes with Change.
package component

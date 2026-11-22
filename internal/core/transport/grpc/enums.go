package grpc

import (
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
)

// Conversions between proto enums and the stored forms. An UNSPECIFIED or
// unknown value never converts (ok = false).

// interestFromProto converts a Faz 1 event type; only Event.type still
// carries the enum, next to type_key.
func interestFromProto(e corev1.EventType) (string, bool) {
	switch e {
	case corev1.EventType_EVENT_TYPE_CONVOY:
		return "convoy", true
	case corev1.EventType_EVENT_TYPE_CAMP:
		return "camp", true
	case corev1.EventType_EVENT_TYPE_HIKE:
		return "hike", true
	case corev1.EventType_EVENT_TYPE_FISHING:
		return "fishing", true
	case corev1.EventType_EVENT_TYPE_GAMING:
		return "gaming", true
	case corev1.EventType_EVENT_TYPE_SPORTS:
		return "sports", true
	case corev1.EventType_EVENT_TYPE_GENERAL:
		return "general", true
	}
	return "", false
}

func interestToProto(s string) corev1.EventType {
	for e := corev1.EventType_EVENT_TYPE_CONVOY; e <= corev1.EventType_EVENT_TYPE_GENERAL; e++ {
		if v, _ := interestFromProto(e); v == s {
			return e
		}
	}
	return corev1.EventType_EVENT_TYPE_UNSPECIFIED
}

// interestKeysFromProto takes a request's interests: keys of the catalog's
// interests (ADR-0025 §2). The Faz 1 enum list must be empty: an event
// type is not an interest, so it is refused rather than dropped. Duplicates
// and unknown keys are left for the app layer to refuse.
func interestKeysFromProto(keys []string, faz1 []corev1.EventType) ([]string, bool) {
	if len(faz1) > 0 || len(keys) > tayfa.MaxInterests {
		return nil, false
	}
	return append([]string{}, keys...), true
}

func moduleFromProto(m corev1.Module) (string, bool) {
	switch m {
	case corev1.Module_MODULE_ANNOUNCEMENTS:
		return "announcements", true
	case corev1.Module_MODULE_EVENTS:
		return "events", true
	case corev1.Module_MODULE_QA:
		return "qa", true
	case corev1.Module_MODULE_POLLS:
		return "polls", true
	case corev1.Module_MODULE_DAILY:
		return "daily", true
	case corev1.Module_MODULE_LIBRARY:
		return "library", true
	case corev1.Module_MODULE_QUESTION_POOL:
		return "question_pool", true
	}
	return "", false
}

func moduleToProto(s string) corev1.Module {
	for m := corev1.Module_MODULE_ANNOUNCEMENTS; m <= corev1.Module_MODULE_QUESTION_POOL; m++ {
		if v, _ := moduleFromProto(m); v == s {
			return m
		}
	}
	return corev1.Module_MODULE_UNSPECIFIED
}

func joinPolicyFromProto(p corev1.JoinPolicy) (string, bool) {
	switch p {
	case corev1.JoinPolicy_JOIN_POLICY_APPROVAL:
		return "approval", true
	case corev1.JoinPolicy_JOIN_POLICY_OPEN:
		return "open", true
	}
	return "", false
}

func joinPolicyToProto(s string) corev1.JoinPolicy {
	switch s {
	case "approval":
		return corev1.JoinPolicy_JOIN_POLICY_APPROVAL
	case "open":
		return corev1.JoinPolicy_JOIN_POLICY_OPEN
	}
	return corev1.JoinPolicy_JOIN_POLICY_UNSPECIFIED
}

func tayfaStatusToProto(s string) corev1.TayfaStatus {
	switch s {
	case "active":
		return corev1.TayfaStatus_TAYFA_STATUS_ACTIVE
	case "warned":
		return corev1.TayfaStatus_TAYFA_STATUS_WARNED
	case "archived":
		return corev1.TayfaStatus_TAYFA_STATUS_ARCHIVED
	}
	return corev1.TayfaStatus_TAYFA_STATUS_UNSPECIFIED
}

func roleToProto(r authz.Role) corev1.Role {
	switch r {
	case authz.Admin:
		return corev1.Role_ROLE_ADMIN
	case authz.Moderator:
		return corev1.Role_ROLE_MODERATOR
	case authz.Member:
		return corev1.Role_ROLE_MEMBER
	}
	return corev1.Role_ROLE_UNSPECIFIED
}

func statusToProto(s authz.Status) corev1.MembershipStatus {
	switch s {
	case authz.StatusInvited:
		return corev1.MembershipStatus_MEMBERSHIP_STATUS_INVITED
	case authz.StatusPending:
		return corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING
	case authz.StatusActive:
		return corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE
	case authz.StatusBanned:
		return corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED
	}
	return corev1.MembershipStatus_MEMBERSHIP_STATUS_UNSPECIFIED
}

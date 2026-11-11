package rest

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// enum maps between the JSON names in api/openapi/gateway.yaml and proto
// enum values. A value core sends that gateway does not know yet (a newer
// core) becomes "unknown"; clients ignore unknown values.
type enum[T ~int32] struct {
	byName  map[string]T
	byValue map[T]string
}

func newEnum[T ~int32](m map[string]T) enum[T] {
	e := enum[T]{byName: m, byValue: make(map[T]string, len(m))}
	for k, v := range m {
		e.byValue[v] = k
	}
	return e
}

func (e enum[T]) name(v T) string {
	if s, ok := e.byValue[v]; ok {
		return s
	}
	return "unknown"
}

func (e enum[T]) value(s string) (T, bool) {
	v, ok := e.byName[s]
	return v, ok
}

// valueOrUnspecified maps "" to the zero (UNSPECIFIED) value.
func (e enum[T]) valueOrUnspecified(s string) (T, bool) {
	if s == "" {
		return 0, true
	}
	return e.value(s)
}

// values maps a list; unknown or repeated names fail. core checks the same.
func (e enum[T]) values(in []string) ([]T, bool) {
	if len(in) > len(e.byName) {
		return nil, false
	}
	out := make([]T, 0, len(in))
	seen := make(map[T]bool, len(in))
	for _, s := range in {
		v, ok := e.byName[s]
		if !ok || seen[v] {
			return nil, false
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, true
}

func (e enum[T]) names(in []T) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, e.name(v))
	}
	return out
}

var (
	eventTypes = newEnum(map[string]corev1.EventType{
		"convoy": corev1.EventType_EVENT_TYPE_CONVOY, "camp": corev1.EventType_EVENT_TYPE_CAMP,
		"hike": corev1.EventType_EVENT_TYPE_HIKE, "fishing": corev1.EventType_EVENT_TYPE_FISHING,
		"gaming": corev1.EventType_EVENT_TYPE_GAMING, "sports": corev1.EventType_EVENT_TYPE_SPORTS,
		"general": corev1.EventType_EVENT_TYPE_GENERAL,
	})
	joinPolicies = newEnum(map[string]corev1.JoinPolicy{
		"approval": corev1.JoinPolicy_JOIN_POLICY_APPROVAL, "open": corev1.JoinPolicy_JOIN_POLICY_OPEN,
	})
	modules = newEnum(map[string]corev1.Module{
		"announcements": corev1.Module_MODULE_ANNOUNCEMENTS, "events": corev1.Module_MODULE_EVENTS,
		"qa": corev1.Module_MODULE_QA, "polls": corev1.Module_MODULE_POLLS,
	})
	tayfaStatuses = newEnum(map[string]corev1.TayfaStatus{
		"active": corev1.TayfaStatus_TAYFA_STATUS_ACTIVE, "warned": corev1.TayfaStatus_TAYFA_STATUS_WARNED,
		"archived": corev1.TayfaStatus_TAYFA_STATUS_ARCHIVED,
	})
	roles = newEnum(map[string]corev1.Role{
		"member": corev1.Role_ROLE_MEMBER, "moderator": corev1.Role_ROLE_MODERATOR, "admin": corev1.Role_ROLE_ADMIN,
	})
	membershipStatuses = newEnum(map[string]corev1.MembershipStatus{
		"invited": corev1.MembershipStatus_MEMBERSHIP_STATUS_INVITED, "pending": corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING,
		"active": corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, "banned": corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED,
	})
	joinSources = newEnum(map[string]corev1.JoinSource{
		"creator": corev1.JoinSource_JOIN_SOURCE_CREATOR, "invite_link": corev1.JoinSource_JOIN_SOURCE_INVITE_LINK,
		"direct_invite": corev1.JoinSource_JOIN_SOURCE_DIRECT_INVITE, "code": corev1.JoinSource_JOIN_SOURCE_CODE,
		"discover": corev1.JoinSource_JOIN_SOURCE_DISCOVER,
	})
)

// optTime returns nil for an unset timestamp, so the field is left out.
func optTime(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	return &v
}

type tayfaJSON struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Interests   []string        `json:"interests"`
	JoinPolicy  string          `json:"join_policy"`
	Modules     map[string]bool `json:"modules"`
	Status      string          `json:"status"`
	SuspendedAt *time.Time      `json:"suspended_at,omitempty"`
	DeletionAt  *time.Time      `json:"deletion_at,omitempty"`
	MemberCount int32           `json:"member_count"`
	// MembersCanCreateEvents: every active member may create events.
	MembersCanCreateEvents bool `json:"members_can_create_events"`
	// Only core decides who gets it (admins); gateway passes it on as is.
	JoinCode  string    `json:"join_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// CoverUploadID: links with POST /v1/media/urls (issue #135).
	CoverUploadID string `json:"cover_upload_id,omitempty"`
}

func tayfaFrom(t *corev1.Tayfa) tayfaJSON {
	mods := make(map[string]bool, len(t.GetModules()))
	for _, m := range t.GetModules() {
		if name, ok := modules.byValue[m.GetModule()]; ok {
			mods[name] = m.GetEnabled()
		}
	}
	return tayfaJSON{
		ID: t.GetId(), Name: t.GetName(), Description: t.GetDescription(),
		Interests: eventTypes.names(t.GetInterests()), JoinPolicy: joinPolicies.name(t.GetJoinPolicy()),
		Modules: mods, Status: tayfaStatuses.name(t.GetStatus()),
		SuspendedAt: optTime(t.GetSuspendTime()), DeletionAt: optTime(t.GetDeletionTime()),
		MemberCount: t.GetMemberCount(), MembersCanCreateEvents: t.GetMembersCanCreateEvents(), JoinCode: t.GetJoinCode(),
		CreatedAt: t.GetCreateTime().AsTime(), UpdatedAt: t.GetUpdateTime().AsTime(),
		CoverUploadID: t.GetCoverUploadId(),
	}
}

type membershipJSON struct {
	TayfaID    string     `json:"tayfa_id"`
	UserID     string     `json:"user_id"`
	Role       string     `json:"role"`
	Status     string     `json:"status"`
	JoinedAt   *time.Time `json:"joined_at,omitempty"`
	MutedUntil *time.Time `json:"muted_until,omitempty"`
}

func membershipFrom(m *corev1.Membership) membershipJSON {
	return membershipJSON{
		TayfaID: m.GetTayfaId(), UserID: m.GetUserId(),
		Role: roles.name(m.GetRole()), Status: membershipStatuses.name(m.GetStatus()),
		JoinedAt: optTime(m.GetJoinTime()), MutedUntil: optTime(m.GetMuteExpireTime()),
	}
}

type inviteJSON struct {
	ID              string     `json:"id"`
	TayfaID         string     `json:"tayfa_id"`
	CreatedByUserID string     `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	MaxUses         int32      `json:"max_uses"`
	UseCount        int32      `json:"use_count"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

func inviteFrom(i *corev1.InviteLink) inviteJSON {
	return inviteJSON{
		ID: i.GetInviteId(), TayfaID: i.GetTayfaId(), CreatedByUserID: i.GetCreatedByUserId(),
		CreatedAt: i.GetCreateTime().AsTime(), ExpiresAt: i.GetExpireTime().AsTime(),
		MaxUses: i.GetMaxUses(), UseCount: i.GetUseCount(), RevokedAt: optTime(i.GetRevokeTime()),
	}
}

package admin

import (
	"html/template"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// panelZone is Turkey's time (UTC+3 all year since 2016). Times are UTC
// everywhere else; only the panel's display uses it.
func panelZone() *time.Location { return time.FixedZone("TSİ", 3*60*60) }

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"label":  label,
		"when":   when,
		"ts":     ts,
		"urgent": urgent,
		"htmx":   func() string { return HTMXPath },
		"activeMember": func(m *corev1.AdminMember) bool {
			return m.GetStatus() == corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE
		},
	}
}

// when formats a time for the panel; the zero time is a dash.
func when(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(panelZone()).Format("02.01.2006 15:04") + " TSİ"
}

// ts formats a protobuf timestamp; unset is a dash.
func ts(t *timestamppb.Timestamp) string {
	if t == nil {
		return "—"
	}
	return when(t.AsTime())
}

// label is the Turkish name of an enum value shown on the panel.
func label(v any) string {
	switch x := v.(type) {
	case corev1.TayfaStatus:
		switch x {
		case corev1.TayfaStatus_TAYFA_STATUS_ACTIVE:
			return "Aktif"
		case corev1.TayfaStatus_TAYFA_STATUS_WARNED:
			return "Uyarıldı (hareketsiz)"
		case corev1.TayfaStatus_TAYFA_STATUS_ARCHIVED:
			return "Arşivlendi"
		}
	case corev1.Role:
		switch x {
		case corev1.Role_ROLE_MEMBER:
			return "Üye"
		case corev1.Role_ROLE_MODERATOR:
			return "Moderatör"
		case corev1.Role_ROLE_ADMIN:
			return "Yönetici"
		}
	case corev1.MembershipStatus:
		switch x {
		case corev1.MembershipStatus_MEMBERSHIP_STATUS_INVITED:
			return "Davet edildi"
		case corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING:
			return "Onay bekliyor"
		case corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE:
			return "Aktif"
		case corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED:
			return "Tayfadan yasaklı"
		}
	case corev1.ReportTargetType:
		return targetLabel(x)
	case corev1.ReportReason:
		return reasonLabel(x)
	case corev1.ReportPriority:
		if x == corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED {
			return "18 yaş altı taraf"
		}
		return "Normal"
	case corev1.PlatformDecision:
		switch x {
		case corev1.PlatformDecision_PLATFORM_DECISION_CONTENT_REMOVED:
			return "İçerik kaldırıldı"
		case corev1.PlatformDecision_PLATFORM_DECISION_WARNED:
			return "Uyarıldı"
		case corev1.PlatformDecision_PLATFORM_DECISION_TAYFA_SUSPENDED:
			return "Tayfa askıya alındı"
		case corev1.PlatformDecision_PLATFORM_DECISION_USER_BANNED:
			return "Kullanıcı banlandı"
		case corev1.PlatformDecision_PLATFORM_DECISION_DISMISSED:
			return "Reddedildi"
		}
	case identityv1.SanctionType:
		switch x {
		case identityv1.SanctionType_SANCTION_TYPE_SUSPEND:
			return "Askıda"
		case identityv1.SanctionType_SANCTION_TYPE_BAN:
			return "Banlı"
		}
	}
	return "—"
}

func targetLabel(t corev1.ReportTargetType) string {
	switch t {
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_USER:
		return "Kullanıcı"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA:
		return "Tayfa"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT:
		return "Duyuru"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_EVENT:
		return "Etkinlik"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_QUESTION:
		return "Soru"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_ANSWER:
		return "Cevap"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_POLL:
		return "Anket"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_FISHING_CATCH:
		return "Av kaydı"
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE:
		return "Birebir mesaj"
	}
	return "—"
}

func reasonLabel(r corev1.ReportReason) string {
	switch r {
	case corev1.ReportReason_REPORT_REASON_HARASSMENT:
		return "Taciz"
	case corev1.ReportReason_REPORT_REASON_HATE_SPEECH:
		return "Nefret söylemi"
	case corev1.ReportReason_REPORT_REASON_VIOLENCE:
		return "Şiddet veya tehdit"
	case corev1.ReportReason_REPORT_REASON_PRIVACY:
		return "Kişisel bilgi paylaşımı"
	case corev1.ReportReason_REPORT_REASON_IMPERSONATION:
		return "Kimliğe bürünme"
	case corev1.ReportReason_REPORT_REASON_ILLEGAL_TRADE:
		return "Yasa dışı ticaret"
	case corev1.ReportReason_REPORT_REASON_DRUGS:
		return "Uyuşturucu"
	case corev1.ReportReason_REPORT_REASON_GAMBLING:
		return "Kumar"
	case corev1.ReportReason_REPORT_REASON_DANGEROUS_ACTIVITY:
		return "Tehlikeli etkinlik"
	case corev1.ReportReason_REPORT_REASON_FAKE_EVENT:
		return "Sahte etkinlik"
	case corev1.ReportReason_REPORT_REASON_SEXUAL_CONTENT:
		return "Cinsel içerik"
	case corev1.ReportReason_REPORT_REASON_CHILD_SAFETY:
		return "Çocuk/genç güvenliği"
	case corev1.ReportReason_REPORT_REASON_SPAM:
		return "Spam"
	case corev1.ReportReason_REPORT_REASON_COPYRIGHT:
		return "Telif hakkı"
	case corev1.ReportReason_REPORT_REASON_STAFF_CONDUCT:
		return "Yönetici/moderatör davranışı"
	case corev1.ReportReason_REPORT_REASON_OTHER:
		return "Diğer"
	}
	return "—"
}

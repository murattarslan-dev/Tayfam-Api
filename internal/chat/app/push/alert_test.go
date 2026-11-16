package push_test

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/notification"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
)

// TestSafetyAlertIsAlert (issue #210, ADR-0033 §10): the safety contact
// alarm is treated like the hike alarm (never muted, pushed while online,
// alerts channel); ordinary notifications are not.
func TestSafetyAlertIsAlert(t *testing.T) {
	for typ, want := range map[string]bool{
		notification.TypeSafetyAlert:      true,
		notification.TypeHikeOverdue:      true,
		notification.TypeFriendSuggestion: false,
		notification.TypeAnnouncement:     false,
	} {
		if got := push.Alert(typ); got != want {
			t.Errorf("Alert(%s) = %v, beklenen %v", typ, got, want)
		}
	}
}

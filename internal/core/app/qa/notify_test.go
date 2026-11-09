package qa

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
)

// An answer notifies the question's author only, not when they answer
// themselves, and not after they left (issue #64).
func TestAnswerRequestsNotification(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	q := e.ask(t, c, c.member, "Kamp için hangi çadırı alalım?", "Dört kişilik arıyoruz, öneriniz var mı?")
	a := e.answer(t, c, c.mod, q, "Tünel tipi iyi olur.")
	e.answer(t, c, c.member, q, "Teşekkürler, bakacağım.")

	got := noticesOf(t, e.pool, notify.TypeAnswerReceived)
	if len(got) != 1 || got[0].target != a.ID || got[0].actor != c.mod || got[0].payload["question_id"] != q.ID.String() ||
		!sameUsers(got[0].recipients, c.member) {
		t.Fatalf("istekler = %+v", got)
	}
}

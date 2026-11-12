package integrity

import (
	"testing"
	"time"
)

// TestBindingIsStable fixes the binding values: the client computes them
// too (ADR-0022 §2), so a change here breaks every client.
func TestBindingIsStable(t *testing.T) {
	if got := SignInBinding("n"); got != "RQikIoUgcNuXXdqIefpw7eiSKtDfUXbWostlt13jJbM" {
		t.Fatalf("giriş = %s", got)
	}
	if got := RefreshBinding("abc"); got != "MgBlpZgH50sh41IuBxeaKNHLWdypFMK37NLLu0VAWSg" {
		t.Fatalf("yenileme = %s", got)
	}
	if SignInBinding("n") == RefreshBinding("n") {
		t.Fatal("giriş ve yenileme bağlaması aynı")
	}
	if len(SignInBinding("x")) > 500 {
		t.Fatal("requestHash sınırı aşıldı")
	}
}

func TestParseMode(t *testing.T) {
	for _, ok := range []string{"off", "report", "enforce"} {
		if _, err := ParseMode(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Report", "on", "block"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("%q kabul edildi", bad)
		}
	}
}

func TestNext(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	fresh := State{Status: StatusUnverified}
	passed := State{Status: StatusPassed, CheckedAt: now.Add(-time.Hour), PassedAt: now.Add(-time.Hour)}
	oldPass := State{Status: StatusPassed, CheckedAt: now.Add(-8 * 24 * time.Hour), PassedAt: now.Add(-8 * 24 * time.Hour)}
	failed := State{Status: StatusFailed, Reason: ReasonDevice, CheckedAt: now.Add(-2 * time.Hour)}

	for _, tc := range []struct {
		name string
		prev State
		r    *Result
		want State
	}{
		{"denetim yok", passed, nil, passed},
		{"geçti", fresh, &Result{Status: StatusPassed}, State{Status: StatusPassed, CheckedAt: now, PassedAt: now}},
		{"geçmedi", passed, &Result{Status: StatusFailed, Reason: ReasonDevice},
			State{Status: StatusFailed, Reason: ReasonDevice, CheckedAt: now, PassedAt: passed.PassedAt}},
		{"kanıt yok, hiç geçmemiş", fresh, Missing(), State{Status: StatusUnverified, Reason: ReasonMissing, CheckedAt: now}},
		{"kanıt yok, geçmişti", passed, Missing(),
			State{Status: StatusFailed, Reason: ReasonStale, CheckedAt: now, PassedAt: passed.PassedAt}},
		{"Google yok, yakın zamanda geçmiş", passed, &Result{Reason: ReasonUnavailable},
			State{Status: StatusPassed, CheckedAt: now, PassedAt: passed.PassedAt}},
		{"Google yok, eski geçiş", oldPass, &Result{Reason: ReasonUnavailable},
			State{Status: StatusUnverified, Reason: ReasonUnavailable, CheckedAt: now, PassedAt: oldPass.PassedAt}},
		{"Google yok, geçmemiş", failed, &Result{Reason: ReasonUnavailable},
			State{Status: StatusUnverified, Reason: ReasonUnavailable, CheckedAt: now}},
	} {
		if got := Next(tc.prev, tc.r, now); got != tc.want {
			t.Errorf("%s: %+v, beklenen %+v", tc.name, got, tc.want)
		}
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		s     State
		due   bool
		after time.Time
	}{
		{"hiç denetlenmemiş", State{Status: StatusUnverified}, true, now},
		{"23 saat önce geçti", State{Status: StatusPassed, CheckedAt: now.Add(-23 * time.Hour), PassedAt: now.Add(-23 * time.Hour)}, false, now.Add(time.Hour)},
		{"24 saat önce geçti", State{Status: StatusPassed, CheckedAt: now.Add(-24 * time.Hour), PassedAt: now.Add(-24 * time.Hour)}, true, now},
		{"30 dk önce geçmedi", State{Status: StatusFailed, Reason: ReasonDevice, CheckedAt: now.Add(-30 * time.Minute)}, false, now.Add(30 * time.Minute)},
		{"2 saat önce geçmedi", State{Status: StatusFailed, Reason: ReasonDevice, CheckedAt: now.Add(-2 * time.Hour)}, true, now.Add(-time.Hour)},
	} {
		if got := tc.s.Due(now); got != tc.due {
			t.Errorf("%s: due = %v", tc.name, got)
		}
		if got := tc.s.CheckAfter(now); !got.Equal(tc.after) {
			t.Errorf("%s: sonraki = %v, beklenen %v", tc.name, got, tc.after)
		}
	}
}

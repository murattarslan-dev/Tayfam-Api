package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoaderDefaultsAndValues(t *testing.T) {
	l := New(env(map[string]string{
		"NAME":    "  gateway ",
		"EMPTY":   "   ",
		"COUNT":   "3",
		"TIMEOUT": "2s",
		"MODE":    "prod",
	}))

	if got := l.String("NAME", "x"); got != "gateway" {
		t.Errorf("String trims value: got %q", got)
	}
	if got := l.String("EMPTY", "def"); got != "def" {
		t.Errorf("String treats blank as unset: got %q", got)
	}
	if got := l.Int("COUNT", 0); got != 3 {
		t.Errorf("Int: got %d", got)
	}
	if got := l.Duration("TIMEOUT", 0); got != 2*time.Second {
		t.Errorf("Duration: got %v", got)
	}
	if got := l.OneOf("MODE", "dev", "dev", "prod"); got != "prod" {
		t.Errorf("OneOf: got %q", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderCollectsAllErrors(t *testing.T) {
	l := New(env(map[string]string{
		"COUNT":   "abc",
		"TIMEOUT": "-1s",
		"MODE":    "staging",
	}))

	l.Required("SECRET")
	l.Int("COUNT", 1)
	l.Duration("TIMEOUT", time.Second)
	l.OneOf("MODE", "dev", "dev", "prod")

	err := l.Err()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, key := range []string{"SECRET", "COUNT", "TIMEOUT", "MODE"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error should mention %s: %v", key, err)
		}
	}
}

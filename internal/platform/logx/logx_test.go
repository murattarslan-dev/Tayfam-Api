package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewTagsServiceAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "gateway", "dev", slog.LevelInfo)

	log.Info("istek",
		slog.String("Authorization", "Bearer abc"),
		slog.Group("user", slog.String("email", "a@b.c"), slog.String("id", "u1")),
		slog.Float64("lat", 41.987654),
		slog.String("path", "/v1/tayfas"),
	)

	out := buf.String()
	for _, leaked := range []string{"Bearer abc", "a@b.c", "41.987654"} {
		if strings.Contains(out, leaked) {
			t.Errorf("sensitive value %q leaked: %s", leaked, out)
		}
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if rec["service"] != "gateway" || rec["env"] != "dev" {
		t.Errorf("missing service/env tags: %v", rec)
	}
	if rec["path"] != "/v1/tayfas" {
		t.Errorf("non-sensitive attribute should be kept: %v", rec)
	}
	user, _ := rec["user"].(map[string]any)
	if user["id"] != "u1" || user["email"] != Redacted {
		t.Errorf("group attributes not handled: %v", user)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "core", "prod", ParseLevel("warn"))
	log.Info("gizli kalmalı")
	if buf.Len() != 0 {
		t.Errorf("info should be filtered at warn level: %s", buf.String())
	}
	if ParseLevel("nonsense") != slog.LevelInfo {
		t.Error("unknown level should default to info")
	}
}

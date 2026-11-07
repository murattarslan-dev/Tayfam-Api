// Package logx builds the structured JSON logger every service uses.
//
// As a second line of defence for ADR-0008 (no personal data in logs), the
// handler redacts attributes whose key looks sensitive, wherever they appear
// in a group. Code must still not log personal data in the first place.
package logx

import (
	"io"
	"log/slog"
	"strings"
)

// Redacted replaces the value of a sensitive attribute.
const Redacted = "[gizlendi]"

// sensitiveKeys are matched case-insensitively against the full attribute key.
var sensitiveKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {},
	"token": {}, "access_token": {}, "refresh_token": {}, "id_token": {},
	"password": {}, "secret": {}, "api_key": {},
	"email": {}, "phone": {}, "plate": {},
	"location": {}, "lat": {}, "lng": {}, "latitude": {}, "longitude": {},
	"body": {}, "message_body": {}, "ciphertext": {},
	"ticket": {}, "invite_token": {},
}

// IsSensitive reports whether an attribute key must never be logged as-is.
func IsSensitive(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}

// ParseLevel maps "debug", "info", "warn" and "error" to a slog level; anything else is info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New returns a JSON logger that tags every record with the service and environment.
func New(w io.Writer, service, env string, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if IsSensitive(a.Key) {
				return slog.String(a.Key, Redacted)
			}
			return a
		},
	})
	return slog.New(h).With(slog.String("service", service), slog.String("env", env))
}

// Package config reads service configuration from environment variables.
//
// Values are collected through a Loader; every problem (missing required
// value, unparsable number) is recorded instead of returned immediately, so a
// service reports all configuration errors at once and refuses to start.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Loader reads typed values from an environment lookup function.
type Loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// New returns a Loader backed by lookup (normally os.LookupEnv).
func New(lookup func(string) (string, bool)) *Loader {
	return &Loader{lookup: lookup}
}

func (l *Loader) get(key string) (string, bool) {
	v, ok := l.lookup(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// String returns the value of key, or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v, ok := l.get(key); ok {
		return v
	}
	return def
}

// Required returns the value of key and records an error when it is unset or empty.
func (l *Loader) Required(key string) string {
	v, ok := l.get(key)
	if !ok {
		l.errs = append(l.errs, fmt.Errorf("%s: zorunlu ortam değişkeni tanımlı değil", key))
	}
	return v
}

// OneOf returns the value of key (or def) and records an error when it is not one of allowed.
func (l *Loader) OneOf(key, def string, allowed ...string) string {
	v := l.String(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.errs = append(l.errs, fmt.Errorf("%s: %q geçersiz; izin verilenler: %s", key, v, strings.Join(allowed, ", ")))
	return def
}

// Int returns key parsed as an integer, or def when unset.
func (l *Loader) Int(key string, def int) int {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: tam sayı değil: %q", key, v))
		return def
	}
	return n
}

// Duration returns key parsed with time.ParseDuration (e.g. "15s"), or def when unset.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		l.errs = append(l.errs, fmt.Errorf("%s: geçerli bir süre değil: %q", key, v))
		return def
	}
	return d
}

// Err returns all recorded problems joined, or nil.
func (l *Loader) Err() error {
	return errors.Join(l.errs...)
}

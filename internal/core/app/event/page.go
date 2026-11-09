package event

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (event.proto): page_size 0 means 50, above 100 means 100, negative
// is a validation error.
const (
	defaultPageSize = 50
	maxPageSize     = 100
)

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// cursor is the position after the last row of a page; the page token is
// its JSON form in base64url. It names the method, the tayfa and the
// request scope (event and status), so a token is only accepted by the same
// request. It carries nothing the caller could not already see; tampering
// only moves the position inside a list the caller may read anyway.
type cursor struct {
	Method string `json:"m"`
	Tayfa  string `json:"t"`
	Scope  string `json:"s,omitempty"`
	// Micros is the sort time in Unix microseconds (Postgres precision).
	Micros int64  `json:"c"`
	ID     string `json:"i"`
}

func encodeCursor(method string, tayfa id.ID, scope string, at time.Time, after id.ID) string {
	b, _ := json.Marshal(cursor{Method: method, Tayfa: tayfa.String(), Scope: scope, Micros: at.UnixMicro(), ID: after.String()}) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor returns the position in token. An empty token is the first
// page: the zero ID at the Unix epoch, before every row.
func decodeCursor(token, method string, tayfa id.ID, scope string) (id.ID, time.Time, error) {
	if token == "" {
		return id.ID{}, time.Unix(0, 0).UTC(), nil
	}
	bad := apperr.Validation("page_token")
	if len(token) > 512 {
		return id.ID{}, time.Time{}, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return id.ID{}, time.Time{}, bad
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil {
		return id.ID{}, time.Time{}, bad
	}
	after, ok := id.Parse(c.ID)
	if !ok || c.Method != method || c.Tayfa != tayfa.String() || c.Scope != scope {
		return id.ID{}, time.Time{}, bad
	}
	return after, time.UnixMicro(c.Micros).UTC(), nil
}

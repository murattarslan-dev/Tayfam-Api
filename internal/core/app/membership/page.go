package membership

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (proto MembershipService): page_size 0 means 50, above 100 means
// 100, negative is a validation error.
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

// cursor is the position after the last row of a page. The page token is its
// JSON form in base64url. It carries the method and the request fields it
// was made for, so a token is only accepted by the same request; it carries
// nothing the caller could not already see. Tampering only moves the cursor
// inside a list the caller may read anyway.
type cursor struct {
	Method string `json:"m"`
	Tayfa  string `json:"t"`
	Status string `json:"s,omitempty"`
	Rank   int32  `json:"r,omitempty"`
	// Micros is the sort time in Unix microseconds (Postgres precision).
	Micros int64  `json:"c"`
	User   string `json:"u"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses token and checks that it belongs to the same method
// and request fields. An empty token means the first page (ok false).
func decodeCursor(token, method string, tayfa id.ID, status string) (c cursor, after id.ID, at time.Time, ok bool, err error) {
	if token == "" {
		return cursor{}, id.ID{}, time.Time{}, false, nil
	}
	bad := apperr.Validation("page_token")
	if len(token) > 512 {
		return cursor{}, id.ID{}, time.Time{}, false, bad
	}
	b, derr := base64.RawURLEncoding.DecodeString(token)
	if derr != nil || json.Unmarshal(b, &c) != nil {
		return cursor{}, id.ID{}, time.Time{}, false, bad
	}
	u, uok := id.Parse(c.User)
	if !uok || c.Method != method || c.Tayfa != tayfa.String() || c.Status != status {
		return cursor{}, id.ID{}, time.Time{}, false, bad
	}
	return c, u, time.UnixMicro(c.Micros).UTC(), true, nil
}

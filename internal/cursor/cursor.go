// Package cursor provides opaque, URL-safe pagination cursors that encode a
// (timestamp, id) keyset bookmark. Shared by every endpoint that paginates a
// time-ordered list (user posts, the feed, …) so the logic lives in ONE place.
package cursor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Encode packs a (time, id) bookmark into an opaque, URL-safe token.
//
// We use RawURLEncoding (alphabet A-Z a-z 0-9 - _, no padding) — NOT standard
// base64 — because standard base64 contains '+' and '/', and a '+' inside a URL
// query string is decoded back into a SPACE, which would corrupt the cursor.
func Encode(t time.Time, id int64) string {
	raw := fmt.Sprintf("%s|%d", t.Format(time.RFC3339Nano), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode reverses Encode: token → "time|id" → (time, id).
func Decode(s string) (time.Time, int64, error) {
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, err
	}
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, 0, err
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, err
	}
	return t, id, nil
}

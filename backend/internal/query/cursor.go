package query

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

// Cursor is the keyset position after the last row of a page: the next page
// returns rows strictly older than (Timestamp, EventID) in the sort order
// timestamp DESC, event_id DESC.
type Cursor struct {
	Timestamp time.Time
	EventID   string
}

const (
	cursorVersion  = 1
	maxCursorLen   = 200
	cursorTimeFmt  = "2006-01-02T15:04:05.000Z"
	errCursorShape = "cursor: invalid; use next_cursor from the previous response unchanged"
)

// eventIDPattern matches Redis stream entry IDs (ms-seq), the only event IDs
// the processor writes.
var eventIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}$`)

type cursorJSON struct {
	V  int    `json:"v"`
	T  string `json:"t"`
	ID string `json:"id"`
}

// EncodeCursor returns the opaque base64url form of c.
func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(cursorJSON{V: cursorVersion, T: c.Timestamp.UTC().Format(cursorTimeFmt), ID: c.EventID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses s strictly: canonical base64url without padding, a
// JSON object with exactly the expected fields, a millisecond UTC timestamp
// and a Redis-style event ID. Anything else is rejected.
func DecodeCursor(s string) (Cursor, error) {
	invalid := errors.New(errCursorShape)
	if s == "" || len(s) > maxCursorLen {
		return Cursor{}, invalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return Cursor{}, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cj cursorJSON
	if err := dec.Decode(&cj); err != nil {
		return Cursor{}, invalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Cursor{}, invalid // trailing data
	}
	if cj.V != cursorVersion || !eventIDPattern.MatchString(cj.ID) {
		return Cursor{}, invalid
	}
	ts, err := time.Parse(cursorTimeFmt, cj.T)
	if err != nil {
		return Cursor{}, invalid
	}
	c := Cursor{Timestamp: ts.UTC(), EventID: cj.ID}
	if EncodeCursor(c) != s {
		return Cursor{}, invalid // only the exact encoding we produce is accepted
	}
	return c, nil
}

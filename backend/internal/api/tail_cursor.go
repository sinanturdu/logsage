package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/query"
)

// tailCursor is the SSE event id of a log event (ADR 0015): the stream it
// belongs to, the highest position delivered so far and the send sequence
// number of that event. It is opaque to clients.
type tailCursor struct {
	Stream string        // random stream token, restores the resume state
	Last   query.TailKey // highest (inserted_at, event_id) delivered
	Seq    uint64        // sequence number of this event in the stream
}

type cursorJSON struct {
	V int    `json:"v"`
	S string `json:"s"`
	T int64  `json:"t"` // inserted_at, Unix milliseconds
	E string `json:"e"` // event_id, empty before the first in-order row
	N uint64 `json:"n"`
}

var (
	streamToken = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	tailEventID = regexp.MustCompile(`^([0-9]{1,20}-[0-9]{1,20})?$`)
)

func newStreamToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func encodeTailCursor(c tailCursor) string {
	b, _ := json.Marshal(cursorJSON{V: 1, S: c.Stream, T: c.Last.InsertedAt.UnixMilli(), E: c.Last.EventID, N: c.Seq})
	return base64.RawURLEncoding.EncodeToString(b)
}

var errBadCursor = errors.New("invalid Last-Event-ID")

// decodeTailCursor accepts only what encodeTailCursor produces: unpadded
// base64url of the exact JSON, with no unknown fields and the canonical
// encoding, so a cursor cannot be altered without failing.
func decodeTailCursor(s string) (tailCursor, error) {
	if s == "" || len(s) > 256 {
		return tailCursor{}, errBadCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return tailCursor{}, errBadCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var j cursorJSON
	if err := dec.Decode(&j); err != nil || dec.More() {
		return tailCursor{}, errBadCursor
	}
	if j.V != 1 || !streamToken.MatchString(j.S) || j.T <= 0 || !tailEventID.MatchString(j.E) {
		return tailCursor{}, errBadCursor
	}
	c := tailCursor{Stream: j.S, Last: query.TailKey{InsertedAt: time.UnixMilli(j.T).UTC(), EventID: j.E}, Seq: j.N}
	if encodeTailCursor(c) != s {
		return tailCursor{}, errBadCursor
	}
	return c, nil
}

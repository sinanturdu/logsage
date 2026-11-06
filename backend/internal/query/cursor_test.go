package query

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	tests := []Cursor{
		{Timestamp: time.Date(2026, 11, 6, 10, 0, 1, 250_000_000, time.UTC), EventID: "1793993613811-0"},
		{Timestamp: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), EventID: "0-0"},
		{Timestamp: time.Date(2026, 11, 6, 13, 0, 1, 0, time.FixedZone("TRT", 3*3600)), EventID: "99999999999999999999-99999999999999999999"},
	}
	for _, c := range tests {
		s := EncodeCursor(c)
		got, err := DecodeCursor(s)
		if err != nil {
			t.Fatalf("DecodeCursor(%q): %v", s, err)
		}
		if !got.Timestamp.Equal(c.Timestamp) || got.EventID != c.EventID || got.Timestamp.Location() != time.UTC {
			t.Errorf("round trip = %+v, want %+v", got, c)
		}
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("cursor %q is not unpadded base64url", s)
		}
	}
}

func TestDecodeCursorRejectsTampering(t *testing.T) {
	valid := EncodeCursor(Cursor{Timestamp: time.Date(2026, 11, 6, 10, 0, 1, 0, time.UTC), EventID: "1-0"})
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	flipped := []byte(valid)
	flipped[len(flipped)/2] ^= 0x01

	tests := []struct{ name, cursor string }{
		{name: "empty", cursor: ""},
		{name: "flipped byte", cursor: string(flipped)},
		{name: "padded", cursor: valid + "=="},
		{name: "standard base64 alphabet", cursor: base64.StdEncoding.EncodeToString([]byte(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"1-0"}`))},
		{name: "not base64", cursor: "!!not-a-cursor!!"},
		{name: "too long", cursor: strings.Repeat("A", maxCursorLen+1)},
		{name: "not json", cursor: enc("hello")},
		{name: "unknown field", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"1-0","project_id":"other"}`)},
		{name: "wrong version", cursor: enc(`{"v":2,"t":"2026-11-06T10:00:01.000Z","id":"1-0"}`)},
		{name: "missing id", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z"}`)},
		{name: "sql in id", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"1-0' OR '1'='1"}`)},
		{name: "comment in id", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"1-0--"}`)},
		{name: "unicode id", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"١-٠"}`)},
		{name: "id too long", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"` + strings.Repeat("1", 21) + `-0"}`)},
		{name: "timestamp not millisecond format", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01Z","id":"1-0"}`)},
		{name: "timestamp with offset", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000+03:00","id":"1-0"}`)},
		{name: "trailing data", cursor: enc(`{"v":1,"t":"2026-11-06T10:00:01.000Z","id":"1-0"}{}`)},
		{name: "non-canonical json", cursor: enc(`{ "v":1, "t":"2026-11-06T10:00:01.000Z", "id":"1-0" }`)},
		{name: "array", cursor: enc(`[1,"2026-11-06T10:00:01.000Z","1-0"]`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeCursor(tt.cursor); err == nil {
				t.Fatalf("DecodeCursor(%q) succeeded, want error", tt.cursor)
			}
		})
	}
}

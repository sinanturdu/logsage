package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// Live tail (ADR 0015). Rows are read in visibility order: (inserted_at,
// event_id), where inserted_at is set by the processor per insert attempt.
// Tail statements read without FINAL: a redelivered row is a separate
// version with its own inserted_at, and the caller deduplicates by
// event_id, so each event is delivered once.

// TailKey is a position in visibility order.
type TailKey struct {
	InsertedAt time.Time
	EventID    string
}

// Less orders keys by inserted_at, then event_id.
func (k TailKey) Less(o TailKey) bool {
	if !k.InsertedAt.Equal(o.InsertedAt) {
		return k.InsertedAt.Before(o.InsertedAt)
	}
	return k.EventID < o.EventID
}

// TailRow is one event and its position.
type TailRow struct {
	Event Event
	Key   TailKey
}

// TailFuture is how far past now a tail accepts event timestamps (clients
// with fast clocks); the time filter needs an upper bound.
const TailFuture = time.Hour

var tailParams = []string{"level", "service", "q", "trace_id", "fingerprint"}

// ParseTail validates a tail query string: the content filters of the read
// endpoints, without from and to (a tail starts now).
func (s *Service) ParseTail(rawQuery string) (Filter, error) {
	vals, err := parseNames(rawQuery, tailParams)
	if err != nil {
		return Filter{}, err
	}
	var f Filter
	if err := s.parseContent(vals, &f); err != nil {
		return Filter{}, err
	}
	return f, nil
}

// TailWindow restricts f to event timestamps from now-lateness to now plus
// TailFuture, so tail polls never scan the whole table. Logs whose event
// time is older than lateness are not streamed live.
func TailWindow(f Filter, now time.Time, lateness time.Duration) Filter {
	f.From, f.To = now.Add(-lateness), now.Add(TailFuture)
	return f
}

const tailColumns = "SELECT event_id, timestamp, level, service, message, trace_id, attributes, fingerprint, inserted_at\n"

func keyParams(params map[string]string, prefix string, k TailKey) {
	params[prefix+"_ts"] = k.InsertedAt.UTC().Format(chTime)
	params[prefix+"_id"] = k.EventID
}

// BuildTailAfter returns rows strictly after key in visibility order, at
// most limit, oldest first.
func BuildTailAfter(scope Scope, f Filter, after TailKey, limit int) (Statement, error) {
	if limit <= 0 {
		return Statement{}, errors.New("query: limit must be positive")
	}
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	conds = append(conds, "(inserted_at, event_id) > ({after_ts:DateTime64(3, 'UTC')}, {after_id:String})")
	keyParams(params, "after", after)
	params["limit"] = strconv.Itoa(limit)
	sql := tailColumns + "FROM logs\n" + joinWhere(conds) +
		"ORDER BY inserted_at, event_id\n" +
		"LIMIT {limit:UInt32}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// rangeConds limits to keys in (lower, upper].
func rangeConds(conds []string, params map[string]string, lower, upper TailKey) []string {
	keyParams(params, "lower", lower)
	keyParams(params, "upper", upper)
	return append(conds,
		"(inserted_at, event_id) > ({lower_ts:DateTime64(3, 'UTC')}, {lower_id:String})",
		"(inserted_at, event_id) <= ({upper_ts:DateTime64(3, 'UTC')}, {upper_id:String})")
}

// BuildTailCount counts the distinct event ids with keys in (lower, upper]:
// the caller compares it with what it delivered to find late-visible rows.
func BuildTailCount(scope Scope, f Filter, lower, upper TailKey) (Statement, error) {
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	conds = rangeConds(conds, params, lower, upper)
	sql := "SELECT uniqExact(event_id) AS n\nFROM logs\n" + joinWhere(conds) + "FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// BuildTailRange returns the rows with keys in (lower, upper], oldest first.
func BuildTailRange(scope Scope, f Filter, lower, upper TailKey, limit int) (Statement, error) {
	if limit <= 0 {
		return Statement{}, errors.New("query: limit must be positive")
	}
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	conds = rangeConds(conds, params, lower, upper)
	params["limit"] = strconv.Itoa(limit)
	sql := tailColumns + "FROM logs\n" + joinWhere(conds) +
		"ORDER BY inserted_at, event_id\n" +
		"LIMIT {limit:UInt32}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// TailAfter runs BuildTailAfter.
func (s *Service) TailAfter(ctx context.Context, scope Scope, f Filter, after TailKey, limit int) ([]TailRow, error) {
	st, err := BuildTailAfter(scope, f, after, limit)
	if err != nil {
		return nil, err
	}
	return s.tailRows(ctx, st, limit)
}

// TailRange runs BuildTailRange.
func (s *Service) TailRange(ctx context.Context, scope Scope, f Filter, lower, upper TailKey, limit int) ([]TailRow, error) {
	st, err := BuildTailRange(scope, f, lower, upper, limit)
	if err != nil {
		return nil, err
	}
	return s.tailRows(ctx, st, limit)
}

// TailCount runs BuildTailCount.
func (s *Service) TailCount(ctx context.Context, scope Scope, f Filter, lower, upper TailKey) (uint64, error) {
	st, err := BuildTailCount(scope, f, lower, upper)
	if err != nil {
		return 0, err
	}
	body, err := s.run(ctx, st, 1)
	if err != nil {
		return 0, err
	}
	var n uint64
	err = eachLine(body, func(line []byte) error {
		var r struct{ N chUint }
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode tail count: %w", err)
		}
		n = uint64(r.N)
		return nil
	})
	return n, err
}

func (s *Service) tailRows(ctx context.Context, st Statement, limit int) ([]TailRow, error) {
	body, err := s.run(ctx, st, limit)
	if err != nil {
		return nil, err
	}
	var rows []TailRow
	err = eachLine(body, func(line []byte) error {
		var r struct {
			rowJSON
			InsertedAt string `json:"inserted_at"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode tail row: %w", err)
		}
		ts, err := time.ParseInLocation(chTime, r.Timestamp, time.UTC)
		if err != nil {
			return fmt.Errorf("decode tail row timestamp: %w", err)
		}
		ins, err := time.ParseInLocation(chTime, r.InsertedAt, time.UTC)
		if err != nil {
			return fmt.Errorf("decode tail row inserted_at: %w", err)
		}
		attrs := r.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		rows = append(rows, TailRow{
			Event: Event{EventID: r.EventID, Timestamp: ts.UTC().Format(cursorTimeFmt), Level: r.Level,
				Service: r.Service, Message: r.Message, TraceID: r.TraceID, Attributes: attrs,
				Fingerprint: fingerprint.Hex(uint64(r.Fingerprint))},
			Key: TailKey{InsertedAt: ins.UTC(), EventID: r.EventID},
		})
		return nil
	})
	return rows, err
}

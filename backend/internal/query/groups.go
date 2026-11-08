package query

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// Error groups (ADR 0022). Counts come from logs FINAL, like every other
// read, so redelivered rows are counted once. All-time first and last seen
// come from group_seen (min and max only, which duplicates cannot change).

// Group sort orders, newest or largest first.
const (
	SortCount     = "count"
	SortLastSeen  = "last_seen"
	SortFirstSeen = "first_seen"
)

// Group list limits.
const (
	DefaultGroupLimit = 50
	MaxGroupLimit     = 100
	// MaxGroupOffset bounds how deep the offset cursor pages.
	MaxGroupOffset = 1000
	// SparkBuckets is the number of sparkline buckets over the window.
	SparkBuckets = 24
	// GroupSamples is the number of latest logs in a group detail.
	GroupSamples = 5
)

// Group status values (stored in PostgreSQL; no row means open).
const (
	StatusOpen     = "open"
	StatusIgnored  = "ignored"
	StatusResolved = "resolved"
)

var groupParams = []string{"from", "to", "level", "service", "q", "status", "sort", "limit", "cursor"}
var groupDetailParams = []string{"from", "to"}

// GroupQuery is a validated group list request. Filter.Text matches the
// template (not the message); Filter.Fingerprint is unused.
type GroupQuery struct {
	Filter
	Sort   string
	Status string // empty: any; resolved by the caller into Include or Exclude
	Offset int
	// Include, when not nil, limits the list to these fingerprints (an empty
	// list means no groups). Exclude leaves these out.
	Include []uint64
	Exclude []uint64
	hash    string // ties a cursor to its query
}

// Group is one item of a group list.
type Group struct {
	Fingerprint string   `json:"fingerprint"`
	Title       string   `json:"title"`
	Sample      string   `json:"sample"`
	ErrorType   string   `json:"error_type"`
	Levels      []string `json:"levels"`
	Services    []string `json:"services"`
	Count       uint64   `json:"count"`
	FirstSeen   string   `json:"first_seen"`
	LastSeen    string   `json:"last_seen"`
	Sparkline   []uint64 `json:"sparkline"`
	IsNew       bool     `json:"is_new"`
	// Status, Regressed and ResolvedAt are filled by the caller from PostgreSQL.
	Status     string  `json:"status"`
	Regressed  bool    `json:"regressed"`
	ResolvedAt *string `json:"resolved_at"`
}

// GroupList is the result of a group list.
type GroupList struct {
	Groups             []Group `json:"groups"`
	NextCursor         *string `json:"next_cursor"`
	SparkBucketSeconds float64 `json:"sparkline_bucket_seconds"`
}

// ParseGroups validates a group list query string.
func (s *Service) ParseGroups(rawQuery string) (GroupQuery, error) {
	vals, f, err := s.parse(rawQuery, groupParams)
	if err != nil {
		return GroupQuery{}, err
	}
	q := GroupQuery{Filter: f, Sort: SortCount}
	has := func(k string) bool { _, ok := vals[k]; return ok }
	if has("sort") {
		switch v := vals.Get("sort"); v {
		case SortCount, SortLastSeen, SortFirstSeen:
			q.Sort = v
		default:
			return GroupQuery{}, fmt.Errorf("sort: unknown value %s; allowed: count, last_seen, first_seen", quoteShort(v))
		}
	}
	if has("status") {
		switch v := vals.Get("status"); v {
		case StatusOpen, StatusIgnored, StatusResolved:
			q.Status = v
		default:
			return GroupQuery{}, fmt.Errorf("status: unknown value %s; allowed: open, ignored, resolved", quoteShort(v))
		}
	}
	q.Limit = DefaultGroupLimit
	if has("limit") {
		n, err := strconv.Atoi(vals.Get("limit"))
		if err != nil || n < 1 || n > MaxGroupLimit {
			return GroupQuery{}, fmt.Errorf("limit: must be an integer between 1 and %d", MaxGroupLimit)
		}
		q.Limit = n
	}
	q.hash = queryHash(vals)
	if has("cursor") {
		off, err := decodeGroupCursor(vals.Get("cursor"), q.hash)
		if err != nil {
			return GroupQuery{}, err
		}
		q.Offset = off
	}
	return q, nil
}

// ParseGroupWindow validates the from and to of a group detail.
func (s *Service) ParseGroupWindow(rawQuery string) (Filter, error) {
	_, f, err := s.parse(rawQuery, groupDetailParams)
	return f, err
}

// queryHash identifies a list query without its cursor, so a cursor cannot
// be replayed against different filters.
func queryHash(vals url.Values) string {
	c := url.Values{}
	for k, v := range vals {
		if k != "cursor" {
			c[k] = v
		}
	}
	sum := sha256.Sum256([]byte(c.Encode())) // Encode sorts by key
	return hex.EncodeToString(sum[:6])
}

const groupCursorPrefix = "g1"

func encodeGroupCursor(offset int, hash string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(groupCursorPrefix + ":" + strconv.Itoa(offset) + ":" + hash))
}

var errGroupCursor = errors.New("cursor: not valid for this query; repeat the same query with next_cursor")

func decodeGroupCursor(c, hash string) (int, error) {
	if len(c) > 64 {
		return 0, errGroupCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, errGroupCursor
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 || parts[0] != groupCursorPrefix || parts[2] != hash {
		return 0, errGroupCursor
	}
	off, err := strconv.Atoi(parts[1])
	if err != nil || off < 0 || off > MaxGroupOffset {
		return 0, errGroupCursor
	}
	return off, nil
}

// uintArray renders fingerprints as an Array(UInt64) parameter value.
func uintArray(v []uint64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatUint(n, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// groupConds is where() for groups: the template replaces the message for
// q, only grouped rows count, and Include/Exclude become array parameters.
func groupConds(scope Scope, q GroupQuery) ([]string, map[string]string, error) {
	f := q.Filter
	text := f.Text
	f.Text = ""
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return nil, nil, err
	}
	conds = append(conds, "fingerprint != 0")
	if text != "" {
		conds = append(conds, "positionCaseInsensitiveUTF8(template, {q:String}) > 0")
		params["q"] = text
	}
	if q.Include != nil {
		conds = append(conds, "fingerprint IN {include:Array(UInt64)}")
		params["include"] = uintArray(q.Include)
	}
	if len(q.Exclude) > 0 {
		conds = append(conds, "fingerprint NOT IN {exclude:Array(UInt64)}")
		params["exclude"] = uintArray(q.Exclude)
	}
	return conds, params, nil
}

// BuildGroups is the only place that produces the group list SQL. Counts,
// titles and samples come from logs FINAL inside the window; first and last
// seen are all-time, from group_seen (falling back to the window). The
// project scope is on both sides of the join.
func BuildGroups(scope Scope, q GroupQuery) (Statement, error) {
	if q.Limit <= 0 || q.Limit > MaxGroupLimit || q.Offset < 0 || q.Offset > MaxGroupOffset {
		return Statement{}, errors.New("query: bad group limit or offset")
	}
	conds, params, err := groupConds(scope, q)
	if err != nil {
		return Statement{}, err
	}
	order := "g.c DESC"
	switch q.Sort {
	case SortLastSeen:
		order = "last_seen DESC"
	case SortFirstSeen:
		order = "first_seen DESC"
	case SortCount, "":
	default:
		return Statement{}, errors.New("query: bad group sort")
	}
	params["limit"] = strconv.Itoa(q.Limit + 1)
	params["offset"] = strconv.Itoa(q.Offset)
	sql := "SELECT g.fingerprint AS fingerprint, g.c AS count, g.title AS title, g.sample AS sample,\n" +
		"  g.error_type AS error_type, g.levels AS levels, g.services AS services,\n" +
		"  if(s.n = 0, g.w_first, least(s.first_seen, g.w_first)) AS first_seen,\n" +
		"  if(s.n = 0, g.w_last, greatest(s.last_seen, g.w_last)) AS last_seen\n" +
		"FROM (\n" +
		"  SELECT fingerprint, count() AS c, argMax(template, timestamp) AS title, argMax(message, timestamp) AS sample,\n" +
		"    argMax(if(attributes['error.type'] != '', attributes['error.type'], attributes['exception.type']), timestamp) AS error_type,\n" +
		"    arraySort(groupUniqArray(level)) AS levels, arraySort(groupUniqArray(20)(service)) AS services,\n" +
		"    min(timestamp) AS w_first, max(timestamp) AS w_last\n" +
		"  FROM logs FINAL\n" +
		"  " + joinWhere(conds) +
		"  GROUP BY fingerprint\n" +
		") AS g\n" +
		"LEFT JOIN (\n" +
		"  SELECT fingerprint, min(first_seen) AS first_seen, max(last_seen) AS last_seen, toUInt8(1) AS n\n" +
		"  FROM group_seen\n" +
		"  WHERE " + projectScope + "\n" +
		"  GROUP BY fingerprint\n" +
		") AS s ON g.fingerprint = s.fingerprint\n" +
		"ORDER BY " + order + ", g.fingerprint\n" +
		"LIMIT {limit:UInt32} OFFSET {offset:UInt32}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// sparkBucket is the sparkline bucket for a window: the window over
// SparkBuckets, rounded up to whole milliseconds, at least 1 ms.
func sparkBucket(f Filter) time.Duration {
	ms := (f.To.Sub(f.From).Milliseconds() + SparkBuckets - 1) / SparkBuckets
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

// BuildSparklines counts the given groups per bucket; buckets start at from.
func BuildSparklines(scope Scope, q GroupQuery, fps []uint64) (Statement, error) {
	if len(fps) == 0 || len(fps) > MaxGroupLimit+1 {
		return Statement{}, errors.New("query: sparklines need 1 to 101 fingerprints")
	}
	q.Include, q.Exclude = fps, nil
	conds, params, err := groupConds(scope, q)
	if err != nil {
		return Statement{}, err
	}
	params["from_ms"] = strconv.FormatInt(q.From.UnixMilli(), 10)
	params["bucket_ms"] = strconv.FormatInt(sparkBucket(q.Filter).Milliseconds(), 10)
	sql := "SELECT fingerprint, intDiv(toUnixTimestamp64Milli(timestamp) - {from_ms:Int64}, {bucket_ms:Int64}) AS b, count() AS c\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
		"GROUP BY fingerprint, b\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

type groupRow struct {
	Fingerprint chUint   `json:"fingerprint"`
	Count       chUint   `json:"count"`
	Title       string   `json:"title"`
	Sample      string   `json:"sample"`
	ErrorType   string   `json:"error_type"`
	Levels      []string `json:"levels"`
	Services    []string `json:"services"`
	FirstSeen   string   `json:"first_seen"`
	LastSeen    string   `json:"last_seen"`
}

func chTimeToAPI(v string) (string, time.Time, error) {
	t, err := time.ParseInLocation(chTime, v, time.UTC)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("decode group time: %w", err)
	}
	return t.UTC().Format(cursorTimeFmt), t, nil
}

// Groups runs a group list. An Include of length 0 returns no groups
// without a query.
func (s *Service) Groups(ctx context.Context, scope Scope, q GroupQuery) (GroupList, error) {
	out := GroupList{Groups: []Group{}, SparkBucketSeconds: sparkBucket(q.Filter).Seconds()}
	if q.Include != nil && len(q.Include) == 0 {
		return out, nil
	}
	stmt, err := BuildGroups(scope, q)
	if err != nil {
		return GroupList{}, err
	}
	body, err := s.run(ctx, stmt, MaxGroupLimit+1)
	if err != nil {
		return GroupList{}, err
	}
	var fps []uint64
	err = eachLine(body, func(line []byte) error {
		var r groupRow
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode group: %w", err)
		}
		first, firstT, err := chTimeToAPI(r.FirstSeen)
		if err != nil {
			return err
		}
		last, _, err := chTimeToAPI(r.LastSeen)
		if err != nil {
			return err
		}
		fps = append(fps, uint64(r.Fingerprint))
		out.Groups = append(out.Groups, Group{
			Fingerprint: fingerprint.Hex(uint64(r.Fingerprint)), Title: r.Title, Sample: r.Sample, ErrorType: r.ErrorType,
			Levels: nonNil(r.Levels), Services: nonNil(r.Services), Count: uint64(r.Count),
			FirstSeen: first, LastSeen: last, IsNew: !firstT.Before(q.From),
			Sparkline: make([]uint64, SparkBuckets), Status: StatusOpen,
		})
		return nil
	})
	if err != nil {
		return GroupList{}, err
	}
	if len(out.Groups) > q.Limit {
		out.Groups, fps = out.Groups[:q.Limit], fps[:q.Limit]
		if next := q.Offset + q.Limit; next <= MaxGroupOffset {
			c := encodeGroupCursor(next, q.hash)
			out.NextCursor = &c
		}
	}
	if len(fps) == 0 {
		return out, nil
	}
	stmt, err = BuildSparklines(scope, q, fps)
	if err != nil {
		return GroupList{}, err
	}
	body, err = s.run(ctx, stmt, (MaxGroupLimit+1)*(SparkBuckets+1))
	if err != nil {
		return GroupList{}, err
	}
	index := make(map[uint64]int, len(fps))
	for i, fp := range fps {
		index[fp] = i
	}
	err = eachLine(body, func(line []byte) error {
		var r struct {
			Fingerprint chUint `json:"fingerprint"`
			B           chInt  `json:"b"`
			C           chUint `json:"c"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode sparkline: %w", err)
		}
		i, ok := index[uint64(r.Fingerprint)]
		if ok && r.B >= 0 && r.B < SparkBuckets {
			out.Groups[i].Sparkline[r.B] += uint64(r.C)
		}
		return nil
	})
	if err != nil {
		return GroupList{}, err
	}
	return out, nil
}

// chInt decodes a ClickHouse signed integer, quoted or not.
type chInt int64

func (n *chInt) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("decode integer: %w", err)
	}
	*n = chInt(v)
	return nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// CountBy is one service and level count of a group.
type CountBy struct {
	Service string `json:"service"`
	Level   string `json:"level"`
	Count   uint64 `json:"count"`
}

// GroupDetail is the detail of one group in a window.
type GroupDetail struct {
	Group
	Counts  []CountBy `json:"counts"`
	Series  Histogram `json:"series"`
	Samples []Event   `json:"samples"`
	// Note and UpdatedBy are filled by the caller from PostgreSQL.
	Note      string `json:"note"`
	UpdatedBy string `json:"updated_by"`
}

// ErrGroupNotFound means the project has never seen the fingerprint.
var ErrGroupNotFound = errors.New("group not found")

// BuildGroupCounts counts one group per service and level in a window.
func BuildGroupCounts(scope Scope, f Filter, fp uint64) (Statement, error) {
	f.Fingerprint = fp
	if fp == 0 {
		return Statement{}, errors.New("query: fingerprint is required")
	}
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	sql := "SELECT service, level, count() AS c\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
		"GROUP BY service, level\n" +
		"ORDER BY c DESC, service, level\n" +
		"LIMIT 200\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// BuildGroupSeen reads the all-time first and last seen of one group and
// its latest template, for a group with no rows in the window.
func BuildGroupSeen(scope Scope, fp uint64) (Statement, error) {
	if scope.ProjectID == "" || scope.Alias == "" || fp == 0 {
		return Statement{}, errors.New("query: project scope and fingerprint are required")
	}
	sql := "SELECT min(first_seen) AS first_seen, max(last_seen) AS last_seen, count() AS n\n" +
		"FROM group_seen\n" +
		"WHERE " + projectScope + " AND fingerprint = {fingerprint:UInt64}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: map[string]string{
		"project_id": scope.ProjectID, "project_alias": scope.Alias, "fingerprint": strconv.FormatUint(fp, 10),
	}}, nil
}

// GroupDetail returns one group in the window f: its list item, counts by
// service and level, a time series and the latest samples. A group seen
// only outside the window has count 0 and the title of its last row.
func (s *Service) GroupDetail(ctx context.Context, scope Scope, f Filter, fp uint64) (GroupDetail, error) {
	q := GroupQuery{Filter: f, Sort: SortCount, Include: []uint64{fp}}
	q.Limit = 1
	list, err := s.Groups(ctx, scope, q)
	if err != nil {
		return GroupDetail{}, err
	}
	var d GroupDetail
	if len(list.Groups) == 1 {
		d.Group = list.Groups[0]
	} else {
		// Not in the window: known at all?
		stmt, err := BuildGroupSeen(scope, fp)
		if err != nil {
			return GroupDetail{}, err
		}
		body, err := s.run(ctx, stmt, 1)
		if err != nil {
			return GroupDetail{}, err
		}
		var seen struct {
			First string `json:"first_seen"`
			Last  string `json:"last_seen"`
			N     chUint `json:"n"`
		}
		if err := json.Unmarshal(firstLineOf(body), &seen); err != nil {
			return GroupDetail{}, fmt.Errorf("decode group seen: %w", err)
		}
		if seen.N == 0 {
			return GroupDetail{}, ErrGroupNotFound
		}
		first, _, err := chTimeToAPI(seen.First)
		if err != nil {
			return GroupDetail{}, err
		}
		last, lastT, err := chTimeToAPI(seen.Last)
		if err != nil {
			return GroupDetail{}, err
		}
		d.Group = Group{Fingerprint: fingerprint.Hex(fp), Levels: []string{}, Services: []string{},
			FirstSeen: first, LastSeen: last, Sparkline: make([]uint64, SparkBuckets), Status: StatusOpen}
		// The title and sample of the last row, read in a one-millisecond window.
		edge := GroupQuery{Filter: Filter{From: lastT, To: lastT.Add(time.Millisecond)}, Sort: SortCount, Include: []uint64{fp}}
		edge.Limit = 1
		if l, err := s.Groups(ctx, scope, edge); err == nil && len(l.Groups) == 1 {
			d.Title, d.Sample, d.ErrorType = l.Groups[0].Title, l.Groups[0].Sample, l.Groups[0].ErrorType
		}
	}
	// Counts by service and level.
	stmt, err := BuildGroupCounts(scope, f, fp)
	if err != nil {
		return GroupDetail{}, err
	}
	body, err := s.run(ctx, stmt, 200)
	if err != nil {
		return GroupDetail{}, err
	}
	d.Counts = []CountBy{}
	err = eachLine(body, func(line []byte) error {
		var r struct {
			Service string `json:"service"`
			Level   string `json:"level"`
			C       chUint `json:"c"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode group counts: %w", err)
		}
		d.Counts = append(d.Counts, CountBy{Service: r.Service, Level: r.Level, Count: uint64(r.C)})
		return nil
	})
	if err != nil {
		return GroupDetail{}, err
	}
	// Time series with the histogram's bucket rules, and the latest samples.
	gf := f
	gf.Fingerprint = fp
	if d.Series, err = s.Histogram(ctx, scope, gf, AutoBucket(f.From, f.To)); err != nil {
		return GroupDetail{}, err
	}
	gf.Limit = GroupSamples
	if d.Samples, _, err = s.Search(ctx, scope, gf); err != nil {
		return GroupDetail{}, err
	}
	return d, nil
}

// GroupExists reports whether the project has ever seen the fingerprint.
func (s *Service) GroupExists(ctx context.Context, scope Scope, fp uint64) (bool, error) {
	stmt, err := BuildGroupSeen(scope, fp)
	if err != nil {
		return false, err
	}
	body, err := s.run(ctx, stmt, 1)
	if err != nil {
		return false, err
	}
	var seen struct {
		N chUint `json:"n"`
	}
	if err := json.Unmarshal(firstLineOf(body), &seen); err != nil {
		return false, fmt.Errorf("decode group seen: %w", err)
	}
	return seen.N > 0, nil
}

func firstLineOf(body []byte) []byte {
	if i := strings.IndexByte(string(body), '\n'); i >= 0 {
		return body[:i]
	}
	return body
}

package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Histogram bucket sizes. Each divides a day, so buckets aligned to the Unix
// epoch are also aligned to UTC days.
var bucketSizes = []struct {
	name string
	size time.Duration
}{
	{"10s", 10 * time.Second}, {"30s", 30 * time.Second}, {"1m", time.Minute}, {"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute}, {"1h", time.Hour}, {"6h", 6 * time.Hour}, {"1d", 24 * time.Hour},
}

const (
	// TargetBuckets is the most buckets an automatic bucket size gives.
	TargetBuckets = 240
	// MaxBuckets is the most buckets an explicit bucket parameter may give.
	MaxBuckets = 500
	// ServiceFacetLimit is how many services the facets list by name.
	ServiceFacetLimit = 50
)

// bucketCount is the number of buckets from the bucket holding from up to to.
func bucketCount(from, to time.Time, b time.Duration) int {
	start := from.Truncate(b)
	return int((to.Sub(start) + b - 1) / b)
}

// AutoBucket picks the smallest allowed size giving at most TargetBuckets.
func AutoBucket(from, to time.Time) time.Duration {
	for _, b := range bucketSizes {
		if bucketCount(from, to, b.size) <= TargetBuckets {
			return b.size
		}
	}
	return bucketSizes[len(bucketSizes)-1].size
}

// ParseHistogram validates a histogram query string and returns the filter
// and the bucket size (from the bucket parameter or AutoBucket).
func (s *Service) ParseHistogram(rawQuery string) (Filter, time.Duration, error) {
	vals, f, err := s.parse(rawQuery, histogramParams)
	if err != nil {
		return Filter{}, 0, err
	}
	if _, ok := vals["bucket"]; !ok {
		return f, AutoBucket(f.From, f.To), nil
	}
	v := vals.Get("bucket")
	names := make([]string, len(bucketSizes))
	for i, b := range bucketSizes {
		names[i] = b.name
		if b.name != v {
			continue
		}
		if n := bucketCount(f.From, f.To, b.size); n > MaxBuckets {
			return Filter{}, 0, fmt.Errorf("bucket: %s gives %d buckets for this range, max %d; choose a larger bucket", v, n, MaxBuckets)
		}
		return f, b.size, nil
	}
	return Filter{}, 0, fmt.Errorf("bucket: must be one of %s", strings.Join(names, ", "))
}

// ParseFacets validates a facets query string.
func (s *Service) ParseFacets(rawQuery string) (Filter, error) {
	_, f, err := s.parse(rawQuery, facetParams)
	return f, err
}

// HistogramBucket is the count per level in one bucket starting at T.
type HistogramBucket struct {
	T     string `json:"t"`
	Debug uint64 `json:"debug"`
	Info  uint64 `json:"info"`
	Warn  uint64 `json:"warn"`
	Error uint64 `json:"error"`
	Fatal uint64 `json:"fatal"`
}

// Histogram is the 200 body of a histogram request.
type Histogram struct {
	BucketSeconds int               `json:"bucket_seconds"`
	Buckets       []HistogramBucket `json:"buckets"`
	TookMs        int64             `json:"took_ms"`
}

// chUint decodes a ClickHouse unsigned integer, which JSON output quotes
// when it is 64-bit.
type chUint uint64

func (u *chUint) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseUint(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("decode count: %w", err)
	}
	*u = chUint(n)
	return nil
}

// Histogram counts logs per level per bucket. Every bucket from the one
// holding f.From up to f.To is present; empty ones are zero.
func (s *Service) Histogram(ctx context.Context, scope Scope, f Filter, bucket time.Duration) (Histogram, error) {
	n := bucketCount(f.From, f.To, bucket)
	if n > MaxBuckets {
		return Histogram{}, errors.New("query: too many buckets")
	}
	stmt, err := BuildHistogram(scope, f, bucket)
	if err != nil {
		return Histogram{}, err
	}
	body, err := s.run(ctx, stmt, MaxBuckets+1)
	if err != nil {
		return Histogram{}, err
	}
	type row struct {
		T                               chUint
		Debug, Info, Warn, Error, Fatal chUint
	}
	got := map[int64]row{}
	err = eachLine(body, func(line []byte) error {
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode histogram row: %w", err)
		}
		got[int64(r.T)] = r
		return nil
	})
	if err != nil {
		return Histogram{}, err
	}
	out := Histogram{BucketSeconds: int(bucket / time.Second), Buckets: make([]HistogramBucket, 0, n)}
	for t := f.From.Truncate(bucket); t.Before(f.To); t = t.Add(bucket) {
		r := got[t.Unix()]
		out.Buckets = append(out.Buckets, HistogramBucket{
			T: t.UTC().Format(time.RFC3339), Debug: uint64(r.Debug), Info: uint64(r.Info),
			Warn: uint64(r.Warn), Error: uint64(r.Error), Fatal: uint64(r.Fatal),
		})
	}
	return out, nil
}

// ServiceCount is one service in the facets. Other marks the remainder
// entry added when more than ServiceFacetLimit services matched.
type ServiceCount struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
	Other bool   `json:"other,omitempty"`
}

// Facets is the 200 body of a facets request.
type Facets struct {
	Total    uint64            `json:"total"`
	Levels   map[string]uint64 `json:"levels"`
	Services []ServiceCount    `json:"services"`
	TookMs   int64             `json:"took_ms"`
}

// Facets counts logs per level (every filter except level) and per service
// (every filter except service). Total applies every filter.
func (s *Service) Facets(ctx context.Context, scope Scope, f Filter) (Facets, error) {
	lst, err := BuildLevelFacets(scope, f)
	if err != nil {
		return Facets{}, err
	}
	sst, err := BuildServiceFacets(scope, f, ServiceFacetLimit)
	if err != nil {
		return Facets{}, err
	}

	body, err := s.run(ctx, lst, len(Levels)+1)
	if err != nil {
		return Facets{}, err
	}
	out := Facets{Levels: map[string]uint64{}, Services: []ServiceCount{}}
	for _, l := range Levels {
		out.Levels[l] = 0
	}
	err = eachLine(body, func(line []byte) error {
		var r struct {
			Level string
			N     chUint
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode level facet: %w", err)
		}
		if validLevel(r.Level) {
			out.Levels[r.Level] = uint64(r.N)
		}
		return nil
	})
	if err != nil {
		return Facets{}, err
	}
	for l, n := range out.Levels {
		if len(f.Levels) == 0 || contains(f.Levels, l) {
			out.Total += n
		}
	}

	body, err = s.run(ctx, sst, ServiceFacetLimit+1)
	if err != nil {
		return Facets{}, err
	}
	var groups, all, listed uint64
	err = eachLine(body, func(line []byte) error {
		var r struct {
			Service string
			N       chUint
			Groups  chUint
			AllN    chUint `json:"all_n"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode service facet: %w", err)
		}
		groups, all = uint64(r.Groups), uint64(r.AllN)
		listed += uint64(r.N)
		out.Services = append(out.Services, ServiceCount{Name: r.Service, Count: uint64(r.N)})
		return nil
	})
	if err != nil {
		return Facets{}, err
	}
	if groups > uint64(len(out.Services)) && all > listed {
		out.Services = append(out.Services, ServiceCount{Name: "other", Count: all - listed, Other: true})
	}
	return out, nil
}

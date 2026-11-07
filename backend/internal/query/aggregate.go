package query

import (
	"errors"
	"strconv"
	"time"
)

// BuildHistogram counts logs per level in buckets of bucket (whole seconds,
// aligned to the Unix epoch). Empty buckets are absent; the caller fills them.
func BuildHistogram(scope Scope, f Filter, bucket time.Duration) (Statement, error) {
	if bucket < time.Second || bucket%time.Second != 0 {
		return Statement{}, errors.New("query: bucket must be whole seconds")
	}
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	params["bucket"] = strconv.Itoa(int(bucket / time.Second))
	sql := "SELECT intDiv(toUnixTimestamp(timestamp), {bucket:UInt32}) * {bucket:UInt32} AS t,\n" +
		"  countIf(level = 'debug') AS debug, countIf(level = 'info') AS info, countIf(level = 'warn') AS warn,\n" +
		"  countIf(level = 'error') AS error, countIf(level = 'fatal') AS fatal\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
		"GROUP BY t\n" +
		"ORDER BY t\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// BuildLevelFacets counts logs per level with every filter except the level
// filter, so the sidebar shows which levels are available.
func BuildLevelFacets(scope Scope, f Filter) (Statement, error) {
	conds, params, err := where(scope, f, skipLevels)
	if err != nil {
		return Statement{}, err
	}
	sql := "SELECT level, count() AS n\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
		"GROUP BY level\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// BuildServiceFacets counts logs per service with every filter except the
// service filter: the top limit services by count, each row also carrying
// the number of services and the total over all of them.
func BuildServiceFacets(scope Scope, f Filter, limit int) (Statement, error) {
	if limit <= 0 {
		return Statement{}, errors.New("query: limit must be positive")
	}
	conds, params, err := where(scope, f, skipServices)
	if err != nil {
		return Statement{}, err
	}
	params["limit"] = strconv.Itoa(limit)
	sql := "SELECT service, count() AS n, count() OVER () AS groups, sum(count()) OVER () AS all_n\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
		"GROUP BY service\n" +
		"ORDER BY n DESC, service ASC\n" +
		"LIMIT {limit:UInt32}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

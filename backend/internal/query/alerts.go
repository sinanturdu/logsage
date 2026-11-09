package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Alert queries (ADR 0024). The evaluator in cmd/alerts runs these through
// the same Service as every other read: project scope, readonly, the time
// limit and a row limit. Their log_comment is "alerts", so they can be told
// apart in system.query_log.

// AlertWindow is the event-time window of one evaluation: [From, To).
type AlertWindow struct {
	From, To time.Time
	Services []string
	Levels   []string
}

// MaxAlertGroups bounds the groups one new_group or regression query returns.
const MaxAlertGroups = 100

func (w AlertWindow) conds(scope Scope) ([]string, map[string]string, error) {
	if scope.ProjectID == "" || scope.Alias == "" {
		return nil, nil, errors.New("query: project scope is required")
	}
	if !w.From.Before(w.To) {
		return nil, nil, errors.New("query: alert window must not be empty")
	}
	conds := []string{
		projectScope,
		"timestamp >= {from:DateTime64(3, 'UTC')}",
		"timestamp < {to:DateTime64(3, 'UTC')}",
	}
	params := map[string]string{
		"project_id": scope.ProjectID, "project_alias": scope.Alias,
		"from": w.From.UTC().Format(chTime), "to": w.To.UTC().Format(chTime),
	}
	if len(w.Services) > 0 {
		conds = append(conds, "service IN {services:Array(String)}")
		params["services"] = arrayParam(w.Services)
	}
	if len(w.Levels) > 0 {
		conds = append(conds, "level IN {levels:Array(String)}")
		params["levels"] = arrayParam(w.Levels)
	}
	return conds, params, nil
}

// BuildAlertCounts counts the logs of a window and the error and fatal ones.
func BuildAlertCounts(scope Scope, w AlertWindow) (Statement, error) {
	conds, params, err := w.conds(scope)
	if err != nil {
		return Statement{}, err
	}
	sql := "SELECT count() AS total, countIf(level IN ('error', 'fatal')) AS errors\nFROM logs FINAL\n" + joinWhere(conds) + "FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// firstSeenIn selects the fingerprints whose all-time first seen is in the window.
const firstSeenIn = "SELECT fingerprint FROM group_seen WHERE " + projectScope +
	" GROUP BY fingerprint HAVING min(first_seen) >= {from:DateTime64(3, 'UTC')} AND min(first_seen) < {to:DateTime64(3, 'UTC')}"

// BuildNewGroupCount counts the groups first seen in the window (the
// new_groups_per_hour guard). Services and levels do not apply.
func BuildNewGroupCount(scope Scope, w AlertWindow) (Statement, error) {
	w.Services, w.Levels = nil, nil
	_, params, err := w.conds(scope)
	if err != nil {
		return Statement{}, err
	}
	return Statement{SQL: "SELECT count() AS total FROM (" + firstSeenIn + ")\nFORMAT JSONEachRow", Params: params}, nil
}

// BuildNewGroups lists groups first seen in the window with at least
// minEvents events in it, largest first.
func BuildNewGroups(scope Scope, w AlertWindow, minEvents uint64) (Statement, error) {
	conds, params, err := w.conds(scope)
	if err != nil {
		return Statement{}, err
	}
	conds = append(conds, "fingerprint != 0", "fingerprint IN ("+firstSeenIn+")")
	params["min_events"] = strconv.FormatUint(max(minEvents, 1), 10)
	params["limit"] = strconv.Itoa(MaxAlertGroups)
	sql := "SELECT fingerprint, count() AS n, argMax(template, timestamp) AS title, argMax(message, timestamp) AS sample, max(timestamp) AS last\nFROM logs FINAL\n" + joinWhere(conds) +
		"GROUP BY fingerprint\nHAVING n >= {min_events:UInt64}\nORDER BY n DESC, fingerprint\nLIMIT {limit:UInt32}\nFORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// BuildGroupActivity returns, for the given fingerprints, their count and
// latest event in the window (the regression rule compares that with
// resolved_at).
func BuildGroupActivity(scope Scope, w AlertWindow, fps []uint64) (Statement, error) {
	conds, params, err := w.conds(scope)
	if err != nil {
		return Statement{}, err
	}
	if len(fps) == 0 || len(fps) > 10_000 {
		return Statement{}, errors.New("query: between 1 and 10000 fingerprints")
	}
	conds = append(conds, "fingerprint IN {fps:Array(UInt64)}")
	params["fps"] = uintArray(fps)
	sql := "SELECT fingerprint, count() AS n, argMax(template, timestamp) AS title, argMax(message, timestamp) AS sample, max(timestamp) AS last\nFROM logs FINAL\n" + joinWhere(conds) +
		"GROUP BY fingerprint\nORDER BY fingerprint\nFORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

// AlertGroup is one group found by an alert query.
type AlertGroup struct {
	Fingerprint uint64
	Count       uint64
	Title       string
	Sample      string // the latest raw message; sent only to channels that opted in
	Last        time.Time
}

func (s *Service) alertRun(ctx context.Context, stmt Statement, maxRows int) ([]byte, error) {
	return s.runWith(ctx, stmt, maxRows, map[string]string{"log_comment": "alerts"})
}

// AlertCounts returns the logs and the error and fatal logs in a window.
func (s *Service) AlertCounts(ctx context.Context, scope Scope, w AlertWindow) (total, errs uint64, err error) {
	stmt, err := BuildAlertCounts(scope, w)
	if err != nil {
		return 0, 0, err
	}
	body, err := s.alertRun(ctx, stmt, 1)
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Total  chUint `json:"total"`
		Errors chUint `json:"errors"`
	}
	err = eachLine(body, func(line []byte) error { return json.Unmarshal(line, &r) })
	if err != nil {
		return 0, 0, fmt.Errorf("decode alert counts: %w", err)
	}
	return uint64(r.Total), uint64(r.Errors), nil
}

// NewGroupCount returns how many groups were first seen in the window.
func (s *Service) NewGroupCount(ctx context.Context, scope Scope, w AlertWindow) (uint64, error) {
	stmt, err := BuildNewGroupCount(scope, w)
	if err != nil {
		return 0, err
	}
	body, err := s.alertRun(ctx, stmt, 1)
	if err != nil {
		return 0, err
	}
	var r struct {
		Total chUint `json:"total"`
	}
	if err := eachLine(body, func(line []byte) error { return json.Unmarshal(line, &r) }); err != nil {
		return 0, fmt.Errorf("decode new group count: %w", err)
	}
	return uint64(r.Total), nil
}

// NewGroups returns groups first seen in the window with at least minEvents events.
func (s *Service) NewGroups(ctx context.Context, scope Scope, w AlertWindow, minEvents uint64) ([]AlertGroup, error) {
	stmt, err := BuildNewGroups(scope, w, minEvents)
	if err != nil {
		return nil, err
	}
	return s.alertGroups(ctx, stmt)
}

// GroupActivity returns the given groups' counts and latest events in the window.
func (s *Service) GroupActivity(ctx context.Context, scope Scope, w AlertWindow, fps []uint64) ([]AlertGroup, error) {
	if len(fps) == 0 {
		return nil, nil
	}
	stmt, err := BuildGroupActivity(scope, w, fps)
	if err != nil {
		return nil, err
	}
	return s.alertGroups(ctx, stmt)
}

func (s *Service) alertGroups(ctx context.Context, stmt Statement) ([]AlertGroup, error) {
	body, err := s.alertRun(ctx, stmt, 10_001)
	if err != nil {
		return nil, err
	}
	var out []AlertGroup
	err = eachLine(body, func(line []byte) error {
		var r struct {
			Fingerprint chUint `json:"fingerprint"`
			N           chUint `json:"n"`
			Title       string `json:"title"`
			Sample      string `json:"sample"`
			Last        string `json:"last"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		last, err := time.ParseInLocation(chTime, r.Last, time.UTC)
		if err != nil {
			return err
		}
		out = append(out, AlertGroup{Fingerprint: uint64(r.Fingerprint), Count: uint64(r.N), Title: r.Title, Sample: r.Sample, Last: last})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("decode alert groups: %w", err)
	}
	return out, nil
}

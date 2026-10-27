package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const installScopeSQL = "r_type NOT IN ('pve','addon','tool')"

// BreakdownRow counts runs that share one value of a dimension.
type BreakdownRow struct {
	Label       string  `json:"label"`
	Runs        int     `json:"runs"`
	Success     int     `json:"success"`
	Failed      int     `json:"failed"`
	Aborted     int     `json:"aborted"`
	NoResult    int     `json:"no_result"`
	InProgress  int     `json:"in_progress"`
	FailureRate float64 `json:"failure_rate"`
	FailedApps  int     `json:"failed_apps,omitempty"`
	TopApps     string  `json:"top_failed_apps,omitempty"`
	durSum      int64
	durN        int64
}

func failureRate(success, failed int) float64 {
	if success+failed == 0 {
		return 0
	}
	return float64(failed) / float64(success+failed) * 100
}

// Scoped dims count install types only, unless the filter names a type.
type breakdownDim struct {
	name   string
	expr   string
	cond   string
	scoped bool
}

var (
	dimAll      = breakdownDim{"all", "''", "", true}
	dimApp      = breakdownDim{"app", "r_app", "r_app != ''", true}
	dimDay      = breakdownDim{"day", "toString(toDate(r_started, 'UTC'))", "", true}
	dimHour     = breakdownDim{"hour", "toString(toStartOfHour(r_started), 'UTC')", "", true}
	dimOS       = breakdownDim{"os", "r_os", "r_os != ''", true}
	dimMethod   = breakdownDim{"method", "r_method", "r_method != ''", true}
	dimPVE      = breakdownDim{"pve", "r_pve", "r_pve != ''", true}
	dimGPU      = breakdownDim{"gpu", "concat(r_gpu_vendor, '|', r_gpu_pass)", "r_gpu_vendor NOT IN ('', 'unknown')", true}
	dimPlatform = breakdownDim{"platform", "if(r_platform = '', 'unknown', r_platform)", "", true}
	dimCategory = breakdownDim{"category", "r_cat", "r_outcome = 'failed' AND r_cat NOT IN ('', 'user_aborted')", true}
	dimExit     = breakdownDim{"exit", "toString(r_exit)", "r_outcome = 'failed'", true}
	dimType     = breakdownDim{"type", "if(r_type = '', 'unknown', r_type)", "", false}
	dimSlug     = breakdownDim{"slug", "r_slug", "r_slug != ''", false}
	dimTool     = breakdownDim{"tool", "r_app", "r_type IN ('pve','tool') AND r_app != ''", false}
	dimAddon    = breakdownDim{"addon", "r_app", "r_type = 'addon' AND r_app != ''", false}
)

// breakdownSQL counts every dim in one pass, one joined row per run and dim.
func breakdownSQL(f RunFilter, dims []breakdownDim) (string, []interface{}) {
	scope := "1"
	if f.Type == "" {
		scope = installScopeSQL
		f.Type = "all"
	}
	tuples := make([]string, len(dims))
	for i, d := range dims {
		cond := "1"
		if d.cond != "" {
			cond = "(" + d.cond + ")"
		}
		if d.scoped {
			cond = "(" + scope + ") AND " + cond
		}
		tuples[i] = "tuple('" + d.name + "', toString(" + d.expr + "), toUInt8(" + cond + "))"
	}
	sel := `tupleElement(kv, 1) AS b_dim, tupleElement(kv, 2) AS b_val,
	toInt64(count()), toInt64(countIf(r_outcome = 'success')), toInt64(countIf(r_outcome = 'failed')),
	toInt64(countIf(r_outcome = 'aborted')), toInt64(countIf(r_outcome = 'unknown')),
	toInt64(countIf(r_outcome = 'in_progress')),
	toInt64(uniqExactIf(r_app, r_outcome = 'failed')),
	arrayStringConcat(topKIf(5)(r_app, r_outcome = 'failed'), ', '),
	toInt64(sumIf(dateDiff('second', r_started, r_end), r_outcome = 'success' AND r_end > r_started)),
	toInt64(countIf(r_outcome = 'success' AND r_end > r_started))`
	join := "ARRAY JOIN arrayFilter(x -> tupleElement(x, 3) = 1, [" + strings.Join(tuples, ", ") + "]) AS kv"
	return f.sql(runQuery{sel: sel, join: join, tail: "\nGROUP BY b_dim, b_val"})
}

// Breakdown maps a dim name to its rows, most runs first.
type Breakdown map[string][]BreakdownRow

func (b Breakdown) top(dim string, n int) []BreakdownRow {
	rows := b[dim]
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

func (b Breakdown) one(dim string) BreakdownRow {
	if rows := b[dim]; len(rows) > 0 {
		return rows[0]
	}
	return BreakdownRow{}
}

type dimRow struct {
	dim string
	row BreakdownRow
}

func newBreakdown(rows []dimRow) Breakdown {
	out := Breakdown{}
	for _, r := range rows {
		r.row.FailureRate = failureRate(r.row.Success, r.row.Failed)
		out[r.dim] = append(out[r.dim], r.row)
	}
	for k := range out {
		rows := out[k]
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Runs != rows[j].Runs {
				return rows[i].Runs > rows[j].Runs
			}
			return rows[i].Label < rows[j].Label
		})
	}
	return out
}

func (ch *CHClient) fetchBreakdown(ctx context.Context, f RunFilter, dims []breakdownDim) (Breakdown, error) {
	q, args := breakdownSQL(f, dims)
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CH breakdown: %w", err)
	}
	defer rows.Close()
	var raw []dimRow
	for rows.Next() {
		var dim string
		var r BreakdownRow
		var runs, ok, bad, ab, nores, running, apps int64
		if err := rows.Scan(&dim, &r.Label, &runs, &ok, &bad, &ab, &nores, &running, &apps, &r.TopApps, &r.durSum, &r.durN); err != nil {
			return nil, fmt.Errorf("CH breakdown scan: %w", err)
		}
		r.Runs, r.Success, r.Failed, r.Aborted = int(runs), int(ok), int(bad), int(ab)
		r.NoResult, r.InProgress, r.FailedApps = int(nores), int(running), int(apps)
		raw = append(raw, dimRow{dim, r})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("CH breakdown: %w", err)
	}
	return newBreakdown(raw), nil
}

// RunSummary is the headline of a filtered set of runs.
type RunSummary struct {
	Runs        int     `json:"runs"`
	Success     int     `json:"success"`
	Failed      int     `json:"failed"`
	Aborted     int     `json:"aborted"`
	NoResult    int     `json:"no_result"`
	InProgress  int     `json:"in_progress"`
	FailureRate float64 `json:"failure_rate"`
	SuccessRate float64 `json:"success_rate"`
	AppsFailing int     `json:"apps_failing"`
	AvgDuration float64 `json:"avg_duration"`
}

func summaryFrom(all BreakdownRow) RunSummary {
	s := RunSummary{
		Runs: all.Runs, Success: all.Success, Failed: all.Failed, Aborted: all.Aborted,
		NoResult: all.NoResult, InProgress: all.InProgress, AppsFailing: all.FailedApps,
		FailureRate: failureRate(all.Success, all.Failed),
	}
	if all.Success+all.Failed > 0 {
		s.SuccessRate = 100 - s.FailureRate
	}
	if all.durN > 0 {
		s.AvgDuration = float64(all.durSum) / float64(all.durN)
	}
	return s
}

// Cause groups failed runs by what went wrong.
type Cause struct {
	Key             string      `json:"key"`
	Kind            string      `json:"kind"`
	Message         string      `json:"message"`
	ExampleMessage  string      `json:"example_message,omitempty"`
	Category        string      `json:"category"`
	ExitCode        int         `json:"exit_code"`
	ExitDescription string      `json:"exit_description"`
	Failures        int         `json:"failures"`
	Apps            int         `json:"apps"`
	TopApps         []string    `json:"top_apps"`
	Setups          int         `json:"setups"`
	FirstSeen       string      `json:"first_seen"`
	LastSeen        string      `json:"last_seen"`
	Last24h         int         `json:"last_24h"`
	Prev24h         int         `json:"prev_24h"`
	Trend           string      `json:"trend"`
	Examples        []string    `json:"examples"`
	ExampleRuns     []RunRecord `json:"example_runs,omitempty"`
	firstMs         int64
}

const causeSelect = `r_cause AS c_key, any(r_cause_kind), arrayElement(topK(1)(r_cat), 1),
	toInt64(arrayElement(topK(1)(r_exit), 1)), toInt64(count()) AS c_failures, toInt64(uniqExact(r_app)),
	arrayStringConcat(topK(5)(r_app), ','), toInt64(uniqExact(r_setup)),
	toInt64(toUnixTimestamp64Milli(min(r_end))), toInt64(toUnixTimestamp64Milli(max(r_end))),
	arrayStringConcat(arrayMap(x -> tupleElement(x, 2), arraySlice(arrayReverseSort(groupArray(tuple(r_end, run_id))), 1, 3)), ',')`

func causesSQL(f RunFilter, limit int) (string, []interface{}) {
	return f.sql(runQuery{sel: causeSelect, where: "r_outcome = 'failed'",
		tail: fmt.Sprintf("\nGROUP BY c_key\nORDER BY c_failures DESC, c_key\nLIMIT %d", limit)})
}

func trendSQL(f RunFilter, now time.Time) (string, []interface{}) {
	f.Days = 0
	f.rowsFrom = now.Add(-72 * time.Hour)
	f.endFrom = now.Add(-48 * time.Hour)
	day := now.Add(-24 * time.Hour)
	return f.sql(runQuery{
		sel:     "r_cause AS t_key, toInt64(countIf(r_end >= ?)), toInt64(countIf(r_end < ?))",
		selArgs: []interface{}{day, day},
		where:   "r_outcome = 'failed'",
		tail:    "\nGROUP BY t_key",
	})
}

// causeTrend compares the last 24 hours with the 24 before them.
func causeTrend(firstMs int64, last, prev int, now time.Time) string {
	switch {
	case last > 0 && firstMs >= now.Add(-24*time.Hour).UnixMilli():
		return "new"
	case last == 0 && prev == 0:
		return "quiet"
	case last == 0:
		return "stopped"
	case last >= prev+2 && float64(last) >= 1.5*float64(prev):
		return "rising"
	case prev >= last+2 && float64(prev) >= 1.5*float64(last):
		return "falling"
	}
	return "steady"
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func causeHeadline(kind, key string, exitCode int) string {
	if kind == "exit_code" {
		return fmt.Sprintf("Exit %d: %s", exitCode, getExitCodeDescription(exitCode))
	}
	return key
}

// FetchCauses with full set attaches the example runs with their traces.
func (ch *CHClient) FetchCauses(ctx context.Context, f RunFilter, limit int, full bool) ([]Cause, error) {
	now := time.Now().UTC()
	q, args := causesSQL(f, limit)
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CH causes: %w", err)
	}
	causes := []Cause{}
	for rows.Next() {
		var c Cause
		var exit, n, apps, setups, lastMs int64
		var topApps, examples string
		if err := rows.Scan(&c.Key, &c.Kind, &c.Category, &exit, &n, &apps, &topApps, &setups,
			&c.firstMs, &lastMs, &examples); err != nil {
			rows.Close()
			return nil, fmt.Errorf("CH causes scan: %w", err)
		}
		c.ExitCode, c.Failures, c.Apps, c.Setups = int(exit), int(n), int(apps), int(setups)
		c.ExitDescription = getExitCodeDescription(c.ExitCode)
		c.Message = causeHeadline(c.Kind, c.Key, c.ExitCode)
		c.TopApps, c.Examples = splitList(topApps), splitList(examples)
		c.FirstSeen, c.LastSeen = msTime(c.firstMs), msTime(lastMs)
		causes = append(causes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("CH causes: %w", err)
	}
	if len(causes) == 0 {
		return causes, nil
	}

	tq, targs := trendSQL(f, now)
	trend := map[string][2]int{}
	if trows, err := ch.db.QueryContext(ctx, tq, targs...); err == nil {
		for trows.Next() {
			var k string
			var last, prev int64
			if trows.Scan(&k, &last, &prev) == nil {
				trend[k] = [2]int{int(last), int(prev)}
			}
		}
		trows.Close()
	} else {
		return nil, fmt.Errorf("CH cause trend: %w", err)
	}
	for i := range causes {
		t := trend[causes[i].Key]
		causes[i].Last24h, causes[i].Prev24h = t[0], t[1]
		causes[i].Trend = causeTrend(causes[i].firstMs, t[0], t[1], now)
	}

	var ids []string
	for _, c := range causes {
		switch {
		case full:
			ids = append(ids, c.Examples...)
		case c.Kind != "message" && len(c.Examples) > 0:
			ids = append(ids, c.Examples[0])
		}
	}
	if len(ids) == 0 {
		return causes, nil
	}
	recs, err := ch.fetchRunDetails(ctx, ids, f.rowFloor(), full)
	if err != nil {
		return nil, err
	}
	attachExamples(causes, recs, full)
	return causes, nil
}

func (f RunFilter) rowFloor() time.Time {
	if f.Days > 0 {
		return f.since().Add(-24 * time.Hour)
	}
	return time.Time{}
}

func attachExamples(causes []Cause, recs []RunRecord, full bool) {
	byID := make(map[string]RunRecord, len(recs))
	for _, r := range recs {
		id := r.ExecutionID
		if id == "" {
			id = r.RandomID
		}
		byID[id] = r
	}
	for i := range causes {
		c := &causes[i]
		for j, id := range c.Examples {
			r, ok := byID[id]
			if !ok {
				continue
			}
			if j == 0 && c.Kind != "message" && r.ErrorMessage != "" && r.ErrorMessage != c.Key {
				c.ExampleMessage = r.ErrorMessage
			}
			if full {
				c.ExampleRuns = append(c.ExampleRuns, r)
			}
		}
	}
}

// AppErrorStat is one application's runs and how they failed.
type AppErrorStat struct {
	App           string  `json:"app"`
	Type          string  `json:"type"`
	Runs          int     `json:"runs"`
	Success       int     `json:"success"`
	Failed        int     `json:"failed"`
	Aborted       int     `json:"aborted"`
	NoResult      int     `json:"no_result"`
	InProgress    int     `json:"in_progress"`
	FailureRate   float64 `json:"failure_rate"`
	SetupsFailed  int     `json:"setups_failed"`
	TopCause      string  `json:"top_cause,omitempty"`
	TopCauseKind  string  `json:"top_cause_kind,omitempty"`
	TopCauseText  string  `json:"top_cause_message,omitempty"`
	TopExitCode   int     `json:"top_exit_code,omitempty"`
	TopCategory   string  `json:"top_category,omitempty"`
	LastFailure   string  `json:"last_failure,omitempty"`
	LastSuccess   string  `json:"last_success,omitempty"`
	FailingNow    bool    `json:"failing_now"`
	LastFailureID string  `json:"last_failure_id,omitempty"`
	lastFailMs    int64
	lastOkMs      int64
}

const appSelect = `r_app AS a_app, arrayElement(topK(1)(r_type), 1), toInt64(count()) AS a_runs,
	toInt64(countIf(r_outcome = 'success')), toInt64(countIf(r_outcome = 'failed')) AS a_failed,
	toInt64(countIf(r_outcome = 'aborted')), toInt64(countIf(r_outcome = 'unknown')),
	toInt64(countIf(r_outcome = 'in_progress')),
	toInt64(uniqExactIf(r_setup, r_outcome = 'failed')),
	arrayElement(topKIf(1)(concat(r_cause_kind, ':', r_cause), r_outcome = 'failed'), 1),
	toInt64(arrayElement(topKIf(1)(r_exit, r_outcome = 'failed'), 1)),
	arrayElement(topKIf(1)(r_cat, r_outcome = 'failed'), 1),
	toInt64(toUnixTimestamp64Milli(maxIf(r_end, r_outcome = 'failed'))),
	toInt64(toUnixTimestamp64Milli(maxIf(r_end, r_outcome = 'success'))),
	argMaxIf(run_id, r_end, r_outcome = 'failed')`

func appStatsSQL(f RunFilter, onlyFailing bool, limit int) (string, []interface{}) {
	tail := "\nGROUP BY a_app"
	if onlyFailing {
		tail += "\nHAVING a_failed > 0\nORDER BY a_failed DESC, a_app"
	} else {
		tail += "\nORDER BY a_runs DESC, a_app"
	}
	if limit > 0 {
		tail += fmt.Sprintf("\nLIMIT %d", limit)
	}
	return f.sql(runQuery{sel: appSelect, where: "r_app != ''", tail: tail})
}

func (ch *CHClient) FetchAppStats(ctx context.Context, f RunFilter, onlyFailing bool, limit int) ([]AppErrorStat, error) {
	q, args := appStatsSQL(f, onlyFailing, limit)
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CH app stats: %w", err)
	}
	defer rows.Close()
	out := []AppErrorStat{}
	for rows.Next() {
		var a AppErrorStat
		var runs, ok, bad, ab, nores, running, setups, exit int64
		var cause string
		if err := rows.Scan(&a.App, &a.Type, &runs, &ok, &bad, &ab, &nores, &running, &setups,
			&cause, &exit, &a.TopCategory, &a.lastFailMs, &a.lastOkMs, &a.LastFailureID); err != nil {
			return nil, fmt.Errorf("CH app stats scan: %w", err)
		}
		a.Runs, a.Success, a.Failed, a.Aborted = int(runs), int(ok), int(bad), int(ab)
		a.NoResult, a.InProgress, a.SetupsFailed, a.TopExitCode = int(nores), int(running), int(setups), int(exit)
		a.finish(cause)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (a *AppErrorStat) finish(kindKey string) {
	a.FailureRate = failureRate(a.Success, a.Failed)
	if kind, key, ok := strings.Cut(kindKey, ":"); ok && a.Failed > 0 {
		a.TopCauseKind, a.TopCause = kind, key
		a.TopCauseText = causeHeadline(kind, key, a.TopExitCode)
	}
	a.LastFailure, a.LastSuccess = msTime(a.lastFailMs), msTime(a.lastOkMs)
	a.FailingNow = a.lastFailMs > 0 && a.lastFailMs > a.lastOkMs
}

// minRunsForRate is the finished-run floor for ranking a failure rate.
func minRunsForRate(days int) int {
	switch {
	case days <= 1:
		return 5
	case days <= 7:
		return 15
	case days <= 30:
		return 40
	default:
		return 100
	}
}

func rankFailureRates(apps []AppErrorStat, minRuns, n int) []AppErrorStat {
	out := make([]AppErrorStat, 0, len(apps))
	for _, a := range apps {
		if a.Failed > 0 && a.Success+a.Failed >= minRuns {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FailureRate != out[j].FailureRate {
			return out[i].FailureRate > out[j].FailureRate
		}
		if out[i].Failed != out[j].Failed {
			return out[i].Failed > out[j].Failed
		}
		return out[i].App < out[j].App
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// StuckRuns splits the runs of the last 24 hours that have no result yet.
type StuckRuns struct {
	Running    int `json:"running"`
	Stalled    int `json:"stalled"`
	NoResult   int `json:"no_result"`
	AfterHours int `json:"after_hours"`
}

func stuckSQL(f RunFilter, stuckHours int, now time.Time) (string, []interface{}) {
	f.Days = 0
	f.Status = ""
	f.rowsFrom = now.Add(-48 * time.Hour)
	f.startedFrom = now.Add(-24 * time.Hour)
	cut := now.Add(-time.Duration(stuckHours) * time.Hour)
	return f.sql(runQuery{
		sel: `toInt64(countIf(r_outcome = 'in_progress' AND r_last_seen >= ?)),
	toInt64(countIf(r_outcome = 'in_progress' AND r_last_seen < ?)),
	toInt64(countIf(r_outcome = 'unknown'))`,
		selArgs: []interface{}{cut, cut},
	})
}

func (ch *CHClient) FetchStuckRuns(ctx context.Context, f RunFilter, stuckHours int) (StuckRuns, error) {
	s := StuckRuns{AfterHours: stuckHours}
	q, args := stuckSQL(f, stuckHours, time.Now().UTC())
	var running, stalled, nores int64
	if err := ch.db.QueryRowContext(ctx, q, args...).Scan(&running, &stalled, &nores); err != nil {
		return s, fmt.Errorf("CH stuck runs: %w", err)
	}
	s.Running, s.Stalled, s.NoResult = int(running), int(stalled), int(nores)
	return s, nil
}

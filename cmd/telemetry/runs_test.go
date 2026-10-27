package main

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// stripLiterals blanks out quoted SQL strings so checks see only SQL.
func stripLiterals(q string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case in && c == '\\':
			i++
		case c == '\'':
			in = !in
			b.WriteByte('\'')
		case !in:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func placeholderCount(q string) int { return strings.Count(stripLiterals(q), "?") }

var tableColumnsRe = regexp.MustCompile(`(?m)^\s+([a-z_]+)\s+(?:String|UInt8|UInt16|UInt32|Int16|DateTime64\(3\)|LowCardinality)|ADD COLUMN IF NOT EXISTS ([a-z_]+)`)

// tableColumns reads the telemetry table's columns from the migration.
func tableColumns(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("clickhouse.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "CREATE TABLE IF NOT EXISTS telemetry_db.telemetry (")
	end := strings.Index(s, "// Backfill materialized views")
	if start < 0 || end < 0 {
		t.Fatal("telemetry schema not found in clickhouse.go")
	}
	create := s[start : strings.Index(s[start:], ") ENGINE")+start]
	alters := s[strings.Index(s, "alters := []string{"):end]
	seen := map[string]bool{}
	var cols []string
	for _, m := range tableColumnsRe.FindAllStringSubmatch(create+"\n"+alters, -1) {
		c := m[1] + m[2]
		if !seen[c] {
			seen[c] = true
			cols = append(cols, c)
		}
	}
	if len(cols) < 40 {
		t.Fatalf("read only %d columns: %v", len(cols), cols)
	}
	return cols
}

func fullFilter() RunFilter {
	code := 100
	return RunFilter{
		Days: 7, RepoSource: "external", RepoSlug: "me/fork", Platform: "incus", Type: "lxc",
		App: "docker", Search: "dock", OS: "debian", Status: "installing", ExitCode: &code,
		Category: "apt", Message: "E: boom", Cause: "E: boom", ExecutionID: "e1", RandomID: "r1",
	}
}

func TestRunAliasesNeverShadowAColumn(t *testing.T) {
	cols := map[string]bool{}
	for _, c := range tableColumns(t) {
		cols[c] = true
	}
	alias := regexp.MustCompile(` AS ([a-z_0-9]+)$`)
	seen := map[string]bool{}
	for _, c := range runColumns(true) {
		m := alias.FindStringSubmatch(c)
		if m == nil {
			t.Fatalf("column without alias: %s", c)
		}
		if cols[m[1]] {
			t.Errorf("alias %s is also a table column", m[1])
		}
		if seen[m[1]] {
			t.Errorf("alias %s defined twice", m[1])
		}
		seen[m[1]] = true
	}
}

// Outer queries read the run subquery, so a raw column there does not exist.
func TestOuterQueriesUseOnlyRunAliases(t *testing.T) {
	ow, _ := fullFilter().runWhere()
	bsel, _ := breakdownSQL(RunFilter{Days: 1}, []breakdownDim{dimAll, dimApp, dimDay, dimHour, dimOS, dimMethod,
		dimPVE, dimGPU, dimPlatform, dimCategory, dimExit, dimType, dimSlug, dimTool, dimAddon})
	outer := bsel[:strings.Index(bsel, "FROM (")] + bsel[strings.LastIndex(bsel, "GROUP BY run_id")+len("GROUP BY run_id"):]
	for name, text := range map[string]string{
		"runWhere": ow, "details": runDetailCols, "causes": causeSelect, "apps": appSelect, "breakdown": outer,
	} {
		plain := stripLiterals(text)
		for _, c := range tableColumns(t) {
			if regexp.MustCompile(`\b` + c + `\b`).MatchString(plain) {
				t.Errorf("%s reads raw column %q", name, c)
			}
		}
	}
	rw, _ := fullFilter().rowWhere()
	if strings.Contains(rw, "r_") {
		t.Errorf("row filter reads a run alias: %s", rw)
	}
}

func TestEveryQueryBindsEachPlaceholder(t *testing.T) {
	now := time.Now()
	f := fullFilter()
	list, la := f.sql(runQuery{sel: "run_id", tail: "\nORDER BY r_started"})
	b, ba := breakdownSQL(f, []breakdownDim{dimAll, dimApp, dimExit, dimType})
	c, ca := causesSQL(f, 10)
	tr, ta := trendSQL(f, now)
	ap, aa := appStatsSQL(f, true, 5)
	st, sa := stuckSQL(f, 4, now)
	for name, q := range map[string]struct {
		q string
		n int
	}{
		"list": {list, len(la)}, "breakdown": {b, len(ba)}, "causes": {c, len(ca)},
		"trend": {tr, len(ta)}, "apps": {ap, len(aa)}, "stuck": {st, len(sa)},
	} {
		if got := placeholderCount(q.q); got != q.n {
			t.Errorf("%s: %d placeholders, %d args\n%s", name, got, q.n, q.q)
		}
		if strings.Contains(q.q, "%!") {
			t.Errorf("%s: unconsumed format verb", name)
		}
		if regexp.MustCompile(`\$[0-9]`).MatchString(q.q) {
			t.Errorf("%s: $N would switch the driver to numeric binding", name)
		}
		if strings.Count(stripLiterals(q.q), "?") != strings.Count(q.q, "?") {
			t.Errorf("%s: a ? inside a string literal would be bound", name)
		}
	}
}

func TestArgumentsFollowPlaceholderOrder(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	_, args := trendSQL(RunFilter{Days: 30, App: "docker"}, now)
	want := []interface{}{now.Add(-24 * time.Hour), now.Add(-24 * time.Hour), now.Add(-72 * time.Hour), "docker", now.Add(-48 * time.Hour)}
	if len(args) != len(want) {
		t.Fatalf("args %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d = %v, want %v", i, args[i], want[i])
		}
	}
}

func TestRowWindowReachesADayBeforeTheRunWindow(t *testing.T) {
	f := RunFilter{Days: 7}
	_, ra := f.rowWhere()
	_, oa := f.runWhere()
	if ra[0].(time.Time) != f.since().Add(-24*time.Hour) || oa[0].(time.Time) != f.since() {
		t.Errorf("row %v run %v since %v", ra[0], oa[0], f.since())
	}
	if w, a := (RunFilter{}).rowWhere(); w != "1=1" || len(a) != 0 {
		t.Errorf("all time: %s %v", w, a)
	}
}

func TestRunFilterPredicates(t *testing.T) {
	cases := []struct {
		f    RunFilter
		want string
		arg  interface{}
	}{
		{RunFilter{Type: ""}, "r_type NOT IN ('pve','addon','tool')", nil},
		{RunFilter{Type: "addon"}, "r_type = ?", "addon"},
		{RunFilter{Type: "all", Status: "configuring"}, "r_outcome = 'in_progress' AND r_stage = ?", "configuring"},
		{RunFilter{Type: "all", Status: "unknown"}, "r_outcome = ?", "unknown"},
		{RunFilter{Type: "all", RepoSource: "ProxmoxVE"}, "r_source IN ('ProxmoxVE','')", nil},
		{RunFilter{Type: "all", RepoSource: "DevScripts"}, "r_source IN ('DevScripts','ProxmoxVED')", nil},
		{RunFilter{Type: "all", Platform: "pve"}, "r_platform = 'pve'", nil},
		{RunFilter{Type: "all", Cause: "exit 1"}, "r_cause = ?", "exit 1"},
		{RunFilter{Type: "all", ExecutionID: "abc"}, "run_id = ?", "abc"},
	}
	for _, c := range cases {
		w, a := c.f.runWhere()
		if !strings.Contains(w, c.want) {
			t.Errorf("%+v: %s lacks %s", c.f, w, c.want)
		}
		if c.arg != nil && (len(a) == 0 || a[len(a)-1] != c.arg) {
			t.Errorf("%+v: args %v", c.f, a)
		}
	}
	if w, _ := (RunFilter{Type: "all"}).runWhere(); w != "1=1" {
		t.Errorf("all types, no filters: %s", w)
	}
}

// Tools and addons are counted under their own dims even when rates exclude them.
func TestBreakdownScopesInstallDimsOnly(t *testing.T) {
	q, _ := breakdownSQL(RunFilter{Days: 1}, []breakdownDim{dimAll, dimTool})
	outer := q[strings.LastIndex(q, "WHERE "):]
	if strings.Contains(outer, installScopeSQL) {
		t.Errorf("scope must not filter the joined rows: %s", outer)
	}
	if !strings.Contains(q, "tuple('all', toString(''), toUInt8(("+installScopeSQL+") AND 1))") {
		t.Errorf("all dim is not scoped:\n%s", q)
	}
	if !strings.Contains(q, "tuple('tool', toString(r_app), toUInt8((r_type IN ('pve','tool') AND r_app != '')))") {
		t.Errorf("tool dim is scoped:\n%s", q)
	}
	q, _ = breakdownSQL(RunFilter{Days: 1, Type: "addon"}, []breakdownDim{dimAll})
	if strings.Contains(q, installScopeSQL) || !strings.Contains(q, "r_type = ?") {
		t.Errorf("a named type replaces the scope:\n%s", q)
	}
}

func TestAbortSQLMatchesTheGoCheck(t *testing.T) {
	sql := abortRowSQL()
	for _, w := range abortWords {
		if !strings.Contains(sql, "'"+w+"'") {
			t.Errorf("abort word %q missing from SQL", w)
		}
		if !isAbortSignal(2, 1, "x "+strings.ToUpper(w)+" y") {
			t.Errorf("isAbortSignal misses %q", w)
		}
	}
	if isAbortSignal(3, 1, "press Ctrl+C to stop") {
		t.Error("a v3 trace quoting Ctrl+C is not an abort")
	}
}

func TestCacheKeysSeparateFiltersAndMatchWarmup(t *testing.T) {
	q, _ := url.ParseQuery("days=1&repo=all&_t=123")
	f, err := parseRunFilter(q, 1, 365, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.cacheKey("dashboard") != (RunFilter{Days: 1}).cacheKey("dashboard") {
		t.Errorf("handler key %q differs from the warmup key", f.cacheKey("dashboard"))
	}
	code := 1
	keys := map[string]bool{}
	for _, v := range []RunFilter{
		{Days: 1}, {Days: 7}, {Days: 1, RepoSource: "ProxmoxVE"}, {Days: 1, RepoSlug: "a/b"},
		{Days: 1, Platform: "pve"}, {Days: 1, Type: "all"}, {Days: 1, App: "x"}, {Days: 1, Search: "x"},
		{Days: 1, Status: "failed"}, {Days: 1, ExitCode: &code}, {Days: 1, Cause: "x"}, {Days: 1, Message: "x"},
	} {
		k := v.cacheKey("errors")
		if keys[k] {
			t.Errorf("filters collide on %q", k)
		}
		keys[k] = true
	}
}

func TestParseRunFilter(t *testing.T) {
	get := func(s string) (RunFilter, error) {
		q, _ := url.ParseQuery(s)
		return parseRunFilter(q, 7, 365, "")
	}
	f, err := get("days=0&repo=ProxmoxVE&platform=PVE&type=LXC&status=failed&exit_code=127&app=docker&search=dock")
	if err != nil {
		t.Fatal(err)
	}
	if f.Days != 365 || f.RepoSource != "ProxmoxVE" || f.Platform != "pve" || f.Type != "lxc" ||
		f.Status != "failed" || f.ExitCode == nil || *f.ExitCode != 127 || f.App != "docker" || f.Search != "dock" {
		t.Errorf("parsed %+v", f)
	}
	if f, _ := get(""); f.Days != 7 || f.Type != "" {
		t.Errorf("defaults %+v", f)
	}
	for _, bad := range []string{"type=vmware", "status=running", "exit_code=one", "platform=esxi"} {
		if _, err := get(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if f, _ := get("days=5000"); f.Days != 365 {
		t.Errorf("days capped to %d", f.Days)
	}
}

func TestBuildStagesTimesEachStep(t *testing.T) {
	base := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC).UnixMilli()
	step := func(s string, sec int64) pipelineStep { return pipelineStep{S: s, ms: base + sec*1000} }

	got := buildStages([]pipelineStep{step("installing", 0), step("validation", 30), step("configuring", 90), step("failed", 300)})
	want := []int{30, 60, 210, 0}
	for i, w := range want {
		if got[i].Seconds != w {
			t.Errorf("stage %s: %d s, want %d", got[i].Stage, got[i].Seconds, w)
		}
	}
	if got[0].At != "2026-10-27T10:00:00Z" {
		t.Errorf("at %s", got[0].At)
	}

	got = buildStages([]pipelineStep{step("installing", 0), step("configuring", 60), step("unknown", 4*3600)})
	if got[1].Seconds != -1 || got[2].Seconds != 0 {
		t.Errorf("the stage before no result has no known end: %+v", got)
	}
	got = buildStages([]pipelineStep{step("installing", 0), step("configuring", 60)})
	if got[1].Seconds != -1 {
		t.Errorf("a running stage is open: %+v", got)
	}
}

func TestRunDuration(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	if d := runDuration(outcomeFailed, 1000, 61_000, 0, 7, now); d != 60 {
		t.Errorf("failed %d", d)
	}
	if d := runDuration(outcomeSuccess, 5000, 5000, 0, 42, now); d != 42 {
		t.Errorf("single event falls back to the client's duration: %d", d)
	}
	if d := runDuration(outcomeUnknown, 1000, 0, 31_000, 0, now); d != 30 {
		t.Errorf("no result lasts until the last stage: %d", d)
	}
	if d := runDuration(outcomeInProgress, now.UnixMilli()-90_000, 0, 0, 0, now); d != 90 {
		t.Errorf("in progress %d", d)
	}
}

func TestRunRecordFromRow(t *testing.T) {
	now := time.Now()
	base := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC).UnixMilli()
	r := runRow{key: "e1", eid: "e1", app: "docker", outcome: outcomeFailed, exit: 1,
		errText: "exit_code=1 | General error | at line 3: apt-get install foo\n---\nE: Unable to locate package foo",
		cause:   "apt-get install foo", causeKind: "command", startedMs: base, termMs: base + 125_000}
	rec := r.record(now)
	if rec.ErrorMessage != "E: Unable to locate package foo" {
		t.Errorf("message %q", rec.ErrorMessage)
	}
	if rec.Duration != 125 || rec.FinishedAt != "2026-10-27T09:02:05Z" || rec.Created != "2026-10-27 09:00:00.000" {
		t.Errorf("times %+v", rec)
	}
	if rec.Cause != "apt-get install foo" || rec.CauseKind != "command" || rec.ExitDescription != "General error" {
		t.Errorf("cause %+v", rec)
	}

	u := runRow{key: "e2", outcome: outcomeUnknown, stage: "configuring", startedMs: base, stageMs: base + 60_000,
		errText: "Installation timed out - no completion status received after 4h"}.record(now)
	if u.Status != "unknown" || u.Reason != "no result" || u.LastStage != "configuring" || u.FinishedAt != "" || u.ErrorMessage != "" {
		t.Errorf("no result %+v", u)
	}

	s := runRow{outcome: outcomeSuccess, cat: "apt", cmd: "x", startedMs: base, termMs: base + 1000}.record(now)
	if s.ErrorCategory != "" || s.FailedCommand != "" || s.Cause != "" {
		t.Errorf("success keeps failure fields: %+v", s)
	}
}

func TestCauseTrend(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour).UnixMilli()
	for _, c := range []struct {
		first      int64
		last, prev int
		want       string
	}{
		{now.Add(-time.Hour).UnixMilli(), 3, 0, "new"},
		{old, 0, 0, "quiet"},
		{old, 0, 5, "stopped"},
		{old, 9, 3, "rising"},
		{old, 2, 9, "falling"},
		{old, 4, 5, "steady"},
		{old, 2, 1, "steady"},
	} {
		if got := causeTrend(c.first, c.last, c.prev, now); got != c.want {
			t.Errorf("%d vs %d: %s, want %s", c.last, c.prev, got, c.want)
		}
	}
}

func TestAppStatFinish(t *testing.T) {
	a := AppErrorStat{Success: 3, Failed: 1, TopExitCode: 100, lastFailMs: 2000, lastOkMs: 1000}
	a.finish("message:E: Unable to locate package foo")
	if a.TopCauseKind != "message" || a.TopCause != "E: Unable to locate package foo" || !a.FailingNow || a.FailureRate != 25 {
		t.Errorf("%+v", a)
	}
	b := AppErrorStat{Success: 3, Failed: 1, TopExitCode: 1, lastFailMs: 1000, lastOkMs: 2000}
	b.finish("exit_code:exit 1")
	if b.FailingNow || b.TopCauseText != "Exit 1: General error" {
		t.Errorf("%+v", b)
	}
}

func TestRankFailureRates(t *testing.T) {
	apps := []AppErrorStat{
		{App: "few", Success: 0, Failed: 2, FailureRate: 100},
		{App: "half", Success: 5, Failed: 5, FailureRate: 50},
		{App: "most", Success: 2, Failed: 8, FailureRate: 80},
		{App: "none", Success: 20, Failed: 0},
	}
	got := rankFailureRates(apps, 5, 10)
	if len(got) != 2 || got[0].App != "most" || got[1].App != "half" {
		t.Errorf("%+v", got)
	}
}

func TestBreakdownSortsAndRates(t *testing.T) {
	b := newBreakdown([]dimRow{
		{"app", BreakdownRow{Label: "b", Runs: 5, Success: 4, Failed: 1}},
		{"app", BreakdownRow{Label: "a", Runs: 9, Success: 6, Failed: 3}},
		{"all", BreakdownRow{Runs: 14, Success: 10, Failed: 4, durSum: 300, durN: 10}},
	})
	if b["app"][0].Label != "a" || int(b["app"][0].FailureRate) != 33 {
		t.Errorf("%+v", b["app"])
	}
	s := summaryFrom(b.one("all"))
	if s.Runs != 14 || s.FailureRate < 28.57 || s.FailureRate > 28.58 || s.SuccessRate != 100-s.FailureRate || s.AvgDuration != 30 {
		t.Errorf("%+v", s)
	}
	if len(b.top("app", 1)) != 1 || b.one("missing").Runs != 0 {
		t.Error("top/one")
	}
}

func TestExampleMessageOnlyForUnnamedCauses(t *testing.T) {
	causes := []Cause{
		{Key: "E: boom", Kind: "message", Examples: []string{"a"}},
		{Key: "npm ci", Kind: "command", Examples: []string{"b"}},
	}
	recs := []RunRecord{
		{TelemetryRecord: TelemetryRecord{TelemetryOut: TelemetryOut{ExecutionID: "a", ErrorMessage: "E: boom"}}},
		{TelemetryRecord: TelemetryRecord{TelemetryOut: TelemetryOut{ExecutionID: "b", ErrorMessage: "npm ERR! code ENOSPC"}}},
	}
	attachExamples(causes, recs, true)
	if causes[0].ExampleMessage != "" || causes[1].ExampleMessage != "npm ERR! code ENOSPC" {
		t.Errorf("%+v", causes)
	}
	if len(causes[1].ExampleRuns) != 1 {
		t.Error("full export lost its example run")
	}
}

func TestDailyStatsAreDense(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	got := buildDailyStats(map[string]BreakdownRow{"2026-10-26": {Runs: 3, Failed: 1}}, 3, now)
	if len(got) != 3 || got[0].Date != "2026-10-25" || got[1].Runs != 3 || got[2].Date != "2026-10-27" {
		t.Errorf("%+v", got)
	}
}

func TestStuckRunsAreRunsWithoutAnyResult(t *testing.T) {
	q := stuckRunsSQL
	if !strings.Contains(q, "GROUP BY "+runKeyExpr) || !strings.Contains(q, "argMin(id, created)") {
		t.Errorf("one row per run, the first one:\n%s", q)
	}
	if !strings.Contains(q, "countIf(status IN ('success','failed','aborted','unknown')) = 0") || placeholderCount(q) != 1 {
		t.Errorf("a run with any result is not stuck:\n%s", q)
	}
}

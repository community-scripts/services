package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A run is every row sharing an execution_id, or a random_id when there is none.

const runKeyExpr = "if(execution_id = '', random_id, execution_id)"

const (
	rowTerminal = "status IN ('success','failed','aborted')"
	rowFailure  = "status IN ('failed','aborted')"
	rowProgress = "status IN ('installing','validation','configuring')"
)

// abortWords mark a pre-v3 "failed" trace as a user abort.
var abortWords = []string{"sigint", "ctrl+c", "ctrl-c", "sighup",
	"aborted by user", "user abort", "cancelled by user", "no changes have been made"}

func abortRowSQL() string {
	words := make([]string, len(abortWords))
	for i, w := range abortWords {
		words[i] = "positionCaseInsensitive(error, '" + w + "') > 0"
	}
	return "(status = 'aborted' OR (status = 'failed' AND (error_category = 'user_aborted'" +
		" OR exit_code IN (129,130) OR (payload_version < 3 AND (" + strings.Join(words, " OR ") + ")))))"
}

const rowCommandSQL = `if(failed_command != '', failed_command, extract(error, 'at line [0-9]+: ([^\n|]{1,200})'))`

// Aliases must never equal a column name, or ClickHouse nests the aggregates.
func runColumns(full bool) []string {
	cols := []string{
		runKeyExpr + " AS run_id",
		"any(execution_id) AS r_eid",
		"any(random_id) AS r_rid",
		"anyIf(nsapp, nsapp != '') AS r_app",
		"anyIf(type, type != '') AS r_type",
		"anyIf(method, method != '') AS r_method",
		"anyIf(os_type, os_type != '') AS r_os",
		"anyIf(os_version, os_version != '') AS r_osver",
		"anyIf(pve_version, pve_version != '') AS r_pve",
		"toString(anyIf(" + platformExpr + ", " + platformExpr + " != '')) AS r_platform",
		"anyIf(repo_source, repo_source != '') AS r_source",
		"anyIf(repo_slug, repo_slug != '') AS r_slug",
		"max(core_count) AS r_cores",
		"max(ram_size) AS r_ram",
		"max(disk_size) AS r_disk",
		"max(ct_type) AS r_ct",
		"anyIf(cpu_vendor, cpu_vendor != '') AS r_cpu_vendor",
		"anyIf(cpu_model, cpu_model != '') AS r_cpu_model",
		"anyIf(gpu_vendor, gpu_vendor != '') AS r_gpu_vendor",
		"anyIf(gpu_model, gpu_model != '') AS r_gpu_model",
		"anyIf(gpu_passthrough, gpu_passthrough != '') AS r_gpu_pass",
		"anyIf(ram_speed, ram_speed != '') AS r_ram_speed",
		"toString(anyIf(arch, arch != '')) AS r_arch",
		"anyIf(kernel_version, kernel_version != '') AS r_kernel",
		"anyIf(app_version, app_version != '') AS r_appver",
		"toString(anyIf(migrated_from, migrated_from != '')) AS r_migrated",
		"max(has_arm) AS r_arm",
		"max(install_duration) AS r_dur",
		"max(payload_version) AS r_pver",
		"countIf(" + rowTerminal + ") AS r_real_n",
		"countIf(status = 'unknown') AS r_unknown_n",
		"argMaxIf(status, created, " + rowTerminal + ") AS r_term",
		"argMaxIf(toUInt8(" + abortRowSQL() + "), created, " + rowTerminal + ") AS r_abort",
		"argMaxIf(exit_code, created, " + rowTerminal + ") AS r_exit",
		"argMaxIf(error_category, created, " + rowFailure + ") AS r_cat",
		"argMaxIf(error_message, created, " + rowFailure + ") AS r_msg",
		"argMaxIf(error_step, created, " + rowFailure + ") AS r_step",
		"toString(argMaxIf(error_phase, created, " + rowFailure + ")) AS r_phase",
		"argMaxIf(failed_function, created, " + rowFailure + ") AS r_func",
		"argMaxIf(" + rowCommandSQL + ", created, " + rowFailure + ") AS r_cmd",
		"argMaxIf(failed_line, created, " + rowFailure + ") AS r_line",
		"min(created) AS r_started",
		"max(created) AS r_last_seen",
		"maxIf(created, " + rowTerminal + ") AS r_term_at",
		"minIf(created, status = 'unknown') AS r_unknown_at",
		"argMaxIf(status, created, " + rowProgress + ") AS r_stage",
		"maxIf(created, " + rowProgress + ") AS r_stage_at",
		"multiIf(r_real_n > 0 AND r_abort = 1, 'aborted', r_real_n > 0, r_term, " +
			"r_unknown_n > 0, 'unknown', 'in_progress') AS r_outcome",
		"if(r_real_n > 0, r_term_at, r_last_seen) AS r_end",
		"r_cmd != '' AND r_cmd != 'unknown' AND r_cmd != 'return' AND NOT startsWith(r_cmd, 'return ') AS r_cmd_ok",
		"multiIf(r_msg != '', 'message', r_cmd_ok, 'command', 'exit_code') AS r_cause_kind",
		"multiIf(r_msg != '', r_msg, r_cmd_ok, r_cmd, concat('exit ', toString(r_exit))) AS r_cause",
		"cityHash64(r_cpu_model, r_gpu_model, r_ram_speed, r_pve, r_kernel) AS r_setup",
	}
	if full {
		cols = append(cols, "argMaxIf(error, created, status IN ('failed','aborted','unknown')) AS r_error")
	}
	return cols
}

func runsSubquery(rowWhere string, full bool) string {
	return "SELECT " + strings.Join(runColumns(full), ",\n\t") +
		"\nFROM telemetry_db.telemetry\nWHERE " + rowWhere + "\nGROUP BY run_id"
}

// Run outcomes as the API reports them.
const (
	outcomeSuccess    = "success"
	outcomeFailed     = "failed"
	outcomeAborted    = "aborted"
	outcomeUnknown    = "unknown"
	outcomeInProgress = "in_progress"
)

var allowedRunStatus = map[string]bool{
	outcomeSuccess: true, outcomeFailed: true, outcomeAborted: true,
	outcomeUnknown: true, outcomeInProgress: true,
	"installing": true, "validation": true, "configuring": true,
}

var allowedRunType = map[string]bool{
	"": true, "all": true, "lxc": true, "vm": true, "turnkey": true,
	"pve": true, "addon": true, "tool": true,
}

// RunFilter selects runs. Type "" means install types only, "all" every type.
type RunFilter struct {
	Days        int
	RepoSource  string
	RepoSlug    string
	Platform    string
	Type        string
	App         string
	Search      string
	OS          string
	Status      string
	ExitCode    *int
	Category    string
	Message     string
	Cause       string
	ExecutionID string
	RandomID    string

	// Fixed windows that ignore Days, for "right now" figures.
	rowsFrom    time.Time
	startedFrom time.Time
	endFrom     time.Time
}

func (f RunFilter) since() time.Time { return chSinceTime(f.Days) }

// rowWhere reaches a day back so the run filter sees each run's real start.
func (f RunFilter) rowWhere() (string, []interface{}) {
	parts := []string{"1=1"}
	var args []interface{}
	switch {
	case !f.rowsFrom.IsZero():
		parts = append(parts, "created >= ?")
		args = append(args, f.rowsFrom)
	case f.Days > 0:
		parts = append(parts, "created >= ?")
		args = append(args, f.since().Add(-24*time.Hour))
	}
	if f.App != "" {
		parts = append(parts, "nsapp = ?")
		args = append(args, f.App)
	}
	return strings.Join(parts, " AND "), args
}

func (f RunFilter) runWhere() (string, []interface{}) {
	parts := []string{"1=1"}
	var args []interface{}
	add := func(p string, a ...interface{}) {
		parts = append(parts, p)
		args = append(args, a...)
	}
	if f.Days > 0 {
		add("r_started >= ?", f.since())
	}
	if !f.startedFrom.IsZero() {
		add("r_started >= ?", f.startedFrom)
	}
	if !f.endFrom.IsZero() {
		add("r_end >= ?", f.endFrom)
	}
	if pred, pa := repoSourcePredCol("r_source", f.RepoSource); pred != "" {
		add(pred, pa...)
	}
	if f.RepoSlug != "" {
		add("r_slug = ?", f.RepoSlug)
	}
	if f.Platform == "pve" || f.Platform == "incus" {
		add("r_platform = '" + f.Platform + "'")
	}
	switch f.Type {
	case "":
		add("r_type NOT IN ('pve','addon','tool')")
	case "all":
	default:
		add("r_type = ?", f.Type)
	}
	if f.Search != "" {
		add("positionCaseInsensitive(r_app, ?) > 0", f.Search)
	}
	if f.OS != "" {
		add("r_os = ?", f.OS)
	}
	switch f.Status {
	case "":
	case "installing", "validation", "configuring":
		add("r_outcome = 'in_progress' AND r_stage = ?", f.Status)
	default:
		add("r_outcome = ?", f.Status)
	}
	if f.ExitCode != nil {
		add("r_exit = ?", *f.ExitCode)
	}
	if f.Category != "" {
		add("r_cat = ?", f.Category)
	}
	if f.Message != "" {
		add("r_msg = ?", f.Message)
	}
	if f.Cause != "" {
		add("r_cause = ?", f.Cause)
	}
	if f.ExecutionID != "" {
		add("run_id = ?", f.ExecutionID)
	}
	if f.RandomID != "" {
		add("r_rid = ?", f.RandomID)
	}
	return strings.Join(parts, " AND "), args
}

type runQuery struct {
	sel      string
	selArgs  []interface{}
	join     string
	where    string
	tail     string
	tailArgs []interface{}
	full     bool
}

// sql returns the query and its arguments in placeholder order.
func (f RunFilter) sql(q runQuery) (string, []interface{}) {
	rw, ra := f.rowWhere()
	ow, oa := f.runWhere()
	if q.where != "" {
		ow += " AND " + q.where
	}
	text := "SELECT " + q.sel + "\nFROM (" + runsSubquery(rw, q.full) + ")"
	if q.join != "" {
		text += "\n" + q.join
	}
	text += "\nWHERE " + ow + q.tail
	args := make([]interface{}, 0, len(q.selArgs)+len(ra)+len(oa)+len(q.tailArgs))
	args = append(append(append(append(args, q.selArgs...), ra...), oa...), q.tailArgs...)
	return text, args
}

func (f RunFilter) cacheKey(prefix string) string {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("days", strconv.Itoa(f.Days))
	set("repo", f.RepoSource)
	set("slug", f.RepoSlug)
	set("platform", f.Platform)
	set("type", f.Type)
	set("app", f.App)
	set("search", f.Search)
	set("os", f.OS)
	set("status", f.Status)
	if f.ExitCode != nil {
		set("exit_code", strconv.Itoa(*f.ExitCode))
	}
	set("category", f.Category)
	set("message", f.Message)
	set("cause", f.Cause)
	set("execution_id", f.ExecutionID)
	set("random_id", f.RandomID)
	return prefix + ":" + v.Encode()
}

// Seconds is the time to the next event: 0 for a result, -1 when none came.
type RunStage struct {
	Stage   string `json:"stage"`
	At      string `json:"at"`
	Seconds int    `json:"seconds"`
}

// RunRecord is one run with metadata merged across all of its events.
type RunRecord struct {
	TelemetryRecord
	StartedAt       string     `json:"started_at"`
	FinishedAt      string     `json:"finished_at,omitempty"`
	LastSeenAt      string     `json:"last_seen_at"`
	Duration        int        `json:"duration"`
	LastStage       string     `json:"last_stage,omitempty"`
	LastStageAt     string     `json:"last_stage_at,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Cause           string     `json:"cause,omitempty"`
	CauseKind       string     `json:"cause_kind,omitempty"`
	ExitDescription string     `json:"exit_description,omitempty"`
	Stages          []RunStage `json:"stages,omitempty"`
}

const runDetailCols = `run_id, r_eid, r_rid, r_app, r_type, r_method, r_os, r_osver, r_pve, r_platform,
	r_source, r_slug, toInt64(r_cores), toInt64(r_ram), toInt64(r_disk), toInt64(r_ct),
	r_cpu_vendor, r_cpu_model, r_gpu_vendor, r_gpu_model, r_gpu_pass, r_ram_speed, r_arch,
	r_kernel, r_appver, r_migrated, toInt64(r_arm), toInt64(r_dur), toInt64(r_pver),
	r_outcome, r_stage, toInt64(r_exit), r_cat, r_msg, r_step, r_phase, r_func, r_cmd, toInt64(r_line),
	r_error, r_cause, r_cause_kind,
	toInt64(toUnixTimestamp64Milli(r_started)), toInt64(toUnixTimestamp64Milli(r_last_seen)),
	if(r_real_n > 0, toInt64(toUnixTimestamp64Milli(r_term_at)), toInt64(0)),
	if(r_stage != '', toInt64(toUnixTimestamp64Milli(r_stage_at)), toInt64(0))`

// runRow is the scan target for runDetailCols.
type runRow struct {
	key, eid, rid, app, typ, method, os, osVer, pve, platform, source, slug string
	cores, ram, disk, ct                                                    int64
	cpuVendor, cpuModel, gpuVendor, gpuModel, gpuPass, ramSpeed, arch       string
	kernel, appVer, migrated                                                string
	arm, dur, pver                                                          int64
	outcome, stage                                                          string
	exit                                                                    int64
	cat, msg, step, phase, fn, cmd                                          string
	line                                                                    int64
	errText, cause, causeKind                                               string
	startedMs, lastSeenMs, termMs, stageMs                                  int64
}

func (r *runRow) dest() []interface{} {
	return []interface{}{&r.key, &r.eid, &r.rid, &r.app, &r.typ, &r.method, &r.os, &r.osVer, &r.pve, &r.platform,
		&r.source, &r.slug, &r.cores, &r.ram, &r.disk, &r.ct,
		&r.cpuVendor, &r.cpuModel, &r.gpuVendor, &r.gpuModel, &r.gpuPass, &r.ramSpeed, &r.arch,
		&r.kernel, &r.appVer, &r.migrated, &r.arm, &r.dur, &r.pver,
		&r.outcome, &r.stage, &r.exit, &r.cat, &r.msg, &r.step, &r.phase, &r.fn, &r.cmd, &r.line,
		&r.errText, &r.cause, &r.causeKind,
		&r.startedMs, &r.lastSeenMs, &r.termMs, &r.stageMs}
}

func msTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func msLegacy(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05.000")
}

// runDuration ends at the result, the last stage, or now for a running run.
func runDuration(outcome string, startedMs, termMs, stageMs int64, installDur int, now time.Time) int {
	switch outcome {
	case outcomeSuccess, outcomeFailed, outcomeAborted:
		if termMs > startedMs {
			return int((termMs - startedMs) / 1000)
		}
		return installDur
	case outcomeUnknown:
		if stageMs > startedMs {
			return int((stageMs - startedMs) / 1000)
		}
		return 0
	case outcomeInProgress:
		if d := now.UnixMilli() - startedMs; d > 0 {
			return int(d / 1000)
		}
	}
	return 0
}

func (r runRow) record(now time.Time) RunRecord {
	out := RunRecord{
		TelemetryRecord: TelemetryRecord{
			TelemetryOut: TelemetryOut{
				RandomID: r.rid, ExecutionID: r.eid, Type: r.typ, NSAPP: r.app, Status: r.outcome,
				CTType: int(r.ct), DiskSize: int(r.disk), CoreCount: int(r.cores), RAMSize: int(r.ram),
				OsType: r.os, OsVersion: r.osVer, PveVer: r.pve, Method: r.method,
				Error: r.errText, ExitCode: int(r.exit),
				GPUVendor: r.gpuVendor, GPUModel: r.gpuModel, GPUPassthrough: r.gpuPass,
				CPUVendor: r.cpuVendor, CPUModel: r.cpuModel, RAMSpeed: r.ramSpeed,
				InstallDuration: int(r.dur), ErrorCategory: r.cat,
				RepoSource: r.source, RepoSlug: r.slug, HasArm: r.arm != 0, Platform: r.platform,
				PayloadVersion: int(r.pver), Arch: r.arch, FailedCommand: r.cmd, FailedLine: int(r.line),
				KernelVersion: r.kernel, AppVersion: r.appVer,
				ErrorMessage: r.msg, ErrorStep: r.step, ErrorPhase: r.phase, FailedFunction: r.fn,
				MigratedFrom: r.migrated,
			},
			Created: msLegacy(r.startedMs),
		},
		StartedAt:   msTime(r.startedMs),
		LastSeenAt:  msTime(r.lastSeenMs),
		LastStage:   r.stage,
		LastStageAt: msTime(r.stageMs),
		Duration:    runDuration(r.outcome, r.startedMs, r.termMs, r.stageMs, int(r.dur), now),
	}
	switch r.outcome {
	case outcomeSuccess:
		out.FinishedAt = msTime(r.termMs)
		out.Error, out.ErrorCategory, out.FailedCommand, out.FailedLine = "", "", "", 0
	case outcomeFailed, outcomeAborted:
		out.FinishedAt = msTime(r.termMs)
		out.ExitDescription = getExitCodeDescription(int(r.exit))
		if out.ErrorMessage == "" {
			out.ErrorMessage = deriveErrorMessage(r.errText)
		}
		if r.outcome == outcomeFailed {
			out.Cause, out.CauseKind = r.cause, r.causeKind
		}
	case outcomeUnknown:
		out.Reason = "no result"
		out.ExitCode = 0
	case outcomeInProgress:
		out.Error, out.ExitCode = "", 0
	}
	return out
}

// buildStages turns a run's ordered events into timed stages.
func buildStages(steps []pipelineStep) []RunStage {
	out := make([]RunStage, 0, len(steps))
	for i, s := range steps {
		st := RunStage{Stage: s.S, At: msTime(s.ms), Seconds: -1}
		switch {
		case isTerminalStatus(s.S):
			st.Seconds = 0
		case i+1 < len(steps) && steps[i+1].S != outcomeUnknown:
			st.Seconds = int((steps[i+1].ms - s.ms) / 1000)
		}
		out = append(out, st)
	}
	return out
}

var runSortSQL = map[string]string{
	"created": "r_started", "-created": "r_started DESC",
	"started": "r_started", "-started": "r_started DESC",
	"nsapp": "r_app", "-nsapp": "r_app DESC",
	"status": "r_outcome", "-status": "r_outcome DESC",
	"os_type": "r_os", "-os_type": "r_os DESC",
	"type": "r_type", "-type": "r_type DESC",
	"method": "r_method", "-method": "r_method DESC",
	"exit_code": "r_exit", "-exit_code": "r_exit DESC",
	"duration": "dateDiff('second', r_started, r_end)", "-duration": "dateDiff('second', r_started, r_end) DESC",
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringArgList(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// FetchRuns returns one page of runs and the number of runs matching f.
func (ch *CHClient) FetchRuns(ctx context.Context, f RunFilter, page, limit int, sort string) ([]RunRecord, int, error) {
	order, ok := runSortSQL[sort]
	if !ok {
		order = "r_started DESC"
	}
	tail := fmt.Sprintf("\nORDER BY %s, run_id\nLIMIT %d OFFSET %d", order, limit, (page-1)*limit)
	q, args := f.sql(runQuery{sel: "run_id, toInt64(toUnixTimestamp64Milli(r_started)), toInt64(count() OVER ())", tail: tail})
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("CH runs: %w", err)
	}
	var ids []string
	var floor, total int64
	for rows.Next() {
		var id string
		var started, n int64
		if err := rows.Scan(&id, &started, &n); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("CH runs scan: %w", err)
		}
		ids = append(ids, id)
		total = n
		if floor == 0 || started < floor {
			floor = started
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("CH runs: %w", err)
	}
	if len(ids) == 0 {
		if page > 1 {
			cq, cargs := f.sql(runQuery{sel: "toInt64(count())"})
			if err := ch.db.QueryRowContext(ctx, cq, cargs...).Scan(&total); err != nil {
				return nil, 0, fmt.Errorf("CH runs count: %w", err)
			}
		}
		return []RunRecord{}, int(total), nil
	}
	recs, err := ch.fetchRunDetails(ctx, ids, time.UnixMilli(floor), true)
	if err != nil {
		return nil, 0, err
	}
	return recs, int(total), nil
}

// fetchRunDetails keeps the order of ids; floor is their earliest start.
func (ch *CHClient) fetchRunDetails(ctx context.Context, ids []string, floor time.Time, stages bool) ([]RunRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rowWhere := runKeyExpr + " IN (" + placeholders(len(ids)) + ")"
	args := stringArgList(ids)
	if floor.Unix() > 0 {
		floor = floor.UTC().Add(-time.Second)
		rowWhere = "created >= ? AND " + rowWhere
		args = append([]interface{}{floor}, args...)
	} else {
		floor = time.Time{}
	}
	q := "SELECT " + runDetailCols + "\nFROM (" + runsSubquery(rowWhere, true) + ")"
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CH run details: %w", err)
	}
	defer rows.Close()
	now := time.Now()
	byKey := make(map[string]RunRecord, len(ids))
	for rows.Next() {
		var r runRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, fmt.Errorf("CH run details scan: %w", err)
		}
		byKey[r.key] = r.record(now)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("CH run details: %w", err)
	}
	steps := map[string][]pipelineStep{}
	if stages {
		if steps, err = ch.fetchPipelines(ctx, ids, floor); err != nil {
			return nil, err
		}
	}
	out := make([]RunRecord, 0, len(ids))
	for _, id := range ids {
		rec, ok := byKey[id]
		if !ok {
			continue
		}
		if s := steps[id]; len(s) > 0 {
			rec.Stages = buildStages(s)
			rec.Pipeline = pipelineJSON(s)
		}
		out = append(out, rec)
	}
	return out, nil
}

package main

// A single aggregate for the consolidated dashboard at /new.
//
// Everything starts by collapsing rows to runs. An install writes several rows --
// installing, configuring, then a terminal status -- so counting rows answers
// "how many pings" while counting runs answers "how many installs". The existing
// pages mix the two, which is why the same figure differs between them. Here
// every number shares one unit, and the page says which.
//
// The vocabulary is the engine's own: error categories come from
// categorize_error in core/api/exitcodes.func, exit codes from the table beside
// it. Grouping by anything else would describe the dashboard rather than the
// thing being measured.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// runsCTE collapses the filtered rows to one row per run.
//
// argMax(status, created) is the terminal status: whatever the run last
// reported. A run that stopped at "installing" never reached one, which makes it
// unfinished rather than failed -- a distinction the old pages do not draw.
const runsCTE = `
	SELECT
		if(execution_id = '', random_id, execution_id) AS run,
		argMax(status, created)                        AS final_status,
		argMax(exit_code, created)                     AS final_exit,
		argMax(error_category, created)                AS final_cat,
		argMax(error, created)                         AS final_err,
		max(created)                                   AS last_seen,
		any(nsapp)                                     AS app,
		any(type)                                      AS kind,
		any(method)                                    AS meth,
		any(os_type)                                   AS ostype,
		any(os_version)                                AS osver,
		any(pve_version)                               AS hostver,
		any(%s)                                        AS plat,
		any(repo_source)                               AS src,
		any(repo_slug)                                 AS slug,
		any(ct_type)                                   AS priv,
		any(core_count)                                AS cores,
		any(ram_size)                                  AS ram,
		any(disk_size)                                 AS disk,
		any(cpu_vendor)                                AS cpu,
		any(gpu_vendor)                                AS gpu,
		any(gpu_passthrough)                           AS gpupass,
		any(has_arm)                                   AS arm,
		any(arch)                                      AS cpuarch,
		any(payload_version)                           AS pver,
		any(failed_command)                            AS failcmd,
		any(failed_line)                               AS failline,
		any(kernel_version)                            AS kernelver,
		any(app_version)                               AS appver,
		any(cpu_model)                                 AS cpumodel,
		any(gpu_model)                                 AS gpumodel,
		any(ram_speed)                                 AS ramspeed,
		max(install_duration)                          AS duration
	FROM telemetry_db.telemetry
	WHERE %s
	GROUP BY run`

// ========================================================
// Outcome: what actually happened to a run
// ========================================================
//
// Derived from the exit code at read time rather than stored as a status.
//
// The engine already writes the code on every terminal row, so deriving here
// needs no client rollout across thousands of hosts, and -- the reason it is
// worth doing this way -- it reclassifies the rows already in the table. A new
// status would only describe runs recorded after the day it shipped, and would
// put a seam through every comparison spanning that day.

// userAbortCodes are the codes the engine raises when the person at the
// keyboard said no. The signals are obvious; the rest are the engine asking a
// question and being told no.
//
// Listed here as well as in the engine because the engine only decides this
// going forward. Rows already written filed 113 as "failed", and that is what
// the table holds.
var userAbortCodes = []int{113, 114, 122, 129, 130, 143, 253, 254}

// blockedExitCodes are the codes raised before the application's install script
// ever ran: the host would not, or could not, provide a working container.
//
// They are not script failures and counting them as such measured the storage
// and network configuration of other people's servers. "Not enough storage
// space" and "APT broken packages" sat in the same column as though they were
// the same kind of event.
var blockedExitCodes = []int{
	// Validation: the environment was rejected before anything was created.
	103, 104, 105, 106, 107, 108,
	// Proxmox setup: no ID, no defaults, no menu selection to act on.
	109, 110, 111,
	// Bring-up: bridge, running state, IP, storage for the container.
	116, 117, 118, 119, 120, 121, 123,
	// The Proxmox block: locks, creation, storage, templates, the LXC stack.
	200, 203, 204, 205, 206, 207, 208, 209, 210, 211, 212, 213, 214, 215,
	216, 217, 218, 219, 220, 221, 222, 223, 224, 225, 226, 227, 231,
}

// sqlIntList renders codes for an IN clause. The values are ints from the two
// lists above, so there is nothing here for a caller to inject.
func sqlIntList(codes []int) string {
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ",")
}

// outcomeExpr classifies one run. The order of the branches is the meaning:
//
//   - success is success.
//   - A status that is not terminal means the run is still going. unknown IS
//     terminal -- the ingest layer says so in isTerminalStatus -- and treating
//     it as unfinished left finished runs sitting in the live panel forever.
//   - An aborted status is taken at its word.
//   - Then the codes, which is what reclassifies the rows written before the
//     engine learned to report a user abort as one.
func outcomeExpr() string {
	return fmt.Sprintf(`multiIf(
		final_status = 'success', 'success',
		final_status NOT IN ('success','failed','aborted','unknown'), 'unfinished',
		final_status = 'aborted', 'aborted',
		final_exit IN (%s), 'aborted',
		final_exit IN (%s), 'blocked',
		'failed')`,
		sqlIntList(userAbortCodes), sqlIntList(blockedExitCodes))
}

// NewCount is a plain label/count, for breakdowns where a rate makes no sense.
type NewCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// NewGroupStat is the shape used everywhere a rate is shown. It always carries
// its own denominators, so the page never has to display a percentage whose
// basis the reader cannot see.
type NewGroupStat struct {
	Label      string `json:"label"`
	Runs       int    `json:"runs"`
	Success    int    `json:"success"`
	Failed     int    `json:"failed"`
	Blocked    int    `json:"blocked"`
	Aborted    int    `json:"aborted"`
	Unfinished int    `json:"unfinished"`
}

// NewExitCode adds the engine's own description to a raw code.
type NewExitCode struct {
	Code  int    `json:"code"`
	Desc  string `json:"desc"`
	Count int    `json:"count"`
	Apps  int    `json:"apps"`
}

// NewErrorSignature groups failures by the exact message the engine captured.
type NewErrorSignature struct {
	Category string `json:"category"`
	ExitCode int    `json:"exit_code"`
	Message  string `json:"message"`
	Count    int    `json:"count"`
	Apps     int    `json:"apps"`
}

// NewRecentRun is one line of the install log.
type NewRecentRun struct {
	Run        string `json:"run"`
	App        string `json:"app"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Category   string `json:"category"`
	Platform   string `json:"platform"`
	OS         string `json:"os"`
	HostVer    string `json:"host_version"`
	Repo       string `json:"repo"`
	Method     string `json:"method"`
	Cores      int    `json:"cores"`
	RAM        int    `json:"ram"`
	Disk       int    `json:"disk"`
	Privileged bool   `json:"privileged"`
	Duration   int    `json:"duration"`
	LastSeen   string `json:"last_seen"`
	Error      string `json:"error"`

	// Detail-view fields, carried on every row rather than fetched per click.
	// The whole page is one request by design, and a dialog that needs a round
	// trip is a second page wearing a modal.
	FailedCommand  string `json:"failed_command"`
	FailedLine     int    `json:"failed_line"`
	Arch           string `json:"arch"`
	KernelVersion  string `json:"kernel_version"`
	AppVersion     string `json:"app_version"`
	CPUVendor      string `json:"cpu_vendor"`
	CPUModel       string `json:"cpu_model"`
	GPUVendor      string `json:"gpu_vendor"`
	GPUModel       string `json:"gpu_model"`
	GPUPassthrough string `json:"gpu_passthrough"`
	RAMSpeed       string `json:"ram_speed"`
	PayloadVersion int    `json:"payload_version"`
}

// NewDailyPoint is one day of the trend, on the same run basis as the rest.
type NewDailyPoint struct {
	Day        string `json:"day"`
	Runs       int    `json:"runs"`
	Success    int    `json:"success"`
	Failed     int    `json:"failed"`
	Blocked    int    `json:"blocked"`
	Aborted    int    `json:"aborted"`
	Unfinished int    `json:"unfinished"`
}

// NewDashData is the whole page in one response. One request, one render: the
// point of this page is that nothing needs a second one.
type NewDashData struct {
	Days     int    `json:"days"`
	Platform string `json:"platform"`
	Repo     string `json:"repo"`
	RepoSlug string `json:"repo_slug"`
	Type     string `json:"type"`

	// Headline, on runs.
	Runs       int `json:"runs"`
	Success    int `json:"success"`
	Failed     int `json:"failed"`
	Blocked    int `json:"blocked"`
	Aborted    int `json:"aborted"`
	Unfinished int `json:"unfinished"`

	// Runs ever recorded, ignoring every filter. Labelled as such on the page,
	// because a number that ignores the controls above it is otherwise a trap.
	AllTime int `json:"all_time"`

	// Median, not mean: one stuck run reporting the 86400-second clamp drags an
	// average far enough to be useless.
	MedianDuration int `json:"median_duration"`

	// MinRuns is the floor below which a rate is shown but not colour-graded.
	// Sent so the page can say what it is instead of hiding the rule.
	MinRuns int `json:"min_runs"`

	// Live: what is happening right now, independent of the selected window.
	LiveInFlight int            `json:"live_in_flight"`
	LiveLastHour int            `json:"live_last_hour"`
	LiveLast24h  int            `json:"live_last_24h"`
	LiveNow      []NewRecentRun `json:"live_now"`

	// Warnings names any section whose query failed, with the database's own
	// message. One broken breakdown used to take the entire page down with a
	// 500 and no clue which one it was; the rest of the page is still worth
	// showing, and the reason is worth reading.
	Warnings []string `json:"warnings"`

	Daily []NewDailyPoint `json:"daily"`

	// Everything that carries a rate uses the same shape.
	TopApps    []NewGroupStat `json:"top_apps"`
	WorstApps  []NewGroupStat `json:"worst_apps"`
	ByPlatform []NewGroupStat `json:"by_platform"`
	ByType     []NewGroupStat `json:"by_type"`
	ByRepo     []NewGroupStat `json:"by_repo"`
	ByOS       []NewGroupStat `json:"by_os"`
	ByHostVer  []NewGroupStat `json:"by_host_version"`
	ByRepoSlug []NewGroupStat `json:"by_repo_slug"`

	// Failure analysis.
	Categories []NewCount          `json:"categories"`
	ExitCodes  []NewExitCode       `json:"exit_codes"`
	Signatures []NewErrorSignature `json:"signatures"`

	// Advanced: what the container was actually given.
	ByPrivilege []NewCount `json:"by_privilege"`
	ByCores     []NewCount `json:"by_cores"`
	ByRAM       []NewCount `json:"by_ram"`
	ByDisk      []NewCount `json:"by_disk"`
	ByCPU       []NewCount `json:"by_cpu"`
	ByGPU       []NewCount `json:"by_gpu"`
	ByGPUPass   []NewCount `json:"by_gpu_passthrough"`
	ByArm       []NewCount `json:"by_arm"`
	ByMethod    []NewCount `json:"by_method"`
	ByClient    []NewCount `json:"by_client"`

	Recent []NewRecentRun `json:"recent"`
}

// minRunsFor scales the floor with the window. A percentage over three runs is
// noise wearing a number's clothes, and on a one-day view almost everything has
// only a handful.
func minRunsFor(days int) int {
	switch {
	case days <= 1:
		return 5
	case days <= 7:
		return 10
	case days <= 30:
		return 20
	default:
		return 40
	}
}

func scanCounts(rows *sql.Rows, err error) ([]NewCount, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NewCount{}
	for rows.Next() {
		var c NewCount
		if err := rows.Scan(&c.Label, &c.Count); err != nil {
			return nil, err
		}
		if c.Label == "" {
			c.Label = "unknown"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanGroupStats(rows *sql.Rows, err error) ([]NewGroupStat, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NewGroupStat{}
	for rows.Next() {
		var g NewGroupStat
		if err := rows.Scan(&g.Label, &g.Runs, &g.Success, &g.Failed,
			&g.Blocked, &g.Aborted, &g.Unfinished); err != nil {
			return nil, err
		}
		if g.Label == "" {
			g.Label = "unknown"
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// dashSection is one panel's worth of the page: a name to put on the warning
// line if it fails, and the query that fills it.
type dashSection struct {
	name string
	run  func(context.Context) error
}

// dashSectionLimit bounds how many sections are in flight at once. The pool
// holds twenty connections and this page is not the only thing using them, so
// the whole set is not let loose at once. Nearly all of the win is in not
// paying for thirty round trips end to end, and that is already there at eight.
const dashSectionLimit = 8

// FetchNewDashboard builds the whole consolidated page in one pass.
//
// The sections are independent -- each reads the same runs subquery and none
// reads another's output -- but they used to be issued one after another, so a
// load cost the sum of about thirty round trips when it only ever needed the
// slowest. They now run concurrently, bounded by dashSectionLimit.
//
// Two things follow from that and are handled below: warnings are appended
// under a lock, and sorted afterwards so the list does not reorder itself
// between two loads that failed in exactly the same way.
func (ch *CHClient) FetchNewDashboard(
	ctx context.Context, days int, repoSource, repoSlug, platform, ctype string,
) (*NewDashData, error) {
	extras := []string{}
	if ctype != "" {
		// Allowlisted by the handler before it reaches here.
		extras = append(extras, fmt.Sprintf("type = '%s'", ctype))
	}
	where, args := chWhere(days, repoSource, repoSlug, platform, extras...)

	// Every section reads outcome rather than final_status, so it is computed
	// once here and carried on the subquery they all select from.
	runs := fmt.Sprintf("SELECT *, %s AS outcome FROM (%s)",
		outcomeExpr(), fmt.Sprintf(runsCTE, platformExpr, where))

	d := &NewDashData{
		Days: days, Platform: platform, Repo: repoSource,
		RepoSlug: repoSlug, Type: ctype, MinRuns: minRunsFor(days),
	}

	// warn records a section that failed without taking the page with it. A
	// dashboard that shows nine of ten panels and names the tenth is more use
	// than a 500, and it says which query to look at.
	var warnMu sync.Mutex
	warn := func(section string, err error) {
		if err == nil {
			return
		}
		warnMu.Lock()
		d.Warnings = append(d.Warnings, fmt.Sprintf("%s: %v", section, err))
		warnMu.Unlock()
	}

	// Each section writes to a field of its own, so only Warnings needs the
	// lock above.
	var sections []dashSection
	add := func(name string, run func(context.Context) error) {
		sections = append(sections, dashSection{name: name, run: run})
	}

	// Headline.
	add("outcome", func(ctx context.Context) error {
		return ch.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT
			count(),
			countIf(outcome = 'success'),
			countIf(outcome = 'failed'),
			countIf(outcome = 'blocked'),
			countIf(outcome = 'aborted'),
			countIf(outcome = 'unfinished')
		FROM (%s)`, runs), args...,
		).Scan(&d.Runs, &d.Success, &d.Failed, &d.Blocked, &d.Aborted, &d.Unfinished)
	})

	// Live, deliberately outside the selected window: "what is happening now"
	// does not change because someone picked a different period. Defined apart
	// from the rest because that same property is what stops the page being
	// cached whole -- see RefreshLive.
	for _, s := range ch.liveSections(d) {
		add(s.name, s.run)
	}

	// All time, deliberately unfiltered. uniq rather than uniqExact: it is a
	// scan of the whole table and approximate is good enough for a figure that
	// is explicitly labelled as an all-time total.
	//
	// Reported like every other section now. Its error used to go on the floor,
	// which rendered a broken all-time query as a confident zero.
	add("all time", func(ctx context.Context) error {
		return ch.db.QueryRowContext(ctx,
			`SELECT uniq(if(execution_id = '', random_id, execution_id)) FROM telemetry_db.telemetry`,
		).Scan(&d.AllTime)
	})

	// Median over runs that actually finished. Reported for the same reason.
	add("median duration", func(ctx context.Context) error {
		return ch.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT toUInt32(median(duration)) FROM (%s)
		WHERE outcome = 'success' AND duration > 0`, runs), args...,
		).Scan(&d.MedianDuration)
	})

	// group runs a breakdown that carries its own denominators.
	//
	// The predicate goes in WHERE, not HAVING: these are per-run columns, not
	// aggregates, and ClickHouse rejects HAVING on an ungrouped column. That
	// mistake is why the first version of this page returned 500 for everything.
	group := func(ctx context.Context, target *[]NewGroupStat,
		expr, pred, order string, limit int) error {
		w := ""
		if pred != "" {
			w = " WHERE " + pred
		}
		q := fmt.Sprintf(`
			SELECT %s AS label, count() runs,
			       countIf(outcome = 'success')    success,
			       countIf(outcome = 'failed')     failed,
			       countIf(outcome = 'blocked')    blocked,
			       countIf(outcome = 'aborted')    aborted,
			       countIf(outcome = 'unfinished') unfinished
			FROM (%s)%s
			GROUP BY label
			ORDER BY %s
			LIMIT %d`, expr, runs, w, order, limit)
		v, e := scanGroupStats(ch.db.QueryContext(ctx, q, args...))
		if e != nil {
			return e
		}
		*target = v
		return nil
	}

	simple := func(ctx context.Context, target *[]NewCount,
		expr, pred string, limit int) error {
		w := ""
		if pred != "" {
			w = " WHERE " + pred
		}
		q := fmt.Sprintf(
			"SELECT %s AS label, count() c FROM (%s)%s GROUP BY label ORDER BY c DESC LIMIT %d",
			expr, runs, w, limit)
		v, e := scanCounts(ch.db.QueryContext(ctx, q, args...))
		if e != nil {
			return e
		}
		*target = v
		return nil
	}

	osExpr := "if(ostype = '', 'unknown', concat(ostype, ' ', osver))"

	groups := []struct {
		name   string
		target *[]NewGroupStat
		expr   string
		pred   string
		order  string
		limit  int
	}{
		{"top apps", &d.TopApps, "app", "app != ''", "runs DESC", 25},
		// Worst by rate, with the floor applied in SQL so the ranking is the
		// ranking -- not a top-25 that the page then has to filter again.
		{"worst apps", &d.WorstApps, "app", "app != ''",
			fmt.Sprintf("if(success + failed >= %d, failed / (success + failed), -1) DESC, failed DESC",
				d.MinRuns), 25},
		{"platform", &d.ByPlatform, "if(plat = '', 'unknown', plat)", "", "runs DESC", 6},
		{"type", &d.ByType, "if(kind = '', 'unknown', kind)", "", "runs DESC", 10},
		{"repo", &d.ByRepo, "if(src = '', 'unknown', src)", "", "runs DESC", 10},
		{"repo slug", &d.ByRepoSlug, "slug", "slug != ''", "runs DESC", 15},
		{"os", &d.ByOS, osExpr, "", "runs DESC", 15},
		{"host version", &d.ByHostVer, "hostver", "hostver != ''", "runs DESC", 15},
	}
	for _, g := range groups {
		add(g.name, func(ctx context.Context) error {
			return group(ctx, g.target, g.expr, g.pred, g.order, g.limit)
		})
	}

	counts := []struct {
		name   string
		target *[]NewCount
		expr   string
		pred   string
		limit  int
	}{
		// user_aborted is excluded: someone answering "no" is not a defect, and
		// including it makes it the largest bar on the chart.
		// blocked is included here on purpose. It is kept out of the failure
		// rate because it is not the script's fault, but it is exactly the list
		// worth working through, so it stays visible in the error panels.
		{"categories", &d.Categories, "final_cat",
			"outcome IN ('failed','blocked') AND final_cat NOT IN ('', 'user_aborted')", 15},
		{"privilege", &d.ByPrivilege, "if(priv = 1, 'unprivileged', 'privileged')", "", 3},
		{"cores", &d.ByCores, "toString(cores)", "cores > 0", 12},
		{"ram", &d.ByRAM, "concat(toString(intDiv(ram, 1024)), ' GB')", "ram > 0", 12},
		{"disk", &d.ByDisk, "concat(toString(disk), ' GB')", "disk > 0", 12},
		{"cpu", &d.ByCPU, "cpu", "cpu != ''", 8},
		{"gpu", &d.ByGPU, "gpu", "gpu != ''", 8},
		// Two vocabularies live in this column. Until payload version 2 it held
		// detect_gpu's verdict about the host -- igpu, dgpu, unknown -- and it now
		// answers whether the container was actually handed the devices. Charting
		// them together would read as one distribution over one question.
		{"gpu passthrough", &d.ByGPUPass,
			"multiIf(gpupass IN ('yes','no'), gpupass, 'host capability (pre-v2)')",
			"gpupass != ''", 6},
		// arch is the real column now. has_arm is the fallback for rows written
		// before the server stored it, where arm64-or-not is all that survives.
		{"arch", &d.ByArm,
			"if(cpuarch != '', cpuarch, if(arm = 1, 'arm64', 'x86_64 (inferred)'))", "", 6},
		{"client", &d.ByClient,
			"if(pver = 0, 'pre-versioning', concat('payload v', toString(pver)))", "", 6},
		{"method", &d.ByMethod, "meth", "meth != ''", 8},
	}
	for _, c := range counts {
		add(c.name, func(ctx context.Context) error {
			return simple(ctx, c.target, c.expr, c.pred, c.limit)
		})
	}

	// Exit codes, labelled with the engine's own descriptions.
	add("exit codes", func(ctx context.Context) error {
		rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT final_exit, count() c, uniqExact(app) apps
		FROM (%s)
		WHERE outcome IN ('failed','blocked') AND final_exit != 0
		GROUP BY final_exit
		ORDER BY c DESC
		LIMIT 20`, runs), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x NewExitCode
			if rows.Scan(&x.Code, &x.Count, &x.Apps) == nil {
				x.Desc = getExitCodeDescription(x.Code)
				d.ExitCodes = append(d.ExitCodes, x)
			}
		}
		return rows.Err()
	})

	// Signatures: the same failure text seen across runs. The closest thing to
	// "why", and the existing pages compute it without ever showing it.
	add("signatures", func(ctx context.Context) error {
		rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT final_cat, final_exit,
		       substring(if(failcmd != '', failcmd, final_err), 1, 200) msg,
		       count() c, uniqExact(app) apps
		FROM (%s)
		WHERE outcome IN ('failed','blocked') AND final_err != ''
		GROUP BY final_cat, final_exit, msg
		ORDER BY c DESC
		LIMIT 30`, runs), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s NewErrorSignature
			if rows.Scan(&s.Category, &s.ExitCode, &s.Message, &s.Count, &s.Apps) == nil {
				d.Signatures = append(d.Signatures, s)
			}
		}
		return rows.Err()
	})

	// Daily trend, on the same run basis as everything else.
	add("daily", func(ctx context.Context) error {
		rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT toString(toDate(last_seen)) day, count(),
		       countIf(outcome = 'success'),
		       countIf(outcome = 'failed'),
		       countIf(outcome = 'blocked'),
		       countIf(outcome = 'aborted'),
		       countIf(outcome = 'unfinished')
		FROM (%s)
		GROUP BY day ORDER BY day`, runs), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p NewDailyPoint
			if rows.Scan(&p.Day, &p.Runs, &p.Success, &p.Failed,
				&p.Blocked, &p.Aborted, &p.Unfinished) == nil {
				d.Daily = append(d.Daily, p)
			}
		}
		return rows.Err()
	})

	// The install log.
	add("recent", func(ctx context.Context) error {
		rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT run, app, kind, final_status, final_exit, final_cat, plat,
		       %s, hostver, slug, meth, cores, ram, disk, priv, duration,
		       formatDateTime(last_seen, '%%Y-%%m-%%d %%H:%%i'),
		       substring(final_err, 1, 400),
		       substring(failcmd, 1, 400), failline,
		       if(cpuarch != '', cpuarch, if(arm = 1, 'arm64', '')),
		       kernelver, appver, cpu, cpumodel, gpu, gpumodel, gpupass,
		       ramspeed, pver
		FROM (%s)
		ORDER BY last_seen DESC
		LIMIT 300`, osExpr, runs), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r NewRecentRun
			var priv uint8
			if rows.Scan(&r.Run, &r.App, &r.Type, &r.Status, &r.ExitCode, &r.Category,
				&r.Platform, &r.OS, &r.HostVer, &r.Repo, &r.Method,
				&r.Cores, &r.RAM, &r.Disk, &priv, &r.Duration, &r.LastSeen, &r.Error,
				&r.FailedCommand, &r.FailedLine, &r.Arch,
				&r.KernelVersion, &r.AppVersion, &r.CPUVendor, &r.CPUModel,
				&r.GPUVendor, &r.GPUModel, &r.GPUPassthrough,
				&r.RAMSpeed, &r.PayloadVersion) == nil {
				// ct_type 1 is unprivileged, so privileged is the other case.
				r.Privileged = priv != 1
				d.Recent = append(d.Recent, r)
			}
		}
		return rows.Err()
	})

	runSections(ctx, dashSectionLimit, sections, warn)

	// Arrival order is arbitrary once the sections race; sorting keeps two
	// loads that failed the same way from disagreeing about the order.
	sort.Strings(d.Warnings)

	return d, nil
}

// runSections executes every section, at most limit of them at a time, and
// reports the ones that failed through warn. Errors are collected rather than
// propagated: the page degrades per section by design, so one dead breakdown
// must not blank the other twenty-nine.
//
// A free function rather than a closure so it can be tested without a
// database. It is worth the seam: the first concurrent version of this page
// registered all thirty sections and then never ran them, and nothing caught
// it, because every other test of this file needs a live ClickHouse.
func runSections(ctx context.Context, limit int, sections []dashSection,
	warn func(string, error)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, s := range sections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Take a slot, unless the request is already over -- otherwise a
			// cancelled page load still queues every remaining section.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				warn(s.name, ctx.Err())
				return
			}
			warn(s.name, s.run(ctx))
		}()
	}
	wg.Wait()
}

// liveSections are the two panels that ignore the period selector on purpose:
// how much is in flight right now, and which runs those are.
//
// Both are bounded to the last day or two by their own WHERE clause, so they
// cost a fraction of the windowed sections, which aggregate over as much as a
// year. That difference is the whole reason they are separated out.
func (ch *CHClient) liveSections(d *NewDashData) []dashSection {
	return []dashSection{
		{name: "live", run: func(ctx context.Context) error {
			return ch.db.QueryRowContext(ctx, `
		SELECT
			uniqExactIf(run, last_seen >= now() - INTERVAL 20 MINUTE
			                 AND final_status NOT IN ('success','failed','aborted','unknown')),
			uniqExactIf(run, last_seen >= now() - INTERVAL 1 HOUR),
			uniqExactIf(run, last_seen >= now() - INTERVAL 24 HOUR)
		FROM (
			SELECT if(execution_id = '', random_id, execution_id) AS run,
			       argMax(status, created) AS final_status,
			       max(created)            AS last_seen
			FROM telemetry_db.telemetry
			WHERE created >= now() - INTERVAL 24 HOUR
			GROUP BY run
		)`).Scan(&d.LiveInFlight, &d.LiveLastHour, &d.LiveLast24h)
		}},

		// Kept separate from Recent: the install log answers "what happened",
		// this answers "what is happening".
		{name: "live runs", run: func(ctx context.Context) error {
			lr, err := ch.db.QueryContext(ctx, `
		SELECT app, kind, final_status, plat, os, slug,
		       formatDateTime(last_seen, '%Y-%m-%d %H:%i'),
		       toUInt32(dateDiff('second', last_seen, now()))
		FROM (
			SELECT if(execution_id = '', random_id, execution_id) AS run,
			       argMax(status, created)  AS final_status,
			       max(created)             AS last_seen,
			       any(nsapp)               AS app,
			       any(type)                AS kind,
			       any(`+platformExpr+`)    AS plat,
			       if(any(os_type) = '', 'unknown',
			          concat(any(os_type), ' ', any(os_version))) AS os,
			       any(repo_slug)           AS slug
			FROM telemetry_db.telemetry
			WHERE created >= now() - INTERVAL 2 HOUR
			GROUP BY run
		)
		WHERE final_status NOT IN ('success','failed','aborted','unknown')
		ORDER BY last_seen DESC
		LIMIT 40`)
			if err != nil {
				return err
			}
			defer lr.Close()
			for lr.Next() {
				var r NewRecentRun
				if lr.Scan(&r.App, &r.Type, &r.Status, &r.Platform, &r.OS,
					&r.Repo, &r.LastSeen, &r.Duration) == nil {
					d.LiveNow = append(d.LiveNow, r)
				}
			}
			return lr.Err()
		}},
	}
}

// RefreshLive re-runs only the live panels over an already-built page.
//
// The response is worth caching because of its windowed sections: thirty
// aggregations over up to a year of rows, which is what made this page slow.
// The live panels are the opposite -- two bounded queries answering "what is
// happening now", a question that must not be answered out of a cache keyed on
// a period selector it deliberately ignores.
//
// So a cache hit serves the stored page and runs these two over the top of it.
// The expensive part is paid once per TTL; the live part is never stale.
func (ch *CHClient) RefreshLive(ctx context.Context, d *NewDashData) {
	// Cleared before the queries rather than after them. If one fails, the
	// panel must come back empty next to a warning saying so -- serving the
	// stored page's rows would present the previous load as the current state,
	// which is the one thing this panel exists not to do.
	d.LiveNow = nil
	d.LiveInFlight, d.LiveLastHour, d.LiveLast24h = 0, 0, 0

	// The stored page also carries that load's warnings. Dropping the live ones
	// stops a complaint outliving the failure that caused it.
	d.Warnings = dropWarnings(d.Warnings, "live", "live runs")

	var mu sync.Mutex
	warn := func(section string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		d.Warnings = append(d.Warnings, fmt.Sprintf("%s: %v", section, err))
		mu.Unlock()
	}

	runSections(ctx, dashSectionLimit, ch.liveSections(d), warn)
	sort.Strings(d.Warnings)
}

// dropWarnings removes the entries belonging to the named sections, matching
// the "section: message" shape that warn writes.
func dropWarnings(warnings []string, sections ...string) []string {
	out := warnings[:0:0]
	for _, w := range warnings {
		keep := true
		for _, s := range sections {
			if strings.HasPrefix(w, s+": ") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, w)
		}
	}
	return out
}

package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCH answers from in-memory runs and checks placeholders and column counts.
type fakeCH struct {
	t    *testing.T
	now  time.Time
	runs []fakeRun
	mu   sync.Mutex
	seen []string
}

type fakeStep struct {
	status string
	at     time.Duration
}

type fakeRun struct {
	id, rid, app, typ, outcome, stage                       string
	started                                                 time.Duration
	steps                                                   []fakeStep
	exit                                                    int
	cat, msg, step, phase, fn, cmd, errText                 string
	line                                                    int
	os, osVer, pve, platform, source, slug, method          string
	cores, ram, disk, ct, dur, pver                         int
	cpuVendor, cpuModel, gpuVendor, gpuModel, gpuPass, ram2 string
	arch, kernel, appVer                                    string
}

func (r fakeRun) startedAt(now time.Time) time.Time { return now.Add(-r.started) }

func (r fakeRun) stepAt(now time.Time, i int) time.Time { return r.startedAt(now).Add(r.steps[i].at) }

func (r fakeRun) endAt(now time.Time) time.Time {
	for i := len(r.steps) - 1; i >= 0; i-- {
		if isTerminalStatus(r.steps[i].status) && r.steps[i].status != outcomeUnknown {
			return r.stepAt(now, i)
		}
	}
	return r.stepAt(now, len(r.steps)-1)
}

func (r fakeRun) lastStage(now time.Time) (string, time.Time) {
	for i := len(r.steps) - 1; i >= 0; i-- {
		if !isTerminalStatus(r.steps[i].status) {
			return r.steps[i].status, r.stepAt(now, i)
		}
	}
	return "", time.Time{}
}

func (r fakeRun) cause() (kind, key string) {
	switch {
	case r.msg != "":
		return "message", r.msg
	case r.cmd != "":
		return "command", r.cmd
	}
	return "exit_code", "exit " + strconv.Itoa(r.exit)
}

func (r fakeRun) setup() string {
	return strings.Join([]string{r.cpuModel, r.gpuModel, r.ram2, r.pve, r.kernel}, "|")
}

func (r fakeRun) install() bool { return r.typ != "pve" && r.typ != "addon" && r.typ != "tool" }

func steps(s ...interface{}) []fakeStep {
	var out []fakeStep
	for i := 0; i < len(s); i += 2 {
		out = append(out, fakeStep{s[i].(string), time.Duration(s[i+1].(int)) * time.Second})
	}
	return out
}

const oldTrace = "exit_code=1 | The command failed; its output says why | at line 214: $STD apt-get install -y libvips-dev\n---\n" +
	"Reading package lists...\nE: Unable to locate package libvips-dev\n[ERROR] in line 214: exit code 1"

// fakeRuns is a small, varied day of installs.
func fakeRuns() []fakeRun {
	host := fakeRun{os: "debian", osVer: "13", pve: "9.0.10", platform: "pve", source: "ProxmoxVE",
		slug: "community-scripts/ProxmoxVE", method: "default", cores: 2, ram: 2048, disk: 8, ct: 1, pver: 3,
		cpuVendor: "amd", cpuModel: "AMD Ryzen 7 5800X", ram2: "3200", arch: "amd64", kernel: "6.14.8-2-pve"}
	mk := func(id, app, typ, outcome string, started int, st []fakeStep, mod func(*fakeRun)) fakeRun {
		r := host
		r.id, r.rid, r.app, r.typ, r.outcome = id, "rid-"+id, app, typ, outcome
		r.started, r.steps = time.Duration(started)*time.Minute, st
		if mod != nil {
			mod(&r)
		}
		return r
	}
	ok := steps("installing", 0, "validation", 20, "configuring", 60, "success", 240)
	var runs []fakeRun
	for i := 0; i < 6; i++ {
		runs = append(runs, mk(fmt.Sprintf("dock-%d", i), "docker", "lxc", "success", 30+i*40, ok, nil))
	}
	runs = append(runs,
		mk("dock-f", "docker", "lxc", "failed", 25, steps("installing", 0, "validation", 15, "configuring", 50, "failed", 130), func(r *fakeRun) {
			r.exit, r.cat, r.msg = 100, "apt", "E: Unable to locate package docker-compose-plugin"
			r.step, r.phase, r.fn, r.cmd, r.line = "Installing Docker", "container", "install_docker", "$STD apt-get install -y docker-compose-plugin", 88
			r.errText = "exit_code=100 | APT: Package manager error | at line 88: $STD apt-get install -y docker-compose-plugin\n---\nE: Unable to locate package docker-compose-plugin"
		}),
		mk("dock-run", "docker", "lxc", "in_progress", 2, steps("installing", 0, "validation", 18, "configuring", 61), nil),
		mk("vault-run", "vaultwarden", "lxc", "in_progress", 1, steps("installing", 0), nil),
	)
	for i := 0; i < 3; i++ {
		runs = append(runs, mk(fmt.Sprintf("ha-%d", i), "haos-vm", "vm", "success", 60+i*90, steps("installing", 0, "success", 420), func(r *fakeRun) {
			r.cores, r.ram, r.disk, r.os, r.osVer = 2, 4096, 32, "", ""
		}))
	}
	for i := 0; i < 2; i++ {
		runs = append(runs, mk(fmt.Sprintf("immich-old-%d", i), "immich", "lxc", "failed", 200+i*30, steps("installing", 0, "configuring", 40, "failed", 900), func(r *fakeRun) {
			r.exit, r.cat, r.pver, r.errText, r.cores, r.ram, r.disk = 1, "apt", 2, oldTrace, 4, 6144, 20
			r.cmd, r.line, r.cpuModel = "$STD apt-get install -y libvips-dev", 214, fmt.Sprintf("Intel N100 #%d", i)
		}))
	}
	runs = append(runs, mk("immich-ok", "immich", "lxc", "success", 150, steps("installing", 0, "configuring", 40, "success", 1300), nil))
	for i := 0; i < 3; i++ {
		runs = append(runs, mk(fmt.Sprintf("immich-%d", i), "immich", "lxc", "failed", 10+i*20, steps("installing", 0, "configuring", 40, "failed", 700), func(r *fakeRun) {
			r.exit, r.cat, r.msg, r.step, r.phase, r.fn = 239, "dependency", "npm ERR! code ENOSPC", "Building Immich", "container", "setup_nodejs"
			r.cmd, r.line, r.cores, r.ram, r.disk = "$STD npm ci", 132, 4, 6144, 20
			r.errText = "exit_code=239 | npm/Node.js: Unexpected runtime error | step: Building Immich | in setup_nodejs | at line 132: $STD npm ci\n---\nnpm ERR! code ENOSPC\nnpm ERR! syscall write\nnpm ERR! errno -28"
			r.cpuModel, r.gpuModel = []string{"Intel N100", "AMD Ryzen 5 5600G", "Intel i5-12500"}[i], []string{"", "AMD Cezanne", "Intel UHD 770"}[i]
		}))
	}
	for i := 0; i < 4; i++ {
		runs = append(runs, mk(fmt.Sprintf("jelly-%d", i), "jellyfin", "lxc", "success", 100+i*50, ok, nil))
	}
	runs = append(runs, mk("jelly-u", "jellyfin", "lxc", "unknown", 400, steps("installing", 0, "validation", 22, "configuring", 75, "unknown", 4*3600+75), func(r *fakeRun) {
		r.stage, r.pver, r.errText = "configuring", 2, "Installation timed out - no completion status received after 4h"
	}))
	runs = append(runs,
		mk("npm-0", "nginxproxymanager", "lxc", "success", 300, ok, nil),
		mk("npm-1", "nginxproxymanager", "lxc", "success", 320, ok, nil),
		mk("npm-a", "nginxproxymanager", "lxc", "aborted", 340, steps("installing", 0, "validation", 10, "aborted", 25), func(r *fakeRun) {
			r.exit, r.cat = 130, "user_aborted"
		}),
		mk("pihole-0", "pihole", "lxc", "success", 500, ok, func(r *fakeRun) { r.platform, r.pve, r.os, r.osVer = "incus", "incus-6.0.4", "alpine", "3.22" }),
		mk("pihole-1", "pihole", "lxc", "success", 520, ok, nil),
	)
	for i := 0; i < 3; i++ {
		runs = append(runs, mk(fmt.Sprintf("ppi-%d", i), "post-pve-install", "pve", "success", 60+i*7, steps("installing", 0, "success", 50), nil))
	}
	runs = append(runs, mk("ppi-f", "post-pve-install", "pve", "failed", 70, steps("installing", 0, "failed", 31), func(r *fakeRun) {
		r.exit, r.cat, r.msg, r.cmd, r.errText = 100, "apt", "E: The repository 'http://download.proxmox.com/debian/pve trixie InRelease' is not signed.", "apt-get update",
			"exit_code=100 | APT: Package manager error\n---\nE: The repository 'http://download.proxmox.com/debian/pve trixie InRelease' is not signed."
	}))
	runs = append(runs,
		mk("netdata-0", "netdata", "addon", "success", 80, steps("installing", 0, "success", 90), nil),
		mk("netdata-f", "netdata", "addon", "failed", 85, steps("installing", 0, "failed", 12), func(r *fakeRun) {
			r.exit, r.cat, r.errText = 127, "command_not_found", "exit_code=127 | Command not found\n---\nbash: line 12: curl: command not found"
		}),
	)
	for i := range runs {
		if runs[i].outcome == outcomeFailed || runs[i].outcome == outcomeAborted {
			if runs[i].stage == "" {
				runs[i].stage, _ = runs[i].lastStage(time.Now())
			}
		}
	}
	return runs
}

func newFakeCH(t *testing.T) (*CHClient, *fakeCH) {
	f := &fakeCH{t: t, now: time.Now().UTC(), runs: fakeRuns()}
	return &CHClient{db: sql.OpenDB(f), stuckHours: 4}, f
}

func (f *fakeCH) Connect(context.Context) (driver.Conn, error) { return &fakeConn{f}, nil }
func (f *fakeCH) Driver() driver.Driver                        { return fakeDriver{f} }

type fakeDriver struct{ f *fakeCH }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{d.f}, nil }

type fakeConn struct{ f *fakeCH }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not used")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("no transactions") }
func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.f.query(q, args)
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// selectList returns the expressions of the outermost SELECT.
func selectList(q string) []string {
	q = strings.TrimSpace(q)
	if !strings.HasPrefix(q, "SELECT") {
		return nil
	}
	var parts []string
	depth, start, in := 0, len("SELECT"), false
	for i := start; i < len(q); i++ {
		c := q[i]
		switch {
		case in && c == '\\':
			i++
		case c == '\'':
			in = !in
		case in:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && c == ',':
			parts = append(parts, strings.TrimSpace(q[start:i]))
			start = i + 1
		case depth == 0 && strings.HasPrefix(q[i:], "FROM") && (q[i-1] == ' ' || q[i-1] == '\n' || q[i-1] == '\t'):
			return append(parts, strings.TrimSpace(q[start:i]))
		}
	}
	return append(parts, strings.TrimSpace(q[start:]))
}

func (f *fakeCH) query(q string, args []driver.NamedValue) (driver.Rows, error) {
	f.mu.Lock()
	f.seen = append(f.seen, q)
	f.mu.Unlock()
	if n := placeholderCount(q); n != len(args) {
		f.t.Errorf("%d placeholders but %d args:\n%s", n, len(args), q)
	}
	cols := selectList(q)
	rows := f.route(q, args)
	for _, r := range rows {
		if len(r) != len(cols) {
			f.t.Errorf("query selects %d columns, fake row has %d:\n%s", len(cols), len(r), q)
			break
		}
	}
	names := make([]string, len(cols))
	for i := range names {
		names[i] = "c" + strconv.Itoa(i)
	}
	return &fakeRows{cols: names, rows: rows}, nil
}

func namedStrings(args []driver.NamedValue) []string {
	var out []string
	for _, a := range args {
		if s, ok := a.Value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

var limitRe = regexp.MustCompile(`LIMIT (\d+)(?: OFFSET (\d+))?`)

// matching applies the few run filters the fixtures use.
func (f *fakeCH) matching(q string, args []driver.NamedValue) []fakeRun {
	strs := namedStrings(args)
	has := func(s string) bool {
		for _, a := range strs {
			if a == s {
				return true
			}
		}
		return false
	}
	outer := q
	if i := strings.LastIndex(q, "\nWHERE "); i >= 0 {
		outer = q[i:]
	}
	var out []fakeRun
	for _, r := range f.runs {
		_, key := r.cause()
		switch {
		case strings.Contains(outer, "r_outcome = ?") && !has(r.outcome):
			continue
		case strings.Contains(outer, "r_outcome = 'failed'") && r.outcome != outcomeFailed:
			continue
		case strings.Contains(outer, "r_cause = ?") && !has(key):
			continue
		case strings.Contains(q, "nsapp = ?") && !has(r.app):
			continue
		case strings.Contains(outer, "r_type NOT IN ('pve','addon','tool')") && !r.install():
			continue
		}
		out = append(out, r)
	}
	return out
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (f *fakeCH) route(q string, args []driver.NamedValue) [][]driver.Value {
	now := f.now
	switch {
	case strings.Contains(q, "count() OVER ()"):
		runs := f.matching(q, args)
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].started < runs[j].started })
		lim, off := len(runs), 0
		if m := limitRe.FindStringSubmatch(q); m != nil {
			lim, _ = strconv.Atoi(m[1])
			off, _ = strconv.Atoi(m[2])
		}
		var out [][]driver.Value
		for i := off; i < len(runs) && i < off+lim; i++ {
			out = append(out, []driver.Value{runs[i].id, ms(runs[i].startedAt(now)), int64(len(runs))})
		}
		return out
	case strings.HasPrefix(q, "SELECT run_id, r_eid"):
		ids := map[string]bool{}
		for _, s := range namedStrings(args) {
			ids[s] = true
		}
		var out [][]driver.Value
		for _, r := range f.runs {
			if ids[r.id] {
				out = append(out, f.detailRow(r))
			}
		}
		return out
	case strings.Contains(q, "ORDER BY k, created"):
		ids := map[string]bool{}
		for _, s := range namedStrings(args) {
			ids[s] = true
		}
		var out [][]driver.Value
		for _, r := range f.runs {
			if ids[r.id] {
				for i, s := range r.steps {
					out = append(out, []driver.Value{r.id, s.status, ms(r.stepAt(now, i))})
				}
			}
		}
		return out
	case strings.Contains(q, "AS b_dim"):
		return f.breakdownRows(q, args)
	case strings.Contains(q, "AS c_key"):
		return f.causeRows(q, args)
	case strings.Contains(q, "AS t_key"):
		groups := map[string][2]int64{}
		for _, r := range f.matching(q, args) {
			_, key := r.cause()
			g := groups[key]
			if now.Sub(r.endAt(now)) < 24*time.Hour {
				g[0]++
			} else {
				g[1]++
			}
			groups[key] = g
		}
		var out [][]driver.Value
		for k, g := range groups {
			out = append(out, []driver.Value{k, g[0], g[1]})
		}
		return out
	case strings.Contains(q, "AS a_app"):
		return f.appRows(q, args)
	case strings.Contains(q, "r_last_seen >= ?"):
		var running, stalled, nores int64
		for _, r := range f.matching(q, args) {
			switch {
			case r.outcome == outcomeInProgress && r.started < 4*time.Hour:
				running++
			case r.outcome == outcomeInProgress:
				stalled++
			case r.outcome == outcomeUnknown:
				nores++
			}
		}
		return [][]driver.Value{{running, stalled, nores}}
	case strings.HasPrefix(q, "SELECT uniq("):
		return [][]driver.Value{{uint64(2_195_915)}}
	case strings.Contains(q, "ORDER BY r_end DESC"):
		var out [][]driver.Value
		for _, r := range f.runs {
			if r.outcome != outcomeInProgress {
				out = append(out, []driver.Value{r.app, r.typ, r.outcome, int64(r.exit), r.os, r.osVer, r.pve, r.method, r.platform,
					r.endAt(now).Format("2006-01-02 15:04:05.000")})
			}
		}
		return out
	case strings.HasPrefix(q, "SELECT toInt64(count())"):
		return [][]driver.Value{{int64(len(f.matching(q, args)))}}
	}
	f.t.Errorf("the fake has no answer for:\n%s", q)
	return nil
}

var lastAliasRe = regexp.MustCompile(`\b(r_[a-z_]+|run_id)\b`)

// detailRow follows runDetailCols, so a scan in another order fails the tests.
func (f *fakeCH) detailRow(r fakeRun) []driver.Value {
	now := f.now
	stage, stageAt := r.lastStage(now)
	kind, key := r.cause()
	term := int64(0)
	if r.outcome != outcomeInProgress && r.outcome != outcomeUnknown {
		term = ms(r.endAt(now))
	}
	last := r.stepAt(now, len(r.steps)-1)
	v := map[string]driver.Value{
		"run_id": r.id, "r_eid": r.id, "r_rid": r.rid, "r_app": r.app, "r_type": r.typ, "r_method": r.method,
		"r_os": r.os, "r_osver": r.osVer, "r_pve": r.pve, "r_platform": r.platform, "r_source": r.source,
		"r_slug": r.slug, "r_cores": int64(r.cores), "r_ram": int64(r.ram), "r_disk": int64(r.disk), "r_ct": int64(r.ct),
		"r_cpu_vendor": r.cpuVendor, "r_cpu_model": r.cpuModel, "r_gpu_vendor": r.gpuVendor, "r_gpu_model": r.gpuModel,
		"r_gpu_pass": r.gpuPass, "r_ram_speed": r.ram2, "r_arch": r.arch, "r_kernel": r.kernel, "r_appver": r.appVer,
		"r_migrated": "", "r_arm": int64(0), "r_dur": int64(r.dur), "r_pver": int64(r.pver),
		"r_outcome": r.outcome, "r_stage": stage, "r_exit": int64(r.exit), "r_cat": r.cat, "r_msg": r.msg,
		"r_step": r.step, "r_phase": r.phase, "r_func": r.fn, "r_cmd": r.cmd, "r_line": int64(r.line),
		"r_error": r.errText, "r_cause": key, "r_cause_kind": kind,
		"r_started": ms(r.startedAt(now)), "r_last_seen": ms(last), "r_term_at": term, "r_stage_at": ms(stageAt),
	}
	var row []driver.Value
	for _, expr := range selectList("SELECT " + runDetailCols + " FROM x") {
		names := lastAliasRe.FindAllString(expr, -1)
		val, ok := v[names[len(names)-1]]
		if !ok {
			f.t.Fatalf("fake has no value for %s", expr)
		}
		row = append(row, val)
	}
	return row
}

var dimNameRe = regexp.MustCompile(`tuple\('([a-z]+)'`)

func (f *fakeCH) breakdownRows(q string, args []driver.NamedValue) [][]driver.Value {
	now := f.now
	type acc struct {
		runs, ok, bad, ab, nores, running, durSum, durN int64
		apps                                            map[string]int
	}
	groups := map[[2]string]*acc{}
	value := func(dim string, r fakeRun) (string, bool) {
		switch dim {
		case "all":
			return "", r.install()
		case "app":
			return r.app, r.install()
		case "day":
			return r.startedAt(now).Format("2006-01-02"), r.install()
		case "hour":
			return r.startedAt(now).Truncate(time.Hour).Format("2006-01-02 15:04:05"), r.install()
		case "os":
			return r.os, r.install() && r.os != ""
		case "method":
			return r.method, r.install()
		case "pve":
			return r.pve, r.install()
		case "platform":
			return r.platform, r.install()
		case "category":
			return r.cat, r.install() && r.outcome == outcomeFailed && r.cat != ""
		case "exit":
			return strconv.Itoa(r.exit), r.install() && r.outcome == outcomeFailed
		case "type":
			return r.typ, true
		case "slug":
			return r.slug, r.slug != ""
		case "tool":
			return r.app, r.typ == "pve" || r.typ == "tool"
		case "addon":
			return r.app, r.typ == "addon"
		case "gpu":
			return r.gpuVendor + "|" + r.gpuPass, r.install() && r.gpuVendor != ""
		}
		return "", false
	}
	for _, m := range dimNameRe.FindAllStringSubmatch(q, -1) {
		for _, r := range f.matching(q, args) {
			val, ok := value(m[1], r)
			if !ok {
				continue
			}
			k := [2]string{m[1], val}
			a := groups[k]
			if a == nil {
				a = &acc{apps: map[string]int{}}
				groups[k] = a
			}
			a.runs++
			switch r.outcome {
			case outcomeSuccess:
				a.ok++
				a.durSum += int64(r.endAt(now).Sub(r.startedAt(now)).Seconds())
				a.durN++
			case outcomeFailed:
				a.bad++
				a.apps[r.app]++
			case outcomeAborted:
				a.ab++
			case outcomeUnknown:
				a.nores++
			case outcomeInProgress:
				a.running++
			}
		}
	}
	var out [][]driver.Value
	for k, a := range groups {
		var top []string
		for app := range a.apps {
			top = append(top, app)
		}
		sort.Strings(top)
		out = append(out, []driver.Value{k[0], k[1], a.runs, a.ok, a.bad, a.ab, a.nores, a.running,
			int64(len(a.apps)), strings.Join(top, ", "), a.durSum, a.durN})
	}
	return out
}

func (f *fakeCH) causeRows(q string, args []driver.NamedValue) [][]driver.Value {
	now := f.now
	type acc struct {
		kind, cat string
		exit      int
		runs      []fakeRun
	}
	groups := map[string]*acc{}
	var order []string
	for _, r := range f.matching(q, args) {
		kind, key := r.cause()
		if groups[key] == nil {
			groups[key] = &acc{kind: kind, cat: r.cat, exit: r.exit}
			order = append(order, key)
		}
		groups[key].runs = append(groups[key].runs, r)
	}
	sort.SliceStable(order, func(i, j int) bool { return len(groups[order[i]].runs) > len(groups[order[j]].runs) })
	var out [][]driver.Value
	for _, key := range order {
		g := groups[key]
		apps, setups := map[string]bool{}, map[string]bool{}
		first, last := now, time.Time{}
		sort.SliceStable(g.runs, func(i, j int) bool { return g.runs[i].endAt(now).After(g.runs[j].endAt(now)) })
		var ids []string
		for i, r := range g.runs {
			apps[r.app], setups[r.setup()] = true, true
			if e := r.endAt(now); e.Before(first) {
				first = e
			}
			if e := r.endAt(now); e.After(last) {
				last = e
			}
			if i < 3 {
				ids = append(ids, r.id)
			}
		}
		var names []string
		for a := range apps {
			names = append(names, a)
		}
		sort.Strings(names)
		out = append(out, []driver.Value{key, g.kind, g.cat, int64(g.exit), int64(len(g.runs)), int64(len(apps)),
			strings.Join(names, ","), int64(len(setups)), ms(first), ms(last), strings.Join(ids, ",")})
	}
	return out
}

func (f *fakeCH) appRows(q string, args []driver.NamedValue) [][]driver.Value {
	now := f.now
	type acc struct {
		typ                               string
		runs, ok, bad, ab, nores, running int64
		setups                            map[string]bool
		causes                            map[string]int
		exit                              int
		cat, lastFailID                   string
		lastFail, lastOk                  time.Time
	}
	groups := map[string]*acc{}
	for _, r := range f.matching(q, args) {
		a := groups[r.app]
		if a == nil {
			a = &acc{typ: r.typ, setups: map[string]bool{}, causes: map[string]int{}}
			groups[r.app] = a
		}
		a.runs++
		switch r.outcome {
		case outcomeSuccess:
			a.ok++
			if r.endAt(now).After(a.lastOk) {
				a.lastOk = r.endAt(now)
			}
		case outcomeFailed:
			a.bad++
			kind, key := r.cause()
			a.setups[r.setup()] = true
			a.causes[kind+":"+key]++
			a.exit, a.cat = r.exit, r.cat
			if r.endAt(now).After(a.lastFail) {
				a.lastFail, a.lastFailID = r.endAt(now), r.id
			}
		case outcomeAborted:
			a.ab++
		case outcomeUnknown:
			a.nores++
		case outcomeInProgress:
			a.running++
		}
	}
	var out [][]driver.Value
	for app, a := range groups {
		if strings.Contains(q, "HAVING a_failed > 0") && a.bad == 0 {
			continue
		}
		top, n := "", 0
		for c, k := range a.causes {
			if k > n || (k == n && c < top) {
				top, n = c, k
			}
		}
		out = append(out, []driver.Value{app, a.typ, a.runs, a.ok, a.bad, a.ab, a.nores, a.running,
			int64(len(a.setups)), top, int64(a.exit), a.cat, ms(a.lastFail), ms(a.lastOk), a.lastFailID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][2].(int64) > out[j][2].(int64) })
	return out
}

func TestFetchRunsKeepsListOrderAndBuildsStages(t *testing.T) {
	ch, fake := newFakeCH(t)
	recs, total, err := ch.FetchRuns(context.Background(), RunFilter{Days: 1, Type: "all"}, 1, 50, "-created")
	if err != nil {
		t.Fatal(err)
	}
	if total != len(fake.runs) || len(recs) != len(fake.runs) {
		t.Fatalf("total %d, got %d records of %d runs", total, len(recs), len(fake.runs))
	}
	byID := map[string]RunRecord{}
	for i, r := range recs {
		byID[r.ExecutionID] = r
		if i > 0 && recs[i-1].StartedAt < r.StartedAt {
			t.Errorf("records out of list order at %d", i)
		}
	}
	run := byID["dock-run"]
	if run.Status != outcomeInProgress || run.LastStage != "configuring" || run.OsType != "debian" || run.CoreCount != 2 {
		t.Errorf("in progress run: %+v", run)
	}
	if len(run.Stages) != 3 || run.Stages[2].Seconds != -1 || run.Stages[0].Seconds != 18 {
		t.Errorf("stages %+v", run.Stages)
	}
	u := byID["jelly-u"]
	if u.Status != outcomeUnknown || u.Reason != "no result" || u.LastStage != "configuring" || u.Duration != 75 {
		t.Errorf("no result run: %+v", u)
	}
	old := byID["immich-old-0"]
	if old.ErrorMessage != "E: Unable to locate package libvips-dev" || old.CauseKind != "command" || old.FailedLine != 214 {
		t.Errorf("old-format failure: %+v", old)
	}
	if a := byID["npm-a"]; a.Status != outcomeAborted || a.ExitDescription == "" || a.Cause != "" {
		t.Errorf("aborted: %+v", a)
	}
}

func TestFetchRunsPastTheLastPageCountsSeparately(t *testing.T) {
	ch, fake := newFakeCH(t)
	recs, total, err := ch.FetchRuns(context.Background(), RunFilter{Days: 1, Type: "all"}, 99, 50, "")
	if err != nil || len(recs) != 0 || total != len(fake.runs) {
		t.Errorf("recs %d total %d err %v", len(recs), total, err)
	}
}

func TestFetchCausesTrendAndExamples(t *testing.T) {
	ch, _ := newFakeCH(t)
	causes, err := ch.FetchCauses(context.Background(), RunFilter{Days: 7}, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(causes) == 0 || causes[0].Key != "npm ERR! code ENOSPC" || causes[0].Failures != 3 || causes[0].Setups != 3 {
		t.Fatalf("top cause %+v", causes)
	}
	var cmd *Cause
	for i := range causes {
		if causes[i].Kind == "command" {
			cmd = &causes[i]
		}
	}
	if cmd == nil || cmd.ExampleMessage != "E: Unable to locate package libvips-dev" || cmd.Trend == "" {
		t.Errorf("command cause %+v", cmd)
	}
	full, err := ch.FetchCauses(context.Background(), RunFilter{Days: 7}, 20, true)
	if err != nil || len(full[0].ExampleRuns) != 3 || full[0].ExampleRuns[0].Error == "" {
		t.Errorf("full export: %v %+v", err, full[0].ExampleRuns)
	}
}

func TestErrorPageCountsRunsNotRows(t *testing.T) {
	ch, _ := newFakeCH(t)
	data, err := ch.FetchErrorAnalysisData(context.Background(), RunFilter{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := data.Summary
	if s.Failed != 6 || s.NoResult != 1 || s.InProgress != 2 || s.Aborted != 1 {
		t.Errorf("summary %+v", s)
	}
	if data.TotalErrors != s.Failed || data.TotalInstalls != s.Runs || data.OverallFailRate != s.FailureRate {
		t.Errorf("legacy totals disagree with the summary: %+v", data)
	}
	if data.Bucket != "hour" || len(data.FailuresOverTime) == 0 {
		t.Errorf("today is bucketed by hour: %s %d", data.Bucket, len(data.FailuresOverTime))
	}
	var immich *AppErrorStat
	for i := range data.Apps {
		if data.Apps[i].App == "immich" {
			immich = &data.Apps[i]
		}
	}
	if immich == nil || !immich.FailingNow || immich.SetupsFailed != 5 || immich.TopCause != "npm ERR! code ENOSPC" {
		t.Errorf("immich %+v", immich)
	}
	for _, a := range data.Apps {
		if a.App == "post-pve-install" || a.App == "netdata" {
			t.Errorf("tools and addons belong to their own type filter: %s", a.App)
		}
	}
	if len(data.AppErrors) == 0 || data.AppErrors[0].TopError == "" {
		t.Errorf("legacy app errors lost the trace: %+v", data.AppErrors)
	}
}

func TestDashboardFiguresArePerRun(t *testing.T) {
	ch, _ := newFakeCH(t)
	d, err := ch.FetchDashboardPage(context.Background(), RunFilter{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalInstalls != d.Summary.Runs || d.FailedCount != 6 || d.InstallingCount != 2 || d.TotalAllTime == 0 {
		t.Errorf("cards %+v", d.Summary)
	}
	if len(d.TopTools) != 1 || d.TotalAddons != 2 {
		t.Errorf("tools %+v addons %d", d.TopTools, d.TotalAddons)
	}
	if len(d.TopCauses) == 0 || len(d.ErrorAnalysis) != len(d.TopCauses) || len(d.RecentRecords) == 0 {
		t.Errorf("error panels: %d causes, %d legacy, %d recent", len(d.TopCauses), len(d.ErrorAnalysis), len(d.RecentRecords))
	}
	if len(d.DailyStats) != 1 || d.Apps[0].Label != "docker" {
		t.Errorf("daily %+v apps %+v", d.DailyStats, d.Apps)
	}
}

func TestScriptStatsCarryTheLastFailure(t *testing.T) {
	ch, _ := newFakeCH(t)
	data, err := ch.FetchScriptStats(context.Background(), 7, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range data.TopScripts {
		if s.App == "netdata" {
			found = true
			if s.Total != 2 || s.Failed != 1 || s.FailureRate != 50 || s.LastFailureCause != "bash: line 12: curl: command not found" {
				t.Errorf("netdata %+v", s)
			}
		}
	}
	if !found || len(data.RecentScripts) == 0 {
		t.Errorf("scripts %+v", data.TopScripts)
	}
}

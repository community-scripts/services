package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildGroup mirrors the query the group closure in FetchNewDashboard builds.
// Keeping the shape in one place here means the regression below is checked
// against the same string the database would see.
func buildGroup(expr, pred, order string, limit int, runs string) string {
	w := ""
	if pred != "" {
		w = " WHERE " + pred
	}
	return fmt.Sprintf(`
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
}

func testRuns(t *testing.T, days int, repo, slug, platform string) (string, []interface{}) {
	t.Helper()
	where, args := chWhere(days, repo, slug, platform)
	return fmt.Sprintf(runsCTE, platformExpr, where), args
}

// The queries are assembled with fmt.Sprintf, and platformExpr contains a
// literal percent in "pve_version LIKE 'incus%'". That is harmless while it
// travels as an argument and corrupts the query the moment it reaches a format
// string, so the assembly is checked rather than assumed.
func TestRunsQueryAssembles(t *testing.T) {
	runs, args := testRuns(t, 7, "ProxmoxVE", "", "incus")

	if strings.Contains(runs, "%!") {
		t.Fatalf("format verb left unconsumed in runs query:\n%s", runs)
	}
	for _, want := range []string{
		"argMax(status, created)",
		"GROUP BY run",
		"LIKE 'incus%'",
	} {
		if !strings.Contains(runs, want) {
			t.Errorf("runs query is missing %q", want)
		}
	}
	if len(args) == 0 {
		t.Error("expected at least the day-range argument to be bound")
	}
}

// Regression: the first version of this page filtered per-run columns with
// HAVING. final_status is neither grouped nor aggregated, so ClickHouse rejected
// every breakdown and the whole page returned 500. The predicate belongs in
// WHERE, which also means it runs before the grouping rather than after it.
func TestRunPredicatesUseWhereNotHaving(t *testing.T) {
	runs, _ := testRuns(t, 7, "", "", "")
	q := buildGroup("nsapp", "nsapp != ''", "runs DESC", 25, runs)

	outer := q[strings.LastIndex(q, "FROM ("):]
	if strings.Contains(outer, "HAVING") {
		t.Error("per-run predicate is in HAVING; it must be WHERE")
	}
	where, group := strings.Index(outer, "WHERE"), strings.Index(outer, "GROUP BY")
	if where < 0 || group < 0 || where > group {
		t.Errorf("WHERE must precede GROUP BY (WHERE at %d, GROUP BY at %d)", where, group)
	}
}

// The install log nests the runs query inside another Sprintf and needs
// %%Y-style escaping for formatDateTime. Getting that wrong yields a query that
// only fails against a live database.
func TestNestedQueriesEscapeCorrectly(t *testing.T) {
	runs, _ := testRuns(t, 30, "", "", "")

	recent := fmt.Sprintf(`
		SELECT nsapp, formatDateTime(last_seen, '%%Y-%%m-%%d %%H:%%M')
		FROM (%s) ORDER BY last_seen DESC LIMIT 200`, runs)

	if strings.Contains(recent, "%!") {
		t.Fatalf("format verb left unconsumed:\n%s", recent)
	}
	if !strings.Contains(recent, "'%Y-%m-%d %H:%M'") {
		t.Error("formatDateTime pattern did not survive the nested Sprintf")
	}
	if !strings.Contains(recent, "argMax(status, created)") {
		t.Error("the runs subquery was not embedded")
	}
}

// The worst-apps ranking puts its floor inside ORDER BY, so a script with two
// runs and two failures cannot outrank one with two hundred runs and a real
// problem. If the floor were applied after the LIMIT, the list would be a
// top-25 by noise.
func TestWorstAppsRankingAppliesFloorBeforeLimit(t *testing.T) {
	runs, _ := testRuns(t, 7, "", "", "")
	min := minRunsFor(7)
	order := fmt.Sprintf(
		"if(success + failed >= %d, failed / (success + failed), -1) DESC, failed DESC", min)
	q := buildGroup("nsapp", "nsapp != ''", order, 25, runs)

	if !strings.Contains(q, fmt.Sprintf("success + failed >= %d", min)) {
		t.Error("the run floor is missing from the ordering")
	}
	if strings.Index(q, "ORDER BY") > strings.Index(q, "LIMIT") {
		t.Error("ORDER BY must come before LIMIT or the floor is applied to an already-truncated list")
	}
	// Aborted runs are excluded from the denominator on purpose: a user pressing
	// Ctrl+C is not the script failing.
	if strings.Contains(order, "aborted") {
		t.Error("aborted runs must not be part of the failure-rate denominator")
	}
}

// Regression, and the reason the page returned 500 twice: the runs subquery
// aliased sixteen aggregates back to the name of the column they aggregate --
// any(nsapp) AS nsapp. ClickHouse rejects that with "Different expressions with
// the same alias", and it rejects the whole query, so every panel went dark at
// once. Every alias now has a name of its own.
func TestNoAliasShadowsItsSourceColumn(t *testing.T) {
	re := regexp.MustCompile(`any\((\w+)\)\s+AS (\w+)`)
	for _, m := range re.FindAllStringSubmatch(runsCTE, -1) {
		if m[1] == m[2] {
			t.Errorf("any(%s) AS %s shadows its own source column", m[1], m[2])
		}
	}
	// The same shape, spelled differently.
	for _, agg := range []string{"argMax", "max", "min"} {
		re := regexp.MustCompile(agg + `\((\w+)[^)]*\)\s+AS (\w+)`)
		for _, m := range re.FindAllStringSubmatch(runsCTE, -1) {
			if m[1] == m[2] {
				t.Errorf("%s(%s ...) AS %s shadows its own source column", agg, m[1], m[2])
			}
		}
	}
}

func TestMinRunsScalesWithWindow(t *testing.T) {
	for _, c := range []struct{ days, want int }{
		{1, 5}, {7, 10}, {30, 20}, {365, 40},
	} {
		if got := minRunsFor(c.days); got != c.want {
			t.Errorf("minRunsFor(%d) = %d, want %d", c.days, got, c.want)
		}
	}
}

// Regression, and the one that actually reached main: the concurrent version of
// this page registered all thirty sections and then never ran them. It did not
// fail loudly -- the headline, live, all-time and median panels simply rendered
// as a confident zero. Every other test of this file needs a live ClickHouse,
// so nothing noticed. runSections is a free function precisely so these four do
// not.
func TestRunSectionsRunsEveryRegisteredSection(t *testing.T) {
	const n = 30

	var mu sync.Mutex
	seen := map[string]bool{}

	sections := make([]dashSection, 0, n)
	for i := range n {
		name := fmt.Sprintf("section-%02d", i)
		sections = append(sections, dashSection{
			name: name,
			run: func(context.Context) error {
				mu.Lock()
				seen[name] = true
				mu.Unlock()
				return nil
			},
		})
	}

	runSections(context.Background(), dashSectionLimit, sections,
		func(name string, err error) {
			if err != nil {
				t.Errorf("section %q reported %v", name, err)
			}
		})

	if len(seen) != n {
		t.Errorf("%d of %d sections ran; the rest would render as zeroes", len(seen), n)
	}
}

// The point of the change is that the sections overlap, and the point of the
// bound is that thirty of them do not hit a twenty-connection pool at once.
// Both halves are worth pinning: peak == 1 would mean the page quietly went
// back to paying for thirty round trips end to end.
func TestRunSectionsRespectsTheConcurrencyLimit(t *testing.T) {
	const limit = 4

	var mu sync.Mutex
	inFlight, peak := 0, 0

	sections := make([]dashSection, 0, 20)
	for range 20 {
		sections = append(sections, dashSection{
			name: "s",
			run: func(context.Context) error {
				mu.Lock()
				inFlight++
				if inFlight > peak {
					peak = inFlight
				}
				mu.Unlock()

				time.Sleep(2 * time.Millisecond)

				mu.Lock()
				inFlight--
				mu.Unlock()
				return nil
			},
		})
	}

	runSections(context.Background(), limit, sections, func(string, error) {})

	mu.Lock()
	defer mu.Unlock()
	if peak > limit {
		t.Errorf("peak concurrency was %d, above the limit of %d", peak, limit)
	}
	if peak < 2 {
		t.Errorf("peak concurrency was %d; the sections ran one at a time", peak)
	}
}

// A failing section is named, and a succeeding one is not. That pairing is what
// lets the page show nine panels and say which tenth is missing.
func TestRunSectionsReportsFailuresByName(t *testing.T) {
	var mu sync.Mutex
	var got []string

	sections := []dashSection{
		{name: "ok", run: func(context.Context) error { return nil }},
		{name: "broken", run: func(context.Context) error { return errors.New("boom") }},
	}

	runSections(context.Background(), dashSectionLimit, sections,
		func(name string, err error) {
			if err == nil {
				return
			}
			mu.Lock()
			got = append(got, fmt.Sprintf("%s: %v", name, err))
			mu.Unlock()
		})

	if len(got) != 1 || got[0] != "broken: boom" {
		t.Errorf(`got %v, want exactly ["broken: boom"]`, got)
	}
}

// Regression: unknown is a terminal status. isTerminalStatus says so, and the
// orphan cleanup agrees, but the dashboard classified anything outside
// success/failed/aborted as still running. Finished runs therefore sat in the
// live panel until they aged out of its two-hour window, and every breakdown
// counted them as unfinished.
func TestOutcomeTreatsUnknownAsTerminal(t *testing.T) {
	expr := outcomeExpr()

	if !strings.Contains(expr, "'success','failed','aborted','unknown'") {
		t.Error("the unfinished branch must exclude unknown; it is a terminal status")
	}
	// Belt and braces: whatever the branch looks like, unknown must not be able
	// to reach the unfinished label through it.
	unfinished := strings.Index(expr, "'unfinished'")
	if unfinished < 0 {
		t.Fatal("no unfinished branch in the outcome expression")
	}
	if !strings.Contains(expr[:unfinished], "'unknown'") {
		t.Error("unknown is not excluded before the unfinished branch is taken")
	}
}

// The two code lists are what reclassify rows already in the table, so the
// codes have to actually reach the expression. Spot-checked against the ones
// that prompted this: 113 was the largest user-abort signature, and 209/213/
// 214/215/223/225/231 were the Proxmox failures counted against the scripts.
func TestOutcomeCarriesTheCodeLists(t *testing.T) {
	expr := outcomeExpr()

	for _, code := range []int{113, 114, 122, 129, 130, 143, 253, 254} {
		if !strings.Contains(expr, strconv.Itoa(code)) {
			t.Errorf("user-abort code %d is missing from the outcome expression", code)
		}
	}
	for _, code := range []int{209, 213, 214, 215, 223, 225, 231} {
		if !strings.Contains(expr, strconv.Itoa(code)) {
			t.Errorf("blocked code %d is missing from the outcome expression", code)
		}
	}

	// Order is meaning: a user abort must be decided before the blocked list is
	// consulted, or a code appearing in both would silently become blocked.
	aborted := strings.LastIndex(expr, "'aborted'")
	blocked := strings.Index(expr, "'blocked'")
	if aborted < 0 || blocked < 0 || aborted > blocked {
		t.Errorf("the aborted branch must precede the blocked one (aborted at %d, blocked at %d)",
			aborted, blocked)
	}
}

// A code in both lists would be decided by branch order rather than by
// intent, which is the kind of thing that is obvious now and baffling later.
func TestUserAbortAndBlockedCodesAreDisjoint(t *testing.T) {
	abort := map[int]bool{}
	for _, c := range userAbortCodes {
		abort[c] = true
	}
	for _, c := range blockedExitCodes {
		if abort[c] {
			t.Errorf("code %d is both a user abort and a blocked code", c)
		}
	}
}

// 1 is bash's exit status for any ordinary command failure, so it has to stay
// a plain failure. If it ever reached either list, most real script failures
// would quietly stop counting against the success rate.
func TestOrdinaryFailureCodesStayFailures(t *testing.T) {
	for _, c := range append(append([]int{}, userAbortCodes...), blockedExitCodes...) {
		switch c {
		case 0, 1, 2, 100, 255:
			t.Errorf("code %d must stay a plain failure, not be reclassified", c)
		}
	}
}

func TestSQLIntListRendersForAnInClause(t *testing.T) {
	if got := sqlIntList([]int{1, 22, 333}); got != "1,22,333" {
		t.Errorf("sqlIntList = %q, want %q", got, "1,22,333")
	}
	if got := sqlIntList(nil); got != "" {
		t.Errorf("sqlIntList(nil) = %q, want empty", got)
	}
}

// A refresh replaces a panel, so it must replace that panel's warning too --
// and leave every other panel's alone.
func TestDropWarningsRemovesOnlyTheNamedSections(t *testing.T) {
	got := dropWarnings([]string{
		"daily: connection refused",
		"live: timeout",
		"top apps: syntax error",
		"live runs: timeout",
	}, "live", "live runs")

	want := []string{"daily: connection refused", "top apps: syntax error"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// "live" must not take "live runs" with it by prefix, and must not match a
// section that merely starts with the same letters.
func TestDropWarningsMatchesWholeSectionNames(t *testing.T) {
	got := dropWarnings([]string{"live runs: boom", "liveliness: boom"}, "live")
	if len(got) != 2 {
		t.Errorf("got %v, want both entries kept: %q is not the %q section",
			got, "live runs", "live")
	}
}

// Every filter this page accepts has to reach the key. If one does not, the
// cache serves a page built for different filters -- which reads as wrong data,
// not as a stale cache.
func TestNewDashCacheKeyDistinguishesEveryFilter(t *testing.T) {
	base := newDashCacheKey(7, "ProxmoxVE", "", "", "")

	for _, c := range []struct {
		what string
		key  string
	}{
		{"days", newDashCacheKey(30, "ProxmoxVE", "", "", "")},
		{"repo source", newDashCacheKey(7, "ProxmoxVED", "", "", "")},
		{"repo slug", newDashCacheKey(7, "ProxmoxVE", "some/repo", "", "")},
		{"platform", newDashCacheKey(7, "ProxmoxVE", "", "incus", "")},
		{"ctype", newDashCacheKey(7, "ProxmoxVE", "", "", "lxc")},
	} {
		if c.key == base {
			t.Errorf("%s does not change the cache key (%q)", c.what, c.key)
		}
	}

	// The two filters this page added on top of the shared helper must not
	// collide with each other either.
	if a, b := newDashCacheKey(7, "", "", "lxc", ""), newDashCacheKey(7, "", "", "", "lxc"); a == b {
		t.Errorf("platform=lxc and ctype=lxc share the key %q", a)
	}
}

// A cancelled request must not render as a page that is merely empty. Whether a
// section was dropped before it took a slot or failed once it had one, it says
// so.
func TestRunSectionsReportsEverySectionWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const n = 12
	sections := make([]dashSection, 0, n)
	for i := range n {
		sections = append(sections, dashSection{
			name: fmt.Sprintf("s%02d", i),
			run:  func(ctx context.Context) error { return ctx.Err() },
		})
	}

	var mu sync.Mutex
	failed := map[string]bool{}

	runSections(ctx, dashSectionLimit, sections, func(name string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		failed[name] = true
		mu.Unlock()
	})

	if len(failed) != n {
		t.Errorf("%d of %d sections reported; a cancelled load must name them all",
			len(failed), n)
	}
}

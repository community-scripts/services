package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

func parallel(fns ...func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

func (ch *CHClient) stuckAfter() int {
	if ch.stuckHours > 0 {
		return ch.stuckHours
	}
	return 4
}

func byLabel(rows []BreakdownRow) map[string]BreakdownRow {
	m := make(map[string]BreakdownRow, len(rows))
	for _, r := range rows {
		m[r.Label] = r
	}
	return m
}

// FetchDashboardData builds the dashboard's figures, every one of them per run.
func (ch *CHClient) FetchDashboardData(ctx context.Context, f RunFilter) (*DashboardData, error) {
	dims := []breakdownDim{dimAll, dimApp, dimOS, dimMethod, dimPVE, dimGPU, dimPlatform,
		dimCategory, dimType, dimSlug, dimTool, dimAddon}
	if f.Days > 1 {
		dims = append(dims, dimDay)
	}
	var (
		b               Breakdown
		bErr            error
		allTime         uint64
		stuck           StuckRuns
		recent          []RunRecord
		stuckErr, rcErr error
	)
	parallel(
		func() { b, bErr = ch.fetchBreakdown(ctx, f, dims) },
		func() {
			_ = ch.db.QueryRowContext(ctx, "SELECT uniq("+runKeyExpr+") FROM telemetry_db.telemetry").Scan(&allTime)
		},
		func() { stuck, stuckErr = ch.FetchStuckRuns(ctx, f, ch.stuckAfter()) },
		func() {
			rf := f
			if rf.Type == "" {
				rf.Type = "all"
			}
			recent, _, rcErr = ch.FetchRuns(ctx, rf, 1, 20, "-created")
		},
	)
	if bErr != nil {
		return nil, bErr
	}
	if stuckErr != nil {
		log.Printf("[CH] dashboard stuck runs: %v", stuckErr)
	}
	if rcErr != nil {
		log.Printf("[CH] dashboard recent runs: %v", rcErr)
	}

	s := summaryFrom(b.one("all"))
	minRuns := minRunsForRate(f.Days)
	data := &DashboardData{
		TotalInstalls: s.Runs, SampleSize: s.Runs, TotalAllTime: int(allTime),
		SuccessCount: s.Success, FailedCount: s.Failed, AbortedCount: s.Aborted,
		InstallingCount: stuck.Running + stuck.Stalled, InstallingWindowDays: 1,
		SuccessRate: s.SuccessRate, AvgInstallDuration: s.AvgDuration,
		RecentRecords: recent,
		Days:          f.Days, Summary: s, MinRuns: minRuns,
		Apps: b.top("app", 15),
		Breakdown: map[string][]BreakdownRow{
			"platform": b.top("platform", 6), "type": b.top("type", 8), "os": b.top("os", 6),
		},
	}
	for _, r := range b.top("app", 20) {
		data.TopApps = append(data.TopApps, AppCount{App: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("os", 15) {
		data.OsDistribution = append(data.OsDistribution, OsCount{Os: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("method", 10) {
		data.MethodStats = append(data.MethodStats, MethodCount{Method: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("pve", 15) {
		data.PveVersions = append(data.PveVersions, PveCount{Version: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("type", 10) {
		data.TypeStats = append(data.TypeStats, TypeCount{Type: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("platform", 0) {
		data.PlatformStats = append(data.PlatformStats, PlatformCount{Platform: r.Label, Count: r.Runs})
	}
	for _, r := range b.top("gpu", 0) {
		vendor, pass, _ := strings.Cut(r.Label, "|")
		data.GPUStats = append(data.GPUStats, GPUCount{Vendor: vendor, Passthrough: pass, Count: r.Runs})
	}
	for _, r := range b.top("category", 0) {
		data.ErrorCategories = append(data.ErrorCategories, ErrorCatCount{Category: r.Label, Count: r.Runs})
	}
	for i, r := range b.top("tool", 0) {
		if i < 15 {
			data.TopTools = append(data.TopTools, ToolCount{Tool: r.Label, Count: r.Runs})
		}
		data.TotalTools += r.Runs
	}
	for i, r := range b.top("addon", 0) {
		if i < 15 {
			data.TopAddons = append(data.TopAddons, AddonCount{Addon: r.Label, Count: r.Runs})
		}
		data.TotalAddons += r.Runs
	}
	if f.RepoSlug == "" {
		for _, r := range b.top("slug", 20) {
			data.RepoSlugs = append(data.RepoSlugs, RepoSlugCount{Slug: r.Label, Count: r.Runs})
		}
	}
	total, failed := map[string]int{}, map[string]int{}
	for _, r := range b["app"] {
		key := r.Label + "|"
		total[key], failed[key] = r.Success+r.Failed, r.Failed
	}
	data.FailedApps = buildFailedApps(total, failed, 16, minRuns)

	days := f.Days
	if days <= 0 || days > 365 {
		days = 365
	}
	if f.Days > 1 {
		data.DailyStats = buildDailyStats(byLabel(b["day"]), days, time.Now())
	} else {
		data.DailyStats = buildDailyStats(map[string]BreakdownRow{time.Now().UTC().Format("2006-01-02"): b.one("all")}, 1, time.Now())
	}
	return data, nil
}

// FetchDashboardPage adds the error analysis panels to the dashboard figures.
func (ch *CHClient) FetchDashboardPage(ctx context.Context, f RunFilter) (*DashboardData, error) {
	var (
		data         *DashboardData
		err          error
		causes       []Cause
		apps         []AppErrorStat
		cErr, appErr error
	)
	parallel(
		func() { data, err = ch.FetchDashboardData(ctx, f) },
		func() { causes, cErr = ch.FetchCauses(ctx, f, 5, false) },
		func() { apps, appErr = ch.FetchAppStats(ctx, f, true, 300) },
	)
	if err != nil {
		return nil, err
	}
	if cErr != nil {
		log.Printf("[CH] dashboard causes: %v", cErr)
	}
	if appErr != nil {
		log.Printf("[CH] dashboard failing apps: %v", appErr)
	}
	data.TopCauses = causes
	data.FailingApps = rankFailureRates(apps, data.MinRuns, 8)
	for _, c := range causes {
		data.ErrorAnalysis = append(data.ErrorAnalysis, ErrorGroup{
			Pattern: c.Message, Count: c.Failures, UniqueApps: c.Apps, Apps: strings.Join(c.TopApps, ", "),
		})
	}
	return data, nil
}

// FetchScriptStats lists every script with per-run counts and its last failure.
func (ch *CHClient) FetchScriptStats(ctx context.Context, days int, repoSource, platform string, knownScripts map[string]ScriptInfo) (*ScriptAnalysisData, error) {
	f := RunFilter{Days: days, RepoSource: repoSource, Platform: platform, Type: "all"}
	apps, err := ch.FetchAppStats(ctx, f, false, 0)
	if err != nil {
		return nil, fmt.Errorf("CH script stats: %w", err)
	}

	var failIDs []string
	for _, a := range apps {
		if a.LastFailureID != "" {
			failIDs = append(failIDs, a.LastFailureID)
		}
	}
	causeByID := map[string]string{}
	if len(failIDs) > 0 {
		recs, err := ch.fetchRunDetails(ctx, failIDs, f.rowFloor(), false)
		if err != nil {
			log.Printf("[CH] script stats last failures: %v", err)
		}
		for _, r := range recs {
			causeByID[runKeyOf(r)] = runHeadline(r)
		}
	}

	data := &ScriptAnalysisData{}
	now := time.Now()
	seen := make(map[string]bool)
	for _, a := range apps {
		if len(knownScripts) > 0 {
			if _, ok := knownScripts[a.App]; !ok {
				continue
			}
		}
		seen[a.App] = true
		st := ScriptStat{
			App: a.App, Type: a.Type,
			Total: a.Runs, Success: a.Success, Failed: a.Failed, Aborted: a.Aborted,
			Installing: a.InProgress, NoResult: a.NoResult, FailureRate: a.FailureRate,
			LastFailure: a.LastFailure, LastFailureID: a.LastFailureID,
			LastFailureCause: causeByID[a.LastFailureID],
		}
		if a.Success+a.Failed > 0 {
			st.SuccessRate = 100 - a.FailureRate
		}
		if st.LastFailureCause == "" && a.TopCauseText != "" {
			st.LastFailureCause = a.TopCauseText
		}
		if days > 0 {
			st.InstallsPerDay = float64(a.Runs) / float64(days)
		}
		if info, ok := knownScripts[a.App]; ok {
			st.Type = info.Type
			st.DaysOld = daysSince(info.Created, now)
		}
		data.TopScripts = append(data.TopScripts, st)
		data.TotalInstalls += a.Runs
	}
	if knownScripts != nil && (days == 0 || days >= 30) {
		for slug, info := range knownScripts {
			if !seen[slug] {
				data.TopScripts = append(data.TopScripts, ScriptStat{App: slug, Type: info.Type, DaysOld: daysSince(info.Created, now)})
			}
		}
	}
	data.TotalScripts = len(data.TopScripts)
	data.RecentScripts = ch.fetchRecentScripts(ctx, days, repoSource, platform)
	return data, nil
}

func daysSince(t, now time.Time) int {
	if t.IsZero() {
		return 0
	}
	if d := int(now.Sub(t).Hours() / 24); d > 1 {
		return d
	}
	return 1
}

func runKeyOf(r RunRecord) string {
	if r.ExecutionID != "" {
		return r.ExecutionID
	}
	return r.RandomID
}

func runHeadline(r RunRecord) string {
	switch {
	case r.ErrorMessage != "":
		return r.ErrorMessage
	case r.FailedCommand != "" && r.CauseKind == "command":
		return r.FailedCommand
	case r.ExitCode != 0:
		return fmt.Sprintf("Exit %d: %s", r.ExitCode, getExitCodeDescription(r.ExitCode))
	}
	return ""
}

func (ch *CHClient) fetchRecentScripts(ctx context.Context, days int, repoSource, platform string) []RecentScript {
	recentDays := days
	if recentDays == 0 || recentDays > 30 {
		recentDays = 30
	}
	f := RunFilter{Days: recentDays, RepoSource: repoSource, Platform: platform, Type: "all",
		Status: ""}
	q, args := f.sql(runQuery{
		sel: `r_app, r_type, r_outcome, toInt64(r_exit), r_os, r_osver, r_pve, r_method, r_platform,
	toString(r_end)`,
		where: "r_app != '' AND r_outcome IN ('success','failed','aborted','unknown')",
		tail:  "\nORDER BY r_end DESC\nLIMIT 50",
	})
	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		log.Printf("[CH] recent scripts: %v", err)
		return nil
	}
	defer rows.Close()
	var out []RecentScript
	for rows.Next() {
		var rs RecentScript
		var ec int64
		if rows.Scan(&rs.App, &rs.Type, &rs.Status, &ec, &rs.OsType, &rs.OsVersion,
			&rs.PveVer, &rs.Method, &rs.Platform, &rs.Created) == nil {
			rs.ExitCode = int(ec)
			out = append(out, rs)
		}
	}
	return out
}

func (ch *CHClient) FetchErrorAnalysisData(ctx context.Context, f RunFilter) (*ErrorAnalysisData, error) {
	bucket := "day"
	dims := []breakdownDim{dimAll, dimExit, dimCategory, dimDay}
	if f.Days == 1 {
		bucket = "hour"
		dims = append(dims, dimHour)
	}
	var (
		b                   Breakdown
		causes              []Cause
		apps                []AppErrorStat
		stuck               StuckRuns
		recent              []RunRecord
		bErr, cErr, aErr    error
		stuckErr, recentErr error
		recentFilter        = f
	)
	recentFilter.Status = outcomeFailed
	parallel(
		func() { b, bErr = ch.fetchBreakdown(ctx, f, dims) },
		func() { causes, cErr = ch.FetchCauses(ctx, f, 50, false) },
		func() { apps, aErr = ch.FetchAppStats(ctx, f, true, 300) },
		func() { stuck, stuckErr = ch.FetchStuckRuns(ctx, f, ch.stuckAfter()) },
		func() { recent, _, recentErr = ch.FetchRuns(ctx, recentFilter, 1, 100, "-created") },
	)
	for name, err := range map[string]error{"breakdown": bErr, "causes": cErr, "apps": aErr} {
		if err != nil {
			return nil, fmt.Errorf("error analysis %s: %w", name, err)
		}
	}
	if stuckErr != nil {
		log.Printf("[CH] error analysis stuck runs: %v", stuckErr)
	}
	if recentErr != nil {
		log.Printf("[CH] error analysis recent failures: %v", recentErr)
	}

	s := summaryFrom(b.one("all"))
	data := &ErrorAnalysisData{
		TotalErrors: s.Failed, TotalInstalls: s.Runs, OverallFailRate: s.FailureRate,
		StuckInstalling: stuck.Running + stuck.Stalled,
		Days:            f.Days, Summary: s, Causes: causes, Apps: apps, Stuck: stuck,
		Bucket: bucket, MinRuns: minRunsForRate(f.Days),
	}

	for _, r := range b.top("exit", 30) {
		code, _ := strconv.Atoi(r.Label)
		pct := 0.0
		if s.Failed > 0 {
			pct = float64(r.Runs) / float64(s.Failed) * 100
		}
		data.ExitCodeStats = append(data.ExitCodeStats, ExitCodeStat{
			ExitCode: code, Count: r.Runs, Percentage: pct, Apps: r.FailedApps,
			Description: getExitCodeDescription(code), Category: getExitCodeCategory(code),
		})
	}
	for _, r := range b.top("category", 0) {
		pct := 0.0
		if s.Failed > 0 {
			pct = float64(r.Runs) / float64(s.Failed) * 100
		}
		data.CategoryStats = append(data.CategoryStats, CategoryStat{
			Category: r.Label, Count: r.Runs, Percentage: pct, TopApps: r.TopApps,
		})
	}

	days := f.Days
	if days <= 0 || days > 365 {
		days = 365
	}
	now := time.Now().UTC()
	byDay := byLabel(b["day"])
	for _, d := range buildDailyStats(byDay, days, now) {
		data.ErrorTimeline = append(data.ErrorTimeline, ErrorTimelinePoint{Date: d.Date, Failed: d.Failed, Aborted: d.Aborted})
	}
	if bucket == "hour" {
		byHour := byLabel(b["hour"])
		start := now.Truncate(24 * time.Hour)
		for h := start; !h.After(now); h = h.Add(time.Hour) {
			r := byHour[h.Format("2006-01-02 15:04:05")]
			data.FailuresOverTime = append(data.FailuresOverTime, TimePoint{T: h.Format(time.RFC3339), Runs: r.Runs, Failed: r.Failed, Aborted: r.Aborted})
		}
	} else {
		for _, d := range buildDailyStats(byDay, days, now) {
			data.FailuresOverTime = append(data.FailuresOverTime, TimePoint{T: d.Date, Runs: d.Runs, Failed: d.Failed, Aborted: d.Aborted})
		}
	}

	for _, c := range causes {
		cmd := c.Key
		if c.Kind == "exit_code" {
			cmd = c.Message
		}
		data.Signatures = append(data.Signatures, ErrorSignature{
			Kind: c.Kind, Command: cmd, Count: c.Failures, TopExitCode: c.ExitCode,
			UniqueApps: c.Apps, Apps: strings.Join(c.TopApps, ", "),
		})
	}

	for _, r := range recent {
		data.RecentErrors = append(data.RecentErrors, ErrorRecord{
			NSAPP: r.NSAPP, Type: r.Type, Status: r.Status, ExitCode: r.ExitCode, Error: r.Error,
			ErrorCategory: r.ErrorCategory, OsType: r.OsType, OsVersion: r.OsVersion, PveVer: r.PveVer,
			Platform: r.Platform, ExecutionID: runKeyOf(r), InstallDuration: r.Duration, Created: r.Created,
			ErrorMessage: r.ErrorMessage, ErrorStep: r.ErrorStep, ErrorPhase: r.ErrorPhase,
			FailedFunction: r.FailedFunction,
		})
	}

	legacy := apps
	if len(legacy) > 50 {
		legacy = legacy[:50]
	}
	var ids []string
	for _, a := range legacy {
		if a.LastFailureID != "" {
			ids = append(ids, a.LastFailureID)
		}
	}
	traces := map[string]RunRecord{}
	if recs, err := ch.fetchRunDetails(ctx, ids, f.rowFloor(), false); err == nil {
		for _, r := range recs {
			traces[runKeyOf(r)] = r
		}
	} else {
		log.Printf("[CH] error analysis app traces: %v", err)
	}
	for _, a := range legacy {
		last := traces[a.LastFailureID]
		msg := a.TopCauseText
		if a.TopCauseKind != "message" && last.ErrorMessage != "" {
			msg = last.ErrorMessage
		}
		data.AppErrors = append(data.AppErrors, AppErrorDetail{
			App: a.App, Type: a.Type, TotalCount: a.Runs, FailedCount: a.Failed, AbortedCount: a.Aborted,
			FailureRate: a.FailureRate, TopExitCode: a.TopExitCode, TopError: last.Error,
			TopMessage: msg, TopCategory: a.TopCategory,
		})
	}
	return data, nil
}

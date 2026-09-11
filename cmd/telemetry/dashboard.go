package main

import (
	"strings"
	"time"
)

// This file holds the shared data types returned by the ClickHouse-backed API
// (see clickhouse.go) plus a couple of pure aggregation helpers. The former
// PocketBase data layer (PBClient / ScriptStatsStore and their fetchers) was
// removed once all runtime paths moved to ClickHouse.

// DashboardData holds aggregated statistics for the dashboard
type DashboardData struct {
	TotalInstalls   int `json:"total_installs"`
	TotalAllTime    int `json:"total_all_time"` // Total records in DB (not limited)
	SampleSize      int `json:"sample_size"`    // How many records were sampled
	SuccessCount    int `json:"success_count"`
	FailedCount     int `json:"failed_count"`
	AbortedCount    int `json:"aborted_count"`
	InstallingCount int `json:"installing_count"`
	// Window InstallingCount actually covers, in days. Fixed at 1 rather than
	// following the selected period, because "still installing" is a question
	// about now. Reported so a page can label it, instead of showing it beside
	// figures that do follow the selector as though they matched.
	InstallingWindowDays int               `json:"installing_window_days"`
	SuccessRate          float64           `json:"success_rate"`
	TopApps              []AppCount        `json:"top_apps"`
	OsDistribution       []OsCount         `json:"os_distribution"`
	MethodStats          []MethodCount     `json:"method_stats"`
	PveVersions          []PveCount        `json:"pve_versions"`
	TypeStats            []TypeCount       `json:"type_stats"`
	ErrorAnalysis        []ErrorGroup      `json:"error_analysis"`
	FailedApps           []AppFailure      `json:"failed_apps"`
	RecentRecords        []TelemetryRecord `json:"recent_records"`
	DailyStats           []DailyStat       `json:"daily_stats"`

	// Extended metrics
	GPUStats           []GPUCount      `json:"gpu_stats"`
	ErrorCategories    []ErrorCatCount `json:"error_categories"`
	TopTools           []ToolCount     `json:"top_tools"`
	TopAddons          []AddonCount    `json:"top_addons"`
	AvgInstallDuration float64         `json:"avg_install_duration"` // seconds
	TotalTools         int             `json:"total_tools"`
	TotalAddons        int             `json:"total_addons"`
	RepoSlugs          []RepoSlugCount `json:"repo_slugs"`
	PlatformStats      []PlatformCount `json:"platform_stats"` // pve vs incus distribution
}

// PlatformCount holds per-platform (pve/incus) installation counts.
type PlatformCount struct {
	Platform string `json:"platform"`
	Count    int    `json:"count"`
}

type AppCount struct {
	App   string `json:"app"`
	Count int    `json:"count"`
}

type OsCount struct {
	Os    string `json:"os"`
	Count int    `json:"count"`
}

type MethodCount struct {
	Method string `json:"method"`
	Count  int    `json:"count"`
}

type PveCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

type TypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type ErrorGroup struct {
	Pattern    string `json:"pattern"`
	Count      int    `json:"count"`       // Total error occurrences
	UniqueApps int    `json:"unique_apps"` // Number of unique apps affected
	Apps       string `json:"apps"`        // Comma-separated list of affected apps
}

type AppFailure struct {
	App         string  `json:"app"`
	Type        string  `json:"type"`
	TotalCount  int     `json:"total_count"`
	FailedCount int     `json:"failed_count"`
	FailureRate float64 `json:"failure_rate"`
}

type DailyStat struct {
	Date    string `json:"date"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`
}

// Extended metric types
type GPUCount struct {
	Vendor      string `json:"vendor"`
	Passthrough string `json:"passthrough"`
	Count       int    `json:"count"`
}

type ErrorCatCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type ToolCount struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

type AddonCount struct {
	Addon string `json:"addon"`
	Count int    `json:"count"`
}

type RepoSlugCount struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// ========================================================
// Error Analysis Data Types
// ========================================================

// ErrorAnalysisData holds comprehensive error analysis
type ErrorAnalysisData struct {
	TotalErrors     int                  `json:"total_errors"`
	TotalInstalls   int                  `json:"total_installs"`
	OverallFailRate float64              `json:"overall_fail_rate"`
	ExitCodeStats   []ExitCodeStat       `json:"exit_code_stats"`
	CategoryStats   []CategoryStat       `json:"category_stats"`
	AppErrors       []AppErrorDetail     `json:"app_errors"`
	RecentErrors    []ErrorRecord        `json:"recent_errors"`
	StuckInstalling int                  `json:"stuck_installing"`
	ErrorTimeline   []ErrorTimelinePoint `json:"error_timeline"`
	Signatures      []ErrorSignature     `json:"signatures"` // grouped by failing command
}

// ErrorSignature groups failures by the exact command that failed (extracted
// from the structured error header "… | at line L: <command>").
type ErrorSignature struct {
	Command     string `json:"command"`
	Count       int    `json:"count"`
	TopExitCode int    `json:"top_exit_code"`
	UniqueApps  int    `json:"unique_apps"`
	Apps        string `json:"apps"`
}

type ExitCodeStat struct {
	ExitCode    int     `json:"exit_code"`
	Count       int     `json:"count"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Percentage  float64 `json:"percentage"`
}

type CategoryStat struct {
	Category   string  `json:"category"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
	TopApps    string  `json:"top_apps"`
}

type AppErrorDetail struct {
	App          string  `json:"app"`
	Type         string  `json:"type"`
	TotalCount   int     `json:"total_count"`
	FailedCount  int     `json:"failed_count"`
	AbortedCount int     `json:"aborted_count"`
	FailureRate  float64 `json:"failure_rate"`
	TopExitCode  int     `json:"top_exit_code"`
	TopError     string  `json:"top_error"`
	TopCategory  string  `json:"top_category"`
}

type ErrorRecord struct {
	NSAPP           string `json:"nsapp"`
	Type            string `json:"type"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	Error           string `json:"error"`
	ErrorCategory   string `json:"error_category"`
	OsType          string `json:"os_type"`
	OsVersion       string `json:"os_version"`
	PveVer          string `json:"pve_version,omitempty"`
	Platform        string `json:"platform,omitempty"` // "pve" | "incus" | ""
	ExecutionID     string `json:"execution_id,omitempty"`
	InstallDuration int    `json:"install_duration,omitempty"`
	Created         string `json:"created"`
}

type ErrorTimelinePoint struct {
	Date    string `json:"date"`
	Failed  int    `json:"failed"`
	Aborted int    `json:"aborted"`
}

// ========================================================
// Script Analysis Data Types
// ========================================================

// ScriptInfo holds slug, type, and creation date for known scripts
type ScriptInfo struct {
	Slug    string
	Type    string // "ct", "vm", "pve", "addon", "turnkey"
	Created time.Time
}

type ScriptAnalysisData struct {
	TotalScripts  int            `json:"total_scripts"`
	TotalInstalls int            `json:"total_installs"`
	TopScripts    []ScriptStat   `json:"top_scripts"`
	RecentScripts []RecentScript `json:"recent_scripts"`
}

type ScriptStat struct {
	App            string  `json:"app"`
	Type           string  `json:"type"`
	Total          int     `json:"total"`
	Success        int     `json:"success"`
	Failed         int     `json:"failed"`
	Aborted        int     `json:"aborted"`
	Installing     int     `json:"installing"`
	SuccessRate    float64 `json:"success_rate"`
	DaysOld        int     `json:"days_old"`
	InstallsPerDay float64 `json:"installs_per_day"`
}

type RecentScript struct {
	App       string `json:"app"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	OsType    string `json:"os_type"`
	OsVersion string `json:"os_version"`
	PveVer    string `json:"pve_version"`
	Platform  string `json:"platform,omitempty"` // "pve" | "incus" | ""
	Created   string `json:"created"`
	Method    string `json:"method"`
}

// TelemetryRecord is a single telemetry row as returned to API clients.
type TelemetryRecord struct {
	TelemetryOut
	Created string `json:"created"`
}

// ========================================================
// Per-Script Detail (daily drill-down)
// ========================================================

// ScriptDetailData is the per-script drill-down: daily series, error
// breakdown, OS/platform distribution, and recent installations.
type ScriptDetailData struct {
	App            string            `json:"app"`
	Type           string            `json:"type"`
	Total          int               `json:"total"`
	Success        int               `json:"success"`
	Failed         int               `json:"failed"`
	Aborted        int               `json:"aborted"`
	SuccessRate    float64           `json:"success_rate"`
	AvgDuration    float64           `json:"avg_duration"` // seconds, successful installs
	Daily          []ScriptDailyStat `json:"daily"`
	ExitCodes      []ExitCodeStat    `json:"exit_codes"`
	OsDistribution []OsCount         `json:"os_distribution"`
	PlatformStats  []PlatformCount   `json:"platform_stats"`
	Recent         []TelemetryRecord `json:"recent"`
}

// ScriptDailyStat is one day of installs for a single script.
type ScriptDailyStat struct {
	Date    string `json:"date"`
	Total   int    `json:"total"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`
	Aborted int    `json:"aborted"`
}

// ========================================================
// Aggregation helpers (used by the ClickHouse dashboard build)
// ========================================================

// buildFailedApps ranks apps by failure rate, balancing LXC and VM entries so
// one type can't crowd the other out of the top-N list.
func buildFailedApps(total, failed map[string]int, n int, minInstalls int) []AppFailure {
	lxcApps := make([]AppFailure, 0)
	vmApps := make([]AppFailure, 0)

	for key, failCount := range failed {
		totalCount := total[key]
		if totalCount < minInstalls {
			continue // Skip apps with too few installations
		}

		// Parse composite key "app|type"
		parts := strings.SplitN(key, "|", 2)
		app := parts[0]
		appType := ""
		if len(parts) > 1 {
			appType = parts[1]
		}

		rate := float64(failCount) / float64(totalCount) * 100
		failure := AppFailure{
			App:         app,
			Type:        appType,
			TotalCount:  totalCount,
			FailedCount: failCount,
			FailureRate: rate,
		}

		// Separate LXC and VM apps (LXC has higher priority)
		if strings.ToLower(appType) == "lxc" {
			lxcApps = append(lxcApps, failure)
		} else {
			vmApps = append(vmApps, failure)
		}
	}

	// Sort each list by failure rate descending
	sortByFailureRate := func(apps []AppFailure) {
		for i := 0; i < len(apps)-1; i++ {
			for j := i + 1; j < len(apps); j++ {
				if apps[j].FailureRate > apps[i].FailureRate {
					apps[i], apps[j] = apps[j], apps[i]
				}
			}
		}
	}
	sortByFailureRate(lxcApps)
	sortByFailureRate(vmApps)

	// Balance: take equal numbers from each, LXC first
	// n/2 from each type, with LXC getting any extra slot
	perType := n / 2
	extra := n % 2 // Extra slot goes to LXC

	result := make([]AppFailure, 0, n)

	// Take LXC apps first (higher priority)
	lxcCount := perType + extra
	if lxcCount > len(lxcApps) {
		lxcCount = len(lxcApps)
	}
	result = append(result, lxcApps[:lxcCount]...)

	// Take VM apps
	vmCount := perType
	if vmCount > len(vmApps) {
		vmCount = len(vmApps)
	}
	result = append(result, vmApps[:vmCount]...)

	// Fill remaining slots if one type had fewer
	remaining := n - len(result)
	if remaining > 0 {
		// Try to fill with more LXC apps
		extraLxc := len(lxcApps) - lxcCount
		if extraLxc > remaining {
			extraLxc = remaining
		}
		if extraLxc > 0 {
			result = append(result, lxcApps[lxcCount:lxcCount+extraLxc]...)
			remaining -= extraLxc
		}
		// Fill with more VM apps if still slots available
		if remaining > 0 {
			extraVm := len(vmApps) - vmCount
			if extraVm > remaining {
				extraVm = remaining
			}
			if extraVm > 0 {
				result = append(result, vmApps[vmCount:vmCount+extraVm]...)
			}
		}
	}

	return result
}

// buildDailyStats produces a dense per-day success/failed series for the last
// `days` days (filling gaps with zeros so the chart has no holes).
func buildDailyStats(success, failed map[string]int, days int) []DailyStat {
	result := make([]DailyStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		result = append(result, DailyStat{
			Date:    date,
			Success: success[date],
			Failed:  failed[date],
		})
	}
	return result
}

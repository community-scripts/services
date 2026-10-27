package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func parseDays(s string, def, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	switch {
	case err != nil:
		return def
	case n <= 0 || n > max:
		return max
	}
	return n
}

func cleanParam(q url.Values, key string) string {
	v := strings.TrimSpace(q.Get(key))
	if len(v) > 500 {
		v = v[:500]
	}
	return v
}

// defaultType applies when no type is given: "" for install types, or "all".
func parseRunFilter(q url.Values, defaultDays, maxDays int, defaultType string) (RunFilter, error) {
	f := RunFilter{
		Days:        parseDays(q.Get("days"), defaultDays, maxDays),
		RepoSource:  cleanParam(q, "repo"),
		RepoSlug:    cleanParam(q, "slug"),
		App:         cleanParam(q, "app"),
		Search:      cleanParam(q, "search"),
		OS:          cleanParam(q, "os"),
		Category:    cleanParam(q, "category"),
		Message:     cleanParam(q, "message"),
		Cause:       cleanParam(q, "cause"),
		ExecutionID: cleanParam(q, "execution_id"),
		RandomID:    cleanParam(q, "random_id"),
	}
	if f.RepoSource == "all" {
		f.RepoSource = ""
	}
	switch p := strings.ToLower(cleanParam(q, "platform")); p {
	case "", "all":
	case "pve", "incus":
		f.Platform = p
	default:
		return f, fmt.Errorf("unknown platform %q: use pve, incus or all", p)
	}
	switch f.Type = strings.ToLower(cleanParam(q, "type")); f.Type {
	case "":
		f.Type = defaultType
	case "install":
		f.Type = ""
	}
	if !allowedRunType[f.Type] {
		return f, fmt.Errorf("unknown type %q: use lxc, vm, turnkey, pve, addon, tool, install or all", f.Type)
	}
	f.Status = strings.ToLower(cleanParam(q, "status"))
	if f.Status != "" && !allowedRunStatus[f.Status] {
		return f, fmt.Errorf("unknown status %q: use success, failed, aborted, unknown or in_progress", f.Status)
	}
	if s := cleanParam(q, "exit_code"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return f, fmt.Errorf("exit_code must be a number, got %q", s)
		}
		f.ExitCode = &n
	}
	return f, nil
}

func parseLimit(s string, def, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	switch {
	case err != nil:
		return def
	case n < 1:
		return 1
	case n > max:
		return max
	}
	return n
}

func dashboardTTL(days int) time.Duration {
	switch {
	case days <= 1:
		return 30 * time.Second
	case days <= 7:
		return 2 * time.Minute
	case days <= 30:
		return 5 * time.Minute
	case days <= 90:
		return 15 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func errorsTTL(days int) time.Duration {
	if days > 7 {
		return 23 * time.Hour
	}
	return 2 * time.Minute
}

// serveCachedJSON refreshes a stale entry in the background.
func serveCachedJSON[T any](w http.ResponseWriter, r *http.Request, c *Cache, enabled bool,
	key string, ttl, timeout time.Duration, build func(context.Context) (*T, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var data *T
	if enabled && c.Get(ctx, key, &data) && data != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		if c.IsStale(ctx, key) {
			w.Header().Set("X-Cache", "STALE")
			if c.TryStartRefresh(key) {
				go func() {
					defer c.FinishRefresh(key)
					bctx, bcancel := context.WithTimeout(context.Background(), timeout)
					defer bcancel()
					fresh, err := build(bctx)
					if err != nil {
						log.Printf("[CACHE] background refresh failed for %s: %v", key, err)
						return
					}
					_ = c.Set(context.Background(), key, fresh, ttl)
				}()
			}
		}
		_ = json.NewEncoder(w).Encode(data)
		return
	}
	data, err := build(ctx)
	if err != nil {
		log.Printf("%s: %v", key, err)
		http.Error(w, "could not query the telemetry database; try again in a minute", http.StatusInternalServerError)
		return
	}
	if enabled {
		_ = c.Set(ctx, key, data, ttl)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	_ = json.NewEncoder(w).Encode(data)
}

// CausesResponse is the body of /api/causes.
type CausesResponse struct {
	Days    int     `json:"days"`
	Causes  []Cause `json:"causes"`
	Updated string  `json:"updated"`
}

// AppErrorsResponse is the body of /api/app-errors.
type AppErrorsResponse struct {
	Days    int            `json:"days"`
	MinRuns int            `json:"min_runs"`
	Apps    []AppErrorStat `json:"apps"`
	Updated string         `json:"updated"`
}

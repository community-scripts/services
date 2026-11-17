package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// githubAPI is where issues are read and created; tests point it at a fake.
var githubAPI = "https://api.github.com"

// issueLabel marks every issue the dashboard creates and is what duplicates are looked up by.
const issueLabel = "telemetry"

// newIssueLimiter allows n new issues an hour for the whole service.
func newIssueLimiter(n int) *RateLimiter {
	rl := NewRateLimiter(n, n)
	rl.mu.Lock()
	rl.window = time.Hour
	rl.mu.Unlock()
	return rl
}

func withIssueLabel(labels []string) []string {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), issueLabel) {
			return labels
		}
	}
	return append(labels, issueLabel)
}

// findOpenIssue returns the URL of an open issue with the telemetry label and this title, or "".
func findOpenIssue(token, owner, repo, title string) (string, error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/issues?state=open&labels=%s&per_page=100", githubAPI, owner, repo, issueLabel), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API error %d", resp.StatusCode)
	}
	var issues []struct {
		Title       string           `json:"title"`
		HTMLURL     string           `json:"html_url"`
		PullRequest *json.RawMessage `json:"pull_request"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return "", err
	}
	for _, i := range issues {
		if i.PullRequest == nil && strings.EqualFold(strings.TrimSpace(i.Title), strings.TrimSpace(title)) {
			return i.HTMLURL, nil
		}
	}
	return "", nil
}

// issueHandler serves POST /api/github/create-issue. The admin password in the body may create
// an issue, and so may a request that reads traces, since it can already see what it would post.
func issueHandler(cfg Config, readsTraces func(*http.Request) bool, rl *RateLimiter) http.HandlerFunc {
	answer := func(w http.ResponseWriter, status int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			answer(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		reader := readsTraces(r)
		if !reader && cfg.AdminPassword == "" {
			answer(w, http.StatusServiceUnavailable, map[string]string{"error": "admin password not configured"})
			return
		}

		var body struct {
			Password    string   `json:"password"`
			Title       string   `json:"title"`
			Body        string   `json:"body"`
			Labels      []string `json:"labels"`
			AppName     string   `json:"app_name"`
			ExitCode    int      `json:"exit_code"`
			ErrorText   string   `json:"error_text"`
			FailureRate float64  `json:"failure_rate"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			answer(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if !reader && body.Password != cfg.AdminPassword {
			answer(w, http.StatusUnauthorized, map[string]string{"error": "incorrect password"})
			return
		}
		if cfg.GitHubToken == "" {
			answer(w, http.StatusServiceUnavailable, map[string]string{"error": "GitHub token not configured on server"})
			return
		}

		if body.Body == "" {
			body.Body = fmt.Sprintf("## Telemetry Error Report\n\n"+
				"**Application:** %s\n"+
				"**Exit Code:** %d\n"+
				"**Error Category:** %s\n"+
				"**Failure Rate:** %.1f%%\n\n"+
				"### Error Details\n```\n%s\n```\n\n"+
				"---\n*This issue was automatically created from the telemetry error analysis dashboard.*",
				body.AppName, body.ExitCode,
				categorizeExitCode(body.ExitCode),
				body.FailureRate, body.ErrorText)
		}
		if body.Title == "" {
			body.Title = fmt.Sprintf("[Telemetry] %s: %s (exit code %d)",
				body.AppName, categorizeExitCode(body.ExitCode), body.ExitCode)
		}
		if len(body.Labels) == 0 {
			body.Labels = []string{"bug"}
		}
		body.Labels = withIssueLabel(body.Labels)

		// A failed lookup does not block the issue; a duplicate is cheaper than a lost report.
		if dup, err := findOpenIssue(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo, body.Title); err != nil {
			log.Printf("WARN: could not look for an existing issue: %v", err)
		} else if dup != "" {
			answer(w, http.StatusConflict, map[string]string{"error": "an open issue with this title already exists", "issue_url": dup})
			return
		}
		if !rl.Allow("issues") {
			answer(w, http.StatusTooManyRequests, map[string]string{"error": "too many issues were created in the last hour; try again later"})
			return
		}

		issueURL, err := createGitHubIssue(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo, body.Title, body.Body, body.Labels)
		if err != nil {
			log.Printf("ERROR: failed to create GitHub issue: %v", err)
			answer(w, http.StatusInternalServerError, map[string]string{"error": "failed to create issue: " + err.Error()})
			return
		}
		log.Printf("[ISSUE] created %s", issueURL)
		answer(w, http.StatusOK, map[string]interface{}{"success": true, "issue_url": issueURL})
	}
}

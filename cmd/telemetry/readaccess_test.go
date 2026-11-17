package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clientText is what the fixtures' clients sent in error, error_message, error_step and failed_command.
var clientText = []string{"npm ERR!", "$STD", "Building Immich", "Installing Docker", "Unable to locate package",
	"curl: command not found", "is not signed", "apt-get"}

func TestAnswersWithoutTheTokenCarryNoClientText(t *testing.T) {
	for _, traces := range []bool{false, true} {
		dir := t.TempDir()
		writeFixtures(t, dir, traces)
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			all.Write(b)
		}
		for _, s := range clientText {
			if strings.Contains(all.String(), s) != traces {
				t.Errorf("traces=%v: %q present=%v", traces, s, !traces)
			}
		}
	}
}

func TestWithoutTracesCausesAreExitCodes(t *testing.T) {
	ch, _ := newFakeCH(t)
	ctx := context.Background()
	causes, err := ch.FetchCauses(ctx, RunFilter{Days: 7}, 20, true)
	if err != nil || len(causes) == 0 {
		t.Fatalf("%v %+v", err, causes)
	}
	for _, c := range causes {
		if c.Kind != "exit_code" || !strings.HasPrefix(c.Key, "exit ") || !strings.HasPrefix(c.Message, "Exit ") || c.ExampleMessage != "" {
			t.Errorf("cause %+v", c)
		}
		for _, r := range c.ExampleRuns {
			if !r.TracesWithheld || r.Error != "" || r.FailedCommand != "" || r.ErrorMessage != "" || r.ErrorStep != "" {
				t.Errorf("example run %+v", r)
			}
		}
	}
	recs, total, err := ch.FetchRuns(ctx, RunFilter{Days: 7, Status: "failed", Cause: "exit 239"}, 1, 50, "")
	if err != nil || total != 3 || recs[0].Cause != "exit 239" || recs[0].FailedFunction != "setup_nodejs" {
		t.Errorf("drill-down by a withheld cause: %v %d %+v", err, total, recs)
	}
}

func TestReadGuard(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := readGuard(ok, NewRateLimiter(3, 3), nil, "s3cret")
	get := func(h http.Handler, ip, path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = ip + ":40000"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/records", "/api/errors", "/metrics"} {
		if rec := get(h, "203.0.113.7", path, ""); rec.Code != http.StatusOK || rec.Header().Get("Vary") != "Authorization" {
			t.Errorf("%s: %d, Vary %q", path, rec.Code, rec.Header().Get("Vary"))
		}
	}
	if c := get(h, "203.0.113.7", "/api/causes", "").Code; c != http.StatusTooManyRequests {
		t.Errorf("fourth read in the minute: %d", c)
	}
	if c := get(h, "203.0.113.7", "/api/records", "Bearer s3cret").Code; c != http.StatusOK {
		t.Errorf("the admin token is limited: %d", c)
	}
	if c := get(h, "203.0.113.7", "/error-analysis", "").Code; c != http.StatusOK {
		t.Errorf("pages are limited: %d", c)
	}
	if c := get(h, "198.51.100.2", "/api/records", "Bearer wrong").Code; c != http.StatusForbidden {
		t.Errorf("a wrong token: %d", c)
	}
	if c := get(h, "198.51.100.2", "/api/records", "").Code; c != http.StatusOK {
		t.Errorf("clients share a bucket: %d", c)
	}
	unset := readGuard(ok, NewRateLimiter(3, 3), nil, "")
	if c := get(unset, "203.0.113.7", "/api/records", "Bearer "); c.Code != http.StatusForbidden {
		t.Errorf("an empty token passes while TELEMETRY_ADMIN_TOKEN is unset: %d", c.Code)
	}
}

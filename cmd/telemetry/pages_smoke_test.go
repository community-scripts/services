package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFixtures builds the API answers with the fetchers the handlers call.
func writeFixtures(t *testing.T, dir string) {
	t.Helper()
	ch, _ := newFakeCH(t)
	ctx := context.Background()
	write := func(name string, v interface{}) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dash, err := ch.FetchDashboardPage(ctx, RunFilter{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	write("dashboard.json", dash)
	recs, total, err := ch.FetchRuns(ctx, RunFilter{Days: 1, Type: "all"}, 1, 100, "-created")
	if err != nil {
		t.Fatal(err)
	}
	write("records.json", map[string]interface{}{"records": recs, "page": 1, "limit": 100, "total": total, "total_pages": 1})
	errs, err := ch.FetchErrorAnalysisData(ctx, RunFilter{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	write("errors.json", errs)
	causes, err := ch.FetchCauses(ctx, RunFilter{Days: 1}, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	write("causes.json", CausesResponse{Days: 1, Causes: causes, Updated: time.Now().UTC().Format(time.RFC3339)})
	apps, err := ch.FetchAppStats(ctx, RunFilter{Days: 1}, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	write("app-errors.json", AppErrorsResponse{Days: 1, MinRuns: minRunsForRate(1), Apps: apps})
	scripts, err := ch.FetchScriptStats(ctx, 1, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	write("scripts.json", scripts)
	write("repo-slugs.json", map[string]interface{}{"repo_slugs": dash.RepoSlugs})
}

// Nothing in the Go toolchain runs the pages' JavaScript; node does it here.
func TestPagesRenderAgainstAPIOutput(t *testing.T) {
	dir := t.TempDir()
	if out := os.Getenv("FIXTURES_OUT"); out != "" {
		dir = out
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtures(t, dir)
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; the fixtures were built but the pages were not rendered")
	}
	out, err := exec.Command("node", "testdata/pages-smoke.js", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("the pages failed to render:\n%s", out)
	}
	if !strings.Contains(string(out), "pages render") {
		t.Errorf("smoke test did not report success:\n%s", out)
	}
}

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The /new page is plain JavaScript inside an embedded template, so nothing in
// the Go toolchain looks at it. node --check would only prove it parses, and a
// function deleted during a refactor still parses -- which is how the page
// reached a browser answering "renderWarnings is not defined" with every panel
// blank.
//
// testdata/newdash-smoke.js renders the page against a synthetic payload and
// asserts each section produced something, so that failure surfaces here.
//
// In testdata/ rather than tools/, which .gitignore excludes -- the first
// version of this test was committed without its script and would have failed
// on any fresh clone.
//
// Skipped when node is absent rather than failing: a Go service should still
// build and test on a machine without a JavaScript runtime.
func TestNewDashboardPageRenders(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping the /new render check")
	}

	out, err := exec.Command("node", "testdata/newdash-smoke.js").CombinedOutput()
	if err != nil {
		t.Fatalf("the /new page failed to render:\n%s", out)
	}
	if !strings.Contains(string(out), "every section") {
		t.Errorf("smoke test did not report success:\n%s", out)
	}
}

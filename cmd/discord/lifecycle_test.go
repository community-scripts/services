package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRouteIssueEvent(t *testing.T) {
	for _, c := range []struct {
		action, label string
		labels        []string
		want          lifecycleMode
	}{
		{"labeled", "Ready For Testing", nil, modeReady},
		{"unlabeled", "Ready For Testing", []string{"task"}, modeHold},
		{"unlabeled", "Ready For Testing", []string{"deferred"}, modeNoop},
		{"unlabeled", "Ready For Testing", []string{"Started Migration To ProxmoxVE"}, modeNoop},
		{"unlabeled", "Ready For Testing", []string{"Migration To ProxmoxVE"}, modeNoop},
		{"labeled", "deferred", nil, modeDefer},
		{"labeled", "Started Migration To ProxmoxVE", nil, modeMigrate},
		{"closed", "", []string{"Ready For Testing"}, modeDelete},
		{"labeled", "bug", nil, modeNoop},
		{"opened", "", nil, modeNoop},
	} {
		if got := routeIssueEvent(c.action, c.label, c.labels); got != c.want {
			t.Errorf("route(%s,%q,%v)=%s want %s", c.action, c.label, c.labels, got, c.want)
		}
	}
}

func comment(login, body string) ghComment {
	c := ghComment{Body: body}
	c.User.Login = login
	return c
}

func TestLastHumanUpdateSkipsBots(t *testing.T) {
	comments := []ghComment{
		comment("github-actions", "auto"),
		comment("alice", "real"),
		comment("sync[bot]", "bot"),
	}
	if got := lastHumanUpdate(comments, "body"); got != "real" {
		t.Fatalf("got %q", got)
	}
	if got := lastHumanUpdate(nil, "body"); got != "body" {
		t.Fatalf("fallback to body, got %q", got)
	}
	long := strings.Repeat("x", 900)
	if got := lastHumanUpdate(nil, long); len(got) != 800+len("\n…\n") || !strings.HasSuffix(got, "\n…\n") {
		t.Fatalf("want 800 chars plus ellipsis, got %d", len(got))
	}
}

func TestPrFromComments(t *testing.T) {
	comments := []ghComment{
		comment("bot", "A PR has been created: community-scripts/ProxmoxVE#12"),
		comment("bot", "superseded by community-scripts/ProxmoxVE#34 today"),
	}
	if got := prFromComments(comments); got != "34" {
		t.Fatalf("got %q", got)
	}
	if got := prFromComments(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestMarkerFromComments(t *testing.T) {
	comments := []ghComment{
		comment("bot", "thread\n<!-- discord-thread-id: 100000000000000001 -->"),
		comment("bot", "resumed\n<!-- discord-thread-id: 100000000000000002 -->"),
	}
	if got := markerFromComments(comments); got != "100000000000000002" {
		t.Fatalf("got %q", got)
	}
}

func TestThreadNameTruncates(t *testing.T) {
	if got := threadName("App"); got != "Wanted Tester for App" {
		t.Fatalf("got %q", got)
	}
	if got := threadName(strings.Repeat("ä", 120)); utf8.RuneCountInString(got) != 100 {
		t.Fatalf("want 100 runes, got %d", utf8.RuneCountInString(got))
	}
}

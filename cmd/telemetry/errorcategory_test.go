package main

import "testing"

// The engine prefixes every error with its own description of the exit code.
// That description is a label, not evidence, and matching keywords against it
// put every exit-1 failure into "permission" — 944 of 5165 over fourteen days,
// the largest category on the dashboard — because exit 1 is described as
// "General error / Operation not permitted".
//
// The strings below are real payloads taken from /api/errors, truncated.
func TestDiagnosticHeaderIsNotEvidence(t *testing.T) {
	cases := []struct {
		name string
		code int
		text string
		want string
	}{
		{
			name: "npm install failure is not a permission problem",
			code: 1,
			text: "exit_code=1 | General error / Operation not permitted | at line 23: " +
				"npm install -g t3 @openai/codex @anthropic-ai/claude-code opencode-ai\n---\nnpm error code E404",
			want: "unknown",
		},
		{
			// Second header shape: no "| at line N: cmd" field at all, which is
			// what the trap produces when it had no command to record.
			name: "header without a command field is still stripped",
			code: 1,
			text: "exit_code=1 | General error / Operation not permitted\n---\n" +
				"Creating filesystem with 262144 4k blocks and 65536 inodes",
			want: "unknown",
		},
		{
			name: "header alone, nothing after it",
			code: 1,
			text: "exit_code=1 | General error / Operation not permitted",
			want: "unknown",
		},
		{
			// The categoriser must still see a genuine permission failure in the
			// part of the text that really is evidence.
			name: "a real permission failure is still caught",
			code: 1,
			text: "exit_code=1 | General error / Operation not permitted | at line 8: install -m 0755 x /usr/local/bin\n---\n" +
				"install: cannot create regular file '/usr/local/bin/x': Permission denied",
			want: "permission",
		},
		{
			name: "apt evidence still wins",
			code: 1,
			text: "exit_code=1 | General error / Operation not permitted | at line 12: apt-get install -y foo\n---\n" +
				"E: Unable to locate package foo",
			want: "apt",
		},
		{
			// A code with its own category must not reach the text matcher at all.
			name: "known exit code is decided by the code",
			code: 100,
			text: "exit_code=100 | APT: Package manager error | at line 3: apt upgrade\n---\nsomething about permission denied",
			want: "apt",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveErrorCategory(c.code, c.text); got != c.want {
				t.Errorf("deriveErrorCategory(%d, …) = %q, want %q", c.code, got, c.want)
			}
		})
	}
}

func TestStripDiagnosticHeaderLeavesRealTextAlone(t *testing.T) {
	// Anything that is not our header must survive untouched, or evidence would
	// be thrown away along with the label.
	for _, s := range []string{
		"install: cannot create regular file: Permission denied",
		"at line 5: apt-get update",
		"",
		"exit code 1 without the header format",
	} {
		if got := stripDiagnosticHeader(s); got != s {
			t.Errorf("stripDiagnosticHeader(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Exit 101 was described as "APT: Configuration error (bad sources)". apt
// reports its problems as 100; 101 is what cargo returns for any failure, and
// over fourteen days every exit-101 signature in the data was a Rust build --
// scanopy 48, vaultwarden 27, oxicloud 25, a hundred of the hundred and twenty.
// The code alone cannot tell them apart, so the evidence has to.
func TestExit101IsDecidedByEvidence(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "cargo build is a build failure, not an apt one",
			text: "exit_code=101 | Build or configuration error | at line 32: " +
				"CARGO_BUILD_JOBS=\"$(get_parallel_jobs)\" cargo build --release --bin server\n---\n" +
				"error: could not compile `diesel` (lib)",
			want: "build",
		},
		{
			name: "a genuine apt problem under 101 is still apt",
			text: "exit_code=101 | Build or configuration error | at line 9: apt-get update\n---\n" +
				"E: Unable to locate package foo",
			want: "apt",
		},
		{
			// A build that died for want of a -dev package is an apt problem, and
			// the apt branch is checked first so that evidence wins.
			name: "missing dev package during a build stays apt",
			text: "exit_code=101 | Build or configuration error | at line 9: cargo build --release\n---\n" +
				"E: Unable to locate package libssl-dev",
			want: "apt",
		},
		{
			name: "no usable evidence stays unknown rather than guessing",
			text: "exit_code=101 | Build or configuration error",
			want: "unknown",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveErrorCategory(101, c.text); got != c.want {
				t.Errorf("deriveErrorCategory(101, …) = %q, want %q", got, c.want)
			}
		})
	}

	// Exit 100 really is apt and must stay decided by the code alone.
	if got := deriveErrorCategory(100, "exit_code=100 | APT: Package manager error | at line 3: apt upgrade"); got != "apt" {
		t.Errorf("exit 100 = %q, want apt", got)
	}
}

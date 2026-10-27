package main

import (
	"strings"
	"testing"
)

func TestDeriveErrorMessage(t *testing.T) {
	cases := []struct {
		name  string
		trace string
		want  string
	}{
		{
			name: "go build out of disk: the cause beats the later FATAL line",
			trace: "exit_code=1 | General error | at line 40: go build -o /opt/app\n---\n" +
				"go: downloading github.com/x/y v1.2.3\n" +
				"/usr/local/go/pkg/tool/linux_amd64/link: mapping output file failed: no space left on device\n" +
				"2026/10/27 09:43:58 [INFO] Cleaning up temporary folder: /tmp/buildenv_123\n" +
				"2026/10/27 09:43:58 [FATAL] exit status 1",
			want: "/usr/local/go/pkg/tool/linux_amd64/link: mapping output file failed: no space left on device",
		},
		{
			name: "python traceback: the exception line",
			trace: "exit_code=1 | General error | at line 77: uv run manage.py migrate\n---\n" +
				"Traceback (most recent call last):\n" +
				"  File \"/opt/app/.venv/lib/python3.12/site-packages/redis/connection.py\", line 357, in connect\n" +
				"    sock = self.retry.call_with_retry(\n" +
				"redis.exceptions.ConnectionError: Error 111 connecting to localhost:6379. Connection refused.",
			want: "redis.exceptions.ConnectionError: Error 111 connecting to localhost:6379. Connection refused.",
		},
		{
			name: "apt fetch: the HTTP 400 line beats the generic last E: line",
			trace: "exit_code=100 | APT: Package manager error | at line 12: apt-get install -y sqlite3\n---\n" +
				"Get:1 http://deb.debian.org/debian trixie/main amd64 libsqlite3-0 amd64 3.46.1-7 [900 kB]\n" +
				"E: Failed to fetch http://deb.debian.org/debian/pool/main/s/sqlite3/libsqlite3-0_3.x.x-7%2bdeb13u2_amd64.deb  400  Bad Request [IP: 151.x.x.132 80]\n" +
				"E: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?",
			want: "E: Failed to fetch http://deb.debian.org/debian/pool/main/s/sqlite3/libsqlite3-0_3.x.x-7%2bdeb13u2_amd64.deb  400  Bad Request [IP: 151.x.x.132 80]",
		},
		{
			name: "engine msg_error line, hint skipped, prefixes stripped",
			trace: "exit_code=7 | curl: Failed to connect | step: Setting up Grafana Repository | in setup_deb822_repo\n---\n" +
				"[2026-10-27 12:55:26] [INFO] Setting up Grafana Repository\n" +
				"[2026-10-27 12:55:27] [ERROR] Failed to download GPG key for grafana from: https://apt.grafana.com/gpg.key\n" +
				"[2026-10-27 12:55:27] [ERROR] Hint: Check network connectivity and that the URL is reachable",
			want: "Failed to download GPG key for grafana from: https://apt.grafana.com/gpg.key",
		},
		{
			name: "pct create on missing LVM volume group, xtrace ignored",
			trace: "exit_code=209 | Proxmox: Container creation failed | step: Creating LXC container\n---\n" +
				"  Volume group \"stockage\" not found\n" +
				"  Cannot process volume group stockage\n" +
				"unable to create CT 102 - command '/sbin/lvs --separator : --noheadings stockage' failed: exit code 5\n" +
				"+ rc=255",
			want: "Volume group \"stockage\" not found",
		},
		{
			name: "engine echo lines are not the message",
			trace: "exit_code=1 | General error\n---\n" +
				"npm error code ERESOLVE\n" +
				"npm error ERESOLVE could not resolve\n" +
				"✖️ [ERROR] in line 52: exit code 1 (General error): while executing command npm ci",
			want: "npm error ERESOLVE could not resolve",
		},
		{
			name:  "generic failed line when nothing specific is there",
			trace: "exit_code=1 | General error\n---\nBuilding frontend\nBuild failed with 3 errors",
			want:  "Build failed with 3 errors",
		},
		{
			name:  "legacy one-line container trace",
			trace: "exit_code=1 | General error / Operation not permitted|---|Reading package lists...|E: Unable to locate package foo",
			want:  "E: Unable to locate package foo",
		},
		{
			name:  "ANSI colours and glyphs are stripped",
			trace: "exit_code=1 | General error\n---\n\x1b[31m✖️  Error: Cannot find module 'vite'\x1b[0m",
			want:  "Error: Cannot find module 'vite'",
		},
		{
			name:  "update-ca-certificates chatter is not a certificate failure",
			trace: "exit_code=1 | General error\n---\nUpdating certificates in /etc/ssl/certs...\n0 added, 0 removed; done.",
			want:  "",
		},
		{name: "header only", trace: "exit_code=1 | General error | at line 3: false", want: ""},
		{name: "synthesised description", trace: "Exit code 100: APT: Package manager error", want: ""},
		{name: "nothing matches", trace: "exit_code=1 | General error\n---\nDone.\nStarting service", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveErrorMessage(c.trace); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestDeriveErrorMessageIsBounded(t *testing.T) {
	got := deriveErrorMessage("exit_code=1 | x\n---\nerror: " + strings.Repeat("a", 500) + " from 192.168.1.101")
	if len(got) != 300 || !strings.HasPrefix(got, "error: aaa") {
		t.Errorf("got %d bytes: %q", len(got), got[:20])
	}
}

func TestIngestDerivesMessageOnlyWhenMissing(t *testing.T) {
	trace := "exit_code=1 | General error\n---\nE: Unable to locate package foo"
	in := TelemetryIn{Type: "lxc", Status: "failed", ExitCode: 1, Error: trace}
	classifyIngest(&in, false)
	if in.ErrorMessage != "E: Unable to locate package foo" {
		t.Errorf("derived %q", in.ErrorMessage)
	}

	in = TelemetryIn{Type: "lxc", Status: "failed", ExitCode: 1, Error: trace, ErrorMessage: "From the engine"}
	classifyIngest(&in, false)
	if in.ErrorMessage != "From the engine" {
		t.Errorf("client message replaced with %q", in.ErrorMessage)
	}

	in = TelemetryIn{Type: "lxc", Status: "aborted", ExitCode: 130, Error: trace}
	classifyIngest(&in, false)
	if in.ErrorMessage != "" {
		t.Errorf("aborted run got message %q", in.ErrorMessage)
	}
}

func TestOutputTailSQLKeepsEscapes(t *testing.T) {
	q := outputTailSQL("final_err")
	if strings.Contains(q, "\n---\n") || !strings.Contains(q, `'\n---\n'`) {
		t.Errorf("separator must reach ClickHouse as an escape:\n%s", q)
	}
	if strings.Contains(q, "%!") {
		t.Errorf("format verb left unconsumed:\n%s", q)
	}
}

package main

import "testing"

func TestToolAndAddonFailuresStayFailed(t *testing.T) {
	for _, typ := range []string{"pve", "addon"} {
		in := TelemetryIn{Type: typ, NSAPP: "post-pve-install", Status: "failed", ExitCode: 100,
			Error: "exit_code=100 | APT: Package manager error"}
		classifyIngest(&in, false)
		if in.Status != "failed" || in.ExitCode != 100 || in.Error == "" || in.ErrorCategory != "apt" {
			t.Errorf("%s: status=%q exit=%d category=%q error=%q", typ, in.Status, in.ExitCode, in.ErrorCategory, in.Error)
		}
	}
}

func TestExitZeroFailureIsSuccess(t *testing.T) {
	in := TelemetryIn{Type: "lxc", Status: "failed", ExitCode: 0, Error: "x", ErrorMessage: "y", ErrorPhase: "host"}
	classifyIngest(&in, false)
	if in.Status != "success" || in.Error != "" || in.ErrorMessage != "" || in.ErrorPhase != "" {
		t.Errorf("got %+v", in)
	}
}

func TestAbortTextOnlyCountsForOldClients(t *testing.T) {
	trace := "exit_code=1 | General error\n---\nServer started, press Ctrl+C to stop\nError: listen EADDRINUSE"
	cases := []struct {
		version, code int
		want          bool
	}{
		{3, 1, false},
		{2, 1, true},
		{0, 1, true},
		{3, 130, true},
		{3, 129, true},
	}
	for _, c := range cases {
		if got := isAbortSignal(c.version, c.code, trace); got != c.want {
			t.Errorf("isAbortSignal(v%d, %d) = %v, want %v", c.version, c.code, got, c.want)
		}
	}

	in := TelemetryIn{Type: "lxc", Status: "failed", ExitCode: 1, PayloadVersion: 3, Error: trace}
	classifyIngest(&in, false)
	if in.Status != "failed" {
		t.Errorf("v3 failure with Ctrl+C in its log became %q", in.Status)
	}
}

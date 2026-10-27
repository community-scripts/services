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

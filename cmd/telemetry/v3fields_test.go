package main

import (
	"strings"
	"testing"
)

func TestValidateV3Fields(t *testing.T) {
	in := TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "grafana", Status: "failed", ExitCode: 7,
		ErrorMessage:   "Failed to reach 192.168.1.101\nretrying " + strings.Repeat("x", 400),
		ErrorStep:      "Setting up Grafana Repository",
		ErrorPhase:     "Container",
		FailedFunction: "setup_uv",
	}
	if err := validate(&in); err != nil {
		t.Fatal(err)
	}
	if len(in.ErrorMessage) != 300 || !strings.HasPrefix(in.ErrorMessage, "Failed to reach 192.x.x.x retrying ") {
		t.Errorf("error_message = %q (%d bytes)", in.ErrorMessage[:40], len(in.ErrorMessage))
	}
	if in.ErrorPhase != "container" || in.ErrorStep != "Setting up Grafana Repository" || in.FailedFunction != "setup_uv" {
		t.Errorf("phase=%q step=%q func=%q", in.ErrorPhase, in.ErrorStep, in.FailedFunction)
	}

	in = TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "x", Status: "failed", ErrorPhase: "guest"}
	if err := validate(&in); err != nil || in.ErrorPhase != "" {
		t.Errorf("unknown phase: err=%v phase=%q", err, in.ErrorPhase)
	}
}

func TestSanitizeShortKeepsUTF8Whole(t *testing.T) {
	if got := sanitizeShort("abcä", 4); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestRescueExtractsV3Fields(t *testing.T) {
	raw := `{
    "payload_version": 3,
    "random_id": "r1",
    "type": "lxc",
    "nsapp": "grafana",
    "status": "failed",
    "exit_code": 7,
    "error": "exit_code=7 | curl: Failed to connect
---
bad "quote" here",
    "error_message": "Failed to download \"GPG\" key",
    "error_step": "Setting up Grafana Repository",
    "error_phase": "container",
    "failed_function": "setup_grafana",
    "error_category": "network"
}`
	in, err := rescueBrokenJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if in.ErrorMessage != `Failed to download "GPG" key` || in.ErrorStep != "Setting up Grafana Repository" ||
		in.ErrorPhase != "container" || in.FailedFunction != "setup_grafana" {
		t.Errorf("got message=%q step=%q phase=%q func=%q", in.ErrorMessage, in.ErrorStep, in.ErrorPhase, in.FailedFunction)
	}
	if !strings.HasSuffix(in.Error, `bad "quote" here`) {
		t.Errorf("error = %q", in.Error)
	}
}

func TestValidateNeverRejectsOverOsType(t *testing.T) {
	cases := map[string]string{
		"kali":        "kali",
		"ArchLinux":   "archlinux",
		"amazonlinux": "amazonlinux",
		"nixos":       "nixos",
		"umbrel-os":   "umbrel-os",
		"-":           "",
		"rm -rf /":    "",
		"../etc":      "",
	}
	for _, typ := range []string{"lxc", "vm", "addon"} {
		for os, want := range cases {
			in := TelemetryIn{RandomID: "r", Type: typ, NSAPP: "x", Status: "installing", OsType: os}
			if err := validate(&in); err != nil {
				t.Errorf("%s os_type %q rejected: %v", typ, os, err)
			}
			if in.OsType != want {
				t.Errorf("%s os_type %q = %q, want %q", typ, os, in.OsType, want)
			}
		}
	}
}

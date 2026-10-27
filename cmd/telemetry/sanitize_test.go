package main

import "testing"

func TestSanitizeIPsKeepsVersions(t *testing.T) {
	cases := map[string]string{
		"Setup uv 0.9.5":                       "Setup uv 0.9.5",
		"MariaDB 11.8.3 installed":             "MariaDB 11.8.3 installed",
		"libzvbi0t64:amd64 (0.2.44-2)":         "libzvbi0t64:amd64 (0.2.44-2)",
		"connect to 192.168.1.101 port 22":     "connect to 192.x.x.x port 22",
		"[IP: 151.101.2.132 80]":               "[IP: 151.x.x.x 80]",
		"http://10.0.0.5:8080/api":             "http://10.x.x.x:8080/api",
		"nameserver 1.1.1.1, fallback 8.8.4.4": "nameserver 1.x.x.x, fallback 8.x.x.x",
	}
	for in, want := range cases {
		if got := sanitizeIPs(in); got != want {
			t.Errorf("sanitizeIPs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateAnonymisesFailedCommand(t *testing.T) {
	in := TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "x", Status: "failed",
		FailedCommand: "curl -fsSL http://192.168.1.101/setup.sh", Error: "ping 10.1.2.3 failed"}
	if err := validate(&in); err != nil {
		t.Fatal(err)
	}
	if in.FailedCommand != "curl -fsSL http://192.x.x.x/setup.sh" {
		t.Errorf("failed_command = %q", in.FailedCommand)
	}
	if in.Error != "ping 10.x.x.x failed" {
		t.Errorf("error = %q", in.Error)
	}
}

package main

import (
	"errors"
	"strings"
	"testing"
)

const formBody = "### Name of the Script\r\n\r\nSnipeIT\r\n\r\n### Script Type\r\n\r\nCT (LXC Container)\r\n\r\n### Script path\r\n\r\n_No response_\r\n\r\n### Where does it run?\r\n\r\n_No response_\r\n"

func TestIssueField(t *testing.T) {
	for _, c := range []struct{ label, want string }{
		{"Name of the Script", "SnipeIT"},
		{"Script Type", "CT (LXC Container)"},
		{"Script path", ""},
		{"Missing", ""},
	} {
		if got := issueField(formBody, c.label); got != c.want {
			t.Errorf("issueField(%q)=%q want %q", c.label, got, c.want)
		}
	}
}

func fetchOnly(paths ...string) rawFetcher {
	return func(path string) ([]byte, bool) {
		for _, p := range paths {
			if p == path {
				return []byte("#!/usr/bin/env bash\n"), true
			}
		}
		return nil, false
	}
}

func TestResolveScriptDefaults(t *testing.T) {
	info, err := resolveScript("SnipeIT", formBody, fetchOnly("ct/snipeit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := scriptInfo{Slug: "snipeit", Type: "ct", Path: "ct/snipeit.sh", RunsOn: "pve"}
	if info != want {
		t.Fatalf("got %+v want %+v", info, want)
	}
}

func TestResolveScriptVmFallback(t *testing.T) {
	body := "### Name of the Script\n\nUmbrel OS\n\n### Script Type\n\nVM (Virtual Machine)\n"
	info, err := resolveScript("Umbrel OS", body, fetchOnly("vm/umbrel-os-vm.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "vm/umbrel-os-vm.sh" || info.Type != "vm" {
		t.Fatalf("got %+v", info)
	}
}

func TestResolveScriptRejectsTraversal(t *testing.T) {
	body := "### Name of the Script\n\napp\n\n### Script Type\n\nCT (LXC Container)\n\n### Script path\n\n../evil.sh\n"
	info, err := resolveScript("app", body, fetchOnly("ct/app.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "ct/app.sh" {
		t.Fatalf("traversal path must be discarded, got %q", info.Path)
	}
}

func TestResolveScriptMissingFile(t *testing.T) {
	_, err := resolveScript("app", "", fetchOnly())
	var missing errScriptMissing
	if !errors.As(err, &missing) || missing.Path != "ct/app.sh" {
		t.Fatalf("want errScriptMissing for ct/app.sh, got %v", err)
	}
}

func TestRunsOn(t *testing.T) {
	for _, c := range []struct {
		typ, path, where, script, want string
	}{
		{"ct", "ct/x.sh", "", "", "pve"},
		{"vm", "vm/x.sh", "", "", "pve"},
		{"pve", "tools/pve/pbs-backup.sh", "", "", "pbs"},
		{"pve", "tools/pve/clean.sh", "", "", "pve"},
		{"addon", "tools/addon/x.sh", "", "#!/bin/bash\npct exec 100 -- ls\n", "pve"},
		{"addon", "tools/addon/x.sh", "", "#!/bin/bash\n# pct is not used\ncommand -v pct\necho hi\n", "guest"},
		{"addon", "tools/addon/x.sh", "Inside an LXC or VM", "pct list\n", "guest"},
		{"other", "tools/foo.sh", "Proxmox Backup Server", "", "pbs"},
		{"other", "tools/foo.sh", "", "", "unknown"},
		{"other", "tools/pmg-x.sh", "", "", "pmg"},
	} {
		if got := runsOn(c.typ, c.path, c.where, []byte(c.script)); got != c.want {
			t.Errorf("runsOn(%s,%s,%q)=%s want %s", c.typ, c.path, c.where, got, c.want)
		}
	}
}

func TestTestingMessageCredentialsAndNotes(t *testing.T) {
	info := scriptInfo{Slug: "app", Type: "ct", Path: "ct/app.sh", RunsOn: "pve"}
	js := []byte(`{"default_credentials":{"username":"admin","password":null},"notes":[{"text":"Note one","type":"info"},{"text":"Note two","type":"warning"}]}`)
	msg := testingMessage("App", info, "https://raw/main", js)
	for _, want := range []string{
		"The App script is ready for testing. Run it in the Proxmox VE shell (or on an Incus host) to create a container:",
		"bash -c \"$(curl -fsSL https://raw/main/ct/app.sh)\"",
		"Default credentials:\nUsername: admin\n",
		"Note one\nNote two\n",
		"it will need to be recreated",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Password:") {
		t.Error("null password must not be printed")
	}
}

func TestTestingMessagePveToolHasNoJson(t *testing.T) {
	info := scriptInfo{Slug: "clean", Type: "pve", Path: "tools/pve/clean.sh", RunsOn: "pve"}
	msg := testingMessage("Clean", info, "https://raw/main", nil)
	if !strings.Contains(msg, "Run it in the Proxmox VE shell:") || strings.Contains(msg, "Default credentials") || strings.Contains(msg, "recreated") {
		t.Fatalf("unexpected message:\n%s", msg)
	}
}

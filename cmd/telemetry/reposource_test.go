package main

import "testing"

func TestValidateFilesDevScriptsUnderOneSource(t *testing.T) {
	cases := []struct{ source, slug string }{
		{"DevScripts", ""},
		{"ProxmoxVED", ""},
		{"", "community-scripts/DevScripts"},
		{"", "community-scripts/ProxmoxVED"},
	}
	for _, c := range cases {
		in := TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "debian", Status: "success", RepoSource: c.source, RepoSlug: c.slug}
		if err := validate(&in); err != nil {
			t.Fatalf("source %q slug %q: %v", c.source, c.slug, err)
		}
		if in.RepoSource != "DevScripts" {
			t.Errorf("source %q slug %q: got %q, want DevScripts", c.source, c.slug, in.RepoSource)
		}
	}
}

func TestRepoSourcePredDevScriptsIncludesLegacyRows(t *testing.T) {
	for _, source := range []string{"DevScripts", "ProxmoxVED"} {
		pred, args := repoSourcePred(source)
		if pred != "repo_source IN ('DevScripts','ProxmoxVED')" || args != nil {
			t.Errorf("%s: got %q %v", source, pred, args)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func TestDashboardBranding(t *testing.T) {
	for _, name := range []string{"dashboard.html", "error-analysis.html", "script-analysis.html", "new/index.html"} {
		t.Run(name, func(t *testing.T) {
			data, err := publicFS.ReadFile("public/templates/" + name)
			if err != nil {
				t.Fatal(err)
			}
			html := string(data)
			for _, expected := range []string{
				`<link rel="icon" type="image/svg+xml" href="/static/img/logo.svg?v=cs-amber">`,
				`<img width="28" height="28" src="/static/img/logo.svg?v=cs-amber" alt="" data-brand-logo>`,
			} {
				if !strings.Contains(html, expected) {
					t.Errorf("missing brand markup: %s", expected)
				}
			}
			if strings.Contains(html, "/static/img/logo.png") {
				t.Error("dashboard should use the SVG master")
			}
		})
	}
}

func TestBrandLogoPreservesColors(t *testing.T) {
	data, err := publicFS.ReadFile("public/static/img/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`fill="#29262b"`, `stroke="#f5f4eb"`, `stroke="#f29a67"`, `mask="url(#weave-clearance)"`} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("missing approved brand element: %s", expected)
		}
	}
	inliner, err := publicFS.ReadFile("public/static/js/svg-inliner.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inliner), ":not([data-brand-logo])") {
		t.Error("brand images must bypass monochrome icon conversion")
	}
}

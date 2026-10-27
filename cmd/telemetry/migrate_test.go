package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMigrator struct {
	calls [][]string
	moved int
}

func (f *fakeMigrator) MigrateScript(_ context.Context, slugs []string) (int, int, error) {
	f.calls = append(f.calls, slugs)
	return f.moved, 3, nil
}

func migratePost(h http.HandlerFunc, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/migrate-script", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestMigrateScriptAuth(t *testing.T) {
	body := `{"slugs":["vane"],"from":"DevScripts","to":"ProxmoxVE"}`
	cases := []struct {
		name, token, auth string
		want              int
	}{
		{"disabled without a token", "", "Bearer anything", http.StatusForbidden},
		{"empty bearer against no token", "", "Bearer ", http.StatusForbidden},
		{"missing header", "s3cret", "", http.StatusForbidden},
		{"wrong token", "s3cret", "Bearer s3cre", http.StatusForbidden},
		{"not a bearer", "s3cret", "s3cret", http.StatusForbidden},
		{"right token", "s3cret", "Bearer s3cret", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &fakeMigrator{moved: 5}
			rec := migratePost(migrateScriptHandler(c.token, m, func(context.Context) {}), c.auth, body)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d", rec.Code, c.want)
			}
			if c.want != http.StatusOK && len(m.calls) != 0 {
				t.Error("migrator ran without authorisation")
			}
		})
	}
}

func TestMigrateScriptValidation(t *testing.T) {
	many := `"a0","a1","a2","a3","a4","a5","a6","a7","a8","a9","b0","b1","b2","b3","b4","b5","b6","b7","b8","b9","c0"`
	cases := map[string]string{
		"no slugs":        `{"slugs":[],"from":"DevScripts","to":"ProxmoxVE"}`,
		"too many slugs":  `{"slugs":[` + many + `],"from":"DevScripts","to":"ProxmoxVE"}`,
		"uppercase slug":  `{"slugs":["Vane"],"from":"DevScripts","to":"ProxmoxVE"}`,
		"quote in slug":   `{"slugs":["x') OR 1=1 --"],"from":"DevScripts","to":"ProxmoxVE"}`,
		"wrong source":    `{"slugs":["vane"],"from":"ProxmoxVE","to":"ProxmoxVE"}`,
		"wrong target":    `{"slugs":["vane"],"from":"DevScripts","to":"Incus"}`,
		"not json":        `slugs=vane`,
		"slug too long":   `{"slugs":["` + strings.Repeat("a", 65) + `"],"from":"DevScripts","to":"ProxmoxVE"}`,
		"leading hyphen":  `{"slugs":["-vane"],"from":"DevScripts","to":"ProxmoxVE"}`,
		"missing from/to": `{"slugs":["vane"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMigrator{}
			rec := migratePost(migrateScriptHandler("t", m, func(context.Context) {}), "Bearer t", body)
			if rec.Code != http.StatusBadRequest || len(m.calls) != 0 {
				t.Errorf("status %d, calls %d", rec.Code, len(m.calls))
			}
		})
	}
}

func TestMigrateScriptResponse(t *testing.T) {
	m := &fakeMigrator{moved: 42}
	invalidated := false
	h := migrateScriptHandler("t", m, func(context.Context) { invalidated = true })
	rec := migratePost(h, "Bearer t", `{"slugs":["vane","hometube","vane"],"from":"ProxmoxVED","to":"ProxmoxVE"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Moved int      `json:"moved"`
		Slugs []string `json:"slugs"`
		Days  int      `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Moved != 42 || got.Days != 3 || strings.Join(got.Slugs, ",") != "vane,hometube" {
		t.Errorf("got %+v", got)
	}
	if !invalidated {
		t.Error("caches were not invalidated")
	}
	if len(m.calls) != 1 || len(m.calls[0]) != 2 {
		t.Errorf("migrator calls %v", m.calls)
	}

	if rec := migratePost(h, "Bearer t", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/migrate-script", nil)
	req.Header.Set("Authorization", "Bearer t")
	r := httptest.NewRecorder()
	h(r, req)
	if r.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", r.Code)
	}
}

func TestMigrateScriptStatements(t *testing.T) {
	slugs, days := []string{"vane", "hometube"}, []string{"2026-10-01", "2026-10-02", "2026-10-03"}

	count := migrateCountSQL(slugs)
	if strings.Count(count.q, "?") != len(count.args) || !strings.Contains(count.q, "repo_source IN ('DevScripts','ProxmoxVED')") {
		t.Errorf("count query:\n%s", count.q)
	}

	stmts := migrateScriptStatements(slugs, days)
	var all []string
	for _, s := range stmts {
		if n := strings.Count(s.q, "?"); n != len(s.args) {
			t.Errorf("%d placeholders, %d args:\n%s", n, len(s.args), s.q)
		}
		if strings.Contains(s.q, "%!") {
			t.Errorf("format verb left in:\n%s", s.q)
		}
		if strings.HasPrefix(strings.TrimSpace(s.q), "ALTER") && !strings.HasSuffix(s.q, "SETTINGS mutations_sync = 2") {
			t.Errorf("mutation is not synchronous:\n%s", s.q)
		}
		all = append(all, s.q)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{
		"UPDATE repo_source = 'ProxmoxVE', repo_slug = 'community-scripts/ProxmoxVE', migrated_from = repo_source",
		"INSERT INTO telemetry_db.mv_daily_stats",
		"ALTER TABLE telemetry_db.mv_daily_stats DELETE",
		"INSERT INTO telemetry_db.mv_daily_errors",
		"ALTER TABLE telemetry_db.mv_daily_errors DELETE",
		"INSERT INTO telemetry_db.mv_daily_os (day, repo_source, os_type, cnt)",
		"INSERT INTO telemetry_db.mv_daily_method (day, repo_source, method, cnt)",
		"INSERT INTO telemetry_db.mv_daily_pve (day, repo_source, pve_version, cnt)",
		"pve_version != '' AND status IN ('success','failed','aborted','unknown')",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("statements lack %q", want)
		}
	}

	// The dev rows of the keyed views are copied before they are deleted.
	if !strings.HasPrefix(strings.TrimSpace(stmts[0].q), "INSERT INTO telemetry_db.mv_daily_stats") ||
		!strings.HasPrefix(strings.TrimSpace(stmts[1].q), "ALTER TABLE telemetry_db.mv_daily_stats DELETE") {
		t.Error("mv_daily_stats must be copied, then deleted")
	}
}

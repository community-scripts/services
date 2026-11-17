package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCreateIssueAccessLabelsAndDuplicates(t *testing.T) {
	var created []map[string]interface{}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/issues" || r.Header.Get("Authorization") != "Bearer gh-token" {
			t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL)
		}
		if r.Method == http.MethodGet {
			if q := r.URL.Query(); q.Get("state") != "open" || q.Get("labels") != "telemetry" {
				t.Errorf("the lookup is not limited to open telemetry issues: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`[{"title":"[Telemetry] immich: npm ERR! code ENOSPC","html_url":"https://github.com/o/r/issues/7"},
				{"title":"[Telemetry] a pull request","html_url":"https://github.com/o/r/pull/8","pull_request":{}}]`))
			return
		}
		var issue map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&issue); err != nil {
			t.Error(err)
		}
		created = append(created, issue)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"html_url":"https://github.com/o/r/issues/9"}`))
	}))
	defer gh.Close()
	defer func(old string) { githubAPI = old }(githubAPI)
	githubAPI = gh.URL

	cfg := Config{AdminPassword: "adm1n", GitHubToken: "gh-token", GitHubOwner: "o", GitHubRepo: "r"}
	reads := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tr4ces" }
	post := func(h http.Handler, auth, body string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodPost, "/api/github/create-issue", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	labels := func(i int) interface{} { return created[i]["labels"] }

	h := issueHandler(cfg, reads, newIssueLimiter(2))
	if c, _ := post(h, "", `{"title":"a","body":"b"}`); c != http.StatusUnauthorized || len(created) != 0 {
		t.Fatalf("no password: %d, %d issues created", c, len(created))
	}
	if c, _ := post(h, "", `{"password":"nope","title":"a","body":"b"}`); c != http.StatusUnauthorized || len(created) != 0 {
		t.Fatalf("a wrong admin password: %d, %d issues created", c, len(created))
	}
	c, out := post(h, "Bearer tr4ces", `{"title":"[Telemetry] docker: apt","body":"b","labels":["bug"]}`)
	if c != http.StatusOK || out["issue_url"] != "https://github.com/o/r/issues/9" || len(created) != 1 ||
		!reflect.DeepEqual(labels(0), []interface{}{"bug", "telemetry"}) {
		t.Fatalf("the trace password: %d %v, created %v", c, out, created)
	}
	if c, _ := post(h, "", `{"password":"adm1n","title":"[Telemetry] a pull request","body":"b","labels":["Telemetry"]}`); c != http.StatusOK ||
		len(created) != 2 || !reflect.DeepEqual(labels(1), []interface{}{"Telemetry"}) {
		t.Fatalf("the admin password, with a title only a pull request has: %d, created %v", c, created)
	}
	c, out = post(h, "Bearer tr4ces", `{"title":" [telemetry] Immich: npm ERR! code ENOSPC ","body":"b"}`)
	if c != http.StatusConflict || out["issue_url"] != "https://github.com/o/r/issues/7" || len(created) != 2 {
		t.Fatalf("a title that is already open: %d %v, %d issues created", c, out, len(created))
	}
	if c, _ := post(h, "Bearer tr4ces", `{"title":"third in the hour","body":"b"}`); c != http.StatusTooManyRequests || len(created) != 2 {
		t.Fatalf("past the hourly limit: %d, %d issues created", c, len(created))
	}

	unset := issueHandler(Config{GitHubToken: "gh-token", GitHubOwner: "o", GitHubRepo: "r"}, reads, newIssueLimiter(2))
	if c, _ := post(unset, "", `{"password":"","title":"a","body":"b"}`); c != http.StatusServiceUnavailable || len(created) != 2 {
		t.Fatalf("an empty password while ADMIN_PASSWORD is unset: %d, %d issues created", c, len(created))
	}
}

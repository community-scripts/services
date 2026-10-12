package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testGitHub(t *testing.T, handler http.HandlerFunc) *gitHub {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &gitHub{apiBase: srv.URL, client: srv.Client(), token: "t", tokenTill: time.Now().Add(time.Hour)}
}

func TestDoRejectsUnexpectedStatus(t *testing.T) {
	g := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := g.do(http.MethodGet, "/x", nil, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want status error with body, got %v", err)
	}
}

func TestListIssuesSkipsPullRequests(t *testing.T) {
	g := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			io.WriteString(w, "[]")
			return
		}
		io.WriteString(w, `[{"number":1,"title":"a","labels":[{"name":"bug"}]},{"number":2,"title":"pr","pull_request":{}}]`)
	})
	issues, err := g.listIssues("o/r", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 1 || issues[0].Labels[0].Name != "bug" {
		t.Fatalf("want issue 1 only, got %+v", issues)
	}
}

func TestCreateCommentPostsBody(t *testing.T) {
	var got map[string]string
	g := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/issues/7/comments" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":99,"html_url":"https://gh/c/99"}`)
	})
	c, err := g.createComment("o/r", 7, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != 99 || c.HTMLURL != "https://gh/c/99" || got["body"] != "hello" {
		t.Fatalf("got %+v body %q", c, got["body"])
	}
}

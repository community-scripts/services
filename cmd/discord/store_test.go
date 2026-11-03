package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T, handler http.HandlerFunc) *store {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &store{baseURL: srv.URL, client: srv.Client(), token: "t", tokenTill: time.Now().Add(time.Hour)}
}

func testJWT(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "h." + payload + ".s"
}

// PocketBase answers an expired token as a guest with an empty list, never 401,
// so the store must log in again on its own before the token runs out.
func TestStoreRenewsTokenBeforeExpiry(t *testing.T) {
	logins := 0
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth-with-password") {
			logins++
			fmt.Fprintf(w, `{"token":%q}`, testJWT(time.Now().Add(time.Hour)))
			return
		}
		if r.Header.Get("Authorization") == "stale" {
			io.WriteString(w, `{"page":1,"totalPages":0,"items":[]}`)
			return
		}
		io.WriteString(w, `{"page":1,"totalPages":1,"items":[{"id":"cfg"}]}`)
	})
	s.token, s.tokenTill = "stale", time.Now().Add(time.Minute)

	raw, err := s.firstRecord("discord_config", "dev_repo='o/dev'")
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil || logins != 1 {
		t.Fatalf("want a fresh login and the record, got %d login(s) and %s", logins, raw)
	}

	if _, err := s.firstRecord("discord_config", "dev_repo='o/dev'"); err != nil {
		t.Fatal(err)
	}
	if logins != 1 {
		t.Fatalf("a valid token was renewed again: %d logins", logins)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	if got := tokenExpiry(testJWT(exp)); !got.Equal(exp) {
		t.Fatalf("want %v, got %v", exp, got)
	}
	if got := tokenExpiry("not-a-jwt"); got.After(time.Now().Add(time.Hour+time.Minute)) || got.Before(time.Now()) {
		t.Fatalf("unreadable token should be kept for about an hour, got %v", got)
	}
}

func TestListRecordsPaginates(t *testing.T) {
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if r.URL.Query().Get("filter") != `repo='o/r'` {
			t.Errorf("filter not forwarded: %q", r.URL.Query().Get("filter"))
		}
		switch page {
		case "1":
			io.WriteString(w, `{"page":1,"totalPages":2,"items":[{"id":"a"},{"id":"b"}]}`)
		default:
			io.WriteString(w, `{"page":2,"totalPages":2,"items":[{"id":"c"}]}`)
		}
	})
	items, err := s.listRecords("discord_issues", "repo='o/r'")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || !strings.Contains(string(items[2]), `"c"`) {
		t.Fatalf("want 3 items across pages, got %d: %s", len(items), items)
	}
}

func TestConfigByDevRepo(t *testing.T) {
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[{"guild_id":"100000000000000001","dev_repo":"o/dev","testing_channel_id":"100000000000000002","allowed_role_ids":["5"],"target_repo":"o/main","enabled":true}]}`)
	})
	cfg, err := s.configByDevRepo("o/dev")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.GuildID != "100000000000000001" || cfg.TestingChannelID != "100000000000000002" || cfg.DevRepo != "o/dev" || !cfg.Enabled {
		t.Fatalf("got %+v", cfg)
	}
}

func TestThreadByIssueDecodesRow(t *testing.T) {
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"r1","guild_id":"1","thread_id":"2","repo":"o/dev","issue_number":7,"issue_url":"https://gh/i/7","kind":"testing","slug":"app","state":"testing","sync_enabled":true}]}`)
	})
	rec, err := s.threadByIssue("o/dev", 7)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.ThreadID != "2" || rec.IssueNumber != 7 || !rec.SyncEnabled || rec.Kind != "testing" {
		t.Fatalf("got %+v", rec)
	}
}

func TestSyncByCommentFilters(t *testing.T) {
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("filter"); f != "comment_id=42" {
			t.Errorf("filter %q", f)
		}
		io.WriteString(w, `{"items":[{"id":"s1","thread_id":"2","message_id":"9","comment_id":42,"origin":"github"}]}`)
	})
	rows, err := s.syncByComment(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MessageID != "9" || rows[0].Origin != "github" {
		t.Fatalf("got %+v", rows)
	}
}

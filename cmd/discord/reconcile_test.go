package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakeThreads struct {
	posts, pins []string
}

func (f *fakeThreads) selfID() string                               { return "100000000000000009" }
func (f *fakeThreads) create(_, _, _, _ string) (string, error)     { return "", nil }
func (f *fakeThreads) edit(_, _, _ string) error                    { return nil }
func (f *fakeThreads) remove(_, _ string) error                     { return nil }
func (f *fakeThreads) setState(_ string, _, _ bool, _ string) error { return nil }
func (f *fakeThreads) locked(string) (bool, error)                  { return false, nil }
func (f *fakeThreads) delete(string) error                          { return nil }
func (f *fakeThreads) exists(threadID string) bool                  { return threadID != "gone" }
func (f *fakeThreads) post(threadID, _ string) (string, error) {
	f.posts = append(f.posts, threadID)
	return "m" + threadID, nil
}
func (f *fakeThreads) pin(_, messageID string) error {
	f.pins = append(f.pins, messageID)
	return nil
}

func TestEnableRecordedThreadsSkipsDeletedOnes(t *testing.T) {
	var patched []string
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if f := r.URL.Query().Get("filter"); !strings.Contains(f, "sync_enabled=false") {
				t.Errorf("filter %q must ask for threads with mirroring off", f)
			}
			io.WriteString(w, `{"page":1,"totalPages":1,"items":[{"id":"r1","thread_id":"2","repo":"o/dev","issue_number":7,"issue_url":"https://gh/i/7","state":"testing"},{"id":"r2","thread_id":"gone","repo":"o/dev","issue_number":8,"issue_url":"https://gh/i/8","state":"testing"}]}`)
		case http.MethodPatch:
			patched = append(patched, strings.TrimPrefix(r.URL.Path, "/api/collections/discord_issues/records/"))
			io.WriteString(w, `{}`)
		}
	})
	threads := &fakeThreads{}
	b := &bot{store: s, threads: threads, index: newSyncIndex()}
	n, err := b.enableRecordedThreads()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(patched) != 1 || patched[0] != "r1" {
		t.Fatalf("want only r1 enabled, got n=%d patched=%v", n, patched)
	}
	if len(threads.posts) != 1 || threads.posts[0] != "2" || len(threads.pins) != 1 || threads.pins[0] != "m2" {
		t.Fatalf("want one pinned notice in thread 2, got posts %v pins %v", threads.posts, threads.pins)
	}
	if _, ok := b.index.get("2"); !ok {
		t.Fatal("enabled thread must be mirrored right away")
	}
}

func TestRecordMarkedThreadsCreatesRowsWithMirroringOn(t *testing.T) {
	var created map[string]any
	s := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "discord_config"):
			io.WriteString(w, `{"page":1,"totalPages":1,"items":[{"guild_id":"100000000000000001","dev_repo":"o/dev","testing_channel_id":"100000000000000002","target_repo":"o/main"}]}`)
		case r.Method == http.MethodGet:
			io.WriteString(w, `{"page":1,"totalPages":1,"items":[]}`)
		case r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&created)
			io.WriteString(w, `{"id":"n1","thread_id":"5","repo":"o/dev","issue_number":7,"issue_url":"https://gh/i/7","kind":"testing","state":"testing","sync_enabled":true}`)
		}
	})
	g := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
			io.WriteString(w, `[{"id":1,"body":"thread\n<!-- discord-thread-id: 5 -->","user":{"login":"github-actions"}}]`)
		case strings.HasSuffix(r.URL.Path, "/issues"):
			if r.URL.Query().Get("page") != "1" {
				io.WriteString(w, "[]")
				return
			}
			io.WriteString(w, `[{"number":7,"title":"App","html_url":"https://gh/i/7","labels":[{"name":"Ready For Testing"}]}]`)
		}
	})
	threads := &fakeThreads{}
	b := &bot{store: s, github: g, threads: threads, index: newSyncIndex()}
	n, err := b.recordMarkedThreads()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || created["thread_id"] != "5" || created["sync_enabled"] != true || created["kind"] != "testing" {
		t.Fatalf("want one testing row with mirroring on, got n=%d row=%v", n, created)
	}
	if len(threads.pins) != 1 || threads.pins[0] != "m5" {
		t.Fatalf("want the notice pinned in thread 5, got %v", threads.pins)
	}
	if _, ok := b.index.get("5"); !ok {
		t.Fatal("recorded thread must be mirrored right away")
	}
}

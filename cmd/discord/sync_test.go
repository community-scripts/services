package main

import "testing"

func TestSyncIndexSetGetDrop(t *testing.T) {
	idx := newSyncIndex()
	if _, ok := idx.get("1"); ok {
		t.Fatal("empty index must miss")
	}
	idx.set(threadRecord{ThreadID: "1", Repo: "o/r", IssueNumber: 3, SyncEnabled: true})
	rec, ok := idx.get("1")
	if !ok || rec.IssueNumber != 3 {
		t.Fatalf("got %+v %v", rec, ok)
	}
	idx.set(threadRecord{ThreadID: "2", SyncEnabled: false})
	if _, ok := idx.get("2"); ok {
		t.Fatal("rows with sync off must not be indexed")
	}
	idx.drop("1")
	if _, ok := idx.get("1"); ok {
		t.Fatal("dropped row still present")
	}
}

func TestSyncIndexReplace(t *testing.T) {
	idx := newSyncIndex()
	idx.set(threadRecord{ThreadID: "1", SyncEnabled: true})
	idx.replace([]threadRecord{{ThreadID: "2", SyncEnabled: true}})
	if _, ok := idx.get("1"); ok {
		t.Fatal("replace must drop stale rows")
	}
	if _, ok := idx.get("2"); !ok {
		t.Fatal("replace must add new rows")
	}
}

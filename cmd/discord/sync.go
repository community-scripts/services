package main

import "sync"

type syncIndex struct {
	mu       sync.RWMutex
	byThread map[string]threadRecord
}

func newSyncIndex() *syncIndex {
	return &syncIndex{byThread: map[string]threadRecord{}}
}

func (x *syncIndex) get(threadID string) (threadRecord, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	rec, ok := x.byThread[threadID]
	return rec, ok
}

func (x *syncIndex) set(rec threadRecord) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if rec.SyncEnabled {
		x.byThread[rec.ThreadID] = rec
	} else {
		delete(x.byThread, rec.ThreadID)
	}
}

func (x *syncIndex) drop(threadID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.byThread, threadID)
}

func (x *syncIndex) replace(recs []threadRecord) {
	next := make(map[string]threadRecord, len(recs))
	for _, rec := range recs {
		if rec.SyncEnabled {
			next[rec.ThreadID] = rec
		}
	}
	x.mu.Lock()
	x.byThread = next
	x.mu.Unlock()
}

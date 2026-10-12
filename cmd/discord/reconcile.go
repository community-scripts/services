package main

import "log"

// Threads the GitHub Action made, and rows that predate always-on mirroring,
// are brought up to date once after login.
func (b *bot) reconcileThreads() {
	enabled, err := b.enableRecordedThreads()
	if err != nil {
		log.Printf("discord bot: reconcile rows: %v", err)
	}
	added, err := b.recordMarkedThreads()
	if err != nil {
		log.Printf("discord bot: reconcile markers: %v", err)
	}
	log.Printf("discord bot: reconcile: mirroring turned on for %d thread(s), %d thread(s) recorded", enabled, added)
}

func (b *bot) enableRecordedThreads() (int, error) {
	raws, err := b.store.listRecords("discord_issues", "kind='testing' && sync_enabled=false && (state='testing' || state='hold')")
	if err != nil {
		return 0, err
	}
	recs, err := decodeList[threadRecord](raws)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range recs {
		if !b.threads.exists(rec.ThreadID) {
			continue
		}
		if err := b.store.updateIssueRecord(rec.ID, map[string]any{"sync_enabled": true}); err != nil {
			return n, err
		}
		rec.SyncEnabled = true
		b.index.set(rec)
		b.pinMirrorNotice(rec.ThreadID, rec.IssueURL, false)
		n++
	}
	return n, nil
}

func (b *bot) recordMarkedThreads() (int, error) {
	configs, err := b.store.allConfigs()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, cfg := range configs {
		if cfg.DevRepo == "" {
			continue
		}
		issues, err := b.github.listIssues(cfg.DevRepo, "open")
		if err != nil {
			return n, err
		}
		for _, issue := range issues {
			rec, err := b.store.threadByIssue(cfg.DevRepo, issue.Number)
			if err != nil {
				return n, err
			}
			if rec != nil {
				continue
			}
			comments, err := b.github.comments(cfg.DevRepo, issue.Number)
			if err != nil {
				return n, err
			}
			id := markerFromComments(comments)
			if id == "" || !b.threads.exists(id) {
				continue
			}
			if _, err := b.recordMarkedThread(&cfg, issue, id); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// A testing thread is mirrored while it is open for testing; locked ones are not.
func (b *bot) recordMarkedThread(cfg *guildSettings, issue ghIssue, threadID string) (*threadRecord, error) {
	state := stateFromLabels(issue.labelNames())
	sync := state == "testing" || state == "hold"
	lc := &lifecycleContext{cfg: cfg, repo: cfg.DevRepo, issue: issue}
	rec, err := b.store.recordIssue(b.threadRow(lc, threadID, state, sync))
	if err != nil {
		return nil, err
	}
	log.Printf("discord bot: recorded %s #%d -> thread %s (%s)", cfg.DevRepo, issue.Number, threadID, state)
	if sync {
		b.index.set(*rec)
		b.pinMirrorNotice(threadID, issue.HTMLURL, false)
	}
	return rec, nil
}

func (b *bot) pinMirrorNotice(threadID, issueURL string, canStop bool) {
	mid, err := b.threads.post(threadID, mirrorOnNotice(issueURL, canStop))
	if err != nil {
		log.Printf("discord bot: notice in %s: %v", threadID, err)
		return
	}
	if err := b.threads.pin(threadID, mid); err != nil {
		log.Printf("discord bot: pin notice in %s: %v", threadID, err)
	}
}

package main

import (
	"flag"
	"fmt"
	"log"
)

// backfill records rows for testing threads the GitHub Action created, found
// through the thread-id marker it left in the issue comments.
func (b *bot) runBackfill(args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	enableSync := fs.Bool("enable-sync", false, "turn mirroring on for every backfilled thread and pin the notice")
	if err := fs.Parse(args); err != nil {
		return err
	}

	configs, err := b.store.allConfigs()
	if err != nil {
		return err
	}
	added, known := 0, 0
	for _, cfg := range configs {
		if cfg.DevRepo == "" {
			continue
		}
		issues, err := b.github.listIssues(cfg.DevRepo, "open")
		if err != nil {
			return err
		}
		for _, issue := range issues {
			rec, err := b.store.threadByIssue(cfg.DevRepo, issue.Number)
			if err != nil {
				return err
			}
			if rec != nil {
				known++
				continue
			}
			comments, err := b.github.comments(cfg.DevRepo, issue.Number)
			if err != nil {
				return err
			}
			id := markerFromComments(comments)
			if id == "" || !b.threads.exists(id) {
				continue
			}
			lc := &lifecycleContext{cfg: &cfg, repo: cfg.DevRepo, issue: issue}
			rec, err = b.store.recordIssue(b.threadRow(lc, id, stateFromLabels(issue.labelNames()), *enableSync))
			if err != nil {
				return err
			}
			added++
			log.Printf("backfill: %s #%d -> thread %s (%s)", cfg.DevRepo, issue.Number, id, rec.State)
			if !*enableSync {
				continue
			}
			mid, err := b.threads.post(id, mirrorOnNotice(issue.HTMLURL))
			if err != nil {
				log.Printf("backfill: notice in %s: %v", id, err)
				continue
			}
			if err := b.threads.pin(id, mid); err != nil {
				log.Printf("backfill: pin notice in %s: %v", id, err)
			}
		}
	}
	fmt.Printf("backfill: %d thread(s) recorded, %d already known\n", added, known)
	return nil
}

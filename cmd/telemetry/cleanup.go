package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// CleanupConfig holds configuration for the cleanup job
type CleanupConfig struct {
	Enabled          bool
	CheckInterval    time.Duration // How often to run cleanup
	StuckAfterHours  int           // Consider "installing" as stuck after X hours
	RetentionDays    int           // Delete records older than X days (0 = disabled)
	RetentionEnabled bool          // Enable automatic data retention/deletion
}

// Cleaner handles cleanup of stuck installations
type Cleaner struct {
	cfg CleanupConfig
	ch  *CHClient
}

// NewCleaner creates a new cleaner instance
func NewCleaner(cfg CleanupConfig, ch *CHClient) *Cleaner {
	return &Cleaner{
		cfg: cfg,
		ch:  ch,
	}
}

// Start begins the cleanup loop
func (c *Cleaner) Start() {
	if !c.cfg.Enabled {
		log.Println("INFO: cleanup job disabled")
		return
	}

	go c.cleanupLoop()
	log.Printf("INFO: cleanup job started (interval: %v, stuck after: %d hours)", c.cfg.CheckInterval, c.cfg.StuckAfterHours)

	// Start retention job if enabled
	if c.cfg.RetentionEnabled && c.cfg.RetentionDays > 0 {
		go c.retentionLoop()
		log.Printf("INFO: data retention job started (delete after: %d days)", c.cfg.RetentionDays)
	}
}

func (c *Cleaner) cleanupLoop() {
	// Run immediately on start
	c.runCleanup()

	ticker := time.NewTicker(c.cfg.CheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		c.runCleanup()
	}
}

// runCleanup finds and updates stuck installations
func (c *Cleaner) runCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	stuckRecords, err := c.ch.FindStuckInstallations(ctx, c.cfg.StuckAfterHours)
	if err != nil {
		log.Printf("WARN: cleanup - failed to find stuck installations: %v", err)
		return
	}

	if len(stuckRecords) == 0 {
		log.Printf("INFO: cleanup - no stuck installations found")
		return
	}

	log.Printf("INFO: cleanup - found %d stuck installations (older than %dh)", len(stuckRecords), c.cfg.StuckAfterHours)

	updated := 0
	for _, record := range stuckRecords {
		if err := c.ch.MarkRecordAsUnknown(ctx, record, c.cfg.StuckAfterHours); err != nil {
			log.Printf("WARN: cleanup - failed to update record %s (%s): %v", record.ID, record.NSAPP, err)
			continue
		}
		updated++
	}

	log.Printf("INFO: cleanup - updated %d/%d stuck installations to 'unknown'", updated, len(stuckRecords))
}

// StuckRecord represents a minimal record for cleanup
type StuckRecord struct {
	ID      string `json:"id"`
	NSAPP   string `json:"nsapp"`
	Created string `json:"created"`
}

// RunNow triggers an immediate cleanup run (for testing/manual trigger)
func (c *Cleaner) RunNow() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stuckRecords, err := c.ch.FindStuckInstallations(ctx, c.cfg.StuckAfterHours)
	if err != nil {
		return 0, fmt.Errorf("failed to find stuck installations: %w", err)
	}

	updated := 0
	for _, record := range stuckRecords {
		if err := c.ch.MarkRecordAsUnknown(ctx, record, c.cfg.StuckAfterHours); err != nil {
			log.Printf("WARN: cleanup - failed to update record %s: %v", record.ID, err)
			continue
		}
		updated++
	}

	return updated, nil
}

// GetStuckCount returns the current number of stuck installations
func (c *Cleaner) GetStuckCount(ctx context.Context) (int, error) {
	return c.ch.GetStuckCount(ctx, c.cfg.StuckAfterHours)
}

// =============================================
// DATA RETENTION (GDPR Löschkonzept)
// =============================================

// retentionLoop runs the data retention job periodically (once per day)
func (c *Cleaner) retentionLoop() {
	// Run once on startup after a delay
	time.Sleep(5 * time.Minute)
	c.runRetention()

	// Run daily at 3:00 AM
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		c.runRetention()
	}
}

// runRetention deletes records older than RetentionDays
func (c *Cleaner) runRetention() {
	if c.cfg.RetentionDays <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Printf("INFO: retention - starting cleanup of records older than %d days", c.cfg.RetentionDays)

	if err := c.ch.DeleteOldRecords(ctx, c.cfg.RetentionDays); err != nil {
		log.Printf("WARN: retention - failed to delete old records: %v", err)
		return
	}

	log.Printf("INFO: retention - DELETE mutation submitted for records older than %d days", c.cfg.RetentionDays)
}

// GetRetentionStats returns statistics about records eligible for deletion
func (c *Cleaner) GetRetentionStats(ctx context.Context) (eligible int, oldestDate string, err error) {
	if c.cfg.RetentionDays <= 0 {
		return 0, "", nil
	}

	cutoff := time.Now().AddDate(0, 0, -c.cfg.RetentionDays)
	var cnt uint64
	if err := c.ch.db.QueryRowContext(ctx,
		"SELECT count() FROM telemetry_db.telemetry WHERE created < ?", cutoff,
	).Scan(&cnt); err != nil {
		return 0, "", err
	}

	var oldest string
	_ = c.ch.db.QueryRowContext(ctx,
		"SELECT toString(min(created)) FROM telemetry_db.telemetry",
	).Scan(&oldest)
	if len(oldest) >= 10 {
		oldest = oldest[:10]
	}

	return int(cnt), oldest, nil
}

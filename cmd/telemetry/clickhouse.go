package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

// CHClient wraps a ClickHouse database/sql connection for telemetry reads and writes.
type CHClient struct {
	db *sql.DB
}

// NewCHClient connects to ClickHouse using a DSN like
// "clickhouse://user:pass@host:9000/telemetry_db" or "http://user:pass@host:8123/telemetry_db".
// Automatically converts clickhouse:// to http:// with port 8123 for better compatibility.
// Retries with exponential backoff for up to ~60s to handle container startup ordering.
func NewCHClient(dsn string) (*CHClient, error) {
	// Convert native protocol DSN to HTTP for better compatibility
	if strings.HasPrefix(dsn, "clickhouse://") {
		dsn = convertToHTTPDSN(dsn)
		log.Printf("[CH] Converted DSN to HTTP protocol")
	}

	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)

	// Retry ping with exponential backoff (containers may start before ClickHouse is ready)
	backoff := []time.Duration{1, 2, 4, 8, 16, 30}
	var pingErr error
	for i, wait := range backoff {
		if pingErr = db.PingContext(context.Background()); pingErr == nil {
			break
		}
		log.Printf("[CH] ping attempt %d/%d failed: %v (retry in %ds)", i+1, len(backoff), pingErr, wait)
		time.Sleep(wait * time.Second)
	}
	if pingErr != nil {
		db.Close()
		return nil, fmt.Errorf("clickhouse ping (after %d retries): %w", len(backoff), pingErr)
	}

	log.Println("[CH] Connected to ClickHouse")
	ch := &CHClient{db: db}
	ch.migrate()
	return ch, nil
}

// convertToHTTPDSN converts a clickhouse:// native protocol DSN to http:// protocol DSN
// Example: "clickhouse://admin:pass@10.0.1.11:9000/db" → "http://admin:pass@10.0.1.11:8123/db"
func convertToHTTPDSN(dsn string) string {
	// Replace clickhouse:// with http://
	dsn = strings.Replace(dsn, "clickhouse://", "http://", 1)

	// Replace :9000 with :8123 (native protocol port to HTTP port)
	// Be careful to only replace the port after the host, not in any other field
	dsn = strings.Replace(dsn, ":9000/", ":8123/", 1)

	return dsn
}

// migrate creates the database, tables, and materialized views if they don't exist.
func (ch *CHClient) migrate() {
	ctx := context.Background()
	stmts := []string{
		`CREATE DATABASE IF NOT EXISTS telemetry_db`,

		// ── Main telemetry table ──
		`CREATE TABLE IF NOT EXISTS telemetry_db.telemetry (
			id               String,
			nsapp            String,
			type             String,
			status           String,
			method           String,
			created          DateTime64(3),
			core_count       UInt8,
			ct_type          UInt8,
			disk_size        UInt32,
			ram_size         UInt32,
			exit_code        Int16,
			error            String,
			error_category   String,
			os_type          String,
			os_version       String,
			pve_version      String,
			random_id        String,
			execution_id     String,
			repo_source      String,
			repo_slug        String,
			cpu_vendor       String,
			cpu_model        String,
			gpu_vendor       String,
			gpu_model        String,
			gpu_passthrough  String,
			ram_speed        String,
			install_duration UInt32,
			has_arm          UInt8
		) ENGINE = MergeTree()
		ORDER BY (created, nsapp)
		PARTITION BY toYYYYMM(created)`,

		// ── Materialized view: daily stats per app ──
		// Pre-aggregates counts per (date, nsapp, type, status, repo_source).
		// Queries GROUP BY date/app become instant.
		`CREATE TABLE IF NOT EXISTS telemetry_db.mv_daily_stats (
			day              Date,
			nsapp            String,
			type             String,
			repo_source      String,
			total            UInt64,
			success          UInt64,
			failed           UInt64,
			aborted          UInt64,
			installing       UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (day, nsapp, type, repo_source)
		PARTITION BY toYYYYMM(day)`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_db.mv_daily_stats_view
		TO telemetry_db.mv_daily_stats AS
		SELECT
			toDate(created) AS day,
			nsapp,
			type,
			repo_source,
			count()                        AS total,
			countIf(status='success')      AS success,
			countIf(status='failed')       AS failed,
			countIf(status='aborted')      AS aborted,
			countIf(status IN ('installing','validation','configuring')) AS installing
		FROM telemetry_db.telemetry
		GROUP BY day, nsapp, type, repo_source`,

		// ── Materialized view: daily OS distribution ──
		`CREATE TABLE IF NOT EXISTS telemetry_db.mv_daily_os (
			day         Date,
			repo_source String,
			os_type     String,
			cnt         UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (day, repo_source, os_type)
		PARTITION BY toYYYYMM(day)`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_db.mv_daily_os_view
		TO telemetry_db.mv_daily_os AS
		SELECT
			toDate(created) AS day,
			repo_source,
			os_type,
			count() AS cnt
		FROM telemetry_db.telemetry
		WHERE os_type != '' AND status IN ('success','failed','aborted','unknown')
		GROUP BY day, repo_source, os_type`,

		// ── Materialized view: daily method distribution ──
		`CREATE TABLE IF NOT EXISTS telemetry_db.mv_daily_method (
			day         Date,
			repo_source String,
			method      String,
			cnt         UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (day, repo_source, method)
		PARTITION BY toYYYYMM(day)`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_db.mv_daily_method_view
		TO telemetry_db.mv_daily_method AS
		SELECT
			toDate(created) AS day,
			repo_source,
			method,
			count() AS cnt
		FROM telemetry_db.telemetry
		WHERE method != '' AND status IN ('success','failed','aborted','unknown')
		GROUP BY day, repo_source, method`,

		// ── Materialized view: daily PVE version distribution ──
		`CREATE TABLE IF NOT EXISTS telemetry_db.mv_daily_pve (
			day         Date,
			repo_source String,
			pve_version String,
			cnt         UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (day, repo_source, pve_version)
		PARTITION BY toYYYYMM(day)`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_db.mv_daily_pve_view
		TO telemetry_db.mv_daily_pve AS
		SELECT
			toDate(created) AS day,
			repo_source,
			pve_version,
			count() AS cnt
		FROM telemetry_db.telemetry
		WHERE pve_version != '' AND status IN ('success','failed','aborted','unknown')
		GROUP BY day, repo_source, pve_version`,

		// ── Materialized view: daily errors (excludes user_aborted) ──
		// Pre-aggregates real failures per (date, app, exit_code, error_category).
		// user_aborted (SIGHUP/SIGINT from closed terminals) is noise, not errors.
		`CREATE TABLE IF NOT EXISTS telemetry_db.mv_daily_errors (
			day            Date,
			nsapp          String,
			type           String,
			exit_code      Int16,
			error_category String,
			repo_source    String,
			cnt            UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (day, nsapp, exit_code, error_category, repo_source)
		PARTITION BY toYYYYMM(day)`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_db.mv_daily_errors_view
		TO telemetry_db.mv_daily_errors AS
		SELECT
			toDate(created) AS day,
			nsapp,
			type,
			exit_code,
			error_category,
			repo_source,
			count() AS cnt
		FROM telemetry_db.telemetry
		WHERE status = 'failed'
		  AND error_category != 'user_aborted'
		  AND exit_code != 0
		GROUP BY day, nsapp, type, exit_code, error_category, repo_source`,
	}

	for _, s := range stmts {
		if _, err := ch.db.ExecContext(ctx, s); err != nil {
			log.Printf("[CH-MIGRATE] %v", err)
		}
	}

	// Additive column migrations for tables that predate the column (idempotent).
	alters := []string{
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS repo_slug String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS has_arm UInt8`,
		// Virtualization platform: 'pve' (Proxmox VE) or 'incus'. Old rows have ''
		// and are derived at query time from pve_version (see platformExpr).
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS platform LowCardinality(String)`,

		// Payload schema version. Rows written before the engine started sending
		// it keep 0, which is how "an old client" is spelled. Previously the only
		// way to guess a client's age was which fields happened to be present,
		// and that is guesswork rather than a contract.
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS payload_version UInt16`,

		// The engine has always sent arch and it was never stored, so every row
		// ever written dropped it on the floor. has_arm was the only substitute
		// and it cannot tell amd64 from riscv64.
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS arch LowCardinality(String)`,

		// Structured failure location. The engine already had both as values and
		// only ever flattened them into the free-text error, where grouping by
		// the failing command means parsing prose.
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS failed_command String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS failed_line UInt32`,

		// Collected by the engine, never transmitted until payload version 2.
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS kernel_version String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS app_version String`,
	}
	for _, s := range alters {
		if _, err := ch.db.ExecContext(ctx, s); err != nil {
			log.Printf("[CH-MIGRATE] alter: %v", err)
		}
	}

	// Backfill materialized views from existing data if they're empty
	var mvCount uint64
	_ = ch.db.QueryRowContext(ctx, "SELECT count() FROM telemetry_db.mv_daily_stats").Scan(&mvCount)
	if mvCount == 0 {
		var srcCount uint64
		_ = ch.db.QueryRowContext(ctx, "SELECT count() FROM telemetry_db.telemetry").Scan(&srcCount)
		if srcCount > 0 {
			log.Printf("[CH-MIGRATE] Backfilling materialized views from %d existing rows...", srcCount)
			backfills := []string{
				`INSERT INTO telemetry_db.mv_daily_stats
				SELECT toDate(created), nsapp, type, repo_source,
					count(), countIf(status='success'), countIf(status='failed'),
					countIf(status='aborted'),
					countIf(status IN ('installing','validation','configuring'))
				FROM telemetry_db.telemetry GROUP BY toDate(created), nsapp, type, repo_source`,

				`INSERT INTO telemetry_db.mv_daily_os
				SELECT toDate(created), repo_source, os_type, count()
				FROM telemetry_db.telemetry
				WHERE os_type != '' AND status IN ('success','failed','aborted','unknown')
				GROUP BY toDate(created), repo_source, os_type`,

				`INSERT INTO telemetry_db.mv_daily_method
				SELECT toDate(created), repo_source, method, count()
				FROM telemetry_db.telemetry
				WHERE method != '' AND status IN ('success','failed','aborted','unknown')
				GROUP BY toDate(created), repo_source, method`,

				`INSERT INTO telemetry_db.mv_daily_pve
				SELECT toDate(created), repo_source, pve_version, count()
				FROM telemetry_db.telemetry
				WHERE pve_version != '' AND status IN ('success','failed','aborted','unknown')
				GROUP BY toDate(created), repo_source, pve_version`,
			}
			for _, q := range backfills {
				if _, err := ch.db.ExecContext(ctx, q); err != nil {
					log.Printf("[CH-MIGRATE] backfill error: %v", err)
				}
			}
			log.Println("[CH-MIGRATE] Backfill complete")
		}
	}

	// Backfill mv_daily_errors separately (may be added after initial deploy)
	var errMVCount uint64
	_ = ch.db.QueryRowContext(ctx, "SELECT count() FROM telemetry_db.mv_daily_errors").Scan(&errMVCount)
	if errMVCount == 0 {
		var srcCount uint64
		_ = ch.db.QueryRowContext(ctx, "SELECT count() FROM telemetry_db.telemetry WHERE status='failed' AND error_category!='user_aborted' AND exit_code!=0").Scan(&srcCount)
		if srcCount > 0 {
			log.Printf("[CH-MIGRATE] Backfilling mv_daily_errors from %d error rows...", srcCount)
			_, err := ch.db.ExecContext(ctx, `INSERT INTO telemetry_db.mv_daily_errors
				SELECT toDate(created), nsapp, type, exit_code, error_category, repo_source, count()
				FROM telemetry_db.telemetry
				WHERE status = 'failed' AND error_category != 'user_aborted' AND exit_code != 0
				GROUP BY toDate(created), nsapp, type, exit_code, error_category, repo_source`)
			if err != nil {
				log.Printf("[CH-MIGRATE] mv_daily_errors backfill error: %v", err)
			} else {
				log.Println("[CH-MIGRATE] mv_daily_errors backfill complete")
			}
		}
	}
	log.Println("[CH-MIGRATE] Schema ready")
}

// ApplyRetention sets or clears a TTL on the raw telemetry table.
//
// ClickHouse enforces this during its normal merges, so there is no job to run
// and nothing to schedule -- unlike the delete-based cleanup, which walks the
// table. With days <= 0 nothing is touched and the table keeps growing without
// bound, which is the current state and is why this exists.
//
// Called explicitly rather than folded into migrate(): every other migration is
// additive and safe to run blind, and this one deletes history.
func (ch *CHClient) ApplyRetention(ctx context.Context, days int) {
	if days <= 0 {
		log.Println("[CH-RETENTION] no retention configured; the telemetry table grows without bound " +
			"(set TELEMETRY_RETENTION_DAYS to change this)")
		return
	}
	q := fmt.Sprintf(
		"ALTER TABLE telemetry_db.telemetry MODIFY TTL created + INTERVAL %d DAY", days)
	if _, err := ch.db.ExecContext(ctx, q); err != nil {
		log.Printf("[CH-RETENTION] could not set %d-day TTL: %v", days, err)
		return
	}
	log.Printf("[CH-RETENTION] rows older than %d days will be dropped during merges", days)
}

func (ch *CHClient) Close() error                   { return ch.db.Close() }
func (ch *CHClient) Ping(ctx context.Context) error { return ch.db.PingContext(ctx) }

func generateRecordID() string {
	b := make([]byte, 15)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ══════════════════════════════════════════════════════════════
//  WRITE OPERATIONS
// ══════════════════════════════════════════════════════════════

func (ch *CHClient) InsertTelemetry(ctx context.Context, p TelemetryOut) error {
	const q = `INSERT INTO telemetry_db.telemetry (
		id, nsapp, type, status, method, created,
		core_count, ct_type, disk_size, ram_size,
		exit_code, error, error_category,
		os_type, os_version, pve_version,
		random_id, execution_id, repo_source, repo_slug,
		cpu_vendor, cpu_model,
		gpu_vendor, gpu_model, gpu_passthrough,
		ram_speed, install_duration, has_arm, platform,
		payload_version, arch, failed_command, failed_line,
		kernel_version, app_version
	) VALUES (
		?, ?, ?, ?, ?, now64(3),
		?, ?, ?, ?,
		?, ?, ?,
		?, ?, ?,
		?, ?, ?, ?,
		?, ?,
		?, ?, ?,
		?, ?, ?, ?,
		?, ?, ?, ?,
		?, ?
	)`
	_, err := ch.db.ExecContext(ctx, q,
		generateRecordID(), p.NSAPP, p.Type, p.Status, p.Method,
		uint8(p.CoreCount), uint8(p.CTType), uint32(p.DiskSize), uint32(p.RAMSize),
		int16(p.ExitCode), p.Error, p.ErrorCategory,
		p.OsType, p.OsVersion, p.PveVer,
		p.RandomID, p.ExecutionID, p.RepoSource, p.RepoSlug,
		p.CPUVendor, p.CPUModel,
		p.GPUVendor, p.GPUModel, p.GPUPassthrough,
		p.RAMSpeed, uint32(p.InstallDuration), boolToUint8(p.HasArm), p.Platform,
		uint16(p.PayloadVersion), p.Arch, p.FailedCommand, uint32(p.FailedLine),
		p.KernelVersion, p.AppVersion,
	)
	return err
}

// boolToUint8 maps a Go bool to ClickHouse's UInt8 (0/1) representation.
func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// HasTerminalExecutionID reports whether a terminal-status row already exists for
// the given execution_id. Used by the write queue to deduplicate the multiple
// failure reports a single bash execution can emit.
func (ch *CHClient) HasTerminalExecutionID(ctx context.Context, eid string) (bool, error) {
	if eid == "" {
		return false, nil
	}
	var cnt uint64
	err := ch.db.QueryRowContext(ctx,
		"SELECT count() FROM telemetry_db.telemetry WHERE execution_id = ? AND status IN ('success','failed','aborted','unknown')", eid,
	).Scan(&cnt)
	return cnt > 0, err
}

func (ch *CHClient) HasExecutionID(ctx context.Context, eid string) (bool, error) {
	var cnt uint64
	err := ch.db.QueryRowContext(ctx,
		"SELECT count() FROM telemetry_db.telemetry WHERE execution_id = ?", eid,
	).Scan(&cnt)
	return cnt > 0, err
}

// ══════════════════════════════════════════════════════════════
//  QUERY HELPERS
// ══════════════════════════════════════════════════════════════

func chSinceTime(days int) time.Time {
	if days == 1 {
		return time.Now().UTC().Truncate(24 * time.Hour)
	}
	return time.Now().UTC().AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
}

// repoSourcePred returns a SQL predicate (and args) for a repo_source filter.
//
// Legacy rows created before repo_source existed have an empty repo_source.
// Since the production client's fallback is "ProxmoxVE" (CI rewrites
// ProxmoxVED→ProxmoxVE on promotion), those untagged historical installs are
// overwhelmingly production traffic. The "ProxmoxVE" filter therefore also
// includes empty repo_source so the count reflects real production volume
// instead of dropping ~70% of historical records into an invisible bucket.
//
// An empty repoSource means "all" (no predicate). Other values match exactly.
func repoSourcePred(repoSource string) (string, []interface{}) {
	switch repoSource {
	case "":
		return "", nil
	case "ProxmoxVE":
		return "repo_source IN ('ProxmoxVE','')", nil
	default:
		return "repo_source = ?", []interface{}{repoSource}
	}
}

// platformExpr derives the virtualization platform for every row, including
// legacy rows written before the platform column existed:
//   - explicit platform column wins
//   - pve_version starting with "incus" (client sends "incus-x.y") → incus
//   - any other non-empty pve_version → pve
//   - otherwise unknown (”)
const platformExpr = `multiIf(platform != '', platform, pve_version LIKE 'incus%', 'incus', pve_version != '', 'pve', '')`

// platformPred returns a SQL predicate for a platform filter ("" = no filter).
// Values are allowlisted by the HTTP layer, so inlining is safe.
func platformPred(platform string) string {
	switch platform {
	case "pve", "incus":
		return platformExpr + " = '" + platform + "'"
	}
	return ""
}

// chWhere builds a WHERE clause from days, repoSource, optional repoSlug,
// optional platform, and extra predicates.
// Always starts with "1=1" so callers can freely AND-chain.
func chWhere(days int, repoSource, repoSlug, platform string, extras ...string) (string, []interface{}) {
	parts := []string{"1=1"}
	var args []interface{}

	if days > 0 {
		parts = append(parts, "created >= ?")
		args = append(args, chSinceTime(days))
	}
	if pred, pArgs := repoSourcePred(repoSource); pred != "" {
		parts = append(parts, pred)
		args = append(args, pArgs...)
	}
	if repoSlug != "" {
		parts = append(parts, "repo_slug = ?")
		args = append(args, repoSlug)
	}
	if pred := platformPred(platform); pred != "" {
		parts = append(parts, pred)
	}
	for _, e := range extras {
		parts = append(parts, e)
	}
	return strings.Join(parts, " AND "), args
}

// chSinceDate returns the date for filtering materialized views (Date column, not DateTime64).
func chSinceDate(days int) string {
	if days == 1 {
		return time.Now().UTC().Truncate(24 * time.Hour).Format("2006-01-02")
	}
	return time.Now().UTC().AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour).Format("2006-01-02")
}

// chMVWhere builds a WHERE clause for materialized view tables (uses "day" Date column).
func chMVWhere(days int, repoSource string) (string, []interface{}) {
	parts := []string{"1=1"}
	var args []interface{}
	if days > 0 {
		parts = append(parts, "day >= ?")
		args = append(args, chSinceDate(days))
	}
	if pred, pArgs := repoSourcePred(repoSource); pred != "" {
		parts = append(parts, pred)
		args = append(args, pArgs...)
	}
	return strings.Join(parts, " AND "), args
}

// scanRecords reads TelemetryRecord rows from a *sql.Rows.
func scanRecords(rows *sql.Rows) []TelemetryRecord {
	var out []TelemetryRecord
	for rows.Next() {
		var r TelemetryRecord
		var coreCount, ctType, hasArm uint8
		var diskSize, ramSize, installDur uint32
		var exitCode int16
		err := rows.Scan(
			&r.NSAPP, &r.Type, &r.Status, &r.Method,
			&coreCount, &ctType, &diskSize, &ramSize,
			&exitCode, &r.Error, &r.ErrorCategory,
			&r.OsType, &r.OsVersion, &r.PveVer,
			&r.RandomID, &r.ExecutionID, &r.RepoSource, &r.RepoSlug,
			&r.CPUVendor, &r.CPUModel,
			&r.GPUVendor, &r.GPUModel, &r.GPUPassthrough,
			&r.RAMSpeed, &installDur, &hasArm,
			&r.Platform,
			&r.Created,
		)
		if err != nil {
			log.Printf("[CH] row scan: %v", err)
			continue
		}
		r.CoreCount = int(coreCount)
		r.CTType = int(ctType)
		r.DiskSize = int(diskSize)
		r.RAMSize = int(ramSize)
		r.ExitCode = int(exitCode)
		r.InstallDuration = int(installDur)
		r.HasArm = hasArm != 0
		out = append(out, r)
	}
	return out
}

// recordSelectCols is the column list shared by all queries that return TelemetryRecord.
const recordSelectCols = `nsapp, type, status, method,
	core_count, ct_type, disk_size, ram_size,
	exit_code, error, error_category,
	os_type, os_version, pve_version,
	random_id, execution_id, repo_source, repo_slug,
	cpu_vendor, cpu_model,
	gpu_vendor, gpu_model, gpu_passthrough,
	ram_speed, install_duration, has_arm,
	` + platformExpr + ` AS platform_resolved,
	toString(created)`

// ══════════════════════════════════════════════════════════════
//  DASHBOARD DATA (SQL aggregation — replaces PB pagination)
// ══════════════════════════════════════════════════════════════

// FetchRepoSlugs returns distinct owner/repo slugs with install counts for the filter dropdown.
func (ch *CHClient) FetchRepoSlugs(ctx context.Context, days int, repoSource string) ([]RepoSlugCount, error) {
	w, a := chWhere(days, repoSource, "", "", "repo_slug != ''")
	rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT repo_slug, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY repo_slug ORDER BY c DESC LIMIT 50", w), a...)
	if err != nil {
		return nil, fmt.Errorf("CH repo slugs: %w", err)
	}
	defer rows.Close()
	var out []RepoSlugCount
	for rows.Next() {
		var rs RepoSlugCount
		var c uint64
		if rows.Scan(&rs.Slug, &c) == nil {
			rs.Count = int(c)
			out = append(out, rs)
		}
	}
	return out, nil
}

func (ch *CHClient) FetchDashboardData(ctx context.Context, days int, repoSource, repoSlug, platform string) (*DashboardData, error) {
	data := &DashboardData{}
	mw, ma := chMVWhere(days, repoSource)
	tw, ta := chWhere(days, repoSource, repoSlug, platform, "status IN ('success','failed','aborted','unknown')")
	// Materialized views have no platform column — platform filters need raw-table aggregation.
	rawAgg := repoSlug != "" || platform != ""

	// ── 1. Main counts ──
	// "Total" counts DISTINCT installations (one per execution), not raw event
	// rows. Every progress ping (validation/configuring) is its own ClickHouse
	// row, so a plain count() inflated the number ~5x vs success+failed+aborted
	// (e.g. "10,455 created" while only ~1,900 installs actually finished).
	var total, sc, fc, ac uint64
	var err error
	rw, ra := chWhere(days, repoSource, repoSlug, platform)
	err = ch.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT
			uniqExact(if(execution_id = '', random_id, execution_id)),
			countIf(status='success'), countIf(status='failed'), countIf(status='aborted')
		FROM telemetry_db.telemetry WHERE %s`, rw), ra...,
	).Scan(&total, &sc, &fc, &ac)
	if err != nil {
		return nil, fmt.Errorf("CH dashboard counts: %w", err)
	}
	data.TotalInstalls = int(total)
	data.SuccessCount = int(sc)
	data.FailedCount = int(fc)
	data.AbortedCount = int(ac)
	if sc+fc > 0 {
		data.SuccessRate = float64(sc) / float64(sc+fc) * 100
	}

	// Avg install duration (needs raw table — only non-zero durations)
	var avgDur float64
	_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT if(countIf(install_duration>0)>0,
			toFloat64(sumIf(install_duration, install_duration>0))/countIf(install_duration>0), 0)
		FROM telemetry_db.telemetry WHERE %s`, tw), ta...,
	).Scan(&avgDur)
	data.AvgInstallDuration = avgDur

	// Total all-time: distinct installations across the whole table.
	// uniq() is approximate (<1% error) but fast enough for a headline number.
	var tat uint64
	_ = ch.db.QueryRowContext(ctx,
		"SELECT uniq(if(execution_id = '', random_id, execution_id)) FROM telemetry_db.telemetry",
	).Scan(&tat)
	data.TotalAllTime = int(tat)
	data.SampleSize = data.TotalInstalls

	// ── 2. Installing count (raw table — execution_id subquery) ──
	//
	// Deliberately a fixed 24 hours, not the selected period: "still installing"
	// is a question about now, and counting everything that never finished over
	// a year answers a different one. The window is reported alongside the
	// figure so the page can say so -- it used to sit beside Failed and Aborted,
	// which do honour the selector, with nothing to mark it as different.
	data.InstallingWindowDays = 1
	stuckW, stuckA := chWhere(1, repoSource, repoSlug, platform,
		"status IN ('installing','validation','configuring')",
		`(execution_id = '' OR execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('success','failed','aborted','unknown') AND execution_id != ''))`)
	var ic uint64
	_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT count() FROM telemetry_db.telemetry WHERE %s", stuckW), stuckA...,
	).Scan(&ic)
	data.InstallingCount = int(ic)

	// ── 3. Top apps ──
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT nsapp, count() c FROM telemetry_db.telemetry WHERE %s AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 20", rw), ra...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var a AppCount
				var c uint64
				if rows.Scan(&a.App, &c) == nil {
					a.Count = int(c)
					data.TopApps = append(data.TopApps, a)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT nsapp, sum(total) c FROM telemetry_db.mv_daily_stats WHERE %s AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 20", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var a AppCount
			var c uint64
			if rows.Scan(&a.App, &c) == nil {
				a.Count = int(c)
				data.TopApps = append(data.TopApps, a)
			}
		}
	}

	// ── 4. OS distribution ──
	if rawAgg {
		osW, osA := chWhere(days, repoSource, repoSlug, platform, "os_type != ''", "status IN ('success','failed','aborted','unknown')")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT os_type, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY os_type ORDER BY c DESC LIMIT 15", osW), osA...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var o OsCount
				var c uint64
				if rows.Scan(&o.Os, &c) == nil {
					o.Count = int(c)
					data.OsDistribution = append(data.OsDistribution, o)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT os_type, sum(cnt) c FROM telemetry_db.mv_daily_os WHERE %s AND os_type!='' GROUP BY os_type ORDER BY c DESC LIMIT 15", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var o OsCount
			var c uint64
			if rows.Scan(&o.Os, &c) == nil {
				o.Count = int(c)
				data.OsDistribution = append(data.OsDistribution, o)
			}
		}
	}

	// ── 5. Method stats ──
	if rawAgg {
		methW, methA := chWhere(days, repoSource, repoSlug, platform, "method != ''", "status IN ('success','failed','aborted','unknown')")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT method, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY method ORDER BY c DESC LIMIT 10", methW), methA...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var m MethodCount
				var c uint64
				if rows.Scan(&m.Method, &c) == nil {
					m.Count = int(c)
					data.MethodStats = append(data.MethodStats, m)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT method, sum(cnt) c FROM telemetry_db.mv_daily_method WHERE %s AND method!='' GROUP BY method ORDER BY c DESC LIMIT 10", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var m MethodCount
			var c uint64
			if rows.Scan(&m.Method, &c) == nil {
				m.Count = int(c)
				data.MethodStats = append(data.MethodStats, m)
			}
		}
	}

	// ── 6. PVE versions ──
	if rawAgg {
		pveW, pveA := chWhere(days, repoSource, repoSlug, platform, "pve_version != ''", "status IN ('success','failed','aborted','unknown')")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT pve_version, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY pve_version ORDER BY c DESC LIMIT 15", pveW), pveA...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var p PveCount
				var c uint64
				if rows.Scan(&p.Version, &c) == nil {
					p.Count = int(c)
					data.PveVersions = append(data.PveVersions, p)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT pve_version, sum(cnt) c FROM telemetry_db.mv_daily_pve WHERE %s AND pve_version!='' GROUP BY pve_version ORDER BY c DESC LIMIT 15", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var p PveCount
			var c uint64
			if rows.Scan(&p.Version, &c) == nil {
				p.Count = int(c)
				data.PveVersions = append(data.PveVersions, p)
			}
		}
	}

	// ── 7. Type stats ──
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT type, count() c FROM telemetry_db.telemetry WHERE %s AND type!='' GROUP BY type ORDER BY c DESC LIMIT 10", rw), ra...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var t TypeCount
				var c uint64
				if rows.Scan(&t.Type, &c) == nil {
					t.Count = int(c)
					data.TypeStats = append(data.TypeStats, t)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT type, sum(total) c FROM telemetry_db.mv_daily_stats WHERE %s AND type!='' GROUP BY type ORDER BY c DESC LIMIT 10", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var t TypeCount
			var c uint64
			if rows.Scan(&t.Type, &c) == nil {
				t.Count = int(c)
				data.TypeStats = append(data.TypeStats, t)
			}
		}
	}

	// ── 8. Error analysis (needs raw table — text pattern matching, excludes user_aborted) ──
	fwErr, faErr := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error!=''", "error_category!='user_aborted'")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
			multiIf(
				positionCaseInsensitive(error,'connection refused')>0,'connection refused',
				positionCaseInsensitive(error,'timeout')>0,'timeout',
				positionCaseInsensitive(error,'no space left')>0,'disk full',
				positionCaseInsensitive(error,'permission denied')>0,'permission denied',
				positionCaseInsensitive(error,'not found')>0,'not found',
				positionCaseInsensitive(error,'apt')>0,'apt error',
				positionCaseInsensitive(error,'dpkg')>0,'dpkg error',
				positionCaseInsensitive(error,'curl')>0,'network error',
				positionCaseInsensitive(error,'wget')>0,'network error',
				positionCaseInsensitive(error,'docker')>0,'docker error',
				positionCaseInsensitive(error,'systemctl')>0,'systemd error',
				substring(lower(error),1,40)
			) as pat,
			count() as cnt,
			uniqExact(nsapp) as ua,
			arrayStringConcat(arraySlice(groupUniqArray(nsapp),1,5),', ') as apps
		FROM telemetry_db.telemetry
		WHERE %s GROUP BY pat ORDER BY cnt DESC LIMIT 15`, fwErr), faErr...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var eg ErrorGroup
			var cnt, ua uint64
			if rows.Scan(&eg.Pattern, &cnt, &ua, &eg.Apps) == nil {
				eg.Count = int(cnt)
				eg.UniqueApps = int(ua)
				data.ErrorAnalysis = append(data.ErrorAnalysis, eg)
			}
		}
	}

	// ── 9. Failed apps with failure rates ──
	minInstalls := 10
	switch {
	case days <= 1:
		minInstalls = 5
	case days <= 7:
		minInstalls = 15
	case days <= 30:
		minInstalls = 40
	default:
		minInstalls = 100
	}
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT nsapp, anyLast(type), count() t, countIf(status='failed') f
			FROM telemetry_db.telemetry
			WHERE %s AND nsapp!=''
			GROUP BY nsapp
			HAVING f > 0 AND t >= %d
			ORDER BY toFloat64(f)/t DESC LIMIT 50`, rw, minInstalls), ra...); err == nil {
			defer rows.Close()
			appTotal := make(map[string]int)
			appFailed := make(map[string]int)
			for rows.Next() {
				var nsapp, typ string
				var t, f uint64
				if rows.Scan(&nsapp, &typ, &t, &f) == nil {
					key := nsapp + "|" + typ
					appTotal[key] = int(t)
					appFailed[key] = int(f)
				}
			}
			data.FailedApps = buildFailedApps(appTotal, appFailed, 16, minInstalls)
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT nsapp, anyLast(type), sum(total) t, sum(failed) f
		FROM telemetry_db.mv_daily_stats
		WHERE %s AND nsapp!=''
		GROUP BY nsapp
		HAVING f > 0 AND t >= %d
		ORDER BY toFloat64(f)/t DESC LIMIT 50`, mw, minInstalls), ma...); err == nil {
		defer rows.Close()
		appTotal := make(map[string]int)
		appFailed := make(map[string]int)
		for rows.Next() {
			var nsapp, typ string
			var t, f uint64
			if rows.Scan(&nsapp, &typ, &t, &f) == nil {
				key := nsapp + "|" + typ
				appTotal[key] = int(t)
				appFailed[key] = int(f)
			}
		}
		data.FailedApps = buildFailedApps(appTotal, appFailed, 16, minInstalls)
	}

	// ── 10. Daily stats ──
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT toString(toDate(created)) d, countIf(status='success') s, countIf(status='failed') f
			FROM telemetry_db.telemetry WHERE %s
			GROUP BY toDate(created) ORDER BY toDate(created)`, rw), ra...); err == nil {
			defer rows.Close()
			sMap := make(map[string]int)
			fMap := make(map[string]int)
			for rows.Next() {
				var d string
				var s, f uint64
				if rows.Scan(&d, &s, &f) == nil {
					sMap[d] = int(s)
					fMap[d] = int(f)
				}
			}
			actualDays := days
			if actualDays <= 0 {
				actualDays = 365
			}
			data.DailyStats = buildDailyStats(sMap, fMap, actualDays)
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT toString(day) d, sum(success) s, sum(failed) f
		FROM telemetry_db.mv_daily_stats WHERE %s
		GROUP BY day ORDER BY day`, mw), ma...); err == nil {
		defer rows.Close()
		sMap := make(map[string]int)
		fMap := make(map[string]int)
		for rows.Next() {
			var d string
			var s, f uint64
			if rows.Scan(&d, &s, &f) == nil {
				sMap[d] = int(s)
				fMap[d] = int(f)
			}
		}
		actualDays := days
		if actualDays <= 0 {
			actualDays = 365
		}
		data.DailyStats = buildDailyStats(sMap, fMap, actualDays)
	}

	// ── 11. GPU stats (raw table — not materialized, low volume) ──
	gpuW, gpuA := chWhere(days, repoSource, repoSlug, platform, "status IN ('success','failed','aborted','unknown')", "gpu_vendor!=''", "gpu_vendor!='unknown'")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT gpu_vendor, gpu_passthrough, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY gpu_vendor, gpu_passthrough ORDER BY c DESC", gpuW), gpuA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var g GPUCount
			var c uint64
			if rows.Scan(&g.Vendor, &g.Passthrough, &c) == nil {
				g.Count = int(c)
				data.GPUStats = append(data.GPUStats, g)
			}
		}
	}

	// ── 12. Error categories (raw table — excludes user_aborted) ──
	catW, catA := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error_category NOT IN ('','user_aborted')")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT error_category, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY error_category ORDER BY c DESC", catW), catA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var e ErrorCatCount
			var c uint64
			if rows.Scan(&e.Category, &c) == nil {
				e.Count = int(c)
				data.ErrorCategories = append(data.ErrorCategories, e)
			}
		}
	}

	// ── 13. Top tools ──
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT nsapp, count() c FROM telemetry_db.telemetry WHERE %s AND type='pve' AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 15", rw), ra...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var t ToolCount
				var c uint64
				if rows.Scan(&t.Tool, &c) == nil {
					t.Count = int(c)
					data.TopTools = append(data.TopTools, t)
					data.TotalTools += int(c)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT nsapp, sum(total) c FROM telemetry_db.mv_daily_stats WHERE %s AND type='pve' AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 15", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var t ToolCount
			var c uint64
			if rows.Scan(&t.Tool, &c) == nil {
				t.Count = int(c)
				data.TopTools = append(data.TopTools, t)
				data.TotalTools += int(c)
			}
		}
	}

	// ── 14. Top addons ──
	if rawAgg {
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT nsapp, count() c FROM telemetry_db.telemetry WHERE %s AND type='addon' AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 15", rw), ra...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var a AddonCount
				var c uint64
				if rows.Scan(&a.Addon, &c) == nil {
					a.Count = int(c)
					data.TopAddons = append(data.TopAddons, a)
					data.TotalAddons += int(c)
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT nsapp, sum(total) c FROM telemetry_db.mv_daily_stats WHERE %s AND type='addon' AND nsapp!='' GROUP BY nsapp ORDER BY c DESC LIMIT 15", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var a AddonCount
			var c uint64
			if rows.Scan(&a.Addon, &c) == nil {
				a.Count = int(c)
				data.TopAddons = append(data.TopAddons, a)
				data.TotalAddons += int(c)
			}
		}
	}

	// ── 15. Recent records (one row per execution_id, with pipeline) ──
	if recs, _, err := ch.FetchRecordsPaginated(ctx, 1, 20, "", "", "", "", "-created", repoSource, repoSlug, platform, days); err == nil {
		data.RecentRecords = recs
	}

	// ── 17. Platform distribution (pve vs incus, derived for legacy rows) ──
	platW, platA := chWhere(days, repoSource, repoSlug, platform, "status IN ('success','failed','aborted','unknown')")
	// NOTE: platformExpr contains '%' (SQL LIKE) — must be passed as a %s
	// argument, never concatenated into the fmt.Sprintf format string.
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s AS plat, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY plat ORDER BY c DESC", platformExpr, platW), platA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var p PlatformCount
			var c uint64
			if rows.Scan(&p.Platform, &c) == nil {
				if p.Platform == "" {
					p.Platform = "unknown"
				}
				p.Count = int(c)
				data.PlatformStats = append(data.PlatformStats, p)
			}
		}
	}

	// ── 16. Repository slug breakdown (owner/repo forks) ──
	if repoSlug == "" {
		slugW, slugA := chWhere(days, repoSource, "", "", "repo_slug != ''")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT repo_slug, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY repo_slug ORDER BY c DESC LIMIT 20", slugW), slugA...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var rs RepoSlugCount
				var c uint64
				if rows.Scan(&rs.Slug, &c) == nil {
					rs.Count = int(c)
					data.RepoSlugs = append(data.RepoSlugs, rs)
				}
			}
		}
	}

	return data, nil
}

// ══════════════════════════════════════════════════════════════
//  SCRIPT STATS (serves /api/scripts + frontend /api/stats)
// ══════════════════════════════════════════════════════════════

func (ch *CHClient) FetchScriptStats(ctx context.Context, days int, repoSource, platform string, knownScripts map[string]ScriptInfo) (*ScriptAnalysisData, error) {
	var rows *sql.Rows
	var err error
	if platform != "" {
		// Platform filter needs the raw table (MVs have no platform column)
		rw, ra := chWhere(days, repoSource, "", platform)
		rows, err = ch.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT nsapp, anyLast(type) as typ,
				count() as total,
				countIf(status='success') as sc,
				countIf(status='failed') as fc,
				countIf(status='aborted') as ac
			FROM telemetry_db.telemetry WHERE %s AND nsapp != ''
			GROUP BY nsapp ORDER BY total DESC`, rw), ra...)
	} else {
		mw, ma := chMVWhere(days, repoSource)
		rows, err = ch.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT nsapp, anyLast(type) as typ,
				sum(total) as total,
				sum(success) as sc,
				sum(failed) as fc,
				sum(aborted) as ac
			FROM telemetry_db.mv_daily_stats WHERE %s
			GROUP BY nsapp ORDER BY total DESC`, mw), ma...)
	}
	if err != nil {
		return nil, fmt.Errorf("CH script stats: %w", err)
	}
	defer rows.Close()

	data := &ScriptAnalysisData{}
	now := time.Now()
	seen := make(map[string]bool)

	for rows.Next() {
		var nsapp, typ string
		var total, sc, fc, ac uint64
		if err := rows.Scan(&nsapp, &typ, &total, &sc, &fc, &ac); err != nil {
			continue
		}
		// Filter to known scripts if provided
		if len(knownScripts) > 0 {
			if _, ok := knownScripts[nsapp]; !ok {
				continue
			}
		}
		seen[nsapp] = true

		// Override type from script_scripts metadata
		if knownScripts != nil {
			if info, ok := knownScripts[nsapp]; ok {
				typ = info.Type
			}
		}

		completed := sc + fc + ac
		rate := float64(0)
		if completed > 0 {
			rate = float64(sc) / float64(completed) * 100
		}
		// Installs per day over the selected window.
		//
		// This used to divide a windowed total by the script's age since
		// creation, which is two different periods in one fraction -- and it
		// never ran anyway, because the whole block was behind knownScripts !=
		// nil and every caller passes nil. The column read 0.00 for every row on
		// every page, always.
		//
		// The window is the honest denominator: it is the period the numerator
		// was counted over, and it needs no metadata the server does not have.
		daysOld := 0
		installsPerDay := float64(0)
		if days > 0 {
			installsPerDay = float64(total) / float64(days)
		}
		// Age still needs script creation dates, which live in PocketBase and
		// never reach this service. Left at 0 rather than guessed.
		if knownScripts != nil {
			if info, ok := knownScripts[nsapp]; ok && !info.Created.IsZero() {
				daysOld = int(now.Sub(info.Created).Hours() / 24)
				if daysOld < 1 {
					daysOld = 1
				}
			}
		}

		data.TopScripts = append(data.TopScripts, ScriptStat{
			App: nsapp, Type: typ,
			Total: int(total), Success: int(sc), Failed: int(fc), Aborted: int(ac),
			SuccessRate: rate, DaysOld: daysOld, InstallsPerDay: installsPerDay,
		})
		data.TotalInstalls += int(total)
	}

	// Add zero-usage known scripts (30d + alltime)
	if knownScripts != nil && (days == 0 || days >= 30) {
		for slug, info := range knownScripts {
			if !seen[slug] {
				daysOld := 0
				if !info.Created.IsZero() {
					daysOld = int(now.Sub(info.Created).Hours() / 24)
					if daysOld < 1 {
						daysOld = 1
					}
				}
				data.TopScripts = append(data.TopScripts, ScriptStat{
					App: slug, Type: info.Type, DaysOld: daysOld,
				})
			}
		}
	}
	data.TotalScripts = len(data.TopScripts)

	// Recent activity: last 50 completed installations (was never populated before —
	// the "Recent Activity" table on the scripts page rendered empty).
	recentDays := days
	if recentDays == 0 || recentDays > 30 {
		recentDays = 30
	}
	recW, recA := chWhere(recentDays, repoSource, "", platform,
		"status IN ('success','failed','aborted','unknown')", "nsapp != ''")
	if rrows, rerr := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT nsapp, type, status, exit_code, os_type, os_version, pve_version,
			method, %s AS plat, toString(created)
		FROM telemetry_db.telemetry WHERE %s
		ORDER BY created DESC LIMIT 50`, platformExpr, recW), recA...); rerr == nil {
		defer rrows.Close()
		for rrows.Next() {
			var rs RecentScript
			var ec int16
			if rrows.Scan(&rs.App, &rs.Type, &rs.Status, &ec, &rs.OsType, &rs.OsVersion,
				&rs.PveVer, &rs.Method, &rs.Platform, &rs.Created) == nil {
				rs.ExitCode = int(ec)
				data.RecentScripts = append(data.RecentScripts, rs)
			}
		}
	}

	log.Printf("[CH] Script stats: %d scripts, %d total installs (days=%d, platform=%s)", data.TotalScripts, data.TotalInstalls, days, platform)
	return data, nil
}

// ══════════════════════════════════════════════════════════════
//  PER-SCRIPT DETAIL (daily drill-down for one app)
// ══════════════════════════════════════════════════════════════

// FetchScriptDetail returns the full drill-down for a single script:
// dense daily series, exit-code breakdown, OS/platform distribution,
// and the most recent installations (with pipelines).
func (ch *CHClient) FetchScriptDetail(ctx context.Context, app string, days int, repoSource, platform string) (*ScriptDetailData, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	data := &ScriptDetailData{App: app}

	// Base filter: this app, terminal statuses only (installing/pings excluded)
	baseW, baseA := chWhere(days, repoSource, "", platform,
		"nsapp = ?", "status IN ('success','failed','aborted','unknown')")
	baseA = append(baseA, app)

	// ── Daily series + totals ──
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT toString(toDate(created)) d,
			count() t,
			countIf(status='success') s,
			countIf(status='failed' AND error_category!='user_aborted') f,
			countIf(status='aborted' OR (status='failed' AND error_category='user_aborted')) a
		FROM telemetry_db.telemetry WHERE %s
		GROUP BY toDate(created) ORDER BY toDate(created)`, baseW), baseA...); err == nil {
		defer rows.Close()
		byDate := make(map[string]ScriptDailyStat)
		for rows.Next() {
			var d string
			var t, s, f, a uint64
			if rows.Scan(&d, &t, &s, &f, &a) == nil {
				byDate[d] = ScriptDailyStat{Date: d, Total: int(t), Success: int(s), Failed: int(f), Aborted: int(a)}
				data.Total += int(t)
				data.Success += int(s)
				data.Failed += int(f)
				data.Aborted += int(a)
			}
		}
		// Dense series (no gaps) so the chart is continuous
		for i := days - 1; i >= 0; i-- {
			date := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
			if st, ok := byDate[date]; ok {
				data.Daily = append(data.Daily, st)
			} else {
				data.Daily = append(data.Daily, ScriptDailyStat{Date: date})
			}
		}
	} else {
		return nil, fmt.Errorf("CH script detail daily: %w", err)
	}
	if data.Success+data.Failed > 0 {
		data.SuccessRate = float64(data.Success) / float64(data.Success+data.Failed) * 100
	}

	// ── Type + avg duration ──
	var typ string
	var avgDur float64
	_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT anyLast(type),
			if(countIf(install_duration>0 AND status='success')>0,
				toFloat64(sumIf(install_duration, install_duration>0 AND status='success'))
					/ countIf(install_duration>0 AND status='success'), 0)
		FROM telemetry_db.telemetry WHERE %s`, baseW), baseA...,
	).Scan(&typ, &avgDur)
	data.Type = typ
	data.AvgDuration = avgDur

	// ── Exit code breakdown (real failures) ──
	ecW, ecA := chWhere(days, repoSource, "", platform,
		"nsapp = ?", "status='failed'", "error_category!='user_aborted'", "exit_code!=0")
	ecA = append(ecA, app)
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT exit_code, count() c FROM telemetry_db.telemetry
		WHERE %s GROUP BY exit_code ORDER BY c DESC LIMIT 10`, ecW), ecA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var code int16
			var cnt uint64
			if rows.Scan(&code, &cnt) == nil {
				pct := float64(0)
				if data.Failed > 0 {
					pct = float64(cnt) / float64(data.Failed) * 100
				}
				data.ExitCodes = append(data.ExitCodes, ExitCodeStat{
					ExitCode: int(code), Count: int(cnt), Percentage: pct,
					Description: getExitCodeDescription(int(code)),
					Category:    getExitCodeCategory(int(code)),
				})
			}
		}
	}

	// ── OS distribution ──
	osW, osA := chWhere(days, repoSource, "", platform,
		"nsapp = ?", "os_type != ''", "status IN ('success','failed','aborted','unknown')")
	osA = append(osA, app)
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT os_type, count() c FROM telemetry_db.telemetry
		WHERE %s GROUP BY os_type ORDER BY c DESC LIMIT 10`, osW), osA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var o OsCount
			var c uint64
			if rows.Scan(&o.Os, &c) == nil {
				o.Count = int(c)
				data.OsDistribution = append(data.OsDistribution, o)
			}
		}
	}

	// ── Platform distribution ──
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS plat, count() c FROM telemetry_db.telemetry
		WHERE %s GROUP BY plat ORDER BY c DESC`, platformExpr, baseW), baseA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var p PlatformCount
			var c uint64
			if rows.Scan(&p.Platform, &c) == nil {
				if p.Platform == "" {
					p.Platform = "unknown"
				}
				p.Count = int(c)
				data.PlatformStats = append(data.PlatformStats, p)
			}
		}
	}

	// ── Recent installations (exact app match, newest first, with pipelines) ──
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s FROM telemetry_db.telemetry
		WHERE %s ORDER BY created DESC LIMIT 20`, recordSelectCols, baseW), baseA...); err == nil {
		defer rows.Close()
		data.Recent = scanRecords(rows)
		ch.attachPipelines(ctx, data.Recent)
	}

	return data, nil
}

// ══════════════════════════════════════════════════════════════
//  ERROR ANALYSIS
// ══════════════════════════════════════════════════════════════

func (ch *CHClient) FetchErrorAnalysisData(ctx context.Context, days int, repoSource, repoSlug, platform string) (*ErrorAnalysisData, error) {
	data := &ErrorAnalysisData{}

	mw, ma := chMVWhere(days, repoSource)
	// Materialized views have no platform column — platform filters need raw-table aggregation.
	rawAgg := repoSlug != "" || platform != ""
	rw, ra := chWhere(days, repoSource, repoSlug, platform)

	// Total installs
	var ti uint64
	if rawAgg {
		_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT count() FROM telemetry_db.telemetry WHERE %s", rw), ra...,
		).Scan(&ti)
	} else {
		_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT sum(total) FROM telemetry_db.mv_daily_stats WHERE %s", mw), ma...,
		).Scan(&ti)
	}
	data.TotalInstalls = int(ti)

	// Total real errors
	var te uint64
	if rawAgg {
		errW, errA := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error_category!='user_aborted'", "exit_code!=0")
		_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT count() FROM telemetry_db.telemetry WHERE %s", errW), errA...,
		).Scan(&te)
	} else {
		_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT sum(cnt) FROM telemetry_db.mv_daily_errors WHERE %s", mw), ma...,
		).Scan(&te)
	}
	data.TotalErrors = int(te)

	if data.TotalInstalls > 0 {
		data.OverallFailRate = float64(data.TotalErrors) / float64(data.TotalInstalls) * 100
	}

	// Stuck installing
	stuckW, stuckA := chWhere(1, repoSource, repoSlug, platform,
		"status IN ('installing','validation','configuring')",
		`(execution_id = '' OR execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('success','failed','aborted','unknown') AND execution_id != ''))`)
	var si uint64
	_ = ch.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT count() FROM telemetry_db.telemetry WHERE %s", stuckW), stuckA...,
	).Scan(&si)
	data.StuckInstalling = int(si)

	// Exit code stats
	if rawAgg {
		errW, errA := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error_category!='user_aborted'", "exit_code!=0")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
			"SELECT exit_code, count() c FROM telemetry_db.telemetry WHERE %s GROUP BY exit_code ORDER BY c DESC LIMIT 30", errW), errA...); err == nil {
			defer rows.Close()
			for rows.Next() {
				var code int16
				var cnt uint64
				if rows.Scan(&code, &cnt) == nil {
					pct := float64(0)
					if data.TotalErrors > 0 {
						pct = float64(cnt) / float64(data.TotalErrors) * 100
					}
					data.ExitCodeStats = append(data.ExitCodeStats, ExitCodeStat{
						ExitCode: int(code), Count: int(cnt), Percentage: pct,
						Description: getExitCodeDescription(int(code)),
						Category:    getExitCodeCategory(int(code)),
					})
				}
			}
		}
	} else if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT exit_code, sum(cnt) c FROM telemetry_db.mv_daily_errors WHERE %s GROUP BY exit_code ORDER BY c DESC LIMIT 30", mw), ma...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var code int16
			var cnt uint64
			if rows.Scan(&code, &cnt) == nil {
				pct := float64(0)
				if data.TotalErrors > 0 {
					pct = float64(cnt) / float64(data.TotalErrors) * 100
				}
				data.ExitCodeStats = append(data.ExitCodeStats, ExitCodeStat{
					ExitCode: int(code), Count: int(cnt), Percentage: pct,
					Description: getExitCodeDescription(int(code)),
					Category:    getExitCodeCategory(int(code)),
				})
			}
		}
	}

	// Category stats (excludes user_aborted)
	cw, ca := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error_category NOT IN ('','user_aborted')")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT error_category, count() c,
			arrayStringConcat(arraySlice(groupUniqArray(nsapp),1,5),', ') apps
		FROM telemetry_db.telemetry WHERE %s
		GROUP BY error_category ORDER BY c DESC`, cw), ca...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var cs CategoryStat
			var cnt uint64
			if rows.Scan(&cs.Category, &cnt, &cs.TopApps) == nil {
				cs.Count = int(cnt)
				if data.TotalErrors > 0 {
					cs.Percentage = float64(cnt) / float64(data.TotalErrors) * 100
				}
				data.CategoryStats = append(data.CategoryStats, cs)
			}
		}
	}

	// App errors (excludes user_aborted)
	aw, aa := chWhere(days, repoSource, repoSlug, platform, "status IN ('success','failed','aborted','unknown')")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT nsapp, anyLast(type), count() t,
			countIf(status='failed' AND error_category!='user_aborted') f,
			countIf(status='aborted' AND error_category!='user_aborted') ab,
			topKIf(1)(exit_code, status='failed' AND error_category!='user_aborted' AND exit_code!=0) topec,
			anyIf(error, status='failed' AND error_category!='user_aborted' AND error!='') toperr,
			anyIf(error_category, status='failed' AND error_category NOT IN ('','uncategorized','user_aborted')) topcat
		FROM telemetry_db.telemetry WHERE %s AND nsapp!=''
		GROUP BY nsapp
		HAVING f+ab > 0
		ORDER BY f DESC LIMIT 50`, aw), aa...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var ae AppErrorDetail
			var t, f, ab uint64
			var topEC []int16
			if rows.Scan(&ae.App, &ae.Type, &t, &f, &ab, &topEC, &ae.TopError, &ae.TopCategory) == nil {
				ae.TotalCount = int(t)
				ae.FailedCount = int(f)
				ae.AbortedCount = int(ab)
				if t > 0 {
					ae.FailureRate = float64(f) / float64(t) * 100
				}
				if len(topEC) > 0 {
					ae.TopExitCode = int(topEC[0])
				}
				data.AppErrors = append(data.AppErrors, ae)
			}
		}
	}

	// Error timeline (failed + aborted per day; the Aborted series was never
	// populated before, leaving the chart's second dataset permanently empty)
	{
		tlW, tlA := chWhere(days, repoSource, repoSlug, platform,
			"status IN ('failed','aborted')")
		if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT toString(toDate(created)) d,
				countIf(status='failed' AND error_category!='user_aborted' AND exit_code!=0) f,
				countIf(status='aborted' OR (status='failed' AND error_category='user_aborted')) a
			FROM telemetry_db.telemetry WHERE %s
			GROUP BY toDate(created) ORDER BY toDate(created)`, tlW), tlA...); err == nil {
			defer rows.Close()
			dailyF := make(map[string]int)
			dailyA := make(map[string]int)
			for rows.Next() {
				var d string
				var f, a uint64
				if rows.Scan(&d, &f, &a) == nil {
					dailyF[d] = int(f)
					dailyA[d] = int(a)
				}
			}
			actualDays := days
			if actualDays <= 0 {
				actualDays = 30
			}
			for i := actualDays - 1; i >= 0; i-- {
				date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
				data.ErrorTimeline = append(data.ErrorTimeline, ErrorTimelinePoint{
					Date:    date,
					Failed:  dailyF[date],
					Aborted: dailyA[date],
				})
			}
		}
	}

	// Failure signatures: group by the exact failing command from the v3
	// structured error header ("… | at line L: <cmd>"). Answers "WHICH command
	// breaks most often, across which apps" — far more actionable than exit codes.
	//
	// Two of those values are not commands and are excluded below. The engine
	// writes "unknown" when the ERR trap had no BASH_COMMAND to record, and a
	// bare "return N" is what the trap sees when a helper propagates a failure
	// that happened earlier — by then the frame has been popped and the name of
	// the function that returned is gone. Both group enormously: over fourteen
	// days "unknown" was the single largest signature at 216 across 66 apps,
	// with return 7, return 22 and return 250 adding 330 more between them. That
	// is more than half the visible list saying nothing, pushing real signatures
	// off the end of it. These failures still count in the exit-code and
	// category breakdowns; they just are not signatures.
	sigW, sigA := chWhere(days, repoSource, repoSlug, platform,
		"status='failed'", "error_category!='user_aborted'", "error != ''")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
			extract(error, 'at line [0-9]+: ([^\n|]{1,200})') AS cmd,
			count() c,
			uniqExact(nsapp) ua,
			topK(1)(exit_code) tec,
			arrayStringConcat(arraySlice(groupUniqArray(nsapp),1,5),', ') apps
		FROM telemetry_db.telemetry
		WHERE %s AND cmd != '' AND cmd != 'unknown' AND NOT match(cmd, '^return [0-9]+$')
		GROUP BY cmd ORDER BY c DESC LIMIT 20`, sigW), sigA...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var sig ErrorSignature
			var c, ua uint64
			var tec []int16
			if rows.Scan(&sig.Command, &c, &ua, &tec, &sig.Apps) == nil {
				sig.Count = int(c)
				sig.UniqueApps = int(ua)
				if len(tec) > 0 {
					sig.TopExitCode = int(tec[0])
				}
				data.Signatures = append(data.Signatures, sig)
			}
		}
	}

	// Recent errors (excludes user_aborted)
	rwErr, raErr := chWhere(days, repoSource, repoSlug, platform, "status='failed'", "error_category!='user_aborted'")
	if rows, err := ch.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT nsapp, type, status, exit_code, error, error_category,
			os_type, os_version, pve_version, %s AS plat,
			execution_id, install_duration, toString(created)
		FROM telemetry_db.telemetry WHERE %s
		ORDER BY created DESC LIMIT 100`, platformExpr, rwErr), raErr...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var er ErrorRecord
			var ec int16
			var dur uint32
			if rows.Scan(&er.NSAPP, &er.Type, &er.Status, &ec, &er.Error, &er.ErrorCategory,
				&er.OsType, &er.OsVersion, &er.PveVer, &er.Platform,
				&er.ExecutionID, &dur, &er.Created) == nil {
				er.ExitCode = int(ec)
				er.InstallDuration = int(dur)
				data.RecentErrors = append(data.RecentErrors, er)
			}
		}
	}

	return data, nil
}

// ══════════════════════════════════════════════════════════════
//  PAGINATED RECORDS (/api/records)
// ══════════════════════════════════════════════════════════════

// statusRankSQL orders rows when collapsing multiple telemetry events per
// execution_id: terminal states win over in-progress heartbeats; richer rows
// (error text / hardware fields) win within the same rank.
const statusRankSQL = `multiIf(
	status IN ('success','failed','aborted','unknown'), 0,
	status = 'configuring', 1,
	status = 'validation', 2,
	status = 'installing', 3, 4)`

const statusRankOrderSQL = statusRankSQL + `, if(status IN ('success','failed','aborted','unknown'), length(error) + toUInt64(disk_size), 0) DESC, created DESC`

type pipelineStep struct {
	S string `json:"s"`
	T string `json:"t"`
}

// recordsBaseWhere builds shared filters for the records API. Status is applied
// after collapsing to the latest row per execution_id (see latestStatusSQL).
func recordsBaseWhere(days int, repoSource, repoSlug, platform, app, osType, typeFilter string) (string, []interface{}) {
	parts := []string{"1=1"}
	var args []interface{}
	if days > 0 {
		parts = append(parts, "created >= ?")
		args = append(args, chSinceTime(days))
	}
	if app != "" {
		parts = append(parts, "positionCaseInsensitive(nsapp, ?) > 0")
		args = append(args, app)
	}
	if osType != "" {
		parts = append(parts, "os_type = ?")
		args = append(args, osType)
	}
	if typeFilter != "" {
		parts = append(parts, "type = ?")
		args = append(args, typeFilter)
	}
	if pred, pArgs := repoSourcePred(repoSource); pred != "" {
		parts = append(parts, pred)
		args = append(args, pArgs...)
	}
	if repoSlug != "" {
		parts = append(parts, "repo_slug = ?")
		args = append(args, repoSlug)
	}
	if pred := platformPred(platform); pred != "" {
		parts = append(parts, pred)
	}
	return strings.Join(parts, " AND "), args
}

// latestStatusSQL filters on the collapsed (current) status of each installation.
func latestStatusSQL(status string) (string, []interface{}) {
	if status == "" {
		return "", nil
	}
	switch status {
	case "aborted":
		return `(status='aborted' OR (status='failed' AND (exit_code IN (129,130)
			OR positionCaseInsensitive(error,'SIGINT')>0 OR positionCaseInsensitive(error,'SIGHUP')>0
			OR positionCaseInsensitive(error,'Ctrl+C')>0 OR positionCaseInsensitive(error,'Ctrl-C')>0)))`, nil
	case "failed":
		return `(status='failed' AND exit_code NOT IN (129,130)
			AND positionCaseInsensitive(error,'SIGINT')=0 AND positionCaseInsensitive(error,'SIGHUP')=0
			AND positionCaseInsensitive(error,'Ctrl+C')=0 AND positionCaseInsensitive(error,'Ctrl-C')=0)`, nil
	default:
		return "status = ?", []interface{}{status}
	}
}

func (ch *CHClient) attachPipelines(ctx context.Context, records []TelemetryRecord) {
	if len(records) == 0 {
		return
	}
	eids := make([]string, 0, len(records))
	for _, r := range records {
		if r.ExecutionID != "" {
			eids = append(eids, r.ExecutionID)
		}
	}
	if len(eids) == 0 {
		return
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(eids)), ",")
	args := make([]interface{}, len(eids))
	for i, e := range eids {
		args[i] = e
	}

	q := fmt.Sprintf(`
		SELECT execution_id, status, toString(created) AS t
		FROM telemetry_db.telemetry
		WHERE execution_id IN (%s)
		  AND status IN ('installing','validation','configuring','success','failed','aborted','unknown')
		ORDER BY execution_id, created ASC`, placeholders)

	rows, err := ch.db.QueryContext(ctx, q, args...)
	if err != nil {
		log.Printf("[CH] pipeline fetch: %v", err)
		return
	}
	defer rows.Close()

	type step struct {
		s, t string
	}
	stepsByEID := make(map[string][]step)
	for rows.Next() {
		var eid, st, ts string
		if rows.Scan(&eid, &st, &ts) != nil {
			continue
		}
		prev := stepsByEID[eid]
		// Collapse consecutive duplicate statuses (retry noise).
		if len(prev) > 0 && prev[len(prev)-1].s == st {
			continue
		}
		stepsByEID[eid] = append(stepsByEID[eid], step{s: st, t: ts})
	}

	for i := range records {
		eid := records[i].ExecutionID
		raw, ok := stepsByEID[eid]
		if !ok || len(raw) == 0 {
			continue
		}
		out := make([]pipelineStep, len(raw))
		for j, s := range raw {
			out[j] = pipelineStep{S: s.s, T: s.t}
		}
		if b, err := json.Marshal(out); err == nil {
			records[i].Pipeline = string(b)
		}
	}
}

func (ch *CHClient) FetchRecordsPaginated(ctx context.Context, page, limit int,
	status, app, osType, typeFilter, sortField, repoSource, repoSlug, platform string, days int,
) ([]TelemetryRecord, int, error) {
	baseWhere, baseArgs := recordsBaseWhere(days, repoSource, repoSlug, platform, app, osType, typeFilter)
	statusPred, statusArgs := latestStatusSQL(status)

	latestWhere := "rn = 1"
	var outerArgs []interface{}
	if statusPred != "" {
		latestWhere += " AND " + statusPred
		outerArgs = append(outerArgs, statusArgs...)
	}

	// Count distinct installations (not raw event rows).
	countQ := fmt.Sprintf(`
		WITH ranked AS (
			SELECT status, exit_code, error,
				row_number() OVER (
					PARTITION BY if(execution_id = '', random_id, execution_id)
					ORDER BY %s
				) AS rn
			FROM telemetry_db.telemetry
			WHERE %s
		)
		SELECT count() FROM ranked WHERE %s`, statusRankOrderSQL, baseWhere, latestWhere)

	countArgs := append(append([]interface{}{}, baseArgs...), outerArgs...)
	var totalU uint64
	if err := ch.db.QueryRowContext(ctx, countQ, countArgs...).Scan(&totalU); err != nil {
		return nil, 0, fmt.Errorf("CH records count: %w", err)
	}
	total := int(totalU)

	sort := "created DESC"
	allowed := map[string]string{
		"created": "created", "-created": "created DESC",
		"nsapp": "nsapp", "-nsapp": "nsapp DESC",
		"status": "status", "-status": "status DESC",
		"os_type": "os_type", "-os_type": "os_type DESC",
		"type": "type", "-type": "type DESC",
		"method": "method", "-method": "method DESC",
		"exit_code": "exit_code", "-exit_code": "exit_code DESC",
	}
	if s, ok := allowed[sortField]; ok {
		sort = s
	}

	offset := (page - 1) * limit
	// SELECT * in the CTE keeps the raw columns (incl. the DateTime `created`),
	// so the outer recordSelectCols (which calls toString(created)) and the
	// status/sort predicates all resolve against real columns.
	q := fmt.Sprintf(`
		WITH ranked AS (
			SELECT *,
				row_number() OVER (
					PARTITION BY if(execution_id = '', random_id, execution_id)
					ORDER BY %s
				) AS rn
			FROM telemetry_db.telemetry
			WHERE %s
		)
		SELECT %s FROM ranked WHERE %s
		ORDER BY %s
		LIMIT %d OFFSET %d`,
		statusRankOrderSQL, baseWhere, recordSelectCols, latestWhere, sort, limit, offset)

	fetchArgs := append(append([]interface{}{}, baseArgs...), outerArgs...)
	rows, err := ch.db.QueryContext(ctx, q, fetchArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("CH records: %w", err)
	}
	defer rows.Close()
	records := scanRecords(rows)
	ch.attachPipelines(ctx, records)
	return records, total, nil
}

// ══════════════════════════════════════════════════════════════
//  CLEANUP (stuck installing + retention)
// ══════════════════════════════════════════════════════════════

func (ch *CHClient) FindStuckInstallations(ctx context.Context, stuckHours int) ([]StuckRecord, error) {
	rows, err := ch.db.QueryContext(ctx, `
		SELECT id, nsapp, toString(created)
		FROM telemetry_db.telemetry
		WHERE status IN ('installing','configuring')
		  AND created < now() - INTERVAL ? HOUR
		  AND (execution_id = '' OR execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('success','failed','aborted','unknown') AND execution_id != ''))
		ORDER BY created LIMIT 500`, stuckHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StuckRecord
	for rows.Next() {
		var r StuckRecord
		if rows.Scan(&r.ID, &r.NSAPP, &r.Created) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func (ch *CHClient) MarkRecordAsUnknown(ctx context.Context, record StuckRecord, stuckHours int) error {
	// Insert a terminal-status row for this record so stats count it properly.
	// Copy nsapp, type from the original row and set status=unknown.
	_, err := ch.db.ExecContext(ctx, `
		INSERT INTO telemetry_db.telemetry
			(id, nsapp, type, status, error, error_category, created, execution_id, random_id, repo_source)
		SELECT ?, nsapp, type, 'unknown', ?, 'timeout', now64(3), execution_id, random_id, repo_source
		FROM telemetry_db.telemetry WHERE id = ? LIMIT 1`,
		generateRecordID(),
		fmt.Sprintf("Installation timed out - no completion status received after %dh", stuckHours),
		record.ID,
	)
	return err
}

func (ch *CHClient) DeleteOldRecords(ctx context.Context, retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	_, err := ch.db.ExecContext(ctx,
		"ALTER TABLE telemetry_db.telemetry DELETE WHERE created < ?", cutoff)
	return err
}

// GetOrphanTerminalCount counts terminal events that have no start behind them.
//
// The mirror image of GetStuckCount. Until core stopped it, a ct script running
// inside a container for an update still armed the host's ERR and signal traps,
// so a Ctrl-C or any error posted a failed/aborted row typed 'lxc' with no
// installing row for that execution_id. Those inflate the failure rate twice
// over: they raise the numerator and leave the denominator untouched.
func (ch *CHClient) GetOrphanTerminalCount(ctx context.Context, sinceDays int) (int, error) {
	var cnt uint64
	err := ch.db.QueryRowContext(ctx, `
		SELECT count() FROM telemetry_db.telemetry
		WHERE status IN ('failed','aborted','unknown')
		  AND execution_id != ''
		  AND created > now() - INTERVAL ? DAY
		  AND execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('installing','validation','configuring') AND execution_id != '')`,
		sinceDays).Scan(&cnt)
	return int(cnt), err
}

// DeleteOrphanTerminals removes those rows. Manual only, never on a timer: "no
// start recorded" can also mean the start POST never landed, and that is a real
// install failure worth keeping.
func (ch *CHClient) DeleteOrphanTerminals(ctx context.Context, sinceDays int) error {
	_, err := ch.db.ExecContext(ctx, `
		ALTER TABLE telemetry_db.telemetry DELETE
		WHERE status IN ('failed','aborted','unknown')
		  AND execution_id != ''
		  AND created > now() - INTERVAL ? DAY
		  AND execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('installing','validation','configuring') AND execution_id != '')`,
		sinceDays)
	return err
}

func (ch *CHClient) GetStuckCount(ctx context.Context, stuckHours int) (int, error) {
	var cnt uint64
	err := ch.db.QueryRowContext(ctx, `
		SELECT count() FROM telemetry_db.telemetry
		WHERE status IN ('installing','configuring')
		  AND created < now() - INTERVAL ? HOUR
		  AND (execution_id = '' OR execution_id NOT IN (
			SELECT execution_id FROM telemetry_db.telemetry
			WHERE status IN ('success','failed','aborted','unknown') AND execution_id != ''))`,
		stuckHours).Scan(&cnt)
	return int(cnt), err
}

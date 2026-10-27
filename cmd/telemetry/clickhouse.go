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
	db         *sql.DB
	stuckHours int
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

		// Payload version 3.
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS error_message String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS error_step String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS error_phase LowCardinality(String)`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS failed_function String`,
		`ALTER TABLE telemetry_db.telemetry ADD COLUMN IF NOT EXISTS migrated_from LowCardinality(String)`,
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
		kernel_version, app_version,
		error_message, error_step, error_phase, failed_function
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
		?, ?,
		?, ?, ?, ?
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
		p.ErrorMessage, p.ErrorStep, p.ErrorPhase, p.FailedFunction,
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
// DevScripts→ProxmoxVE on promotion), those untagged historical installs are
// overwhelmingly production traffic. The "ProxmoxVE" filter therefore also
// includes empty repo_source so the count reflects real production volume
// instead of dropping ~70% of historical records into an invisible bucket.
//
// DevScripts also matches the rows it wrote as ProxmoxVED before the rename.
//
// An empty repoSource means "all" (no predicate). Other values match exactly.
func repoSourcePred(repoSource string) (string, []interface{}) {
	return repoSourcePredCol("repo_source", repoSource)
}

func repoSourcePredCol(col, repoSource string) (string, []interface{}) {
	switch repoSource {
	case "":
		return "", nil
	case "ProxmoxVE":
		return col + " IN ('ProxmoxVE','')", nil
	case "DevScripts", legacyDevSource:
		return col + " IN ('DevScripts','" + legacyDevSource + "')", nil
	default:
		return col + " = ?", []interface{}{repoSource}
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

// scanRecords reads TelemetryRecord rows from a *sql.Rows.
func scanRecords(rows *sql.Rows) []TelemetryRecord {
	var out []TelemetryRecord
	for rows.Next() {
		var r TelemetryRecord
		var coreCount, ctType, hasArm uint8
		var diskSize, ramSize, installDur, failedLine uint32
		var exitCode int16
		var payloadVersion uint16
		err := rows.Scan(
			&r.NSAPP, &r.Type, &r.Status, &r.Method,
			&coreCount, &ctType, &diskSize, &ramSize,
			&exitCode, &r.Error, &r.ErrorCategory,
			&r.OsType, &r.OsVersion, &r.PveVer,
			&r.RandomID, &r.ExecutionID, &r.RepoSource, &r.RepoSlug,
			&r.CPUVendor, &r.CPUModel,
			&r.GPUVendor, &r.GPUModel, &r.GPUPassthrough,
			&r.RAMSpeed, &installDur, &hasArm,
			&payloadVersion, &r.Arch, &r.FailedCommand, &failedLine,
			&r.KernelVersion, &r.AppVersion,
			&r.ErrorMessage, &r.ErrorStep, &r.ErrorPhase, &r.FailedFunction, &r.MigratedFrom,
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
		r.PayloadVersion = int(payloadVersion)
		r.FailedLine = int(failedLine)
		if needsErrorMessage(r.Status, r.ErrorMessage) {
			r.ErrorMessage = deriveErrorMessage(r.Error)
		}
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
	payload_version, arch, failed_command, failed_line,
	kernel_version, app_version,
	error_message, error_step, error_phase, failed_function, migrated_from,
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

// ══════════════════════════════════════════════════════════════
//  PIPELINES
// ══════════════════════════════════════════════════════════════

type pipelineStep struct {
	S  string `json:"s"`
	T  string `json:"t"`
	ms int64
}

func (ch *CHClient) attachPipelines(ctx context.Context, records []TelemetryRecord) {
	var eids []string
	for _, r := range records {
		if r.ExecutionID != "" {
			eids = append(eids, r.ExecutionID)
		}
	}
	steps, err := ch.fetchPipelines(ctx, eids, time.Time{})
	if err != nil {
		log.Printf("[CH] pipeline fetch: %v", err)
		return
	}
	for i := range records {
		if s := steps[records[i].ExecutionID]; len(s) > 0 {
			records[i].Pipeline = pipelineJSON(s)
		}
	}
}

// fetchPipelines returns each run's events in time order, consecutive repeats
// collapsed. A non-zero floor bounds the scan.
func (ch *CHClient) fetchPipelines(ctx context.Context, keys []string, floor time.Time) (map[string][]pipelineStep, error) {
	out := make(map[string][]pipelineStep)
	if len(keys) == 0 {
		return out, nil
	}
	where := runKeyExpr + " IN (" + placeholders(len(keys)) + ")"
	args := stringArgList(keys)
	if !floor.IsZero() {
		where = "created >= ? AND " + where
		args = append([]interface{}{floor}, args...)
	}
	rows, err := ch.db.QueryContext(ctx, `
		SELECT `+runKeyExpr+` AS k, status, toInt64(toUnixTimestamp64Milli(created))
		FROM telemetry_db.telemetry
		WHERE `+where+`
		  AND status IN ('installing','validation','configuring','success','failed','aborted','unknown')
		ORDER BY k, created`, args...)
	if err != nil {
		return nil, fmt.Errorf("CH pipelines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, st string
		var ms int64
		if rows.Scan(&k, &st, &ms) != nil {
			continue
		}
		prev := out[k]
		if len(prev) > 0 && prev[len(prev)-1].S == st {
			continue
		}
		out[k] = append(prev, pipelineStep{S: st, T: msLegacy(ms), ms: ms})
	}
	return out, rows.Err()
}

func pipelineJSON(steps []pipelineStep) string {
	b, err := json.Marshal(steps)
	if err != nil {
		return ""
	}
	return string(b)
}

// ══════════════════════════════════════════════════════════════
//  CLEANUP (stuck installing + retention)
// ══════════════════════════════════════════════════════════════

// stuckRunsSQL selects runs of the last 30 days that never sent a result and
// have been silent for stuckHours. The first row is the one with metadata.
const stuckRunsSQL = `
	SELECT argMin(id, created) AS s_id, any(nsapp), toString(min(created))
	FROM telemetry_db.telemetry
	WHERE created >= now() - INTERVAL 30 DAY
	GROUP BY ` + runKeyExpr + `
	HAVING countIf(status IN ('success','failed','aborted','unknown')) = 0
	   AND max(created) < now() - INTERVAL ? HOUR`

func (ch *CHClient) FindStuckInstallations(ctx context.Context, stuckHours int) ([]StuckRecord, error) {
	rows, err := ch.db.QueryContext(ctx, stuckRunsSQL+"\n\tORDER BY min(created) LIMIT 500", stuckHours)
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
	// Everything the original row knew comes along: copying only nsapp and
	// repo_source left these rows without an OS, resources or a repo slug,
	// which read as a second, broken kind of record.
	_, err := ch.db.ExecContext(ctx, `
		INSERT INTO telemetry_db.telemetry
			(id, nsapp, type, status, error, error_category, created,
			 method, core_count, ct_type, disk_size, ram_size,
			 os_type, os_version, pve_version, execution_id, random_id,
			 repo_source, repo_slug, cpu_vendor, cpu_model,
			 gpu_vendor, gpu_model, gpu_passthrough, ram_speed,
			 install_duration, has_arm)
		SELECT ?, nsapp, type, 'unknown', ?, 'timeout', now64(3),
			 method, core_count, ct_type, disk_size, ram_size,
			 os_type, os_version, pve_version, execution_id, random_id,
			 repo_source, repo_slug, cpu_vendor, cpu_model,
			 gpu_vendor, gpu_model, gpu_passthrough, ram_speed,
			 install_duration, has_arm
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
	err := ch.db.QueryRowContext(ctx, "SELECT count() FROM ("+stuckRunsSQL+")", stuckHours).Scan(&cnt)
	return int(cnt), err
}

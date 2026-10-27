package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var migrateSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const devSourcesSQL = "('DevScripts','" + legacyDevSource + "')"

type migrateRequest struct {
	Slugs []string `json:"slugs"`
	From  string   `json:"from"`
	To    string   `json:"to"`
}

type scriptMigrator interface {
	MigrateScript(ctx context.Context, slugs []string) (moved, days int, err error)
}

// validateMigrateRequest returns the de-duplicated slugs of a valid request.
func validateMigrateRequest(req migrateRequest) ([]string, error) {
	if req.From != "DevScripts" && req.From != legacyDevSource {
		return nil, errors.New("from must be DevScripts")
	}
	if req.To != "ProxmoxVE" {
		return nil, errors.New("to must be ProxmoxVE")
	}
	if len(req.Slugs) == 0 || len(req.Slugs) > 20 {
		return nil, errors.New("slugs must hold 1 to 20 entries")
	}
	seen := make(map[string]bool, len(req.Slugs))
	out := make([]string, 0, len(req.Slugs))
	for _, s := range req.Slugs {
		if !migrateSlugRe.MatchString(s) {
			return nil, fmt.Errorf("invalid slug %q", s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func validAdminToken(header, token string) bool {
	if token == "" {
		return false
	}
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// migrateScriptHandler serves POST /api/admin/migrate-script.
func migrateScriptHandler(token string, m scriptMigrator, invalidate func(context.Context)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validAdminToken(r.Header.Get("Authorization"), token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req migrateRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		slugs, err := validateMigrateRequest(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		moved, days, err := m.MigrateScript(ctx, slugs)
		if err != nil {
			log.Printf("[ADMIN] migrate-script %v failed: %v", slugs, err)
			http.Error(w, "migration failed", http.StatusInternalServerError)
			return
		}
		invalidate(ctx)
		log.Printf("[ADMIN] migrate-script %v: moved %d rows over %d days from %s to ProxmoxVE", slugs, moved, days, req.From)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"moved": moved, "slugs": slugs, "days": days})
	}
}

type sqlStmt struct {
	q    string
	args []interface{}
}

func inList(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func stringArgs(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// migrateCountSQL counts the dev rows of the slugs and lists the days they were created on.
func migrateCountSQL(slugs []string) sqlStmt {
	return sqlStmt{fmt.Sprintf(`SELECT count(), arrayMap(d -> toString(d), groupUniqArray(toDate(created)))
		FROM telemetry_db.telemetry
		WHERE nsapp IN (%s) AND repo_source IN %s`, inList(len(slugs)), devSourcesSQL), stringArgs(slugs)}
}

// migrateScriptStatements moves the slugs' history to ProxmoxVE. mv_daily_stats and
// mv_daily_errors have repo_source in their key, so their rows are copied and deleted;
// the per-source views without nsapp are rebuilt from the raw table for the affected days.
func migrateScriptStatements(slugs, days []string) []sqlStmt {
	in, sa := inList(len(slugs)), stringArgs(slugs)
	dayIn, da := inList(len(days)), stringArgs(days)
	sources := "('ProxmoxVE','DevScripts','" + legacyDevSource + "')"
	const sync = " SETTINGS mutations_sync = 2"

	stmts := []sqlStmt{
		{fmt.Sprintf(`INSERT INTO telemetry_db.mv_daily_stats
			(day, nsapp, type, repo_source, total, success, failed, aborted, installing)
			SELECT day, nsapp, type, 'ProxmoxVE', total, success, failed, aborted, installing
			FROM telemetry_db.mv_daily_stats WHERE nsapp IN (%s) AND repo_source IN %s`, in, devSourcesSQL), sa},
		{fmt.Sprintf(`ALTER TABLE telemetry_db.mv_daily_stats DELETE
			WHERE nsapp IN (%s) AND repo_source IN %s`, in, devSourcesSQL) + sync, sa},
		{fmt.Sprintf(`INSERT INTO telemetry_db.mv_daily_errors
			(day, nsapp, type, exit_code, error_category, repo_source, cnt)
			SELECT day, nsapp, type, exit_code, error_category, 'ProxmoxVE', cnt
			FROM telemetry_db.mv_daily_errors WHERE nsapp IN (%s) AND repo_source IN %s`, in, devSourcesSQL), sa},
		{fmt.Sprintf(`ALTER TABLE telemetry_db.mv_daily_errors DELETE
			WHERE nsapp IN (%s) AND repo_source IN %s`, in, devSourcesSQL) + sync, sa},
		{fmt.Sprintf(`ALTER TABLE telemetry_db.telemetry
			UPDATE repo_source = 'ProxmoxVE', repo_slug = 'community-scripts/ProxmoxVE', migrated_from = repo_source
			WHERE nsapp IN (%s) AND repo_source IN %s`, in, devSourcesSQL) + sync, sa},
	}

	for _, v := range []struct{ table, col string }{
		{"mv_daily_os", "os_type"}, {"mv_daily_method", "method"}, {"mv_daily_pve", "pve_version"},
	} {
		stmts = append(stmts,
			sqlStmt{fmt.Sprintf(`ALTER TABLE telemetry_db.%s DELETE
				WHERE day IN (%s) AND repo_source IN %s`, v.table, dayIn, sources) + sync, da},
			sqlStmt{fmt.Sprintf(`INSERT INTO telemetry_db.%[1]s (day, repo_source, %[2]s, cnt)
				SELECT toDate(created), repo_source, %[2]s, count()
				FROM telemetry_db.telemetry
				WHERE toDate(created) IN (%[3]s) AND repo_source IN %[4]s
				  AND %[2]s != '' AND status IN ('success','failed','aborted','unknown')
				GROUP BY toDate(created), repo_source, %[2]s`, v.table, v.col, dayIn, sources), da},
		)
	}
	return stmts
}

func (ch *CHClient) MigrateScript(ctx context.Context, slugs []string) (int, int, error) {
	count := migrateCountSQL(slugs)
	var moved uint64
	var days []string
	if err := ch.db.QueryRowContext(ctx, count.q, count.args...).Scan(&moved, &days); err != nil {
		return 0, 0, fmt.Errorf("count: %w", err)
	}
	if moved == 0 {
		return 0, 0, nil
	}
	for i, s := range migrateScriptStatements(slugs, days) {
		if _, err := ch.db.ExecContext(ctx, s.q, s.args...); err != nil {
			return 0, 0, fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	return int(moved), len(days), nil
}

package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed public
var publicFS embed.FS

// How far back the orphan-terminal count and cleanup look. Bounded because the
// query is a NOT IN over the same table, and because rows older than this are
// past the point where the distinction still matters.
const orphanWindowDays = 90

type Config struct {
	ListenAddr         string
	TrustedProxiesCIDR []string

	// ClickHouse (primary telemetry store)
	CHDSN string // clickhouse://user:pass@host:9000/telemetry_db

	// Limits
	MaxBodyBytes     int64
	RateLimitRPM     int           // requests per minute per key
	RateBurst        int           // burst tokens
	RateKeyMode      string        // "ip" or "header"
	RateKeyHeader    string        // e.g. "X-Telemetry-Key"
	RequestTimeout   time.Duration // upstream timeout
	EnableReqLogging bool          // default false (GDPR-friendly)

	// RetentionDays sets a TTL on the raw telemetry table: rows older than this
	// are dropped by ClickHouse during its normal merges, with no job to run and
	// nothing to schedule.
	//
	// Off by default, and deliberately so. Without it the table grows without
	// bound, which is the real state today -- but switching it on deletes
	// history irreversibly, and how far back the project wants to see is a
	// decision, not a default someone should inherit by upgrading. Set
	// TELEMETRY_RETENTION_DAYS when that decision is made; 400 keeps thirteen
	// months, which covers the longest window any dashboard offers.
	RetentionDays int

	// Cache
	RedisURL     string
	EnableRedis  bool
	CacheTTL     time.Duration
	CacheEnabled bool

	// Alerts (SMTP)
	AlertEnabled          bool
	SMTPHost              string
	SMTPPort              int
	SMTPUser              string
	SMTPPassword          string
	SMTPFrom              string
	SMTPTo                []string
	SMTPUseTLS            bool
	AlertFailureThreshold float64
	AlertCheckInterval    time.Duration
	AlertCooldown         time.Duration

	// GitHub Integration
	GitHubToken   string // Personal access token for creating issues
	GitHubOwner   string // Repository owner (e.g., "community-scripts")
	GitHubRepo    string // Repository name (e.g., "ProxmoxVE")
	AdminPassword string // Password to protect admin actions (issue creation)
}

// TelemetryIn matches payload from api.func (bash client)
type TelemetryIn struct {
	// Required
	RandomID    string `json:"random_id"`              // Session UUID
	ExecutionID string `json:"execution_id,omitempty"` // Unique execution ID
	Type        string `json:"type"`                   // "lxc", "vm", "turnkey", "pve", "addon"
	NSAPP       string `json:"nsapp"`                  // Application name (e.g., "jellyfin")
	Status      string `json:"status"`                 // "installing", "success", "failed", "aborted", "unknown"

	// Container/VM specs
	CTType    int `json:"ct_type,omitempty"`    // 1=unprivileged, 2=privileged/VM
	DiskSize  int `json:"disk_size,omitempty"`  // GB
	CoreCount int `json:"core_count,omitempty"` // CPU cores
	RAMSize   int `json:"ram_size,omitempty"`   // MB

	// System info
	OsType    string `json:"os_type,omitempty"`    // "debian", "ubuntu", "alpine", etc.
	OsVersion string `json:"os_version,omitempty"` // "12", "24.04", etc.
	PveVer    string `json:"pve_version,omitempty"`

	// Optional
	Method   string `json:"method,omitempty"`    // "default", "advanced"
	Error    string `json:"error,omitempty"`     // Error description (max 120 chars)
	ExitCode int    `json:"exit_code,omitempty"` // 0-255

	// === EXTENDED FIELDS ===

	// GPU Passthrough stats
	GPUVendor      string `json:"gpu_vendor,omitempty"`      // "intel", "amd", "nvidia"
	GPUModel       string `json:"gpu_model,omitempty"`       // e.g., "Intel Arc Graphics"
	GPUPassthrough string `json:"gpu_passthrough,omitempty"` // "igpu", "dgpu", "vgpu", "none"

	// CPU stats
	CPUVendor string `json:"cpu_vendor,omitempty"` // "intel", "amd", "arm"
	CPUModel  string `json:"cpu_model,omitempty"`  // e.g., "Intel Core Ultra 7 155H"

	// RAM stats
	RAMSpeed string `json:"ram_speed,omitempty"` // e.g., "4800" (MT/s)

	// Performance metrics
	InstallDuration int `json:"install_duration,omitempty"` // Seconds

	// Error categorization
	ErrorCategory string `json:"error_category,omitempty"` // "network", "storage", "dependency", "permission", "timeout", "unknown"

	// Repository source for collection routing
	RepoSource string `json:"repo_source,omitempty"` // "ProxmoxVE", "ProxmoxVED", or "external"

	// Dynamic repository slug "owner/repo" (e.g. "community-scripts/ProxmoxVE", "MickLesk/ProxmoxVE")
	RepoSlug string `json:"repo_slug,omitempty"`

	// HasArm is true when the install actually ran on arm64 hardware (only
	// possible for scripts that declare var_arm64=yes).
	HasArm bool `json:"has_arm,omitempty"`

	// Platform is the virtualization platform: "pve" (Proxmox VE) or "incus".
	// Derived from pve_version when the client doesn't send it.
	Platform string `json:"platform,omitempty"`

	// PayloadVersion is the engine's schema version. 0 means a client from
	// before it existed, which is the only honest way to say "unknown age" --
	// inferring it from which fields happen to be present is guesswork.
	PayloadVersion int `json:"payload_version,omitempty"`

	// Arch is the CPU architecture, e.g. amd64 or arm64. The engine has always
	// sent it; the server dropped it on the floor until payload version 2.
	Arch string `json:"arch,omitempty"`

	// FailedCommand and FailedLine locate a failure without parsing the
	// free-text error string it used to be flattened into.
	FailedCommand string `json:"failed_command,omitempty"`
	FailedLine    int    `json:"failed_line,omitempty"`

	// KernelVersion and AppVersion were collected by the engine long before
	// anything transmitted them.
	KernelVersion string `json:"kernel_version,omitempty"`
	AppVersion    string `json:"app_version,omitempty"`
}

// TelemetryOut is the output shape for telemetry records
type TelemetryOut struct {
	RandomID    string `json:"random_id"`
	ExecutionID string `json:"execution_id,omitempty"`
	Type        string `json:"type"`
	NSAPP       string `json:"nsapp"`
	Status      string `json:"status"`
	CTType      int    `json:"ct_type,omitempty"`
	DiskSize    int    `json:"disk_size,omitempty"`
	CoreCount   int    `json:"core_count,omitempty"`
	RAMSize     int    `json:"ram_size,omitempty"`
	OsType      string `json:"os_type,omitempty"`
	OsVersion   string `json:"os_version,omitempty"`
	PveVer      string `json:"pve_version,omitempty"`
	Method      string `json:"method,omitempty"`
	Error       string `json:"error,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`

	// Extended fields
	GPUVendor       string `json:"gpu_vendor,omitempty"`
	GPUModel        string `json:"gpu_model,omitempty"`
	GPUPassthrough  string `json:"gpu_passthrough,omitempty"`
	CPUVendor       string `json:"cpu_vendor,omitempty"`
	CPUModel        string `json:"cpu_model,omitempty"`
	RAMSpeed        string `json:"ram_speed,omitempty"`
	InstallDuration int    `json:"install_duration,omitempty"`
	ErrorCategory   string `json:"error_category,omitempty"`

	// Repository source: "ProxmoxVE", "ProxmoxVED", or "external"
	RepoSource string `json:"repo_source,omitempty"`

	// Dynamic repository slug "owner/repo"
	RepoSlug string `json:"repo_slug,omitempty"`

	// HasArm is true when the install ran on arm64 hardware.
	HasArm bool `json:"has_arm,omitempty"`

	// Platform is the virtualization platform: "pve" or "incus".
	Platform string `json:"platform,omitempty"`

	// Installation pipeline: JSON array [{s:"installing",t:"..."}, ...] (server-built for API responses)
	Pipeline string `json:"pipeline,omitempty"`

	// Payload version 2 additions. See TelemetryIn for why each exists.
	PayloadVersion int    `json:"payload_version,omitempty"`
	Arch           string `json:"arch,omitempty"`
	FailedCommand  string `json:"failed_command,omitempty"`
	FailedLine     int    `json:"failed_line,omitempty"`
	KernelVersion  string `json:"kernel_version,omitempty"`
	AppVersion     string `json:"app_version,omitempty"`
}

// TelemetryStatusUpdate contains only fields needed for status updates
type TelemetryStatusUpdate struct {
	Status          string `json:"status"`
	ExecutionID     string `json:"execution_id,omitempty"`
	Error           string `json:"error,omitempty"`
	ExitCode        int    `json:"exit_code"`
	InstallDuration int    `json:"install_duration,omitempty"`
	ErrorCategory   string `json:"error_category,omitempty"`
	GPUVendor       string `json:"gpu_vendor,omitempty"`
	GPUModel        string `json:"gpu_model,omitempty"`
	GPUPassthrough  string `json:"gpu_passthrough,omitempty"`
	CPUVendor       string `json:"cpu_vendor,omitempty"`
	CPUModel        string `json:"cpu_model,omitempty"`
	RAMSpeed        string `json:"ram_speed,omitempty"`
	RepoSource      string `json:"repo_source,omitempty"`
	RepoSlug        string `json:"repo_slug,omitempty"`
}

// Allowed values for 'repo_source' field
var allowedRepoSource = map[string]bool{
	"ProxmoxVE":  true,
	"ProxmoxVED": true,
	"external":   true,
}

// ---------- Write-Ahead Queue ----------
// Decouples HTTP accept from ClickHouse write. The /telemetry handler enqueues
// work and returns 202 immediately. A pool of workers drains the queue with retries.

// WriteItem is a single telemetry payload queued for ClickHouse write.
type WriteItem struct {
	Payload   TelemetryOut
	Attempt   int
	EnqueueAt time.Time
}

// WriteQueue buffers telemetry writes and processes them via worker goroutines.
type WriteQueue struct {
	ch       chan WriteItem
	client   *CHClient
	workers  int
	maxRetry int
	index    *ExecIndex // in-memory execution_id dedup
	inFlight atomic.Int64
}

// NewWriteQueue creates a buffered write queue with the given capacity and worker count.
func NewWriteQueue(client *CHClient, capacity, workers int, index *ExecIndex) *WriteQueue {
	wq := &WriteQueue{
		ch:       make(chan WriteItem, capacity),
		client:   client,
		workers:  workers,
		maxRetry: 3,
		index:    index,
	}
	return wq
}

// Start launches the worker goroutines.
func (wq *WriteQueue) Start() {
	for i := 0; i < wq.workers; i++ {
		go wq.worker(i)
	}
	log.Printf("[QUEUE] Started %d write workers (buffer=%d)", wq.workers, cap(wq.ch))
}

// Enqueue adds a payload to the write queue. Returns false if the queue is full.
func (wq *WriteQueue) Enqueue(payload TelemetryOut) bool {
	select {
	case wq.ch <- WriteItem{Payload: payload, Attempt: 0, EnqueueAt: time.Now()}:
		return true
	default:
		return false
	}
}

// Len returns the current queue depth.
func (wq *WriteQueue) Len() int {
	return len(wq.ch)
}

// Stop blocks until the queue is fully drained (buffer empty and no in-flight
// writes) or the timeout elapses. Call it after the HTTP server has stopped
// accepting new requests so a deploy/restart doesn't silently lose queued
// telemetry. The channel is intentionally left open so in-flight retries can
// re-enqueue without panicking on a closed channel.
func (wq *WriteQueue) Stop(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if wq.Len() == 0 && wq.inFlight.Load() == 0 {
			log.Printf("[QUEUE] drained cleanly")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("[QUEUE] drain timed out after %v (qlen=%d, in-flight=%d) — remaining writes lost",
		timeout, wq.Len(), wq.inFlight.Load())
}

func (wq *WriteQueue) worker(id int) {
	for item := range wq.ch {
		// inFlight stays incremented for the entire iteration (including retry
		// backoff and re-enqueue) so Stop() never reports "drained" while a
		// worker is still about to put an item back on the queue.
		wq.inFlight.Add(1)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := wq.processItem(ctx, item)
		cancel()

		if err != nil {
			item.Attempt++
			if item.Attempt < wq.maxRetry {
				// Exponential backoff: 1s, 2s, 4s
				backoff := time.Duration(1<<uint(item.Attempt)) * time.Second
				time.Sleep(backoff)
				// Re-enqueue for retry (non-blocking â€” drop if queue full)
				select {
				case wq.ch <- item:
				default:
					log.Printf("[QUEUE] worker %d: retry queue full, dropping nsapp=%s status=%s exec=%s (attempt %d)",
						id, item.Payload.NSAPP, item.Payload.Status, item.Payload.ExecutionID, item.Attempt)
				}
			} else {
				log.Printf("[QUEUE] worker %d: final failure nsapp=%s status=%s exec=%s: %v",
					id, item.Payload.NSAPP, item.Payload.Status, item.Payload.ExecutionID, err)
			}
		}
		wq.inFlight.Add(-1)
	}
}

// isTerminalStatus reports whether a status represents a final outcome of an execution.
func isTerminalStatus(status string) bool {
	switch status {
	case "success", "failed", "aborted", "unknown":
		return true
	}
	return false
}

// processItem performs the ClickHouse INSERT.
// Every event is a new row in ClickHouse (append-only). There is no find+update â€” every event is a new row.
//
// Deduplication strategy (prevents the over-counting that inflates failure rates):
//   - "installing": only the first event per execution_id is written.
//   - terminal events (success/failed/aborted/unknown): only the FIRST terminal
//     event per execution_id is written. The bash client legitimately reports the
//     same failure multiple times (host trap + EXIT trap + container abort), so
//     without this guard every failure was counted 2-4Ã—.
func (wq *WriteQueue) processItem(ctx context.Context, item WriteItem) error {
	payload := item.Payload

	// Dedup: skip duplicate "installing" events for the same execution_id
	if payload.Status == "installing" && payload.ExecutionID != "" {
		if _, found := wq.index.Get(payload.ExecutionID); found {
			return nil
		}
	}

	// Dedup: only the first terminal event per execution_id is persisted.
	terminalMarked := false
	if isTerminalStatus(payload.Status) && payload.ExecutionID != "" {
		// Atomic check-and-set in memory (handles concurrent workers).
		if !wq.index.MarkTerminalIfAbsent(payload.ExecutionID) {
			return nil // a terminal event for this execution was already handled
		}
		terminalMarked = true
		// In-memory miss: also check the DB (covers process restarts / multi-instance).
		if has, err := wq.client.HasTerminalExecutionID(ctx, payload.ExecutionID); err == nil && has {
			return nil // a terminal row already exists in ClickHouse â€” keep it marked
		}
	}

	// INSERT into ClickHouse (all events â€” installing, configuring, success, failed, etc.)
	if err := wq.client.InsertTelemetry(ctx, payload); err != nil {
		// Roll back the terminal mark so a retry can still write the row.
		if terminalMarked {
			wq.index.UnmarkTerminal(payload.ExecutionID)
		}
		return err
	}

	// Update in-memory index
	if payload.ExecutionID != "" {
		switch payload.Status {
		case "installing":
			wq.index.Set(payload.ExecutionID, payload.ExecutionID)
		case "success", "failed", "aborted", "unknown":
			wq.index.Delete(payload.ExecutionID)
		}
	}
	return nil
}

// ---------- In-Memory Execution ID Index ----------
// Maps execution_id â†’ PB record_id to avoid repeated FindRecord calls.

type ExecIndex struct {
	m        sync.Map // execution_id -> record_id (tracks active "installing" executions)
	terminal sync.Map // execution_id -> time.Time (tracks executions that already got a terminal row)
}

func NewExecIndex() *ExecIndex {
	return &ExecIndex{}
}

func (idx *ExecIndex) Get(executionID string) (string, bool) {
	v, ok := idx.m.Load(executionID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (idx *ExecIndex) Set(executionID, recordID string) {
	if executionID != "" && recordID != "" {
		idx.m.Store(executionID, recordID)
	}
}

func (idx *ExecIndex) Delete(executionID string) {
	idx.m.Delete(executionID)
}

// MarkTerminalIfAbsent atomically records that a terminal event for executionID
// is being handled. It returns true if this caller is the first (i.e. the event
// should be written) and false if a terminal event was already seen.
func (idx *ExecIndex) MarkTerminalIfAbsent(executionID string) bool {
	if executionID == "" {
		return true
	}
	_, loaded := idx.terminal.LoadOrStore(executionID, time.Now())
	return !loaded
}

// UnmarkTerminal removes a terminal mark (used to roll back after a failed write).
func (idx *ExecIndex) UnmarkTerminal(executionID string) {
	if executionID != "" {
		idx.terminal.Delete(executionID)
	}
}

// StartJanitor periodically evicts old terminal marks to bound memory usage.
// Duplicate terminal events for one execution always arrive within seconds, so a
// generous window is safe; anything older is covered by the ClickHouse fallback check.
func (idx *ExecIndex) StartJanitor() {
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for range t.C {
			cutoff := time.Now().Add(-48 * time.Hour)
			idx.terminal.Range(func(k, v interface{}) bool {
				if ts, ok := v.(time.Time); ok && ts.Before(cutoff) {
					idx.terminal.Delete(k)
				}
				return true
			})
		}
	}()
}

// -------- Rate limiter (token bucket / minute window, simple) --------
type bucket struct {
	tokens int
	reset  time.Time
}

type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rpm      int
	burst    int
	window   time.Duration
	cleanInt time.Duration
}

func NewRateLimiter(rpm, burst int) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rpm:      rpm,
		burst:    burst,
		window:   time.Minute,
		cleanInt: 5 * time.Minute,
	}
	go rl.cleanupLoop()
	return rl
}

func (r *RateLimiter) cleanupLoop() {
	t := time.NewTicker(r.cleanInt)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		r.mu.Lock()
		for k, b := range r.buckets {
			if now.After(b.reset.Add(2 * r.window)) {
				delete(r.buckets, k)
			}
		}
		r.mu.Unlock()
	}
}

func (r *RateLimiter) Allow(key string) bool {
	if r.rpm <= 0 {
		return true
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.buckets[key]
	if !ok || now.After(b.reset) {
		r.buckets[key] = &bucket{tokens: min(r.burst, r.rpm), reset: now.Add(r.window)}
		b = r.buckets[key]
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// -------- Utility: GDPR-safe key extraction --------

type ProxyTrust struct {
	nets []*net.IPNet
}

func NewProxyTrust(cidrs []string) (*ProxyTrust, error) {
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, err
		}
		nets = append(nets, n)
	}
	return &ProxyTrust{nets: nets}, nil
}

func (pt *ProxyTrust) isTrusted(ip net.IP) bool {
	for _, n := range pt.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isPrivateIP returns true if the IP is in RFC 1918 / RFC 6598 private ranges
// (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10) or loopback.
// These are always trusted as reverse proxy sources.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	privateRanges := []struct {
		start net.IP
		end   net.IP
	}{
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")},
		{net.ParseIP("100.64.0.0"), net.ParseIP("100.127.255.255")},
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, r := range privateRanges {
		if bytes.Compare(ip4, r.start.To4()) >= 0 && bytes.Compare(ip4, r.end.To4()) <= 0 {
			return true
		}
	}
	return false
}

func getClientIP(r *http.Request, pt *ProxyTrust) net.IP {
	// If behind reverse proxy, trust X-Forwarded-For if remote is a configured
	// trusted proxy OR a private/RFC1918 IP (common Docker/K8s/reverse proxy setup).
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	remote := net.ParseIP(host)
	if remote == nil {
		return nil
	}

	trusted := (pt != nil && pt.isTrusted(remote)) || isPrivateIP(remote)
	if trusted {
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			parts := strings.Split(xff, ",")
			ip := net.ParseIP(strings.TrimSpace(parts[0]))
			if ip != nil {
				return ip
			}
		}
	}
	return remote
}

// -------- Validation (strict allowlist) --------

var (
	// Allowed values for 'type' field
	allowedType = map[string]bool{"lxc": true, "vm": true, "turnkey": true, "pve": true, "addon": true, "tool": true}

	// Allowed values for 'status' field
	allowedStatus = map[string]bool{"installing": true, "validation": true, "configuring": true, "success": true, "failed": true, "aborted": true, "unknown": true}

	// Allowed values for 'os_type' field
	allowedOsType = map[string]bool{
		"debian": true, "ubuntu": true, "alpine": true, "devuan": true,
		"fedora": true, "rocky": true, "alma": true, "centos": true,
		"opensuse": true, "gentoo": true, "openeuler": true,
		// VM-specific OS types
		"homeassistant": true, "opnsense": true, "openwrt": true,
		"mikrotik": true, "umbrel-os": true, "pimox-haos": true,
		"owncloud": true, "turnkey-nextcloud": true, "arch-linux": true,
	}

	// Allowed values for 'gpu_vendor' field
	allowedGPUVendor = map[string]bool{"intel": true, "amd": true, "nvidia": true, "unknown": true, "": true}

	// Allowed values for 'gpu_passthrough' field
	allowedGPUPassthrough = map[string]bool{"igpu": true, "dgpu": true, "vgpu": true, "none": true, "unknown": true, "": true}

	// Allowed values for 'cpu_vendor' field
	allowedCPUVendor = map[string]bool{"intel": true, "amd": true, "arm": true, "apple": true, "qualcomm": true, "unknown": true, "": true}

	// Allowed values for 'error_category' field
	allowedErrorCategory = map[string]bool{
		"network": true, "storage": true, "dependency": true, "permission": true,
		"timeout": true, "config": true, "resource": true, "unknown": true, "": true,
		"user_aborted": true, "apt": true, "command_not_found": true,
		"service": true, "database": true, "signal": true, "proxmox": true,
		"shell": true, "build": true, "preflight": true, "runtime": true,
	}

	// exitCodeInfo consolidates description and category for all known exit codes.
	// This is the single source of truth â€” dashboard.go and all other code should
	// use getExitCodeDescription() / getExitCodeCategory() instead of duplicating.
	exitCodeInfo = map[int]struct {
		Desc     string
		Category string
	}{
		// --- Generic / Shell ---
		0: {"Success", ""},
		1: {"General error", "unknown"},
		2: {"Misuse of shell builtins", "unknown"},
		3: {"General syntax or argument error", "unknown"},

		// --- curl / wget ---
		4:  {"curl: Feature not supported or protocol error", "network"},
		5:  {"curl: Could not resolve proxy", "network"},
		6:  {"curl: DNS resolution failed", "network"},
		7:  {"curl: Connection refused / host down", "network"},
		8:  {"curl: Server reply error", "network"},
		16: {"curl: HTTP/2 framing layer error", "network"},
		18: {"curl: Partial file (transfer incomplete)", "network"},
		22: {"curl: HTTP error (404/500 etc.)", "network"},
		23: {"curl: Write error (disk full?)", "storage"},
		24: {"curl: Write to local file failed", "storage"},
		25: {"curl: Upload failed", "network"},
		26: {"curl: Read error on local file (I/O)", "storage"},
		27: {"curl: Out of memory", "resource"},
		28: {"curl: Connection timed out", "timeout"},
		30: {"curl: FTP port command failed", "network"},
		32: {"curl: FTP SIZE command failed", "network"},
		33: {"curl: HTTP range error", "network"},
		34: {"curl: HTTP post error", "network"},
		35: {"curl: SSL/TLS handshake failed", "network"},
		36: {"curl: FTP bad download resume", "network"},
		47: {"curl: Too many redirects", "network"},
		51: {"curl: SSL peer certificate verification failed", "network"},
		52: {"curl: Empty reply from server", "network"},
		55: {"curl: Failed sending network data", "network"},
		56: {"curl: Receive error (connection reset)", "network"},
		59: {"curl: Couldn't use specified SSL cipher", "network"},
		75: {"Temporary failure (retry later)", "network"},
		78: {"curl: Remote file not found (404)", "network"},
		92: {"curl: HTTP/2 stream error", "network"},
		95: {"curl: HTTP/3 layer error", "network"},

		// --- Docker / Privileged ---
		10: {"Docker / privileged mode required", "config"},

		// --- BSD sysexits.h (64-78) ---
		64: {"Usage error (wrong arguments)", "config"},
		65: {"Data format error (bad input data)", "unknown"},
		66: {"Input file not found", "unknown"},
		67: {"User not found", "unknown"},
		68: {"Host not found", "network"},
		69: {"Service unavailable", "service"},
		70: {"Internal software error", "unknown"},
		71: {"System error (OS-level failure)", "unknown"},
		72: {"Critical OS file missing", "unknown"},
		73: {"Cannot create output file", "storage"},
		74: {"I/O error", "storage"},
		76: {"Remote protocol error", "network"},
		77: {"Permission denied", "permission"},

		// --- APT / DPKG ---
		100: {"APT: Package manager error (broken packages)", "apt"},
		// Not apt. apt reports its problems as 100; 101 is what cargo returns for
		// any failure at all, and in fourteen days of data every single exit-101
		// signature was a Rust build -- scanopy 48, vaultwarden 27, oxicloud 25,
		// a hundred of the hundred and twenty. Calling that "APT: Configuration
		// error (bad sources)" sent people to look at sources.list for a
		// compiler error. Left to the evidence below, like exit 1.
		101: {"Build or configuration error", "unknown"},
		102: {"APT: Lock held by another process", "apt"},

		// --- Script Validation & Setup (103-123) ---
		103: {"Validation: Shell is not Bash", "preflight"},
		104: {"Validation: Not running as root", "preflight"},
		105: {"Validation: PVE version not supported", "preflight"},
		106: {"Validation: Architecture not supported (ARM/PiMox)", "preflight"},
		107: {"Validation: Kernel key parameters unreadable", "preflight"},
		108: {"Validation: Kernel key limits exceeded", "preflight"},
		109: {"Proxmox: No available container ID", "proxmox"},
		110: {"Proxmox: Failed to apply default.vars", "proxmox"},
		111: {"Proxmox: App defaults file not available", "proxmox"},
		112: {"Proxmox: Invalid install menu option", "config"},
		113: {"LXC: Under-provisioned â€” user aborted", "user_aborted"},
		114: {"LXC: Storage too low â€” user aborted", "user_aborted"},
		115: {"Download: install.func failed or incomplete", "network"},
		116: {"Proxmox: Default bridge vmbr0 not found", "config"},
		117: {"LXC: Container did not reach running state", "proxmox"},
		118: {"LXC: No IP assigned after timeout", "timeout"},
		119: {"Proxmox: No valid storage for rootdir", "storage"},
		120: {"Proxmox: No valid storage for vztmpl", "storage"},
		121: {"LXC: Container network not ready", "network"},
		122: {"LXC: No internet â€” user declined", "user_aborted"},
		123: {"LXC: Local IP detection failed", "network"},

		// --- Common shell/system errors ---
		124: {"Command timed out", "timeout"},
		125: {"Docker daemon error / command failed to start", "config"},
		126: {"Command cannot execute (permission problem)", "permission"},
		127: {"Command not found", "command_not_found"},
		128: {"Invalid argument to exit", "signal"},
		129: {"Killed by SIGHUP (terminal closed)", "user_aborted"},
		130: {"Script terminated by Ctrl+C (SIGINT)", "user_aborted"},
		131: {"Killed by SIGQUIT (core dump)", "signal"},
		132: {"Killed by SIGILL (illegal instruction)", "signal"},
		134: {"Process aborted (SIGABRT)", "signal"},
		137: {"Process killed (SIGKILL) â€” likely OOM", "resource"},
		139: {"Segmentation fault (SIGSEGV)", "unknown"},
		141: {"Broken pipe (SIGPIPE)", "signal"},
		143: {"Process terminated (SIGTERM)", "signal"},
		144: {"Killed by signal 16 (SIGUSR1/SIGSTKFLT)", "signal"},
		146: {"Killed by signal 18 (SIGTSTP)", "signal"},

		// --- Systemd / Service errors (150-154) ---
		150: {"Systemd: Service failed to start", "service"},
		151: {"Systemd: Service unit not found", "service"},
		152: {"Permission denied (EACCES)", "permission"},
		153: {"Build/compile failed (make/gcc/cmake)", "build"},
		154: {"Node.js: Native addon build failed (node-gyp)", "build"},

		// --- Python / pip / uv (160-162) ---
		160: {"Python: Virtualenv/uv environment missing or broken", "dependency"},
		161: {"Python: Dependency resolution failed", "dependency"},
		162: {"Python: Installation aborted (EXTERNALLY-MANAGED)", "dependency"},

		// --- PostgreSQL (170-173) ---
		170: {"PostgreSQL: Connection failed", "database"},
		171: {"PostgreSQL: Authentication failed", "database"},
		172: {"PostgreSQL: Database does not exist", "database"},
		173: {"PostgreSQL: Fatal error in query", "database"},

		// --- MySQL / MariaDB (180-183) ---
		180: {"MySQL/MariaDB: Connection failed", "database"},
		181: {"MySQL/MariaDB: Authentication failed", "database"},
		182: {"MySQL/MariaDB: Database does not exist", "database"},
		183: {"MySQL/MariaDB: Fatal error in query", "database"},

		// --- MongoDB (190-193) ---
		190: {"MongoDB: Connection failed", "database"},
		191: {"MongoDB: Authentication failed", "database"},
		192: {"MongoDB: Database not found", "database"},
		193: {"MongoDB: Fatal query error", "database"},

		// --- Proxmox Custom Codes (200-231) ---
		200: {"Proxmox: Failed to create lock file", "proxmox"},
		203: {"Proxmox: Missing CTID variable", "config"},
		204: {"Proxmox: Missing PCT_OSTYPE variable", "config"},
		205: {"Proxmox: Invalid CTID (<100)", "config"},
		206: {"Proxmox: CTID already in use", "config"},
		207: {"Proxmox: Password contains unescaped special chars", "config"},
		208: {"Proxmox: Invalid configuration (DNS/MAC/Network)", "config"},
		209: {"Proxmox: Container creation failed", "proxmox"},
		210: {"Proxmox: Cluster not quorate", "proxmox"},
		211: {"Proxmox: Timeout waiting for template lock", "timeout"},
		212: {"Proxmox: Storage 'iscsidirect' does not support containers", "proxmox"},
		213: {"Proxmox: Storage does not support 'rootdir' content", "proxmox"},
		214: {"Proxmox: Not enough storage space", "storage"},
		215: {"Proxmox: Container created but not listed (ghost state)", "proxmox"},
		216: {"Proxmox: RootFS entry missing in config", "proxmox"},
		217: {"Proxmox: Storage not accessible", "storage"},
		218: {"Proxmox: Template file corrupted or incomplete", "proxmox"},
		219: {"Proxmox: CephFS does not support containers", "storage"},
		220: {"Proxmox: Unable to resolve template path", "proxmox"},
		221: {"Proxmox: Template file not readable", "proxmox"},
		222: {"Proxmox: Template download failed", "proxmox"},
		223: {"Proxmox: Template not available after download", "proxmox"},
		224: {"Proxmox: PBS storage is for backups only", "storage"},
		225: {"Proxmox: No template available for OS/Version", "proxmox"},
		226: {"Proxmox: VM disk import or post-creation setup failed", "proxmox"},
		231: {"Proxmox: LXC stack upgrade failed", "proxmox"},

		// --- Tools & Addon Scripts (232-238) ---
		232: {"Tools: Wrong execution environment", "config"},
		233: {"Tools: Application not installed (update prerequisite missing)", "config"},
		234: {"Tools: No LXC containers found", "proxmox"},
		235: {"Tools: Backup or restore operation failed", "storage"},
		236: {"Tools: Required hardware not detected", "config"},
		237: {"Tools: Dependency package installation failed", "dependency"},
		238: {"Tools: OS or distribution not supported", "config"},

		// --- Node.js / npm (239-249) ---
		239: {"npm/Node.js: Unexpected runtime error", "dependency"},
		243: {"Node.js: Out of memory (heap overflow)", "resource"},
		245: {"Node.js: Invalid command-line option", "config"},
		246: {"Node.js: Internal JavaScript Parse Error", "unknown"},
		247: {"Node.js: Fatal internal error", "unknown"},
		248: {"Node.js: Invalid C++ addon / N-API failure", "unknown"},
		249: {"npm/pnpm/yarn: Unknown fatal error", "unknown"},

		// --- Application Install/Update Errors (250-254) ---
		250: {"App: Download failed or version not determined", "network"},
		251: {"App: File extraction failed (corrupt/incomplete)", "storage"},
		252: {"App: Required file or resource not found", "unknown"},
		253: {"App: Data migration required â€” update aborted", "config"},
		254: {"App: User declined prompt or input timed out", "user_aborted"},

		// --- DPKG ---
		255: {"DPKG: Fatal internal error / set -e triggered", "apt"},
	}
)

// getExitCodeDescription returns the human-readable description for an exit code.
// Falls back to signal-based description for codes 128-191, or "Unknown" otherwise.
func getExitCodeDescription(code int) string {
	if info, ok := exitCodeInfo[code]; ok {
		return info.Desc
	}
	if code > 128 && code < 192 {
		sigNum := code - 128
		sigNames := map[int]string{
			1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 6: "SIGABRT",
			9: "SIGKILL", 11: "SIGSEGV", 13: "SIGPIPE", 15: "SIGTERM",
			24: "SIGXCPU", 25: "SIGXFSZ",
		}
		if name, ok := sigNames[sigNum]; ok {
			return fmt.Sprintf("Killed by %s (signal %d)", name, sigNum)
		}
		return fmt.Sprintf("Killed by signal %d", sigNum)
	}
	return fmt.Sprintf("Unknown (exit code %d)", code)
}

// getExitCodeCategory returns the error category for an exit code.
// Falls back to "signal" for codes 128-191, or "unknown" otherwise.
func getExitCodeCategory(code int) string {
	if info, ok := exitCodeInfo[code]; ok {
		return info.Category
	}
	if code > 128 && code < 192 {
		return "signal"
	}
	return "unknown"
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// errDiagnosticHeader matches the header the engine puts in front of the real
// error text. It comes in two shapes, and both have to be handled:
//
//	exit_code=1 | General error / Operation not permitted | at line 23: npm …
//	exit_code=1 | General error / Operation not permitted\n---\nCreating filesyst…
//
// The second occurs whenever the ERR trap had no command to record, and it is
// common: five of eighteen exit-1 samples pulled from the live API. Requiring
// the third field would have left those still matching on the description.
var errDiagnosticHeader = regexp.MustCompile(`^exit_code=\d+\s*\|[^|\n]*(\|\s*)?`)

// stripDiagnosticHeader removes that header before any keyword matching below.
//
// The middle field is our own description of the exit code, not evidence about
// what happened, and matching keywords against it was actively wrong. Exit 1 is
// described as "General error / Operation not permitted", which contains
// "operation not permitted" -- so every exit-1 failure was filed as a permission
// problem. Over fourteen days that was 944 of 5165 errors, the largest single
// category on the dashboard, and none of them had anything to do with
// permissions; the record that surfaced it was a failing `npm install -g`.
func stripDiagnosticHeader(s string) string {
	return errDiagnosticHeader.ReplaceAllString(s, "")
}

// deriveErrorCategory is the authoritative server-side error categorization.
// It first trusts the exit code (single source of truth via exitCodeInfo). For
// generic codes (1/2/exit-from-set-e) where the exit code says "unknown", it
// inspects the error text for well-known root causes so that exit_code=1 failures
// don't all collapse into the meaningless "unknown" bucket.
func deriveErrorCategory(code int, errText string) string {
	if cat := getExitCodeCategory(code); cat != "unknown" && cat != "" {
		return cat
	}
	e := strings.ToLower(stripDiagnosticHeader(errText))
	switch {
	case containsAny(e, "out of memory", "oom-kill", "cannot allocate memory", "killed process", "memory exhausted"):
		return "resource"
	case containsAny(e, "no space left", "disk full", "quota exceeded", "write error"):
		return "storage"
	case containsAny(e, "could not resolve", "connection refused", "connection timed out", "network is unreachable",
		"temporary failure in name resolution", "failed to connect", "tls handshake", "ssl certificate", "curl:", "wget:"):
		return "network"
	case containsAny(e, "permission denied", "operation not permitted", "eacces", "must be run as root"):
		return "permission"
	case containsAny(e, "unable to locate package", "broken packages", "unmet dependencies", "dpkg was interrupted",
		"held broken packages", "e: package", "apt-get", "dpkg:"):
		return "apt"
	// Compiling from source is its own failure mode and its own fix -- more
	// memory, a toolchain version, a dependency the crate needs -- so it does
	// not belong in "runtime" with things that failed while running. Checked
	// after apt on purpose: a build that died because a -dev package was
	// missing is an apt problem, and that evidence should win.
	case containsAny(e, "cargo build", "error: could not compile", "rustc", "cargo:",
		"go build", "cmake error", "make: ***", "ninja: build stopped", "c++: fatal error"):
		return "build"
	case containsAny(e, "command not found"):
		return "command_not_found"
	case containsAny(e, "timed out", "timeout"):
		return "timeout"
	case containsAny(e, "prisma", "migration.sql", "datasource", "sqlite database", "could not find the migration"):
		return "database"
	}
	return "unknown"
}

// isAbortSignal reports whether a "failed" outcome is really a user-initiated
// abort (Ctrl+C / SIGINT, terminal close / SIGHUP, or an explicit user-cancel
// message) and should be reclassified as "aborted" rather than counted as a
// failure. Used both at ingest time and when reading records back, so the two
// paths can never diverge.
func isAbortSignal(exitCode int, errText string) bool {
	if exitCode == 129 || exitCode == 130 {
		return true
	}
	e := strings.ToLower(errText)
	return containsAny(e, "sigint", "ctrl+c", "ctrl-c", "sighup",
		"aborted by user", "user abort", "cancelled by user", "no changes have been made")
}

// parseRepoFilters reads repo_source (repo) and repo_slug (slug) query parameters.
func parseRepoFilters(r *http.Request) (repoSource, repoSlug string) {
	// repo_source is OPTIONAL: no param (or "all") means all sources.
	repoSource = r.URL.Query().Get("repo")
	if repoSource == "all" {
		repoSource = ""
	}
	repoSlug = strings.TrimSpace(r.URL.Query().Get("slug"))
	return
}

// parsePlatform reads the optional platform filter ("pve" | "incus"), "" = all.
func parsePlatform(r *http.Request) string {
	p := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("platform")))
	if p == "pve" || p == "incus" {
		return p
	}
	return ""
}

func telemetryCacheKey(prefix string, days int, repoSource, repoSlug string) string {
	key := fmt.Sprintf("%s:%d:%s", prefix, days, repoSource)
	if repoSlug != "" {
		key += ":" + repoSlug
	}
	return key
}

// telemetryCacheKeyP extends the cache key with the platform filter.
func telemetryCacheKeyP(prefix string, days int, repoSource, repoSlug, platform string) string {
	key := telemetryCacheKey(prefix, days, repoSource, repoSlug)
	if platform != "" {
		key += ":p=" + platform
	}
	return key
}

// newDashCacheKey keys the consolidated page on every filter that changes it.
//
// ctype is appended here rather than passed through the shared helper, which
// predates this page and knows nothing about it. Leaving it out of the key
// would serve an lxc-filtered page to a vm request, so the omission would look
// like wrong data rather than a stale cache.
func newDashCacheKey(days int, repoSource, repoSlug, platform, ctype string) string {
	key := telemetryCacheKeyP("new", days, repoSource, repoSlug, platform)
	if ctype != "" {
		key += ":t=" + ctype
	}
	return key
}

func sanitizeShort(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// remove line breaks and high-risk chars
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// sanitizeMultiLine allows newlines (for log output) but limits total length.
func sanitizeMultiLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// ipv4Re matches IPv4 addresses (e.g. 192.168.1.100) for GDPR anonymization.
var ipv4Re = regexp.MustCompile(`(\d{1,3}\.)\d{1,3}\.\d{1,3}`)

// numericFieldSuffixRe matches JSON numeric fields with unit suffixes like 32G, 100M.
// Captures: "field_name": 123G â†’ replace with "field_name": 123
var numericFieldSuffixRe = regexp.MustCompile(`("(?:disk_size|core_count|ram_size)"\s*:\s*)(\d+)[A-Za-z]+`)

// hexEscapeRe matches invalid \xNN hex escapes in JSON strings (JSON only supports \uNNNN).
var hexEscapeRe = regexp.MustCompile(`\\x([0-9A-Fa-f]{2})`)

// sanitizeRawJSON attempts to fix common JSON encoding issues from bash clients.
// Called only when the initial json.Decode fails â€” this is a best-effort rescue.
func sanitizeRawJSON(raw []byte) []byte {
	// 1. Strip unit suffixes from numeric fields: "disk_size": 32G â†’ "disk_size": 32
	raw = numericFieldSuffixRe.ReplaceAll(raw, []byte("${1}${2}"))

	// 2. Replace \xNN hex escapes with \u00NN (valid JSON unicode escapes)
	raw = hexEscapeRe.ReplaceAll(raw, []byte(`\u00$1`))

	// 3. Replace literal control characters inside strings with safe alternatives
	// AND fix invalid escape sequences (e.g. \_ \G \P -> \\_ \\G \\P)
	// Valid JSON escapes: \" \\ \/ \b \f \n \r \t \uXXXX
	inString := false
	escaped := false
	cleaned := make([]byte, 0, len(raw))
	for _, b := range raw {
		if escaped {
			escaped = false
			// Check if this is a valid JSON escape character
			switch b {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				cleaned = append(cleaned, b)
			case 'u':
				// \uXXXX: pass through (hex digits validated by JSON decoder)
				cleaned = append(cleaned, b)
			default:
				// Invalid escape like \_ or \G: double the backslash so \G -> \\G
				cleaned = append(cleaned, '\\', b)
			}
			continue
		}
		if b == '\\' && inString {
			escaped = true
			cleaned = append(cleaned, b)
			continue
		}
		if b == '"' {
			inString = !inString
			cleaned = append(cleaned, b)
			continue
		}
		if inString && b < 0x20 {
			// Replace control characters with space (\n and \r are common, others rare)
			switch b {
			case '\n':
				cleaned = append(cleaned, '\\', 'n')
			case '\r':
				cleaned = append(cleaned, '\\', 'r')
			case '\t':
				cleaned = append(cleaned, '\\', 't')
			default:
				cleaned = append(cleaned, ' ')
			}
			continue
		}
		cleaned = append(cleaned, b)
	}
	return cleaned
}

// rescueBrokenJSON is the last-resort handler for telemetry payloads where the error
// field contains unescaped characters (typically " from awk/mawk gsub inconsistencies)
// that broke JSON parsing and couldn't be fixed by sanitizeRawJSON.
//
// Strategy: extract all simple/numeric fields via regex (they're always clean),
// then find the error field boundaries and properly re-encode its content.
func rescueBrokenJSON(raw []byte) (TelemetryIn, error) {
	var in TelemetryIn
	s := string(raw)

	// Helper: extract a simple JSON string value (no escaped quotes in value)
	strField := func(name string) string {
		re := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*"([^"]*)"`)
		m := re.FindStringSubmatch(s)
		if len(m) >= 2 {
			return m[1]
		}
		return ""
	}

	// Helper: extract a JSON integer value
	intField := func(name string) int {
		re := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*(-?\d+)`)
		m := re.FindStringSubmatch(s)
		if len(m) >= 2 {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}

	// Extract simple fields (these never contain problematic characters)
	in.RandomID = strField("random_id")
	in.ExecutionID = strField("execution_id")
	in.Type = strField("type")
	in.NSAPP = strField("nsapp")
	in.Status = strField("status")
	in.OsType = strField("os_type")
	in.OsVersion = strField("os_version")
	in.PveVer = strField("pve_version")
	in.Method = strField("method")
	in.ErrorCategory = strField("error_category")
	in.RepoSource = strField("repo_source")
	in.RepoSlug = strField("repo_slug")
	in.GPUVendor = strField("gpu_vendor")
	in.GPUModel = strField("gpu_model")
	in.GPUPassthrough = strField("gpu_passthrough")
	in.CPUVendor = strField("cpu_vendor")
	in.CPUModel = strField("cpu_model")
	in.RAMSpeed = strField("ram_speed")
	in.Platform = strField("platform")
	in.Arch = strField("arch")
	in.FailedCommand = strField("failed_command")
	in.KernelVersion = strField("kernel_version")
	in.AppVersion = strField("app_version")

	in.CTType = intField("ct_type")
	in.DiskSize = intField("disk_size")
	in.CoreCount = intField("core_count")
	in.RAMSize = intField("ram_size")
	in.ExitCode = intField("exit_code")
	in.InstallDuration = intField("install_duration")
	in.PayloadVersion = intField("payload_version")
	in.FailedLine = intField("failed_line")

	// Validate that we got the minimum required fields
	if in.RandomID == "" || in.NSAPP == "" || in.Status == "" {
		return in, fmt.Errorf("rescue failed: missing required fields (random_id=%q, nsapp=%q, status=%q)", in.RandomID, in.NSAPP, in.Status)
	}

	// Extract the error field: the one with broken escaping.
	// Find "error": " and then locate the boundary by searching for the next
	// known field pattern: ",\n followed by "error_category" or similar.
	errorRe := regexp.MustCompile(`"error"\s*:\s*"`)
	eloc := errorRe.FindStringIndex(s)
	if eloc != nil {
		valueStart := eloc[1]
		// Find the boundary: the next known field after "error"
		// Pattern in the JSON: ...error content...",\n    "error_category": "..."
		boundaryFields := []string{"error_category", "install_duration", "cpu_vendor", "gpu_vendor", "ram_speed", "repo_source"}
		endPos := -1
		for _, field := range boundaryFields {
			re := regexp.MustCompile(`",?\s*\n\s*"` + regexp.QuoteMeta(field) + `"`)
			m := re.FindStringIndex(s[valueStart:])
			if m != nil && (endPos < 0 || m[0] < endPos) {
				endPos = m[0]
			}
		}
		// Also check for end-of-object after error (in minimal payloads)
		if endPos < 0 {
			re := regexp.MustCompile(`",?\s*\n?\s*}`)
			m := re.FindStringIndex(s[valueStart:])
			if m != nil {
				endPos = m[0]
			}
		}

		if endPos > 0 {
			rawError := s[valueStart : valueStart+endPos]
			// Truncate to 16KB to keep it manageable (the full log was likely 120KB)
			if len(rawError) > 16384 {
				rawError = rawError[:16384] + "... [truncated by server rescue]"
			}
			// The raw error has broken escaping: unescape what we can, then store as plain text
			rawError = strings.ReplaceAll(rawError, "\\n", "\n")
			rawError = strings.ReplaceAll(rawError, "\\t", "\t")
			rawError = strings.ReplaceAll(rawError, `\\`, `\`)
			rawError = strings.ReplaceAll(rawError, `\"`, `"`)
			in.Error = rawError
		}
	}

	return in, nil
}

// sanitizeIPs anonymizes IPv4 addresses in log text for GDPR compliance.
// Keeps the first octet visible for debugging (e.g. "192.x.x"), strips the rest.
func sanitizeIPs(s string) string {
	if s == "" {
		return s
	}
	return ipv4Re.ReplaceAllString(s, "${1}x.x")
}

func validate(in *TelemetryIn) error {
	// Sanitize all string fields
	in.RandomID = sanitizeShort(in.RandomID, 64)
	in.ExecutionID = sanitizeShort(in.ExecutionID, 64)
	in.Type = sanitizeShort(in.Type, 8)
	in.NSAPP = sanitizeShort(in.NSAPP, 64)
	in.Status = sanitizeShort(in.Status, 16)
	in.OsType = sanitizeShort(in.OsType, 32)
	in.OsVersion = sanitizeShort(in.OsVersion, 32)
	in.PveVer = sanitizeShort(in.PveVer, 32)
	in.Method = sanitizeShort(in.Method, 32)

	// Sanitize extended fields
	in.GPUVendor = strings.ToLower(sanitizeShort(in.GPUVendor, 16))
	in.GPUModel = sanitizeShort(in.GPUModel, 64)
	in.GPUPassthrough = strings.ToLower(sanitizeShort(in.GPUPassthrough, 16))
	in.CPUVendor = strings.ToLower(sanitizeShort(in.CPUVendor, 16))
	in.CPUModel = sanitizeShort(in.CPUModel, 64)
	in.RAMSpeed = sanitizeShort(in.RAMSpeed, 16)
	in.ErrorCategory = strings.ToLower(sanitizeShort(in.ErrorCategory, 32))

	// Sanitize repo_source (routing field) and repo_slug ("owner/repo")
	in.RepoSource = sanitizeShort(in.RepoSource, 64)
	in.RepoSlug = sanitizeShort(in.RepoSlug, 128)

	// Payload version 2 fields. Bounded like every other client-supplied string:
	// the endpoint is unauthenticated, so nothing arriving here is trusted for
	// length any more than for content.
	in.Arch = strings.ToLower(sanitizeShort(in.Arch, 16))
	in.KernelVersion = sanitizeShort(in.KernelVersion, 64)
	in.AppVersion = sanitizeShort(in.AppVersion, 64)
	// Long enough for a real command, short enough to group on.
	in.FailedCommand = sanitizeShort(in.FailedCommand, 512)
	if in.FailedLine < 0 || in.FailedLine > 1_000_000 {
		in.FailedLine = 0
	}
	// A version the server has never heard of is more likely a typo or a probe
	// than a client from the future.
	if in.PayloadVersion < 0 || in.PayloadVersion > 1000 {
		in.PayloadVersion = 0
	}

	// Platform: allowlist "pve"/"incus"; derive from pve_version when missing
	// (the client sends pve_version="incus-x.y" on Incus hosts).
	in.Platform = strings.ToLower(sanitizeShort(in.Platform, 8))
	if in.Platform != "pve" && in.Platform != "incus" {
		switch {
		case strings.HasPrefix(strings.ToLower(in.PveVer), "incus"):
			in.Platform = "incus"
		case in.PveVer != "":
			in.Platform = "pve"
		default:
			in.Platform = ""
		}
	}

	// Safety net: derive repo_source from repo_slug when the routing field is
	// missing, so older/partial clients still land in the right bucket.
	if in.RepoSource == "" && in.RepoSlug != "" {
		switch {
		case strings.HasSuffix(in.RepoSlug, "/ProxmoxVE"):
			in.RepoSource = "ProxmoxVE"
		case strings.HasSuffix(in.RepoSlug, "/ProxmoxVED"):
			in.RepoSource = "ProxmoxVED"
		default:
			in.RepoSource = "external"
		}
	}

	// Default empty values to "unknown" for consistency
	if in.GPUVendor == "" {
		in.GPUVendor = "unknown"
	}
	if in.GPUPassthrough == "" {
		in.GPUPassthrough = "unknown"
	}
	if in.CPUVendor == "" {
		in.CPUVendor = "unknown"
	}

	// Allow longer error text to capture full installation log + anonymize IPs (GDPR)
	in.Error = sanitizeIPs(sanitizeMultiLine(in.Error, 131072))

	// Required fields for all requests
	if in.RandomID == "" || in.Type == "" || in.NSAPP == "" || in.Status == "" {
		return errors.New("missing required fields: random_id, type, nsapp, status")
	}

	// Normalize common typos for backwards compatibility
	if in.Status == "sucess" {
		in.Status = "success"
	}

	// Validate enums with fallback to safe defaults (never reject writes)
	if !allowedType[in.Type] {
		log.Printf("[WARN] unknown type %q from nsapp=%s, rejecting", in.Type, in.NSAPP)
		return errors.New("invalid type")
	}
	if !allowedStatus[in.Status] {
		log.Printf("[WARN] unknown status %q from nsapp=%s, falling back to 'unknown'", in.Status, in.NSAPP)
		in.Status = "unknown"
	}

	// Enum fields: fallback to "unknown" instead of rejecting
	if !allowedGPUVendor[in.GPUVendor] {
		log.Printf("[WARN] unknown gpu_vendor %q from nsapp=%s, falling back to 'unknown'", in.GPUVendor, in.NSAPP)
		in.GPUVendor = "unknown"
	}
	if !allowedGPUPassthrough[in.GPUPassthrough] {
		log.Printf("[WARN] unknown gpu_passthrough %q from nsapp=%s, falling back to 'unknown'", in.GPUPassthrough, in.NSAPP)
		in.GPUPassthrough = "unknown"
	}
	if !allowedCPUVendor[in.CPUVendor] {
		log.Printf("[WARN] unknown cpu_vendor %q from nsapp=%s, falling back to 'unknown'", in.CPUVendor, in.NSAPP)
		in.CPUVendor = "unknown"
	}
	if !allowedErrorCategory[in.ErrorCategory] {
		log.Printf("[WARN] unknown error_category %q from nsapp=%s, falling back to 'unknown'", in.ErrorCategory, in.NSAPP)
		in.ErrorCategory = "unknown"
	}

	// For status updates (not installing), skip numeric field validation
	// These are only required for initial creation
	isUpdate := in.Status != "installing"

	// os_type: normalize "-" (used by VM scripts for "not applicable") to empty
	if in.OsType == "-" || in.OsType == "none" {
		in.OsType = ""
	}
	// os_type is optional but if provided must be valid (only for lxc/vm)
	if (in.Type == "lxc" || in.Type == "vm") && in.OsType != "" && !allowedOsType[in.OsType] {
		return errors.New("invalid os_type")
	}

	// method is optional and flexible - just sanitized, no strict validation
	// Values like "default", "advanced", "mydefaults-global", "mydefaults-app" are all valid

	// Validate numeric ranges (only strict for new records)
	if !isUpdate && (in.Type == "lxc" || in.Type == "vm") {
		if in.CTType < 0 || in.CTType > 2 {
			return errors.New("invalid ct_type (must be 0, 1, or 2)")
		}
	}
	if in.DiskSize < 0 || in.DiskSize > 100000 {
		return errors.New("invalid disk_size")
	}
	if in.CoreCount < 0 || in.CoreCount > 256 {
		return errors.New("invalid core_count")
	}
	if in.RAMSize < 0 || in.RAMSize > 1048576 {
		return errors.New("invalid ram_size")
	}
	if in.ExitCode < 0 || in.ExitCode > 255 {
		return errors.New("invalid exit_code")
	}
	if in.InstallDuration < 0 || in.InstallDuration > 86400 {
		return errors.New("invalid install_duration (max 24h)")
	}

	// Validate repo_source: must be a known value or empty
	if in.RepoSource != "" && !allowedRepoSource[in.RepoSource] {
		return fmt.Errorf("rejected repo_source '%s' (must be 'ProxmoxVE', 'ProxmoxVED', or 'external')", in.RepoSource)
	}

	return nil
}

// -------- HTTP server --------

func serveHTMLFile(w http.ResponseWriter, r *http.Request, filePath string) {
	content, err := publicFS.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("Error reading embedded file %s: %v", filePath, err)
		return
	}
	// HTML must never be served stale: it carries the versioned asset links
	// (?v=N) that bust CSS/JS caches after a deploy.
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write(content)
}

func main() {
	cfg := Config{
		ListenAddr:         env("LISTEN_ADDR", ":8080"),
		TrustedProxiesCIDR: splitCSV(env("TRUSTED_PROXIES_CIDR", "")),

		// ClickHouse (primary telemetry store)
		CHDSN: env("CH_DSN", "clickhouse://default:@localhost:9000/telemetry_db"),

		MaxBodyBytes:     envInt64("MAX_BODY_BYTES", 262144),
		RateLimitRPM:     envInt("RATE_LIMIT_RPM", 300),
		RateBurst:        envInt("RATE_BURST", 60),
		RateKeyMode:      env("RATE_KEY_MODE", "ip"), // "ip" or "header"
		RateKeyHeader:    env("RATE_KEY_HEADER", "X-Telemetry-Key"),
		RequestTimeout:   time.Duration(envInt("UPSTREAM_TIMEOUT_MS", 60000)) * time.Millisecond,
		EnableReqLogging: envBool("ENABLE_REQUEST_LOGGING", false),

		// 0 = no TTL, which is the state this replaces. 400 keeps thirteen
		// months, past the longest window any dashboard offers.
		RetentionDays: envInt("TELEMETRY_RETENTION_DAYS", 0),

		// Cache config
		RedisURL:     env("REDIS_URL", ""),
		EnableRedis:  envBool("ENABLE_REDIS", false),
		CacheTTL:     time.Duration(envInt("CACHE_TTL_SECONDS", 3600)) * time.Second,
		CacheEnabled: envBool("ENABLE_CACHE", true),

		// Alert config
		AlertEnabled:          envBool("ALERT_ENABLED", false),
		SMTPHost:              env("SMTP_HOST", ""),
		SMTPPort:              envInt("SMTP_PORT", 587),
		SMTPUser:              env("SMTP_USER", ""),
		SMTPPassword:          env("SMTP_PASSWORD", ""),
		SMTPFrom:              env("SMTP_FROM", "telemetry@proxmoxved.local"),
		SMTPTo:                splitCSV(env("SMTP_TO", "")),
		SMTPUseTLS:            envBool("SMTP_USE_TLS", false),
		AlertFailureThreshold: envFloat("ALERT_FAILURE_THRESHOLD", 20.0),
		AlertCheckInterval:    time.Duration(envInt("ALERT_CHECK_INTERVAL_MIN", 15)) * time.Minute,
		AlertCooldown:         time.Duration(envInt("ALERT_COOLDOWN_MIN", 60)) * time.Minute,

		// GitHub integration
		GitHubToken:   env("GITHUB_TOKEN", ""),
		GitHubOwner:   env("GITHUB_OWNER", "community-scripts"),
		GitHubRepo:    env("GITHUB_REPO", "ProxmoxVE"),
		AdminPassword: env("ADMIN_PASSWORD", ""),
	}

	// Debug: log whether critical env vars are set (not the values!)
	log.Printf("CONFIG: ADMIN_PASSWORD set=%v (len=%d), GITHUB_TOKEN set=%v, GITHUB_OWNER=%s, GITHUB_REPO=%s",
		cfg.AdminPassword != "", len(cfg.AdminPassword), cfg.GitHubToken != "", cfg.GitHubOwner, cfg.GitHubRepo)

	var pt *ProxyTrust
	if strings.TrimSpace(env("TRUSTED_PROXIES_CIDR", "")) != "" {
		p, err := NewProxyTrust(cfg.TrustedProxiesCIDR)
		if err != nil {
			log.Fatalf("invalid TRUSTED_PROXIES_CIDR: %v", err)
		}
		pt = p
	}

	// ClickHouse: primary telemetry data store
	ch, err := NewCHClient(cfg.CHDSN)
	if err != nil {
		log.Fatalf("clickhouse: %v", err)
	}

	// Separate from the schema migration on purpose: those are additive and safe
	// to run blind, this one deletes history.
	{
		rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
		ch.ApplyRetention(rctx, cfg.RetentionDays)
		rcancel()
	}

	// Write-ahead queue: decouples HTTP accept from CH writes
	execIndex := NewExecIndex()
	execIndex.StartJanitor()
	writeQueue := NewWriteQueue(ch, envInt("WRITE_QUEUE_SIZE", 10000), envInt("WRITE_WORKERS", 4), execIndex)
	writeQueue.Start()

	rl := NewRateLimiter(cfg.RateLimitRPM, cfg.RateBurst)

	// Initialize cache
	cache := NewCache(CacheConfig{
		RedisURL:    cfg.RedisURL,
		EnableRedis: cfg.EnableRedis,
		DefaultTTL:  cfg.CacheTTL,
	})

	// Initialize alerter
	alerter := NewAlerter(AlertConfig{
		Enabled:          cfg.AlertEnabled,
		SMTPHost:         cfg.SMTPHost,
		SMTPPort:         cfg.SMTPPort,
		SMTPUser:         cfg.SMTPUser,
		SMTPPassword:     cfg.SMTPPassword,
		SMTPFrom:         cfg.SMTPFrom,
		SMTPTo:           cfg.SMTPTo,
		UseTLS:           cfg.SMTPUseTLS,
		FailureThreshold: cfg.AlertFailureThreshold,
		CheckInterval:    cfg.AlertCheckInterval,
		Cooldown:         cfg.AlertCooldown,
	}, ch)
	alerter.Start()

	// Initialize cleanup/retention job (GDPR LÃ¶schkonzept)
	cleaner := NewCleaner(CleanupConfig{
		Enabled:          envBool("CLEANUP_ENABLED", true),
		CheckInterval:    time.Duration(envInt("CLEANUP_INTERVAL_MIN", 60)) * time.Minute,
		StuckAfterHours:  envInt("CLEANUP_STUCK_HOURS", 1),
		RetentionEnabled: envBool("RETENTION_ENABLED", false),
		RetentionDays:    envInt("RETENTION_DAYS", 365),
	}, ch)
	cleaner.Start()

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Check ClickHouse connectivity
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := map[string]interface{}{
			"status": "ok",
			"time":   time.Now().UTC().Format(time.RFC3339),
		}

		// Header values must be set before WriteHeader, otherwise they are dropped.
		w.Header().Set("Content-Type", "application/json")
		if err := ch.Ping(ctx); err != nil {
			status["status"] = "degraded"
			status["clickhouse"] = "disconnected"
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			status["clickhouse"] = "connected"
			w.WriteHeader(http.StatusOK)
		}

		json.NewEncoder(w).Encode(status)
	})

	// Dashboard HTML page - serve on root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveHTMLFile(w, r, "public/templates/dashboard.html")
	})

	// Redirect /dashboard to / for backwards compatibility
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		serveHTMLFile(w, r, "public/templates/dashboard.html")
	})

	// Consolidated dashboard, served alongside the existing three pages so it
	// can be compared against them on live data before anything is replaced.
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		serveHTMLFile(w, r, "public/templates/new/index.html")
	})

	// newDashTTL scales the cache lifetime with the window, on the same
	// reasoning and the same ladder as the older dashboard endpoint: a one-day
	// view moves quickly, a year barely moves at all.
	//
	// It governs the windowed sections only. The live panels are re-run on
	// every hit no matter what this returns.
	newDashTTL := func(days int) time.Duration {
		switch {
		case days <= 1:
			return 30 * time.Second
		case days <= 7:
			return 2 * time.Minute
		case days <= 30:
			return 5 * time.Minute
		case days <= 90:
			return 15 * time.Minute
		default:
			return 30 * time.Minute
		}
	}

	mux.HandleFunc("/api/new", func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 1 {
				days = 1
			}
			if days > 365 {
				days = 365
			}
		}

		repoSource, repoSlug := parseRepoFilters(r)
		platform := parsePlatform(r)

		// Allowlisted: the value is interpolated into SQL, and the set of script
		// types the engine reports is small and known.
		ctype := ""
		switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("ctype"))) {
		case "lxc":
			ctype = "lxc"
		case "vm":
			ctype = "vm"
		case "addon":
			ctype = "addon"
		case "pve":
			ctype = "pve"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		// This was the only dashboard endpoint serving every request from
		// scratch, and the most expensive one: thirty aggregations over as much
		// as a year. The other three have cached for as long as they have
		// existed, on the same stale-while-revalidate pattern used here.
		cacheKey := newDashCacheKey(days, repoSource, repoSlug, platform, ctype)

		// no-store stays on every path below. It refers to the browser, which
		// must keep asking so the live panels stay live; the server-side cache
		// is what stops that question being expensive.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		// data != nil guards RefreshLive below: Get unmarshals into a pointer,
		// so a stored "null" would hand it one to dereference. Falling through
		// to a fresh build is the right answer for that anyway.
		var data *NewDashData
		if cfg.CacheEnabled && cache.Get(ctx, cacheKey, &data) && data != nil {
			// The stored page is the windowed sections. The live panels are
			// re-run over the top, because they answer "what is happening now"
			// and must not come out of a cache keyed on a period they ignore.
			ch.RefreshLive(ctx, data)
			w.Header().Set("X-Cache", "HIT")

			if cache.IsStale(ctx, cacheKey) {
				w.Header().Set("X-Cache", "STALE")
				if cache.TryStartRefresh(cacheKey) {
					go func() {
						defer cache.FinishRefresh(cacheKey)
						refreshCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
						defer cancel()
						fresh, err := ch.FetchNewDashboard(refreshCtx, days, repoSource, repoSlug, platform, ctype)
						if err != nil {
							log.Printf("[CACHE] background refresh failed for %s: %v", cacheKey, err)
							return
						}
						_ = cache.Set(context.Background(), cacheKey, fresh, newDashTTL(days))
						log.Printf("[CACHE] background refresh completed for %s", cacheKey)
					}()
				}
			}

			if err := json.NewEncoder(w).Encode(data); err != nil {
				log.Printf("new dashboard encode: %v", err)
			}
			return
		}

		data, err := ch.FetchNewDashboard(ctx, days, repoSource, repoSlug, platform, ctype)
		if err != nil {
			log.Printf("new dashboard: %v", err)
			http.Error(w, "Failed to load dashboard data", http.StatusInternalServerError)
			return
		}

		if cfg.CacheEnabled {
			_ = cache.Set(ctx, cacheKey, data, newDashTTL(days))
		}

		w.Header().Set("X-Cache", "MISS")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("new dashboard encode: %v", err)
		}
	})

	// Prometheus-style metrics endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		data, err := ch.FetchDashboardData(ctx, 1, "ProxmoxVE", "", "") // Last 24h, production only for metrics
		if err != nil {
			http.Error(w, "failed to fetch metrics", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP telemetry_installs_total Total number of installations\n")
		fmt.Fprintf(w, "# TYPE telemetry_installs_total counter\n")
		fmt.Fprintf(w, "telemetry_installs_total %d\n\n", data.TotalInstalls)
		fmt.Fprintf(w, "# HELP telemetry_installs_success_total Successful installations\n")
		fmt.Fprintf(w, "# TYPE telemetry_installs_success_total counter\n")
		fmt.Fprintf(w, "telemetry_installs_success_total %d\n\n", data.SuccessCount)
		fmt.Fprintf(w, "# HELP telemetry_installs_failed_total Failed installations\n")
		fmt.Fprintf(w, "# TYPE telemetry_installs_failed_total counter\n")
		fmt.Fprintf(w, "telemetry_installs_failed_total %d\n\n", data.FailedCount)
		fmt.Fprintf(w, "# HELP telemetry_installs_aborted_total Aborted installations (SIGINT)\n")
		fmt.Fprintf(w, "# TYPE telemetry_installs_aborted_total counter\n")
		fmt.Fprintf(w, "telemetry_installs_aborted_total %d\n\n", data.AbortedCount)
		fmt.Fprintf(w, "# HELP telemetry_installs_pending Current installing count\n")
		fmt.Fprintf(w, "# TYPE telemetry_installs_pending gauge\n")
		fmt.Fprintf(w, "telemetry_installs_pending %d\n\n", data.InstallingCount)
		fmt.Fprintf(w, "# HELP telemetry_success_rate Success rate percentage\n")
		fmt.Fprintf(w, "# TYPE telemetry_success_rate gauge\n")
		fmt.Fprintf(w, "telemetry_success_rate %.2f\n", data.SuccessRate)
	})

	// Dashboard API endpoint (with caching)
	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		days := 1 // Default: Today
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 0 {
				days = 1
			}
			// Cap to 365 days to prevent unbounded queries that timeout
			if days == 0 || days > 365 {
				days = 365
			}
		}

		// repo_source + optional repo_slug + optional platform filter
		repoSource, repoSlug := parseRepoFilters(r)
		platform := parsePlatform(r)

		// Increase timeout for large datasets (dashboard aggregation takes time)
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		// Try cache first (stale-while-revalidate)
		cacheKey := telemetryCacheKeyP("dashboard", days, repoSource, repoSlug, platform)
		var data *DashboardData
		if cfg.CacheEnabled && cache.Get(ctx, cacheKey, &data) {
			// Serve cached data immediately
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")

			// If stale, trigger background refresh (non-blocking)
			if cache.IsStale(ctx, cacheKey) {
				w.Header().Set("X-Cache", "STALE")
				if cache.TryStartRefresh(cacheKey) {
					go func() {
						defer cache.FinishRefresh(cacheKey)
						refreshCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
						defer cancel()
						freshData, err := ch.FetchDashboardData(refreshCtx, days, repoSource, repoSlug, platform)
						if err != nil {
							log.Printf("[CACHE] background refresh failed for %s: %v", cacheKey, err)
							return
						}
						_ = cache.Set(context.Background(), cacheKey, freshData, cfg.CacheTTL)
						log.Printf("[CACHE] background refresh completed for %s", cacheKey)
					}()
				}
			}

			json.NewEncoder(w).Encode(data)
			return
		}

		data, err := ch.FetchDashboardData(ctx, days, repoSource, repoSlug, platform)
		if err != nil {
			log.Printf("dashboard fetch failed: %v", err)
			http.Error(w, "failed to fetch data", http.StatusInternalServerError)
			return
		}

		// Cache the result with dynamic TTL based on period
		if cfg.CacheEnabled {
			// Short periods change faster â†’ shorter cache TTL
			cacheTTL := cfg.CacheTTL
			switch {
			case days <= 1:
				cacheTTL = 30 * time.Second // Today: 30s cache
			case days <= 7:
				cacheTTL = 2 * time.Minute // 7 Days: 2min cache
			case days <= 30:
				cacheTTL = 5 * time.Minute // 30 Days: 5min cache
			case days <= 90:
				cacheTTL = 15 * time.Minute // 90 Days: 15min cache
			default:
				cacheTTL = 30 * time.Minute // 1 Year+: 30min cache
			}
			_ = cache.Set(ctx, cacheKey, data, cacheTTL)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		json.NewEncoder(w).Encode(data)
	})

	// Paginated records API
	mux.HandleFunc("/api/records", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		limit := 50
		status := r.URL.Query().Get("status")
		app := r.URL.Query().Get("app")
		if s := r.URL.Query().Get("search"); s != "" && app == "" {
			app = s
		}
		osType := r.URL.Query().Get("os")
		typeFilter := r.URL.Query().Get("type")
		sort := r.URL.Query().Get("sort")
		repoSource, repoSlug := parseRepoFilters(r)
		platform := parsePlatform(r)

		// Days filter for Installation Log (default: 1 = today)
		days := 1
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 0 {
				days = 1
			}
			if days == 0 || days > 365 {
				days = 365
			}
		}

		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
			if page < 1 {
				page = 1
			}
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
			if limit < 1 {
				limit = 1
			}
			if limit > 100 {
				limit = 100
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		records, total, err := ch.FetchRecordsPaginated(ctx, page, limit, status, app, osType, typeFilter, sort, repoSource, repoSlug, platform, days)
		if err != nil {
			log.Printf("records fetch failed: %v", err)
			http.Error(w, "failed to fetch records", http.StatusInternalServerError)
			return
		}

		// Auto-reclassify old records that have status=failed but are actually user aborts
		for i := range records {
			if records[i].Status == "failed" && isAbortSignal(records[i].ExitCode, records[i].Error) {
				records[i].Status = "aborted"
			}
		}

		response := map[string]interface{}{
			"records":     records,
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + limit - 1) / limit,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	// Alert history and test endpoints
	mux.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": cfg.AlertEnabled,
			"history": alerter.GetAlertHistory(),
		})
	})

	mux.HandleFunc("/api/alerts/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := alerter.TestAlert(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test alert sent"))
	})

	// Error Analysis page
	mux.HandleFunc("/error-analysis", func(w http.ResponseWriter, r *http.Request) {
		serveHTMLFile(w, r, "public/templates/error-analysis.html")
	})

	// Script Analysis page
	mux.HandleFunc("/script-analysis", func(w http.ResponseWriter, r *http.Request) {
		serveHTMLFile(w, r, "public/templates/script-analysis.html")
	})

	// Script Analysis API
	mux.HandleFunc("/api/scripts", func(w http.ResponseWriter, r *http.Request) {
		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 0 {
				days = 1
			}
			// days=0 is allowed â†’ All Time (served from AllTimeStore)
			if days > 365 && days != 0 {
				days = 365
			}
		}

		// repo_source is optional (no param = all sources)
		repoSource := r.URL.Query().Get("repo")
		if repoSource == "all" {
			repoSource = ""
		}
		platform := parsePlatform(r)

		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		cacheKey := fmt.Sprintf("scripts:%d:%s", days, repoSource)
		if platform != "" {
			cacheKey += ":p=" + platform
		}
		var data *ScriptAnalysisData
		if cfg.CacheEnabled && cache.Get(ctx, cacheKey, &data) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			if cache.IsStale(ctx, cacheKey) {
				w.Header().Set("X-Cache", "STALE")
				if cache.TryStartRefresh(cacheKey) {
					go func() {
						defer cache.FinishRefresh(cacheKey)
						refreshCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
						defer cancel()
						freshData, err := ch.FetchScriptStats(refreshCtx, days, repoSource, platform, nil)
						if err != nil {
							log.Printf("[CACHE] background refresh failed for %s: %v", cacheKey, err)
							return
						}
						_ = cache.Set(context.Background(), cacheKey, freshData, 2*time.Minute)
					}()
				}
			}
			json.NewEncoder(w).Encode(data)
			return
		}

		data, err := ch.FetchScriptStats(ctx, days, repoSource, platform, nil)
		if err != nil {
			log.Printf("script stats fetch failed: %v", err)
			http.Error(w, "failed to fetch script data", http.StatusInternalServerError)
			return
		}

		if cfg.CacheEnabled {
			cacheTTL := 2 * time.Minute
			if days > 7 {
				cacheTTL = 23 * time.Hour
			}
			_ = cache.Set(ctx, cacheKey, data, cacheTTL)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		json.NewEncoder(w).Encode(data)
	})

	// Per-script drill-down: daily series, exit codes, OS/platform, recent installs
	mux.HandleFunc("/api/script-detail", func(w http.ResponseWriter, r *http.Request) {
		app := strings.TrimSpace(r.URL.Query().Get("app"))
		if app == "" {
			http.Error(w, "missing app parameter", http.StatusBadRequest)
			return
		}

		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 1 {
				days = 1
			}
			if days > 365 {
				days = 365
			}
		}
		repoSource, _ := parseRepoFilters(r)
		platform := parsePlatform(r)

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		cacheKey := fmt.Sprintf("scriptdetail:%s:%d:%s:p=%s", app, days, repoSource, platform)
		var data *ScriptDetailData
		if cfg.CacheEnabled && cache.Get(ctx, cacheKey, &data) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			json.NewEncoder(w).Encode(data)
			return
		}

		data, err := ch.FetchScriptDetail(ctx, app, days, repoSource, platform)
		if err != nil {
			log.Printf("script detail fetch failed (app=%s): %v", app, err)
			http.Error(w, "failed to fetch script detail", http.StatusInternalServerError)
			return
		}

		if cfg.CacheEnabled {
			_ = cache.Set(ctx, cacheKey, data, 2*time.Minute)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		json.NewEncoder(w).Encode(data)
	})

	// Error Analysis API - detailed error data
	mux.HandleFunc("/api/errors", func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 1 {
				days = 1
			}
			if days > 365 {
				days = 365
			}
		}

		repoSource, repoSlug := parseRepoFilters(r)
		platform := parsePlatform(r)

		// Scale timeout by data volume
		timeout := 120 * time.Second
		if days >= 90 {
			timeout = 300 * time.Second
		}
		if days >= 365 {
			timeout = 600 * time.Second
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		cacheKey := telemetryCacheKeyP("errors", days, repoSource, repoSlug, platform)
		var data *ErrorAnalysisData
		if cfg.CacheEnabled && cache.Get(ctx, cacheKey, &data) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			if cache.IsStale(ctx, cacheKey) {
				w.Header().Set("X-Cache", "STALE")
				if cache.TryStartRefresh(cacheKey) {
					go func() {
						defer cache.FinishRefresh(cacheKey)
						refreshTimeout := 120 * time.Second
						if days >= 90 {
							refreshTimeout = 300 * time.Second
						}
						if days >= 365 {
							refreshTimeout = 600 * time.Second
						}
						refreshCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
						defer cancel()
						freshData, err := ch.FetchErrorAnalysisData(refreshCtx, days, repoSource, repoSlug, platform)
						if err != nil {
							log.Printf("[CACHE] background refresh failed for %s: %v", cacheKey, err)
							return
						}
						refreshTTL := 2 * time.Minute
						if days > 7 {
							refreshTTL = 23 * time.Hour
						}
						_ = cache.Set(context.Background(), cacheKey, freshData, refreshTTL)
					}()
				}
			}
			json.NewEncoder(w).Encode(data)
			return
		}

		data, err := ch.FetchErrorAnalysisData(ctx, days, repoSource, repoSlug, platform)
		if err != nil {
			log.Printf("error analysis fetch failed: %v", err)
			http.Error(w, "failed to fetch error data", http.StatusInternalServerError)
			return
		}

		if cfg.CacheEnabled {
			cacheTTL := 2 * time.Minute
			if days > 7 {
				cacheTTL = 23 * time.Hour
			}
			_ = cache.Set(ctx, cacheKey, data, cacheTTL)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		json.NewEncoder(w).Encode(data)
	})

	// Repository slug list for fork-specific filtering (owner/repo)
	mux.HandleFunc("/api/repo-slugs", func(w http.ResponseWriter, r *http.Request) {
		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			fmt.Sscanf(d, "%d", &days)
			if days < 1 {
				days = 1
			}
			if days > 365 {
				days = 365
			}
		}
		repoSource, _ := parseRepoFilters(r)

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		slugs, err := ch.FetchRepoSlugs(ctx, days, repoSource)
		if err != nil {
			log.Printf("repo-slugs fetch failed: %v", err)
			http.Error(w, "failed to fetch repo slugs", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"repo_slugs": slugs,
		})
	})

	// API: Get exit code descriptions (static reference data)
	mux.HandleFunc("/api/exit-codes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Build a simple map[int]string from the unified exitCodeInfo
		descs := make(map[int]string, len(exitCodeInfo))
		for code, info := range exitCodeInfo {
			descs[code] = info.Desc
		}
		json.NewEncoder(w).Encode(descs)
	})

	// Serve static files from the /public/static directory.
	// Short client cache + revalidation: templates reference assets with a
	// version query (?v=N), so bumping the version busts stale browser caches
	// after a deploy (previously users kept old CSS/JS indefinitely).
	staticFS, err := fs.Sub(publicFS, "public/static")
	if err != nil {
		log.Fatalf("Failed to create static FS: %v", err)
	}
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			// Versioned asset: cache aggressively, version change busts it
			w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		} else {
			// Unversioned: force revalidation so deploys reach browsers
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		staticHandler.ServeHTTP(w, r)
	}))

	// Cleanup trigger & status API
	mux.HandleFunc("/api/cleanup/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		count, err := cleaner.GetStuckCount(ctx)
		if err != nil {
			http.Error(w, "failed to check stuck count", http.StatusInternalServerError)
			return
		}

		// Reported next to the stuck count because they are the two halves of the
		// same problem: a start with no finish, and a finish with no start.
		orphans, orphanErr := cleaner.ch.GetOrphanTerminalCount(ctx, orphanWindowDays)
		if orphanErr != nil {
			log.Printf("WARN: cleanup/status - orphan count failed: %v", orphanErr)
			orphans = -1
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"stuck_count":        count,
			"stuck_after_hours":  cleaner.cfg.StuckAfterHours,
			"check_interval":     cleaner.cfg.CheckInterval.String(),
			"enabled":            cleaner.cfg.Enabled,
			"orphan_terminals":   orphans,
			"orphan_window_days": orphanWindowDays,
		})
	})

	// Deliberately manual. A terminal event with no start is usually an update
	// run from an older client, but it can also be an install whose first POST
	// never landed -- so nothing deletes these on a timer.
	mux.HandleFunc("/api/cleanup/orphans", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()

		before, err := cleaner.ch.GetOrphanTerminalCount(ctx, orphanWindowDays)
		if err != nil {
			http.Error(w, "failed to count orphan terminals", http.StatusInternalServerError)
			return
		}
		if err := cleaner.ch.DeleteOrphanTerminals(ctx, orphanWindowDays); err != nil {
			http.Error(w, "failed to delete orphan terminals", http.StatusInternalServerError)
			return
		}
		log.Printf("INFO: cleanup - DELETE mutation submitted for %d orphan terminal rows", before)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"submitted":          before,
			"orphan_window_days": orphanWindowDays,
			"note":               "ClickHouse mutations are asynchronous; the count settles shortly after",
		})
	})

	mux.HandleFunc("/api/cleanup/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Require admin password
		if cfg.AdminPassword == "" {
			http.Error(w, "admin password not configured", http.StatusServiceUnavailable)
			return
		}
		password := r.Header.Get("X-Admin-Password")
		if password == "" {
			// Try from JSON body
			var body struct {
				Password string `json:"password"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			password = body.Password
		}
		if password != cfg.AdminPassword {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		updated, err := cleaner.RunNow()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"updated": updated,
			"message": fmt.Sprintf("Cleaned up %d stuck installations", updated),
		})
	})

	// GitHub Issue creation API
	mux.HandleFunc("/api/github/create-issue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}

		// Require admin password
		if cfg.AdminPassword == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "admin password not configured"})
			return
		}

		var body struct {
			Password    string   `json:"password"`
			Title       string   `json:"title"`
			Body        string   `json:"body"`
			Labels      []string `json:"labels"`
			AppName     string   `json:"app_name"`
			ExitCode    int      `json:"exit_code"`
			ErrorText   string   `json:"error_text"`
			FailureRate float64  `json:"failure_rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
			return
		}

		if body.Password != cfg.AdminPassword {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "incorrect password"})
			return
		}

		if cfg.GitHubToken == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "GitHub token not configured on server"})
			return
		}

		// Build issue body if not provided
		if body.Body == "" {
			body.Body = fmt.Sprintf("## Telemetry Error Report\n\n"+
				"**Application:** %s\n"+
				"**Exit Code:** %d\n"+
				"**Error Category:** %s\n"+
				"**Failure Rate:** %.1f%%\n\n"+
				"### Error Details\n```\n%s\n```\n\n"+
				"---\n*This issue was automatically created from the telemetry error analysis dashboard.*",
				body.AppName, body.ExitCode,
				categorizeExitCode(body.ExitCode),
				body.FailureRate, body.ErrorText)
		}

		if body.Title == "" {
			body.Title = fmt.Sprintf("[Telemetry] %s: %s (exit code %d)",
				body.AppName, categorizeExitCode(body.ExitCode), body.ExitCode)
		}

		// Default labels
		if len(body.Labels) == 0 {
			body.Labels = []string{"bug", "telemetry"}
		}

		// Create GitHub issue via API
		issueURL, err := createGitHubIssue(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo, body.Title, body.Body, body.Labels)
		if err != nil {
			log.Printf("ERROR: failed to create GitHub issue: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "failed to create issue: " + err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"issue_url": issueURL,
		})
	})

	mux.HandleFunc("/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// rate key: IP or header (header allows non-identifying keys, but header can be abused too)
		var key string
		switch cfg.RateKeyMode {
		case "header":
			key = strings.TrimSpace(r.Header.Get(cfg.RateKeyHeader))
			if key == "" {
				key = "missing"
			}
		default:
			ip := getClientIP(r, pt)
			if ip == nil {
				key = "unknown"
			} else {
				// GDPR: do NOT store IP anywhere permanent; use it only in-memory for RL key
				key = ip.String()
			}
		}
		if !rl.Allow(key) {
			log.Printf("[RATE] rejected key=%s", key)
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[REJECT] body read error: %v", err)
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		// Lenient JSON decode: ignore unknown fields for forward compatibility.
		// When api.func adds new fields before the server is updated, requests
		// must not be rejected â€” otherwise ALL telemetry is lost until deploy.
		var in TelemetryIn
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&in); err != nil {
			// Attempt rescue: sanitize common bash-client JSON issues and retry
			sanitized := sanitizeRawJSON(raw)
			var in2 TelemetryIn
			dec2 := json.NewDecoder(bytes.NewReader(sanitized))
			if err2 := dec2.Decode(&in2); err2 != nil {
				// Both sanitize attempts failed. Last resort: extract fields manually.
				// This handles cases where the error field has unescaped quotes (mawk gsub issue).
				in3, err3 := rescueBrokenJSON(raw)
				if err3 != nil {
					snippet := string(raw)
					if len(snippet) > 2000 {
						snippet = snippet[:2000] + "..."
					}
					var syntaxErr *json.SyntaxError
					if errors.As(err, &syntaxErr) {
						log.Printf("[REJECT] json decode: %v (offset %d) | body=%s", err, syntaxErr.Offset, snippet)
					} else {
						log.Printf("[REJECT] json decode: %v | body=%s", err, snippet)
					}
					http.Error(w, "invalid json", http.StatusBadRequest)
					return
				}
				in = in3
				log.Printf("[WARN] json rescued: nsapp=%s exec=%s (original error: %v)", in.NSAPP, in.ExecutionID, err)
			} else {
				in = in2
				log.Printf("[WARN] json sanitized: nsapp=%s exec=%s (original error: %v)", in.NSAPP, in.ExecutionID, err)
			}
		}
		if err := validate(&in); err != nil {
			log.Printf("[REJECT] validation: %v | nsapp=%s status=%s", err, in.NSAPP, in.Status)
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}

		// Auto-reclassify: exit_code=0 is NEVER an error â€” always reclassify as success
		if in.Status == "failed" && in.ExitCode == 0 {
			in.Status = "success"
			in.Error = ""
			in.ErrorCategory = ""
			if cfg.EnableReqLogging {
				log.Printf("auto-reclassified exit_code=0 as success: nsapp=%s", in.NSAPP)
			}
		}

		// Auto-reclassify: addon and pve type failures â†’ success (excluded from failure stats)
		if in.Status == "failed" && (in.Type == "addon" || in.Type == "pve") {
			in.Status = "success"
			in.Error = ""
			in.ErrorCategory = ""
			in.ExitCode = 0
			if cfg.EnableReqLogging {
				log.Printf("auto-reclassified %s failure as success: nsapp=%s", in.Type, in.NSAPP)
			}
		}

		// Auto-reclassify: clients still send status="failed" for SIGINT/Ctrl+C and SIGHUP,
		// detect and reclassify as "aborted" server-side.
		if in.Status == "failed" && isAbortSignal(in.ExitCode, in.Error) {
			in.Status = "aborted"
			if in.ErrorCategory == "" || in.ErrorCategory == "unknown" {
				in.ErrorCategory = "user_aborted"
			}
			if cfg.EnableReqLogging {
				log.Printf("auto-reclassified as aborted: nsapp=%s exit_code=%d", in.NSAPP, in.ExitCode)
			}
		}

		// Authoritative error categorization: the server is the single source of
		// truth. When the server can confidently derive a category (from exit code
		// or error text), it overrides whatever the client sent so that client/server
		// mappings can never diverge. Only when the server is unsure do we keep the
		// client's category (it may have had extra local context).
		if in.Status == "failed" {
			if serverCat := deriveErrorCategory(in.ExitCode, in.Error); serverCat != "unknown" && serverCat != "" {
				in.ErrorCategory = serverCat
			} else if in.ErrorCategory == "" {
				in.ErrorCategory = "unknown"
			}
		}

		// Enrich error text with exit code description if error text is empty
		if in.Status == "failed" && in.Error == "" && in.ExitCode != 0 {
			in.Error = fmt.Sprintf("Exit code %d: %s", in.ExitCode, getExitCodeDescription(in.ExitCode))
		}

		// Map input to telemetry schema
		out := TelemetryOut{
			RandomID:        in.RandomID,
			ExecutionID:     in.ExecutionID,
			Type:            in.Type,
			NSAPP:           in.NSAPP,
			Status:          in.Status,
			CTType:          in.CTType,
			DiskSize:        in.DiskSize,
			CoreCount:       in.CoreCount,
			RAMSize:         in.RAMSize,
			OsType:          in.OsType,
			OsVersion:       in.OsVersion,
			PveVer:          in.PveVer,
			Method:          in.Method,
			Error:           in.Error,
			ExitCode:        in.ExitCode,
			GPUVendor:       in.GPUVendor,
			GPUModel:        in.GPUModel,
			GPUPassthrough:  in.GPUPassthrough,
			CPUVendor:       in.CPUVendor,
			CPUModel:        in.CPUModel,
			RAMSpeed:        in.RAMSpeed,
			InstallDuration: in.InstallDuration,
			ErrorCategory:   in.ErrorCategory,
			RepoSource:      in.RepoSource,
			RepoSlug:        in.RepoSlug,
			HasArm:          in.HasArm,
			Platform:        in.Platform,
			PayloadVersion:  in.PayloadVersion,
			Arch:            in.Arch,
			FailedCommand:   in.FailedCommand,
			FailedLine:      in.FailedLine,
			KernelVersion:   in.KernelVersion,
			AppVersion:      in.AppVersion,
		}

		// Enqueue for async PB write (decoupled from HTTP response)
		if !writeQueue.Enqueue(out) {
			log.Printf("[QUEUE] full, dropping nsapp=%s status=%s exec=%s", out.NSAPP, out.Status, out.ExecutionID)
			http.Error(w, "server busy", http.StatusServiceUnavailable)
			return
		}

		if cfg.EnableReqLogging {
			log.Printf("telemetry accepted nsapp=%s status=%s repo=%s qlen=%d", out.NSAPP, out.Status, in.RepoSource, writeQueue.Len())
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 3 * time.Second,
	}

	// Background cache warmup job
	// - On startup: fast warmup (day=1 + script stats) then deferred heavy warmup (90d)
	// - Every 30 min: refresh "today" data only (fast, changes frequently)
	// - Nightly at 02:00 UTC: full warmup + heavy dashboard rebuild
	if cfg.CacheEnabled {
		go func() {
			// Fast warmup: day=1 dashboard/errors + script stats from ClickHouse
			time.Sleep(5 * time.Second)
			warmupCaches(ch, cache, cfg, false)

			// Deferred heavy warmup: 90d dashboard + errors (runs in background)
			go warmupHeavyDashboard(ch, cache, cfg)

			// Periodic "today" refresh every 30 min
			todayTicker := time.NewTicker(30 * time.Minute)
			// Nightly full refresh at 02:00 UTC
			nightlyTimer := time.NewTimer(timeUntilNextUTC(2, 0))

			for {
				select {
				case <-todayTicker.C:
					warmupCaches(ch, cache, cfg, true)
				case <-nightlyTimer.C:
					log.Println("[CACHE] Nightly full warmup triggered")
					warmupCaches(ch, cache, cfg, false)
					go warmupHeavyDashboard(ch, cache, cfg)
					nightlyTimer.Reset(24 * time.Hour)
				}
			}
		}()
		log.Printf("background cache warmup enabled (nightly 02:00 UTC, today refresh every 30m)")
	}

	log.Printf("telemetry-ingest listening on %s", cfg.ListenAddr)

	// Serve in the background so the main goroutine can wait for shutdown signals.
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown: stop accepting requests, then drain the write queue so a
	// deploy/restart doesn't lose telemetry that was accepted but not yet written.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("shutdown signal received — draining (qlen=%d)", writeQueue.Len())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful HTTP shutdown failed: %v", err)
	}

	writeQueue.Stop(20 * time.Second)
	log.Printf("shutdown complete")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Minimal security headers (no cookies anyway)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// categorizeExitCode returns a short human-readable description for an exit code.
// Delegates to getExitCodeDescription (single source of truth).
func categorizeExitCode(code int) string {
	return getExitCodeDescription(code)
}

// createGitHubIssue creates a new issue in the specified GitHub repository
func createGitHubIssue(token, owner, repo, title, body string, labels []string) (string, error) {
	payload := map[string]interface{}{
		"title":  title,
		"body":   body,
		"labels": labels,
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("https://api.github.com/repos/%s/%s/issues", owner, repo),
		bytes.NewReader(b),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(rb))
	}

	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.HTMLURL, nil
}

func env(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}
func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var i int
	_, _ = fmt.Sscanf(v, "%d", &i)
	if i == 0 && v != "0" {
		return def
	}
	return i
}
func envInt64(k string, def int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var i int64
	_, _ = fmt.Sscanf(v, "%d", &i)
	if i == 0 && v != "0" {
		return def
	}
	return i
}
func envBool(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
func envFloat(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var f float64
	_, _ = fmt.Sscanf(v, "%f", &f)
	if f == 0 && v != "0" {
		return def
	}
	return f
}
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// timeUntilNextUTC calculates the duration until the next occurrence of hour:minute UTC
func timeUntilNextUTC(hour, minute int) time.Duration {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	if now.After(next) {
		next = next.Add(24 * time.Hour)
	}
	return time.Until(next)
}

// warmupCaches pre-populates the cache for dashboard, scripts, AND errors endpoints.
// If todayOnly=true, only warms days=1 (fast refresh for current-day data).
// If todayOnly=false, warms all day ranges with long TTLs (nightly/startup).
func warmupCaches(ch *CHClient, cache *Cache, cfg Config, todayOnly bool) {
	label := "full"
	if todayOnly {
		label = "today-only"
	}
	log.Printf("[CACHE] Starting %s cache warmup...", label)
	start := time.Now()

	dayRanges := []int{1}
	// Warm the DEFAULT view: all sources ("") — matches the new UI default.
	repos := []string{""}

	warmed := 0
	failed := 0

	// Warm script stats from ClickHouse (7d, 30d, alltime) on full warmup
	if !todayOnly {
		for _, spec := range []struct {
			days int
		}{
			{7}, {30}, {0},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			data, err := ch.FetchScriptStats(ctx, spec.days, "", "", nil)
			cancel()
			if err != nil {
				log.Printf("[CACHE] scripts:%d warmup failed: %v", spec.days, err)
				failed++
				continue
			}
			cacheKey := fmt.Sprintf("scripts:%d:", spec.days)
			_ = cache.Set(context.Background(), cacheKey, data, 23*time.Hour)
			warmed++
			log.Printf("[CACHE] scripts:%d cache warmed from ClickHouse", spec.days)
		}
	}

	for _, days := range dayRanges {
		cacheTTL := 2 * time.Minute
		if days > 1 {
			cacheTTL = 23 * time.Hour
		}

		// Scale timeout by data volume
		timeout := 180 * time.Second
		if days >= 90 {
			timeout = 300 * time.Second
		}

		for _, repo := range repos {
			// --- Dashboard ---
			{
				cacheKey := fmt.Sprintf("dashboard:%d:%s", days, repo)
				if cache.TryStartRefresh(cacheKey) {
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					data, err := ch.FetchDashboardData(ctx, days, repo, "", "")
					cancel()
					cache.FinishRefresh(cacheKey)
					if err != nil {
						log.Printf("[CACHE] Warmup dashboard failed days=%d repo=%q: %v", days, repo, err)
						failed++
					} else {
						_ = cache.Set(context.Background(), cacheKey, data, cacheTTL)
						warmed++
					}
				}
				time.Sleep(1 * time.Second)
			}

			// --- Scripts (only for today â€” PB SQL collections handle 7d/30d/alltime) ---
			if days == 1 {
				cacheKey := fmt.Sprintf("scripts:%d:%s", days, repo)
				if cache.TryStartRefresh(cacheKey) {
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					data, err := ch.FetchScriptStats(ctx, days, repo, "", nil)
					cancel()
					cache.FinishRefresh(cacheKey)
					if err != nil {
						log.Printf("[CACHE] Warmup scripts failed days=%d repo=%q: %v", days, repo, err)
						failed++
					} else {
						_ = cache.Set(context.Background(), cacheKey, data, cacheTTL)
						warmed++
					}
				}
				time.Sleep(1 * time.Second)
			}

			// --- Errors (skip for today-only refresh â€” errors don't change that fast) ---
			if !todayOnly {
				cacheKey := fmt.Sprintf("errors:%d:%s", days, repo)
				if cache.TryStartRefresh(cacheKey) {
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					data, err := ch.FetchErrorAnalysisData(ctx, days, repo, "", "")
					cancel()
					cache.FinishRefresh(cacheKey)
					if err != nil {
						log.Printf("[CACHE] Warmup errors failed days=%d repo=%q: %v", days, repo, err)
						failed++
					} else {
						_ = cache.Set(context.Background(), cacheKey, data, cacheTTL)
						warmed++
					}
				}
				time.Sleep(1 * time.Second)
			}
		}
	}

	log.Printf("[CACHE] Warmup %s complete: %d warmed, %d failed (took %v)", label, warmed, failed, time.Since(start).Round(time.Second))
}

// warmupHeavyDashboard builds dashboard + error data for expensive day ranges (90d)
// in the background with generous timeouts. Results are cached.
func warmupHeavyDashboard(ch *CHClient, cache *Cache, cfg Config) {
	heavyRanges := []int{90}
	// Warm the DEFAULT view: all sources ("")
	repos := []string{""}

	log.Println("[CACHE] Starting deferred heavy dashboard warmup (90d)...")
	start := time.Now()
	warmed := 0
	failed := 0

	for _, days := range heavyRanges {
		// Generous timeout: ~10s per expected page of 1000 records
		timeout := 900 * time.Second
		cacheTTL := 23 * time.Hour

		for _, repo := range repos {
			// --- Dashboard ---
			cacheKey := fmt.Sprintf("dashboard:%d:%s", days, repo)
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				data, err := ch.FetchDashboardData(ctx, days, repo, "", "")
				if err != nil {
					log.Printf("[CACHE] Heavy warmup dashboard:%d failed: %v", days, err)
					failed++
					return
				}
				_ = cache.Set(context.Background(), cacheKey, data, cacheTTL)
				warmed++
				log.Printf("[CACHE] Heavy warmup dashboard:%d complete", days)
			}()

			time.Sleep(5 * time.Second)

			// --- Errors ---
			cacheKey = fmt.Sprintf("errors:%d:%s", days, repo)
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				data, err := ch.FetchErrorAnalysisData(ctx, days, repo, "", "")
				if err != nil {
					log.Printf("[CACHE] Heavy warmup errors:%d failed: %v", days, err)
					failed++
					return
				}
				_ = cache.Set(context.Background(), cacheKey, data, cacheTTL)
				warmed++
				log.Printf("[CACHE] Heavy warmup errors:%d complete", days)
			}()

			time.Sleep(5 * time.Second)
		}
	}

	log.Printf("[CACHE] Heavy warmup complete: %d warmed, %d failed (took %v)", warmed, failed, time.Since(start).Round(time.Second))
}

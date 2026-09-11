package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// AlertConfig holds SMTP alert configuration
type AlertConfig struct {
	Enabled          bool
	SMTPHost         string
	SMTPPort         int
	SMTPUser         string
	SMTPPassword     string
	SMTPFrom         string
	SMTPTo           []string
	UseTLS           bool
	FailureThreshold float64       // Alert when failure rate exceeds this (e.g., 20.0 = 20%)
	CheckInterval    time.Duration // How often to check
	Cooldown         time.Duration // Minimum time between alerts

	// Weekly Report settings
	WeeklyReportEnabled bool         // Enable weekly summary reports
	WeeklyReportDay     time.Weekday // Day to send report (0=Sunday, 1=Monday, etc.)
	WeeklyReportHour    int          // Hour to send report (0-23)
}

// WeeklyReportData contains aggregated weekly statistics
type WeeklyReportData struct {
	CalendarWeek     int
	Year             int
	StartDate        time.Time
	EndDate          time.Time
	TotalInstalls    int
	SuccessCount     int
	FailedCount      int
	AbortedCount     int
	SuccessRate      float64
	TopApps          []AppStat
	TopFailedApps    []AppStat
	ComparedToPrev   WeekComparison
	OsDistribution   map[string]int
	TypeDistribution map[string]int
}

// AppStat represents statistics for a single app
type AppStat struct {
	Name        string
	Total       int
	Failed      int
	FailureRate float64
}

// WeekComparison shows changes compared to previous week
type WeekComparison struct {
	InstallsChange  int     // Difference in total installs
	InstallsPercent float64 // Percentage change
	FailRateChange  float64 // Change in failure rate (percentage points)
}

// Alerter handles alerting functionality
type Alerter struct {
	cfg              AlertConfig
	lastAlertAt      time.Time
	lastWeeklyReport time.Time
	mu               sync.Mutex
	pb               *CHClient
	lastStats        alertStats
	alertHistory     []AlertEvent
}

type alertStats struct {
	successCount int
	failedCount  int
	checkedAt    time.Time
}

// AlertEvent records an alert that was sent
type AlertEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Type        string    `json:"type"`
	Message     string    `json:"message"`
	FailureRate float64   `json:"failure_rate,omitempty"`
}

// NewAlerter creates a new alerter instance
func NewAlerter(cfg AlertConfig, pb *CHClient) *Alerter {
	return &Alerter{
		cfg:          cfg,
		pb:           pb,
		alertHistory: make([]AlertEvent, 0),
	}
}

// Start begins the alert monitoring loop
func (a *Alerter) Start() {
	if !a.cfg.Enabled {
		log.Println("INFO: alerting disabled")
		return
	}

	if a.cfg.SMTPHost == "" || len(a.cfg.SMTPTo) == 0 {
		log.Println("WARN: alerting enabled but SMTP not configured")
		return
	}

	go a.monitorLoop()
	log.Printf("INFO: alert monitoring started (threshold: %.1f%%, interval: %v)", a.cfg.FailureThreshold, a.cfg.CheckInterval)

	// Start weekly report scheduler if enabled
	if a.cfg.WeeklyReportEnabled {
		go a.weeklyReportLoop()
		log.Printf("INFO: weekly report scheduler started (day: %s, hour: %02d:00)", a.cfg.WeeklyReportDay, a.cfg.WeeklyReportHour)
	}
}

func (a *Alerter) monitorLoop() {
	ticker := time.NewTicker(a.cfg.CheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		a.checkAndAlert()
	}
}

func (a *Alerter) checkAndAlert() {
	ctx, cancel := newTimeoutContext(10 * time.Second)
	defer cancel()

	// Fetch last hour's data
	data, err := a.pb.FetchDashboardData(ctx, 1, "ProxmoxVE", "", "")
	if err != nil {
		log.Printf("WARN: alert check failed: %v", err)
		return
	}

	// Calculate current failure rate
	total := data.SuccessCount + data.FailedCount
	if total < 10 {
		// Not enough data to determine rate
		return
	}

	failureRate := float64(data.FailedCount) / float64(total) * 100

	// Check if we should alert
	if failureRate >= a.cfg.FailureThreshold {
		a.maybeSendAlert(failureRate, data.FailedCount, total)
	}
}

func (a *Alerter) maybeSendAlert(rate float64, failed, total int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check cooldown
	if time.Since(a.lastAlertAt) < a.cfg.Cooldown {
		return
	}

	// Send alert
	subject := fmt.Sprintf("[ProxmoxVED Alert] High Failure Rate: %.1f%%", rate)
	body := fmt.Sprintf(`ProxmoxVE Helper Scripts - Telemetry Alert

⚠️ High installation failure rate detected!

Current Statistics (last 24h):
- Failure Rate: %.1f%%
- Failed Installations: %d
- Total Installations: %d
- Threshold: %.1f%%

Time: %s

Please check the dashboard for more details.

---
This is an automated alert from the telemetry service.
`, rate, failed, total, a.cfg.FailureThreshold, time.Now().Format(time.RFC1123))

	if err := a.sendEmail(subject, body); err != nil {
		log.Printf("ERROR: failed to send alert email: %v", err)
		return
	}

	a.lastAlertAt = time.Now()
	a.alertHistory = append(a.alertHistory, AlertEvent{
		Timestamp:   time.Now(),
		Type:        "high_failure_rate",
		Message:     fmt.Sprintf("Failure rate %.1f%% exceeded threshold %.1f%%", rate, a.cfg.FailureThreshold),
		FailureRate: rate,
	})

	// Keep only last 100 alerts
	if len(a.alertHistory) > 100 {
		a.alertHistory = a.alertHistory[len(a.alertHistory)-100:]
	}

	log.Printf("ALERT: sent high failure rate alert (%.1f%%)", rate)
}

func (a *Alerter) sendEmail(subject, body string) error {
	return a.sendEmailWithType(subject, body, "text/plain")
}

func (a *Alerter) sendHTMLEmail(subject, body string) error {
	return a.sendEmailWithType(subject, body, "text/html")
}

func (a *Alerter) sendEmailWithType(subject, body, contentType string) error {
	// Build message
	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", a.cfg.SMTPFrom))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(a.cfg.SMTPTo, ", ")))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("Content-Type: %s; charset=UTF-8\r\n", contentType))
	msg.WriteString("\r\n")
	msg.WriteString(body)

	addr := fmt.Sprintf("%s:%d", a.cfg.SMTPHost, a.cfg.SMTPPort)

	var auth smtp.Auth
	if a.cfg.SMTPUser != "" && a.cfg.SMTPPassword != "" {
		auth = smtp.PlainAuth("", a.cfg.SMTPUser, a.cfg.SMTPPassword, a.cfg.SMTPHost)
	}

	if a.cfg.UseTLS {
		// TLS connection
		tlsConfig := &tls.Config{
			ServerName: a.cfg.SMTPHost,
		}

		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS dial failed: %w", err)
		}
		defer conn.Close()

		client, err := smtp.NewClient(conn, a.cfg.SMTPHost)
		if err != nil {
			return fmt.Errorf("SMTP client failed: %w", err)
		}
		defer client.Close()

		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("SMTP auth failed: %w", err)
			}
		}

		if err := client.Mail(a.cfg.SMTPFrom); err != nil {
			return fmt.Errorf("SMTP MAIL failed: %w", err)
		}

		for _, to := range a.cfg.SMTPTo {
			if err := client.Rcpt(to); err != nil {
				return fmt.Errorf("SMTP RCPT failed: %w", err)
			}
		}

		w, err := client.Data()
		if err != nil {
			return fmt.Errorf("SMTP DATA failed: %w", err)
		}

		_, err = w.Write(msg.Bytes())
		if err != nil {
			return fmt.Errorf("SMTP write failed: %w", err)
		}

		return w.Close()
	}

	// Non-TLS (STARTTLS)
	return smtp.SendMail(addr, auth, a.cfg.SMTPFrom, a.cfg.SMTPTo, msg.Bytes())
}

// GetAlertHistory returns recent alert events
func (a *Alerter) GetAlertHistory() []AlertEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]AlertEvent, len(a.alertHistory))
	copy(result, a.alertHistory)
	return result
}

// TestAlert sends a test alert email
func (a *Alerter) TestAlert() error {
	if !a.cfg.Enabled || a.cfg.SMTPHost == "" {
		return fmt.Errorf("alerting not configured")
	}

	subject := "[ProxmoxVED] Test Alert"
	body := fmt.Sprintf(`This is a test alert from ProxmoxVE Helper Scripts telemetry service.

If you received this email, your alert configuration is working correctly.

Time: %s
SMTP Host: %s
Recipients: %s

---
This is an automated test message.
`, time.Now().Format(time.RFC1123), a.cfg.SMTPHost, strings.Join(a.cfg.SMTPTo, ", "))

	return a.sendEmail(subject, body)
}

// Helper for timeout context
func newTimeoutContext(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// weeklyReportLoop checks periodically if it's time to send the weekly report
func (a *Alerter) weeklyReportLoop() {
	// Check every hour
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		a.checkAndSendWeeklyReport()
	}
}

// checkAndSendWeeklyReport sends the weekly report if it's the right time
func (a *Alerter) checkAndSendWeeklyReport() {
	now := time.Now()

	// Check if it's the right day and hour
	if now.Weekday() != a.cfg.WeeklyReportDay || now.Hour() != a.cfg.WeeklyReportHour {
		return
	}

	a.mu.Lock()
	// Check if we already sent a report this week
	_, lastWeek := a.lastWeeklyReport.ISOWeek()
	_, currentWeek := now.ISOWeek()
	if a.lastWeeklyReport.Year() == now.Year() && lastWeek == currentWeek {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// Send the weekly report
	if err := a.SendWeeklyReport(); err != nil {
		log.Printf("ERROR: failed to send weekly report: %v", err)
	}
}

// SendWeeklyReport generates and sends the weekly summary email
func (a *Alerter) SendWeeklyReport() error {
	if !a.cfg.Enabled || a.cfg.SMTPHost == "" {
		return fmt.Errorf("alerting not configured")
	}

	ctx, cancel := newTimeoutContext(30 * time.Second)
	defer cancel()

	// Get data for the past week
	reportData, err := a.fetchWeeklyReportData(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch weekly data: %w", err)
	}

	// Generate email content
	subject := fmt.Sprintf("[ProxmoxVED] Weekly Report - Week %d, %d", reportData.CalendarWeek, reportData.Year)
	body := a.generateWeeklyReportHTML(reportData)

	if err := a.sendHTMLEmail(subject, body); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	a.mu.Lock()
	a.lastWeeklyReport = time.Now()
	a.alertHistory = append(a.alertHistory, AlertEvent{
		Timestamp: time.Now(),
		Type:      "weekly_report",
		Message:   fmt.Sprintf("Weekly report KW %d/%d sent", reportData.CalendarWeek, reportData.Year),
	})
	a.mu.Unlock()

	log.Printf("INFO: weekly report KW %d/%d sent successfully", reportData.CalendarWeek, reportData.Year)
	return nil
}

// fetchWeeklyReportData collects data for the weekly report
func (a *Alerter) fetchWeeklyReportData(ctx context.Context) (*WeeklyReportData, error) {
	// Calculate the previous week's date range (Mon-Sun)
	now := time.Now()

	// Find last Monday
	daysToLastMonday := int(now.Weekday() - time.Monday)
	if daysToLastMonday < 0 {
		daysToLastMonday += 7
	}
	// Go back to the Monday of LAST week
	lastMonday := now.AddDate(0, 0, -daysToLastMonday-7)
	lastMonday = time.Date(lastMonday.Year(), lastMonday.Month(), lastMonday.Day(), 0, 0, 0, 0, lastMonday.Location())
	lastSunday := lastMonday.AddDate(0, 0, 6)
	lastSunday = time.Date(lastSunday.Year(), lastSunday.Month(), lastSunday.Day(), 23, 59, 59, 0, lastSunday.Location())

	// Get calendar week
	year, week := lastMonday.ISOWeek()

	// Fetch current week's data (7 days)
	currentData, err := a.pb.FetchDashboardData(ctx, 7, "ProxmoxVE", "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch current week data: %w", err)
	}

	// Fetch previous week's data for comparison (14 days, we'll compare)
	prevData, err := a.pb.FetchDashboardData(ctx, 14, "ProxmoxVE", "", "")
	if err != nil {
		// Non-fatal, just log
		log.Printf("WARN: could not fetch previous week data: %v", err)
		prevData = nil
	}

	// Build report data
	report := &WeeklyReportData{
		CalendarWeek:     week,
		Year:             year,
		StartDate:        lastMonday,
		EndDate:          lastSunday,
		TotalInstalls:    currentData.TotalInstalls,
		SuccessCount:     currentData.SuccessCount,
		FailedCount:      currentData.FailedCount,
		AbortedCount:     currentData.AbortedCount,
		OsDistribution:   make(map[string]int),
		TypeDistribution: make(map[string]int),
	}

	// Calculate success rate (only success vs failed, excluding aborted)
	completed := report.SuccessCount + report.FailedCount
	if completed > 0 {
		report.SuccessRate = float64(report.SuccessCount) / float64(completed) * 100
	}

	// Top 5 installed apps
	for i, app := range currentData.TopApps {
		if i >= 5 {
			break
		}
		report.TopApps = append(report.TopApps, AppStat{
			Name:  app.App,
			Total: app.Count,
		})
	}

	// Top 5 failed apps
	for i, app := range currentData.FailedApps {
		if i >= 5 {
			break
		}
		report.TopFailedApps = append(report.TopFailedApps, AppStat{
			Name:        app.App,
			Total:       app.TotalCount,
			Failed:      app.FailedCount,
			FailureRate: app.FailureRate,
		})
	}

	// OS distribution
	for _, os := range currentData.OsDistribution {
		report.OsDistribution[os.Os] = os.Count
	}

	// Type distribution (LXC vs VM)
	for _, t := range currentData.TypeStats {
		report.TypeDistribution[t.Type] = t.Count
	}

	// Calculate comparison to previous week
	if prevData != nil {
		// Previous week stats (subtract current from 14-day total)
		prevInstalls := prevData.TotalInstalls - currentData.TotalInstalls
		prevFailed := prevData.FailedCount - currentData.FailedCount
		prevSuccess := prevData.SuccessCount - currentData.SuccessCount

		if prevInstalls > 0 {
			prevFailRate := float64(prevFailed) / float64(prevInstalls) * 100
			currentFailRate := 100 - report.SuccessRate

			report.ComparedToPrev.InstallsChange = report.TotalInstalls - prevInstalls
			if prevInstalls > 0 {
				report.ComparedToPrev.InstallsPercent = float64(report.TotalInstalls-prevInstalls) / float64(prevInstalls) * 100
			}
			report.ComparedToPrev.FailRateChange = currentFailRate - prevFailRate
			_ = prevSuccess // suppress unused warning
		}
	}

	return report, nil
}

// generateWeeklyReportHTML creates the HTML email body for the weekly report
func (a *Alerter) generateWeeklyReportHTML(data *WeeklyReportData) string {
	var b strings.Builder

	// HTML Email Template
	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin:0;padding:0;background-color:#f6f9fc;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
<table width="100%" cellpadding="0" cellspacing="0" style="background-color:#f6f9fc;padding:40px 20px;">
<tr><td align="center">
<table width="600" cellpadding="0" cellspacing="0" style="background-color:#ffffff;border-radius:12px;box-shadow:0 4px 6px rgba(0,0,0,0.07);">

<!-- Header -->
<tr>
<td style="background:linear-gradient(135deg,#667eea 0%,#764ba2 100%);padding:32px 40px;border-radius:12px 12px 0 0;">
<h1 style="margin:0;color:#ffffff;font-size:24px;font-weight:600;">📊 Weekly Telemetry Report</h1>
<p style="margin:8px 0 0;color:rgba(255,255,255,0.85);font-size:14px;">ProxmoxVE Helper Scripts</p>
</td>
</tr>

<!-- Week Info -->
<tr>
<td style="padding:24px 40px 0;">
<table width="100%" style="background:#f8fafc;border-radius:8px;padding:16px;">
<tr>
<td style="padding:12px 16px;">
<span style="color:#64748b;font-size:12px;text-transform:uppercase;letter-spacing:0.5px;">Calendar Week</span><br>
<span style="color:#1e293b;font-size:20px;font-weight:600;">Week `)
	b.WriteString(fmt.Sprintf("%d, %d", data.CalendarWeek, data.Year))
	b.WriteString(`</span>
</td>
<td style="padding:12px 16px;text-align:right;">
<span style="color:#64748b;font-size:12px;text-transform:uppercase;letter-spacing:0.5px;">Period</span><br>
<span style="color:#1e293b;font-size:14px;">`)
	b.WriteString(fmt.Sprintf("%s – %s", data.StartDate.Format("Jan 02"), data.EndDate.Format("Jan 02, 2006")))
	b.WriteString(`</span>
</td>
</tr>
</table>
</td>
</tr>

<!-- Stats Grid -->
<tr>
<td style="padding:24px 40px;">
<table width="100%" cellpadding="0" cellspacing="0">
<tr>
<td width="20%" style="padding:8px;">
<div style="background:#f0fdf4;border-radius:8px;padding:16px;text-align:center;">
<div style="color:#16a34a;font-size:28px;font-weight:700;">`)
	b.WriteString(fmt.Sprintf("%d", data.TotalInstalls))
	b.WriteString(`</div>
<div style="color:#166534;font-size:11px;text-transform:uppercase;letter-spacing:0.5px;margin-top:4px;">Total</div>
</div>
</td>
<td width="20%" style="padding:8px;">
<div style="background:#f0fdf4;border-radius:8px;padding:16px;text-align:center;">
<div style="color:#16a34a;font-size:28px;font-weight:700;">`)
	b.WriteString(fmt.Sprintf("%d", data.SuccessCount))
	b.WriteString(`</div>
<div style="color:#166534;font-size:11px;text-transform:uppercase;letter-spacing:0.5px;margin-top:4px;">Successful</div>
</div>
</td>
<td width="20%" style="padding:8px;">
<div style="background:#fef2f2;border-radius:8px;padding:16px;text-align:center;">
<div style="color:#dc2626;font-size:28px;font-weight:700;">`)
	b.WriteString(fmt.Sprintf("%d", data.FailedCount))
	b.WriteString(`</div>
<div style="color:#991b1b;font-size:11px;text-transform:uppercase;letter-spacing:0.5px;margin-top:4px;">Failed</div>
</div>
</td>
<td width="20%" style="padding:8px;">
<div style="background:#f3f0ff;border-radius:8px;padding:16px;text-align:center;">
<div style="color:#a855f7;font-size:28px;font-weight:700;">`)
	b.WriteString(fmt.Sprintf("%d", data.AbortedCount))
	b.WriteString(`</div>
<div style="color:#7c3aed;font-size:11px;text-transform:uppercase;letter-spacing:0.5px;margin-top:4px;">Aborted</div>
</div>
</td>
<td width="20%" style="padding:8px;">
<div style="background:#eff6ff;border-radius:8px;padding:16px;text-align:center;">
<div style="color:#2563eb;font-size:28px;font-weight:700;">`)
	b.WriteString(fmt.Sprintf("%.1f%%", data.SuccessRate))
	b.WriteString(`</div>
<div style="color:#1e40af;font-size:11px;text-transform:uppercase;letter-spacing:0.5px;margin-top:4px;">Success Rate</div>
</div>
</td>
</tr>
</table>
</td>
</tr>
`)

	// Week comparison
	if data.ComparedToPrev.InstallsChange != 0 || data.ComparedToPrev.FailRateChange != 0 {
		installIcon := "📈"
		installColor := "#16a34a"
		if data.ComparedToPrev.InstallsChange < 0 {
			installIcon = "📉"
			installColor = "#dc2626"
		}
		failIcon := "✅"
		failColor := "#16a34a"
		if data.ComparedToPrev.FailRateChange > 0 {
			failIcon = "⚠️"
			failColor = "#dc2626"
		}

		b.WriteString(`<tr>
<td style="padding:0 40px 24px;">
<table width="100%" style="background:#fafafa;border-radius:8px;">
<tr>
<td style="padding:16px;border-right:1px solid #e5e7eb;">
<span style="font-size:12px;color:#64748b;">vs. Previous Week</span><br>
<span style="font-size:16px;color:`)
		b.WriteString(installColor)
		b.WriteString(`;">`)
		b.WriteString(installIcon)
		b.WriteString(fmt.Sprintf(" %+d installations (%.1f%%)", data.ComparedToPrev.InstallsChange, data.ComparedToPrev.InstallsPercent))
		b.WriteString(`</span>
</td>
<td style="padding:16px;">
<span style="font-size:12px;color:#64748b;">Failure Rate Change</span><br>
<span style="font-size:16px;color:`)
		b.WriteString(failColor)
		b.WriteString(`;">`)
		b.WriteString(failIcon)
		b.WriteString(fmt.Sprintf(" %+.1f percentage points", data.ComparedToPrev.FailRateChange))
		b.WriteString(`</span>
</td>
</tr>
</table>
</td>
</tr>
`)
	}

	// Top 5 Installed Scripts
	b.WriteString(`<tr>
<td style="padding:0 40px 24px;">
<h2 style="margin:0 0 16px;font-size:16px;color:#1e293b;border-bottom:2px solid #e2e8f0;padding-bottom:8px;">🏆 Top 5 Installed Scripts</h2>
<table width="100%" cellpadding="0" cellspacing="0" style="font-size:14px;">
`)
	if len(data.TopApps) > 0 {
		for i, app := range data.TopApps {
			bgColor := "#ffffff"
			if i%2 == 0 {
				bgColor = "#f8fafc"
			}
			b.WriteString(fmt.Sprintf(`<tr style="background:%s;">
<td style="padding:12px 16px;border-radius:4px 0 0 4px;">
<span style="background:#e0e7ff;color:#4338ca;padding:2px 8px;border-radius:4px;font-size:12px;font-weight:600;">%d</span>
<span style="margin-left:12px;font-weight:500;color:#1e293b;">%s</span>
</td>
<td style="padding:12px 16px;text-align:right;border-radius:0 4px 4px 0;color:#64748b;">%d installs</td>
</tr>`, bgColor, i+1, app.Name, app.Total))
		}
	} else {
		b.WriteString(`<tr><td style="padding:12px 16px;color:#64748b;">No data available</td></tr>`)
	}
	b.WriteString(`</table>
</td>
</tr>
`)

	// Top 5 Failed Scripts
	b.WriteString(`<tr>
<td style="padding:0 40px 24px;">
<h2 style="margin:0 0 16px;font-size:16px;color:#1e293b;border-bottom:2px solid #e2e8f0;padding-bottom:8px;">⚠️ Top 5 Scripts with Highest Failure Rates</h2>
<table width="100%" cellpadding="0" cellspacing="0" style="font-size:14px;">
`)
	if len(data.TopFailedApps) > 0 {
		for i, app := range data.TopFailedApps {
			bgColor := "#ffffff"
			if i%2 == 0 {
				bgColor = "#fef2f2"
			}
			rateColor := "#dc2626"
			if app.FailureRate < 20 {
				rateColor = "#ea580c"
			}
			if app.FailureRate < 10 {
				rateColor = "#ca8a04"
			}
			b.WriteString(fmt.Sprintf(`<tr style="background:%s;">
<td style="padding:12px 16px;border-radius:4px 0 0 4px;">
<span style="font-weight:500;color:#1e293b;">%s</span>
</td>
<td style="padding:12px 16px;text-align:center;color:#64748b;">%d / %d failed</td>
<td style="padding:12px 16px;text-align:right;border-radius:0 4px 4px 0;">
<span style="background:%s;color:#ffffff;padding:4px 10px;border-radius:12px;font-size:12px;font-weight:600;">%.1f%%</span>
</td>
</tr>`, bgColor, app.Name, app.Failed, app.Total, rateColor, app.FailureRate))
		}
	} else {
		b.WriteString(`<tr><td style="padding:12px 16px;color:#16a34a;">🎉 No failures this week!</td></tr>`)
	}
	b.WriteString(`</table>
</td>
</tr>
`)

	// Type Distribution
	if len(data.TypeDistribution) > 0 {
		b.WriteString(`<tr>
<td style="padding:0 40px 24px;">
<h2 style="margin:0 0 16px;font-size:16px;color:#1e293b;border-bottom:2px solid #e2e8f0;padding-bottom:8px;">📦 Distribution by Type</h2>
<table width="100%" cellpadding="0" cellspacing="0">
<tr>
`)
		for t, count := range data.TypeDistribution {
			percent := float64(count) / float64(data.TotalInstalls) * 100
			b.WriteString(fmt.Sprintf(`<td style="padding:8px;">
<div style="background:#f1f5f9;border-radius:8px;padding:16px;text-align:center;">
<div style="font-size:24px;font-weight:700;color:#475569;">%d</div>
<div style="font-size:12px;color:#64748b;margin-top:4px;">%s (%.1f%%)</div>
</div>
</td>`, count, strings.ToUpper(t), percent))
		}
		b.WriteString(`</tr>
</table>
</td>
</tr>
`)
	}

	// OS Distribution
	if len(data.OsDistribution) > 0 {
		b.WriteString(`<tr>
<td style="padding:0 40px 24px;">
<h2 style="margin:0 0 16px;font-size:16px;color:#1e293b;border-bottom:2px solid #e2e8f0;padding-bottom:8px;">🐧 Top Operating Systems</h2>
<table width="100%" cellpadding="0" cellspacing="0" style="font-size:14px;">
`)
		// Sort OS by count
		type osEntry struct {
			name  string
			count int
		}
		var osList []osEntry
		for name, count := range data.OsDistribution {
			osList = append(osList, osEntry{name, count})
		}
		for i := 0; i < len(osList); i++ {
			for j := i + 1; j < len(osList); j++ {
				if osList[j].count > osList[i].count {
					osList[i], osList[j] = osList[j], osList[i]
				}
			}
		}
		for i, os := range osList {
			if i >= 5 {
				break
			}
			percent := float64(os.count) / float64(data.TotalInstalls) * 100
			barWidth := int(percent * 2) // Scale for visual
			if barWidth > 100 {
				barWidth = 100
			}
			b.WriteString(fmt.Sprintf(`<tr>
<td style="padding:8px 16px;width:100px;">%s</td>
<td style="padding:8px 16px;">
<div style="background:#e2e8f0;border-radius:4px;height:20px;width:100%%;">
<div style="background:linear-gradient(90deg,#667eea,#764ba2);border-radius:4px;height:20px;width:%d%%;"></div>
</div>
</td>
<td style="padding:8px 16px;text-align:right;width:80px;color:#64748b;">%d (%.1f%%)</td>
</tr>`, os.name, barWidth, os.count, percent))
		}
		b.WriteString(`</table>
</td>
</tr>
`)
	}

	// Footer
	b.WriteString(`<tr>
<td style="padding:24px 40px;background:#f8fafc;border-radius:0 0 12px 12px;border-top:1px solid #e2e8f0;">
<p style="margin:0;font-size:12px;color:#64748b;text-align:center;">
Generated `)
	b.WriteString(time.Now().Format("Jan 02, 2006 at 15:04 MST"))
	b.WriteString(`<br>
<a href="https://github.com/community-scripts/ProxmoxVE" style="color:#667eea;text-decoration:none;">ProxmoxVE Helper Scripts</a> — 
This is an automated report from the telemetry service.
</p>
</td>
</tr>

</table>
</td></tr>
</table>
</body>
</html>`)

	return b.String()
}

// generateWeeklyReportEmail creates the plain text email body (kept for compatibility)
func (a *Alerter) generateWeeklyReportEmail(data *WeeklyReportData) string {
	var b strings.Builder

	b.WriteString("ProxmoxVE Helper Scripts - Weekly Telemetry Report\n")
	b.WriteString("==================================================\n\n")

	b.WriteString(fmt.Sprintf("Calendar Week: %d, %d\n", data.CalendarWeek, data.Year))
	b.WriteString(fmt.Sprintf("Period: %s - %s\n\n",
		data.StartDate.Format("Jan 02, 2006"),
		data.EndDate.Format("Jan 02, 2006")))

	b.WriteString("OVERVIEW\n")
	b.WriteString("--------\n")
	b.WriteString(fmt.Sprintf("Total Installations:  %d\n", data.TotalInstalls))
	b.WriteString(fmt.Sprintf("Successful:           %d\n", data.SuccessCount))
	b.WriteString(fmt.Sprintf("Failed:               %d\n", data.FailedCount))
	b.WriteString(fmt.Sprintf("Aborted (SIGINT):     %d\n", data.AbortedCount))
	b.WriteString(fmt.Sprintf("Success Rate:         %.1f%%\n\n", data.SuccessRate))

	if data.ComparedToPrev.InstallsChange != 0 || data.ComparedToPrev.FailRateChange != 0 {
		b.WriteString("vs. Previous Week:\n")
		b.WriteString(fmt.Sprintf("  Installations: %+d (%.1f%%)\n", data.ComparedToPrev.InstallsChange, data.ComparedToPrev.InstallsPercent))
		b.WriteString(fmt.Sprintf("  Failure Rate:  %+.1f pp\n\n", data.ComparedToPrev.FailRateChange))
	}

	b.WriteString("TOP 5 INSTALLED SCRIPTS\n")
	b.WriteString("-----------------------\n")
	for i, app := range data.TopApps {
		if i >= 5 {
			break
		}
		b.WriteString(fmt.Sprintf("%d. %-25s %5d installs\n", i+1, app.Name, app.Total))
	}
	b.WriteString("\n")

	b.WriteString("TOP 5 FAILED SCRIPTS\n")
	b.WriteString("--------------------\n")
	if len(data.TopFailedApps) > 0 {
		for i, app := range data.TopFailedApps {
			if i >= 5 {
				break
			}
			b.WriteString(fmt.Sprintf("%d. %-20s %3d/%3d failed (%.1f%%)\n",
				i+1, app.Name, app.Failed, app.Total, app.FailureRate))
		}
	} else {
		b.WriteString("No failures this week!\n")
	}
	b.WriteString("\n")

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("Generated: %s\n", time.Now().Format("Jan 02, 2006 15:04 MST")))
	b.WriteString("This is an automated report from the telemetry service.\n")

	return b.String()
}

// TestWeeklyReport sends a test weekly report email
func (a *Alerter) TestWeeklyReport() error {
	return a.SendWeeklyReport()
}

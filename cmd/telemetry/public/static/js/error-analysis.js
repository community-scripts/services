let charts = {};
let currentData = null;
let currentTheme = localStorage.getItem('theme') || 'dark';
if (currentTheme === 'light') document.documentElement.setAttribute('data-theme', 'light');

const catColors = {
  'apt': '#ef4444', 'network': '#3b82f6', 'permission': '#f97316',
  'command_not_found': '#a855f7', 'user_aborted': '#64748b', 'timeout': '#eab308',
  'storage': '#ec4899', 'resource': '#f97316', 'dependency': '#22d3ee',
  'signal': '#eab308', 'config': '#84cc16', 'unknown': '#64748b',
  'uncategorized': '#94a3b8', 'service': '#06b6d4', 'database': '#8b5cf6',
  'proxmox': '#f59e0b', 'shell': '#fb923c', 'build': '#d946ef',
  'preflight': '#14b8a6'
};

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function escapeAttr(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;').replace(/\n/g, '&#10;').replace(/\r/g, '&#13;');
}

function toggleError(id) {
  var s = document.getElementById(id + '-short');
  var f = document.getElementById(id + '-full');
  if (f && s) {
    if (f.style.display === 'none') { f.style.display = 'block'; s.style.display = 'none'; }
    else { f.style.display = 'none'; s.style.display = 'block'; }
  }
}

function formatTimestamp(ts) {
  if (!ts) return '-';
  return new Date(ts).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit', hour12: true });
}

function getActiveFilters() {
  const days = document.querySelector('.filter-btn.active')?.dataset.days || '1';
  // Default: ALL sources
  const repo = document.querySelector('.source-btn.active')?.dataset.repo || 'all';
  const platform = document.querySelector('.platform-btn.active')?.dataset.platform || '';
  const slug = document.getElementById('filterSlug')?.value || '';
  return { days, repo, platform, slug };
}

function platformBadge(p) {
  if (p === 'incus') return '<span class="platform-badge incus" title="Incus host">INCUS</span>';
  if (p === 'pve') return '<span class="platform-badge pve" title="Proxmox VE host">PVE</span>';
  return '<span class="platform-badge unknown">—</span>';
}

function formatDuration(seconds) {
  if (!seconds) return '-';
  if (seconds < 60) return seconds + 's';
  const mins = Math.floor(seconds / 60);
  const secs = seconds % 60;
  if (mins < 60) return mins + 'm ' + secs + 's';
  return Math.floor(mins / 60) + 'h ' + (mins % 60) + 'm';
}

async function loadRepoSlugOptions() {
  const select = document.getElementById('filterSlug');
  if (!select) return;
  const { days, repo, slug } = getActiveFilters();
  try {
    const resp = await fetch('/api/repo-slugs?days=' + days + '&repo=' + encodeURIComponent(repo));
    if (!resp.ok) return;
    const data = await resp.json();
    const slugs = data.repo_slugs || [];
    const prev = slug;
    select.innerHTML = '<option value="">All repositories</option>' +
      slugs.map(s => '<option value="' + escapeAttr(s.slug) + '">' + escapeHtml(s.slug) + ' (' + s.count.toLocaleString() + ')</option>').join('');
    if (prev && slugs.some(s => s.slug === prev)) {
      select.value = prev;
    }
  } catch (e) {
    console.log('Could not load repo slugs');
  }
}

function onSlugFilterChange() {
  refreshData();
}

async function fetchData() {
  const { days, repo, platform, slug } = getActiveFilters();
  try {
    let url = '/api/errors?days=' + days + '&repo=' + repo;
    if (platform) url += '&platform=' + encodeURIComponent(platform);
    if (slug) url += '&slug=' + encodeURIComponent(slug);
    const resp = await fetch(url);
    if (!resp.ok) throw new Error('Fetch failed');
    return await resp.json();
  } catch (e) {
    console.error(e);
    return null;
  }
}

function updateStats(data) {
  document.getElementById('totalErrors').textContent = (data.total_errors || 0).toLocaleString();
  document.getElementById('failRate').textContent = (data.overall_fail_rate || 0).toFixed(1) + '%';
  document.getElementById('stuckCount2').textContent = (data.stuck_installing || 0).toLocaleString();
  document.getElementById('totalInstalls').textContent = (data.total_installs || 0).toLocaleString();

  // Stuck banner
  if (data.stuck_installing > 0) {
    document.getElementById('stuckBanner').style.display = 'flex';
    document.getElementById('stuckCount').textContent = data.stuck_installing;
  } else {
    document.getElementById('stuckBanner').style.display = 'none';
  }
}

function updateExitCodeTable(exitCodes) {
  const tbody = document.getElementById('exitCodeTable');
  if (!exitCodes || exitCodes.length === 0) {
    tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-muted);padding:24px;">No exit code data</td></tr>';
    return;
  }
  const maxCount = Math.max(...exitCodes.map(e => e.count));
  tbody.innerHTML = exitCodes.map(e => {
    const barWidth = (e.count / maxCount * 100).toFixed(0);
    const codeClass = e.exit_code === 0 ? 'ok' : 'err';
    const catClass = (e.category || 'unknown').replace(/ /g, '_');
    return '<tr>' +
      '<td><span class="exit-code ' + codeClass + '">' + e.exit_code + '</span></td>' +
      '<td>' + escapeHtml(e.description) + '</td>' +
      '<td><span class="category-badge ' + catClass + '">' + escapeHtml(e.category) + '</span></td>' +
      '<td><strong>' + e.count.toLocaleString() + '</strong></td>' +
      '<td>' + e.percentage.toFixed(1) + '%</td>' +
      '<td style="min-width:150px;"><div class="progress-bar"><div class="progress-bar-fill" style="width:' + barWidth + '%;background:var(--accent-red);"></div></div></td>' +
      '</tr>';
  }).join('');
}

function updateSignatureTable(signatures) {
  const tbody = document.getElementById('signatureTable');
  if (!tbody) return;
  if (!signatures || signatures.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--text-muted);padding:24px;">No structured failure signatures in this period (older clients don\'t report the failing command)</td></tr>';
    return;
  }
  tbody.innerHTML = signatures.map(s => {
    return '<tr>' +
      '<td style="font-family:monospace;font-size:12px;max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + escapeAttr(s.command) + '">' + escapeHtml(s.command) + '</td>' +
      '<td><strong style="color:var(--accent-red);">' + s.count.toLocaleString() + '</strong></td>' +
      '<td>' + (s.top_exit_code ? '<span class="exit-code err">' + s.top_exit_code + '</span>' : '-') + '</td>' +
      '<td>' + s.unique_apps + '</td>' +
      '<td style="font-size:12px;color:var(--text-secondary);max-width:300px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + escapeAttr(s.apps) + '">' + escapeHtml(s.apps) + '</td>' +
      '</tr>';
  }).join('');
}

function updateCategoryTable(categories) {
  const tbody = document.getElementById('categoryTable');
  if (!categories || categories.length === 0) {
    tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--text-muted);padding:24px;">No category data</td></tr>';
    return;
  }
  tbody.innerHTML = categories.map(c => {
    const catClass = (c.category || 'unknown').replace(/ /g, '_');
    return '<tr>' +
      '<td><span class="category-badge ' + catClass + '">' + escapeHtml(c.category) + '</span></td>' +
      '<td><strong>' + c.count.toLocaleString() + '</strong></td>' +
      '<td>' + c.percentage.toFixed(1) + '%</td>' +
      '<td style="font-size:12px;color:var(--text-secondary);max-width:400px;overflow:hidden;text-overflow:ellipsis;">' + escapeHtml(c.top_apps) + '</td>' +
      '</tr>';
  }).join('');
}

let allAppErrors = [];
function updateAppErrorTable(apps) {
  allAppErrors = apps || [];
  filterAppTable();
}

function filterAppTable() {
  const filter = (document.getElementById('appFilter').value || '').toLowerCase();
  const filtered = filter ? allAppErrors.filter(a => a.app.toLowerCase().includes(filter)) : allAppErrors;
  const tbody = document.getElementById('appErrorTable');
  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:24px;">No matching apps</td></tr>';
    return;
  }
  tbody.innerHTML = filtered.map((a, idx) => {
    const typeClass = (a.type || '').toLowerCase();
    const failRateColor = a.failure_rate > 50 ? 'var(--accent-red)' : a.failure_rate > 20 ? 'var(--accent-orange)' : 'var(--accent-yellow)';
    const topCat = a.top_category ? '<span class="category-badge ' + a.top_category + '">' + escapeHtml(a.top_category) + '</span>' : '-';
    const errorId = 'err-app-' + idx;
    const shortError = escapeHtml((a.top_error || '-').substring(0, 120));
    const fullError = escapeHtml(a.top_error || '-');
    const isLong = (a.top_error || '').length > 120;
    return '<tr>' +
      '<td><strong>' + escapeHtml(a.app) + '</strong></td>' +
      '<td><span class="type-badge ' + typeClass + '">' + (a.type || '-').toUpperCase() + '</span></td>' +
      '<td>' + a.total_count + '</td>' +
      '<td style="color:var(--accent-red);font-weight:600;">' + a.failed_count + '</td>' +
      '<td style="color:var(--accent-purple);">' + (a.aborted_count || 0) + '</td>' +
      '<td style="color:' + failRateColor + ';font-weight:600;">' + a.failure_rate.toFixed(1) + '%</td>' +
      '<td>' + (a.top_exit_code ? '<span class="exit-code err">' + a.top_exit_code + '</span>' : '-') + '</td>' +
      '<td class="error-text">' +
      '<div id="' + errorId + '-short">' + shortError + (isLong ? ' <a href="#" onclick="toggleError(\'' + errorId + '\');return false;" style="color:var(--accent-blue);font-size:11px;">show more</a>' : '') + '</div>' +
      (isLong ? '<div id="' + errorId + '-full" style="display:none;white-space:pre-wrap;word-break:break-all;max-height:600px;overflow-y:auto;">' + fullError + ' <a href="#" onclick="toggleError(\'' + errorId + '\');return false;" style="color:var(--accent-blue);font-size:11px;">show less</a></div>' : '') +
      '</td>' +
      '<td><button class="btn issue-btn" data-app="' + escapeAttr(a.app) + '" data-exit="' + (a.top_exit_code || 0) + '" data-error="' + escapeAttr(a.top_error || '') + '" data-rate="' + a.failure_rate.toFixed(1) + '">🐛 Issue</button></td>' +
      '</tr>';
  }).join('');
}

// ── Recent Error Log: filterable table + detail modal ──
let allRecentErrors = [];

function updateRecentErrors(errors) {
  allRecentErrors = errors || [];
  filterRecentTable();
}

// parseErrorText splits the structured error format into {header, trace}.
// Formats: "exit_code=N | desc | at line L: cmd\n---\n<trace>"  (host, v3)
//          "exit_code=N | desc|---|line1|line2|..."             (legacy container)
function parseErrorText(raw) {
  raw = raw || '';
  let header = '', trace = '';
  const nlIdx = raw.indexOf('\n---\n');
  if (nlIdx !== -1) {
    header = raw.substring(0, nlIdx).trim();
    trace = raw.substring(nlIdx + 5).trim();
  } else {
    const pipeIdx = raw.indexOf('|---|');
    if (pipeIdx !== -1) {
      header = raw.substring(0, pipeIdx).trim();
      trace = raw.substring(pipeIdx + 5).replace(/\|/g, '\n').trim();
    } else if (raw.indexOf('\n') !== -1) {
      header = raw.substring(0, raw.indexOf('\n')).trim();
      trace = raw.substring(raw.indexOf('\n') + 1).trim();
    } else {
      header = raw.trim();
    }
  }
  return { header: header, trace: trace };
}

// errorSummary extracts the human-readable part of the header:
// "exit_code=1 | General error | at line 42: npm run build" → "General error — at line 42: npm run build"
function errorSummary(raw) {
  const parsed = parseErrorText(raw);
  if (!parsed.header) return '-';
  const parts = parsed.header.split('|').map(function(s) { return s.trim(); });
  // Drop the leading "exit_code=N" (the table already has an Exit Code column)
  if (parts.length > 1 && /^exit_code=\d+$/.test(parts[0])) parts.shift();
  return parts.join(' — ');
}

function filterRecentTable() {
  const filter = (document.getElementById('recentFilter')?.value || '').toLowerCase();
  const errors = filter
    ? allRecentErrors.filter(e => (e.nsapp || '').toLowerCase().includes(filter) || (e.error || '').toLowerCase().includes(filter))
    : allRecentErrors;

  const tbody = document.getElementById('recentErrorTable');
  if (!errors || errors.length === 0) {
    tbody.innerHTML = '<tr><td colspan="11" style="text-align:center;color:var(--text-muted);padding:24px;">No recent errors</td></tr>';
    return;
  }
  tbody.innerHTML = errors.map((e, idx) => {
    const statusClass = e.status || 'unknown';
    const typeClass = (e.type || '').toLowerCase();
    const codeClass = e.exit_code === 0 ? 'ok' : 'err';
    const catClass = (e.error_category || 'unknown').replace(/ /g, '_');
    const os = e.os_type ? e.os_type + (e.os_version ? ' ' + e.os_version : '') : '-';
    const summary = errorSummary(e.error);
    const origIdx = allRecentErrors.indexOf(e);
    return '<tr class="clickable-row" onclick="showErrorDetail(' + origIdx + ')" style="cursor:pointer;">' +
      '<td><span class="status-badge ' + statusClass + '">' + escapeHtml(e.status) + '</span></td>' +
      '<td>' + platformBadge(e.platform) + '</td>' +
      '<td><span class="type-badge ' + typeClass + '">' + (e.type || '-').toUpperCase() + '</span></td>' +
      '<td><strong>' + escapeHtml(e.nsapp) + '</strong></td>' +
      '<td><span class="exit-code ' + codeClass + '">' + e.exit_code + '</span></td>' +
      '<td><span class="category-badge ' + catClass + '">' + escapeHtml(e.error_category || 'unknown') + '</span></td>' +
      '<td class="error-text" style="max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="' + escapeAttr(summary) + '">' + escapeHtml(summary) + '</td>' +
      '<td>' + escapeHtml(os) + '</td>' +
      '<td>' + formatDuration(e.install_duration) + '</td>' +
      '<td style="white-space:nowrap;">' + formatTimestamp(e.created) + '</td>' +
      '<td onclick="event.stopPropagation()"><button class="btn issue-btn" data-app="' + escapeAttr(e.nsapp) + '" data-exit="' + e.exit_code + '" data-error="' + escapeAttr(e.error || '') + '" data-rate="0">🐛</button></td>' +
      '</tr>';
  }).join('');
}

function showErrorDetail(idx) {
  const e = allRecentErrors[idx];
  if (!e) return;
  const parsed = parseErrorText(e.error);
  const os = e.os_type ? e.os_type + (e.os_version ? ' ' + e.os_version : '') : '—';

  document.getElementById('errorDetailTitle').textContent = e.nsapp + ' — exit ' + e.exit_code;

  let html = '<div class="detail-chips">';
  html += '<span class="status-badge ' + (e.status || 'unknown') + '">' + escapeHtml(e.status) + '</span>';
  html += platformBadge(e.platform);
  html += '<span class="type-badge ' + (e.type || '').toLowerCase() + '">' + (e.type || '-').toUpperCase() + '</span>';
  html += '<span class="category-badge ' + (e.error_category || 'unknown') + '">' + escapeHtml(e.error_category || 'unknown') + '</span>';
  html += '</div>';

  html += '<div class="detail-meta-grid">';
  html += '<div><span class="dm-label">OS</span><span>' + escapeHtml(os) + '</span></div>';
  html += '<div><span class="dm-label">' + (e.platform === 'incus' ? 'Incus' : 'PVE') + ' Version</span><span>' + escapeHtml(e.pve_version || '—') + '</span></div>';
  html += '<div><span class="dm-label">Duration</span><span>' + formatDuration(e.install_duration) + '</span></div>';
  html += '<div><span class="dm-label">Time</span><span>' + formatTimestamp(e.created) + '</span></div>';
  if (e.execution_id) {
    html += '<div style="grid-column:1/-1;"><span class="dm-label">Execution ID</span><span style="font-family:monospace;font-size:11px;">' + escapeHtml(e.execution_id) + '</span></div>';
  }
  html += '</div>';

  if (parsed.header) {
    html += '<div class="error-header-line">' + escapeHtml(parsed.header) + '</div>';
  }

  if (parsed.trace) {
    html += '<div class="error-trace-header"><span>Error Trace</span>' +
      '<button class="btn btn-sm" onclick="copyDetailTrace(this)">📋 Copy</button></div>';
    html += '<pre class="error-trace-box" id="detailTraceBox" data-trace="' + escapeAttr(parsed.trace) + '">' + escapeHtml(parsed.trace) + '</pre>';
  } else {
    html += '<div style="color:var(--text-muted);padding:12px 0;">No error trace was captured for this record.</div>';
  }

  html += '<div style="display:flex;justify-content:flex-end;margin-top:16px;">' +
    '<button class="btn issue-btn" data-app="' + escapeAttr(e.nsapp) + '" data-exit="' + e.exit_code + '" data-error="' + escapeAttr(e.error || '') + '" data-rate="0">🐛 Create GitHub Issue</button></div>';

  document.getElementById('errorDetailBody').innerHTML = html;
  document.getElementById('errorDetailModal').classList.add('active');
}

function closeErrorDetail() {
  document.getElementById('errorDetailModal').classList.remove('active');
}

function copyDetailTrace(btn) {
  const box = document.getElementById('detailTraceBox');
  const text = box ? (box.getAttribute('data-trace') || box.textContent) : '';
  navigator.clipboard.writeText(text).then(function() {
    const orig = btn.textContent;
    btn.textContent = '✅ Copied!';
    setTimeout(function() { btn.textContent = orig; }, 1500);
  });
}

function updateCharts(data) {
  // Timeline chart
  if (charts.timeline) charts.timeline.destroy();
  const timeline = data.error_timeline || [];
  charts.timeline = new Chart(document.getElementById('timelineChart'), {
    type: 'line',
    data: {
      labels: timeline.map(d => d.date.slice(5)),
      datasets: [
        { label: 'Failed', data: timeline.map(d => d.failed), borderColor: '#ef4444', backgroundColor: 'rgba(239,68,68,0.1)', fill: true, tension: 0.4, borderWidth: 2 },
        { label: 'Aborted', data: timeline.map(d => d.aborted), borderColor: '#a855f7', backgroundColor: 'rgba(168,85,247,0.1)', fill: true, tension: 0.4, borderWidth: 2 }
      ]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { labels: { color: '#8b949e', usePointStyle: true } } }, scales: { x: { ticks: { color: '#8b949e' }, grid: { color: '#2d3748' } }, y: { ticks: { color: '#8b949e' }, grid: { color: '#2d3748' } } } }
  });

  // Category pie chart
  if (charts.category) charts.category.destroy();
  const cats = data.category_stats || [];
  charts.category = new Chart(document.getElementById('categoryChart'), {
    type: 'doughnut',
    data: {
      labels: cats.map(c => c.category),
      datasets: [{ data: cats.map(c => c.count), backgroundColor: cats.map(c => catColors[c.category] || '#64748b'), borderWidth: 0 }]
    },
    options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'right', labels: { color: '#8b949e', padding: 12 } } } }
  });
}

// GitHub Issue Modal
function openIssueModal(app, exitCode, errorText, failRate) {
  const title = '[Telemetry] ' + app + ': Error (exit code ' + exitCode + ')';
  const fence = String.fromCharCode(96, 96, 96);
  const body = '## Telemetry Error Report\n\n' +
    '**Application:** ' + app + '\n' +
    '**Exit Code:** ' + exitCode + '\n' +
    '**Failure Rate:** ' + failRate + '%\n\n' +
    '### Error Details\n' + fence + '\n' + errorText + '\n' + fence + '\n\n' +
    '---\n*Created from telemetry error analysis dashboard.*';
  document.getElementById('issueTitle').value = title;
  document.getElementById('issueBody').value = body;
  document.getElementById('issueAlert').style.display = 'none';
  document.getElementById('issueModal').classList.add('active');
}

function closeIssueModal() {
  document.getElementById('issueModal').classList.remove('active');
}

async function submitIssue() {
  const btn = document.getElementById('submitIssueBtn');
  const alert = document.getElementById('issueAlert');
  const password = document.getElementById('issuePassword').value;
  if (!password) { alert.className = 'alert-box error'; alert.textContent = 'Password required'; alert.style.display = 'block'; return; }

  btn.disabled = true;
  btn.textContent = 'Creating...';

  try {
    const resp = await fetch('/api/github/create-issue', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        password: password,
        title: document.getElementById('issueTitle').value,
        body: document.getElementById('issueBody').value,
        labels: document.getElementById('issueLabels').value.split(',').map(l => l.trim()).filter(Boolean)
      })
    });
    const data = await resp.json();
    if (resp.ok && data.success) {
      alert.className = 'alert-box success';
      alert.innerHTML = '✅ Issue created! <a href="' + data.issue_url + '" target="_blank" style="color:var(--accent-green);">View on GitHub →</a>';
      alert.style.display = 'block';
    } else {
      throw new Error(data.error || data.message || resp.statusText || 'Failed');
    }
  } catch (e) {
    alert.className = 'alert-box error';
    alert.textContent = '❌ ' + e.message;
    alert.style.display = 'block';
  } finally {
    btn.disabled = false;
    btn.textContent = 'Create Issue';
  }
}

// Cleanup Modal
function triggerCleanup() {
  document.getElementById('cleanupAlert').style.display = 'none';
  document.getElementById('cleanupModal').classList.add('active');
}

function closeCleanupModal() {
  document.getElementById('cleanupModal').classList.remove('active');
}

async function runCleanup() {
  const btn = document.getElementById('runCleanupBtn');
  const alert = document.getElementById('cleanupAlert');
  const password = document.getElementById('cleanupPassword').value;
  if (!password) { alert.className = 'alert-box error'; alert.textContent = 'Password required'; alert.style.display = 'block'; return; }

  btn.disabled = true;
  btn.textContent = 'Running...';

  try {
    const resp = await fetch('/api/cleanup/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: password })
    });
    const data = await resp.json();
    if (resp.ok) {
      alert.className = 'alert-box success';
      alert.textContent = '✅ ' + data.message;
      alert.style.display = 'block';
      setTimeout(() => { closeCleanupModal(); refreshData(); }, 2000);
    } else {
      throw new Error(data.message || resp.statusText || 'Failed');
    }
  } catch (e) {
    alert.className = 'alert-box error';
    alert.textContent = '❌ ' + e.message;
    alert.style.display = 'block';
  } finally {
    btn.disabled = false;
    btn.textContent = 'Run Cleanup';
  }
}

async function refreshData() {
  await loadRepoSlugOptions();
  const data = await fetchData();
  if (!data) return;
  currentData = data;
  updateStats(data);
  updateExitCodeTable(data.exit_code_stats);
  updateSignatureTable(data.signatures);
  updateCategoryTable(data.category_stats);
  updateAppErrorTable(data.app_errors);
  updateRecentErrors(data.recent_errors);
  updateCharts(data);
}

// Filter button handling
document.querySelectorAll('.filter-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    refreshData();
  });
});
document.querySelectorAll('.source-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.source-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    const slugSelect = document.getElementById('filterSlug');
    if (slugSelect) slugSelect.value = '';
    refreshData();
  });
});
document.querySelectorAll('.platform-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.platform-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    refreshData();
  });
});
document.addEventListener('keydown', e => { if (e.key === 'Escape') { closeIssueModal(); closeCleanupModal(); closeErrorDetail(); } });

// Event delegation for Issue buttons (avoids inline onclick escaping issues)
document.addEventListener('click', function(e) {
  var btn = e.target.closest('.issue-btn');
  if (!btn) return;
  var app = btn.getAttribute('data-app') || '';
  var exitCode = parseInt(btn.getAttribute('data-exit') || '0', 10);
  var errorText = btn.getAttribute('data-error') || '';
  var rate = parseFloat(btn.getAttribute('data-rate') || '0');
  openIssueModal(app, exitCode, errorText, rate);
});

// Initial load
refreshData();

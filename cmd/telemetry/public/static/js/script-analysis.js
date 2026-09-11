let currentData = null;
let expandTop = false;
let expandBottom = false;
let expandRecent = false;
const LIMIT = 10;

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function formatTimestamp(ts) {
  if (!ts) return '-';
  return new Date(ts).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit', hour12: true });
}

function platformBadge(p) {
  if (p === 'incus') return '<span class="platform-badge incus" title="Incus host">INCUS</span>';
  if (p === 'pve') return '<span class="platform-badge pve" title="Proxmox VE host">PVE</span>';
  return '<span class="platform-badge unknown">—</span>';
}

async function fetchData() {
  // Default: today, ALL sources (repo filter is optional)
  const days = document.querySelector('.filter-btn.active')?.dataset.days || '1';
  const repo = document.querySelector('.source-btn.active')?.dataset.repo || 'all';
  const platform = document.querySelector('.platform-btn.active')?.dataset.platform || '';
  try {
    let url = '/api/scripts?days=' + days + '&repo=' + encodeURIComponent(repo);
    if (platform) url += '&platform=' + encodeURIComponent(platform);
    const resp = await fetch(url);
    if (!resp.ok) throw new Error('Fetch failed');
    return await resp.json();
  } catch (e) {
    console.error('Fetch error:', e);
    return null;
  }
}

function updateStats(data) {
  document.getElementById('totalInstalls').textContent = (data.total_installs || 0).toLocaleString();
  document.getElementById('uniqueScripts').textContent = (data.total_scripts || 0).toLocaleString();
  const avg = data.total_scripts > 0 ? (data.total_installs / data.total_scripts).toFixed(1) : '0';
  document.getElementById('avgInstalls').textContent = avg;
}

function renderTopTable() {
  const tbody = document.getElementById('topTableBody');
  if (!currentData || !currentData.top_scripts) {
    tbody.innerHTML = '<tr><td colspan="11" style="text-align:center;color:var(--text-muted);padding:24px;">No data</td></tr>';
    return;
  }
  const search = (document.getElementById('searchTop').value || '').toLowerCase();
  let scripts = currentData.top_scripts;
  if (search) {
    scripts = scripts.filter(s => s.app.toLowerCase().includes(search) || (s.type || '').toLowerCase().includes(search));
  }
  const limit = expandTop ? scripts.length : Math.min(LIMIT, scripts.length);
  const shown = scripts.slice(0, limit);

  tbody.innerHTML = shown.map((s, idx) => {
    const typeClass = (s.type || '').toLowerCase();
    const rateColor = s.success_rate >= 90 ? 'var(--accent-green)' : s.success_rate >= 70 ? 'var(--accent-yellow)' : 'var(--accent-red)';
    const total = s.success + s.failed + s.aborted + s.installing;
    const pctSuccess = total > 0 ? (s.success / total * 100) : 0;
    const pctFailed = total > 0 ? (s.failed / total * 100) : 0;
    const pctAborted = total > 0 ? (s.aborted / total * 100) : 0;
    const pctInstalling = total > 0 ? (s.installing / total * 100) : 0;
    const ipd = (s.installs_per_day || 0).toFixed(2);
    const ipdColor = s.installs_per_day >= 10 ? 'var(--accent-green)' : s.installs_per_day >= 1 ? 'var(--accent-cyan)' : 'var(--text-muted)';
    return '<tr' + scriptRowAttrs(s.app) + '>' +
      '<td style="color:var(--text-muted);font-weight:600;">' + (idx + 1) + '</td>' +
      '<td><strong>' + escapeHtml(s.app) + '</strong></td>' +
      '<td><span class="type-badge ' + typeClass + '">' + (s.type || '-').toUpperCase() + '</span></td>' +
      '<td style="font-weight:600;">' + s.total.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-green);">' + s.success.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-red);">' + s.failed.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-purple);">' + s.aborted.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-yellow);">' + s.installing.toLocaleString() + '</td>' +
      '<td style="color:' + rateColor + ';font-weight:600;">' + s.success_rate.toFixed(1) + '%</td>' +
      '<td style="color:' + ipdColor + ';font-weight:600;">' + ipd + '</td>' +
      '<td><div class="success-bar">' +
      '<div class="seg-success" style="width:' + pctSuccess + '%"></div>' +
      '<div class="seg-failed" style="width:' + pctFailed + '%"></div>' +
      '<div class="seg-aborted" style="width:' + pctAborted + '%"></div>' +
      '<div class="seg-installing" style="width:' + pctInstalling + '%"></div>' +
      '</div></td>' +
      '</tr>';
  }).join('');

  document.getElementById('expandTopBtn').textContent = expandTop ? 'Show Top 10' : 'Show All (' + scripts.length + ')';
}

function renderBottomTable() {
  const tbody = document.getElementById('bottomTableBody');
  if (!currentData || !currentData.top_scripts) {
    tbody.innerHTML = '<tr><td colspan="11" style="text-align:center;color:var(--text-muted);padding:24px;">No data</td></tr>';
    return;
  }
  const search = (document.getElementById('searchBottom').value || '').toLowerCase();
  // Reverse: least used first
  let scripts = [...currentData.top_scripts].reverse();
  if (search) {
    scripts = scripts.filter(s => s.app.toLowerCase().includes(search) || (s.type || '').toLowerCase().includes(search));
  }
  const limit = expandBottom ? scripts.length : Math.min(LIMIT, scripts.length);
  const shown = scripts.slice(0, limit);
  const totalScripts = currentData.top_scripts.length;

  tbody.innerHTML = shown.map((s, idx) => {
    const typeClass = (s.type || '').toLowerCase();
    const rateColor = s.success_rate >= 90 ? 'var(--accent-green)' : s.success_rate >= 70 ? 'var(--accent-yellow)' : 'var(--accent-red)';
    const total = s.success + s.failed + s.aborted + s.installing;
    const pctSuccess = total > 0 ? (s.success / total * 100) : 0;
    const pctFailed = total > 0 ? (s.failed / total * 100) : 0;
    const pctAborted = total > 0 ? (s.aborted / total * 100) : 0;
    const pctInstalling = total > 0 ? (s.installing / total * 100) : 0;
    const ipd = (s.installs_per_day || 0).toFixed(2);
    const ipdColor = s.installs_per_day >= 10 ? 'var(--accent-green)' : s.installs_per_day >= 1 ? 'var(--accent-cyan)' : 'var(--text-muted)';
    return '<tr' + scriptRowAttrs(s.app) + '>' +
      '<td style="color:var(--text-muted);font-weight:600;">' + (totalScripts - idx) + '</td>' +
      '<td><strong>' + escapeHtml(s.app) + '</strong></td>' +
      '<td><span class="type-badge ' + typeClass + '">' + (s.type || '-').toUpperCase() + '</span></td>' +
      '<td style="font-weight:600;">' + s.total.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-green);">' + s.success.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-red);">' + s.failed.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-purple);">' + s.aborted.toLocaleString() + '</td>' +
      '<td style="color:var(--accent-yellow);">' + s.installing.toLocaleString() + '</td>' +
      '<td style="color:' + rateColor + ';font-weight:600;">' + s.success_rate.toFixed(1) + '%</td>' +
      '<td style="color:' + ipdColor + ';font-weight:600;">' + ipd + '</td>' +
      '<td><div class="success-bar">' +
      '<div class="seg-success" style="width:' + pctSuccess + '%"></div>' +
      '<div class="seg-failed" style="width:' + pctFailed + '%"></div>' +
      '<div class="seg-aborted" style="width:' + pctAborted + '%"></div>' +
      '<div class="seg-installing" style="width:' + pctInstalling + '%"></div>' +
      '</div></td>' +
      '</tr>';
  }).join('');

  document.getElementById('expandBottomBtn').textContent = expandBottom ? 'Show Bottom 10' : 'Show All (' + scripts.length + ')';
}

function renderRecentTable() {
  const tbody = document.getElementById('recentTableBody');
  if (!currentData || !currentData.recent_scripts) {
    tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:var(--text-muted);padding:24px;">No data</td></tr>';
    return;
  }
  const search = (document.getElementById('searchRecent').value || '').toLowerCase();
  let scripts = currentData.recent_scripts;
  if (search) {
    scripts = scripts.filter(s => s.app.toLowerCase().includes(search) || (s.status || '').toLowerCase().includes(search) || (s.type || '').toLowerCase().includes(search));
  }
  const limit = expandRecent ? scripts.length : Math.min(LIMIT, scripts.length);
  const shown = scripts.slice(0, limit);

  tbody.innerHTML = shown.map(s => {
    const typeClass = (s.type || '').toLowerCase();
    const statusClass = s.status || 'unknown';
    const codeClass = s.exit_code === 0 ? 'ok' : 'err';
    const os = s.os_type ? s.os_type + (s.os_version ? ' ' + s.os_version : '') : '-';
    return '<tr>' +
      '<td><strong>' + escapeHtml(s.app) + '</strong></td>' +
      '<td><span class="type-badge ' + typeClass + '">' + (s.type || '-').toUpperCase() + '</span></td>' +
      '<td>' + platformBadge(s.platform) + '</td>' +
      '<td><span class="status-badge ' + statusClass + '">' + escapeHtml(s.status) + '</span></td>' +
      '<td><span class="exit-code ' + codeClass + '">' + s.exit_code + '</span></td>' +
      '<td>' + escapeHtml(os) + '</td>' +
      '<td>' + escapeHtml(s.pve_version || '-') + '</td>' +
      '<td>' + escapeHtml(s.method || '-') + '</td>' +
      '<td style="white-space:nowrap;">' + formatTimestamp(s.created) + '</td>' +
      '</tr>';
  }).join('');

  document.getElementById('expandRecentBtn').textContent = expandRecent ? 'Show Last 10' : 'Show All (' + scripts.length + ')';
}

// ── Per-script drill-down (daily chart + errors + recent installs) ──
let detailChart = null;

function scriptRowAttrs(app) {
  return ' class="clickable-row" style="cursor:pointer;" onclick="openScriptDetail(\'' + escapeHtml(app).replace(/'/g, "\\'") + '\')"';
}

async function openScriptDetail(app) {
  const modal = document.getElementById('scriptDetailModal');
  const body = document.getElementById('scriptDetailBody');
  document.getElementById('scriptDetailTitle').textContent = app;
  body.innerHTML = '<div class="loading" style="padding:40px;text-align:center;color:var(--text-muted);">Loading ' + escapeHtml(app) + '…</div>';
  modal.classList.add('active');

  // Drill-down window: per-day analysis needs a real range.
  // Today → 30d, All Time → 90d, otherwise the page period.
  const pageDays = parseInt(document.querySelector('.filter-btn.active')?.dataset.days || '30', 10);
  const days = pageDays === 0 ? 90 : (pageDays < 7 ? 30 : pageDays);
  const repo = document.querySelector('.source-btn.active')?.dataset.repo || 'all';
  const platform = document.querySelector('.platform-btn.active')?.dataset.platform || '';

  try {
    let url = '/api/script-detail?app=' + encodeURIComponent(app) + '&days=' + days + '&repo=' + encodeURIComponent(repo);
    if (platform) url += '&platform=' + encodeURIComponent(platform);
    const resp = await fetch(url);
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    renderScriptDetail(await resp.json(), days);
  } catch (e) {
    body.innerHTML = '<div style="padding:24px;color:var(--accent-red);">Failed to load: ' + escapeHtml(e.message) + '</div>';
  }
}

function closeScriptDetail() {
  document.getElementById('scriptDetailModal').classList.remove('active');
  if (detailChart) { detailChart.destroy(); detailChart = null; }
}

function renderScriptDetail(d, days) {
  const body = document.getElementById('scriptDetailBody');
  const rate = (d.success_rate || 0).toFixed(1);
  const rateColor = d.success_rate >= 90 ? 'var(--accent-green)' : d.success_rate >= 70 ? 'var(--accent-yellow)' : 'var(--accent-red)';
  const avgDur = d.avg_duration ? Math.round(d.avg_duration) : 0;
  const avgDurStr = avgDur ? (avgDur < 60 ? avgDur + 's' : Math.floor(avgDur / 60) + 'm ' + (avgDur % 60) + 's') : '—';

  let html = '';

  // Summary chips
  html += '<div class="sd-chips">';
  html += '<span class="type-badge ' + (d.type || '').toLowerCase() + '">' + (d.type || '-').toUpperCase() + '</span>';
  html += '<div class="sd-chip"><span class="sd-label">Installs (' + days + 'd)</span><strong>' + (d.total || 0).toLocaleString() + '</strong></div>';
  html += '<div class="sd-chip"><span class="sd-label">Success Rate</span><strong style="color:' + rateColor + ';">' + rate + '%</strong></div>';
  html += '<div class="sd-chip"><span class="sd-label">Failed</span><strong style="color:var(--accent-red);">' + (d.failed || 0).toLocaleString() + '</strong></div>';
  html += '<div class="sd-chip"><span class="sd-label">Aborted</span><strong style="color:var(--accent-purple);">' + (d.aborted || 0).toLocaleString() + '</strong></div>';
  html += '<div class="sd-chip"><span class="sd-label">Ø Duration</span><strong>' + avgDurStr + '</strong></div>';
  (d.platform_stats || []).forEach(p => {
    if (p.platform !== 'unknown') {
      html += '<div class="sd-chip">' + platformBadge(p.platform) + '<strong>' + p.count.toLocaleString() + '</strong></div>';
    }
  });
  html += '</div>';

  // Daily chart
  html += '<div class="sd-section"><h3>Installations per Day</h3><div style="height:220px;"><canvas id="sdDailyChart"></canvas></div></div>';

  // Two-column: exit codes + OS
  html += '<div class="sd-grid">';
  html += '<div class="sd-section"><h3>Top Exit Codes</h3>';
  if (d.exit_codes && d.exit_codes.length) {
    html += '<table class="sd-table"><thead><tr><th>Code</th><th>Description</th><th>Count</th></tr></thead><tbody>';
    d.exit_codes.forEach(ec => {
      html += '<tr><td><span class="exit-code err">' + ec.exit_code + '</span></td><td>' + escapeHtml(ec.description) + '</td><td><strong>' + ec.count + '</strong></td></tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="sd-empty">No failures in this period 🎉</div>';
  }
  html += '</div>';
  html += '<div class="sd-section"><h3>OS Distribution</h3>';
  if (d.os_distribution && d.os_distribution.length) {
    const osMax = Math.max(...d.os_distribution.map(o => o.count));
    d.os_distribution.forEach(o => {
      const w = (o.count / osMax * 100).toFixed(0);
      html += '<div class="sd-bar-row"><span class="sd-bar-label">' + escapeHtml(o.os) + '</span>' +
        '<div class="sd-bar"><div class="sd-bar-fill" style="width:' + w + '%"></div></div>' +
        '<span class="sd-bar-count">' + o.count.toLocaleString() + '</span></div>';
    });
  } else {
    html += '<div class="sd-empty">No OS data</div>';
  }
  html += '</div></div>';

  // Recent installations
  html += '<div class="sd-section"><h3>Recent Installations</h3>';
  if (d.recent && d.recent.length) {
    html += '<table class="sd-table"><thead><tr><th>Status</th><th>Platform</th><th>Exit</th><th>OS</th><th>Duration</th><th>Time</th></tr></thead><tbody>';
    d.recent.forEach(r => {
      const dur = r.install_duration ? (r.install_duration < 60 ? r.install_duration + 's' : Math.floor(r.install_duration / 60) + 'm') : '-';
      const os = r.os_type ? r.os_type + (r.os_version ? ' ' + r.os_version : '') : '-';
      html += '<tr>' +
        '<td><span class="status-badge ' + (r.status || 'unknown') + '">' + escapeHtml(r.status) + '</span></td>' +
        '<td>' + platformBadge(r.platform) + '</td>' +
        '<td>' + (r.exit_code ? '<span class="exit-code err">' + r.exit_code + '</span>' : '<span class="exit-code ok">0</span>') + '</td>' +
        '<td>' + escapeHtml(os) + '</td>' +
        '<td>' + dur + '</td>' +
        '<td style="white-space:nowrap;">' + formatTimestamp(r.created) + '</td>' +
        '</tr>';
    });
    html += '</tbody></table>';
  } else {
    html += '<div class="sd-empty">No installations in this period</div>';
  }
  html += '</div>';

  body.innerHTML = html;

  // Render the stacked daily chart
  const daily = d.daily || [];
  if (detailChart) detailChart.destroy();
  if (typeof Chart !== 'undefined' && daily.length) {
    detailChart = new Chart(document.getElementById('sdDailyChart'), {
      type: 'bar',
      data: {
        labels: daily.map(x => x.date.slice(5)),
        datasets: [
          { label: 'Success', data: daily.map(x => x.success), backgroundColor: '#22c55e', stack: 's' },
          { label: 'Failed', data: daily.map(x => x.failed), backgroundColor: '#ef4444', stack: 's' },
          { label: 'Aborted', data: daily.map(x => x.aborted), backgroundColor: '#a855f7', stack: 's' }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { labels: { color: '#8b949e', usePointStyle: true } } },
        scales: {
          x: { stacked: true, ticks: { color: '#8b949e', maxRotation: 45 }, grid: { display: false } },
          y: { stacked: true, beginAtZero: true, ticks: { color: '#8b949e', precision: 0 }, grid: { color: '#2d3748' } }
        }
      }
    });
  }
}

document.addEventListener('keydown', e => { if (e.key === 'Escape') closeScriptDetail(); });

function toggleExpand(which) {
  if (which === 'top') {
    expandTop = !expandTop;
    renderTopTable();
  } else if (which === 'bottom') {
    expandBottom = !expandBottom;
    renderBottomTable();
  } else {
    expandRecent = !expandRecent;
    renderRecentTable();
  }
}

async function refreshData() {
  const data = await fetchData();
  if (!data) return;
  currentData = data;
  updateStats(data);
  renderTopTable();
  renderBottomTable();
  renderRecentTable();
}

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
refreshData();

// Deep link: /script-analysis?app=jellyfin opens the drill-down directly
// (used by the clickable Top Applications chart on the dashboard)
(function() {
  const params = new URLSearchParams(window.location.search);
  const app = params.get('app');
  if (app) openScriptDetail(app);
})();

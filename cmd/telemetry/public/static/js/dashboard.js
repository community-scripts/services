let charts = {};
let allRecords = [];
let allAppsData = [];
let showingAllApps = false;
let currentPage = 1;
let totalPages = 1;
let perPage = 25;
let currentTheme = localStorage.getItem('theme') || 'dark';
let currentSort = { field: 'created', dir: 'desc' };

function getActiveFilters() {
  const days = document.querySelector('.filter-btn.active')?.dataset.days || '1';
  // Default: ALL sources, today
  const repo = document.querySelector('.source-btn.active')?.dataset.repo || 'all';
  const platform = document.querySelector('.platform-btn.active')?.dataset.platform || '';
  const slug = document.getElementById('filterSlug')?.value || '';
  return { days, repo, platform, slug };
}

// platformBadge renders a small pve/incus badge (empty string when unknown)
function platformBadge(p) {
  if (p === 'incus') return '<span class="platform-badge incus" title="Incus host">INCUS</span>';
  if (p === 'pve') return '<span class="platform-badge pve" title="Proxmox VE host">PVE</span>';
  return '<span class="platform-badge unknown">—</span>';
}

function escapeAttr(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
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

function selectRepoSlug(slug) {
  const select = document.getElementById('filterSlug');
  if (select) {
    select.value = slug;
    refreshData();
  }
}

function updateRepoSlugs(slugs) {
  const section = document.getElementById('repoSlugsSection');
  const list = document.getElementById('repoSlugsList');
  if (!section || !list) return;
  const activeSlug = document.getElementById('filterSlug')?.value || '';
  if (!slugs || slugs.length <= 1 || activeSlug) {
    section.style.display = 'none';
    return;
  }
  section.style.display = 'block';
  list.innerHTML = slugs.map(s =>
    '<button type="button" class="repo-slug-chip" onclick="selectRepoSlug(\'' + escapeAttr(s.slug) + '\')">' +
    escapeHtml(s.slug) + ' <span class="count">' + s.count.toLocaleString() + '</span></button>'
  ).join('');
}

// Auto-refresh state
let autoRefreshEnabled = localStorage.getItem('autoRefresh') === 'true';
let autoRefreshInterval = 15000; // 15 seconds
let autoRefreshTimer = null;

// Colorful palette for Top Applications chart
const appBarColors = [
  '#3b82f6', '#f97316', '#22c55e', '#a855f7', '#ef4444',
  '#22d3ee', '#eab308', '#ec4899', '#84cc16', '#6366f1',
  '#14b8a6', '#f43f5e', '#8b5cf6', '#10b981', '#06b6d4',
  '#d946ef', '#facc15', '#2dd4bf'
];

// Apply saved theme on load
if (currentTheme === 'light') {
  document.documentElement.setAttribute('data-theme', 'light');
  document.getElementById('themeIcon').textContent = '☀️';
}

// Fetch GitHub stars
async function fetchGitHubStars() {
  try {
    const resp = await fetch('https://api.github.com/repos/community-scripts/ProxmoxVE');
    const data = await resp.json();
    if (data.stargazers_count) {
      document.getElementById('starCount').textContent = data.stargazers_count.toLocaleString();
    }
  } catch (e) {
    console.log('Could not fetch GitHub stars');
  }
}
fetchGitHubStars();

function toggleTheme() {
  if (currentTheme === 'dark') {
    document.documentElement.setAttribute('data-theme', 'light');
    document.getElementById('themeIcon').textContent = '☀️';
    currentTheme = 'light';
  } else {
    document.documentElement.removeAttribute('data-theme');
    document.getElementById('themeIcon').textContent = '🌙';
    currentTheme = 'dark';
  }
  localStorage.setItem('theme', currentTheme);
  if (Object.keys(charts).length > 0) {
    refreshData();
  }
}

function handleGlobalSearch(event) {
  if (event.key === 'Enter') {
    const query = event.target.value.trim();
    if (query) {
      document.getElementById('filterApp').value = query;
      filterTable();
      document.querySelector('.section-card:last-of-type').scrollIntoView({ behavior: 'smooth' });
    }
  }
}

// Keyboard shortcut for search
document.addEventListener('keydown', function(e) {
  if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
    e.preventDefault();
    document.getElementById('globalSearch').focus();
  }
});

const chartDefaults = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      labels: { color: '#8b949e' }
    }
  },
  scales: {
    x: {
      ticks: { color: '#8b949e' },
      grid: { color: '#2d3748' }
    },
    y: {
      ticks: { color: '#8b949e' },
      grid: { color: '#2d3748' }
    }
  }
};

async function fetchData() {
  const { days, repo, platform, slug } = getActiveFilters();

  // Show loading indicator
  document.getElementById('loadingIndicator').style.display = 'flex';
  document.getElementById('cacheStatus').textContent = '';

  try {
    // Add cache-busting timestamp for filter changes to ensure fresh data
    const cacheBuster = '&_t=' + Date.now();
    let url = '/api/dashboard?days=' + days + '&repo=' + repo + cacheBuster;
    if (platform) url += '&platform=' + encodeURIComponent(platform);
    if (slug) url += '&slug=' + encodeURIComponent(slug);
    const response = await fetch(url);
    if (!response.ok) throw new Error('Failed to fetch data');

    // Check cache status from header
    const cacheHit = response.headers.get('X-Cache') === 'HIT';
    document.getElementById('cacheStatus').textContent = cacheHit ? '(cached)' : '(fresh)';

    return await response.json();
  } catch (error) {
    document.getElementById('error').style.display = 'flex';
    document.getElementById('errorText').textContent = error.message;
    throw error;
  } finally {
    document.getElementById('loadingIndicator').style.display = 'none';
  }
}

function updateStats(data) {
  // Show time-filtered total as main number, all-time as subtitle
  document.getElementById('totalInstalls').textContent = (data.total_installs || 0).toLocaleString();
  
  // Show all-time total in subtitle if different from filtered total
  const allTime = data.total_all_time || data.total_installs || 0;
  const subtitle = document.getElementById('totalSubtitle');
  if (subtitle) {
    subtitle.textContent = allTime !== data.total_installs 
      ? allTime.toLocaleString() + ' all time' 
      : '';
  }

  // Failed count (separate card)
  document.getElementById('failedCount').textContent = (data.failed_count || 0).toLocaleString();
  document.getElementById('failedSubtitle').textContent = data.failed_count > 0 ? 'installation failures' : 'no failures';

  // Aborted count (separate card)
  document.getElementById('abortedCount').textContent = (data.aborted_count || 0).toLocaleString();

  document.getElementById('successRate').textContent = data.success_rate.toFixed(1) + '%';
  document.getElementById('successSubtitle').textContent = data.success_count.toLocaleString() + ' successful installations';
  document.getElementById('lastUpdated').textContent = 'Updated ' + new Date().toLocaleTimeString();
  document.getElementById('error').style.display = 'none';

  // Most Popular - update podium
  function formatCompact(n) {
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
    return n.toString();
  }
  if (data.top_apps && data.top_apps.length >= 3) {
    document.getElementById('podium1App').textContent = data.top_apps[0].app;
    document.getElementById('podium1Count').textContent = formatCompact(data.top_apps[0].count);
    document.getElementById('podium2App').textContent = data.top_apps[1].app;
    document.getElementById('podium2Count').textContent = formatCompact(data.top_apps[1].count);
    document.getElementById('podium3App').textContent = data.top_apps[2].app;
    document.getElementById('podium3Count').textContent = formatCompact(data.top_apps[2].count);
  } else if (data.top_apps && data.top_apps.length > 0) {
    document.getElementById('podium1App').textContent = data.top_apps[0].app;
    document.getElementById('podium1Count').textContent = formatCompact(data.top_apps[0].count);
  }

  // Store all apps data for View All feature
  allAppsData = data.top_apps || [];

  // Error Analysis
  updateErrorAnalysis(data.error_analysis || []);

  // Failed Apps
  updateFailedApps(data.failed_apps || []);

  // Repository fork breakdown
  updateRepoSlugs(data.repo_slugs || []);
}

function updateErrorAnalysis(errors) {
  const container = document.getElementById('errorList');
  if (!errors || errors.length === 0) {
    container.innerHTML = '<div style="padding: 20px; color: var(--text-muted); text-align: center; font-size: 13px;">No errors recorded</div>';
    return;
  }
  container.innerHTML = errors.slice(0, 6).map(e =>
    '<div class="error-item">' +
    '<div style="min-width:0;flex:1;">' +
    '<div class="pattern">' + escapeHtml(e.pattern) + '</div>' +
    '<div class="meta">' + e.unique_apps + ' app' + (e.unique_apps !== 1 ? 's' : '') + ' affected</div>' +
    '</div>' +
    '<span class="count-badge">' + e.count.toLocaleString() + 'x</span>' +
    '</div>'
  ).join('');
}

function updateFailedApps(apps) {
  const container = document.getElementById('failedAppsGrid');
  const activeDays = parseInt(document.querySelector('.filter-btn.active')?.dataset.days || '1');
  let minInstalls = 10;
  if (activeDays <= 1) minInstalls = 5;
  else if (activeDays <= 7) minInstalls = 15;
  else if (activeDays <= 30) minInstalls = 40;
  else if (activeDays <= 90) minInstalls = 100;
  else minInstalls = 100;
  document.getElementById('failedAppsThreshold').textContent = '(min. ' + minInstalls + ' installs)';
  if (!apps || apps.length === 0) {
    container.innerHTML = '<div style="padding: 20px; color: var(--text-muted); text-align: center; font-size: 13px;">Not enough data (min. ' + minInstalls + ' installs)</div>';
    return;
  }
  container.innerHTML = apps.slice(0, 8).map(a => {
    const typeClass = (a.type || '').toLowerCase();
    const typeBadge = a.type && a.type !== 'unknown' ? '<span class="type-badge ' + typeClass + '">' + a.type.toUpperCase() + '</span>' : '';
    const rate = a.failure_rate;
    const severityClass = rate >= 30 ? 'critical' : rate >= 15 ? 'warning' : 'moderate';
    return '<div class="failed-app-card">' +
      '<div class="app-info">' + typeBadge + '<span class="app-name">' + escapeHtml(a.app) + '</span>' +
      '<span class="details">' + a.failed_count + '/' + a.total_count + '</span>' +
      '</div>' +
      '<span class="failure-rate ' + severityClass + '">' + rate.toFixed(1) + '%</span>' +
      '</div>';
  }).join('');
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function formatTimestamp(ts) {
  if (!ts) return '-';
  const d = new Date(ts);
  // Format: "Feb 11, 2026, 4:33 PM"
  return d.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    hour12: true
  });
}

function initSortableHeaders() {
  document.querySelectorAll('th.sortable').forEach(th => {
    th.style.cursor = 'pointer';
    th.addEventListener('click', () => sortByColumn(th.dataset.sort));
  });
}

function sortByColumn(field) {
  if (currentSort.field === field) {
    currentSort.dir = currentSort.dir === 'asc' ? 'desc' : 'asc';
  } else {
    currentSort.field = field;
    currentSort.dir = 'desc';
  }

  document.querySelectorAll('th.sortable').forEach(th => {
    th.classList.remove('sort-asc', 'sort-desc');
    th.textContent = th.textContent.replace(/[▲▼]/g, '').trim();
  });

  const activeTh = document.querySelector('th[data-sort=\"' + field + '\"]');
  if (activeTh) {
    activeTh.classList.add(currentSort.dir === 'asc' ? 'sort-asc' : 'sort-desc');
    activeTh.textContent += ' ' + (currentSort.dir === 'asc' ? '▲' : '▼');
  }

  currentPage = 1;
  fetchPaginatedRecords();
}

function toggleAllApps() {
  showingAllApps = !showingAllApps;
  const btn = document.getElementById('viewAllAppsBtn');
  const container = document.getElementById('appsChartContainer');

  if (showingAllApps) {
    btn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg> Show Less';
    container.style.height = '600px';
  } else {
    btn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg> View All';
    container.style.height = '420px';
  }

  updateAppsChart(allAppsData);
}

function updateAppsChart(topApps) {
  const displayApps = showingAllApps ? topApps.slice(0, 30) : topApps.slice(0, 15);
  const colors = displayApps.map((_, i) => appBarColors[i % appBarColors.length]);

  if (charts.apps) charts.apps.destroy();
  charts.apps = new Chart(document.getElementById('appsChart'), {
    type: 'bar',
    data: {
      labels: displayApps.map(a => a.app),
      datasets: [{
        label: 'Installations',
        data: displayApps.map(a => a.count),
        backgroundColor: colors,
        borderRadius: 6,
        borderSkipped: false
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      indexAxis: 'x',
      onClick: function(evt, elements) {
        // Click a bar → open the per-script daily drill-down on the Scripts page
        if (elements && elements.length > 0) {
          const app = displayApps[elements[0].index]?.app;
          if (app) window.location.href = '/script-analysis?app=' + encodeURIComponent(app);
        }
      },
      onHover: function(evt, elements) {
        evt.native.target.style.cursor = elements && elements.length ? 'pointer' : 'default';
      },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(21, 27, 35, 0.95)',
          titleColor: '#e2e8f0',
          bodyColor: '#e2e8f0',
          borderColor: '#2d3748',
          borderWidth: 1,
          padding: 12,
          displayColors: true,
          callbacks: {
            label: function(ctx) {
              return ctx.parsed.y.toLocaleString() + ' installations (click for details)';
            }
          }
        }
      },
      scales: {
        x: {
          ticks: {
            color: '#8b949e',
            maxRotation: 45,
            minRotation: 45
          },
          grid: { display: false }
        },
        y: {
          beginAtZero: true,
          ticks: {
            color: '#8b949e',
            callback: function(value) {
              if (value >= 1000) return (value / 1000).toFixed(0) + 'k';
              return value;
            }
          },
          grid: { color: '#2d3748' }
        }
      }
    }
  });
}

function updateCharts(data) {
  // Daily chart
  if (charts.daily) charts.daily.destroy();
  charts.daily = new Chart(document.getElementById('dailyChart'), {
    type: 'line',
    data: {
      labels: data.daily_stats.map(d => d.date.slice(5)),
      datasets: [
        {
          label: 'Success',
          data: data.daily_stats.map(d => d.success),
          borderColor: '#22c55e',
          backgroundColor: 'rgba(34, 197, 94, 0.1)',
          fill: true,
          tension: 0.4,
          borderWidth: 2
        },
        {
          label: 'Failed',
          data: data.daily_stats.map(d => d.failed),
          borderColor: '#ef4444',
          backgroundColor: 'rgba(239, 68, 68, 0.1)',
          fill: true,
          tension: 0.4,
          borderWidth: 2
        }
      ]
    },
    options: {
      ...chartDefaults,
      plugins: { legend: { display: true, position: 'top', labels: { color: '#8b949e', usePointStyle: true } } }
    }
  });

  // OS distribution - horizontal bar chart
  if (charts.os) charts.os.destroy();
  charts.os = new Chart(document.getElementById('osChart'), {
    type: 'bar',
    data: {
      labels: data.os_distribution.map(o => o.os),
      datasets: [{
        data: data.os_distribution.map(o => o.count),
        backgroundColor: appBarColors.slice(0, data.os_distribution.length),
        borderRadius: 4,
        borderSkipped: false
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      indexAxis: 'y',
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            label: function(ctx) {
              return ctx.parsed.x.toLocaleString() + ' installations';
            }
          }
        }
      },
      scales: {
        x: {
          ticks: {
            color: '#8b949e',
            callback: function(v) { return v >= 1000 ? (v / 1000).toFixed(0) + 'k' : v; }
          },
          grid: { color: '#2d3748' }
        },
        y: {
          ticks: { color: '#8b949e' },
          grid: { display: false }
        }
      }
    }
  });

  // Status pie chart (now includes Aborted — was silently missing before)
  if (charts.status) charts.status.destroy();
  charts.status = new Chart(document.getElementById('statusChart'), {
    type: 'doughnut',
    data: {
      labels: ['Success', 'Failed', 'Aborted', 'Installing'],
      datasets: [{
        data: [data.success_count, data.failed_count, data.aborted_count || 0, data.installing_count],
        backgroundColor: ['#22c55e', '#ef4444', '#a855f7', '#eab308'],
        borderWidth: 0
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { position: 'right', labels: { color: '#8b949e', padding: 12 } }
      }
    }
  });

  // Platform doughnut (pve vs incus)
  if (charts.platform) charts.platform.destroy();
  const platStats = (data.platform_stats || []);
  const platColors = { pve: '#e67e22', incus: '#14b8a6', unknown: '#64748b' };
  charts.platform = new Chart(document.getElementById('platformChart'), {
    type: 'doughnut',
    data: {
      labels: platStats.map(p => p.platform === 'pve' ? 'Proxmox VE' : p.platform === 'incus' ? 'Incus' : 'Unknown'),
      datasets: [{
        data: platStats.map(p => p.count),
        backgroundColor: platStats.map(p => platColors[p.platform] || '#64748b'),
        borderWidth: 0
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { position: 'right', labels: { color: '#8b949e', padding: 12 } }
      }
    }
  });

  // Top apps chart
  updateAppsChart(data.top_apps || []);
}

function updateTable(records) {
  allRecords = records || [];

  // Populate OS filter
  const osFilter = document.getElementById('filterOs');
  const uniqueOs = [...new Set(allRecords.map(r => r.os_type).filter(Boolean))];
  osFilter.innerHTML = '<option value="">All OS</option>' +
    uniqueOs.map(os => '<option value="' + os + '">' + os + '</option>').join('');

  filterTable();
}

function changePerPage() {
  perPage = parseInt(document.getElementById('perPageSelect').value);
  currentPage = 1;
  fetchPaginatedRecords();
}

async function fetchPaginatedRecords() {
  const status = document.getElementById('filterStatus').value;
  const app = document.getElementById('filterApp').value;
  const os = document.getElementById('filterOs').value;
  const type = document.getElementById('filterType').value;
  const platformSel = document.getElementById('filterPlatform') ? document.getElementById('filterPlatform').value : '';

  try {
    const { days, repo, platform, slug } = getActiveFilters();
    // Table-local platform select overrides the global chip filter
    const effPlatform = platformSel || platform;

    let url = '/api/records?page=' + currentPage + '&limit=' + perPage + '&days=' + days + '&repo=' + encodeURIComponent(repo);
    if (effPlatform) url += '&platform=' + encodeURIComponent(effPlatform);
    if (slug) url += '&slug=' + encodeURIComponent(slug);
    if (status) url += '&status=' + encodeURIComponent(status);
    if (app) url += '&app=' + encodeURIComponent(app);
    if (os) url += '&os=' + encodeURIComponent(os);
    if (type) url += '&type=' + encodeURIComponent(type);
    if (currentSort.field) {
      url += '&sort=' + (currentSort.dir === 'desc' ? '-' : '') + currentSort.field;
    }

    const response = await fetch(url);
    if (!response.ok) throw new Error('Failed to fetch records');
    const data = await response.json();

    totalPages = data.total_pages || 1;
    document.getElementById('pageInfo').textContent = 'Page ' + currentPage + ' of ' + totalPages + ' (' + data.total + ' total)';
    document.getElementById('prevBtn').disabled = currentPage <= 1;
    document.getElementById('nextBtn').disabled = currentPage >= totalPages;

    renderTableRows(data.records || []);
  } catch (e) {
    console.error('Pagination error:', e);
  }
}

function prevPage() {
  if (currentPage > 1) {
    currentPage--;
    fetchPaginatedRecords();
  }
}

function nextPage() {
  if (currentPage < totalPages) {
    currentPage++;
    fetchPaginatedRecords();
  }
}

// Store current records for detail view
let currentRecords = [];

function renderTableRows(records) {
  const tbody = document.getElementById('recordsTable');
  currentRecords = records;

  if (records.length === 0) {
    tbody.innerHTML = '<tr><td colspan="10"><div class="loading" style="padding: 40px;">No records found</div></td></tr>';
    return;
  }

  tbody.innerHTML = records.map((r, index) => {
    const statusClass = r.status || 'unknown';
    const typeClass = (r.type || '').toLowerCase();
    const created = r.created ? formatTimestamp(r.created) : '-';
    const osDisplay = r.os_type ? (r.os_type + (r.os_version ? ' ' + r.os_version : '')) : '-';
    const repoDisplay = r.repo_slug || r.repo_source || '-';

    // Compact resources column: "4C · 8GB · 20GB"
    const resParts = [];
    if (r.core_count) resParts.push(r.core_count + 'C');
    if (r.ram_size) resParts.push((r.ram_size >= 1024 ? (r.ram_size / 1024).toFixed(0) + 'G' : r.ram_size + 'M'));
    if (r.disk_size) resParts.push(r.disk_size + 'G');
    const resources = resParts.length ? resParts.join(' · ') : '-';

    const duration = r.install_duration ? formatDuration(r.install_duration) : '-';

    // Exit code column: show badge for failed, dash for success/running
    let exitCodeCell = '-';
    if (r.status === 'failed' && r.exit_code !== undefined && r.exit_code !== null) {
      exitCodeCell = '<span class="exit-code-badge">' + r.exit_code + '</span>';
    } else if (r.status === 'aborted' && r.exit_code !== undefined && r.exit_code !== null) {
      exitCodeCell = '<span class="exit-code-badge aborted">' + r.exit_code + '</span>';
    }

    const archBadge = r.has_arm
      ? '<span class="arm-badge" title="Installed on ARM64 hardware">ARM64</span>'
      : '';

    return '<tr class="clickable-row' + (r.has_arm ? ' arm-row' : '') + '" onclick="showRecordDetail(' + index + ')">' +
      '<td><span class="status-badge ' + statusClass + '">' + escapeHtml(r.status || 'unknown') + '</span></td>' +
      '<td>' + exitCodeCell + '</td>' +
      '<td><span class="type-badge ' + typeClass + '">' + escapeHtml((r.type || '-').toUpperCase()) + '</span></td>' +
      '<td>' + platformBadge(r.platform) + '</td>' +
      '<td><strong>' + escapeHtml(r.nsapp || '-') + '</strong> ' + archBadge + '</td>' +
      '<td class="repo-slug-cell" title="' + escapeAttr(repoDisplay) + '">' + escapeHtml(repoDisplay) + '</td>' +
      '<td>' + escapeHtml(osDisplay) + '</td>' +
      '<td style="white-space:nowrap;">' + resources + '</td>' +
      '<td>' + duration + '</td>' +
      '<td>' + created + '</td>' +
      '</tr>';
  }).join('');
}

function showRecordDetail(index) {
  const record = currentRecords[index];
  if (!record) return;

  const modal = document.getElementById('detailModal');
  const modalTitle = document.getElementById('modalTitle').querySelector('span');
  const modalBody = document.getElementById('modalBody');

  modalTitle.textContent = record.nsapp || 'Record Details';

  let html = '';

  // ── Header Card: quick overview strip ──
  const statusCls = record.status || 'unknown';
  const typeLbl = formatType(record.type) || record.type || '-';
  const durStr = record.install_duration ? formatDuration(record.install_duration) : null;

  html += '<div class="detail-header-strip">';
  html += '<div class="header-chip"><span class="status-badge ' + statusCls + '">' + escapeHtml(record.status || 'unknown') + '</span></div>';
  if (record.exit_code !== undefined && record.exit_code !== null) {
    const ecClass = record.exit_code === 0 ? 'ec-ok' : 'ec-fail';
    html += '<div class="header-chip"><span class="header-chip-label">Exit</span><span class="exit-code-inline ' + ecClass + '">' + record.exit_code + '</span></div>';
  }
  html += '<div class="header-chip"><span class="header-chip-label">Type</span>' + escapeHtml(typeLbl) + '</div>';
  if (record.method && record.method !== 'default') {
    html += '<div class="header-chip"><span class="header-chip-label">Method</span>' + escapeHtml(record.method) + '</div>';
  }
  if (durStr) {
    html += '<div class="header-chip"><span class="header-chip-label">Duration</span>' + escapeHtml(durStr) + '</div>';
  }
  if (record.created) {
    html += '<div class="header-chip"><span class="header-chip-label">Created</span>' + escapeHtml(formatFullTimestamp(record.created)) + '</div>';
  }
  html += '</div>';

  // ── Pipeline Section ──
  html += '<div class="detail-section">';
  html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg> Installation Pipeline</div>';
  if (record.pipeline) {
    html += renderPipeline(record.pipeline);
  } else {
    html += '<div class="pipeline-empty">Pipeline tracking not available for this record</div>';
  }
  html += '</div>';

  // ── Error Section (if failed) ──
  if (record.error) {
    html += '<div class="detail-section">';
    html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg> Error Details</div>';
    html += renderErrorSection(record);
    html += '</div>';
  }

  // ── System & Hardware (2-col layout) ──
  html += '<div class="detail-section">';
  html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="4" y="4" width="16" height="16" rx="2" ry="2"/><rect x="9" y="9" width="6" height="6"/><line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/><line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/><line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="14" x2="23" y2="14"/><line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="14" x2="4" y2="14"/></svg> System Resources</div>';
  html += '<div class="detail-grid">';
  html += buildDetailItem('CPU Cores', record.core_count ? record.core_count + ' Cores' : null);
  html += buildDetailItem('RAM', record.ram_size ? formatBytes(record.ram_size * 1024 * 1024) : null);
  html += buildDetailItem('Disk Size', record.disk_size ? record.disk_size + ' GB' : null);
  html += buildDetailItem('CT Type', record.ct_type !== undefined ? (record.ct_type === 1 ? 'Unprivileged' : 'Privileged') : null);
  html += '</div></div>';

  // ── OS Section ──
  html += '<div class="detail-section">';
  html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2" ry="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg> Operating System</div>';
  html += '<div class="detail-grid">';
  html += buildDetailItem('OS Type', record.os_type);
  html += buildDetailItem('OS Version', record.os_version);
  html += buildDetailItem('Platform', platformBadge(record.platform), null, true);
  html += buildDetailItem(record.platform === 'incus' ? 'Incus Version' : 'PVE Version', record.pve_version);
  html += buildDetailItem('Architecture', record.has_arm
    ? '<span class="arm-badge">ARM64</span>'
    : '<span class="arch-badge">amd64</span>', null, true);
  html += '</div></div>';

  // ── Hardware Section ──
  const hasHardwareInfo = record.cpu_vendor || record.cpu_model || record.gpu_vendor || record.gpu_model || record.ram_speed;
  if (hasHardwareInfo) {
    html += '<div class="detail-section">';
    html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg> Hardware</div>';
    html += '<div class="detail-grid">';
    html += buildDetailItem('CPU Vendor', record.cpu_vendor);
    html += buildDetailItem('CPU Model', record.cpu_model);
    html += buildDetailItem('RAM Speed', record.ram_speed);
    html += buildDetailItem('GPU Vendor', record.gpu_vendor);
    html += buildDetailItem('GPU Model', record.gpu_model);
    html += buildDetailItem('GPU Passthrough', formatPassthrough(record.gpu_passthrough));
    html += '</div></div>';
  }

  // ── Meta Section (IDs / timestamps) ──
  html += '<div class="detail-section">';
  html += '<div class="detail-section-header"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg> Metadata</div>';
  html += '<div class="detail-grid">';
  html += buildDetailItem('Repo Source', record.repo_source);
  html += buildDetailItem('Repository', record.repo_slug);
  html += buildDetailItem('Random ID', record.random_id, 'mono');
  if (record.execution_id) {
    html += buildDetailItem('Execution ID', record.execution_id, 'mono');
  }
  html += buildDetailItem('Created', formatFullTimestamp(record.created));
  html += buildDetailItem('Updated', formatFullTimestamp(record.updated));
  html += '</div></div>';

  modalBody.innerHTML = html;
  modal.classList.add('active');
  document.body.style.overflow = 'hidden';
}

// Render the error section with parsed trace
function renderErrorSection(record) {
  var raw = record.error || '';
  var html = '';

  // Parse the structured error format: "exit_code=N | description\n---\nlog_lines"
  var headerLine = '';
  var traceLines = '';
  var separatorIdx = raw.indexOf('\n---\n');
  if (separatorIdx !== -1) {
    headerLine = raw.substring(0, separatorIdx).trim();
    traceLines = raw.substring(separatorIdx + 5).trim();
  } else {
    // No separator — try pipe-separated (container fallback uses | as newline)
    var pipeIdx = raw.indexOf('|---|');
    if (pipeIdx !== -1) {
      headerLine = raw.substring(0, pipeIdx).trim();
      traceLines = raw.substring(pipeIdx + 4).replace(/\|/g, '\n').trim();
    } else {
      headerLine = raw;
    }
  }

  // Error category badge
  if (record.error_category) {
    html += '<div class="error-category-row">';
    html += '<span class="error-category-badge">' + escapeHtml(record.error_category) + '</span>';
    if (headerLine) {
      html += '<span class="error-explanation">' + escapeHtml(headerLine) + '</span>';
    }
    html += '</div>';
  } else if (headerLine) {
    html += '<div class="error-category-row">';
    html += '<span class="error-explanation">' + escapeHtml(headerLine) + '</span>';
    html += '</div>';
  }

  // Error trace / log
  if (traceLines) {
    html += '<div class="error-trace-header">';
    html += '<span>Error Trace</span>';
    html += '<button class="btn-copy-trace" onclick="copyErrorTrace(this)" title="Copy to clipboard"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1"/></svg> Copy</button>';
    html += '</div>';
    html += '<pre class="error-trace-box" data-trace="' + escapeAttr(traceLines) + '">' + escapeHtml(traceLines) + '</pre>';
  } else if (!headerLine && raw) {
    // Raw unstructured error — show as-is
    html += '<div class="error-trace-header"><span>Raw Error</span></div>';
    html += '<pre class="error-trace-box">' + escapeHtml(raw) + '</pre>';
  } else if (!traceLines && headerLine) {
    html += '<div class="error-trace-empty">No error trace was captured for this record.</div>';
  }

  return html;
}

function copyErrorTrace(btn) {
  var box = btn.closest('.detail-section').querySelector('.error-trace-box');
  var text = box ? (box.getAttribute('data-trace') || box.textContent) : '';
  navigator.clipboard.writeText(text).then(function() {
    var orig = btn.innerHTML;
    btn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg> Copied!';
    setTimeout(function() { btn.innerHTML = orig; }, 1500);
  });
}

function buildDetailItem(label, value, extraClass, allowHtml) {
  if (value === null || value === undefined || value === '') {
    return '<div class="detail-item"><div class="label">' + escapeHtml(label) + '</div><div class="value" style="color: var(--text-secondary);">—</div></div>';
  }
  const valueClass = extraClass ? 'value ' + extraClass : 'value';
  const rendered = allowHtml ? String(value) : escapeHtml(String(value));
  return '<div class="detail-item"><div class="label">' + escapeHtml(label) + '</div><div class="' + valueClass + '">' + rendered + '</div></div>';
}

// renderPipeline renders the installation pipeline as a visual step indicator.
// Input: JSON string or array of [{s: "installing", t: "2025-..."}, ...]
function renderPipeline(pipelineData) {
  var steps;
  try {
    steps = typeof pipelineData === 'string' ? JSON.parse(pipelineData) : pipelineData;
  } catch (e) {
    return '<div class="pipeline-empty">Pipeline data unavailable</div>';
  }
  if (!Array.isArray(steps) || steps.length === 0) {
    return '<div class="pipeline-empty">No pipeline data</div>';
  }

  // Status display labels
  var statusLabels = {
    'installing': 'Installing',
    'validation': 'Validation',
    'configuring': 'Configuring',
    'success': 'Completed',
    'failed': 'Failed',
    'aborted': 'Aborted'
  };

  // Terminal statuses
  var terminalStatuses = { 'success': true, 'failed': true, 'aborted': true };

  var html = '<div class="pipeline-track">';
  for (var i = 0; i < steps.length; i++) {
    var step = steps[i];
    var status = step.s || 'unknown';
    var label = statusLabels[status] || status;
    var isTerminal = terminalStatuses[status] || false;
    var isLast = (i === steps.length - 1);

    // Determine step state for styling
    var stepClass = 'pipeline-step';
    if (status === 'success') {
      stepClass += ' step-success';
    } else if (status === 'failed') {
      stepClass += ' step-failed';
    } else if (status === 'aborted') {
      stepClass += ' step-aborted';
    } else if (!isLast || !isTerminal) {
      // Non-terminal step that was reached = passed
      stepClass += ' step-ok';
    }

    // Format timestamp
    var timeStr = '';
    if (step.t) {
      try {
        var d = new Date(step.t);
        timeStr = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
      } catch (e) { timeStr = ''; }
    }

    html += '<div class="' + stepClass + '">';
    html += '<div class="step-icon">';
    if (status === 'success') {
      html += '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="20 6 9 17 4 12"/></svg>';
    } else if (status === 'failed') {
      html += '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    } else if (status === 'aborted') {
      html += '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/></svg>';
    } else {
      html += '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><polyline points="20 6 9 17 4 12"/></svg>';
    }
    html += '</div>';
    html += '<div class="step-label">' + escapeHtml(label) + '</div>';
    if (timeStr) {
      html += '<div class="step-time">' + escapeHtml(timeStr) + '</div>';
    }
    html += '</div>';

    // Arrow between steps
    if (!isLast) {
      html += '<div class="pipeline-arrow"><svg width="20" height="14" viewBox="0 0 20 14"><path d="M0 7h16M12 2l5 5-5 5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg></div>';
    }
  }
  html += '</div>';
  return html;
}

function formatType(type) {
  if (!type) return null;
  const types = {
    'lxc': 'LXC Container',
    'vm': 'Virtual Machine',
    'addon': 'Add-on',
    'pve': 'Proxmox VE',
    'tool': 'Tool'
  };
  return types[type.toLowerCase()] || type;
}

function formatPassthrough(pt) {
  if (!pt) return null;
  const modes = {
    'igpu': 'Integrated GPU',
    'dgpu': 'Dedicated GPU',
    'vgpu': 'Virtual GPU',
    'none': 'None',
    'unknown': 'Unknown'
  };
  return modes[pt.toLowerCase()] || pt;
}

function formatBytes(bytes) {
  if (!bytes) return null;
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return gb.toFixed(1) + ' GB';
  const mb = bytes / (1024 * 1024);
  return mb.toFixed(0) + ' MB';
}

function formatDuration(seconds) {
  if (!seconds) return null;
  if (seconds < 60) return seconds + 's';
  const mins = Math.floor(seconds / 60);
  const secs = seconds % 60;
  if (mins < 60) return mins + 'm ' + secs + 's';
  const hours = Math.floor(mins / 60);
  const remainMins = mins % 60;
  return hours + 'h ' + remainMins + 'm';
}

function formatFullTimestamp(ts) {
  if (!ts) return null;
  const d = new Date(ts);
  return d.toLocaleDateString() + ' ' + d.toLocaleTimeString();
}

function closeModal() {
  const modal = document.getElementById('detailModal');
  modal.classList.remove('active');
  document.body.style.overflow = '';
}

function closeModalOutside(event) {
  if (event.target === document.getElementById('detailModal')) {
    closeModal();
  }
}

// Close modal with Escape key
document.addEventListener('keydown', function(e) {
  if (e.key === 'Escape') {
    closeModal();
    closeHealthModal();
  }
});

function filterTable() {
  currentPage = 1;
  fetchPaginatedRecords();
}

function exportCSV() {
  if (allRecords.length === 0) {
    alert('No data to export');
    return;
  }

  const headers = ['App', 'Status', 'Platform', 'Repository', 'OS Type', 'OS Version', 'Type', 'Method', 'Cores', 'RAM (MB)', 'Disk (GB)', 'Exit Code', 'Duration (s)', 'Error', 'PVE Version'];
  const rows = allRecords.map(r => [
    r.nsapp || '',
    r.status || '',
    r.platform || '',
    r.repo_slug || r.repo_source || '',
    r.os_type || '',
    r.os_version || '',
    r.type || '',
    r.method || '',
    r.core_count || '',
    r.ram_size || '',
    r.disk_size || '',
    r.exit_code || '',
    r.install_duration || '',
    (r.error || '').replace(/,/g, ';'),
    r.pve_version || ''
  ]);

  const csv = [headers.join(','), ...rows.map(r => r.join(','))].join('\\n');
  const blob = new Blob([csv], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'telemetry_' + new Date().toISOString().slice(0, 10) + '.csv';
  a.click();
  URL.revokeObjectURL(url);
}

async function showHealthCheck() {
  const modal = document.getElementById('healthModal');
  const body = document.getElementById('healthModalBody');
  body.innerHTML = '<div class="loading">Checking...</div>';
  modal.classList.add('active');
  document.body.style.overflow = 'hidden';

  try {
    const resp = await fetch('/healthz');
    const data = await resp.json();

    const isOk = data.status === 'ok';
    const statusClass = isOk ? 'ok' : 'error';
    const icon = isOk ? '✅' : '❌';
    const title = isOk ? 'All Systems Operational' : 'Service Degraded';

    let html = '<div class="health-status ' + statusClass + '">';
    html += '<span class="icon">' + icon + '</span>';
    html += '<div class="details">';
    html += '<div class="title">' + title + '</div>';
    html += '<div class="subtitle">Last checked: ' + new Date().toLocaleTimeString() + '</div>';
    html += '</div></div>';

    html += '<div class="health-info">';
    html += '<div><span>Status</span><span>' + data.status + '</span></div>';
    html += '<div><span>Server Time</span><span>' + new Date(data.time).toLocaleString() + '</span></div>';
    if (data.pocketbase) {
      html += '<div><span>PocketBase</span><span>' + (data.pocketbase === 'connected' ? '🟢 Connected' : '🔴 ' + data.pocketbase) + '</span></div>';
    }
    if (data.version) {
      html += '<div><span>Version</span><span>' + data.version + '</span></div>';
    }
    html += '</div>';

    body.innerHTML = html;
  } catch (e) {
    body.innerHTML = '<div class="health-status error"><span class="icon">❌</span><div class="details"><div class="title">Connection Failed</div><div class="subtitle">' + e.message + '</div></div></div>';
  }
}

function closeHealthModal(event) {
  if (event && event.target !== document.getElementById('healthModal')) return;
  document.getElementById('healthModal').classList.remove('active');
  document.body.style.overflow = '';
}

async function refreshData() {
  try {
    await loadRepoSlugOptions();
    const data = await fetchData();
    updateStats(data);
    updateCharts(data);
    // Refresh paginated Installation Log with current filters (NOT from cached recent_records)
    currentPage = 1;
    fetchPaginatedRecords();
  } catch (e) {
    console.error(e);
  }
}

// Initial load
refreshData();
initSortableHeaders();

// Source button clicks
document.querySelectorAll('.source-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.source-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    const slugSelect = document.getElementById('filterSlug');
    if (slugSelect) slugSelect.value = '';
    refreshData();
  });
});

// Quickfilter button clicks
document.querySelectorAll('.filter-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    refreshData();
  });
});

// Platform filter button clicks (pve / incus / all)
document.querySelectorAll('.platform-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    document.querySelectorAll('.platform-btn').forEach(b => b.classList.remove('active'));
    this.classList.add('active');
    // Keep the table-local platform select in sync with the global chip
    const sel = document.getElementById('filterPlatform');
    if (sel) sel.value = this.dataset.platform || '';
    refreshData();
  });
});

// Auto-refresh functionality
function toggleAutoRefresh() {
  autoRefreshEnabled = document.getElementById('autoRefreshToggle').checked;
  localStorage.setItem('autoRefresh', autoRefreshEnabled);

  const intervalDisplay = document.getElementById('refreshInterval');

  if (autoRefreshEnabled) {
    intervalDisplay.classList.add('active');
    startAutoRefresh();
  } else {
    intervalDisplay.classList.remove('active');
    stopAutoRefresh();
  }
}

function startAutoRefresh() {
  stopAutoRefresh(); // Clear any existing timer

  let countdown = autoRefreshInterval / 1000;
  const intervalDisplay = document.getElementById('refreshInterval');

  // Update countdown display
  const countdownTimer = setInterval(() => {
    countdown--;
    if (countdown <= 0) {
      countdown = autoRefreshInterval / 1000;
    }
    intervalDisplay.textContent = countdown + 's';
  }, 1000);

  // Actual refresh
  autoRefreshTimer = setInterval(() => {
    refreshData();
    countdown = autoRefreshInterval / 1000;
  }, autoRefreshInterval);

  // Store countdown timer for cleanup
  autoRefreshTimer.countdownTimer = countdownTimer;
}

function stopAutoRefresh() {
  if (autoRefreshTimer) {
    clearInterval(autoRefreshTimer);
    if (autoRefreshTimer.countdownTimer) {
      clearInterval(autoRefreshTimer.countdownTimer);
    }
    autoRefreshTimer = null;
  }
  document.getElementById('refreshInterval').textContent = '15s';
}


// Initialize auto-refresh state on load
document.getElementById('autoRefreshToggle').checked = autoRefreshEnabled;
if (autoRefreshEnabled) {
  document.getElementById('refreshInterval').classList.add('active');
  startAutoRefresh();
}

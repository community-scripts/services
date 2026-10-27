(function () {
  'use strict';
  const R = window.RunUI;
  const esc = R.esc;
  const $ = function (id) { return document.getElementById(id); };

  const state = {
    days: '1', repo: 'all', platform: '', slug: '',
    page: 1, limit: 25, sort: '-created', search: '', status: '', type: '', os: '',
  };
  let records = [];
  let totalPages = 1;
  let timer = null;

  function readURL() {
    const q = new URLSearchParams(location.search);
    ['days', 'repo', 'platform', 'slug', 'search', 'status', 'type', 'os', 'sort'].forEach(function (k) {
      if (q.has(k)) state[k] = q.get(k);
    });
  }
  function writeURL() {
    const q = new URLSearchParams();
    const defaults = { days: '1', repo: 'all', sort: '-created' };
    ['days', 'repo', 'platform', 'slug', 'search', 'status', 'type', 'os', 'sort'].forEach(function (k) {
      if (state[k] && state[k] !== defaults[k]) q.set(k, state[k]);
    });
    const s = q.toString();
    history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
  }
  function filterQuery() {
    const q = new URLSearchParams({ days: state.days, repo: state.repo });
    if (state.platform) q.set('platform', state.platform);
    if (state.slug) q.set('slug', state.slug);
    return q;
  }

  function syncControls() {
    document.querySelectorAll('.seg[data-filter]').forEach(function (seg) {
      const key = seg.getAttribute('data-filter');
      seg.querySelectorAll('button').forEach(function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-value') === state[key]));
      });
    });
    $('logSearch').value = state.search;
    $('logStatus').value = state.status;
    $('logType').value = state.type;
    $('logLimit').value = String(state.limit);
    document.querySelectorAll('.th-sort').forEach(function (b) {
      const f = b.getAttribute('data-sort');
      const dir = state.sort === f ? 'ascending' : state.sort === '-' + f ? 'descending' : '';
      if (dir) b.setAttribute('aria-sort', dir); else b.removeAttribute('aria-sort');
    });
  }

  function showError(msg) {
    const b = $('errorBanner');
    b.textContent = msg;
    b.hidden = !msg;
  }

  function card(label, value, sub, cls) {
    return '<div class="card ' + (cls || '') + '"><div class="card-label">' + esc(label) + '</div>' +
      '<div class="card-value">' + value + '</div><div class="card-sub">' + sub + '</div></div>';
  }

  function renderCards(d) {
    const s = d.summary || {};
    const finished = (s.success || 0) + (s.failed || 0);
    const popular = (d.apps || []).slice(0, 3).map(function (a, i) {
      return '<li><span class="rank">' + (i + 1) + '</span><span class="name">' + esc(a.label) + '</span><span class="n">' + R.num(a.runs) + '</span></li>';
    }).join('');
    $('cards').innerHTML =
      card('Runs', R.num(s.runs), R.num(d.total_all_time) + ' all time, ' + R.num(s.in_progress) + ' still running') +
      card('Success rate', finished ? R.pct(s.success_rate) : 'No data', R.num(s.success) + ' of ' + R.num(finished) + ' finished runs', 'ok') +
      card('Failed', R.num(s.failed), finished ? R.pct(s.failure_rate) + ' of finished runs' : 'no finished runs', 'bad') +
      card('Aborted', R.num(s.aborted), 'cancelled or declined', 'abort') +
      card('No result', R.num(s.no_result), 'stopped reporting before a result', 'silent') +
      '<div class="card popular"><div class="card-label">Most popular</div>' +
      (popular ? '<ol class="podium">' + popular + '</ol>' : '<div class="card-sub">No runs in this period</div>') + '</div>';
  }

  function errorsURL(extra) {
    const q = filterQuery();
    Object.keys(extra || {}).forEach(function (k) { q.set(k, extra[k]); });
    return '/error-analysis?' + q.toString();
  }

  function renderCauses(causes) {
    if (!causes || !causes.length) {
      $('topCauses').innerHTML = '<p class="empty">No failed runs in this period.</p>';
      return;
    }
    $('topCauses').innerHTML = '<table class="compact"><thead><tr><th scope="col">Cause</th><th scope="col" class="num">Failures</th>' +
      '<th scope="col" class="num">Apps</th><th scope="col" class="num">Setups</th><th scope="col" class="num">Last 24 h</th></tr></thead><tbody>' +
      causes.map(function (c) {
        return '<tr><td class="cause-cell"><a href="' + esc(errorsURL({ cause: c.key })) + '" class="cause-link' + (c.kind === 'command' ? ' is-code' : '') + '" title="' +
          esc(c.message) + '">' + esc(c.message) + '</a><div class="cause-meta">' + esc((c.top_apps || []).join(', ')) + '</div></td>' +
          '<td class="num strong bad">' + R.num(c.failures) + '</td><td class="num">' + R.num(c.apps) + '</td>' +
          '<td class="num">' + R.num(c.setups) + '</td><td class="num">' + R.trend(c) + '</td></tr>';
      }).join('') + '</tbody></table>';
  }

  function renderFailing(apps, minRuns) {
    $('errorsNote').textContent = 'Failure rates count apps with at least ' + minRuns + ' finished runs in this period.';
    if (!apps || !apps.length) {
      $('failingApps').innerHTML = '<p class="empty">No app with ' + minRuns + ' or more finished runs failed in this period.</p>';
      return;
    }
    $('failingApps').innerHTML = '<table class="compact"><thead><tr><th scope="col">App</th><th scope="col" class="num">Finished</th>' +
      '<th scope="col" class="num">Failed</th><th scope="col" class="num">Rate</th><th scope="col" class="num">Setups</th><th scope="col">Now</th></tr></thead><tbody>' +
      apps.map(function (a) {
        const sev = a.failure_rate >= 30 ? 'bad' : a.failure_rate >= 15 ? 'warn' : '';
        const now = a.failing_now
          ? '<span class="chip chip-failed" title="Last failure ' + esc(R.dateTime(a.last_failure)) + ' is newer than the last success">Failing</span>'
          : '<span class="chip chip-success" title="Last success ' + esc(R.dateTime(a.last_success)) + '">Recovered</span>';
        return '<tr><td><a href="' + esc(errorsURL({ app: a.app })) + '">' + esc(a.app) + '</a> ' + R.typeBadge(a.type) + '</td>' +
          '<td class="num">' + R.num(a.success + a.failed) + '</td><td class="num bad">' + R.num(a.failed) + '</td>' +
          '<td class="num strong ' + sev + '">' + R.pct(a.failure_rate) + '</td><td class="num" title="Distinct hardware and host setups among the failures">' +
          R.num(a.setups_failed) + '</td><td>' + now + '</td></tr>';
      }).join('') + '</tbody></table>';
  }

  function rateCell(r) {
    const finished = r.success + r.failed;
    if (!finished) return '<td class="num muted">none finished</td>';
    const sev = r.failure_rate >= 30 ? 'bad' : r.failure_rate >= 15 ? 'warn' : '';
    return '<td class="num ' + sev + '">' + R.pct(r.failure_rate) + '</td>';
  }

  function renderTopApps(apps) {
    if (!apps || !apps.length) {
      $('topApps').innerHTML = '<p class="empty">No runs in this period.</p>';
      return;
    }
    $('topApps').innerHTML = '<table class="compact"><thead><tr><th scope="col" class="num">Rank</th><th scope="col">App</th><th scope="col" class="num">Runs</th>' +
      '<th scope="col" class="num">Failure rate</th></tr></thead><tbody>' + apps.map(function (a, i) {
        return '<tr><td class="num muted">' + (i + 1) + '</td><td><a href="/script-analysis?app=' + encodeURIComponent(a.label) + '">' + esc(a.label) +
          '</a></td><td class="num">' + R.num(a.runs) + '</td>' + rateCell(a) + '</tr>';
      }).join('') + '</tbody></table>';
  }

  function renderBreakdown(b) {
    b = b || {};
    const names = { platform: 'Platform', type: 'Type', os: 'OS' };
    const label = function (dim, v) {
      if (dim === 'platform') return R.platformName(v);
      if (dim === 'type') return R.typeName(v);
      return v;
    };
    $('breakdown').innerHTML = ['platform', 'type', 'os'].map(function (dim) {
      const rows = b[dim] || [];
      const total = rows.reduce(function (a, r) { return a + r.runs; }, 0) || 1;
      return '<table class="compact"><thead><tr><th scope="col">' + names[dim] + '</th>' +
        '<th scope="col" class="num">Runs</th><th scope="col" class="num">Share</th><th scope="col" class="num">Failure rate</th></tr></thead><tbody>' +
        (rows.length ? rows.map(function (r) {
          return '<tr><td>' + esc(label(dim, r.label)) + '</td><td class="num">' + R.num(r.runs) + '</td><td class="num"><span class="share"><span style="width:' +
            (r.runs / total * 100).toFixed(1) + '%"></span></span>' + Math.round(r.runs / total * 100) + '%</td>' + rateCell(r) + '</tr>';
        }).join('') : '<tr><td colspan="4" class="empty">No runs</td></tr>') + '</tbody></table>';
    }).join('');
  }

  function renderDaily(d) {
    const show = Number(state.days) > 1 && (d.daily_stats || []).length > 1;
    $('dailySection').hidden = !show;
    if (!show) return;
    $('daily').innerHTML = R.columnsSVG(d.daily_stats, [
      { key: 'success', cls: 'c-ok', name: 'successful' },
      { key: 'failed', cls: 'c-bad', name: 'failed' },
    ], { title: 'Runs per day', label: function (p) { return p.date.slice(5); } }) +
      '<div class="legend"><span class="key c-ok"></span>Success <span class="key c-bad"></span>Failed</div>';
  }

  async function loadSlugs() {
    try {
      const data = await R.getJSON('/api/repo-slugs?days=' + encodeURIComponent(state.days) + '&repo=' + encodeURIComponent(state.repo));
      const slugs = data.repo_slugs || [];
      $('fSlug').innerHTML = '<option value="">All repositories</option>' + slugs.map(function (s) {
        return '<option value="' + esc(s.slug) + '">' + esc(s.slug) + ' (' + R.num(s.count) + ')</option>';
      }).join('');
      $('fSlug').value = slugs.some(function (s) { return s.slug === state.slug; }) ? state.slug : '';
    } catch (e) {
      $('fSlug').innerHTML = '<option value="">All repositories</option>';
    }
  }

  async function loadDashboard() {
    const q = filterQuery();
    const d = await R.getJSON('/api/dashboard?' + q.toString());
    renderCards(d);
    renderCauses(d.top_causes);
    renderFailing(d.failing_apps, d.min_runs);
    renderTopApps(d.apps);
    renderBreakdown(d.breakdown);
    renderDaily(d);
    $('errorsLink').href = errorsURL();
    const os = new Set();
    (d.os_distribution || []).forEach(function (o) { if (o.os) os.add(o.os); });
    $('logOS').innerHTML = '<option value="">All OS</option>' + Array.from(os).sort().map(function (o) {
      return '<option value="' + esc(o) + '">' + esc(o) + '</option>';
    }).join('');
    $('logOS').value = state.os;
  }

  function causeLine(r) {
    if (r.status === 'failed' || r.status === 'aborted') {
      const h = R.headline(r);
      return '<div class="cause-line' + (r.status === 'aborted' ? ' is-abort' : '') + '" title="' + esc(h) + '">' +
        (r.exit_code ? '<span class="exit">' + r.exit_code + '</span>' : '') + esc(h) + '</div>';
    }
    if (r.status === 'unknown') {
      return '<div class="cause-line is-silent">Stopped reporting after ' + esc(R.stageName(r.last_stage || 'installing').toLowerCase()) + '</div>';
    }
    return '';
  }

  function logRows(list) {
    if (!list.length) return '<tr><td colspan="8" class="empty">No runs match these filters.</td></tr>';
    return list.map(function (r, i) {
      const plat = r.platform ? '<span class="platform-badge ' + esc(r.platform) + '">' + esc(r.platform === 'pve' ? 'PVE' : 'Incus') + '</span>' : '<span class="muted">unknown</span>';
      return '<tr class="run-row status-' + esc(r.status) + '" data-run="' + i + '">' +
        '<td>' + R.statusChip(r) + '</td>' +
        '<td class="app-cell"><button type="button" class="run-open" data-run="' + i + '">' + esc(r.nsapp || 'unknown') + '</button> ' +
        R.typeBadge(r.type) + causeLine(r) + '</td>' +
        '<td>' + plat + '</td>' +
        '<td class="repo-cell" title="' + esc(r.repo_slug || r.repo_source || '') + '">' + esc(r.repo_slug || r.repo_source || '') + '</td>' +
        '<td class="os-cell">' + (esc(R.osName(r)) || '<span class="muted">unknown</span>') + '</td>' +
        '<td class="nowrap">' + (esc(R.resources(r)) || '<span class="muted">unknown</span>') + '</td>' +
        '<td class="nowrap" title="' + esc(r.started_at) + '">' + esc(R.dateTime(r.started_at)) + '</td>' +
        '<td class="num nowrap">' + (r.status === 'in_progress' ? '<span class="muted">' + esc(R.duration(r.duration)) + ' so far</span>' : esc(R.duration(r.duration))) + '</td></tr>';
    }).join('');
  }

  async function loadLog() {
    const q = filterQuery();
    q.set('page', state.page);
    q.set('limit', state.limit);
    q.set('sort', state.sort);
    if (state.search) q.set('search', state.search);
    if (state.status) q.set('status', state.status);
    q.set('type', state.type || 'all');
    if (state.os) q.set('os', state.os);
    try {
      const data = await R.getJSON('/api/records?' + q.toString());
      records = data.records || [];
      totalPages = Math.max(1, data.total_pages || 1);
      $('logBody').innerHTML = logRows(records);
      $('logInfo').textContent = 'Page ' + state.page + ' of ' + totalPages + ', ' + R.runs(data.total);
      $('logPrev').disabled = state.page <= 1;
      $('logNext').disabled = state.page >= totalPages;
    } catch (e) {
      $('logBody').innerHTML = '<tr><td colspan="8" class="empty">Could not load the log: ' + esc(e.message) + '</td></tr>';
    }
  }

  async function refresh() {
    writeURL();
    syncControls();
    $('refreshBtn').disabled = true;
    try {
      await loadSlugs();
      await Promise.all([loadDashboard(), loadLog()]);
      showError('');
      $('updated').textContent = 'Updated ' + new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    } catch (e) {
      showError('Could not load the dashboard: ' + e.message);
    } finally {
      $('refreshBtn').disabled = false;
    }
  }

  function exportCSV() {
    const cols = ['started_at', 'nsapp', 'type', 'status', 'exit_code', 'error_message', 'platform', 'pve_version', 'os_type', 'os_version',
      'core_count', 'ram_size', 'disk_size', 'duration', 'repo_slug', 'execution_id'];
    const cell = function (v) {
      const s = String(v == null ? '' : v);
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
    };
    const csv = [cols.join(',')].concat(records.map(function (r) {
      return cols.map(function (c) { return cell(c === 'error_message' ? R.headline(r) : r[c]); }).join(',');
    })).join('\n');
    R.download('telemetry-runs-' + new Date().toISOString().slice(0, 10) + '.csv', csv, 'text/csv');
  }

  function setAutoRefresh(on) {
    try { localStorage.setItem('autoRefresh', String(on)); } catch (e) { /* storage unavailable */ }
    if (timer) clearInterval(timer);
    timer = on ? setInterval(refresh, 30000) : null;
  }

  let searchTimer = null;
  function bind() {
    document.addEventListener('click', function (e) {
      const t = e.target;
      if (!t || !t.closest) return;
      const segBtn = t.closest('.seg[data-filter] button');
      if (segBtn) {
        const key = segBtn.closest('.seg').getAttribute('data-filter');
        state[key] = segBtn.getAttribute('data-value');
        if (key === 'repo') state.slug = '';
        state.page = 1;
        refresh();
        return;
      }
      const sortBtn = t.closest('.th-sort');
      if (sortBtn) {
        const f = sortBtn.getAttribute('data-sort');
        state.sort = state.sort === '-' + f ? f : '-' + f;
        state.page = 1;
        writeURL();
        syncControls();
        loadLog();
        return;
      }
      const row = t.closest('[data-run]');
      if (row && !t.closest('a')) {
        const rec = records[Number(row.getAttribute('data-run'))];
        if (rec) R.openRecord(rec);
      }
    });
    $('fSlug').addEventListener('change', function () { state.slug = this.value; state.page = 1; refresh(); });
    $('refreshBtn').addEventListener('click', refresh);
    $('csvBtn').addEventListener('click', exportCSV);
    $('logSearch').addEventListener('input', function () {
      const v = this.value.trim();
      clearTimeout(searchTimer);
      searchTimer = setTimeout(function () { state.search = v; state.page = 1; writeURL(); loadLog(); }, 300);
    });
    [['logStatus', 'status'], ['logType', 'type'], ['logOS', 'os']].forEach(function (p) {
      $(p[0]).addEventListener('change', function () { state[p[1]] = this.value; state.page = 1; writeURL(); loadLog(); });
    });
    $('logLimit').addEventListener('change', function () { state.limit = Number(this.value); state.page = 1; loadLog(); });
    $('logPrev').addEventListener('click', function () { if (state.page > 1) { state.page--; loadLog(); } });
    $('logNext').addEventListener('click', function () { if (state.page < totalPages) { state.page++; loadLog(); } });
    $('autoRefresh').addEventListener('change', function () { setAutoRefresh(this.checked); });
  }

  function init() {
    let auto = false;
    try { auto = localStorage.getItem('autoRefresh') === 'true'; } catch (e) { /* storage unavailable */ }
    R.initChrome();
    readURL();
    bind();
    $('autoRefresh').checked = auto;
    setAutoRefresh(auto);
    return refresh();
  }

  window.Dashboard = { init: init, state: state, records: function () { return records; } };
  if (!window.__NO_AUTOSTART__) init();
})();

(function () {
  'use strict';
  const R = window.RunUI;
  const esc = R.esc;
  const $ = function (id) { return document.getElementById(id); };

  const KEYS = ['days', 'repo', 'platform', 'slug', 'type', 'search', 'csort', 'asort', 'cause', 'app', 'exit_code'];
  const DEFAULTS = { days: '7', repo: 'all', platform: '', slug: '', type: '', search: '', csort: '-failures', asort: '-failed' };
  const state = Object.assign({}, DEFAULTS);
  let open = null;
  let data = null;
  let drill = { key: '', page: 1, records: [], total: 0, loaded: false };

  function readURL() {
    const q = new URLSearchParams(location.search);
    KEYS.forEach(function (k) { if (q.has(k)) state[k] = q.get(k); });
    if (q.get('cause')) open = { kind: 'cause', value: q.get('cause') };
    else if (q.get('app')) open = { kind: 'app', value: q.get('app') };
    else if (q.get('exit_code')) open = { kind: 'exit', value: q.get('exit_code') };
  }
  function writeURL() {
    const q = new URLSearchParams();
    Object.keys(DEFAULTS).forEach(function (k) { if (state[k] && state[k] !== DEFAULTS[k]) q.set(k, state[k]); });
    if (open) q.set(open.kind === 'exit' ? 'exit_code' : open.kind, open.value);
    const s = q.toString();
    history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
  }
  function filterQuery() {
    const q = new URLSearchParams({ days: state.days, repo: state.repo });
    if (state.platform) q.set('platform', state.platform);
    if (state.slug) q.set('slug', state.slug);
    if (state.type) q.set('type', state.type);
    return q;
  }
  function period() { return state.days === '1' ? 'today' : 'in the last ' + state.days + ' days'; }

  function syncControls() {
    document.querySelectorAll('.seg[data-filter]').forEach(function (seg) {
      const key = seg.getAttribute('data-filter');
      seg.querySelectorAll('button').forEach(function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-value') === state[key]));
      });
    });
    $('fType').value = state.type;
    $('fSearch').value = state.search;
    document.querySelectorAll('[data-csort]').forEach(function (b) { sortMark(b, b.getAttribute('data-csort'), state.csort); });
    document.querySelectorAll('[data-asort]').forEach(function (b) { sortMark(b, b.getAttribute('data-asort'), state.asort); });
    $('apiLink').href = '/api/errors?' + filterQuery().toString();
    $('apiLink').textContent = '/api/errors?' + filterQuery().toString();
  }
  function sortMark(b, field, sort) {
    const dir = sort === field ? 'ascending' : sort === '-' + field ? 'descending' : '';
    if (dir) b.setAttribute('aria-sort', dir); else b.removeAttribute('aria-sort');
  }

  function sorted(list, sort) {
    const desc = sort.charAt(0) === '-';
    const key = desc ? sort.slice(1) : sort;
    return list.slice().sort(function (a, b) {
      const x = a[key], y = b[key];
      const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x || '').localeCompare(String(y || ''));
      return desc ? -c : c;
    });
  }
  function matches(text) {
    const s = state.search.trim().toLowerCase();
    return !s || text.toLowerCase().indexOf(s) !== -1;
  }

  function card(label, value, sub, cls) {
    return '<div class="card ' + (cls || '') + '"><div class="card-label">' + esc(label) + '</div><div class="card-value">' + value +
      '</div><div class="card-sub">' + sub + '</div></div>';
  }
  function renderSummary(s) {
    const finished = s.success + s.failed;
    $('summary').innerHTML =
      card('Failed runs', R.num(s.failed), 'of ' + R.num(finished) + ' finished runs', 'bad') +
      card('Failure rate', finished ? R.pct(s.failure_rate) : 'No data', 'failed divided by finished runs', 'bad') +
      card('No result', R.num(s.no_result), 'stopped reporting before a result', 'silent') +
      card('Still running', R.num(s.in_progress), 'no result yet', '') +
      card('Apps failing', R.num(s.apps_failing), 'with at least one failed run', '');
  }

  function renderStuck(st) {
    const parts = [];
    if (st.running) parts.push('<p>' + R.runs(st.running) + (st.running === 1 ? ' is' : ' are') + ' still running: the last report came in under ' + st.after_hours + ' hours ago.</p>');
    if (st.stalled) {
      parts.push('<p>' + R.runs(st.stalled) + ' stopped reporting more than ' + st.after_hours +
        ' hours ago. The next cleanup marks them as no result. <button type="button" class="btn btn-xs" id="cleanupOpen">Run the cleanup now</button></p>');
    }
    if (st.no_result) parts.push('<p>' + R.runs(st.no_result) + ' that started in the last 24 hours ended without a result.</p>');
    $('stuckBanner').innerHTML = parts.join('');
    $('stuckBanner').hidden = !parts.length;
  }

  function isOpen(kind, value) { return open && open.kind === kind && String(open.value) === String(value); }
  function drillRow(cols) { return '<tr class="drill-row"><td colspan="' + cols + '"><div class="drill" id="drillBox" aria-live="polite">Loading runs</div></td></tr>'; }

  function causeRow(c, i) {
    const meta = [];
    if (c.category) meta.push('<span class="cat">' + esc(c.category) + '</span>');
    if (c.exit_code) meta.push('<span class="exit" title="' + esc(c.exit_description) + '">exit ' + c.exit_code + '</span>');
    if (c.top_apps && c.top_apps.length) meta.push(esc(c.top_apps.join(', ')));
    const example = c.example_message ? '<div class="cause-example">Example: ' + esc(c.example_message) + '</div>' : '';
    return '<tr class="open-row' + (isOpen('cause', c.key) ? ' is-open' : '') + '" data-open="cause" data-idx="' + i + '">' +
      '<td class="cause-cell"><button type="button" class="row-toggle' + (c.kind === 'command' ? ' is-code' : '') + '" aria-expanded="' + isOpen('cause', c.key) +
      '" title="' + esc(c.message) + '">' + esc(c.message) + '</button><div class="cause-meta">' + meta.join(' ') + '</div>' + example + '</td>' +
      '<td class="num strong bad">' + R.num(c.failures) + '</td><td class="num">' + R.num(c.apps) + '</td><td class="num">' + R.num(c.setups) + '</td>' +
      '<td class="nowrap">' + esc(R.dateTime(c.first_seen)) + '</td><td class="nowrap" title="' + esc(R.dateTime(c.last_seen)) + '">' + esc(R.ago(c.last_seen)) + '</td>' +
      '<td class="num">' + R.trend(c) + '</td></tr>';
  }

  function renderCauses() {
    const all = (data && data.causes) || [];
    const list = sorted(all.filter(function (c) {
      return matches([c.message, c.key, c.example_message || '', (c.top_apps || []).join(' '), c.category].join(' '));
    }), state.csort);
    let html = '';
    let found = false;
    list.forEach(function (c) {
      html += causeRow(c, all.indexOf(c));
      if (isOpen('cause', c.key)) { html += drillRow(7); found = true; }
    });
    if (open && open.kind === 'cause' && !found) {
      html = '<tr class="is-open"><td colspan="7" class="cause-cell"><strong>Cause from the link:</strong> ' + esc(open.value) + '</td></tr>' + drillRow(7) + html;
    }
    $('causesBody').innerHTML = html || '<tr><td colspan="7" class="empty">' + (all.length ? 'No cause matches the filter.' : 'No failed runs ' + period() + '.') + '</td></tr>';
  }

  function renderApps() {
    const all = (data && data.apps) || [];
    $('appsNote').textContent = 'Failing now means the last failure is newer than the last success. Rates are shown for every app; the dashboard ranks only apps with at least ' +
      ((data && data.min_runs) || 0) + ' finished runs.';
    const list = sorted(all.filter(function (a) { return matches(a.app + ' ' + (a.top_cause_message || '')); }), state.asort);
    let html = '';
    let found = false;
    list.forEach(function (a) {
      const i = all.indexOf(a);
      const now = a.failing_now ? ' <span class="chip chip-failed">Failing now</span>' : '';
      html += '<tr class="open-row' + (isOpen('app', a.app) ? ' is-open' : '') + '" data-open="app" data-idx="' + i + '">' +
        '<td class="nowrap"><button type="button" class="row-toggle" aria-expanded="' + isOpen('app', a.app) + '">' + esc(a.app) + '</button> ' + R.typeBadge(a.type) + now + '</td>' +
        '<td class="num">' + R.num(a.runs) + '</td><td class="num bad strong">' + R.num(a.failed) + '</td>' +
        '<td class="num ' + (a.failure_rate >= 30 ? 'bad' : a.failure_rate >= 15 ? 'warn' : '') + '">' + R.pct(a.failure_rate) + '</td>' +
        '<td class="num">' + R.num(a.setups_failed) + '</td>' +
        '<td class="top-cause' + (a.top_cause_kind === 'command' ? ' is-code' : '') + '" title="' + esc(a.top_cause_message) + '">' + esc(a.top_cause_message || '') + '</td>' +
        '<td class="nowrap" title="' + esc(R.dateTime(a.last_failure)) + '">' + esc(R.ago(a.last_failure)) + '</td>' +
        '<td class="nowrap" title="' + esc(R.dateTime(a.last_success)) + '">' + (a.last_success ? esc(R.ago(a.last_success)) : '<span class="muted">none in period</span>') + '</td></tr>';
      if (isOpen('app', a.app)) { html += drillRow(8); found = true; }
    });
    if (open && open.kind === 'app' && !found) {
      html = '<tr class="is-open"><td colspan="8"><strong>' + esc(open.value) + '</strong> <span class="muted">has no failed runs with these filters.</span></td></tr>' + drillRow(8) + html;
    }
    $('appsBody').innerHTML = html || '<tr><td colspan="8" class="empty">' + (all.length ? 'No app matches the filter.' : 'No app failed ' + period() + '.') + '</td></tr>';
  }

  function renderExits() {
    const list = (data && data.exit_code_stats) || [];
    let html = '';
    list.forEach(function (e, i) {
      html += '<tr class="open-row' + (isOpen('exit', e.exit_code) ? ' is-open' : '') + '" data-open="exit" data-idx="' + i + '">' +
        '<td class="num"><button type="button" class="row-toggle mono" aria-expanded="' + isOpen('exit', e.exit_code) + '">' + e.exit_code + '</button></td>' +
        '<td>' + esc(e.description) + '</td><td class="num">' + R.num(e.count) + '</td><td class="num">' + R.num(e.apps) + '</td></tr>';
      if (isOpen('exit', e.exit_code)) html += drillRow(4);
    });
    $('exitBody').innerHTML = html || '<tr><td colspan="4" class="empty">No failed runs ' + period() + '.</td></tr>';
  }

  function renderChart() {
    const pts = ((data && data.failures_over_time) || []).map(function (p) {
      return { t: p.t, failed: p.failed, other: Math.max(0, p.runs - p.failed), runs: p.runs };
    });
    const hourly = data && data.bucket === 'hour';
    $('chartTitle').textContent = hourly ? 'Failures per hour' : 'Failures per day';
    if (!pts.some(function (p) { return p.runs; })) {
      $('failChart').innerHTML = '<p class="empty">No runs ' + period() + '.</p>';
      return;
    }
    $('failChart').innerHTML = R.columnsSVG(pts, [
      { key: 'failed', cls: 'c-bad', name: 'failed' },
      { key: 'other', cls: 'c-runs', name: 'other runs' },
    ], { title: 'Failed runs and other runs per ' + (hourly ? 'hour' : 'day'), label: function (p) { return hourly ? p.t.slice(11, 16) : p.t.slice(5); } }) +
      '<div class="legend"><span class="key c-bad"></span>Failed <span class="key c-runs"></span>Other runs</div>';
  }

  function renderAll() {
    renderCauses();
    renderApps();
    renderExits();
    if (!open) return;
    const key = open.kind + ':' + open.value;
    if (drill.key === key && drill.loaded) {
      renderDrill();
    } else {
      drill = { key: key, page: 1, records: [], total: 0, loaded: false };
      loadDrill();
    }
  }

  function openItem() {
    if (!open || !data) return null;
    if (open.kind === 'cause') return (data.causes || []).find(function (c) { return c.key === open.value; }) || { key: open.value, message: open.value };
    if (open.kind === 'app') return (data.apps || []).find(function (a) { return a.app === open.value; }) || { app: open.value };
    return (data.exit_code_stats || []).find(function (e) { return String(e.exit_code) === String(open.value); }) || { exit_code: Number(open.value) };
  }

  function drillQuery() {
    const q = filterQuery();
    q.set('type', state.type || 'install');
    q.set('status', 'failed');
    if (open.kind === 'cause') q.set('cause', open.value);
    if (open.kind === 'app') q.set('app', open.value);
    if (open.kind === 'exit') q.set('exit_code', open.value);
    return q;
  }

  function drillTitle(item) {
    if (open.kind === 'cause') return 'Failed runs with this cause';
    if (open.kind === 'app') return 'Failed runs of ' + item.app;
    return 'Failed runs with exit code ' + open.value;
  }

  async function loadDrill() {
    const box = $('drillBox');
    if (!box) return;
    const q = drillQuery();
    q.set('page', drill.page);
    q.set('limit', 20);
    try {
      const res = await R.getJSON('/api/records?' + q.toString());
      drill.records = drill.page === 1 ? (res.records || []) : drill.records.concat(res.records || []);
      drill.total = res.total || 0;
      drill.loaded = true;
      renderDrill();
    } catch (e) {
      box.innerHTML = '<p class="empty">Could not load the runs: ' + esc(e.message) + '</p>';
    }
  }

  function renderDrill() {
    const box = $('drillBox');
    if (!box) return;
    const item = openItem();
    const apiQ = drillQuery();
    let html = '<div class="drill-head"><div><h3>' + esc(drillTitle(item)) + '</h3><p class="muted">' + R.runs(drill.total) + ' ' + esc(period()) + '</p></div>' +
      '<div class="rd-actions"><button type="button" class="btn" data-drill="markdown">Copy as Markdown</button>' +
      '<a class="btn" href="/api/records?' + esc(apiQ.toString()) + '" target="_blank" rel="noopener">Runs as JSON</a>' +
      '<button type="button" class="btn" data-drill="issue">Create GitHub issue</button>' +
      '<button type="button" class="btn" data-drill="close">Close</button></div></div>';
    if (!drill.records.length) {
      box.innerHTML = html + '<p class="empty">No failed run matches.</p>';
      return;
    }
    html += '<div class="table-wrapper"><table class="compact drill-table"><thead><tr><th scope="col">Started</th><th scope="col">App</th>' +
      (open.kind === 'cause' ? '' : '<th scope="col">Message</th>') +
      '<th scope="col">Step</th><th scope="col">OS</th><th scope="col">Host</th><th scope="col">Setup</th><th scope="col" class="num">Duration</th></tr></thead><tbody>' +
      drill.records.map(function (r, i) {
        return '<tr class="run-row" data-drun="' + i + '"><td class="nowrap"><button type="button" class="run-open" data-drun="' + i + '">' + esc(R.dateTime(r.started_at)) + '</button></td>' +
          '<td class="nowrap">' + esc(r.nsapp) + ' ' + R.typeBadge(r.type) + '</td>' +
          (open.kind === 'cause' ? '' : '<td class="top-cause" title="' + esc(R.headline(r)) + '">' + esc(R.headline(r)) + '</td>') +
          '<td>' + esc(r.error_step || '') + (r.error_phase ? ' <span class="muted">(' + esc(r.error_phase) + ')</span>' : '') + '</td>' +
          '<td class="nowrap">' + esc(R.osName(r)) + '</td><td class="nowrap">' + esc([R.hostName(r), r.kernel_version].filter(Boolean).join(', ')) + '</td>' +
          '<td class="setup" title="' + esc(R.setupName(r)) + '">' + esc(R.setupName(r)) + '</td><td class="num">' + esc(R.duration(r.duration)) + '</td></tr>';
      }).join('') + '</tbody></table></div>';
    if (drill.records.length < drill.total) {
      html += '<div class="drill-more"><button type="button" class="btn" data-drill="more">Show ' + Math.min(20, drill.total - drill.records.length) + ' more</button></div>';
    }
    box.innerHTML = html;
  }

  function runsURL() { return location.origin + '/api/records?' + drillQuery().toString(); }

  function appMarkdown(a, runs) {
    let md = '## ' + a.app + ': ' + R.num(a.failed) + (a.failed === 1 ? ' failed run ' : ' failed runs ') + period() + '\n\n';
    md += [
      'Runs: ' + R.num(a.runs) + ' (' + R.num(a.success) + ' success, ' + R.num(a.failed) + ' failed, ' + R.num(a.aborted) + ' aborted, ' +
        R.num(a.no_result) + ' no result, ' + R.num(a.in_progress) + ' in progress)',
      'Failure rate: ' + R.pct(a.failure_rate) + ' of finished runs',
      'Distinct setups among failures: ' + R.num(a.setups_failed),
      'Top cause: ' + (a.top_cause_message || 'none'),
      'Last failure: ' + (a.last_failure || 'none') + ', last success: ' + (a.last_success || 'none') + (a.failing_now ? ' (failing now)' : ''),
      'All runs: ' + runsURL(),
    ].map(function (l) { return '- ' + l; }).join('\n') + '\n';
    (runs || []).forEach(function (r) { md += '\n' + R.runMarkdown(r, 3); });
    return md;
  }

  function drillMarkdown() {
    const item = openItem();
    const runs = drill.records.slice(0, 3);
    if (open.kind === 'cause') return R.causeMarkdown(item, runs, { period: period(), runsURL: runsURL() });
    if (open.kind === 'app') return appMarkdown(item, runs);
    let md = '## Exit code ' + item.exit_code + ': ' + (item.description || '') + '\n\n- Failed runs: ' + R.num(item.count) + ' ' + period() +
      '\n- Apps: ' + R.num(item.apps) + '\n- All runs: ' + runsURL() + '\n';
    runs.forEach(function (r) { md += '\n' + R.runMarkdown(r, 3); });
    return md;
  }

  function toggle(kind, value) {
    open = isOpen(kind, value) ? null : { kind: kind, value: String(value) };
    writeURL();
    renderAll();
  }

  async function load() {
    writeURL();
    syncControls();
    $('refreshBtn').disabled = true;
    try {
      await loadSlugs();
      data = await R.getJSON('/api/errors?' + filterQuery().toString());
      renderSummary(data.summary || {});
      renderStuck(data.stuck || {});
      renderChart();
      drill.loaded = false;
      renderAll();
      $('errorBanner').hidden = true;
      $('updated').textContent = 'Updated ' + new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    } catch (e) {
      $('errorBanner').textContent = 'Could not load the error analysis: ' + e.message;
      $('errorBanner').hidden = false;
    } finally {
      $('refreshBtn').disabled = false;
    }
  }

  async function loadSlugs() {
    try {
      const res = await R.getJSON('/api/repo-slugs?days=' + encodeURIComponent(state.days) + '&repo=' + encodeURIComponent(state.repo));
      const slugs = res.repo_slugs || [];
      $('fSlug').innerHTML = '<option value="">All repositories</option>' + slugs.map(function (s) {
        return '<option value="' + esc(s.slug) + '">' + esc(s.slug) + ' (' + R.num(s.count) + ')</option>';
      }).join('');
      $('fSlug').value = slugs.some(function (s) { return s.slug === state.slug; }) ? state.slug : '';
    } catch (e) {
      $('fSlug').innerHTML = '<option value="">All repositories</option>';
    }
  }

  async function exportView() {
    const btn = $('exportBtn');
    btn.disabled = true;
    try {
      const q = filterQuery();
      q.set('examples', 'full');
      q.set('limit', '50');
      const res = await R.getJSON('/api/causes?' + q.toString());
      const out = {
        generated_at: new Date().toISOString(),
        view: location.href,
        filters: { days: Number(state.days), repo: state.repo, platform: state.platform || 'all', slug: state.slug, type: state.type || 'install' },
        summary: data ? data.summary : null,
        stuck: data ? data.stuck : null,
        causes: res.causes || [],
        apps: data ? data.apps : [],
        exit_codes: data ? data.exit_code_stats : [],
      };
      R.download('telemetry-errors-' + state.days + 'd-' + new Date().toISOString().slice(0, 10) + '.json', JSON.stringify(out, null, 2));
      return out;
    } catch (e) {
      $('errorBanner').textContent = 'Could not export: ' + e.message;
      $('errorBanner').hidden = false;
      return null;
    } finally {
      btn.disabled = false;
    }
  }

  async function postJSON(url, body) {
    const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const text = await r.text();
    let json = null;
    try { json = JSON.parse(text); } catch (e) { /* plain text answer */ }
    if (!r.ok) throw new Error((json && (json.error || json.message)) || text.trim() || 'The server answered ' + r.status);
    return json || {};
  }

  function openIssue() {
    const item = openItem();
    const title = open.kind === 'cause' ? '[Telemetry] ' + (item.top_apps && item.top_apps.length === 1 ? item.top_apps[0] + ': ' : '') + item.message
      : open.kind === 'app' ? '[Telemetry] ' + item.app + ': ' + (item.top_cause_message || 'failures')
        : '[Telemetry] Exit code ' + item.exit_code + ': ' + (item.description || '');
    $('issueTitle').value = title.slice(0, 200);
    $('issueBody').value = drillMarkdown();
    $('issueResult').textContent = '';
    $('issueDialog').showModal();
  }

  let searchTimer = null;
  function bind() {
    document.addEventListener('click', function (e) {
      const t = e.target;
      if (!t || !t.closest) return;
      const seg = t.closest('.seg[data-filter] button');
      if (seg) {
        const key = seg.closest('.seg').getAttribute('data-filter');
        state[key] = seg.getAttribute('data-value');
        if (key === 'repo') state.slug = '';
        load();
        return;
      }
      const cs = t.closest('[data-csort]');
      if (cs) { const f = cs.getAttribute('data-csort'); state.csort = state.csort === '-' + f ? f : '-' + f; writeURL(); syncControls(); renderAll(); return; }
      const as = t.closest('[data-asort]');
      if (as) { const f = as.getAttribute('data-asort'); state.asort = state.asort === '-' + f ? f : '-' + f; writeURL(); syncControls(); renderAll(); return; }
      const action = t.closest('[data-drill]');
      if (action) {
        const a = action.getAttribute('data-drill');
        if (a === 'close') { open = null; writeURL(); renderAll(); }
        if (a === 'more') { drill.page++; loadDrill(); }
        if (a === 'markdown') R.copyText(drillMarkdown(), action);
        if (a === 'issue') openIssue();
        return;
      }
      const run = t.closest('[data-drun]');
      if (run) { const rec = drill.records[Number(run.getAttribute('data-drun'))]; if (rec) R.openRecord(rec); return; }
      if (t.closest('#cleanupOpen')) { $('cleanupResult').textContent = ''; $('cleanupDialog').showModal(); return; }
      const row = t.closest('[data-open]');
      if (row && data && !t.closest('a')) {
        const kind = row.getAttribute('data-open');
        const i = Number(row.getAttribute('data-idx'));
        if (kind === 'cause') toggle('cause', data.causes[i].key);
        if (kind === 'app') toggle('app', data.apps[i].app);
        if (kind === 'exit') toggle('exit', data.exit_code_stats[i].exit_code);
      }
    });
    $('fSlug').addEventListener('change', function () { state.slug = this.value; load(); });
    $('fType').addEventListener('change', function () { state.type = this.value; load(); });
    $('fSearch').addEventListener('input', function () {
      const v = this.value;
      clearTimeout(searchTimer);
      searchTimer = setTimeout(function () { state.search = v; writeURL(); renderAll(); }, 200);
    });
    $('refreshBtn').addEventListener('click', load);
    $('exportBtn').addEventListener('click', exportView);
    $('cleanupForm').addEventListener('submit', async function (e) {
      e.preventDefault();
      $('cleanupRun').disabled = true;
      try {
        const res = await postJSON('/api/cleanup/run', { password: $('cleanupPassword').value });
        $('cleanupResult').textContent = res.message || 'Cleanup finished.';
        setTimeout(function () { $('cleanupDialog').close(); load(); }, 1500);
      } catch (err) {
        $('cleanupResult').textContent = 'Cleanup did not run: ' + err.message;
      } finally {
        $('cleanupRun').disabled = false;
      }
    });
    $('issueForm').addEventListener('submit', async function (e) {
      e.preventDefault();
      $('issueSubmit').disabled = true;
      try {
        const res = await postJSON('/api/github/create-issue', {
          password: $('issuePassword').value, title: $('issueTitle').value, body: $('issueBody').value,
          labels: $('issueLabels').value.split(',').map(function (l) { return l.trim(); }).filter(Boolean),
        });
        $('issueResult').innerHTML = 'Issue created: <a href="' + esc(res.issue_url) + '" target="_blank" rel="noopener">' + esc(res.issue_url) + '</a>';
      } catch (err) {
        $('issueResult').textContent = 'The issue was not created: ' + err.message;
      } finally {
        $('issueSubmit').disabled = false;
      }
    });
  }

  function init() {
    R.initChrome();
    readURL();
    bind();
    return load();
  }

  window.ErrorPage = {
    init: init, state: state, exportView: exportView, drillMarkdown: drillMarkdown, toggle: toggle,
    data: function () { return data; }, drill: function () { return drill; },
  };
  if (!window.__NO_AUTOSTART__) init();
})();

(function () {
  'use strict';
  const R = window.RunUI;
  const esc = R.esc;
  const $ = function (id) { return document.getElementById(id); };

  const PAGE = 50;
  const KEYS = ['days', 'repo', 'platform', 'slug', 'search', 'type', 'failing', 'sort', 'app'];
  const DEFAULTS = { days: '1', repo: 'all', sort: '-total' };
  const PERIODS = [['1', 'Today'], ['7', '7 days'], ['30', '30 days'], ['90', '90 days'], ['365', '1 year']];
  const OUTCOMES = [['success', 'c-ok', 'successful'], ['failed', 'c-bad', 'failed'], ['aborted', 'c-abort', 'aborted'],
    ['no_result', 'c-silent', 'without result'], ['installing', 'c-running', 'running']];

  const state = { days: '1', repo: 'all', platform: '', slug: '', search: '', type: '', failing: '', sort: '-total', app: '', all: false };
  let data = null;
  let ranks = {};
  let rows = [];
  let detail = null;
  let detailDays = '1';
  let detailSeq = 0;

  function readURL() {
    const q = new URLSearchParams(location.search);
    KEYS.forEach(function (k) { if (q.has(k)) state[k] = q.get(k); });
  }
  function writeURL() {
    const q = new URLSearchParams();
    KEYS.forEach(function (k) { if (state[k] && state[k] !== DEFAULTS[k]) q.set(k, state[k]); });
    const s = q.toString();
    history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
  }
  function filterQuery(days) {
    const q = new URLSearchParams({ days: days || state.days, repo: state.repo });
    if (state.platform) q.set('platform', state.platform);
    if (state.slug) q.set('slug', state.slug);
    return q;
  }
  function periodName(days) {
    return days === '1' ? 'today' : days === '365' ? 'in the last year' : 'in the last ' + days + ' days';
  }

  function syncControls() {
    document.querySelectorAll('.seg[data-filter]').forEach(function (seg) {
      const key = seg.getAttribute('data-filter');
      seg.querySelectorAll('button').forEach(function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-value') === state[key]));
      });
    });
    $('fSearch').value = state.search;
    $('fType').value = state.type;
    $('fFailing').checked = state.failing === '1';
    document.querySelectorAll('.th-sort[data-ssort]').forEach(function (b) {
      const f = b.getAttribute('data-ssort');
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

  function renderCards() {
    const list = data.top_scripts || [];
    let ok = 0, bad = 0, failing = 0;
    list.forEach(function (s) {
      ok += s.success;
      bad += s.failed;
      if (s.failing_now) failing++;
    });
    const finished = ok + bad;
    const avg = list.length ? data.total_installs / list.length : 0;
    $('cards').innerHTML =
      card('Scripts', R.num(list.length), 'with at least one run') +
      card('Runs', R.num(data.total_installs), avg.toFixed(1) + ' per script on average') +
      card('Failure rate', finished ? R.pct(bad / finished * 100) : 'No data', R.num(bad) + ' of ' + R.num(finished) + ' finished runs failed', 'bad') +
      card('Failing now', R.num(failing), 'last failure is newer than the last success', 'bad');
  }

  function sortValue(s, f) {
    if (f === 'app') return s.app.toLowerCase();
    if (f === 'last_failure') return s.last_failure || '';
    if (f === 'failure_rate') return s.success + s.failed ? s.failure_rate : -1;
    return Number(s[f]) || 0;
  }
  function sorted(list) {
    const desc = state.sort.charAt(0) === '-';
    const f = desc ? state.sort.slice(1) : state.sort;
    return list.slice().sort(function (a, b) {
      const x = sortValue(a, f), y = sortValue(b, f);
      if (x < y) return desc ? 1 : -1;
      if (x > y) return desc ? -1 : 1;
      return (ranks[a.app] || 0) - (ranks[b.app] || 0);
    });
  }
  function matches(s) {
    if (state.search && s.app.toLowerCase().indexOf(state.search.toLowerCase()) === -1) return false;
    if (state.type === 'pve' && s.type !== 'pve' && s.type !== 'tool') return false;
    if (state.type && state.type !== 'pve' && s.type !== state.type) return false;
    return state.failing !== '1' || s.failing_now;
  }

  function rateCell(r) {
    if (!(r.success + r.failed)) return '<td class="num muted">none finished</td>';
    const sev = r.failure_rate >= 30 ? 'bad' : r.failure_rate >= 15 ? 'warn' : '';
    return '<td class="num strong ' + sev + '">' + R.pct(r.failure_rate) + '</td>';
  }

  function outcomeBar(s) {
    const total = OUTCOMES.reduce(function (a, o) { return a + (s[o[0]] || 0); }, 0);
    if (!total) return '';
    const label = OUTCOMES.filter(function (o) { return s[o[0]]; }).map(function (o) { return R.num(s[o[0]]) + ' ' + o[2]; }).join(', ');
    return '<div class="outcome-bar" role="img" aria-label="' + esc(label) + '" title="' + esc(label) + '">' +
      OUTCOMES.map(function (o) {
        return s[o[0]] ? '<span class="' + o[1] + '" style="width:' + (s[o[0]] / total * 100).toFixed(1) + '%"></span>' : '';
      }).join('') + '</div>';
  }

  function failingChip(failingNow) {
    return failingNow
      ? '<span class="chip chip-failed" title="The last failure is newer than the last success">Failing</span>'
      : '<span class="chip chip-success" title="A successful run came after the last failure">Recovered</span>';
  }

  function lastFailure(s) {
    if (!s.last_failure) return '<span class="muted">none</span>';
    return '<span title="' + esc(R.dateTime(s.last_failure)) + '">' + esc(R.ago(s.last_failure)) + '</span>' +
      (s.failing_now ? ' ' + failingChip(true) : '');
  }

  function muted(n) { return n ? '' : ' muted'; }

  function scriptRow(s, i) {
    const cause = s.failed && s.last_failure_cause
      ? '<div class="cause-line" title="' + esc(s.last_failure_cause) + '">' + esc(s.last_failure_cause) + '</div>' : '';
    return '<tr class="script-row" data-script="' + i + '">' +
      '<td class="num muted">' + (ranks[s.app] || '') + '</td>' +
      '<td class="app-cell"><button type="button" class="run-open" data-script="' + i + '">' + esc(s.app) + '</button> ' +
      R.typeBadge(s.type) + cause + '</td>' +
      '<td class="num strong">' + R.num(s.total) + '</td>' +
      '<td class="num' + muted(s.success) + '">' + R.num(s.success) + '</td>' +
      '<td class="num' + (s.failed ? ' bad' : ' muted') + '">' + R.num(s.failed) + '</td>' +
      '<td class="num' + muted(s.aborted) + '">' + R.num(s.aborted) + '</td>' +
      '<td class="num' + muted(s.no_result) + '">' + R.num(s.no_result) + '</td>' +
      rateCell(s) +
      '<td class="num' + muted(s.setups_failed) + '">' + R.num(s.setups_failed) + '</td>' +
      '<td class="nowrap">' + lastFailure(s) + '</td>' +
      '<td>' + outcomeBar(s) + '</td></tr>';
  }

  function renderTable() {
    const list = data ? data.top_scripts || [] : [];
    const filtered = sorted(list.filter(matches));
    rows = state.all ? filtered : filtered.slice(0, PAGE);
    $('scriptsBody').innerHTML = rows.length ? rows.map(scriptRow).join('')
      : '<tr><td colspan="11" class="empty">' + (list.length ? 'No script matches these filters.' : 'No script ran in this period.') + '</td></tr>';
    $('scriptsInfo').textContent = 'Showing ' + R.num(rows.length) + ' of ' + R.num(filtered.length) + (filtered.length === 1 ? ' script' : ' scripts');
    $('showAll').hidden = filtered.length <= PAGE;
    $('showAll').textContent = state.all ? 'Show the first ' + PAGE : 'Show all ' + R.num(filtered.length);
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

  async function load() {
    writeURL();
    syncControls();
    $('refreshBtn').disabled = true;
    try {
      await loadSlugs();
      const q = filterQuery().toString();
      $('apiLink').href = '/api/scripts?' + q;
      data = await R.getJSON('/api/scripts?' + q);
      ranks = {};
      (data.top_scripts || []).forEach(function (s, i) { ranks[s.app] = i + 1; });
      renderCards();
      renderTable();
      showError('');
      $('updated').textContent = 'Updated ' + new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    } catch (e) {
      showError('Could not load the scripts: ' + e.message);
    } finally {
      $('refreshBtn').disabled = false;
    }
  }

  // Script detail dialog

  function detailQuery() {
    const q = filterQuery(detailDays);
    q.set('app', state.app);
    return q;
  }

  function detailHead(app, d) {
    const errs = filterQuery(detailDays);
    errs.set('type', 'all');
    errs.set('app', app);
    const log = filterQuery(detailDays);
    log.set('search', app);
    return '<header class="rd-head"><div class="rd-title"><h2 id="scriptTitle">' + esc(app) + '</h2>' +
      (d ? R.typeBadge(d.type) + (d.failed ? failingChip(d.failing_now) : '') : '') + '</div>' +
      '<div class="sd-controls"><div class="seg" data-dfilter="days" role="group" aria-label="Period">' +
      PERIODS.map(function (p) {
        return '<button type="button" data-value="' + p[0] + '" aria-pressed="' + (p[0] === detailDays) + '">' + p[1] + '</button>';
      }).join('') + '</div><div class="rd-actions">' +
      '<a class="btn" href="/error-analysis?' + esc(errs.toString()) + '">Error analysis</a>' +
      '<a class="btn" href="/?' + esc(log.toString()) + '">Installation log</a>' +
      (d ? '<button type="button" class="btn" data-sd="markdown">Copy as Markdown</button>' : '') +
      '<a class="btn" href="/api/script-detail?' + esc(detailQuery().toString()) + '">JSON</a>' +
      '</div></div></header>';
  }

  function stat(label, value, cls) {
    return '<div class="' + (cls || '') + '"><dt>' + esc(label) + '</dt><dd>' + value + '</dd></div>';
  }

  function lastSeen(d) {
    const parts = [];
    if (d.last_failure) parts.push('last failure ' + R.ago(d.last_failure) + ' (' + R.dateTime(d.last_failure) + ')');
    if (d.last_success) parts.push('last success ' + R.ago(d.last_success) + ' (' + R.dateTime(d.last_success) + ')');
    if (!parts.length) return '';
    const text = parts.join(', ');
    return '<p class="sd-note">' + esc(text.charAt(0).toUpperCase() + text.slice(1)) + '.</p>';
  }

  function hourLabel(t) {
    const d = R.parseTime(t);
    return d ? d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '';
  }

  function resultLine(r) {
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

  function breakdownTable(name, list, label) {
    list = list || [];
    return '<div class="sd-table table-wrapper"><table class="compact"><thead><tr><th scope="col">' + esc(name) + '</th>' +
      '<th scope="col" class="num">Runs</th><th scope="col" class="num">Failure rate</th></tr></thead><tbody>' +
      (list.length ? list.map(function (r) {
        return '<tr><td>' + esc(label(r.label)) + '</td><td class="num">' + R.num(r.runs) + '</td>' + rateCell(r) + '</tr>';
      }).join('') : '<tr><td colspan="3" class="empty">Not reported</td></tr>') + '</tbody></table></div>';
  }

  function detailHTML(d) {
    let html = detailHead(d.app, d);
    if (!d.total) return html + '<p class="empty">' + esc(d.app) + ' did not run ' + esc(periodName(detailDays)) + '.</p>';
    const finished = d.success + d.failed;
    html += '<dl class="sd-stats">' +
      stat('Runs', R.num(d.total)) +
      stat('Success rate', finished ? R.pct(d.success_rate) : 'none finished', 'ok') +
      stat('Failed', R.num(d.failed), 'bad') +
      stat('Aborted', R.num(d.aborted), 'abort') +
      stat('No result', R.num(d.no_result), 'silent') +
      stat('Running', R.num(d.in_progress)) +
      stat('Failed setups', R.num(d.setups_failed)) +
      stat('Avg. success', d.avg_duration ? R.duration(d.avg_duration) : 'none') + '</dl>';
    html += lastSeen(d);

    const hourly = d.bucket === 'hour';
    html += '<section class="rd-section"><h3>' + (hourly ? 'Runs per hour today' : 'Runs per day') + '</h3>' +
      R.columnsSVG(d.daily || [], [
        { key: 'success', cls: 'c-ok', name: 'successful' },
        { key: 'failed', cls: 'c-bad', name: 'failed' },
        { key: 'aborted', cls: 'c-abort', name: 'aborted' },
      ], { title: 'Runs of ' + d.app, label: function (p) { return hourly ? hourLabel(p.date) : p.date.slice(5); } }) +
      '<div class="legend"><span class="key c-ok"></span>Success <span class="key c-bad"></span>Failed <span class="key c-abort"></span>Aborted</div></section>';

    const causes = d.causes || [];
    html += '<section class="rd-section"><h3>What fails</h3>';
    if (!causes.length) {
      html += '<p class="muted">No failed runs ' + esc(periodName(detailDays)) + '.</p>';
    } else {
      html += '<div class="sd-table table-wrapper"><table class="compact"><thead><tr><th scope="col">Cause</th><th scope="col" class="num">Failures</th>' +
        '<th scope="col" class="num">Setups</th><th scope="col">Last seen</th><th scope="col" class="num">Last 24 h</th></tr></thead><tbody>' +
        causes.map(function (c) {
          const u = filterQuery(detailDays);
          u.set('type', 'all');
          u.set('cause', c.key);
          return '<tr><td class="cause-cell"><a class="cause-link' + (c.kind === 'command' ? ' is-code' : '') + '" href="/error-analysis?' + esc(u.toString()) +
            '" title="' + esc(c.message) + '">' + esc(c.message) + '</a><div class="cause-meta">Exit ' + c.exit_code + ': ' + esc(c.exit_description) +
            (c.category ? ', ' + esc(c.category) : '') + '</div></td>' +
            '<td class="num strong bad">' + R.num(c.failures) + '</td><td class="num">' + R.num(c.setups) + '</td>' +
            '<td class="nowrap" title="' + esc(R.dateTime(c.last_seen)) + '">' + esc(R.ago(c.last_seen)) + '</td><td class="num">' + R.trend(c) + '</td></tr>';
        }).join('') + '</tbody></table></div>';
    }
    html += '</section>';

    const recent = d.recent || [];
    html += '<section class="rd-section"><h3>Recent runs</h3>';
    if (!recent.length) {
      html += '<p class="muted">No runs ' + esc(periodName(detailDays)) + '.</p>';
    } else {
      html += '<div class="sd-table table-wrapper"><table class="compact runs"><thead><tr><th scope="col">Started</th><th scope="col">Status</th>' +
        '<th scope="col">Result</th><th scope="col" class="num">Duration</th><th scope="col">Host</th><th scope="col">OS</th></tr></thead><tbody>' +
        recent.map(function (r, i) {
          return '<tr class="srun-row" data-srun="' + i + '"><td class="nowrap"><button type="button" class="run-open" data-srun="' + i + '">' +
            esc(R.dateTime(r.started_at)) + '</button></td><td>' + R.statusChip(r) + '</td><td class="result-cell">' + resultLine(r) + '</td>' +
            '<td class="num nowrap">' + esc(R.duration(r.duration)) + '</td><td class="nowrap">' + (esc(R.hostName(r)) || '<span class="muted">unknown</span>') +
            '</td><td class="nowrap">' + (esc(R.osName(r)) || '<span class="muted">unknown</span>') + '</td></tr>';
        }).join('') + '</tbody></table></div>';
    }
    html += '</section>';

    const b = d.breakdown || {};
    html += '<section class="rd-section sd-split">' + breakdownTable('Platform', b.platform, R.platformName) +
      breakdownTable('OS', b.os, String) + '</section>';
    return html;
  }

  function detailMarkdown() {
    const d = detail;
    if (!d) return '';
    const finished = d.success + d.failed;
    let md = '## ' + d.app + ' (' + R.typeName(d.type) + '), ' + periodName(detailDays) + '\n\n';
    md += [
      'Runs: ' + d.total + ' (' + d.success + ' successful, ' + d.failed + ' failed, ' + d.aborted + ' aborted, ' +
        d.no_result + ' without result, ' + d.in_progress + ' running)',
      'Failure rate: ' + (finished ? R.pct(d.failure_rate) + ' of ' + finished + ' finished runs' : 'no finished runs'),
      d.failed ? 'Failing now: ' + (d.failing_now ? 'yes' : 'no') + ' (last failure ' + d.last_failure +
        (d.last_success ? ', last success ' + d.last_success : ', no success') + ')' : '',
      d.failed ? 'Failed setups: ' + d.setups_failed + ' distinct hardware and host combinations' : '',
      d.avg_duration ? 'Average successful run: ' + R.duration(d.avg_duration) : '',
    ].filter(Boolean).map(function (l) { return '- ' + l; }).join('\n') + '\n';
    if ((d.causes || []).length) {
      md += '\n### Causes\n\n| Cause | Failures | Setups | Exit code | Last seen |\n| --- | ---: | ---: | --- | --- |\n';
      d.causes.forEach(function (c) {
        md += '| ' + [c.message, c.failures, c.setups, c.exit_code + ' (' + c.exit_description + ')', c.last_seen].map(R.mdCell).join(' | ') + ' |\n';
      });
    }
    if ((d.recent || []).length) {
      md += '\n### Recent runs\n\n| Started | Status | Result | Duration | Host | OS | Execution ID |\n| --- | --- | --- | --- | --- | --- | --- |\n';
      d.recent.forEach(function (r) {
        md += '| ' + [r.started_at, (R.STATUS[r.status] || { label: r.status }).label, R.headline(r), R.duration(r.duration),
          R.hostName(r), R.osName(r), r.execution_id || r.random_id].map(R.mdCell).join(' | ') + ' |\n';
      });
    }
    md += '\nData: ' + location.origin + '/api/script-detail?' + detailQuery().toString() + '\n';
    return md;
  }

  async function loadDetail() {
    const seq = ++detailSeq;
    detail = null;
    $('scriptBody').innerHTML = detailHead(state.app, null) + '<p class="empty">Loading the runs of ' + esc(state.app) + '</p>';
    try {
      const d = await R.getJSON('/api/script-detail?' + detailQuery().toString());
      if (seq !== detailSeq) return;
      detail = d;
      $('scriptBody').innerHTML = detailHTML(d);
    } catch (e) {
      if (seq !== detailSeq) return;
      $('scriptBody').innerHTML = detailHead(state.app, null) +
        '<p class="banner banner-error">Could not load ' + esc(state.app) + ': ' + esc(e.message) + '</p>';
    }
  }

  function openScript(app) {
    state.app = app;
    detailDays = state.days;
    writeURL();
    const dlg = $('scriptDialog');
    if (dlg.showModal && !dlg.open) dlg.showModal();
    else dlg.setAttribute('open', '');
    return loadDetail();
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
        state.all = false;
        load();
        return;
      }
      const daySeg = t.closest('.seg[data-dfilter] button');
      if (daySeg) {
        detailDays = daySeg.getAttribute('data-value');
        loadDetail();
        return;
      }
      const sortBtn = t.closest('.th-sort[data-ssort]');
      if (sortBtn) {
        const f = sortBtn.getAttribute('data-ssort');
        state.sort = state.sort === '-' + f ? f : '-' + f;
        writeURL();
        syncControls();
        renderTable();
        return;
      }
      const md = t.closest('[data-sd="markdown"]');
      if (md) {
        R.copyText(detailMarkdown(), md);
        return;
      }
      const run = t.closest('[data-srun]');
      if (run && detail) {
        const rec = (detail.recent || [])[Number(run.getAttribute('data-srun'))];
        if (rec) R.openRecord(rec);
        return;
      }
      const row = t.closest('[data-script]');
      if (row && !t.closest('a')) {
        const s = rows[Number(row.getAttribute('data-script'))];
        if (s) openScript(s.app);
      }
    });
    $('fSlug').addEventListener('change', function () { state.slug = this.value; state.all = false; load(); });
    $('refreshBtn').addEventListener('click', load);
    $('fSearch').addEventListener('input', function () {
      const v = this.value.trim();
      clearTimeout(searchTimer);
      searchTimer = setTimeout(function () { state.search = v; state.all = false; writeURL(); renderTable(); }, 150);
    });
    $('fType').addEventListener('change', function () { state.type = this.value; state.all = false; writeURL(); renderTable(); });
    $('fFailing').addEventListener('change', function () { state.failing = this.checked ? '1' : ''; state.all = false; writeURL(); renderTable(); });
    $('showAll').addEventListener('click', function () { state.all = !state.all; renderTable(); });
    $('scriptDialog').addEventListener('close', function () { state.app = ''; detailSeq++; writeURL(); });
  }

  async function init() {
    R.initChrome();
    readURL();
    bind();
    const app = state.app;
    await load();
    if (app) await openScript(app);
  }

  window.ScriptsPage = {
    init: init, state: state, openScript: openScript, detailMarkdown: detailMarkdown,
    data: function () { return data; }, detail: function () { return detail; }, rows: function () { return rows; },
  };
  if (!window.__NO_AUTOSTART__) init();
})();

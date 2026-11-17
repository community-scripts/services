(function () {
  'use strict';
  const R = window.RunUI;
  const esc = R.esc;
  const $ = function (id) { return document.getElementById(id); };

  // app and cause scope the whole page; open names the selected app or cause.
  const state = {
    days: '1', repo: 'all', platform: '', slug: '', app: '', cause: '', open: '',
    page: 1, limit: 25, sort: '-created', search: '', status: '', type: '', os: '',
  };
  const URL_KEYS = ['days', 'repo', 'platform', 'slug', 'app', 'cause', 'open', 'search', 'status', 'type', 'os', 'sort'];
  const triage = { tab: 'apps', sort: 'rate', query: '', failingNow: false, apps: [], causes: [], minRuns: 5, sel: null, error: '' };
  let records = [];
  let totalPages = 1;
  let timer = null;
  let dash = null;
  let pane = null;
  let drill = null;
  let cells = [];

  function readURL() {
    const q = new URLSearchParams(location.search);
    URL_KEYS.forEach(function (k) { if (q.has(k)) state[k] = q.get(k); });
    const i = state.open.indexOf(':');
    if (i > 0) {
      triage.sel = { kind: state.open.slice(0, i), id: state.open.slice(i + 1) };
      triage.tab = triage.sel.kind === 'cause' ? 'causes' : 'apps';
    }
  }
  function writeURL() {
    const q = new URLSearchParams();
    const defaults = { days: '1', repo: 'all', sort: '-created' };
    URL_KEYS.forEach(function (k) { if (state[k] && state[k] !== defaults[k]) q.set(k, state[k]); });
    const s = q.toString();
    history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
  }
  function baseQuery() {
    const q = new URLSearchParams({ days: state.days, repo: state.repo });
    if (state.platform) q.set('platform', state.platform);
    if (state.slug) q.set('slug', state.slug);
    return q;
  }
  function filterQuery() {
    const q = baseQuery();
    if (state.app) q.set('app', state.app);
    if (state.cause) q.set('cause', state.cause);
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

  function show(dialogId) {
    const dlg = $(dialogId);
    if (dlg.showModal && !dlg.open) dlg.showModal();
    else dlg.setAttribute('open', '');
  }

  function sevClass(rate) { return rate >= 30 ? 'bad' : rate >= 15 ? 'warn' : ''; }
  function nowChip(a) {
    return a.failing_now
      ? '<span class="chip chip-failed" title="Last failure ' + esc(R.dateTime(a.last_failure)) + ' is newer than the last success">Failing</span>'
      : '<span class="chip chip-success" title="Last success ' + esc(R.dateTime(a.last_success)) + '">Recovered</span>';
  }
  function exitChip(code) { return '<span class="exit-code">' + esc(code) + '</span>'; }
  function runCause(r) {
    if (r.cause_kind === 'exit_code' || !r.cause) return 'Exit ' + (r.exit_code || 0) + ': ' + (r.exit_description || 'no description');
    return r.cause;
  }
  function causeMessage(key) {
    const lists = [triage.causes, dash && dash.failure_matrix ? dash.failure_matrix.causes : [], dash && dash.top_causes ? dash.top_causes : []];
    for (let i = 0; i < lists.length; i++) {
      const hit = (lists[i] || []).filter(function (c) { return c.key === key; })[0];
      if (hit) return hit.message;
    }
    return key;
  }
  function errorsURL(extra) {
    const q = baseQuery();
    Object.keys(extra || {}).forEach(function (k) { q.set(k, extra[k]); });
    return '/error-analysis?' + q.toString();
  }

  function renderScope() {
    const chip = function (key, label, text) {
      return '<span class="scope-chip"><span>' + label + '</span><b title="' + esc(text) + '">' + esc(text) + '</b>' +
        '<button type="button" data-unscope="' + key + '" aria-label="Remove the ' + label.toLowerCase() + ' filter">&times;</button></span>';
    };
    const chips = (state.app ? chip('app', 'App', state.app) : '') + (state.cause ? chip('cause', 'Cause', causeMessage(state.cause)) : '');
    $('scopeRow').hidden = !chips;
    $('scopeChips').innerHTML = chips;
  }
  function setScope(key, value) {
    state[key] = state[key] === value ? '' : value;
    state.page = 1;
    return refresh();
  }

  function card(label, value, sub, cls, drillTo) {
    const body = '<div class="card-label">' + esc(label) + '</div><div class="card-value">' + value + '</div><div class="card-sub">' + sub + '</div>';
    if (drillTo) {
      return '<button type="button" class="card card-btn ' + (cls || '') + '" data-drill="' + drillTo + '">' + body + '<span class="card-go">View runs</span></button>';
    }
    return '<div class="card ' + (cls || '') + '">' + body + '</div>';
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
      card('Failed', R.num(s.failed), finished ? R.pct(s.failure_rate) + ' of finished runs' : 'no finished runs', 'bad', s.failed ? 'failed' : '') +
      card('Aborted', R.num(s.aborted), 'cancelled or declined', 'abort', s.aborted ? 'aborted' : '') +
      card('No result', R.num(s.no_result), 'stopped reporting before a result', 'silent', s.no_result ? 'unknown' : '') +
      '<div class="card popular"><div class="card-label">Most popular</div>' +
      (popular ? '<ol class="podium">' + popular + '</ol>' : '<div class="card-sub">No runs in this period</div>') + '</div>';
  }

  /* Triage: every app or cause with failures on the left, its failed runs on the right. */

  function appItems() {
    const q = triage.query.toLowerCase();
    const graded = function (a) { return a.success + a.failed >= triage.minRuns ? 1 : 0; };
    const byRate = triage.sort === 'rate' && !state.cause;
    return triage.apps.filter(function (a) {
      return (!triage.failingNow || a.failing_now) && (!q || a.app.toLowerCase().indexOf(q) !== -1);
    }).sort(function (a, b) {
      const rate = byRate ? graded(b) - graded(a) || b.failure_rate - a.failure_rate : 0;
      return rate || b.failed - a.failed || (a.app < b.app ? -1 : 1);
    });
  }
  function causeItems() {
    const q = triage.query.toLowerCase();
    return triage.causes.filter(function (c) { return !q || String(c.message).toLowerCase().indexOf(q) !== -1; });
  }
  function listed() {
    return triage.tab === 'apps'
      ? appItems().map(function (a) { return { kind: 'app', id: a.app }; })
      : causeItems().map(function (c) { return { kind: 'cause', id: c.key }; });
  }
  function isSelected(kind, id) { return !!triage.sel && triage.sel.kind === kind && triage.sel.id === id; }

  function renderTriage() {
    $('tabApps').setAttribute('aria-selected', String(triage.tab === 'apps'));
    $('tabCauses').setAttribute('aria-selected', String(triage.tab === 'causes'));
    $('tabAppsCount').textContent = R.num(triage.apps.length);
    $('tabCausesCount').textContent = R.num(triage.causes.length);
    $('triageSearch').value = triage.query;
    $('triageSort').value = triage.sort;
    $('triageSort').hidden = triage.tab !== 'apps' || !!state.cause;
    $('triageNow').hidden = triage.tab !== 'apps';
    $('triageNow').setAttribute('aria-pressed', String(triage.failingNow));
    $('errorsNote').textContent = 'Every app and cause with failed runs. Failure rates grade apps with at least ' + triage.minRuns + ' finished runs in this period.';
    $('errorsLink').href = errorsURL();

    let html = '';
    if (triage.error) {
      html = '<p class="empty">Could not load the failures: ' + esc(triage.error) + '</p>';
    } else if (triage.tab === 'apps') {
      html = appItems().map(function (a) {
        const finished = a.success + a.failed;
        const on = isSelected('app', a.app);
        const figure = state.cause ? R.num(a.failed)
          : '<span class="' + (finished >= triage.minRuns ? sevClass(a.failure_rate) : 'muted') + '"' +
            (finished >= triage.minRuns ? '' : ' title="Fewer than ' + triage.minRuns + ' finished runs, so the rate is not graded"') + '>' + R.pct(a.failure_rate) + '</span>';
        return '<button type="button" class="tri-item' + (on ? ' is-on' : '') + '" role="option" aria-selected="' + on + '" data-pick="app" data-id="' + esc(a.app) + '">' +
          '<span class="tri-top"><span class="tri-name">' + esc(a.app) + '</span>' + R.typeBadge(a.type) + '<span class="tri-figure">' + figure + '</span></span>' +
          '<span class="tri-sub"><span>' + R.num(a.failed) + ' of ' + R.num(finished) + ' finished runs failed</span>' + nowChip(a) + '</span>' +
          (a.top_cause_message ? '<span class="tri-cause' + (a.top_cause_kind === 'command' ? ' is-code' : '') + '">' + esc(a.top_cause_message) + '</span>' : '') + '</button>';
      }).join('') || '<p class="empty">' + (triage.apps.length ? 'No app matches.' : 'No failed runs in this period.') + '</p>';
    } else {
      html = causeItems().map(function (c) {
        const on = isSelected('cause', c.key);
        return '<button type="button" class="tri-item' + (on ? ' is-on' : '') + '" role="option" aria-selected="' + on + '" data-pick="cause" data-id="' + esc(c.key) + '">' +
          '<span class="tri-top"><span class="tri-msg' + (c.kind === 'command' ? ' is-code' : '') + '">' + esc(c.message) + '</span><span class="tri-figure bad">' + R.num(c.failures) + '</span></span>' +
          '<span class="tri-sub"><span>' + exitChip(c.exit_code) + ' ' + R.num(c.apps) + (c.apps === 1 ? ' app, ' : ' apps, ') + R.num(c.setups) + (c.setups === 1 ? ' setup' : ' setups') + '</span>' + R.trend(c) + '</span></button>';
      }).join('') || '<p class="empty">' + (triage.causes.length ? 'No cause matches.' : 'No failed runs in this period.') + '</p>';
    }
    $('triageList').innerHTML = html;
  }

  // Keeps a selection that still exists, otherwise takes the first entry of the open tab.
  function settleSelection() {
    const sel = triage.sel;
    const known = sel && (sel.kind === 'app'
      ? triage.apps.some(function (a) { return a.app === sel.id; })
      : triage.causes.some(function (c) { return c.key === sel.id; }));
    if (known) return;
    const first = listed()[0];
    triage.sel = first || null;
    state.open = '';
  }

  async function loadTriage() {
    const q = filterQuery().toString();
    try {
      const res = await Promise.all([R.getJSON('/api/app-errors?' + q + '&limit=300'), R.getJSON('/api/causes?' + q + '&limit=100')]);
      triage.apps = res[0].apps || [];
      triage.minRuns = res[0].min_runs || 5;
      triage.causes = res[1].causes || [];
      triage.error = '';
    } catch (e) {
      triage.apps = [];
      triage.causes = [];
      triage.error = e.message;
    }
  }

  const GROUPS = {
    cause: {
      name: 'Cause', key: function (r) { return r.cause || 'exit ' + (r.exit_code || 0); },
      label: function (k, r) { return '<span' + (r.cause_kind === 'command' ? ' class="is-code"' : '') + '>' + esc(runCause(r)) + '</span>'; }, jump: 'cause',
    },
    app: { name: 'App', key: function (r) { return r.nsapp || 'unknown'; }, label: function (k, r) { return esc(k) + ' ' + R.typeBadge(r.type); }, jump: 'app' },
    exit: {
      name: 'Exit code', key: function (r) { return String(r.exit_code || 0); },
      label: function (k, r) { return exitChip(k) + ' ' + esc(r.exit_description || 'no description'); },
    },
    os: { name: 'OS', key: function (r) { return R.osName(r) || 'unknown'; }, label: function (k) { return esc(k); } },
    host: {
      name: 'Host', key: function (r) { return [r.cpu_model, r.pve_version, r.kernel_version].join('|'); },
      label: function (k, r) {
        return esc(r.cpu_model || 'Unknown CPU') + ' <span class="muted">' + esc([R.hostName(r), r.kernel_version].filter(Boolean).join(', ')) + '</span>';
      },
    },
    stage: { name: 'Last stage', key: function (r) { return r.last_stage || 'unknown'; }, label: function (k) { return esc(R.stageName(k)); } },
  };

  function majority(list, key) {
    let total = 0;
    let best = null;
    (list || []).forEach(function (x) { total += x.count; if (!best || x.count > best.count) best = x; });
    return best && best.count * 2 > total ? best[key] : '';
  }

  // Says what the runs have in common. The usual OS and host version are left out:
  // most runs share them, so they explain nothing.
  function insights(runs) {
    if (runs.length < 2) return '';
    const usualOS = dash ? majority(dash.os_distribution, 'os') : '';
    const usualHost = dash ? majority(dash.pve_versions, 'version') : '';
    const dims = [
      [function (r) { return r.cpu_model ? [r.cpu_model, r.gpu_model, r.ram_speed, r.pve_version, r.kernel_version].join('|') : ''; },
        function (v, all) { return 'on one host setup (' + v.split('|')[0] + ')' + (all ? ', which usually means a single host retrying' : ''); }],
      [function (r) { return r.os_type && r.os_type !== usualOS ? R.osName(r) : ''; }, function (v) { return 'on ' + v; }],
      [function (r) { return r.pve_version && r.pve_version !== usualHost ? R.hostName(r) : ''; }, function (v) { return 'on ' + v; }],
      [function (r) { return r.failed_function || ''; }, function (v) { return 'in ' + v; }],
      [function (r) { return r.error_phase === 'host' ? 'host' : ''; }, function () { return 'on the host, before anything ran in the container'; }],
      [function (r) { return r.last_stage || ''; }, function (v) { return 'during ' + R.stageName(v).toLowerCase(); }],
      [function (r) { return r.ram_size && r.ram_size <= 1024 ? 'low' : ''; }, function () { return 'with 1 GB of RAM or less'; }],
      [function (r) { return r.has_arm ? 'arm' : ''; }, function () { return 'on ARM hosts'; }],
    ];
    const out = [];
    dims.forEach(function (d) {
      const counts = {};
      let best = '';
      runs.forEach(function (r) {
        const v = d[0](r);
        if (!v) return;
        counts[v] = (counts[v] || 0) + 1;
        if (!best || counts[v] > counts[best]) best = v;
      });
      if (!best) return;
      const n = counts[best];
      if (n === runs.length) out.push('<li><b>All ' + n + ' runs</b> happened ' + esc(d[1](best, true)) + '</li>');
      else if (runs.length >= 4 && n / runs.length >= 0.75) out.push('<li><b>' + n + ' of ' + runs.length + ' runs</b> happened ' + esc(d[1](best, false)) + '</li>');
    });
    if (!out.length) return '';
    return '<div class="insight"><h4>Common denominators</h4><ul>' + out.slice(0, 3).join('') + '</ul></div>';
  }

  function runRow(m, r, i, p) {
    const trace = R.traceText(r.error);
    const tail = trace ? trace.split('\n').slice(-8).join('\n') : '';
    const cmd = r.failed_command ? '<code>' + esc(r.failed_command) + '</code>' + (r.failed_line ? ' at line ' + r.failed_line : '') : '';
    const facts = [
      ['Step', esc(r.error_step || '')],
      ['Where', r.error_phase === 'host' ? 'Host' : r.error_phase === 'container' ? 'Container' : ''],
      ['Function', r.failed_function ? '<code>' + esc(r.failed_function) + '</code>' : ''],
      ['Command', cmd],
      ['Exit code', r.exit_code ? esc(r.exit_code + ': ' + (r.exit_description || '')) : ''],
      ['Resources', esc(R.resources(r))],
      ['Host', esc([R.hostName(r), r.kernel_version].filter(Boolean).join(', '))],
      ['Repository', esc(r.repo_slug || r.repo_source || '')],
    ].filter(function (f) { return f[1]; });
    return '<details class="run"><summary>' +
      '<span class="nowrap" title="' + esc(r.started_at) + '">' + esc(R.dateTime(r.started_at)) + '</span>' +
      (m.showApp ? '<span class="run-app">' + esc(r.nsapp || 'unknown') + ' ' + R.typeBadge(r.type) + '</span>' : '') +
      '<span>' + (r.exit_code ? exitChip(r.exit_code) : '') + '</span>' +
      '<span class="run-what" title="' + esc(R.headline(r)) + '">' + esc(R.headline(r) || (r.status === 'unknown' ? 'Stopped reporting after ' + R.stageName(r.last_stage || 'installing').toLowerCase() : '')) + '</span>' +
      '<span class="nowrap">' + (esc(R.osName(r)) || '<span class="muted">unknown</span>') + '</span>' +
      '<span class="run-host">' + (esc(r.cpu_model) || '<span class="muted">unknown</span>') + '</span>' +
      '<span class="num nowrap">' + esc(R.duration(r.duration)) + '</span></summary>' +
      '<div class="run-body">' + (facts.length ? '<dl class="kv">' + facts.map(function (f) { return '<div><dt>' + f[0] + '</dt><dd>' + f[1] + '</dd></div>'; }).join('') + '</dl>' : '') +
      (tail ? '<pre class="run-trace">' + esc(tail) + '</pre>'
        : r.status === 'failed' ? (r.traces_withheld ? R.unlockForm() : '<p class="muted">No trace was sent with this run.</p>') : '') +
      '<div class="rd-actions"><button type="button" class="btn" data-' + p + 'run="' + i + '">Open run</button>' +
      '<button type="button" class="btn" data-' + p + 'md="' + i + '">Copy as Markdown</button></div></div></details>';
  }

  function hostsFold(runs) {
    const by = {};
    const order = [];
    runs.forEach(function (r) {
      const k = [r.cpu_model, r.gpu_model, r.pve_version, r.kernel_version].join('|');
      if (!by[k]) { by[k] = { rep: r, n: 0, apps: {} }; order.push(k); }
      by[k].n++;
      by[k].apps[r.nsapp || 'unknown'] = true;
    });
    if (order.length < 2) return '';
    order.sort(function (a, b) { return by[b].n - by[a].n; });
    return '<details class="fold"><summary>Host setups <span class="count">' + order.length + '</span></summary><div class="table-wrapper"><table class="compact">' +
      '<thead><tr><th scope="col">CPU</th><th scope="col">GPU</th><th scope="col">Host</th><th scope="col">Kernel</th><th scope="col" class="num">Runs</th><th scope="col">Apps</th></tr></thead><tbody>' +
      order.slice(0, 20).map(function (k) {
        const g = by[k];
        const r = g.rep;
        return '<tr><td>' + (esc(r.cpu_model) || '<span class="muted">unknown</span>') + '</td><td>' + esc(r.gpu_model || '') + '</td><td class="nowrap">' + esc(R.hostName(r)) +
          '</td><td class="nowrap">' + esc(r.kernel_version || '') + '</td><td class="num strong">' + g.n + '</td><td>' + esc(Object.keys(g.apps).slice(0, 4).join(', ')) + '</td></tr>';
      }).join('') + '</tbody></table></div></details>';
  }

  // The grouped runs of a selection or a drill-down; p keeps the pane ('p') and the dialog ('d') apart.
  function failureView(m, p) {
    if (m.loading) return '<p class="empty">Loading the runs</p>';
    if (m.error) return '<p class="empty">Could not load the runs: ' + esc(m.error) + '</p>';
    if (!m.runs.length) return '<p class="empty">No ' + m.what + ' match the current filters.</p>';
    const g = GROUPS[m.group];
    const by = {};
    m.keys = [];
    m.runs.forEach(function (r) {
      const k = g.key(r);
      if (!by[k]) { by[k] = { rep: r, n: 0 }; m.keys.push(k); }
      by[k].n++;
    });
    m.keys.sort(function (a, b) { return by[b].n - by[a].n; });
    const top = m.keys.slice(0, 8);
    const max = by[top[0]].n;

    let html = m.total > m.runs.length
      ? '<p class="fv-note">These are the newest ' + m.runs.length + ' of ' + R.num(m.total) + ' ' + m.what + '. Groups and patterns count only these.</p>' : '';
    html += insights(m.runs);
    html += '<div class="fv-groupbar"><span>Group by</span><div class="seg">' + m.groups.map(function (k) {
      return '<button type="button" data-' + p + 'group="' + k + '" aria-pressed="' + (k === m.group) + '">' + GROUPS[k].name + '</button>';
    }).join('') + '</div><span class="muted">Select a group to filter the runs below.</span></div>';
    html += top.map(function (k, i) {
      const on = m.pick === k;
      return '<div class="grp-row"><button type="button" class="grp' + (on ? ' is-on' : '') + '" data-' + p + 'pick="' + i + '" aria-pressed="' + on + '">' +
        '<span class="grp-label">' + g.label(k, by[k].rep) + '</span><span class="grp-bar"><span style="width:' + Math.max(3, by[k].n / max * 100).toFixed(1) + '%"></span></span>' +
        '<span class="grp-n">' + by[k].n + '</span></button>' +
        (g.jump && !(m.kind === g.jump) ? '<button type="button" class="btn btn-xs" data-pick="' + g.jump + '" data-id="' + esc(k) + '" title="Open this ' + g.jump + ' in the error analysis above">Open</button>' : '') + '</div>';
    }).join('');
    if (m.keys.length > top.length) html += '<p class="muted fv-more">and ' + (m.keys.length - top.length) + ' smaller groups</p>';

    let n = 0;
    let rows = '';
    m.runs.forEach(function (r, i) {
      if (m.pick != null && g.key(r) !== m.pick) return;
      if (n++ < 50) rows += runRow(m, r, i, p);
    });
    html += '<h4 class="fv-h">' + m.what.charAt(0).toUpperCase() + m.what.slice(1) + ' <span class="count">' + n + '</span></h4>' +
      '<div class="runs' + (m.showApp ? ' with-app' : '') + '"><div class="runs-head"><span>Started</span>' + (m.showApp ? '<span>App</span>' : '') +
      '<span>Exit</span><span>What happened</span><span>OS</span><span>CPU</span><span class="num">Duration</span></div>' + rows + '</div>';
    if (n > 50) html += '<p class="muted fv-more">The list shows the newest 50. Select a group to narrow it.</p>';
    return html + hostsFold(m.pick == null ? m.runs : m.runs.filter(function (r) { return g.key(r) === m.pick; }));
  }

  function stat(label, value, cls) { return '<div' + (cls ? ' class="' + cls + '"' : '') + '><dt>' + label + '</dt><dd>' + value + '</dd></div>'; }
  function seen(t) { return t ? esc(R.dateTime(t)) + ' <small>' + esc(R.ago(t)) + '</small>' : '<span class="muted">none</span>'; }

  function paneHead() {
    if (pane.kind === 'app') {
      const a = triage.apps.filter(function (x) { return x.app === pane.id; })[0];
      const scoped = state.app === pane.id;
      let html = '<header class="pane-head"><div class="rd-title"><h3>' + esc(pane.id) + '</h3>' + (a ? R.typeBadge(a.type) + nowChip(a) : '') + '</div><div class="rd-actions">' +
        '<button type="button" class="btn" data-scope-app="' + esc(pane.id) + '">' + (scoped ? 'Show all apps again' : 'Filter the page to this app') + '</button>' +
        '<a class="btn" href="/script-analysis?app=' + encodeURIComponent(pane.id) + '">Script analysis</a>' +
        '<a class="btn" href="' + esc(errorsURL({ app: pane.id })) + '">Error analysis</a>' +
        (a && R.unlocked() ? '<button type="button" class="btn" data-issue="pane">Create issue</button>' : '') + '</div></header>';
      if (a) {
        const finished = a.success + a.failed;
        html += '<dl class="fv-stats">' + stat('Finished', R.num(finished)) + stat('Failed', R.num(a.failed), 'bad') +
          stat('Failure rate', R.pct(a.failure_rate), finished >= triage.minRuns ? sevClass(a.failure_rate) : '') + stat('Aborted', R.num(a.aborted), 'abort') +
          stat('Failed setups', R.num(a.setups_failed)) + '</dl>' +
          '<p class="fv-text">Last failure ' + seen(a.last_failure) + ', last success ' + seen(a.last_success) + '.</p>';
      }
      const done = (pane.recent || []).filter(function (r) { return r.status === 'success' || r.status === 'failed'; }).reverse();
      if (done.length > 1) {
        html += '<div class="run-strip"><span>Last ' + done.length + ' finished runs</span><span class="run-dots">' + done.map(function (r) {
          return '<i class="' + (r.status === 'failed' ? 'bad' : 'ok') + '" title="' + esc(R.dateTime(r.started_at) + ', ' + r.status) + '"></i>';
        }).join('') + '</span><span class="muted">oldest first</span></div>';
      }
      return html;
    }
    const c = triage.causes.filter(function (x) { return x.key === pane.id; })[0];
    const scoped = state.cause === pane.id;
    let html = '<header class="pane-head"><div class="rd-title"><h3 class="pane-cause' + (c && c.kind === 'command' ? ' is-code' : '') + '">' + esc(c ? c.message : causeMessage(pane.id)) + '</h3></div><div class="rd-actions">' +
      '<button type="button" class="btn" data-scope-cause="' + esc(pane.id) + '">' + (scoped ? 'Show all causes again' : 'Filter the page to this cause') + '</button>' +
      '<a class="btn" href="' + esc(errorsURL({ cause: pane.id })) + '">Error analysis</a>' +
      (c ? '<button type="button" class="btn" data-pcausemd="1">Copy as Markdown</button>' : '') +
      (c && R.unlocked() ? '<button type="button" class="btn" data-issue="pane">Create issue</button>' : '') + '</div></header>';
    if (c) {
      html += '<p class="pane-meta">' + (c.category ? '<span class="cat">' + esc(c.category) + '</span>' : '') + exitChip(c.exit_code) + ' ' + esc(c.exit_description || '') +
        (c.example_message ? '<span class="muted">Example: ' + esc(c.example_message) + '</span>' : '') + '</p>' +
        '<dl class="fv-stats">' + stat('Failures', R.num(c.failures), 'bad') + stat('Apps', R.num(c.apps)) + stat('Setups', R.num(c.setups)) +
        stat('Last 24 h', R.trend(c)) + '</dl><p class="fv-text">First seen ' + seen(c.first_seen) + ', last seen ' + seen(c.last_seen) + '.</p>';
    }
    return html;
  }

  function paneCauseMarkdown(c) {
    return R.causeMarkdown(c, pane.runs.slice(0, 3), {
      runsURL: location.origin + '/api/records?' + filterQuery().toString() + '&status=failed&cause=' + encodeURIComponent(c.key),
    });
  }

  // The open app as an issue text: its figures, what fails it and the newest failed runs.
  function paneAppMarkdown(a) {
    const counts = {};
    const order = [];
    pane.runs.forEach(function (r) {
      const k = runCause(r);
      if (!counts[k]) { counts[k] = 0; order.push(k); }
      counts[k]++;
    });
    order.sort(function (x, y) { return counts[y] - counts[x]; });
    let md = '## ' + a.app + ' (' + R.typeName(a.type) + ')\n\n' + [
      'Period: ' + (state.days === '1' ? 'today' : 'the last ' + state.days + ' days'),
      'Failed: ' + R.num(a.failed) + ' of ' + R.num(a.success + a.failed) + ' finished runs (' + R.pct(a.failure_rate) + ')',
      'Failing now: ' + (a.failing_now ? 'yes' : 'no'),
      'Distinct setups among the failures: ' + R.num(a.setups_failed),
      'Last failure: ' + (a.last_failure || 'none') + ', last success: ' + (a.last_success || 'none'),
    ].map(function (l) { return '- ' + l; }).join('\n') + '\n\n### Causes' +
      (pane.total > pane.runs.length ? ' among the newest ' + pane.runs.length + ' failed runs' : '') + '\n\n| Cause | Failed runs |\n| --- | --- |\n';
    order.forEach(function (k) { md += '| ' + R.mdCell(k) + ' | ' + counts[k] + ' |\n'; });
    pane.runs.slice(0, 3).forEach(function (r) { md += '\n' + R.runMarkdown(r, 3); });
    return md;
  }

  function renderPane() {
    $('triagePane').innerHTML = pane ? paneHead() + failureView(pane, 'p')
      : '<p class="empty">' + (triage.error ? '' : 'Nothing failed in this period. Select another period or source to look further back.') + '</p>';
  }

  async function loadPane() {
    const sel = triage.sel;
    if (!sel) { pane = null; renderPane(); return; }
    const mine = pane = {
      kind: sel.kind, id: sel.id, loading: true, runs: [], total: 0, recent: [], error: '', what: 'failed runs', showApp: sel.kind === 'cause',
      groups: sel.kind === 'app' ? ['cause', 'exit', 'os', 'host'] : ['app', 'os', 'host'], group: sel.kind === 'app' ? 'cause' : 'app', pick: null,
    };
    renderPane();
    const q = filterQuery();
    q.set(sel.kind, sel.id);
    q.set('type', 'install');
    q.set('sort', '-created');
    const failed = new URLSearchParams(q.toString());
    failed.set('status', 'failed');
    failed.set('limit', '100');
    const jobs = [R.getJSON('/api/records?' + failed.toString())];
    if (sel.kind === 'app') {
      q.set('limit', '60');
      jobs.push(R.getJSON('/api/records?' + q.toString()).catch(function () { return null; }));
    }
    try {
      const res = await Promise.all(jobs);
      mine.runs = res[0].records || [];
      mine.total = res[0].total || mine.runs.length;
      mine.recent = res[1] ? res[1].records || [] : [];
    } catch (e) {
      mine.error = e.message;
    }
    mine.loading = false;
    if (pane === mine) renderPane();
  }

  async function select(kind, id) {
    triage.sel = { kind: kind, id: id };
    triage.tab = kind === 'cause' ? 'causes' : 'apps';
    state.open = kind + ':' + id;
    writeURL();
    const inList = document.activeElement && document.activeElement.closest && document.activeElement.closest('#triageList');
    renderTriage();
    const on = document.querySelector ? document.querySelector('#triageList .is-on') : null;
    if (on && inList) on.focus();
    else if (on && on.scrollIntoView) on.scrollIntoView({ block: 'nearest' });
    await loadPane();
  }

  /* Drill-down dialog: the runs behind a card, a matrix cell or a breakdown row. */

  function renderDrill() {
    $('drillBody').innerHTML = '<header class="rd-head"><div class="rd-title"><h2 id="drillTitle">' + esc(drill.title) + '</h2>' +
      (drill.loading ? '' : '<span class="chip chip-' + drill.chip + '">' + R.runs(drill.total) + '</span>') + '</div>' +
      (drill.note ? '<p class="fv-text">' + esc(drill.note) + '</p>' : '') + '</header>' + failureView(drill, 'd');
    const body = $('drillBody');
    if (body.focus && (!document.activeElement || document.activeElement === document.body)) body.focus();
  }

  async function openDrill(spec) {
    const mine = drill = {
      title: spec.title, note: spec.note || '', chip: spec.chip || 'failed', what: spec.what || 'failed runs', showApp: !spec.params.app,
      loading: true, runs: [], total: 0, error: '', groups: spec.groups, group: spec.groups[0], pick: null,
    };
    show('drillDialog');
    renderDrill();
    const q = filterQuery();
    q.set('type', 'install');
    Object.keys(spec.params).forEach(function (k) { q.set(k, spec.params[k]); });
    q.set('limit', '100');
    q.set('sort', '-created');
    try {
      const data = await R.getJSON('/api/records?' + q.toString());
      mine.runs = data.records || [];
      mine.total = data.total || mine.runs.length;
    } catch (e) {
      mine.error = e.message;
    }
    mine.loading = false;
    if (drill === mine) renderDrill();
  }

  const DRILLS = {
    failed: { title: 'Failed runs', params: { status: 'failed' }, groups: ['cause', 'app', 'exit', 'os', 'host'] },
    aborted: {
      title: 'Aborted runs', params: { status: 'aborted' }, groups: ['exit', 'app', 'stage'], chip: 'aborted', what: 'aborted runs',
      note: 'Cancelled or declined by the user. These do not count as failures, but many at one stage can point at a confusing prompt.',
    },
    unknown: {
      title: 'Runs without a result', params: { status: 'unknown' }, groups: ['stage', 'app', 'os'], chip: 'noresult', what: 'runs without a result',
      note: 'These runs stopped reporting before a result, usually because the host lost the session.',
    },
  };

  function renderMatrix(m) {
    cells = [];
    if (!m || !m.apps || !m.apps.length) {
      $('matrix').innerHTML = '<p class="empty">No failed runs in this period.</p>';
      return;
    }
    const count = {};
    let max = 1;
    m.cells.forEach(function (c) {
      count[c.app + '\n' + c.cause] = c.failures;
      if (c.failures > max) max = c.failures;
    });
    const other = m.apps.some(function (a) { return a.other > 0; });
    const shown = m.causes.reduce(function (sum, c) { return sum + c.failures; }, 0);
    $('matrix').innerHTML = '<table class="matrix"><thead><tr><th scope="col">App</th>' + m.causes.map(function (c) {
      const text = c.kind === 'exit_code' ? String(c.message).replace(/^Exit \d+: /, '') : c.message;
      return '<th scope="col"><button type="button" class="mx-col' + (state.cause === c.key ? ' is-on' : '') + '" data-scope-cause="' + esc(c.key) + '" title="' + esc(c.message) + '">' +
        exitChip(c.exit_code) + '<span class="mx-msg' + (c.kind === 'command' ? ' is-code' : '') + '">' + esc(text) + '</span></button></th>';
    }).join('') + (other ? '<th scope="col" class="mx-plain">Other causes</th>' : '') + '<th scope="col" class="num">Failed</th></tr></thead><tbody>' +
      m.apps.map(function (a) {
        return '<tr><th scope="row"><button type="button" class="mx-row' + (state.app === a.app ? ' is-on' : '') + '" data-scope-app="' + esc(a.app) + '">' + esc(a.app) + '</button> ' + R.typeBadge(a.type) + '</th>' +
          m.causes.map(function (c) {
            const n = count[a.app + '\n' + c.key] || 0;
            if (!n) return '<td class="mx-cell"></td>';
            cells.push({ app: a.app, cause: c.key, label: c.message });
            return '<td class="mx-cell"><button type="button" data-cell="' + (cells.length - 1) + '" style="--heat:' + (0.18 + 0.62 * n / max).toFixed(2) + '" title="' +
              esc(R.runs(n) + ' of ' + a.app + ' failed with: ' + c.message) + '"><span>' + R.num(n) + '</span></button></td>';
          }).join('') + (other ? '<td class="mx-cell mx-plain">' + (a.other ? R.num(a.other) : '') + '</td>' : '') + '<td class="num strong bad">' + R.num(a.failures) + '</td></tr>';
      }).join('') + '</tbody><tfoot><tr><th scope="row">All apps</th>' + m.causes.map(function (c) { return '<td class="mx-cell">' + R.num(c.failures) + '</td>'; }).join('') +
      (other ? '<td class="mx-cell">' + R.num(m.failures - shown) + '</td>' : '') + '<td class="num strong">' + R.num(m.failures) + '</td></tr></tfoot></table>';
  }

  function renderTimeline(d) {
    const points = (d.timeline || []).map(function (p) {
      return { t: p.t, failed: p.failed, aborted: p.aborted, rest: Math.max(0, p.runs - p.failed - p.aborted) };
    });
    const any = points.some(function (p) { return p.failed + p.aborted + p.rest > 0; });
    $('timelineSection').hidden = !any;
    if (!any) return;
    const hourly = d.bucket === 'hour';
    $('timelineNote').textContent = hourly ? 'By the hour they started, shown in your time zone. The day starts at 00:00 UTC.' : 'By the day they started.';
    $('timeline').innerHTML = R.columnsSVG(points, [
      { key: 'failed', cls: 'c-bad', name: 'failed' },
      { key: 'aborted', cls: 'c-abort', name: 'aborted' },
      { key: 'rest', cls: 'c-runs', name: 'other' },
    ], {
      title: 'Runs over time',
      label: function (p) {
        if (!hourly) return p.t.slice(5);
        const t = R.parseTime(p.t);
        return t ? t.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '';
      },
    }) + '<div class="legend"><span class="key c-bad"></span>Failed <span class="key c-abort"></span>Aborted <span class="key c-runs"></span>Other runs</div>';
  }

  function rateCell(r, button) {
    const finished = r.success + r.failed;
    if (!finished) return '<td class="num muted">none finished</td>';
    const sev = sevClass(r.failure_rate);
    return '<td class="num ' + sev + '">' + (button && r.failed ? button + R.pct(r.failure_rate) + '</button>' : R.pct(r.failure_rate)) + '</td>';
  }

  function renderTopApps(apps) {
    if (!apps || !apps.length) {
      $('topApps').innerHTML = '<p class="empty">No runs in this period.</p>';
      return;
    }
    $('topApps').innerHTML = '<table class="compact"><thead><tr><th scope="col" class="num">Rank</th><th scope="col">App</th><th scope="col" class="num">Runs</th>' +
      '<th scope="col" class="num">Failure rate</th></tr></thead><tbody>' + apps.map(function (a, i) {
        return '<tr><td class="num muted">' + (i + 1) + '</td><td><a href="/script-analysis?app=' + encodeURIComponent(a.label) + '">' + esc(a.label) +
          '</a></td><td class="num">' + R.num(a.runs) + '</td>' +
          rateCell(a, '<button type="button" class="link-btn" data-pick="app" data-id="' + esc(a.label) + '" title="Show the ' + R.runs(a.failed) + ' that failed">') + '</tr>';
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
          const button = r.label && r.label !== 'unknown'
            ? '<button type="button" class="link-btn" data-bdim="' + dim + '" data-bval="' + esc(r.label) + '" title="Show the ' + R.runs(r.failed) + ' that failed">' : '';
          return '<tr><td>' + esc(label(dim, r.label)) + '</td><td class="num">' + R.num(r.runs) + '</td><td class="num"><span class="share"><span style="width:' +
            (r.runs / total * 100).toFixed(1) + '%"></span></span>' + Math.round(r.runs / total * 100) + '%</td>' + rateCell(r, button) + '</tr>';
        }).join('') : '<tr><td colspan="4" class="empty">No runs</td></tr>') + '</tbody></table>';
    }).join('');
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
    const d = await R.getJSON('/api/dashboard?' + filterQuery().toString());
    dash = d;
    renderCards(d);
    renderMatrix(d.failure_matrix);
    renderTimeline(d);
    renderTopApps(d.apps);
    renderBreakdown(d.breakdown);
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

  // The timer passes keepPane, so a refresh does not collapse the runs someone is reading.
  async function refresh(opts) {
    writeURL();
    syncControls();
    $('refreshBtn').disabled = true;
    try {
      await loadSlugs();
      await Promise.all([loadDashboard(), loadTriage(), loadLog()]);
      settleSelection();
      renderScope();
      renderTriage();
      if (!(opts && opts.keepPane && pane && triage.sel && pane.kind === triage.sel.kind && pane.id === triage.sel.id)) await loadPane();
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
    timer = on ? setInterval(function () { refresh({ keepPane: true }); }, 30000) : null;
  }

  function viewClick(t, m, p, redraw) {
    const group = t.closest('[data-' + p + 'group]');
    if (group) {
      m.group = group.getAttribute('data-' + p + 'group');
      m.pick = null;
      redraw();
      return true;
    }
    const pick = t.closest('[data-' + p + 'pick]');
    if (pick) {
      const key = m.keys[Number(pick.getAttribute('data-' + p + 'pick'))];
      m.pick = m.pick === key ? null : key;
      redraw();
      return true;
    }
    const run = t.closest('[data-' + p + 'run]');
    if (run) {
      const rec = m.runs[Number(run.getAttribute('data-' + p + 'run'))];
      if (rec) R.openRecord(rec);
      return true;
    }
    const md = t.closest('[data-' + p + 'md]');
    if (md) {
      const rec = m.runs[Number(md.getAttribute('data-' + p + 'md'))];
      if (rec) R.copyText(R.runMarkdown(rec, 3), md);
      return true;
    }
    return false;
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
      const unscope = t.closest('[data-unscope]');
      if (unscope) { setScope(unscope.getAttribute('data-unscope'), state[unscope.getAttribute('data-unscope')]); return; }
      const scopeApp = t.closest('[data-scope-app]');
      if (scopeApp) { setScope('app', scopeApp.getAttribute('data-scope-app')); return; }
      const scopeCause = t.closest('[data-scope-cause]');
      if (scopeCause) { setScope('cause', scopeCause.getAttribute('data-scope-cause')); return; }

      const tab = t.closest('[data-tab]');
      if (tab) {
        triage.tab = tab.getAttribute('data-tab');
        triage.query = '';
        const first = listed()[0];
        if (first) select(first.kind, first.id); else renderTriage();
        return;
      }
      if (t.closest('[data-now]')) { triage.failingNow = !triage.failingNow; renderTriage(); return; }
      const pick = t.closest('[data-pick]');
      if (pick) {
        const dlg = $('drillDialog');
        if (dlg.open && dlg.close) dlg.close();
        triage.query = '';
        triage.failingNow = false;
        select(pick.getAttribute('data-pick'), pick.getAttribute('data-id'));
        const section = $('triageSection');
        if (!t.closest('#triageSection') && section.scrollIntoView) section.scrollIntoView({ block: 'start' });
        return;
      }
      if (pane && viewClick(t, pane, 'p', renderPane)) return;
      if (drill && viewClick(t, drill, 'd', renderDrill)) return;
      if (t.closest('[data-pcausemd]') && pane) {
        const c = triage.causes.filter(function (x) { return x.key === pane.id; })[0];
        if (c) R.copyText(paneCauseMarkdown(c), t.closest('[data-pcausemd]'));
        return;
      }
      if (t.closest('[data-issue="pane"]') && pane) {
        if (pane.kind === 'cause') {
          const c = triage.causes.filter(function (x) { return x.key === pane.id; })[0];
          if (c) R.openIssue('[Telemetry] ' + c.message, paneCauseMarkdown(c));
        } else {
          const a = triage.apps.filter(function (x) { return x.app === pane.id; })[0];
          if (a) R.openIssue('[Telemetry] ' + a.app + ': ' + (a.top_cause_message || 'failed runs'), paneAppMarkdown(a));
        }
        return;
      }

      const kpi = t.closest('[data-drill]');
      if (kpi) { openDrill(DRILLS[kpi.getAttribute('data-drill')]); return; }
      const cell = t.closest('[data-cell]');
      if (cell) {
        const c = cells[Number(cell.getAttribute('data-cell'))];
        if (c) openDrill({ title: c.app + ': ' + c.label, params: { status: 'failed', app: c.app, cause: c.cause }, groups: ['os', 'host', 'exit'] });
        return;
      }
      const dim = t.closest('[data-bdim]');
      if (dim) {
        const name = dim.getAttribute('data-bdim');
        const value = dim.getAttribute('data-bval');
        const params = { status: 'failed' };
        params[name] = value;
        const shown = name === 'platform' ? R.platformName(value) : name === 'type' ? R.typeName(value) : value;
        openDrill({ title: 'Failed runs on ' + shown, params: params, groups: ['cause', 'app', 'exit', 'host'] });
        return;
      }

      const row = t.closest('[data-run]');
      if (row && !t.closest('a')) {
        const rec = records[Number(row.getAttribute('data-run'))];
        if (rec) R.openRecord(rec);
      }
    });

    // Arrow keys walk the triage list while it holds the focus.
    document.addEventListener('keydown', function (e) {
      if ((e.key !== 'ArrowDown' && e.key !== 'ArrowUp') || !e.target || !e.target.closest || !e.target.closest('#triageList')) return;
      const list = listed();
      if (!list.length) return;
      e.preventDefault();
      let i = -1;
      list.forEach(function (x, n) { if (isSelected(x.kind, x.id)) i = n; });
      const next = list[Math.max(0, Math.min(list.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))];
      if (!isSelected(next.kind, next.id)) select(next.kind, next.id);
    });

    $('fSlug').addEventListener('change', function () { state.slug = this.value; state.page = 1; refresh(); });
    $('refreshBtn').addEventListener('click', function () { refresh(); });
    $('csvBtn').addEventListener('click', exportCSV);
    $('triageSearch').addEventListener('input', function () { triage.query = this.value.trim(); renderTriage(); });
    $('triageSort').addEventListener('change', function () { triage.sort = this.value; renderTriage(); });
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
    // With traces readable a cause is its message, so an exit-code key no longer names one.
    R.onUnlock(function () {
      state.cause = '';
      if (triage.sel && triage.sel.kind === 'cause') { triage.sel = null; state.open = ''; }
      refresh();
    });
    $('autoRefresh').checked = auto;
    setAutoRefresh(auto);
    return refresh();
  }

  window.Dashboard = {
    init: init, state: state, triage: triage, records: function () { return records; },
    pane: function () { return pane; }, drill: function () { return drill; },
  };
  if (!window.__NO_AUTOSTART__) init();
})();

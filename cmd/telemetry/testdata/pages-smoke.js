// Renders the dashboard, the error page and the scripts page against API
// fixtures and checks what they show. Run from cmd/telemetry: node testdata/pages-smoke.js <fixtures dir>
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const pub = path.join(__dirname, '..', 'public');
const fixtures = process.argv[2];
if (!fixtures) {
  console.error('usage: node testdata/pages-smoke.js <fixtures dir>');
  process.exit(2);
}
const fixture = (n) => JSON.parse(fs.readFileSync(path.join(fixtures, n), 'utf8'));
const allRecords = fixture('records.json').records;

let failures = 0;
const fail = (msg) => { console.error('  ' + msg); failures++; };
const expect = (cond, msg) => { if (!cond) fail(msg); };

// Answers like the API: /api/records honours the run filters the pages send.
function api(url) {
  const u = new URL(url, 'http://localhost');
  const q = u.searchParams;
  switch (u.pathname) {
    case '/api/dashboard': return fixture('dashboard.json');
    case '/api/errors': return fixture('errors.json');
    case '/api/causes': return fixture('causes.json');
    case '/api/app-errors': return fixture('app-errors.json');
    case '/api/repo-slugs': return fixture('repo-slugs.json');
    case '/api/scripts': return fixture('scripts.json');
    case '/api/script-detail': return q.get('app') === 'immich' ? fixture('script-detail.json') : null;
    case '/api/records': {
      let recs = allRecords.filter((r) =>
        (!q.get('status') || r.status === q.get('status')) &&
        (!q.get('cause') || r.cause === q.get('cause')) &&
        (!q.get('app') || r.nsapp === q.get('app')) &&
        (!q.get('exit_code') || String(r.exit_code || 0) === q.get('exit_code')) &&
        (!q.get('search') || r.nsapp.includes(q.get('search'))));
      const limit = Number(q.get('limit') || 50), page = Number(q.get('page') || 1);
      const total = recs.length;
      recs = recs.slice((page - 1) * limit, page * limit);
      return { records: recs, page, limit, total, total_pages: Math.max(1, Math.ceil(total / limit)) };
    }
  }
  return null;
}

function page(template, scripts, search) {
  const html = fs.readFileSync(path.join(pub, 'templates', template), 'utf8');
  const ids = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map((m) => m[1]));
  const elements = {};
  const listeners = {};
  const missing = new Set();
  const element = (id) => {
    const el = {
      id, innerHTML: '', textContent: '', value: '', hidden: false, disabled: false, checked: false, open: false,
      href: '', attrs: {}, handlers: {},
      setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'open') this.open = true; },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; if (k === 'open') this.open = false; },
      addEventListener(type, fn) { (this.handlers[type] ||= []).push(fn); },
      showModal() { this.open = true; },
      close() { this.open = false; },
      focus() {}, select() {}, remove() {}, click() {},
      querySelectorAll() { return []; },
    };
    return el;
  };
  const downloads = [];
  const clipboard = [];
  const document = {
    documentElement: element('html'),
    body: { appendChild() {} },
    getElementById(id) {
      if (!ids.has(id)) { missing.add(id); }
      return (elements[id] ||= element(id));
    },
    querySelectorAll() { return []; },
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    createElement(tag) {
      const el = element(tag);
      if (tag === 'a') el.click = function () { downloads.push({ name: this.download, href: this.href }); };
      return el;
    },
    execCommand() { return true; },
  };
  const blobs = {};
  const ctx = {
    document, console, setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
    URLSearchParams, URL: Object.assign(function (u, b) { return new URL(u, b); }, {
      createObjectURL(b) { const k = 'blob:' + Object.keys(blobs).length; blobs[k] = b; return k; }, revokeObjectURL() {},
    }),
    Blob, Promise, JSON, Date, Math, Number, String, Array, Object, Set, Error, RegExp, isNaN,
    location: { pathname: template === 'dashboard.html' ? '/' : '/' + template.replace('.html', ''), search: search || '', origin: 'http://localhost', href: 'http://localhost/' },
    history: { replaceState(_s, _t, url) { ctx.location.search = url.includes('?') ? url.slice(url.indexOf('?')) : ''; } },
    localStorage: { getItem: () => null, setItem() {} },
    navigator: { clipboard: { writeText(t) { clipboard.push(t); return Promise.resolve(); } } },
    isSecureContext: true,
    __NO_AUTOSTART__: true,
    fetch: async (url) => {
      const body = api(url);
      if (!body) return { ok: false, status: 404, text: async () => 'no fixture for ' + url, json: async () => ({}) };
      return { ok: true, status: 200, text: async () => JSON.stringify(body), json: async () => JSON.parse(JSON.stringify(body)) };
    },
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  for (const s of scripts) {
    vm.runInContext(fs.readFileSync(path.join(pub, 'static', 'js', s), 'utf8'), ctx, { filename: s });
  }
  const click = (target) => listeners.click.forEach((fn) => fn({ target, preventDefault() {} }));
  // A fake event target whose closest() answers for one selector.
  const target = (selector, attrs) => ({
    tagName: 'BUTTON',
    closest(sel) { return sel === selector ? { getAttribute: (k) => (k in attrs ? attrs[k] : null), closest: () => null } : null; },
  });
  // Ids the scripts render themselves are fine once some markup carries them.
  const unknown = () => [...missing].filter((id) => !Object.values(elements).some((e) => String(e.innerHTML).includes('id="' + id + '"')));
  return { ctx, el: (id) => document.getElementById(id), missing: unknown, click, target, downloads, blobs, clipboard };
}

const tick = () => new Promise((r) => setTimeout(r, 20));

async function dashboard() {
  const p = page('dashboard.html', ['runs.js', 'dashboard.js']);
  p.ctx.Dashboard.state.limit = 100;
  await p.ctx.Dashboard.init();
  const cards = p.el('cards').innerHTML;
  for (const s of ['Runs', 'Success rate', 'Failed', 'Aborted', 'cancelled or declined', 'No result', 'Most popular', 'still running']) {
    expect(cards.includes(s), 'cards lack ' + JSON.stringify(s));
  }
  expect(!cards.includes('Ctrl+C'), 'aborted is not only Ctrl+C');
  expect(p.el('topCauses').innerHTML.includes('npm ERR! code ENOSPC'), 'top causes lack the npm cause');
  expect(p.el('topCauses').innerHTML.includes('/error-analysis?'), 'causes do not link into the error page');
  const failing = p.el('failingApps').innerHTML;
  expect(failing.includes('immich') && failing.includes('Failing'), 'highest failure rates lack immich failing now');
  expect(p.el('topApps').innerHTML.includes('docker'), 'top applications lack docker');
  expect(p.el('breakdown').innerHTML.includes('Platform') && p.el('breakdown').innerHTML.includes('Proxmox VE'), 'breakdown lacks platforms');
  expect(p.el('dailySection').hidden === true, 'the daily chart shows for a one-day period');

  const log = p.el('logBody').innerHTML;
  const recs = p.ctx.Dashboard.records();
  const running = recs.findIndex((r) => r.execution_id === 'dock-run');
  expect(running >= 0, 'the running docker run is missing from the log');
  const rowOf = (i) => log.split('<tr ').find((r) => r.includes('data-run="' + i + '"')) || '';
  const runRow = rowOf(running);
  expect(runRow.includes('In progress') && runRow.includes('configuring'), 'in-progress row lacks its stage: ' + runRow.slice(0, 200));
  expect(runRow.includes('debian 13') && runRow.includes('2 cores'), 'in-progress row lacks merged metadata');
  expect(log.includes('chip-noresult') && log.includes('No result'), 'log lacks a no result chip');
  expect(log.includes('Stopped reporting after configuring'), 'no result row lacks its last stage');
  expect(log.includes('E: Unable to locate package docker-compose-plugin'), 'failed row lacks its cause line');
  expect(log.includes('E: Unable to locate package libvips-dev'), 'old-format row lacks the derived message');
  expect(!log.includes('exit_code='), 'a raw exit_code header reached the log');
  expect(p.el('logInfo').textContent.includes('runs'), 'pagination info is empty');

  const failed = recs.findIndex((r) => r.execution_id === 'immich-0');
  p.click(p.target('[data-run]', { 'data-run': String(failed) }));
  const dlg = p.el('recordBody').innerHTML;
  expect(p.el('recordDialog').open, 'the record dialog did not open');
  for (const s of ['Timeline', 'Time in stage', 'Configuring', 'What went wrong', 'npm ERR! code ENOSPC', 'Building Immich', 'Container',
    'setup_nodejs', '$STD npm ci', 'at line 132', '239: ', 'Copy trace', 'Copy as Markdown', 'Kernel', 'Execution ID', 'Random ID', 'Payload version']) {
    expect(dlg.includes(s), 'record dialog lacks ' + JSON.stringify(s));
  }
  expect(/<pre>[^<]*\nnpm ERR! syscall write/.test(dlg), 'the trace lost its line breaks');
  p.click(p.target('[data-copy]', { 'data-copy': 'markdown' }));
  await tick();
  const md = p.clipboard.pop() || '';
  for (const s of ['### immich: npm ERR! code ENOSPC', '| Field | Value |', '| Command | `$STD npm ci` (line 132) |', '```text', 'npm ERR! errno -28']) {
    expect(md.includes(s), 'run Markdown lacks ' + JSON.stringify(s));
  }

  const silent = recs.findIndex((r) => r.status === 'unknown');
  p.click(p.target('[data-run]', { 'data-run': String(silent) }));
  expect(p.el('recordBody').innerHTML.includes('No final status was received; the run stopped reporting after configuring at'),
    'the no result dialog does not explain itself');
  expect(p.el('recordBody').innerHTML.includes('tl-silent'), 'the no result timeline does not mark the silent stage');
  return p.missing();
}

async function errorPage() {
  const p = page('error-analysis.html', ['runs.js', 'error-analysis.js']);
  await p.ctx.ErrorPage.init();
  const sum = p.el('summary').innerHTML;
  for (const s of ['Failed runs', 'Failure rate', 'No result', 'Still running', 'Apps failing']) {
    expect(sum.includes(s), 'summary lacks ' + JSON.stringify(s));
  }
  expect(!p.el('stuckBanner').hidden && p.el('stuckBanner').innerHTML.includes('still running'), 'the stuck banner does not split running runs');
  const causes = p.el('causesBody').innerHTML;
  expect(causes.includes('npm ERR! code ENOSPC'), 'causes lack the npm cause');
  expect(causes.includes('$STD apt-get install -y libvips-dev') && causes.includes('Example: E: Unable to locate package libvips-dev'),
    'the old-format cause lacks its command or example message');
  expect(!causes.includes('exit_code='), 'a raw exit_code header reached the cause table');
  expect(p.el('appsBody').innerHTML.includes('Failing now'), 'apps lack failing now');
  expect(p.el('exitBody').innerHTML.includes('239'), 'exit codes lack 239');
  expect(p.el('failChart').innerHTML.includes('<svg'), 'no failures chart');
  expect(p.el('apiLink').href.startsWith('/api/errors?'), 'the API link is missing');

  const data = p.ctx.ErrorPage.data();
  const idx = data.causes.findIndex((c) => c.key === 'npm ERR! code ENOSPC');
  p.click(p.target('[data-open]', { 'data-open': 'cause', 'data-idx': String(idx) }));
  await tick();
  expect(p.ctx.location.search.includes('cause='), 'opening a cause does not reach the URL');
  const drill = p.el('drillBox').innerHTML;
  expect(drill.includes('Failed runs with this cause') && drill.includes('3 runs'), 'the cause drill-down lacks its runs: ' + drill.slice(0, 200));
  expect(drill.includes('Intel N100') && drill.includes('Copy as Markdown'), 'the drill-down lacks setups or the Markdown action');
  const md = p.ctx.ErrorPage.drillMarkdown();
  for (const s of ['## npm ERR! code ENOSPC', 'Distinct setups: 3', 'All runs: http://localhost/api/records?', '### immich:']) {
    expect(md.includes(s), 'cause Markdown lacks ' + JSON.stringify(s));
  }
  p.click(p.target('[data-drun]', { 'data-drun': '0' }));
  expect(p.el('recordDialog').open && p.el('recordBody').innerHTML.includes('What went wrong'), 'a drill-down run does not open the record dialog');

  const out = await p.ctx.ErrorPage.exportView();
  expect(out && out.causes.length && out.causes[0].example_runs && out.causes[0].example_runs[0].error, 'the export lacks example runs with traces');
  expect(p.downloads.length === 1 && /^telemetry-errors-\d+d-/.test(p.downloads[0].name), 'the export did not download');

  const linked = page('error-analysis.html', ['runs.js', 'error-analysis.js'], '?days=1&app=immich');
  await linked.ctx.ErrorPage.init();
  await tick();
  expect(linked.el('drillBox').innerHTML.includes('Failed runs of immich'), 'a linked app does not open its drill-down');
  return new Set([...p.missing(), ...linked.missing()]);
}

async function scriptsPage() {
  const p = page('script-analysis.html', ['runs.js', 'script-analysis.js']);
  await p.ctx.ScriptsPage.init();
  const cards = p.el('cards').innerHTML;
  for (const s of ['Scripts', 'Runs', 'Failure rate', 'Failing now']) {
    expect(cards.includes(s), 'scripts cards lack ' + JSON.stringify(s));
  }
  const body = p.el('scriptsBody').innerHTML;
  expect(body.includes('docker') && body.includes('immich'), 'the scripts table lacks docker or immich');
  expect(body.includes('outcome-bar') && body.includes('chip-failed'), 'the scripts table lacks outcome bars or the failing chip');
  expect(body.includes('npm ERR! code ENOSPC'), 'the immich row lacks its last failure cause');
  expect(p.el('scriptsInfo').textContent.startsWith('Showing'), 'the table footer is empty');
  expect(p.el('apiLink').href.startsWith('/api/scripts?'), 'the JSON link is missing');

  p.click(p.target('.th-sort[data-ssort]', { 'data-ssort': 'failure_rate' }));
  expect(p.ctx.location.search.includes('sort=-failure_rate'), 'sorting does not reach the URL');
  const first = p.ctx.ScriptsPage.rows()[0];
  expect(first && first.failure_rate === Math.max(...p.ctx.ScriptsPage.data().top_scripts.map((s) => s.failure_rate)),
    'sorting by failure rate does not put the highest rate first');

  const idx = p.ctx.ScriptsPage.rows().findIndex((s) => s.app === 'immich');
  p.click(p.target('[data-script]', { 'data-script': String(idx) }));
  await tick();
  expect(p.el('scriptDialog').open, 'the script dialog did not open');
  expect(p.ctx.location.search.includes('app=immich'), 'the open script does not reach the URL');
  const dlg = p.el('scriptBody').innerHTML;
  for (const s of ['immich', 'Failing', 'Success rate', 'Failed setups', 'What fails', 'npm ERR! code ENOSPC', 'Recent runs',
    'Runs per hour', '<svg', 'Platform', 'Copy as Markdown', '/error-analysis?', 'cause=']) {
    expect(dlg.includes(s), 'script dialog lacks ' + JSON.stringify(s));
  }
  expect(!dlg.includes('exit_code='), 'a raw exit_code header reached the script dialog');
  const md = p.ctx.ScriptsPage.detailMarkdown();
  for (const s of ['## immich (LXC), today', 'Failing now: yes', '### Causes', '| npm ERR! code ENOSPC |', '### Recent runs', 'Data: http://localhost/api/script-detail?']) {
    expect(md.includes(s), 'script Markdown lacks ' + JSON.stringify(s));
  }
  const failedRun = p.ctx.ScriptsPage.detail().recent.findIndex((r) => r.status === 'failed');
  p.click(p.target('[data-srun]', { 'data-srun': String(failedRun) }));
  expect(p.el('recordDialog').open && p.el('recordBody').innerHTML.includes('What went wrong'), 'a recent run does not open the record dialog');

  const linked = page('script-analysis.html', ['runs.js', 'script-analysis.js'], '?days=1&app=immich');
  await linked.ctx.ScriptsPage.init();
  await tick();
  expect(linked.el('scriptDialog').open && linked.el('scriptBody').innerHTML.includes('What fails'), 'a linked script does not open its dialog');
  return new Set([...p.missing(), ...linked.missing()]);
}

(async () => {
  const missing = new Set([...(await dashboard()), ...(await errorPage()), ...(await scriptsPage())]);
  for (const id of missing) fail('a script reads #' + id + ', which its template does not have');
  if (failures) {
    console.error('  ' + failures + ' problem(s)');
    process.exit(1);
  }
  console.log('  pages render: dashboard cards, log, record dialog, error page tables, drill-down and export, scripts table and script dialog');
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });

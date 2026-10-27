(function () {
  'use strict';

  const STATUS = {
    success: { label: 'Success', cls: 'success' },
    failed: { label: 'Failed', cls: 'failed' },
    aborted: { label: 'Aborted', cls: 'aborted' },
    in_progress: { label: 'In progress', cls: 'running' },
    unknown: { label: 'No result', cls: 'noresult' },
  };
  const STAGES = {
    installing: 'Installing', validation: 'Validation', configuring: 'Configuring',
    success: 'Success', failed: 'Failed', aborted: 'Aborted', unknown: 'No result',
  };
  const TYPES = { lxc: 'LXC', vm: 'VM', turnkey: 'TurnKey', pve: 'Tool', tool: 'Tool', addon: 'Add-on' };

  function esc(v) {
    return String(v == null ? '' : v).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }
  function num(n) { return (Number(n) || 0).toLocaleString('en-US'); }
  function pct(x) { return (Number(x) || 0).toFixed(1) + '%'; }
  function runs(n) { return num(n) + (Number(n) === 1 ? ' run' : ' runs'); }

  function duration(sec) {
    sec = Math.round(Number(sec) || 0);
    if (sec <= 0) return '0s';
    if (sec < 60) return sec + 's';
    const m = Math.floor(sec / 60), s = sec % 60;
    if (m < 60) return m + 'm ' + String(s).padStart(2, '0') + 's';
    const h = Math.floor(m / 60);
    if (h < 48) return h + 'h ' + String(m % 60).padStart(2, '0') + 'm';
    return Math.floor(h / 24) + 'd ' + (h % 24) + 'h';
  }

  function parseTime(t) {
    if (!t) return null;
    const s = String(t);
    const d = new Date(/[TZ]/.test(s) ? s : s.replace(' ', 'T') + 'Z');
    return isNaN(d.getTime()) ? null : d;
  }
  function dateTime(t) {
    const d = parseTime(t);
    return d ? d.toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : '';
  }
  function clock(t) {
    const d = parseTime(t);
    return d ? d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '';
  }
  function ago(t, now) {
    const d = parseTime(t);
    if (!d) return '';
    const s = Math.round(((now || Date.now()) - d.getTime()) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + ' min ago';
    if (s < 172800) return Math.floor(s / 3600) + ' h ago';
    return Math.floor(s / 86400) + ' days ago';
  }

  function stageName(s) { return STAGES[s] || s || ''; }
  function typeName(t) { return TYPES[t] || (t ? t : 'Unknown'); }
  function typeBadge(t) {
    return '<span class="type-badge ' + esc(t || '') + '">' + esc(typeName(t)) + '</span>';
  }
  function platformName(p) { return p === 'incus' ? 'Incus' : p === 'pve' ? 'Proxmox VE' : 'Unknown'; }

  function statusChip(rec) {
    const s = STATUS[rec.status] || { label: rec.status || 'Unknown', cls: 'noresult' };
    let html = '<span class="chip chip-' + s.cls + '">' + s.label + '</span>';
    if (rec.status === 'in_progress' && rec.last_stage) {
      html += ' <span class="chip-note">' + esc(stageName(rec.last_stage).toLowerCase()) + '</span>';
    }
    return html;
  }

  // What went wrong, never the raw "exit_code=N |" header.
  function headline(rec) {
    if (rec.error_message) return rec.error_message;
    if (rec.status === 'failed' || rec.status === 'aborted') {
      if (rec.exit_code) return 'Exit ' + rec.exit_code + ': ' + (rec.exit_description || 'no description');
      return 'The run sent no error details';
    }
    return '';
  }

  function traceText(err) {
    const s = String(err || '');
    if (s.indexOf('\n---\n') === -1 && s.indexOf('|---|') !== -1) {
      const i = s.indexOf('|---|');
      return s.slice(0, i) + '\n---\n' + s.slice(i + 5).split('|').join('\n');
    }
    return s;
  }

  function resources(rec) {
    const parts = [];
    if (rec.core_count) parts.push(rec.core_count + (rec.core_count === 1 ? ' core' : ' cores'));
    if (rec.ram_size) parts.push(rec.ram_size >= 1024 ? +(rec.ram_size / 1024).toFixed(1) + ' GB RAM' : rec.ram_size + ' MB RAM');
    if (rec.disk_size) parts.push(rec.disk_size + ' GB disk');
    return parts.join(', ');
  }
  function osName(rec) { return rec.os_type ? rec.os_type + (rec.os_version ? ' ' + rec.os_version : '') : ''; }
  function hostName(rec) {
    if (!rec.pve_version) return '';
    return rec.platform === 'incus' ? rec.pve_version.replace(/^incus-?/, 'Incus ') : 'PVE ' + rec.pve_version;
  }
  function setupName(rec) {
    return [rec.cpu_model, rec.gpu_model].filter(Boolean).join(', ');
  }

  // Stage segments sized by the time spent in each.
  function stageTimeline(rec, now) {
    const stages = rec.stages || [];
    if (!stages.length) {
      return '<p class="muted">No stage timestamps were recorded for this run.</p>';
    }
    const segs = stages.map(function (st, i) {
      let sec = st.seconds;
      let open = false;
      if (sec < 0) {
        open = true;
        const at = parseTime(st.at);
        sec = rec.status === 'in_progress' && at ? Math.max(1, ((now || Date.now()) - at.getTime()) / 1000) : 0;
      }
      return { st: st, sec: sec, open: open, last: i === stages.length - 1 };
    });
    const total = segs.reduce(function (a, s) { return a + (s.sec > 0 ? s.sec : 0); }, 0) || 1;
    let bar = '<div class="timeline" role="img" aria-label="' + esc(stages.map(function (s) { return stageName(s.stage); }).join(', ')) + '">';
    segs.forEach(function (s) {
      const terminal = ['success', 'failed', 'aborted', 'unknown'].indexOf(s.st.stage) !== -1;
      if (terminal) {
        bar += '<span class="tl-end tl-' + esc(s.st.stage) + '" title="' + esc(stageName(s.st.stage) + ' at ' + clock(s.st.at)) + '"></span>';
        return;
      }
      const silent = s.open && rec.status !== 'in_progress';
      const grow = silent ? 30 : Math.max(s.sec / total * 100, 2);
      const cls = 'tl-seg tl-' + esc(s.st.stage) + (s.open && rec.status !== 'in_progress' ? ' tl-silent' : '') +
        (s.open && rec.status === 'in_progress' ? ' tl-live' : '');
      bar += '<span class="' + cls + '" style="flex-grow:' + grow.toFixed(2) + '" title="' +
        esc(stageName(s.st.stage) + ', ' + (s.open ? (rec.status === 'in_progress' ? 'running' : 'no later report') : duration(s.sec))) + '"></span>';
    });
    bar += '</div>';

    let rows = '';
    segs.forEach(function (s) {
      let spent;
      if (['success', 'failed', 'aborted'].indexOf(s.st.stage) !== -1) spent = '';
      else if (s.st.stage === 'unknown') spent = 'marked by the cleanup';
      else if (s.open) spent = rec.status === 'in_progress' ? 'running for ' + duration(s.sec) : 'no later report';
      else spent = duration(s.sec);
      rows += '<tr><td><span class="dot tl-' + esc(s.st.stage) + '"></span>' + esc(stageName(s.st.stage)) + '</td>' +
        '<td class="num">' + esc(clock(s.st.at)) + '</td><td>' + esc(spent) + '</td></tr>';
    });
    return bar + '<table class="mini stage-table"><thead><tr><th>Stage</th><th>At</th><th>Time in stage</th></tr></thead><tbody>' +
      rows + '</tbody></table>';
  }

  const TREND = { new: 'new', rising: 'rising', falling: 'falling', steady: 'steady', quiet: 'none lately', stopped: 'stopped' };
  function trend(c) {
    return '<span class="trend trend-' + esc(c.trend) + '" title="' + esc(c.last_24h + ' in the last 24 h, ' + c.prev_24h + ' in the 24 h before') + '">' +
      num(c.last_24h) + ' <small>' + esc(TREND[c.trend] || c.trend || '') + '</small></span>';
  }

  function kv(pairs) {
    const items = pairs.filter(function (p) { return p[1] !== '' && p[1] != null; });
    if (!items.length) return '';
    return '<dl class="kv">' + items.map(function (p) {
      return '<div><dt>' + esc(p[0]) + '</dt><dd>' + (p[2] ? p[1] : esc(p[1])) + '</dd></div>';
    }).join('') + '</dl>';
  }

  function code(s) { return '<code>' + esc(s) + '</code>'; }

  function recordDialogHTML(rec, now) {
    const st = STATUS[rec.status] || STATUS.unknown;
    let html = '<header class="rd-head">';
    html += '<div class="rd-title"><h2 id="recordTitle">' + esc(rec.nsapp || 'Unknown app') + '</h2>' +
      typeBadge(rec.type) + statusChip(rec) + '</div>';
    html += kv([
      ['Platform', platformName(rec.platform)],
      ['Repository', rec.repo_slug || rec.repo_source || ''],
      ['Started', dateTime(rec.started_at) + (rec.started_at ? ' (' + ago(rec.started_at, now) + ')' : '')],
      [rec.status === 'unknown' ? 'Last report' : 'Finished', rec.finished_at ? clock(rec.finished_at) : (rec.status === 'unknown' ? clock(rec.last_stage_at) : '')],
      [rec.status === 'in_progress' ? 'Running for' : rec.status === 'unknown' ? 'Reported for' : 'Duration', duration(rec.duration)],
    ]);
    html += '</header>';

    html += '<section class="rd-section"><h3>Timeline</h3>' + stageTimeline(rec, now) + '</section>';

    if (rec.status === 'unknown') {
      html += '<section class="rd-section rd-noresult"><h3>No result</h3><p>No final status was received; the run stopped reporting after ' +
        esc(stageName(rec.last_stage || 'installing').toLowerCase()) + (rec.last_stage_at ? ' at ' + esc(clock(rec.last_stage_at)) : '') +
        '. This usually means the host lost the session, for example a closed console, before the script could report.</p></section>';
    }

    if (rec.status === 'failed' || rec.status === 'aborted') {
      const trace = traceText(rec.error);
      const lines = trace ? trace.split('\n').length : 0;
      html += '<section class="rd-section rd-error ' + st.cls + '"><h3>' + (rec.status === 'aborted' ? 'Why it stopped' : 'What went wrong') + '</h3>';
      html += '<p class="rd-headline">' + esc(headline(rec)) + '</p>';
      const cmd = rec.failed_command ? code(rec.failed_command) + (rec.failed_line ? ' at line ' + rec.failed_line : '') : '';
      html += kv([
        ['Step', rec.error_step || ''],
        ['Where', rec.error_phase === 'host' ? 'Host' : rec.error_phase === 'container' ? 'Container' : ''],
        ['Function', rec.failed_function ? code(rec.failed_function) : '', true],
        ['Command', cmd, true],
        ['Exit code', rec.exit_code ? rec.exit_code + ': ' + (rec.exit_description || '') : ''],
        ['Category', rec.error_category || ''],
      ]);
      if (trace) {
        html += '<details class="trace" open><summary>Trace, ' + lines + (lines === 1 ? ' line' : ' lines') + '</summary><pre>' + esc(trace) + '</pre></details>';
      } else {
        html += '<p class="muted">No trace was sent with this run.</p>';
      }
      html += '<div class="rd-actions">' +
        (trace ? '<button type="button" class="btn" data-copy="trace">Copy trace</button>' : '') +
        '<button type="button" class="btn" data-copy="markdown">Copy as Markdown</button></div>';
      html += '</section>';
    }

    const gpu = [rec.gpu_vendor && rec.gpu_vendor !== 'unknown' ? rec.gpu_vendor : '', rec.gpu_model].filter(Boolean).join(' ');
    html += '<section class="rd-section"><h3>System</h3>' + kv([
      ['OS', osName(rec)],
      [rec.platform === 'incus' ? 'Incus version' : 'PVE version', rec.pve_version || ''],
      ['Kernel', rec.kernel_version || ''],
      ['Architecture', rec.arch || (rec.has_arm ? 'arm64' : '')],
      ['CPU', rec.cpu_model || (rec.cpu_vendor !== 'unknown' ? rec.cpu_vendor || '' : '')],
      ['GPU', gpu],
      ['RAM speed', rec.ram_speed ? rec.ram_speed + ' MT/s' : ''],
      ['GPU passthrough', rec.gpu_passthrough || ''],
      ['Resources', resources(rec)],
      ['Container', rec.type === 'lxc' && rec.os_type ? (rec.ct_type === 1 ? 'Unprivileged' : 'Privileged') : ''],
      ['Method', rec.method || ''],
      ['App version', rec.app_version || ''],
      ['Payload version', rec.payload_version ? String(rec.payload_version) : 'before versioning'],
      ['Migrated from', rec.migrated_from || ''],
    ]) + '</section>';

    html += '<section class="rd-section"><h3>Identifiers</h3>' + kv([
      ['Execution ID', rec.execution_id ? code(rec.execution_id) + ' <button type="button" class="btn btn-xs" data-copy="execution_id">Copy</button>' : '', true],
      ['Random ID', rec.random_id ? code(rec.random_id) + ' <button type="button" class="btn btn-xs" data-copy="random_id">Copy</button>' : '', true],
    ]) + '</section>';
    return html;
  }

  function mdCell(s) { return String(s == null ? '' : s).replace(/\|/g, '\\|').replace(/\n/g, ' '); }
  function fence(text, lang) {
    let f = '```';
    while (String(text).indexOf(f) !== -1) f += '`';
    return f + (lang || '') + '\n' + text + '\n' + f;
  }

  function runMarkdown(rec, level) {
    const h = '#'.repeat(level || 3);
    const st = STATUS[rec.status] || { label: rec.status };
    const rows = [
      ['App', (rec.nsapp || '') + ' (' + typeName(rec.type) + ')' + (rec.app_version ? ', version ' + rec.app_version : '')],
      ['Status', st.label + (rec.exit_code ? ', exit code ' + rec.exit_code + ' (' + (rec.exit_description || '') + ')' : '')],
      ['Message', headline(rec)],
      ['Step', rec.error_step || ''],
      ['Where', [rec.error_phase, rec.failed_function ? 'function ' + rec.failed_function : ''].filter(Boolean).join(', ')],
      ['Command', rec.failed_command ? '`' + rec.failed_command.replace(/`/g, "'") + '`' + (rec.failed_line ? ' (line ' + rec.failed_line + ')' : '') : ''],
      ['OS', osName(rec)],
      ['Platform', [platformName(rec.platform) + (rec.pve_version ? ' ' + rec.pve_version.replace(/^incus-?/, '') : ''),
        rec.kernel_version ? 'kernel ' + rec.kernel_version : '', rec.arch || ''].filter(Boolean).join(', ')],
      ['Resources', resources(rec)],
      ['Hardware', [rec.cpu_model, rec.gpu_model, rec.ram_speed ? 'RAM ' + rec.ram_speed + ' MT/s' : ''].filter(Boolean).join(', ')],
      ['Repository', rec.repo_slug || rec.repo_source || ''],
      ['Started', rec.started_at || ''],
      ['Duration', duration(rec.duration)],
      ['Execution ID', rec.execution_id || rec.random_id || ''],
    ].filter(function (r) { return r[1]; });
    let md = h + ' ' + (rec.nsapp || 'Run') + ': ' + (headline(rec) || st.label) + '\n\n| Field | Value |\n| --- | --- |\n';
    rows.forEach(function (r) { md += '| ' + r[0] + ' | ' + mdCell(r[1]) + ' |\n'; });
    const trace = traceText(rec.error);
    if (trace && rec.status !== 'unknown') {
      md += '\n<details><summary>Trace</summary>\n\n' + fence(trace, 'text') + '\n\n</details>\n';
    }
    return md;
  }

  function causeMarkdown(cause, examples, ctx) {
    ctx = ctx || {};
    let md = '## ' + (cause.message || cause.key) + '\n\n';
    const lines = [
      'Failures: ' + runs(cause.failures) + (ctx.period ? ' ' + ctx.period : '') +
        ' (' + cause.last_24h + ' in the last 24 h, ' + cause.prev_24h + ' in the 24 h before; ' + cause.trend + ')',
      'Apps: ' + (cause.top_apps || []).join(', ') + (cause.apps > (cause.top_apps || []).length ? ' and ' + (cause.apps - cause.top_apps.length) + ' more' : ''),
      'Distinct setups: ' + num(cause.setups),
      'Category: ' + (cause.category || 'none') + '; most common exit code ' + cause.exit_code + ' (' + cause.exit_description + ')',
      'First seen: ' + cause.first_seen + ', last seen: ' + cause.last_seen,
    ];
    if (cause.kind === 'command') lines.unshift('Failing command: `' + String(cause.key).replace(/`/g, "'") + '`');
    if (cause.example_message) lines.push('Example message: ' + cause.example_message);
    if (ctx.runsURL) lines.push('All runs: ' + ctx.runsURL);
    md += lines.map(function (l) { return '- ' + l; }).join('\n') + '\n';
    (examples || []).forEach(function (r) { md += '\n' + runMarkdown(r, 3); });
    return md;
  }

  // Stacked columns; series draw bottom up.
  function columnsSVG(points, series, opts) {
    opts = opts || {};
    const w = 640, h = opts.height || 120, pad = 18;
    if (!points.length) return '';
    const max = Math.max(1, ...points.map(function (p) { return series.reduce(function (a, s) { return a + (p[s.key] || 0); }, 0); }));
    const bw = w / points.length;
    let bars = '';
    points.forEach(function (p, i) {
      let y = h - pad;
      const tip = (opts.label ? opts.label(p) : '') + ': ' + series.map(function (s) { return num(p[s.key]) + ' ' + s.name; }).join(', ');
      bars += '<g><title>' + esc(tip) + '</title>';
      series.forEach(function (s) {
        const v = p[s.key] || 0;
        const bh = v / max * (h - pad - 4);
        if (bh > 0) {
          y -= bh;
          bars += '<rect class="' + s.cls + '" x="' + (i * bw + bw * 0.12).toFixed(1) + '" y="' + y.toFixed(1) +
            '" width="' + (bw * 0.76).toFixed(1) + '" height="' + bh.toFixed(1) + '"></rect>';
        }
      });
      bars += '</g>';
    });
    const ticks = [0, Math.floor(points.length / 2), points.length - 1].filter(function (v, i, a) { return a.indexOf(v) === i; });
    let labels = '';
    ticks.forEach(function (i) {
      const anchor = i === 0 ? 'start' : i === points.length - 1 ? 'end' : 'middle';
      const x = i === 0 ? 0 : i === points.length - 1 ? w : i * bw + bw / 2;
      labels += '<text x="' + x.toFixed(1) + '" y="' + (h - 4) + '" text-anchor="' + anchor + '">' + esc(opts.label ? opts.label(points[i]) : '') + '</text>';
    });
    return '<svg class="columns" viewBox="0 0 ' + w + ' ' + h + '" preserveAspectRatio="none" role="img" aria-label="' +
      esc(opts.title || 'Chart') + '">' + bars + labels + '</svg>';
  }

  function copyText(text, btn) {
    const done = function () {
      if (!btn) return;
      const orig = btn.textContent;
      btn.textContent = 'Copied';
      setTimeout(function () { btn.textContent = orig; }, 1500);
    };
    const fallback = function () {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy'); done(); } catch (e) { /* nothing else to try */ }
      ta.remove();
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(done, fallback);
    } else {
      fallback();
    }
  }

  function download(name, text, type) {
    const blob = new Blob([text], { type: type || 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
  }

  async function getJSON(url) {
    const r = await fetch(url, { cache: 'no-store' });
    if (!r.ok) {
      const msg = (await r.text()).trim();
      throw new Error(msg || 'The server answered ' + r.status);
    }
    return r.json();
  }

  let current = null;
  function openRecord(rec) {
    current = rec;
    const dlg = document.getElementById('recordDialog');
    document.getElementById('recordBody').innerHTML = recordDialogHTML(rec);
    if (dlg.showModal && !dlg.open) dlg.showModal();
    else dlg.setAttribute('open', '');
  }
  function closeRecord() {
    const dlg = document.getElementById('recordDialog');
    if (dlg.close) dlg.close(); else dlg.removeAttribute('open');
  }

  document.addEventListener('click', function (e) {
    const t = e.target;
    if (!t || !t.closest) return;
    const copy = t.closest('[data-copy]');
    if (copy && current) {
      const what = copy.getAttribute('data-copy');
      const text = what === 'trace' ? traceText(current.error) : what === 'markdown' ? runMarkdown(current, 3) : current[what] || '';
      copyText(text, copy);
      return;
    }
    if (t.closest('[data-close-dialog]')) {
      const d = t.closest('dialog');
      if (d) d.close();
      return;
    }
    if (t.tagName === 'DIALOG') t.close();
  });

  window.RunUI = {
    STATUS: STATUS, esc: esc, num: num, pct: pct, runs: runs, duration: duration, parseTime: parseTime, dateTime: dateTime,
    clock: clock, ago: ago, stageName: stageName, typeName: typeName, typeBadge: typeBadge, platformName: platformName,
    statusChip: statusChip, headline: headline, trend: trend, traceText: traceText, resources: resources, osName: osName,
    hostName: hostName, setupName: setupName, stageTimeline: stageTimeline, recordDialogHTML: recordDialogHTML,
    runMarkdown: runMarkdown, causeMarkdown: causeMarkdown, columnsSVG: columnsSVG, copyText: copyText,
    download: download, getJSON: getJSON, openRecord: openRecord, closeRecord: closeRecord,
    current: function () { return current; },
  };
})();

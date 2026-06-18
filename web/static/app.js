const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

let authed = false;
let eventSource = null;
let exploreSelected = null;
let exploreConnections = [];
let activeJobId = null;

async function api(path, opts = {}) {
  const res = await fetch('/api' + path, {
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
    credentials: 'same-origin',
    ...opts,
  });
  if (res.status === 401) {
    showAuth();
    throw new Error('Unauthorized');
  }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!res.ok) {
    const msg = (data && (data.message || data.error)) || (typeof data === 'string' ? data : res.statusText);
    throw new Error(msg || 'Request failed');
  }
  return data;
}

function showToast(message, kind = 'ok', ms = 2600) {
  const el = $('#action-toast');
  if (!el) return;
  el.textContent = message;
  el.className = 'action-toast' + (kind === 'error' ? ' error' : '');
  el.classList.remove('hidden');
  clearTimeout(el._timer);
  el._timer = setTimeout(() => el.classList.add('hidden'), ms);
}

async function withBtn(btn, fn, opts = {}) {
  if (!btn || btn.disabled || btn.classList.contains('busy')) return;
  const orig = btn.textContent;
  btn.disabled = true;
  btn.classList.add('busy', 'pressed');
  if (opts.pending) btn.textContent = opts.pending;
  setTimeout(() => btn.classList.remove('pressed'), 180);
  try {
    const result = await fn();
    btn.classList.remove('busy');
    btn.classList.add('done');
    btn.textContent = opts.success || orig;
    if (opts.toast) showToast(opts.toast);
    clearTimeout(btn._resetTimer);
    btn._resetTimer = setTimeout(() => {
      btn.classList.remove('done');
      btn.textContent = orig;
      btn.disabled = false;
    }, opts.holdMs ?? 1600);
    return result;
  } catch (err) {
    btn.classList.remove('busy');
    btn.classList.add('error-flash');
    btn.textContent = opts.errorLabel || 'Failed';
    if (opts.toast !== false) showToast(err.message, 'error');
    clearTimeout(btn._resetTimer);
    btn._resetTimer = setTimeout(() => {
      btn.classList.remove('error-flash');
      btn.textContent = orig;
      btn.disabled = false;
    }, 2200);
    if (!opts.rethrow) return;
    throw err;
  }
}

function showAuth(setup = false) {
  authed = false;
  stopSSE();
  $('#auth-panel').classList.remove('hidden');
  $('#main-panel').classList.add('hidden');
  $('#logout-btn').classList.add('hidden');
  $('#auth-title').textContent = setup ? 'Create admin password' : 'Sign in';
  $('#auth-subtitle').textContent = setup
    ? 'First launch — choose a password to protect this console.'
    : 'Enter your admin password to manage connections and jobs.';
}

function showMain() {
  authed = true;
  $('#auth-panel').classList.add('hidden');
  $('#main-panel').classList.remove('hidden');
  $('#logout-btn').classList.remove('hidden');
  startSSE();
  loadSettingsForm();
  loadConnections().catch(() => {});
}

async function bootstrap() {
  const b = await fetch('/api/bootstrap').then(r => r.json());
  if (b.setup_required) {
    showAuth(true);
    return;
  }
  try {
    await api('/status');
    showMain();
  } catch {
    showAuth(false);
  }
}

$('#auth-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const password = $('#password').value;
    $('#auth-error').classList.add('hidden');
    const setup = $('#auth-title').textContent.includes('Create');
    await api(setup ? '/setup' : '/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
    $('#password').value = '';
    showMain();
  }, { pending: 'Signing in…', success: 'Signed in', toast: false, rethrow: true }).catch((err) => {
    $('#auth-error').textContent = err.message;
    $('#auth-error').classList.remove('hidden');
  });
});

$('#logout-btn').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    await api('/logout', { method: 'POST' });
    showAuth(false);
  }, { pending: 'Signing out…', success: 'Signed out', toast: 'Signed out' });
});

$$('.tab').forEach((tab) => {
  tab.addEventListener('click', () => {
    $$('.tab').forEach(t => t.classList.remove('active'));
    $$('.tab-panel').forEach(p => p.classList.remove('active'));
    tab.classList.add('active');
    $('#tab-' + tab.dataset.tab).classList.add('active');
  });
});

function fmtNum(n) {
  return (n || 0).toLocaleString();
}

function fmtNumRows(row) {
  const n = row.num_rows ?? row.source_row_count;
  const known = row.num_rows_known ?? row.source_row_count_known;
  const approx = row.num_rows_approx ?? row.source_row_count_approx;
  const exceeded = row.num_rows_exceeded ?? row.source_row_count_exceeded;
  if (!known) return 'NUM_ROWS unknown';
  const label = exceeded ? `${fmtNum(n)}+` : fmtNum(n);
  if (approx) return `NUM_ROWS ${label} (stats)`;
  return `NUM_ROWS ${label}`;
}

function fmtTime(ts) {
  if (!ts) return '';
  return new Date(ts).toLocaleString();
}

function fmtRelative(ts) {
  if (!ts) return 'never';
  const sec = Math.round((Date.now() - new Date(ts).getTime()) / 1000);
  if (sec < 0) return 'soon';
  if (sec < 60) return sec <= 5 ? 'just now' : `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min} min ago`;
  const hr = Math.round(min / 60);
  if (hr < 48) return `${hr} hr ago`;
  return fmtTime(ts);
}

function fmtScheduleLastJob(job) {
  if (!job) return 'none yet';
  const when = job.completed_at || job.started_at || job.created_at;
  const detail = job.status === 'completed' && job.rows_done === 0
    ? 'no changes'
    : `${fmtNum(job.rows_done)} row(s) · ${job.status}`;
  return `${fmtRelative(when)} — ${detail}`;
}

function eventSourceLabel(ev, jobById) {
  if (!ev.job_id) return 'Scheduler';
  const job = jobById[ev.job_id];
  if (!job) return 'Job';
  if (job.type === 'schema_sample') return 'Schema sample';
  if (job.type === 'incremental_sync') return 'Incremental';
  return (job.type || 'job').replace(/_/g, ' ');
}

function renderScheduleCard(schedule, engineOn) {
  const card = $('#schedule-card');
  if (!schedule) {
    card.classList.add('hidden');
    return;
  }
  card.classList.remove('hidden');
  const armed = schedule.armed && engineOn;
  const badge = $('#schedule-badge');
  badge.textContent = armed ? 'Armed' : (engineOn ? 'Waiting for connections' : 'Engine stopped');
  badge.className = 'badge ' + (armed ? 'running' : 'cancelled');
  $('#schedule-label').textContent = schedule.label || schedule.cron || '';
  $('#schedule-next').textContent = armed && schedule.next_run_at
    ? fmtTime(schedule.next_run_at)
    : (engineOn ? '—' : 'Start engine first');
  $('#schedule-last').textContent = fmtScheduleLastJob(schedule.last_job);
  const detail = $('#schedule-detail');
  if (!engineOn) {
    detail.textContent = 'Start the migration engine to enable automatic incremental sync on this schedule.';
  } else if (!schedule.source_id || !schedule.dest_id) {
    detail.textContent = 'Pick source and destination connections in Settings, then save.';
  } else if (armed) {
    detail.textContent = 'Runs appear in Activity log below and on the Jobs tab. The dashboard updates live when each scheduled run starts.';
  } else {
    detail.textContent = '';
  }
}

function fmtJobDetail(job) {
  const base = (job.type || '').replace(/_/g, ' ');
  if (job.type === 'schema_sample') {
    if (job.current_phase === 'schema') {
      return `${base} · creating tables${job.current_table ? ' · ' + job.current_table : ''}`;
    }
    if (job.current_phase === 'sample' || job.status === 'running') {
      return `${base} · copying 5 sample rows per table${job.current_table ? ' · ' + job.current_table : ''}`;
    }
    return `${base} · ${fmtNum(job.rows_done)} sample row(s) across ${job.tables_done} table(s)`;
  }
  if (job.type === 'incremental_sync') {
    if (job.status === 'running') {
      const table = job.current_table ? ` · ${job.current_table}` : '';
      const phase = job.current_phase === 'scanning' ? 'scanning tables' : 'checking for changes';
      return `${base} · ${phase}${table}`;
    }
    if (job.status === 'completed' && job.rows_done === 0) {
      return `${base} · no changes detected (see Activity log)`;
    }
    return `${base} · ${fmtNum(job.rows_done)} row(s) synced`;
  }
  if (job.date_from || job.date_to) {
    return `${base} · ${fmtDateRange(job)} · started ${fmtTime(job.started_at)}`;
  }
  return `${base} · started ${fmtTime(job.started_at)}`;
}

function fmtTaskProgress(t) {
  if (t.sync_mode === 'schema_sample') {
    if (t.status === 'completed' && t.rows_done === 0) {
      return 'schema only / skipped';
    }
    return `${fmtNum(t.rows_done)} sample row(s)`;
  }
  if (t.sync_mode && t.sync_mode !== 'date_backup') {
    const mode = t.sync_mode === 'ora_rowscn' ? 'SCN' : (t.watermark_col || t.sync_mode);
    if (t.status === 'completed' && t.rows_done === 0) {
      return `no changes (${mode})`;
    }
    return `${fmtNum(t.rows_done)} synced (${mode})`;
  }
  if (t.source_row_count_known) {
    return `${fmtNum(t.rows_done)} / ${t.source_row_count_exceeded ? fmtNum(t.source_row_count) + '+' : fmtNum(t.source_row_count)} copied`;
  }
  return `${fmtNum(t.rows_done)} copied`;
}

function renderDashboard(data) {
  const working = data.working;
  const engineOn = !!data.engine_enabled;
  const anim = $('#status-anim');
  const indicator = $('#work-indicator');
  anim.classList.toggle('working', working);
  anim.classList.toggle('idle', !working);
  indicator.classList.toggle('hidden', !working);

  const badge = $('#engine-badge');
  badge.textContent = engineOn ? 'Engine running' : 'Engine stopped';
  badge.className = 'badge ' + (engineOn ? 'running' : 'cancelled');
  $('#engine-start-btn').classList.toggle('hidden', engineOn);
  $('#engine-stop-btn').classList.toggle('hidden', !engineOn);

  const job = data.active_job;
  const card = $('#active-job-card');
  activeJobId = job ? job.id : null;
  const schedule = data.schedule;
  renderScheduleCard(schedule, engineOn);
  if (!job) {
    if (!engineOn) {
      $('#status-headline').textContent = 'Engine stopped';
      $('#status-detail').textContent = 'Start the migration engine to run jobs or scheduled incremental sync.';
    } else if (working) {
      $('#status-headline').textContent = 'Working';
      $('#status-detail').textContent = 'Background migration activity in progress.';
    } else if (schedule && schedule.armed) {
      $('#status-headline').textContent = 'Scheduled sync armed';
      const next = schedule.next_run_at ? fmtTime(schedule.next_run_at) : '—';
      $('#status-detail').textContent = `Incremental sync runs ${(schedule.label || '').toLowerCase()}. Next check at ${next}. Watch Activity log for each run.`;
    } else {
      $('#status-headline').textContent = 'Ready';
      $('#status-detail').textContent = 'Engine is running. Create a job on the Jobs tab or configure a schedule in Settings.';
    }
    card.classList.add('hidden');
  } else {
    card.classList.remove('hidden');
    const paused = job.status === 'paused' || job.status === 'failed';
    $('#status-headline').textContent = job.status === 'running'
      ? (job.type === 'incremental_sync' ? 'Incremental sync running'
        : job.type === 'schema_sample' ? 'Schema sample running'
        : 'Migration in progress')
      : titleCase(job.status);
    $('#status-detail').textContent = paused && job.error_message
      ? job.error_message
      : fmtJobDetail(job);
    $('#job-phase').textContent = job.current_phase || job.status;
    $('#job-table').textContent = job.current_table || '';
    $('#job-rows').textContent = fmtNum(job.rows_done);
    const est = $('#job-num-rows-total');
    if (job.type === 'incremental_sync') {
      est.textContent = job.status === 'running' ? '· rows synced this run' : '';
    } else if (job.rows_total > 0) {
      est.textContent = `· Σ NUM_ROWS ${fmtNum(job.rows_total)} (stats)`;
    } else {
      est.textContent = '';
    }
    $('#job-tables').textContent = `${job.tables_done}/${job.tables_total}`;
    const pct = job.tables_total ? Math.round((job.tables_done / job.tables_total) * 100) : 0;
    $('#job-progress').style.width = pct + '%';
    $('#paused-job-controls').classList.toggle('hidden', !paused);
    $('#running-job-controls').classList.toggle('hidden', paused || job.status !== 'running');
    if (paused) {
      $('#paused-batch-size').value = job.batch_size || 50000;
      $('#paused-parallel').value = job.parallel_tables || 2;
      $('#paused-chunk-timeout').value = job.chunk_timeout_sec || '';
      $('#paused-max-rows').value = job.max_rows_per_table || '';
      const showDates = job.type === 'date_range_backup';
      $('#paused-date-fields').classList.toggle('hidden', !showDates);
      if (showDates) {
        $('#paused-date-from').value = (job.date_from || '').slice(0, 10);
        $('#paused-date-to').value = (job.date_to || '').slice(0, 10);
        $('#paused-date-column').value = job.date_column || '';
      }
    }
  }

  const tasks = data.table_tasks || [];
  const taskEl = $('#table-task-list');
  if (!tasks.length) {
    taskEl.className = 'task-list empty-state';
    taskEl.textContent = 'No table tasks yet.';
  } else {
    taskEl.className = 'task-list';
    taskEl.innerHTML = tasks.map(t => {
      const err = t.error_message ? `<div class="error">${esc(t.error_message)}</div>` : '';
      const progress = fmtTaskProgress(t);
      const dateCol = t.sync_mode === 'date_backup' && t.watermark_col ? ` · ${esc(t.watermark_col)}` : '';
      return `
      <div class="task-item ${t.status}">
        <div class="task-meta">
          <strong>${esc(t.schema_name)}.${esc(t.table_name)}</strong>
          <span class="badge ${t.status}">${t.status}</span>
        </div>
        <div class="task-meta muted">
          <span>${fmtNumRows(t)}${dateCol}</span>
          <span>${progress}</span>
          <span>${t.rows_per_sec ? t.rows_per_sec.toFixed(0) + '/s' : ''}</span>
        </div>
        ${t.status === 'running' ? '<div class="mini-bar"><span></span></div>' : ''}
        ${err}
      </div>`;
    }).join('');
  }

  const events = (data.events || []).slice();
  const jobById = Object.fromEntries((data.recent_jobs || []).map(j => [j.id, j]));
  const logEl = $('#event-log');
  if (!events.length) {
    logEl.className = 'event-log empty-state';
    logEl.textContent = schedule && schedule.armed
      ? 'Waiting for the first scheduled run…'
      : 'Waiting for events…';
  } else {
    logEl.className = 'event-log';
    logEl.innerHTML = events.map(ev => {
      const src = eventSourceLabel(ev, jobById);
      return `
      <div class="event-item ${esc(ev.level)}">
        <div class="event-head">
          <span class="event-source">${esc(src)}</span>
          <span class="event-time">${fmtTime(ev.created_at)}</span>
        </div>
        <div>${esc(ev.message)}</div>
      </div>`;
    }).join('');
  }

  if (Array.isArray(data.connections)) {
    renderConnections(data.connections);
    fillJobSelects(data.connections);
    fillExploreSelects(data.connections);
  }
  renderJobs(data.recent_jobs || []);
}

async function loadConnections() {
  const list = await api('/connections');
  renderConnections(list);
  fillJobSelects(list);
  fillExploreSelects(list);
  return list;
}

function renderConnections(list) {
  const el = $('#conn-list');
  if (!list.length) {
    el.className = 'conn-list empty-state';
    el.textContent = 'No connections yet.';
    return;
  }
  el.className = 'conn-list';
  el.innerHTML = list.map(c => `
    <div class="conn-item">
      <div class="task-meta">
        <strong>${esc(c.name)}</strong>
        <span class="badge ${c.type === 'oracle' ? 'running' : 'completed'}">${c.type}</span>
      </div>
      <div class="muted">${esc(c.host)}:${c.port || '—'} · ${esc(c.database || '—')}${c.windows_auth ? ' · Windows auth' : ''}</div>
      <button class="btn danger" style="margin-top:0.45rem" data-del="${c.id}" type="button">Delete</button>
    </div>`).join('');
  el.querySelectorAll('[data-del]').forEach(btn => {
    btn.addEventListener('click', async () => {
      if (!confirm('Delete this connection?')) return;
      await withBtn(btn, async () => {
        await api('/connections/' + btn.dataset.del, { method: 'DELETE' });
        await loadConnections();
      }, { pending: 'Deleting…', success: 'Deleted', toast: 'Connection deleted' });
    });
  });
}

function renderJobs(list) {
  const el = $('#job-list');
  if (!list.length) {
    el.className = 'job-list empty-state';
    el.textContent = 'No jobs yet.';
    return;
  }
  el.className = 'job-list';
  el.innerHTML = list.map(j => `
    <div class="job-item">
      <div class="task-meta">
        <strong>${esc(j.type.replace(/_/g, ' '))}</strong>
        <span class="badge ${j.status}">${j.status}</span>
        ${j.status === 'paused' || j.status === 'failed' ? `<button class="btn primary" type="button" data-resume="${j.id}" style="margin-left:auto">Resume</button>` : ''}
      </div>
      <div class="muted">${fmtNum(j.rows_done)} rows · ${j.tables_done}/${j.tables_total} tables · ${fmtTime(j.created_at)}${j.type === 'incremental_sync' && j.status === 'completed' && j.rows_done === 0 ? ' · no changes' : ''}</div>
      ${j.error_message ? `<div class="error">${esc(j.error_message)}</div>` : ''}
    </div>`).join('');
  el.querySelectorAll('[data-resume]').forEach(btn => {
    btn.addEventListener('click', () => resumeJob(btn.dataset.resume, btn));
  });
}

function fillJobSelects(list) {
  const oracle = list.filter(c => c.type === 'oracle');
  const mssql = list.filter(c => c.type === 'mssql');
  const fill = (el, items, placeholder) => {
    el.innerHTML = items.length
      ? items.map(c => `<option value="${c.id}">${esc(c.name)}</option>`).join('')
      : `<option value="">${placeholder}</option>`;
  };
  fill($('#job-source'), oracle, 'Add an Oracle source first');
  fill($('#job-dest'), mssql, 'Add a SQL Server destination first');
  fill($('#schedule-source'), oracle, 'None');
  fill($('#schedule-dest'), mssql, 'None');
}

function titleCase(s) {
  return (s || '').replace(/\b\w/g, c => c.toUpperCase());
}

function esc(s) {
  return String(s || '').replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
}

function startSSE() {
  stopSSE();
  eventSource = new EventSource('/api/events', { withCredentials: true });
  eventSource.addEventListener('status', (ev) => {
    try {
      renderDashboard(JSON.parse(ev.data));
    } catch (_) {}
  });
  eventSource.onerror = () => {
    setTimeout(() => { if (authed) startSSE(); }, 3000);
  };
}

function stopSSE() {
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
}

function connPayload(fd) {
  const body = Object.fromEntries(fd.entries());
  body.port = parseInt(body.port || '0', 10);
  body.windows_auth = body.windows_auth === '1';
  return body;
}

function updateConnAuthUI() {
  const type = $('#conn-form select[name=type]').value;
  const winAuth = $('#conn-windows-auth').checked;
  const showWin = type === 'mssql';
  $('#conn-windows-wrap').classList.toggle('hidden', !showWin);
  const sqlLogin = !(showWin && winAuth);
  $('#conn-username').required = sqlLogin;
  $('#conn-password-wrap').classList.toggle('hidden', !sqlLogin);
}

$('#conn-form select[name=type]').addEventListener('change', updateConnAuthUI);
$('#conn-windows-auth').addEventListener('change', updateConnAuthUI);
updateConnAuthUI();

$('#conn-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const body = connPayload(new FormData(e.target));
    await api('/connections', { method: 'POST', body: JSON.stringify(body) });
    $('#conn-msg').textContent = 'Connection saved.';
    $('#conn-msg').className = 'muted';
    clearConnTestTable();
    e.target.reset();
    updateConnAuthUI();
    await loadConnections();
  }, { pending: 'Saving…', success: 'Saved', toast: 'Connection saved' });
});

function clearConnTestTable() {
  $('#conn-test-wrap').classList.add('hidden');
  $('#conn-test-body').innerHTML = '';
}

function renderConnTestTable(steps) {
  const wrap = $('#conn-test-wrap');
  const body = $('#conn-test-body');
  if (!steps || !steps.length) {
    clearConnTestTable();
    return;
  }
  body.innerHTML = steps.map((s) => `
    <tr>
      <td>${esc(s.name)}</td>
      <td><span class="badge ${s.status === 'ok' ? 'completed' : s.status === 'skipped' ? 'pending' : 'failed'}">${esc(s.status)}</span></td>
      <td>${fmtNum(s.duration_ms)} ms</td>
      <td class="${s.status === 'ok' ? 'muted' : 'error'}">${esc(s.message || '')}</td>
    </tr>
  `).join('');
  wrap.classList.remove('hidden');
}

$('#test-conn-btn').addEventListener('click', async (e) => {
  const btn = e.currentTarget;
  await withBtn(btn, async () => {
    const body = connPayload(new FormData($('#conn-form')));
    $('#conn-msg').textContent = 'Testing host, port, and database in sequence...';
    $('#conn-msg').className = 'muted';
    clearConnTestTable();
    const res = await api('/connections/test/sequence', { method: 'POST', body: JSON.stringify(body) });
    renderConnTestTable(res.steps || []);
    if (res.status === 'error') {
      $('#conn-msg').textContent = 'Connection test failed at one step. Check the table below.';
      $('#conn-msg').className = 'error';
      throw new Error('Connection test failed');
    }
    if (res.empty === false) {
      $('#conn-msg').textContent = `Connection successful — destination has ${res.table_count} table(s). Bulk migration will be blocked until empty.`;
    } else {
      $('#conn-msg').textContent = 'Connection successful.';
    }
    $('#conn-msg').className = 'muted';
  }, { pending: 'Testing…', success: 'Test done', toast: false });
});

function fmtDateRange(job) {
  if (!job.date_from && !job.date_to) return 'all records';
  if (job.date_from && job.date_to) return `${job.date_from.slice(0, 10)} – ${job.date_to.slice(0, 10)}`;
  if (job.date_from) return `from ${job.date_from.slice(0, 10)}`;
  return `through ${job.date_to.slice(0, 10)}`;
}

function toggleDateRangeFields() {
  const show = $('#job-type').value === 'date_range_backup';
  $('#date-range-fields').classList.toggle('hidden', !show);
}

function toggleJobTypeFields() {
  const type = $('#job-type').value;
  const isDate = type === 'date_range_backup';
  const isSample = type === 'schema_sample';
  $('#date-range-fields').classList.toggle('hidden', !isDate);
  $('#job-sample-note').classList.toggle('hidden', !isSample);
  $('#job-advanced-fields').classList.toggle('hidden', isSample);
}

$('#job-type').addEventListener('change', () => {
  toggleDateRangeFields();
  toggleJobTypeFields();
});
toggleDateRangeFields();
toggleJobTypeFields();

$('#job-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const fd = new FormData(e.target);
    const body = Object.fromEntries(fd.entries());
    body.batch_size = parseInt(body.batch_size || '0', 10);
    body.parallel_tables = parseInt(body.parallel_tables || '0', 10);
    body.chunk_timeout_sec = parseInt(body.chunk_timeout_sec || '0', 10);
    body.max_rows_per_table = parseInt(body.max_rows_per_table || '0', 10);
    if (body.type !== 'date_range_backup') {
      delete body.date_from;
      delete body.date_to;
      delete body.date_column;
    }
    if (body.type === 'schema_sample') {
      delete body.batch_size;
      delete body.max_rows_per_table;
      delete body.chunk_timeout_sec;
    }
    body.start = true;
    await api('/jobs', { method: 'POST', body: JSON.stringify(body) });
  }, { pending: 'Starting…', success: 'Started', toast: 'Job started — see Dashboard' });
});

async function activeJobPath(action) {
  if (activeJobId) {
    await api('/jobs/' + activeJobId + '/' + action, { method: 'POST' });
    return;
  }
  const jobs = await api('/jobs');
  const active = jobs.find(j => j.status === 'running' || j.status === 'pending' || j.status === 'paused');
  if (active) await api('/jobs/' + active.id + '/' + action, { method: 'POST' });
}

$('#pause-job-btn').addEventListener('click', async (e) => {
  if (!confirm('Pause migration? Progress is saved and you can adjust settings before resuming.')) return;
  await withBtn(e.currentTarget, async () => {
    await activeJobPath('pause');
  }, { pending: 'Pausing…', success: 'Paused', toast: 'Migration paused' });
});

$('#cancel-job-btn').addEventListener('click', async (e) => {
  if (!confirm('Cancel this job permanently? You cannot resume a cancelled job.')) return;
  await withBtn(e.currentTarget, async () => {
    await activeJobPath('cancel');
  }, { pending: 'Cancelling…', success: 'Cancelled', toast: 'Job cancelled' });
});

async function resumeJob(jobId, btn) {
  await withBtn(btn, async () => {
    await api('/jobs/' + jobId + '/start', { method: 'POST' });
  }, { pending: 'Resuming…', success: 'Resumed', toast: 'Job resumed' });
}

$('#resume-job-btn').addEventListener('click', async (e) => {
  if (!activeJobId) return;
  await withBtn(e.currentTarget, async () => {
    const patch = {
      batch_size: parseInt($('#paused-batch-size').value, 10),
      parallel_tables: parseInt($('#paused-parallel').value, 10),
      chunk_timeout_sec: parseInt($('#paused-chunk-timeout').value || '0', 10),
      max_rows_per_table: parseInt($('#paused-max-rows').value || '0', 10),
    };
    if (!$('#paused-date-fields').classList.contains('hidden')) {
      patch.date_from = $('#paused-date-from').value;
      patch.date_to = $('#paused-date-to').value;
      patch.date_column = $('#paused-date-column').value.trim();
    }
    await api('/jobs/' + activeJobId, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    });
    await api('/jobs/' + activeJobId + '/start', { method: 'POST' });
  }, { pending: 'Resuming…', success: 'Resumed', toast: 'Job resumed' });
});

async function loadSettingsForm() {
  const s = await api('/settings');
  const form = $('#settings-form');
  form.schedule_cron.value = s.schedule_cron || '0 */4 * * *';
  form.default_batch_size.value = s.default_batch_size || 50000;
  form.default_parallel.value = s.default_parallel || 2;
  if (form.default_chunk_timeout_sec) {
    form.default_chunk_timeout_sec.value = s.default_chunk_timeout_sec || 300;
  }
  if (form.default_row_count_fallback_cap) {
    form.default_row_count_fallback_cap.value = s.default_row_count_fallback_cap || 0;
  }
  if (s.schedule_source_id) form.schedule_source_id.value = s.schedule_source_id;
  if (s.schedule_dest_id) form.schedule_dest_id.value = s.schedule_dest_id;
  const connForm = $('#connectivity-form');
  if (connForm) {
    connForm.default_connect_timeout_sec.value = s.default_connect_timeout_sec || 30;
    connForm.mssql_encrypt.checked = s.mssql_encrypt !== false;
    connForm.mssql_trust_server_cert.checked = s.mssql_trust_server_cert !== false;
  }
}

$('#settings-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const fd = new FormData(e.target);
    const body = Object.fromEntries(fd.entries());
    body.default_batch_size = parseInt(body.default_batch_size, 10);
    body.default_parallel = parseInt(body.default_parallel, 10);
    body.default_chunk_timeout_sec = parseInt(body.default_chunk_timeout_sec, 10);
    body.default_row_count_fallback_cap = parseInt(body.default_row_count_fallback_cap || '0', 10);
    await api('/settings', { method: 'PUT', body: JSON.stringify(body) });
  }, { pending: 'Saving…', success: 'Saved', toast: 'Settings saved' });
});

$('#connectivity-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const s = await api('/settings');
    const fd = new FormData(e.target);
    const body = {
      schedule_cron: s.schedule_cron,
      schedule_source_id: s.schedule_source_id || '',
      schedule_dest_id: s.schedule_dest_id || '',
      default_batch_size: s.default_batch_size,
      default_parallel: s.default_parallel,
      default_chunk_timeout_sec: s.default_chunk_timeout_sec,
      default_row_count_fallback_cap: s.default_row_count_fallback_cap || 0,
      default_connect_timeout_sec: parseInt(fd.get('default_connect_timeout_sec'), 10) || 30,
      mssql_encrypt: fd.get('mssql_encrypt') === 'on',
      mssql_trust_server_cert: fd.get('mssql_trust_server_cert') === 'on',
    };
    await api('/settings', { method: 'PUT', body: JSON.stringify(body) });
  }, { pending: 'Saving…', success: 'Saved', toast: 'Connectivity settings saved' });
});

$('#password-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  await withBtn(btn, async () => {
    const fd = new FormData(e.target);
    const body = Object.fromEntries(fd.entries());
    $('#password-msg').textContent = '';
    await api('/settings/password', { method: 'POST', body: JSON.stringify(body) });
    $('#password-msg').textContent = 'Password updated.';
    $('#password-msg').className = 'muted';
    e.target.reset();
  }, { pending: 'Updating…', success: 'Updated', toast: 'Password updated' });
});

$('#engine-start-btn').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    await api('/engine/start', { method: 'POST', body: '{}' });
  }, { pending: 'Starting…', success: 'Engine on', toast: 'Migration engine started' });
});

$('#engine-stop-btn').addEventListener('click', async (e) => {
  if (!confirm('Stop the migration engine? Running jobs will be paused.')) return;
  await withBtn(e.currentTarget, async () => {
    await api('/engine/stop', { method: 'POST', body: '{}' });
  }, { pending: 'Stopping…', success: 'Engine off', toast: 'Migration engine stopped' });
});

function fillExploreSelects(list) {
  exploreConnections = list;
  const el = $('#explore-conn-select');
  el.innerHTML = '<option value="">Manual connection…</option>' +
    list.map(c => `<option value="${c.id}">${esc(c.name)} (${c.type})</option>`).join('');
  const mssql = list.filter(c => c.type === 'mssql');
  const dest = $('#explore-dest-select');
  if (dest) {
    dest.innerHTML = '<option value="">None — Oracle-only analysis</option>' +
      mssql.map(c => `<option value="${c.id}">${esc(c.name)}</option>`).join('');
  }
}

function exploreReportPayload() {
  const body = explorePayload();
  const destId = $('#explore-dest-select')?.value;
  if (destId) body.dest_connection_id = destId;
  return body;
}

function renderMigrationReport(report) {
  const card = $('#explore-report-card');
  card.classList.remove('hidden');
  const s = report.summary || {};
  $('#explore-report-summary').textContent =
    `${s.table_count || 0} table(s) analyzed` +
    (s.rows_estimate_known ? ` · ~${fmtNum(s.estimated_rows)} total rows (stats)` : ' · row totals incomplete (missing Oracle stats)') +
    (s.incremental_notes ? ` — ${s.incremental_notes}` : '');

  const stats = $('#explore-report-stats');
  stats.innerHTML = [
    `<span class="badge critical">${s.critical_count || 0} critical</span>`,
    `<span class="badge warning">${s.warning_count || 0} warning</span>`,
    `<span class="badge info">${s.info_count || 0} info</span>`,
    `<span class="badge ${s.bulk_migration_ready ? 'completed' : 'failed'}">Bulk ${s.bulk_migration_ready ? 'ready' : 'blocked'}</span>`,
    `<span class="badge completed">Schema + sample OK</span>`,
  ].join('');

  const serverBits = [];
  if (report.source?.version) serverBits.push(`Oracle: ${report.source.version}`);
  if (report.source?.details?.user_segment_gb) serverBits.push(`Schema data ~${report.source.details.user_segment_gb} GB (user segments)`);
  if (report.destination?.server?.version) serverBits.push(`SQL Server: ${report.destination.server.version}`);
  if (report.destination?.reachable) {
    serverBits.push(`Destination [${esc(report.destination.schema)}]: ${report.destination.table_count} table(s)`);
  }
  $('#explore-report-server').textContent = serverBits.join(' · ');

  const top = $('#explore-report-top');
  const tops = report.top_risks || [];
  top.innerHTML = tops.length
    ? tops.map(t => `<li>${esc(t)}</li>`).join('')
    : '<li class="muted">No critical or warning issues detected.</li>';

  const findings = report.findings || [];
  const fEl = $('#explore-report-findings');
  if (!findings.length) {
    fEl.className = 'report-findings empty-state';
    fEl.textContent = 'No findings — schema looks straightforward for migration.';
  } else {
    fEl.className = 'report-findings';
    fEl.innerHTML = findings.map(f => `
      <div class="report-finding ${esc(f.severity)}">
        <div class="report-finding-meta">
          <span class="badge ${esc(f.severity)}">${esc(f.severity)}</span>
          <span class="badge pending">${esc(f.category)}</span>
          ${f.table ? `<span class="muted">${esc(f.table)}${f.column ? '.' + esc(f.column) : ''}</span>` : ''}
        </div>
        <div>${esc(f.message)}</div>
        ${f.recommendation ? `<div class="report-finding-rec">${esc(f.recommendation)}</div>` : ''}
      </div>`).join('');
  }

  const tables = report.tables || [];
  const tEl = $('#explore-report-tables');
  if (!tables.length) {
    tEl.className = 'report-tables empty-state';
    tEl.textContent = 'No tables analyzed.';
  } else {
    tEl.className = 'report-tables';
    tEl.innerHTML = tables.map(t => `
      <div class="report-table-item ${esc(t.risk_level)}">
        <div class="task-meta">
          <strong>${esc(t.label)}</strong>
          <span class="badge ${esc(t.risk_level)}">${esc(t.risk_level)}</span>
        </div>
        <div class="muted">${fmtNum(t.row_count)} rows · ${t.column_count} cols · sync: ${esc(t.sync_mode || '—')}${t.primary_keys?.length ? ' · PK: ' + esc(t.primary_keys.join(', ')) : ''}</div>
        ${t.mssql_ddl ? `<div class="report-table-ddl">MSSQL: ${esc(t.mssql_ddl)}</div>` : ''}
        ${(t.findings || []).filter(f => f.severity !== 'info').slice(0, 3).map(f => `<div class="muted" style="margin-top:0.25rem">• ${esc(f.message)}</div>`).join('')}
      </div>`).join('');
  }
  card.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function explorePayload(extra = {}) {
  const fd = new FormData($('#explore-form'));
  const body = Object.fromEntries(fd.entries());
  body.port = parseInt(body.port || '0', 10);
  body.windows_auth = body.windows_auth === '1';
  if (!body.connection_id) delete body.connection_id;
  return { ...body, ...extra };
}

function applyExploreSavedConn() {
  const id = $('#explore-conn-select').value;
  const manual = !id;
  ['host', 'port', 'database', 'schema', 'username', 'password'].forEach((name) => {
    const input = $(`#explore-form [name=${name}]`);
    if (input) input.disabled = !manual;
  });
  $('#explore-type').disabled = !manual;
  $('#explore-windows-auth').disabled = !manual;
  if (id) {
    const c = exploreConnections.find(x => x.id === id);
    if (c) {
      $('#explore-type').value = c.type;
      $('#explore-form [name=host]').value = c.host || '';
      $('#explore-form [name=port]').value = c.port || '';
      $('#explore-form [name=database]').value = c.database || '';
      $('#explore-form [name=schema]').value = c.schema || '';
      $('#explore-form [name=username]').value = c.username || '';
      $('#explore-form [name=password]').value = '';
      $('#explore-windows-auth').checked = !!c.windows_auth;
    }
  }
  updateExploreAuthUI();
}

function updateExploreAuthUI() {
  const manual = !$('#explore-conn-select').value;
  const type = $('#explore-type').value;
  const winAuth = $('#explore-windows-auth').checked;
  const showWin = manual && type === 'mssql';
  $('#explore-windows-wrap').classList.toggle('hidden', !showWin);
  const sqlLogin = !(showWin && winAuth);
  if (manual) $('#explore-username').required = sqlLogin;
  $('#explore-password-wrap').classList.toggle('hidden', !sqlLogin || !manual);
}

$('#explore-conn-select').addEventListener('change', applyExploreSavedConn);
$('#explore-type').addEventListener('change', updateExploreAuthUI);
$('#explore-windows-auth').addEventListener('change', updateExploreAuthUI);

$('#explore-list-btn').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    exploreSelected = null;
    $('#explore-detail-card').classList.add('hidden');
    const res = await api('/explore/tables', {
      method: 'POST',
      body: JSON.stringify(explorePayload()),
    });
    renderExploreTables(res.tables || []);
    $('#explore-msg').textContent = `${(res.tables || []).length} table(s) found.`;
    $('#explore-msg').className = 'muted';
  }, { pending: 'Listing…', success: 'Listed', toast: false });
});

$('#explore-report-btn').addEventListener('click', async (e) => {
  const type = $('#explore-conn-select').value
    ? (exploreConnections.find(c => c.id === $('#explore-conn-select').value)?.type || $('#explore-type').value)
    : $('#explore-type').value;
  if (type !== 'oracle') {
    showToast('Migration report requires an Oracle source connection', 'error');
    return;
  }
  const schema = ($('#explore-form [name=schema]').value || '').trim();
  if (!schema && !$('#explore-conn-select').value) {
    showToast('Set Oracle schema (owner) on the connection', 'error');
    return;
  }
  await withBtn(e.currentTarget, async () => {
    const report = await api('/explore/migration-report', {
      method: 'POST',
      body: JSON.stringify(exploreReportPayload()),
    });
    renderMigrationReport(report);
    $('#explore-msg').textContent = `Report generated — ${report.summary?.critical_count || 0} critical, ${report.summary?.warning_count || 0} warning.`;
    $('#explore-msg').className = 'muted';
  }, { pending: 'Analyzing…', success: 'Report ready', toast: 'Migration readiness report generated' });
});

function renderExploreTables(tables) {
  const el = $('#explore-table-list');
  if (!tables.length) {
    el.className = 'explore-table-list empty-state';
    el.textContent = 'No tables found for this target.';
    return;
  }
  el.className = 'explore-table-list';
  el.innerHTML = tables.map(t => {
    const key = `${t.schema}.${t.name}`;
    const rows = fmtNumRows(t);
    return `<button type="button" class="explore-table-item" data-schema="${esc(t.schema)}" data-name="${esc(t.name)}">
      <span class="explore-table-name">${esc(key)}</span>
      <span class="explore-table-rows muted">${esc(rows)}</span>
    </button>`;
  }).join('');
  el.querySelectorAll('.explore-table-item').forEach(btn => {
    btn.addEventListener('click', () => {
      el.querySelectorAll('.explore-table-item').forEach(b => b.classList.remove('selected'));
      btn.classList.add('selected');
      exploreSelected = { schema: btn.dataset.schema, name: btn.dataset.name };
      $('#explore-detail-card').classList.remove('hidden');
      $('#explore-detail-title').textContent = exploreSelected.schema + '.' + exploreSelected.name;
      $('#explore-sample-wrap').classList.add('hidden');
      $('#explore-schema-wrap').classList.add('hidden');
    });
  });
}

async function exploreTableRequest(path, extraQuery = '') {
  if (!exploreSelected) throw new Error('Select a table first');
  return explorePayload({
    table_schema: exploreSelected.schema,
    table_name: exploreSelected.name,
    limit: 5,
  });
}

$('#explore-sample-btn').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    const body = await exploreTableRequest();
    const sample = await api('/explore/sample', { method: 'POST', body: JSON.stringify(body) });
    renderSampleTable(sample);
    $('#explore-sample-wrap').classList.remove('hidden');
  }, { pending: 'Loading…', success: 'Loaded', toast: 'Sample rows loaded' });
});

function renderSampleTable(sample) {
  const cols = sample.columns || [];
  const rows = sample.rows || [];
  const head = cols.map(c => `<th>${esc(c)}</th>`).join('');
  const body = rows.map(r => `<tr>${r.map(v => `<td title="${esc(v)}">${esc(v)}</td>`).join('')}</tr>`).join('');
  $('#explore-sample-table').innerHTML = `<table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
}

async function downloadExplore(path, query, filename) {
  const body = await exploreTableRequest();
  const res = await fetch('/api' + path + query, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    let msg = res.statusText;
    try { msg = JSON.parse(text).message || msg; } catch (_) {}
    throw new Error(msg);
  }
  const blob = await res.blob();
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = filename;
  a.click();
  URL.revokeObjectURL(a.href);
}

$('#explore-sample-dl').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.sample.csv` : 'sample.csv';
    await downloadExplore('/explore/sample', '?download=csv', name);
  }, { pending: 'Downloading…', success: 'Downloaded', toast: 'CSV download started' });
});

$('#explore-schema-btn').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    const body = await exploreTableRequest();
    const res = await api('/explore/schema', { method: 'POST', body: JSON.stringify(body) });
    $('#explore-schema-pre').textContent = JSON.stringify(res.columns || [], null, 2);
    $('#explore-schema-wrap').classList.remove('hidden');
  }, { pending: 'Loading…', success: 'Loaded', toast: 'Schema loaded' });
});

$('#explore-schema-json-dl').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.schema.json` : 'schema.json';
    await downloadExplore('/explore/schema', '?download=json', name);
  }, { pending: 'Downloading…', success: 'Downloaded', toast: 'JSON download started' });
});

$('#explore-schema-ddl-dl').addEventListener('click', async (e) => {
  await withBtn(e.currentTarget, async () => {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.schema.sql` : 'schema.sql';
    await downloadExplore('/explore/schema', '?download=ddl', name);
  }, { pending: 'Downloading…', success: 'Downloaded', toast: 'DDL download started' });
});

applyExploreSavedConn();

bootstrap();

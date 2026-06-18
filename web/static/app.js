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
  const password = $('#password').value;
  $('#auth-error').classList.add('hidden');
  try {
    const setup = $('#auth-title').textContent.includes('Create');
    await api(setup ? '/setup' : '/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
    $('#password').value = '';
    showMain();
  } catch (err) {
    $('#auth-error').textContent = err.message;
    $('#auth-error').classList.remove('hidden');
  }
});

$('#logout-btn').addEventListener('click', async () => {
  await api('/logout', { method: 'POST' });
  showAuth(false);
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
  if (!job) {
    if (!engineOn) {
      $('#status-headline').textContent = 'Engine stopped';
      $('#status-detail').textContent = 'Start the migration engine to run jobs or scheduled incremental sync.';
    } else if (working) {
      $('#status-headline').textContent = 'Working';
      $('#status-detail').textContent = 'Background migration activity in progress.';
    } else {
      $('#status-headline').textContent = 'Ready';
      $('#status-detail').textContent = 'Engine is running. Create a job on the Jobs tab when you are ready.';
    }
    card.classList.add('hidden');
  } else {
    card.classList.remove('hidden');
    const paused = job.status === 'paused' || job.status === 'failed';
    $('#status-headline').textContent = job.status === 'running' ? 'Migration in progress' : titleCase(job.status);
    $('#status-detail').textContent = paused && job.error_message
      ? job.error_message
      : `${job.type.replace(/_/g, ' ')}${job.date_from || job.date_to ? ` · ${fmtDateRange(job)}` : ''} · started ${fmtTime(job.started_at)}`;
    $('#job-phase').textContent = job.current_phase || job.status;
    $('#job-table').textContent = job.current_table || '';
    $('#job-rows').textContent = fmtNum(job.rows_done);
    const est = $('#job-num-rows-total');
    if (job.rows_total > 0) {
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
      const progress = t.source_row_count_known
        ? `${fmtNum(t.rows_done)} / ${t.source_row_count_exceeded ? fmtNum(t.source_row_count) + '+' : fmtNum(t.source_row_count)} copied`
        : `${fmtNum(t.rows_done)} copied`;
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

  const events = (data.events || []).slice().reverse();
  const logEl = $('#event-log');
  if (!events.length) {
    logEl.className = 'event-log empty-state';
    logEl.textContent = 'Waiting for events…';
  } else {
    logEl.className = 'event-log';
    logEl.innerHTML = events.map(ev => `
      <div class="event-item ${esc(ev.level)}">
        <div>${esc(ev.message)}</div>
        <div class="event-time">${fmtTime(ev.created_at)}</div>
      </div>`).join('');
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
      await api('/connections/' + btn.dataset.del, { method: 'DELETE' });
      await loadConnections();
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
        <strong>${esc(j.type.replace('_', ' '))}</strong>
        <span class="badge ${j.status}">${j.status}</span>
        ${j.status === 'paused' || j.status === 'failed' ? `<button class="btn primary" type="button" data-resume="${j.id}" style="margin-left:auto">Resume</button>` : ''}
      </div>
      <div class="muted">${fmtNum(j.rows_done)} rows · ${j.tables_done}/${j.tables_total} tables · ${fmtTime(j.created_at)}</div>
      ${j.error_message ? `<div class="error">${esc(j.error_message)}</div>` : ''}
    </div>`).join('');
  el.querySelectorAll('[data-resume]').forEach(btn => {
    btn.addEventListener('click', () => resumeJob(btn.dataset.resume));
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
  const body = connPayload(new FormData(e.target));
  try {
    await api('/connections', { method: 'POST', body: JSON.stringify(body) });
    $('#conn-msg').textContent = 'Connection saved.';
    clearConnTestTable();
    e.target.reset();
    updateConnAuthUI();
    await loadConnections();
  } catch (err) {
    $('#conn-msg').textContent = err.message;
  }
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

$('#test-conn-btn').addEventListener('click', async () => {
  const body = connPayload(new FormData($('#conn-form')));
  $('#conn-msg').textContent = 'Testing host, port, and database in sequence...';
  clearConnTestTable();
  try {
    const res = await api('/connections/test/sequence', { method: 'POST', body: JSON.stringify(body) });
    renderConnTestTable(res.steps || []);
    if (res.status === 'error') {
      $('#conn-msg').textContent = 'Connection test failed at one step. Check the table below.';
    } else if (res.empty === false) {
      $('#conn-msg').textContent = `Connection successful — destination has ${res.table_count} table(s). Bulk migration will be blocked until empty.`;
    } else {
      $('#conn-msg').textContent = 'Connection successful.';
    }
  } catch (err) {
    $('#conn-msg').textContent = err.message;
    clearConnTestTable();
  }
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

$('#job-type').addEventListener('change', toggleDateRangeFields);
toggleDateRangeFields();

$('#job-form').addEventListener('submit', async (e) => {
  e.preventDefault();
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
  body.start = true;
  try {
    await api('/jobs', { method: 'POST', body: JSON.stringify(body) });
  } catch (err) {
    alert(err.message);
  }
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

$('#pause-job-btn').addEventListener('click', async () => {
  if (!confirm('Pause migration? Progress is saved and you can adjust settings before resuming.')) return;
  try {
    await activeJobPath('pause');
  } catch (err) {
    alert(err.message);
  }
});

$('#cancel-job-btn').addEventListener('click', async () => {
  if (!confirm('Cancel this job permanently? You cannot resume a cancelled job.')) return;
  try {
    await activeJobPath('cancel');
  } catch (err) {
    alert(err.message);
  }
});

async function resumeJob(jobId) {
  try {
    await api('/jobs/' + jobId + '/start', { method: 'POST' });
  } catch (err) {
    alert(err.message);
  }
}

$('#resume-job-btn').addEventListener('click', async () => {
  if (!activeJobId) return;
  try {
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
  } catch (err) {
    alert(err.message);
  }
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
  const fd = new FormData(e.target);
  const body = Object.fromEntries(fd.entries());
  body.default_batch_size = parseInt(body.default_batch_size, 10);
  body.default_parallel = parseInt(body.default_parallel, 10);
  body.default_chunk_timeout_sec = parseInt(body.default_chunk_timeout_sec, 10);
  body.default_row_count_fallback_cap = parseInt(body.default_row_count_fallback_cap || '0', 10);
  await api('/settings', { method: 'PUT', body: JSON.stringify(body) });
});

$('#connectivity-form').addEventListener('submit', async (e) => {
  e.preventDefault();
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
});

$('#password-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = Object.fromEntries(fd.entries());
  $('#password-msg').textContent = '';
  try {
    await api('/settings/password', { method: 'POST', body: JSON.stringify(body) });
    $('#password-msg').textContent = 'Password updated.';
    $('#password-msg').className = 'muted';
    e.target.reset();
  } catch (err) {
    $('#password-msg').textContent = err.message;
    $('#password-msg').className = 'error';
  }
});

$('#engine-start-btn').addEventListener('click', async () => {
  try {
    await api('/engine/start', { method: 'POST', body: '{}' });
  } catch (err) {
    alert(err.message);
  }
});

$('#engine-stop-btn').addEventListener('click', async () => {
  if (!confirm('Stop the migration engine? Running jobs will be paused.')) return;
  try {
    await api('/engine/stop', { method: 'POST', body: '{}' });
  } catch (err) {
    alert(err.message);
  }
});

function fillExploreSelects(list) {
  exploreConnections = list;
  const el = $('#explore-conn-select');
  el.innerHTML = '<option value="">Manual connection…</option>' +
    list.map(c => `<option value="${c.id}">${esc(c.name)} (${c.type})</option>`).join('');
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

$('#explore-list-btn').addEventListener('click', async () => {
  $('#explore-msg').textContent = 'Connecting…';
  exploreSelected = null;
  $('#explore-detail-card').classList.add('hidden');
  try {
    const res = await api('/explore/tables', {
      method: 'POST',
      body: JSON.stringify(explorePayload()),
    });
    renderExploreTables(res.tables || []);
    $('#explore-msg').textContent = `${(res.tables || []).length} table(s) found.`;
  } catch (err) {
    $('#explore-msg').textContent = err.message;
    $('#explore-table-list').className = 'explore-table-list empty-state';
    $('#explore-table-list').textContent = 'Could not list tables.';
  }
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

$('#explore-sample-btn').addEventListener('click', async () => {
  try {
    const body = await exploreTableRequest();
    const sample = await api('/explore/sample', { method: 'POST', body: JSON.stringify(body) });
    renderSampleTable(sample);
    $('#explore-sample-wrap').classList.remove('hidden');
  } catch (err) {
    alert(err.message);
  }
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

$('#explore-sample-dl').addEventListener('click', async () => {
  try {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.sample.csv` : 'sample.csv';
    await downloadExplore('/explore/sample', '?download=csv', name);
  } catch (err) {
    alert(err.message);
  }
});

$('#explore-schema-btn').addEventListener('click', async () => {
  try {
    const body = await exploreTableRequest();
    const res = await api('/explore/schema', { method: 'POST', body: JSON.stringify(body) });
    $('#explore-schema-pre').textContent = JSON.stringify(res.columns || [], null, 2);
    $('#explore-schema-wrap').classList.remove('hidden');
  } catch (err) {
    alert(err.message);
  }
});

$('#explore-schema-json-dl').addEventListener('click', async () => {
  try {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.schema.json` : 'schema.json';
    await downloadExplore('/explore/schema', '?download=json', name);
  } catch (err) {
    alert(err.message);
  }
});

$('#explore-schema-ddl-dl').addEventListener('click', async () => {
  try {
    const name = exploreSelected ? `${exploreSelected.schema}.${exploreSelected.name}.schema.sql` : 'schema.sql';
    await downloadExplore('/explore/schema', '?download=ddl', name);
  } catch (err) {
    alert(err.message);
  }
});

applyExploreSavedConn();

bootstrap();

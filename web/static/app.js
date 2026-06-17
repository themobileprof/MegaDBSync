const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

let authed = false;
let eventSource = null;

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
    const msg = (data && data.message) || (typeof data === 'string' ? data : res.statusText);
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

function fmtTime(ts) {
  if (!ts) return '';
  return new Date(ts).toLocaleString();
}

function renderDashboard(data) {
  const working = data.working;
  const anim = $('#status-anim');
  const indicator = $('#work-indicator');
  anim.classList.toggle('working', working);
  anim.classList.toggle('idle', !working);
  indicator.classList.toggle('hidden', !working);

  const job = data.active_job;
  const card = $('#active-job-card');
  if (!job) {
    $('#status-headline').textContent = 'Idle';
    $('#status-detail').textContent = 'No active migration. Close this page anytime — workers keep running in the background.';
    card.classList.add('hidden');
  } else {
    card.classList.remove('hidden');
    $('#status-headline').textContent = job.status === 'running' ? 'Migration in progress' : titleCase(job.status);
    $('#status-detail').textContent = `${job.type.replace('_', ' ')} · started ${fmtTime(job.started_at)}`;
    $('#job-phase').textContent = job.current_phase || job.status;
    $('#job-table').textContent = job.current_table || '';
    $('#job-rows').textContent = fmtNum(job.rows_done);
    $('#job-tables').textContent = `${job.tables_done}/${job.tables_total}`;
    const pct = job.tables_total ? Math.round((job.tables_done / job.tables_total) * 100) : 0;
    $('#job-progress').style.width = pct + '%';
  }

  const tasks = data.table_tasks || [];
  const taskEl = $('#table-task-list');
  if (!tasks.length) {
    taskEl.className = 'task-list empty-state';
    taskEl.textContent = 'No table tasks yet.';
  } else {
    taskEl.className = 'task-list';
    taskEl.innerHTML = tasks.map(t => `
      <div class="task-item ${t.status}">
        <div class="task-meta">
          <strong>${esc(t.schema_name)}.${esc(t.table_name)}</strong>
          <span class="badge ${t.status}">${t.status}</span>
        </div>
        <div class="task-meta muted">
          <span>${fmtNum(t.rows_done)} rows · ${t.sync_mode || '—'}</span>
          <span>${t.rows_per_sec ? t.rows_per_sec.toFixed(0) + '/s' : ''}</span>
        </div>
        ${t.status === 'running' ? '<div class="mini-bar"><span></span></div>' : ''}
      </div>`).join('');
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

  renderConnections(data.connections || []);
  renderJobs(data.recent_jobs || []);
  fillJobSelects(data.connections || []);
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
      <div class="muted">${esc(c.host)}:${c.port || '—'} · ${esc(c.database || '—')}</div>
      <button class="btn danger" style="margin-top:0.45rem" data-del="${c.id}" type="button">Delete</button>
    </div>`).join('');
  el.querySelectorAll('[data-del]').forEach(btn => {
    btn.addEventListener('click', async () => {
      if (!confirm('Delete this connection?')) return;
      await api('/connections/' + btn.dataset.del, { method: 'DELETE' });
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
      </div>
      <div class="muted">${fmtNum(j.rows_done)} rows · ${j.tables_done}/${j.tables_total} tables · ${fmtTime(j.created_at)}</div>
      ${j.error_message ? `<div class="error">${esc(j.error_message)}</div>` : ''}
    </div>`).join('');
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

$('#conn-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = Object.fromEntries(fd.entries());
  body.port = parseInt(body.port || '0', 10);
  try {
    await api('/connections', { method: 'POST', body: JSON.stringify(body) });
    $('#conn-msg').textContent = 'Connection saved.';
    e.target.reset();
  } catch (err) {
    $('#conn-msg').textContent = err.message;
  }
});

$('#test-conn-btn').addEventListener('click', async () => {
  const fd = new FormData($('#conn-form'));
  const body = Object.fromEntries(fd.entries());
  body.port = parseInt(body.port || '0', 10);
  try {
    const res = await api('/connections/test', { method: 'POST', body: JSON.stringify(body) });
    if (res.empty === false) {
      $('#conn-msg').textContent = `Connected — destination has ${res.table_count} table(s). Bulk migration will be blocked until empty.`;
    } else {
      $('#conn-msg').textContent = res.message ? 'Error: ' + res.message : 'Connection successful.';
    }
  } catch (err) {
    $('#conn-msg').textContent = err.message;
  }
});

$('#job-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = Object.fromEntries(fd.entries());
  body.batch_size = parseInt(body.batch_size || '0', 10);
  body.parallel_tables = parseInt(body.parallel_tables || '0', 10);
  body.start = true;
  try {
    await api('/jobs', { method: 'POST', body: JSON.stringify(body) });
  } catch (err) {
    alert(err.message);
  }
});

$('#cancel-job-btn').addEventListener('click', async () => {
  if (!confirm('Cancel the active job?')) return;
  await api('/jobs/active/cancel', { method: 'POST' }).catch(async () => {
    const jobs = await api('/jobs');
    const active = jobs.find(j => j.status === 'running' || j.status === 'pending');
    if (active) await api('/jobs/' + active.id + '/cancel', { method: 'POST' });
  });
});

async function loadSettingsForm() {
  const s = await api('/settings');
  const form = $('#settings-form');
  form.schedule_cron.value = s.schedule_cron || '0 */4 * * *';
  form.default_batch_size.value = s.default_batch_size || 50000;
  form.default_parallel.value = s.default_parallel || 2;
  if (s.schedule_source_id) form.schedule_source_id.value = s.schedule_source_id;
  if (s.schedule_dest_id) form.schedule_dest_id.value = s.schedule_dest_id;
}

$('#settings-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = Object.fromEntries(fd.entries());
  body.default_batch_size = parseInt(body.default_batch_size, 10);
  body.default_parallel = parseInt(body.default_parallel, 10);
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

bootstrap();

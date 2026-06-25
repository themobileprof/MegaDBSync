package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dataDir string
}

func Open(dataDir string) (*Store, error) {
	if err := osMkdir(dataDir); err != nil {
		return nil, err
	}
	dbPath := resolveDBPath(dataDir)
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-64000)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dataDir: dataDir}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  admin_password_hash TEXT NOT NULL DEFAULT '',
  schedule_cron TEXT NOT NULL DEFAULT '0 */4 * * *',
  schedule_source_id TEXT NOT NULL DEFAULT '',
  schedule_dest_id TEXT NOT NULL DEFAULT '',
  default_batch_size INTEGER NOT NULL DEFAULT 50000,
  default_parallel INTEGER NOT NULL DEFAULT 2,
  default_chunk_timeout_sec INTEGER NOT NULL DEFAULT 300,
  default_row_count_fallback_cap INTEGER NOT NULL DEFAULT 0,
  default_connect_timeout_sec INTEGER NOT NULL DEFAULT 30,
  mssql_encrypt INTEGER NOT NULL DEFAULT 1,
  mssql_trust_server_cert INTEGER NOT NULL DEFAULT 1,
  engine_enabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  database_name TEXT NOT NULL DEFAULT '',
  schema_name TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL,
  windows_auth INTEGER NOT NULL DEFAULT 0,
  password_enc TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  dest_id TEXT NOT NULL,
  status TEXT NOT NULL,
  batch_size INTEGER NOT NULL,
  parallel_tables INTEGER NOT NULL,
  chunk_timeout_sec INTEGER NOT NULL DEFAULT 0,
  table_filter TEXT NOT NULL DEFAULT '',
  date_column TEXT NOT NULL DEFAULT '',
  date_from TEXT NOT NULL DEFAULT '',
  date_to TEXT NOT NULL DEFAULT '',
  max_rows_per_table INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  rows_total INTEGER NOT NULL DEFAULT 0,
  rows_done INTEGER NOT NULL DEFAULT 0,
  tables_total INTEGER NOT NULL DEFAULT 0,
  tables_done INTEGER NOT NULL DEFAULT 0,
  current_table TEXT NOT NULL DEFAULT '',
  current_phase TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS table_tasks (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  schema_name TEXT NOT NULL,
  table_name TEXT NOT NULL,
  status TEXT NOT NULL,
  sync_mode TEXT NOT NULL DEFAULT '',
  watermark_col TEXT NOT NULL DEFAULT '',
  last_watermark TEXT NOT NULL DEFAULT '',
  last_max_key TEXT NOT NULL DEFAULT '',
  last_scn INTEGER NOT NULL DEFAULT 0,
  last_row_id TEXT NOT NULL DEFAULT '',
  source_row_count INTEGER NOT NULL DEFAULT 0,
  source_row_count_known INTEGER NOT NULL DEFAULT 0,
  source_row_count_approx INTEGER NOT NULL DEFAULT 0,
  source_row_count_exceeded INTEGER NOT NULL DEFAULT 0,
  rows_total INTEGER NOT NULL DEFAULT 0,
  rows_done INTEGER NOT NULL DEFAULT 0,
  rows_per_sec REAL NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  UNIQUE(job_id, schema_name, table_name)
);

CREATE TABLE IF NOT EXISTS sync_state (
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL,
  dest_id TEXT NOT NULL,
  schema_name TEXT NOT NULL,
  table_name TEXT NOT NULL,
  sync_mode TEXT NOT NULL,
  watermark_col TEXT NOT NULL DEFAULT '',
  last_watermark TEXT NOT NULL DEFAULT '',
  last_max_key TEXT NOT NULL DEFAULT '',
  last_scn INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE(source_id, dest_id, schema_name, table_name)
);

CREATE TABLE IF NOT EXISTS activity_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL DEFAULT '',
  level TEXT NOT NULL,
  message TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_table_tasks_job ON table_tasks(job_id);
CREATE INDEX IF NOT EXISTS idx_events_created ON activity_events(created_at DESC);

CREATE TABLE IF NOT EXISTS insert_failures (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL DEFAULT '',
  schema_name TEXT NOT NULL,
  table_name TEXT NOT NULL,
  row_index INTEGER NOT NULL DEFAULT 0,
  row_json TEXT NOT NULL DEFAULT '',
  error_msg TEXT NOT NULL DEFAULT '',
  statement TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_insert_failures_job ON insert_failures(job_id, created_at DESC);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := s.db.Exec(`INSERT INTO settings (id) VALUES (1)`)
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN schedule_source_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN schedule_dest_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE connections ADD COLUMN windows_auth INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN engine_enabled INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN default_chunk_timeout_sec INTEGER NOT NULL DEFAULT 300`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN chunk_timeout_sec INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE table_tasks ADD COLUMN last_row_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE table_tasks ADD COLUMN source_row_count INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN default_row_count_fallback_cap INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE table_tasks ADD COLUMN source_row_count_known INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE table_tasks ADD COLUMN source_row_count_approx INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE table_tasks ADD COLUMN source_row_count_exceeded INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN date_column TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN date_from TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN date_to TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN max_rows_per_table INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN default_connect_timeout_sec INTEGER NOT NULL DEFAULT 30`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN mssql_encrypt INTEGER NOT NULL DEFAULT 1`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN mssql_trust_server_cert INTEGER NOT NULL DEFAULT 1`)
	_, _ = s.db.Exec(`ALTER TABLE settings ADD COLUMN auto_start_engine INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (s *Store) GetSettings() (AppSettings, error) {
	var st AppSettings
	var engine, mssqlEnc, mssqlTrust, autoStart int
	err := s.db.QueryRow(`SELECT admin_password_hash, schedule_cron, schedule_source_id, schedule_dest_id, default_batch_size, default_parallel, default_chunk_timeout_sec, default_row_count_fallback_cap, default_connect_timeout_sec, mssql_encrypt, mssql_trust_server_cert, engine_enabled, auto_start_engine FROM settings WHERE id = 1`).
		Scan(&st.AdminPasswordHash, &st.ScheduleCron, &st.ScheduleSourceID, &st.ScheduleDestID, &st.DefaultBatchSize, &st.DefaultParallel, &st.DefaultChunkTimeoutSec, &st.DefaultRowCountFallbackCap, &st.DefaultConnectTimeoutSec, &mssqlEnc, &mssqlTrust, &engine, &autoStart)
	st.MssqlEncrypt = mssqlEnc == 1
	st.MssqlTrustServerCert = mssqlTrust == 1
	st.EngineEnabled = engine == 1
	st.AutoStartEngine = autoStart == 1
	return st, err
}

func (s *Store) UpdateSettings(st AppSettings) error {
	_, err := s.db.Exec(`UPDATE settings SET schedule_cron=?, schedule_source_id=?, schedule_dest_id=?, default_batch_size=?, default_parallel=?, default_chunk_timeout_sec=?, default_row_count_fallback_cap=?, default_connect_timeout_sec=?, mssql_encrypt=?, mssql_trust_server_cert=?, auto_start_engine=? WHERE id=1`,
		st.ScheduleCron, st.ScheduleSourceID, st.ScheduleDestID, st.DefaultBatchSize, st.DefaultParallel, st.DefaultChunkTimeoutSec, st.DefaultRowCountFallbackCap, st.DefaultConnectTimeoutSec, boolInt(st.MssqlEncrypt), boolInt(st.MssqlTrustServerCert), boolInt(st.AutoStartEngine))
	return err
}

func (s *Store) SetAutoStartEngine(on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE settings SET auto_start_engine=? WHERE id=1`, v)
	return err
}

func (s *Store) SetEngineEnabled(on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE settings SET engine_enabled=? WHERE id=1`, v)
	return err
}

func (s *Store) SetAdminPasswordHash(hash string) error {
	_, err := s.db.Exec(`UPDATE settings SET admin_password_hash=? WHERE id=1`, hash)
	return err
}

func (s *Store) ListConnections() ([]Connection, error) {
	rows, err := s.db.Query(`SELECT id, name, type, host, port, database_name, schema_name, username, windows_auth, password_enc, created_at, updated_at FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnection(id string) (Connection, error) {
	row := s.db.QueryRow(`SELECT id, name, type, host, port, database_name, schema_name, username, windows_auth, password_enc, created_at, updated_at FROM connections WHERE id=?`, id)
	return scanConnectionRow(row)
}

func (s *Store) SaveConnection(c Connection, plainPassword string) (Connection, error) {
	now := time.Now().UTC()
	if c.ID == "" {
		c.ID = uuid.NewString()
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	enc := ""
	if c.WindowsAuth {
		enc = ""
	} else if plainPassword != "" {
		var err error
		enc, err = EncryptPassword(s.dataDir, plainPassword)
		if err != nil {
			return c, err
		}
	} else if c.ID != "" {
		old, err := s.GetConnection(c.ID)
		if err != nil {
			return c, err
		}
		encOld, _ := s.dbPasswordEnc(c.ID)
		if encOld != "" {
			enc = encOld
		}
		_ = old
	}
	_, err := s.db.Exec(`
INSERT INTO connections (id, name, type, host, port, database_name, schema_name, username, windows_auth, password_enc, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, type=excluded.type, host=excluded.host, port=excluded.port,
  database_name=excluded.database_name, schema_name=excluded.schema_name,
  username=excluded.username, windows_auth=excluded.windows_auth,
  password_enc=CASE WHEN excluded.windows_auth = 1 THEN '' WHEN excluded.password_enc != '' THEN excluded.password_enc ELSE connections.password_enc END,
  updated_at=excluded.updated_at`,
		c.ID, c.Name, c.Type, c.Host, c.Port, c.Database, c.Schema, c.Username, boolToInt(c.WindowsAuth), enc,
		c.CreatedAt.Format(time.RFC3339), c.UpdatedAt.Format(time.RFC3339))
	return c, err
}

func (s *Store) dbPasswordEnc(id string) (string, error) {
	var enc string
	err := s.db.QueryRow(`SELECT password_enc FROM connections WHERE id=?`, id).Scan(&enc)
	return enc, err
}

func (s *Store) ConnectionPassword(id string) (string, error) {
	c, err := s.GetConnection(id)
	if err != nil {
		return "", err
	}
	if c.WindowsAuth {
		return "", nil
	}
	enc, err := s.dbPasswordEnc(id)
	if err != nil {
		return "", err
	}
	return DecryptPassword(s.dataDir, enc)
}

func (s *Store) DeleteConnection(id string) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE id=?`, id)
	return err
}

func (s *Store) CreateJob(j Job) (Job, error) {
	now := time.Now().UTC()
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	j.Status = JobPending
	j.CreatedAt = now
	j.UpdatedAt = now
	_, err := s.db.Exec(`
INSERT INTO jobs (id, type, source_id, dest_id, status, batch_size, parallel_tables, chunk_timeout_sec, table_filter, date_column, date_from, date_to, max_rows_per_table, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Type, j.SourceID, j.DestID, j.Status, j.BatchSize, j.ParallelTables, j.ChunkTimeoutSec, j.TableFilter,
		j.DateColumn, j.DateFrom, j.DateTo, j.MaxRowsPerTable,
		j.CreatedAt.Format(time.RFC3339), j.UpdatedAt.Format(time.RFC3339))
	return j, err
}

func (s *Store) UpdateJobSettings(id string, batchSize, parallel, chunkTimeout, maxRows int, dateColumn, dateFrom, dateTo string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE jobs SET batch_size=?, parallel_tables=?, chunk_timeout_sec=?, max_rows_per_table=?, date_column=?, date_from=?, date_to=?, updated_at=? WHERE id=? AND status IN (?, ?)`,
		batchSize, parallel, chunkTimeout, maxRows, dateColumn, dateFrom, dateTo, now, id, JobPaused, JobFailed)
	return err
}

func (s *Store) PrepareJobResume(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE jobs SET status=?, error_message='', completed_at=NULL, updated_at=? WHERE id=? AND status IN (?, ?)`,
		JobRunning, now, id, JobPaused, JobFailed)
	return err
}

func (s *Store) UpdateJob(j Job) error {
	j.UpdatedAt = time.Now().UTC()
	var started interface{}
	if j.StartedAt != nil {
		started = j.StartedAt.Format(time.RFC3339)
	}
	now := j.UpdatedAt.Format(time.RFC3339)
	if j.CompletedAt != nil {
		completed := j.CompletedAt.Format(time.RFC3339)
		_, err := s.db.Exec(`
UPDATE jobs SET status=?, error_message=?, rows_total=?, rows_done=?, tables_total=?, tables_done=?,
  current_table=?, current_phase=?, started_at=COALESCE(?, started_at), completed_at=?, updated_at=?
WHERE id=?`,
			j.Status, j.ErrorMessage, j.RowsTotal, j.RowsDone, j.TablesTotal, j.TablesDone,
			j.CurrentTable, j.CurrentPhase, started, completed, now, j.ID)
		return err
	}
	_, err := s.db.Exec(`
UPDATE jobs SET status=?, error_message=?, rows_total=?, rows_done=?, tables_total=?, tables_done=?,
  current_table=?, current_phase=?, started_at=COALESCE(?, started_at), completed_at=NULL, updated_at=?
WHERE id=?`,
		j.Status, j.ErrorMessage, j.RowsTotal, j.RowsDone, j.TablesTotal, j.TablesDone,
		j.CurrentTable, j.CurrentPhase, started, now, j.ID)
	return err
}

func (s *Store) GetJob(id string) (Job, error) {
	row := s.db.QueryRow(`SELECT id, type, source_id, dest_id, status, batch_size, parallel_tables, chunk_timeout_sec, table_filter,
  date_column, date_from, date_to, max_rows_per_table, error_message, rows_total, rows_done, tables_total, tables_done, current_table, current_phase,
  started_at, completed_at, created_at, updated_at FROM jobs WHERE id=?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, type, source_id, dest_id, status, batch_size, parallel_tables, chunk_timeout_sec, table_filter,
  date_column, date_from, date_to, max_rows_per_table, error_message, rows_total, rows_done, tables_total, tables_done, current_table, current_phase,
  started_at, completed_at, created_at, updated_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FindResumableBulkJob returns a paused or failed bulk job for the same source/destination pair.
func (s *Store) FindResumableBulkJob(sourceID, destID string) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, type, source_id, dest_id, status, batch_size, parallel_tables, chunk_timeout_sec, table_filter,
  date_column, date_from, date_to, max_rows_per_table, error_message, rows_total, rows_done, tables_total, tables_done, current_table, current_phase,
  started_at, completed_at, created_at, updated_at FROM jobs
  WHERE type=? AND source_id=? AND dest_id=? AND status IN (?, ?)
  ORDER BY created_at DESC LIMIT 1`, JobBulkFull, sourceID, destID, JobPaused, JobFailed)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) ActiveJob() (*Job, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT id, type, source_id, dest_id, status, batch_size, parallel_tables, chunk_timeout_sec, table_filter,
  date_column, date_from, date_to, max_rows_per_table, error_message, rows_total, rows_done, tables_total, tables_done, current_table, current_phase,
  started_at, completed_at, created_at, updated_at FROM jobs WHERE status IN ('pending','running','paused') ORDER BY created_at LIMIT 1`))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) UpsertTableTask(t TableTask) error {
	now := time.Now().UTC()
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	t.UpdatedAt = now
	var started, completed interface{}
	if t.StartedAt != nil {
		started = t.StartedAt.Format(time.RFC3339)
	}
	if t.CompletedAt != nil {
		completed = t.CompletedAt.Format(time.RFC3339)
	}
	_, err := s.db.Exec(`
INSERT INTO table_tasks (id, job_id, schema_name, table_name, status, sync_mode, watermark_col,
  last_watermark, last_max_key, last_scn, last_row_id, source_row_count, source_row_count_known,
  source_row_count_approx, source_row_count_exceeded, rows_total, rows_done, rows_per_sec, error_message,
  started_at, completed_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id, schema_name, table_name) DO UPDATE SET
  status=excluded.status, sync_mode=excluded.sync_mode, watermark_col=excluded.watermark_col,
  last_watermark=excluded.last_watermark, last_max_key=excluded.last_max_key, last_scn=excluded.last_scn,
  last_row_id=excluded.last_row_id, source_row_count=excluded.source_row_count,
  source_row_count_known=excluded.source_row_count_known,
  source_row_count_approx=excluded.source_row_count_approx,
  source_row_count_exceeded=excluded.source_row_count_exceeded,
  rows_total=excluded.rows_total, rows_done=excluded.rows_done, rows_per_sec=excluded.rows_per_sec,
  error_message=excluded.error_message, started_at=COALESCE(table_tasks.started_at, excluded.started_at),
  completed_at=excluded.completed_at, updated_at=excluded.updated_at`,
		t.ID, t.JobID, t.SchemaName, t.TableName, t.Status, t.SyncMode, t.WatermarkCol,
		t.LastWatermark, t.LastMaxKey, t.LastSCN, t.LastRowID, t.SourceRowCount, boolInt(t.SourceRowCountKnown),
		boolInt(t.SourceRowCountApprox), boolInt(t.SourceRowCountExceeded),
		t.RowsTotal, t.RowsDone, t.RowsPerSec, t.ErrorMessage,
		started, completed, t.UpdatedAt.Format(time.RFC3339))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) ListTableTasks(jobID string) ([]TableTask, error) {
	rows, err := s.db.Query(`SELECT id, job_id, schema_name, table_name, status, sync_mode, watermark_col,
  last_watermark, last_max_key, last_scn, last_row_id, source_row_count, source_row_count_known,
  source_row_count_approx, source_row_count_exceeded, rows_total, rows_done, rows_per_sec, error_message,
  started_at, completed_at, updated_at FROM table_tasks WHERE job_id=? ORDER BY source_row_count ASC, schema_name, table_name`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TableTask
	for rows.Next() {
		t, err := scanTableTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpsertSyncState(sourceID, destID, schema, table, mode, wmCol, wm, maxKey string, scn int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("%s:%s:%s:%s", sourceID, destID, schema, table)
	_, err := s.db.Exec(`
INSERT INTO sync_state (id, source_id, dest_id, schema_name, table_name, sync_mode, watermark_col,
  last_watermark, last_max_key, last_scn, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  sync_mode=excluded.sync_mode, watermark_col=excluded.watermark_col,
  last_watermark=excluded.last_watermark, last_max_key=excluded.last_max_key,
  last_scn=excluded.last_scn, updated_at=excluded.updated_at`,
		id, sourceID, destID, schema, table, mode, wmCol, wm, maxKey, scn, now)
	return err
}

func (s *Store) GetSyncState(sourceID, destID, schema, table string) (mode, wmCol, wm, maxKey string, scn int64, err error) {
	id := fmt.Sprintf("%s:%s:%s:%s", sourceID, destID, schema, table)
	err = s.db.QueryRow(`SELECT sync_mode, watermark_col, last_watermark, last_max_key, last_scn FROM sync_state WHERE id=?`, id).
		Scan(&mode, &wmCol, &wm, &maxKey, &scn)
	if err == sql.ErrNoRows {
		return "", "", "", "", 0, nil
	}
	return
}

func (s *Store) LogEvent(jobID, level, message string) error {
	_, err := s.db.Exec(`INSERT INTO activity_events (job_id, level, message, created_at) VALUES (?, ?, ?, ?)`,
		jobID, level, message, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM activity_events WHERE id NOT IN (SELECT id FROM activity_events ORDER BY id DESC LIMIT 500)`)
	return nil
}

func (s *Store) RecentEvents(limit int) ([]ActivityEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, job_id, level, message, created_at FROM activity_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityEvent
	for rows.Next() {
		var e ActivityEvent
		var ts string
		if err := rows.Scan(&e.ID, &e.JobID, &e.Level, &e.Message, &ts); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Dashboard() (DashboardState, error) {
	return s.dashboard(true)
}

func (s *Store) DashboardLive() (DashboardState, error) {
	return s.dashboard(false)
}

func (s *Store) dashboard(includeConnections bool) (DashboardState, error) {
	var ds DashboardState
	active, err := s.ActiveJob()
	if err != nil {
		return ds, err
	}
	ds.ActiveJob = active
	ds.RecentJobs, err = s.ListJobs(10)
	if err != nil {
		return ds, err
	}
	if active != nil {
		ds.TableTasks, err = s.ListTableTasks(active.ID)
		if err != nil {
			return ds, err
		}
	}
	ds.Events, err = s.RecentEvents(50)
	if err != nil {
		return ds, err
	}
	if includeConnections {
		ds.Connections, err = s.ListConnections()
		if err != nil {
			return ds, err
		}
	}
	ds.Working = active != nil && active.Status == JobRunning
	st, err := s.GetSettings()
	if err == nil {
		ds.EngineEnabled = st.EngineEnabled
	}
	return ds, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanConnection(rows scannable) (Connection, error) {
	var c Connection
	var created, updated, enc string
	var winAuth int
	if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Host, &c.Port, &c.Database, &c.Schema, &c.Username, &winAuth, &enc, &created, &updated); err != nil {
		return c, err
	}
	c.WindowsAuth = winAuth == 1
	c.CreatedAt, _ = time.Parse(time.RFC3339, created)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return c, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func resolveDBPath(dataDir string) string {
	return filepath.Join(dataDir, "megadbsync.db")
}

func osMkdir(dir string) error {
	return os.MkdirAll(dir, 0)
}

func scanConnectionRow(row *sql.Row) (Connection, error) {
	return scanConnection(row)
}

func scanJob(rows scannable) (Job, error) {
	var j Job
	var started, completed, created, updated sql.NullString
	if err := rows.Scan(&j.ID, &j.Type, &j.SourceID, &j.DestID, &j.Status, &j.BatchSize, &j.ParallelTables, &j.ChunkTimeoutSec, &j.TableFilter,
		&j.DateColumn, &j.DateFrom, &j.DateTo, &j.MaxRowsPerTable, &j.ErrorMessage, &j.RowsTotal, &j.RowsDone, &j.TablesTotal, &j.TablesDone, &j.CurrentTable, &j.CurrentPhase,
		&started, &completed, &created, &updated); err != nil {
		return j, err
	}
	if started.Valid {
		t, _ := time.Parse(time.RFC3339, started.String)
		j.StartedAt = &t
	}
	if completed.Valid {
		t, _ := time.Parse(time.RFC3339, completed.String)
		j.CompletedAt = &t
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339, created.String)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updated.String)
	return j, nil
}

func scanTableTask(rows scannable) (TableTask, error) {
	var t TableTask
	var started, completed, updated sql.NullString
	var known, approx, exceeded int
	if err := rows.Scan(&t.ID, &t.JobID, &t.SchemaName, &t.TableName, &t.Status, &t.SyncMode, &t.WatermarkCol,
		&t.LastWatermark, &t.LastMaxKey, &t.LastSCN, &t.LastRowID, &t.SourceRowCount, &known, &approx, &exceeded,
		&t.RowsTotal, &t.RowsDone, &t.RowsPerSec, &t.ErrorMessage,
		&started, &completed, &updated); err != nil {
		return t, err
	}
	t.SourceRowCountKnown = known == 1
	t.SourceRowCountApprox = approx == 1
	t.SourceRowCountExceeded = exceeded == 1
	if started.Valid {
		tt, _ := time.Parse(time.RFC3339, started.String)
		t.StartedAt = &tt
	}
	if completed.Valid {
		tt, _ := time.Parse(time.RFC3339, completed.String)
		t.CompletedAt = &tt
	}
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updated.String)
	return t, nil
}

func (s *Store) LogInsertFailure(rec InsertFailureRecord) error {
	_, err := s.db.Exec(`INSERT INTO insert_failures (job_id, schema_name, table_name, row_index, row_json, error_msg, statement, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.JobID, rec.SchemaName, rec.TableName, rec.RowIndex, rec.RowJSON, rec.ErrorMsg, rec.Statement, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM insert_failures WHERE id NOT IN (SELECT id FROM insert_failures ORDER BY id DESC LIMIT 5000)`)
	return nil
}

func (s *Store) ListInsertFailures(jobID string, limit int) ([]InsertFailureRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if jobID != "" {
		rows, err = s.db.Query(`SELECT id, job_id, schema_name, table_name, row_index, row_json, error_msg, statement, created_at FROM insert_failures WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	} else {
		rows, err = s.db.Query(`SELECT id, job_id, schema_name, table_name, row_index, row_json, error_msg, statement, created_at FROM insert_failures ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InsertFailureRecord
	for rows.Next() {
		var rec InsertFailureRecord
		var ts string
		if err := rows.Scan(&rec.ID, &rec.JobID, &rec.SchemaName, &rec.TableName, &rec.RowIndex, &rec.RowJSON, &rec.ErrorMsg, &rec.Statement, &ts); err != nil {
			return nil, err
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, rec)
	}
	return out, rows.Err()
}

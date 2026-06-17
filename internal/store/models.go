package store

import "time"

type ConnType string

const (
	ConnOracle ConnType = "oracle"
	ConnMSSQL  ConnType = "mssql"
)

type Connection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      ConnType  `json:"type"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Database  string    `json:"database"`
	Schema      string    `json:"schema"`
	Username    string    `json:"username"`
	WindowsAuth bool      `json:"windows_auth"`
	Password    string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type JobType string

const (
	JobBulkFull        JobType = "bulk_full"
	JobIncrementalSync JobType = "incremental_sync"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobPaused    JobStatus = "paused"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type Job struct {
	ID              string    `json:"id"`
	Type            JobType   `json:"type"`
	SourceID        string    `json:"source_id"`
	DestID          string    `json:"dest_id"`
	Status          JobStatus `json:"status"`
	BatchSize       int       `json:"batch_size"`
	ParallelTables  int       `json:"parallel_tables"`
	TableFilter     string    `json:"table_filter"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	RowsTotal       int64     `json:"rows_total"`
	RowsDone        int64     `json:"rows_done"`
	TablesTotal     int       `json:"tables_total"`
	TablesDone      int       `json:"tables_done"`
	CurrentTable    string    `json:"current_table,omitempty"`
	CurrentPhase    string    `json:"current_phase,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TableTask struct {
	ID            string    `json:"id"`
	JobID         string    `json:"job_id"`
	SchemaName    string    `json:"schema_name"`
	TableName     string    `json:"table_name"`
	Status        JobStatus `json:"status"`
	SyncMode      string    `json:"sync_mode"`
	WatermarkCol  string    `json:"watermark_col,omitempty"`
	LastWatermark string    `json:"last_watermark,omitempty"`
	LastMaxKey    string    `json:"last_max_key,omitempty"`
	LastSCN       int64     `json:"last_scn,omitempty"`
	RowsTotal     int64     `json:"rows_total"`
	RowsDone      int64     `json:"rows_done"`
	RowsPerSec    float64   `json:"rows_per_sec"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type AppSettings struct {
	AdminPasswordHash string `json:"-"`
	ScheduleCron      string `json:"schedule_cron"`
	ScheduleSourceID  string `json:"schedule_source_id"`
	ScheduleDestID    string `json:"schedule_dest_id"`
	DefaultBatchSize  int    `json:"default_batch_size"`
	DefaultParallel   int    `json:"default_parallel"`
}

type ActivityEvent struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type DashboardState struct {
	ActiveJob   *Job         `json:"active_job,omitempty"`
	RecentJobs  []Job        `json:"recent_jobs"`
	TableTasks  []TableTask  `json:"table_tasks"`
	Events      []ActivityEvent `json:"events"`
	Connections []Connection `json:"connections"`
	Working     bool         `json:"working"`
}

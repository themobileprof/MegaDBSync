package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/themobileprof/megadbsync/internal/auth"
	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/jobs"
	"github.com/themobileprof/megadbsync/internal/migrate"
	"github.com/themobileprof/megadbsync/internal/store"
)

type Server struct {
	Store     *store.Store
	Auth      *auth.Manager
	Runner    *jobs.Runner
	Scheduler *jobs.Scheduler
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/bootstrap", s.handleBootstrap)

	protected := http.NewServeMux()
	protected.HandleFunc("/logout", s.handleLogout)
	protected.HandleFunc("/status", s.handleStatus)
	protected.HandleFunc("/events", s.handleSSE)
	protected.HandleFunc("/connections", s.handleConnections)
	protected.HandleFunc("/connections/test/sequence", s.handleTestConnectionSequence)
	protected.HandleFunc("/connections/test", s.handleTestConnection)
	protected.HandleFunc("/connections/", s.handleConnectionByID)
	protected.HandleFunc("/jobs", s.handleJobs)
	protected.HandleFunc("/jobs/", s.handleJobByID)
	protected.HandleFunc("/settings", s.handleSettings)
	protected.HandleFunc("/settings/password", s.handleChangePassword)
	protected.HandleFunc("/engine/", s.handleEngine)
	protected.HandleFunc("/explore/tables", s.handleExploreTables)
	protected.HandleFunc("/explore/sample", s.handleExploreSample)
	protected.HandleFunc("/explore/schema", s.handleExploreSchema)
	protected.HandleFunc("/explore/migration-report", s.handleExploreMigrationReport)
	protected.HandleFunc("/explore/table-dependencies", s.handleExploreTableDependencies)
	protected.HandleFunc("/explore/discover-table-relationships", s.handleExploreDiscoverTableRelationships)
	mux.Handle("/api/", http.StripPrefix("/api", s.Auth.Middleware(protected)))
	return mux
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSettings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"setup_required": st.AdminPasswordHash == ""})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Auth.HasPassword() {
		writeErrorJSON(w, "already configured", http.StatusConflict)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < 8 {
		writeErrorJSON(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetAdminPasswordHash(hash); err != nil {
		writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Auth.SetPasswordHash(hash)
	s.Auth.Login(w, body.Password)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := s.Store.GetSettings()
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Auth.SetPasswordHash(st.AdminPasswordHash)
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.Auth.HasPassword() {
		writeErrorJSON(w, "setup required", http.StatusPreconditionRequired)
		return
	}
	if !s.Auth.Login(w, body.Password) {
		writeErrorJSON(w, "invalid password", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.Auth.Logout(w, r)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.Dashboard()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.enrichDashboard(&ds)
	writeJSON(w, ds)
}

func (s *Server) enrichDashboard(ds *store.DashboardState) {
	st, err := s.Store.GetSettings()
	if err != nil || st.ScheduleCron == "" || st.ScheduleSourceID == "" || st.ScheduleDestID == "" {
		return
	}
	now := time.Now().UTC()
	ds.Schedule = &store.ScheduleInfo{
		Cron:      st.ScheduleCron,
		Label:     jobs.ScheduleLabel(st.ScheduleCron),
		SourceID:  st.ScheduleSourceID,
		DestID:    st.ScheduleDestID,
		Armed:     st.EngineEnabled,
		NextRunAt: jobs.NextCronRun(st.ScheduleCron, now),
	}
	for i := range ds.RecentJobs {
		j := ds.RecentJobs[i]
		if j.Type == store.JobIncrementalSync && j.SourceID == st.ScheduleSourceID && j.DestID == st.ScheduleDestID {
			copy := j
			ds.Schedule.LastJob = &copy
			break
		}
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(full bool) {
		var ds store.DashboardState
		var err error
		if full {
			ds, err = s.Store.Dashboard()
		} else {
			ds, err = s.Store.DashboardLive()
		}
		if err != nil {
			return
		}
		s.enrichDashboard(&ds)
		b, _ := json.Marshal(ds)
		_, _ = io.WriteString(w, "event: status\ndata: ")
		_, _ = w.Write(b)
		_, _ = io.WriteString(w, "\n\n")
		flusher.Flush()
	}
	send(true)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	notify := s.Runner.Notify()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send(false)
		case <-notify:
			send(false)
		}
	}
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.Store.ListConnections()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var c store.Connection
		var body struct {
			store.Connection
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c = body.Connection
		if c.Name == "" || c.Host == "" {
			http.Error(w, "name and host required", http.StatusBadRequest)
			return
		}
		if c.Type == store.ConnMSSQL && c.WindowsAuth {
			// Windows integrated auth — no SQL login required.
		} else if c.Username == "" {
			http.Error(w, "username required", http.StatusBadRequest)
			return
		}
		if c.Type != store.ConnOracle && c.Type != store.ConnMSSQL {
			http.Error(w, "type must be oracle or mssql", http.StatusBadRequest)
			return
		}
		saved, err := s.Store.SaveConnection(c, body.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConnectionByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/connections/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := s.Store.DeleteConnection(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "deleted"})
	case http.MethodPut:
		var body struct {
			store.Connection
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.ID = id
		saved, err := s.Store.SaveConnection(body.Connection, body.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		store.Connection
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pass := body.Password
	if pass == "" && body.ID != "" {
		var err error
		pass, err = s.Store.ConnectionPassword(body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := dbconn.TestConnection(r.Context(), body.Connection, pass); err != nil {
		writeJSON(w, map[string]string{"status": "error", "message": err.Error()})
		return
	}
	if body.Type == store.ConnMSSQL {
		db, err := dbconn.OpenMSSQL(r.Context(), body.Connection, pass)
		if err == nil {
			count, _ := dbconn.DestinationMustBeEmpty(r.Context(), db, body.Schema)
			_ = db.Close()
			writeJSON(w, map[string]any{"status": "ok", "table_count": count, "empty": count == 0})
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

type connectionTestStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	DurationMs int64  `json:"duration_ms"`
}

func (s *Server) handleTestConnectionSequence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		store.Connection
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pass := body.Password
	if pass == "" && body.ID != "" {
		var err error
		pass, err = s.Store.ConnectionPassword(body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	opts := dbconn.DefaultConnectOpts()
	timeout := time.Duration(opts.TimeoutSec) * time.Second
	steps := make([]connectionTestStep, 0, 6)
	addStep := func(name string, started time.Time, status, msg string) {
		steps = append(steps, connectionTestStep{
			Name:       name,
			Status:     status,
			Message:    msg,
			DurationMs: time.Since(started).Milliseconds(),
		})
	}

	start := time.Now()
	if strings.TrimSpace(body.Host) == "" {
		addStep("Host resolution", start, "error", "host is required")
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": "host is required"})
		return
	}
	lookupCtx, cancelLookup := context.WithTimeout(r.Context(), timeout)
	_, err := net.DefaultResolver.LookupIPAddr(lookupCtx, body.Host)
	cancelLookup()
	if err != nil {
		addStep("Host resolution", start, "error", err.Error())
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": err.Error()})
		return
	}
	addStep("Host resolution", start, "ok", "DNS/IP lookup successful")

	port := body.Port
	switch body.Type {
	case store.ConnOracle:
		if port == 0 {
			port = 1521
		}
	case store.ConnMSSQL:
		if port == 0 {
			port = 1433
		}
	default:
		addStep("Port connectivity", time.Now(), "error", "unsupported connection type")
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": "unsupported connection type"})
		return
	}

	start = time.Now()
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(r.Context(), "tcp", net.JoinHostPort(body.Host, strconv.Itoa(port)))
	if err != nil {
		addStep("Port connectivity", start, "error", err.Error())
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": err.Error()})
		return
	}
	_ = conn.Close()
	addStep("Port connectivity", start, "ok", "TCP connection successful")

	start = time.Now()
	if err := dbconn.TestConnection(r.Context(), body.Connection, pass); err != nil {
		addStep("Database login", start, "error", err.Error())
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": err.Error()})
		return
	}
	addStep("Database login", start, "ok", "Connected and ping successful")

	db, err := dbconn.OpenFromParams(r.Context(), body.Connection, pass)
	if err != nil {
		start = time.Now()
		addStep("Schema access", start, "error", err.Error())
		writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": err.Error()})
		return
	}
	defer db.Close()

	schemaName := strings.TrimSpace(body.Schema)
	if schemaName == "" {
		addStep("Schema access", time.Now(), "skipped", "No schema specified (optional)")
	} else {
		start = time.Now()
		tableCount, err := dbconn.VerifySchema(r.Context(), db, body.Type, schemaName)
		if err != nil {
			addStep("Schema access", start, "error", err.Error())
			writeJSON(w, map[string]any{"status": "error", "steps": steps, "message": err.Error()})
			return
		}
		addStep("Schema access", start, "ok", fmt.Sprintf("Schema %q found — %d table(s) visible", schemaName, tableCount))
	}

	resp := map[string]any{
		"status": "ok",
		"steps":  steps,
	}
	if body.Type == store.ConnMSSQL {
		start = time.Now()
		count, err := dbconn.DestinationMustBeEmpty(r.Context(), db, body.Schema)
		if err != nil {
			addStep("Destination scan", start, "error", err.Error())
			resp["status"] = "error"
			resp["message"] = err.Error()
			resp["steps"] = steps
			writeJSON(w, resp)
			return
		}
		destSchema := dbconn.EffectiveDestSchema(body.Schema)
		msg := fmt.Sprintf("Schema [%s] has %d table(s) — bulk migration %s", destSchema, count, bulkMigrationNote(count))
		addStep("Destination scan", start, "ok", msg)
		resp["table_count"] = count
		resp["empty"] = count == 0
		resp["steps"] = steps
	}

	writeJSON(w, resp)
}

func bulkMigrationNote(tableCount int) string {
	if tableCount == 0 {
		return "can proceed (empty)"
	}
	return "blocked until empty"
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.Store.ListJobs(30)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		var body struct {
			Type            store.JobType `json:"type"`
			SourceID        string        `json:"source_id"`
			DestID          string        `json:"dest_id"`
			BatchSize       int           `json:"batch_size"`
			ParallelTables  int           `json:"parallel_tables"`
			ChunkTimeoutSec int           `json:"chunk_timeout_sec"`
			TableFilter     string        `json:"table_filter"`
			DateColumn      string        `json:"date_column"`
			DateFrom        string        `json:"date_from"`
			DateTo          string        `json:"date_to"`
			MaxRowsPerTable int           `json:"max_rows_per_table"`
			Start           bool          `json:"start"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		st, _ := s.Store.GetSettings()
		if body.BatchSize <= 0 {
			body.BatchSize = st.DefaultBatchSize
		}
		if body.ParallelTables <= 0 {
			body.ParallelTables = st.DefaultParallel
		}
		if _, err := migrate.ParseDateBounds(body.DateFrom, body.DateTo); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job := store.Job{
			Type: body.Type, SourceID: body.SourceID, DestID: body.DestID,
			BatchSize: body.BatchSize, ParallelTables: body.ParallelTables,
			ChunkTimeoutSec: body.ChunkTimeoutSec, TableFilter: body.TableFilter,
			DateColumn: body.DateColumn, DateFrom: body.DateFrom, DateTo: body.DateTo,
			MaxRowsPerTable: body.MaxRowsPerTable,
		}
		if job.Type == "" {
			job.Type = store.JobBulkFull
		}
		if job.Type == store.JobSchemaSample {
			job.MaxRowsPerTable = migrate.SchemaSampleRowsPerTable
			if job.ParallelTables <= 0 {
				job.ParallelTables = 2
			}
		}
		src, err := s.Store.GetConnection(body.SourceID)
		if err != nil {
			http.Error(w, "invalid source", http.StatusBadRequest)
			return
		}
		dst, err := s.Store.GetConnection(body.DestID)
		if err != nil {
			http.Error(w, "invalid destination", http.StatusBadRequest)
			return
		}
		if src.Type != store.ConnOracle || dst.Type != store.ConnMSSQL {
			http.Error(w, "source must be oracle and destination must be mssql", http.StatusBadRequest)
			return
		}
		if job.Type == store.JobBulkFull {
			pass, _ := s.Store.ConnectionPassword(dst.ID)
			db, err := dbconn.OpenMSSQL(r.Context(), dst, pass)
			if err != nil {
				http.Error(w, "destination unreachable", http.StatusBadRequest)
				return
			}
			count, err := dbconn.DestinationMustBeEmpty(r.Context(), db, dst.Schema)
			var tables []string
			if err == nil && count > 0 {
				tables, _ = dbconn.ListDestinationTables(r.Context(), db, dst.Schema)
			}
			_ = db.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if count > 0 {
				var resumableID, resumableStatus string
				if resumable, _ := s.Store.FindResumableBulkJob(body.SourceID, body.DestID); resumable != nil {
					resumableID = resumable.ID
					resumableStatus = string(resumable.Status)
				}
				http.Error(w, dbconn.FormatBulkBlockedError(dst.Schema, count, tables, resumableID, resumableStatus), http.StatusConflict)
				return
			}
		}
		job, err = s.Store.CreateJob(job)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if body.Start {
			if err := s.Runner.StartJob(job.ID); err != nil {
				job.Status = store.JobFailed
				job.ErrorMessage = err.Error()
				_ = s.Store.UpdateJob(job)
				writeErrorJSON(w, err.Error(), http.StatusConflict)
				return
			}
			job, _ = s.Store.GetJob(job.ID)
		}
		writeJSON(w, job)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/jobs/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) > 1 && parts[1] == "start" && r.Method == http.MethodPost {
		if err := s.Runner.StartJob(id); err != nil {
			writeErrorJSON(w, err.Error(), http.StatusConflict)
			return
		}
		job, _ := s.Store.GetJob(id)
		writeJSON(w, job)
		return
	}
	if len(parts) > 1 && parts[1] == "pause" && r.Method == http.MethodPost {
		s.Runner.PauseJob()
		writeJSON(w, map[string]string{"status": "pausing"})
		return
	}
	if len(parts) > 1 && parts[1] == "cancel" && r.Method == http.MethodPost {
		s.Runner.CancelJob()
		writeJSON(w, map[string]string{"status": "cancelling"})
		return
	}
	if len(parts) > 1 && parts[1] == "insert-failures" && r.Method == http.MethodGet {
		limit := 100
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		recs, err := s.Store.ListInsertFailures(id, limit)
		if err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, recs)
		return
	}
	if r.Method == http.MethodPatch {
		job, err := s.Store.GetJob(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if job.Status != store.JobPaused && job.Status != store.JobFailed {
			http.Error(w, "job settings can only be changed while paused or failed", http.StatusConflict)
			return
		}
		var body struct {
			BatchSize       int    `json:"batch_size"`
			ParallelTables  int    `json:"parallel_tables"`
			ChunkTimeoutSec int    `json:"chunk_timeout_sec"`
			DateColumn      string `json:"date_column"`
			DateFrom        string `json:"date_from"`
			DateTo          string `json:"date_to"`
			MaxRowsPerTable int    `json:"max_rows_per_table"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		st, _ := s.Store.GetSettings()
		if body.BatchSize <= 0 {
			body.BatchSize = job.BatchSize
			if body.BatchSize <= 0 {
				body.BatchSize = st.DefaultBatchSize
			}
		}
		if body.ParallelTables <= 0 {
			body.ParallelTables = job.ParallelTables
			if body.ParallelTables <= 0 {
				body.ParallelTables = st.DefaultParallel
			}
		}
		dateColumn := body.DateColumn
		if dateColumn == "" {
			dateColumn = job.DateColumn
		}
		if _, err := migrate.ParseDateBounds(body.DateFrom, body.DateTo); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Store.UpdateJobSettings(id, body.BatchSize, body.ParallelTables, body.ChunkTimeoutSec, body.MaxRowsPerTable, dateColumn, body.DateFrom, body.DateTo); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		job, _ = s.Store.GetJob(id)
		writeJSON(w, job)
		return
	}
	if r.Method == http.MethodGet {
		job, err := s.Store.GetJob(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tasks, _ := s.Store.ListTableTasks(id)
		writeJSON(w, map[string]any{"job": job, "tasks": tasks})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := s.Store.GetSettings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Auth.SetPasswordHash(st.AdminPasswordHash)
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.NewPassword) < 8 {
		http.Error(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if !s.Auth.CheckPassword(body.CurrentPassword) {
		http.Error(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetAdminPasswordHash(hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Auth.SetPasswordHash(hash)
	s.Auth.ClearSessions()
	s.Auth.Login(w, body.NewPassword)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st, err := s.Store.GetSettings()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"schedule_cron":                  st.ScheduleCron,
			"schedule_source_id":             st.ScheduleSourceID,
			"schedule_dest_id":             st.ScheduleDestID,
			"default_batch_size":             st.DefaultBatchSize,
			"default_parallel":               st.DefaultParallel,
			"default_chunk_timeout_sec":      st.DefaultChunkTimeoutSec,
			"default_row_count_fallback_cap": st.DefaultRowCountFallbackCap,
			"default_connect_timeout_sec":    st.DefaultConnectTimeoutSec,
			"mssql_encrypt":                  st.MssqlEncrypt,
			"mssql_trust_server_cert":        st.MssqlTrustServerCert,
			"engine_enabled":                 st.EngineEnabled,
			"has_password":                   st.AdminPasswordHash != "",
		})
	case http.MethodPut:
		var body store.AppSettings
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.DefaultBatchSize <= 0 {
			body.DefaultBatchSize = 50000
		}
		if body.DefaultParallel <= 0 {
			body.DefaultParallel = 2
		}
		if body.DefaultChunkTimeoutSec <= 0 {
			body.DefaultChunkTimeoutSec = 300
		}
		if body.DefaultConnectTimeoutSec <= 0 {
			body.DefaultConnectTimeoutSec = 30
		}
		if body.ScheduleCron == "" {
			body.ScheduleCron = "0 */4 * * *"
		}
		if err := s.Store.UpdateSettings(body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.applyConnectOpts()
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) applyConnectOpts() {
	st, err := s.Store.GetSettings()
	if err != nil {
		return
	}
	dbconn.SetDefaultConnectOpts(dbconn.ConnectOptsFromSettings(
		st.DefaultConnectTimeoutSec, st.MssqlEncrypt, st.MssqlTrustServerCert,
	))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

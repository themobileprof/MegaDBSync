package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mdas/mdas/internal/auth"
	"github.com/mdas/mdas/internal/dbconn"
	"github.com/mdas/mdas/internal/jobs"
	"github.com/mdas/mdas/internal/store"
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
	protected.HandleFunc("/connections/test", s.handleTestConnection)
	protected.HandleFunc("/connections/", s.handleConnectionByID)
	protected.HandleFunc("/jobs", s.handleJobs)
	protected.HandleFunc("/jobs/", s.handleJobByID)
	protected.HandleFunc("/settings", s.handleSettings)
	protected.HandleFunc("/settings/password", s.handleChangePassword)
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Auth.HasPassword() {
		http.Error(w, "already configured", http.StatusConflict)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetAdminPasswordHash(hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Auth.SetPasswordHash(hash)
	s.Auth.Login(w, body.Password)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
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
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.Auth.HasPassword() {
		http.Error(w, "setup required", http.StatusPreconditionRequired)
		return
	}
	if !s.Auth.Login(w, body.Password) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
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
	writeJSON(w, ds)
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

	send := func() {
		ds, err := s.Store.Dashboard()
		if err != nil {
			return
		}
		b, _ := json.Marshal(ds)
		_, _ = io.WriteString(w, "event: status\ndata: ")
		_, _ = w.Write(b)
		_, _ = io.WriteString(w, "\n\n")
		flusher.Flush()
	}
	send()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	notify := s.Runner.Notify()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send()
		case <-notify:
			send()
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
		if c.Name == "" || c.Host == "" || c.Username == "" {
			http.Error(w, "name, host, and username required", http.StatusBadRequest)
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
			Type           store.JobType `json:"type"`
			SourceID       string        `json:"source_id"`
			DestID         string        `json:"dest_id"`
			BatchSize      int           `json:"batch_size"`
			ParallelTables int           `json:"parallel_tables"`
			TableFilter    string        `json:"table_filter"`
			Start          bool          `json:"start"`
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
		job := store.Job{
			Type: body.Type, SourceID: body.SourceID, DestID: body.DestID,
			BatchSize: body.BatchSize, ParallelTables: body.ParallelTables, TableFilter: body.TableFilter,
		}
		if job.Type == "" {
			job.Type = store.JobBulkFull
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
			_ = db.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if count > 0 {
				http.Error(w, "destination is not empty; bulk migration blocked", http.StatusConflict)
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
				http.Error(w, err.Error(), http.StatusConflict)
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
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		job, _ := s.Store.GetJob(id)
		writeJSON(w, job)
		return
	}
	if len(parts) > 1 && parts[1] == "cancel" && r.Method == http.MethodPost {
		s.Runner.CancelJob()
		writeJSON(w, map[string]string{"status": "cancelling"})
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
			"schedule_cron":       st.ScheduleCron,
			"schedule_source_id":  st.ScheduleSourceID,
			"schedule_dest_id":    st.ScheduleDestID,
			"default_batch_size":  st.DefaultBatchSize,
			"default_parallel":    st.DefaultParallel,
			"has_password":        st.AdminPasswordHash != "",
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
		if body.ScheduleCron == "" {
			body.ScheduleCron = "0 */4 * * *"
		}
		if err := s.Store.UpdateSettings(body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

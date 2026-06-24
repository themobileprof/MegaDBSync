package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/store"
)

type exploreRequest struct {
	ConnectionID string `json:"connection_id"`
	store.Connection
	Password    string `json:"password"`
	TableSchema string `json:"table_schema"`
	TableName   string `json:"table_name"`
	Limit       int    `json:"limit"`
}

func (s *Server) handleExploreTables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, pass, err := s.resolveExploreConn(r)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadRequest)
		return
	}
	tables, err := dbconn.ListTables(r.Context(), c, pass)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"tables": tables})
}

func (s *Server) handleExploreSample(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req exploreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	c, pass, err := s.resolveExploreConnFromReq(r.Context(), req)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TableSchema == "" || req.TableName == "" {
		writeErrorJSON(w, "table_schema and table_name required", http.StatusBadRequest)
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	sample, err := dbconn.SampleRows(r.Context(), c, pass, req.TableSchema, req.TableName, limit)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	if r.URL.Query().Get("download") == "csv" {
		csv, err := dbconn.SampleCSV(sample)
		if err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := req.TableSchema + "." + req.TableName + ".sample.csv"
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write(csv)
		return
	}
	writeJSON(w, sample)
}

func (s *Server) handleExploreSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req exploreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	c, pass, err := s.resolveExploreConnFromReq(r.Context(), req)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TableSchema == "" || req.TableName == "" {
		writeErrorJSON(w, "table_schema and table_name required", http.StatusBadRequest)
		return
	}
	cols, err := dbconn.TableSchema(r.Context(), c, pass, req.TableSchema, req.TableName)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	dl := r.URL.Query().Get("download")
	switch dl {
	case "ddl":
		ddl := dbconn.SchemaDDL(c, req.TableSchema, req.TableName, cols)
		name := req.TableSchema + "." + req.TableName + ".schema.sql"
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write([]byte(ddl))
		return
	case "json":
		b, err := dbconn.SchemaJSON(cols)
		if err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := req.TableSchema + "." + req.TableName + ".schema.json"
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write(b)
		return
	}
	writeJSON(w, map[string]any{"columns": cols})
}

type migrationReportRequest struct {
	exploreRequest
	DestConnectionID string           `json:"dest_connection_id"`
	Dest             store.Connection `json:"dest"`
	DestPassword     string           `json:"dest_password"`
}

func (s *Server) handleExploreMigrationReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req migrationReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	src, pass, err := s.resolveExploreConnFromReq(r.Context(), req.exploreRequest)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadRequest)
		return
	}
	if src.Type != store.ConnOracle {
		writeErrorJSON(w, "migration report requires an Oracle source connection", http.StatusBadRequest)
		return
	}
	owner := strings.TrimSpace(src.Schema)
	if owner == "" {
		writeErrorJSON(w, "Oracle schema (owner) is required on the source connection", http.StatusBadRequest)
		return
	}

	st, _ := s.Store.GetSettings()
	rowCountCap := st.DefaultRowCountFallbackCap

	ora, err := dbconn.OpenOracle(r.Context(), src, pass)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer ora.Close()

	var destConn *store.Connection
	var destPass string
	if req.DestConnectionID != "" {
		c, err := s.Store.GetConnection(req.DestConnectionID)
		if err != nil {
			writeErrorJSON(w, "invalid destination connection", http.StatusBadRequest)
			return
		}
		destConn = &c
		destPass, err = s.Store.ConnectionPassword(req.DestConnectionID)
		if err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.DestPassword != "" {
			destPass = req.DestPassword
		}
	} else if req.Dest.Host != "" {
		c := req.Dest
		if c.Type == "" {
			c.Type = store.ConnMSSQL
		}
		destConn = &c
		destPass = req.DestPassword
	}

	if destConn != nil && destConn.Type != store.ConnMSSQL {
		writeErrorJSON(w, "destination must be SQL Server", http.StatusBadRequest)
		return
	}

	report, err := dbconn.BuildOracleMigrationReport(r.Context(), ora, owner, rowCountCap, destConn, destPass)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, report)
}

type tableDependenciesRequest struct {
	ConnectionID string   `json:"connection_id"`
	Tables       []string `json:"tables"`
}

func (s *Server) handleExploreTableDependencies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tableDependenciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ConnectionID == "" {
		writeErrorJSON(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	c, err := s.Store.GetConnection(req.ConnectionID)
	if err != nil {
		writeErrorJSON(w, "invalid connection", http.StatusBadRequest)
		return
	}
	if c.Type != store.ConnOracle {
		writeErrorJSON(w, "table dependencies require an Oracle source connection", http.StatusBadRequest)
		return
	}
	pass, err := s.Store.ConnectionPassword(req.ConnectionID)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := dbconn.TableFKDependencies(r.Context(), c, pass, req.Tables)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleExploreDiscoverTableRelationships(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tableDependenciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ConnectionID == "" {
		writeErrorJSON(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	if len(req.Tables) == 0 {
		writeErrorJSON(w, "select at least one table", http.StatusBadRequest)
		return
	}
	c, err := s.Store.GetConnection(req.ConnectionID)
	if err != nil {
		writeErrorJSON(w, "invalid connection", http.StatusBadRequest)
		return
	}
	pass, err := s.Store.ConnectionPassword(req.ConnectionID)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := dbconn.DiscoverTableRelationships(r.Context(), c, pass, req.Tables)
	if err != nil {
		writeErrorJSON(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}

func (s *Server) resolveExploreConn(r *http.Request) (store.Connection, string, error) {
	var req exploreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return store.Connection{}, "", fmt.Errorf("bad request")
	}
	return s.resolveExploreConnFromReq(r.Context(), req)
}

func (s *Server) resolveExploreConnFromReq(ctx context.Context, req exploreRequest) (store.Connection, string, error) {
	if req.ConnectionID != "" {
		c, err := s.Store.GetConnection(req.ConnectionID)
		if err != nil {
			return store.Connection{}, "", err
		}
		pass, err := s.Store.ConnectionPassword(req.ConnectionID)
		if err != nil {
			return store.Connection{}, "", err
		}
		if req.Password != "" {
			pass = req.Password
		}
		return c, pass, nil
	}
	c := req.Connection
	if c.Host == "" {
		return store.Connection{}, "", fmt.Errorf("host is required (or choose a saved connection)")
	}
	if c.Type != store.ConnOracle && c.Type != store.ConnMSSQL {
		return store.Connection{}, "", fmt.Errorf("type must be oracle or mssql")
	}
	if c.Type == store.ConnMSSQL && c.WindowsAuth {
		// ok
	} else if c.Username == "" {
		return store.Connection{}, "", fmt.Errorf("username is required")
	}
	return c, req.Password, nil
}

func (s *Server) handleEngine(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/engine/")
	switch {
	case path == "start" && r.Method == http.MethodPost:
		if err := s.Store.SetEngineEnabled(true); err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = s.Store.LogEvent("", "info", "Migration engine started from web console")
		writeJSON(w, map[string]any{"engine_enabled": true})
	case path == "stop" && r.Method == http.MethodPost:
		s.Runner.PauseJob()
		if err := s.Store.SetEngineEnabled(false); err != nil {
			writeErrorJSON(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = s.Store.LogEvent("", "info", "Migration engine stopped from web console")
		writeJSON(w, map[string]any{"engine_enabled": false})
	default:
		writeErrorJSON(w, "not found", http.StatusNotFound)
	}
}

func writeErrorJSON(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message, "message": message})
}

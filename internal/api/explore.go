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
		s.Runner.CancelJob()
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

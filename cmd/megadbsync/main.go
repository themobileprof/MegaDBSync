//go:build windows

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/themobileprof/megadbsync/internal/api"
	"github.com/themobileprof/megadbsync/internal/auth"
	"github.com/themobileprof/megadbsync/internal/dbconn"
	"github.com/themobileprof/megadbsync/internal/jobs"
	"github.com/themobileprof/megadbsync/internal/platform"
	"github.com/themobileprof/megadbsync/internal/store"
	"github.com/themobileprof/megadbsync/web"

	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dataDir := flag.String("data", platform.DefaultDataDir(), "Directory for app state and credentials")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	settings, err := st.GetSettings()
	if err != nil {
		log.Fatalf("settings: %v", err)
	}
	applyConnectOpts(settings)
	applySetupPending(st, *dataDir, &settings)

	authMgr := auth.NewManager()
	authMgr.SetPasswordHash(settings.AdminPasswordHash)

	runner := jobs.NewRunner(st)
	scheduler := jobs.NewScheduler(st, runner)
	scheduler.Start()
	defer scheduler.Stop()

	apiServer := &api.Server{
		Store:     st,
		Auth:      authMgr,
		Runner:    runner,
		Scheduler: scheduler,
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiServer.Handler())
	mux.Handle("/", web.Handler())

	log.Printf("MegaDBSync listening on http://%s", *addr)
	log.Printf("State directory: %s", *dataDir)
	if settings.EngineEnabled {
		log.Printf("Migration engine is running (scheduled incremental sync can run).")
	} else if settings.AutoStartEngine {
		log.Printf("Migration engine is stopped. Enable auto-start in Settings or start from the dashboard.")
	} else {
		log.Printf("Open the URL above in your browser. The migration engine is stopped until you start it from the dashboard.")
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      0, // SSE dashboard keeps connections open
		IdleTimeout:       2 * time.Minute,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func applyConnectOpts(st store.AppSettings) {
	dbconn.SetDefaultConnectOpts(dbconn.ConnectOptsFromSettings(
		st.DefaultConnectTimeoutSec, st.MssqlEncrypt, st.MssqlTrustServerCert,
	))
}

func applySetupPending(st *store.Store, dataDir string, settings *store.AppSettings) {
	pending, ok := platform.ReadSetupPending(dataDir)
	if !ok {
		if settings.AutoStartEngine && !settings.EngineEnabled {
			if err := st.SetEngineEnabled(true); err == nil {
				settings.EngineEnabled = true
				log.Printf("Migration engine auto-started (Settings → auto-start engine).")
			}
		}
		return
	}
	if pending.AutoStartEngine {
		_ = st.SetAutoStartEngine(true)
		_ = st.SetEngineEnabled(true)
		settings.AutoStartEngine = true
		settings.EngineEnabled = true
		log.Printf("Migration engine auto-started (setup installer).")
	}
	platform.RemoveSetupPending(dataDir)
}

func init() {
	fmt.Println("MegaDBSync — Oracle to SQL Server migration & sync (Windows)")
}

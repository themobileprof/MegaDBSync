//go:build windows

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/themobileprof/megadbsync/internal/api"
	"github.com/themobileprof/megadbsync/internal/auth"
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

	authMgr := auth.NewManager()
	authMgr.SetPasswordHash(settings.AdminPasswordHash)

	runner := jobs.NewRunner(st)
	scheduler := jobs.NewScheduler(st, runner)
	scheduler.Start()
	defer scheduler.Stop()

	srv := &api.Server{
		Store:     st,
		Auth:      authMgr,
		Runner:    runner,
		Scheduler: scheduler,
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Handler())
	mux.Handle("/", web.Handler())

	log.Printf("MegaDBSync listening on http://%s", *addr)
	log.Printf("State directory: %s", *dataDir)
	log.Printf("Open the URL above in your browser. The migration engine is stopped until you start it from the dashboard.")
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func init() {
	fmt.Println("MegaDBSync — Oracle to SQL Server migration & sync (Windows)")
}

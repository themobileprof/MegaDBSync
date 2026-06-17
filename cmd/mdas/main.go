//go:build windows

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/mdas/mdas/internal/api"
	"github.com/mdas/mdas/internal/auth"
	"github.com/mdas/mdas/internal/jobs"
	"github.com/mdas/mdas/internal/platform"
	"github.com/mdas/mdas/internal/store"
	"github.com/mdas/mdas/web"

	_ "github.com/sijms/go-ora/v2"
	_ "github.com/microsoft/go-mssqldb"
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
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/index.html", http.StatusTemporaryRedirect)
			return
		}
		web.Handler().ServeHTTP(w, r)
	}))

	log.Printf("MDAS listening on http://%s", *addr)
	log.Printf("State directory: %s", *dataDir)
	log.Printf("Close the browser anytime — background jobs keep running until you stop this process.")
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func init() {
	fmt.Println("MDAS — Migration & Daily Sync (Windows)")
}

// Command server is the runnable entry point for the shield-tunnel segment
// quality-closure service. It wires the five business components over SQLite
// persistence, hosts the embedded frontend dashboard, and serves the
// documented JSON API with restart recovery.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shieldtunnel/catalog"
	"shieldtunnel/httpapi"
	"shieldtunnel/material"
	"shieldtunnel/process"
	"shieldtunnel/ring"
	"shieldtunnel/store"
	"shieldtunnel/verdict"
)

func main() {
	addr := envOr("SHIELD_ADDR", ":8080")
	dbPath := envOr("SHIELD_DB", "shieldtunnel.db")

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cat := catalog.NewStatic()
	rings := ring.NewAggregate(cat, db)
	materials := material.NewManager(db)
	procs := process.NewRecorder(db)
	verdicts := verdict.NewArbiter(db)

	srv := httpapi.New(cat, rings, materials, procs, verdicts, db)

	s := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("shield-tunnel service listening on %s (db=%s)", addr, dbPath)
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	_ = s.Close()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

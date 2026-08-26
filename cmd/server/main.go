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
	// Grace period given to in-flight requests during a rolling shutdown. A
	// whole-ring lock that has already been received may still be uploading
	// its body when SIGTERM arrives; Shutdown drains it within this window
	// instead of severing the connection mid-write.
	shutdownTimeout := envDuration("SHIELD_SHUTDOWN_TIMEOUT", 30*time.Second)

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

	// ctx is already cancelled by the signal, so it cannot bound the drain.
	// Use a fresh deadline so already-received quality submissions (a lock
	// still uploading its body, an evidence or terminal-decision in flight)
	// finish their transaction and write a response before the process exits.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown incomplete within %s: %v", shutdownTimeout, err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

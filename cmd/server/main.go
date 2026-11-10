// Command server runs the appointment scheduling API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/swizzley/future-scheduler/internal/httpapi"
	"github.com/swizzley/future-scheduler/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := getenv("DATABASE_URL", "postgres://scheduler:scheduler@localhost:5432/scheduler?sslmode=disable")
	addr := getenv("ADDR", ":8080")
	seedFile := os.Getenv("SEED_FILE") // e.g. appointments.json; empty skips seeding

	db, err := connectWithRetry(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if seedFile != "" {
		n, err := db.Seed(ctx, seedFile, func(id int64, reason string) {
			slog.Warn("seed row breaks current booking rules; imported anyway because it is an existing record", "seed_id", id, "reason", reason)
		})
		if err != nil {
			return err
		}
		slog.Info("seed loaded", "file", seedFile, "inserted", n)
	}

	api := &httpapi.Server{Store: db, Now: time.Now, Ping: db.Ping}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// connectWithRetry waits for Postgres, which may still be starting under docker compose.
func connectWithRetry(ctx context.Context, url string) (*store.Postgres, error) {
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		db, err := store.NewPostgres(ctx, url)
		if err == nil {
			return db, nil
		}
		lastErr = err
		slog.Info("waiting for database", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, lastErr
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Command tkc-server runs the Trusted Kids Communications backend.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/frbayart/tkc/server/internal/httpapi"
	"github.com/frbayart/tkc/server/internal/identity"
	"github.com/frbayart/tkc/server/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbURL := flag.String("database-url", os.Getenv("TKC_DATABASE_URL"), "PostgreSQL URL (default $TKC_DATABASE_URL)")
	migrate := flag.Bool("migrate", true, "apply pending database migrations at startup")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*addr, *dbURL, *migrate, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dbURL string, migrate bool, logger *slog.Logger) error {
	if dbURL == "" {
		return errors.New("database URL required (-database-url or TKC_DATABASE_URL)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if migrate {
		applied, err := store.Migrate(ctx, pool)
		if err != nil {
			return err
		}
		logger.Info("migrations applied", "versions", applied)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(&httpapi.API{Identity: identity.New(pool), Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

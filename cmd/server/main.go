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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saadrupai/stripe-usage-billing/internal/api"
	"github.com/saadrupai/stripe-usage-billing/internal/billing"
	"github.com/saadrupai/stripe-usage-billing/internal/store"
	"github.com/saadrupai/stripe-usage-billing/internal/stripex"
	"github.com/saadrupai/stripe-usage-billing/internal/webhook"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, closeStore := openStore(ctx, log)
	defer closeStore()

	reporter := &billing.Reporter{
		Store:    st,
		Stripe:   stripex.NewClient(mustEnv("STRIPE_SECRET_KEY")),
		Interval: envDuration("REPORT_INTERVAL", 30*time.Second),
		Log:      log,
	}
	go reporter.Run(ctx)

	srv := &api.Server{Store: st, APIKey: mustEnv("API_KEY"), Log: log}
	wh := &webhook.Handler{Store: st, Secret: mustEnv("STRIPE_WEBHOOK_SECRET"), Log: log}

	httpSrv := &http.Server{
		Addr:              ":" + envOr("PORT", "8080"),
		Handler:           srv.Routes(wh),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}

func openStore(ctx context.Context, log *slog.Logger) (store.Store, func()) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Warn("DATABASE_URL not set; using in-memory store (data is lost on restart)")
		return store.NewMemory(), func() {}
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Error("connect postgres", "err", err)
		os.Exit(1)
	}
	return store.NewPostgres(pool), pool.Close
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing required env var", "key", k)
		os.Exit(1)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}

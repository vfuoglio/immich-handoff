package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/config"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	base := strings.TrimRight(env("IMMICH_BASE_URL", "http://immich-server:2283"), "/")
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("WEBHOOK_SECRET is required")
	}
	sweepH, _ := strconv.ParseFloat(env("SWEEP_INTERVAL_HOURS", "24"), 64)

	services, err := config.Load(os.Getenv, func(key string) (*immich.API, error) {
		return immich.New(base, immich.Auth{APIKey: key}, nil)
	})
	if err != nil {
		return err
	}
	s := &syncer.Syncer{
		Services: services, Log: log,
		Cfg: syncer.Config{
			DeleteForce: env("DELETE_FORCE", "false") == "true", DryRun: env("DRY_RUN", "false") == "true",
			NotifyURL: os.Getenv("NOTIFY_URL"),
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	probe := services[0].Receivers[0].API
	major, minor, err := probe.ServerVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("connected to Immich", "major", major, "minor", minor, "services", len(services))
	if major < 3 || (major == 3 && minor < 2) {
		log.Warn("Immich < 3.2: the tag workflow trigger is not available; only the startup and daily passes will hand photos over")
	}

	w := &web.Server{S: s, BaseURL: base, WebhookHeader: env("WEBHOOK_HEADER", "X-Webhook-Secret"),
		WebhookSecret: secret, Log: log, BackgroundCtx: ctx}
	if err := w.Init(ctx); err != nil {
		return err
	}

	hook := syncer.Webhook{URL: os.Getenv("PUBLIC_WEBHOOK_URL"), Header: w.WebhookHeader, Secret: secret}
	go s.ProvisionRetry(ctx, hook) // background: Immich may still be loading its plugins

	// Startup catch-up and a slow daily sweep (also expires the inbox tag). This is a safety net, not polling.
	go func() {
		reason := "startup"
		for {
			s.Provision(ctx, hook) // heals a deleted or edited workflow; no-op when already correct
			s.Run(ctx, reason, true)
			reason = "daily sweep"
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(sweepH * float64(time.Hour))):
			}
		}
	}()

	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: w.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(sh)
	}()
	log.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

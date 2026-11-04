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
	destKey, secret := os.Getenv("IMMICH_DEST_API_KEY"), os.Getenv("WEBHOOK_SECRET")
	var srcKeys []string
	for _, k := range strings.Split(os.Getenv("IMMICH_SOURCE_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			srcKeys = append(srcKeys, k)
		}
	}
	if destKey == "" || secret == "" || len(srcKeys) == 0 {
		return errors.New("IMMICH_DEST_API_KEY, IMMICH_SOURCE_API_KEYS and WEBHOOK_SECRET are required")
	}
	days, _ := strconv.Atoi(env("INBOX_EXPIRY_DAYS", "30"))
	sweepH, _ := strconv.ParseFloat(env("SWEEP_INTERVAL_HOURS", "24"), 64)

	dest, err := immich.New(base, immich.Auth{APIKey: destKey}, nil)
	if err != nil {
		return err
	}
	s := &syncer.Syncer{
		Dest: dest, Log: log,
		Cfg: syncer.Config{
			ShareTag: env("SHARE_TAG", "share"), InboxTag: env("INBOX_TAG", "inbox"), RefusedTag: env("REFUSED_TAG", "share-duplicate"), ExpiryDays: days,
			DeleteForce: env("DELETE_FORCE", "false") == "true", DryRun: env("DRY_RUN", "false") == "true",
			NotifyURL: os.Getenv("NOTIFY_URL"),
		},
	}
	for _, k := range srcKeys {
		a, err := immich.New(base, immich.Auth{APIKey: k}, nil)
		if err != nil {
			return err
		}
		s.Sources = append(s.Sources, a)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	major, minor, err := dest.ServerVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("connected to Immich", "major", major, "minor", minor)
	if major < 3 || (major == 3 && minor < 2) {
		log.Warn("Immich < 3.2: the tag workflow trigger is not available; only the daily sweep will import")
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

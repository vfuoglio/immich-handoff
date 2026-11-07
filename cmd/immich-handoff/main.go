package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/config"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	// Lets a release check what it built without needing Immich: `immich-handoff --version`.
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Println(version)
		return
	}
	// For the container's HEALTHCHECK, which has no shell or curl: `immich-handoff --healthcheck`.
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		os.Exit(runHealthcheck(env("LISTEN_ADDR", ":8080")))
	}
	// Backing up and restoring the data file: `immich-handoff backup [--wait|--list]`, `immich-handoff restore <file>`.
	if len(os.Args) >= 2 && (os.Args[1] == "backup" || os.Args[1] == "restore") {
		os.Exit(runBackupCommand(context.Background(), os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
	}
	// A check of the setup: `immich-handoff doctor` (run it inside the container; it reads the same settings).
	if len(os.Args) == 2 && os.Args[1] == "doctor" {
		os.Exit(runDoctor(context.Background(), os.Stdout))
	}
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
	if len(secret) < 16 {
		return errors.New("WEBHOOK_SECRET is required and must be at least 16 characters (openssl rand -hex 24 makes a good one)")
	}
	sweepH, _ := strconv.ParseFloat(env("SWEEP_INTERVAL_HOURS", "24"), 64)
	tidyMin, _ := strconv.ParseFloat(env("CLEANUP_INTERVAL_MINUTES", "15"), 64)
	albumMin, _ := strconv.ParseFloat(env("ALBUM_CHECK_MINUTES", "15"), 64)

	connect := func(key string) (*immich.API, error) { return immich.New(base, immich.Auth{APIKey: key}, nil) }
	services, err := config.Load(os.Getenv, connect)
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

	probe, err := connect("") // the version endpoint needs no key
	if err != nil {
		return err
	}
	major, minor, err := probe.ServerVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("connected to Immich", "major", major, "minor", minor, "services", len(services), "version", version)
	if major < 3 || (major == 3 && minor < 2) {
		log.Warn("Immich < 3.2: the tag workflow trigger is not available; only the startup and daily passes will hand photos over")
	}

	webhookHeader := env("WEBHOOK_HEADER", "X-Webhook-Secret")
	hook := syncer.Webhook{URL: os.Getenv("PUBLIC_WEBHOOK_URL"), Header: webhookHeader, Secret: secret}

	for _, a := range syncer.ResolveUsers(ctx, services) {
		log.Warn("an API key from the configuration is not accepted by Immich", "account", a.Name)
	}

	var backups *backup.Manager // set when there is a store to back up

	// Self-service joining needs a place to keep keys, protected by a key of its own.
	defaults := config.DefaultsFrom(os.Getenv)
	h := &hub.Hub{Static: services, Connect: connect, Hook: hook, Log: log,
		Options: hub.Options{InboxTag: defaults.InboxTag, RefusedTag: defaults.RefusedTag, SentTag: defaults.SentTag, ExpiryDays: defaults.ExpiryDays},
		Wake:    func(reason string) { go s.Run(ctx, reason, false) },
		Retry:   func() { s.ProvisionRetry(ctx, hook) }}
	if storeKey, err := readStoreKey(); err != nil {
		return err
	} else if storeKey == "" {
		log.Info("HANDOFF_STORE_KEY is not set: people cannot join from the web page (keys from the configuration still work)")
	} else {
		v, err := vault.New(storeKey)
		if err != nil {
			return err
		}
		dir := env("DATA_DIR", "/data")
		// marks the data folder as in use, so a restore can refuse to replace the file under a running app
		unlock, err := backup.Lock(dir)
		if err != nil {
			return fmt.Errorf("%s: %w (is another copy of the app using the same data?)", dir, err)
		}
		defer unlock()
		st, err := store.Open(dir+"/"+backup.DataFile, v)
		if err != nil {
			return err
		}
		defer st.Close()
		h.Store = st
		backups = &backup.Manager{Dir: dir + "/backups", Source: st, Idle: s, Log: log, Tell: s.Tell,
			Event: func(kind, text string) {
				if err := st.AddEvent(context.WithoutCancel(ctx), store.Event{Kind: kind, Text: text}); err != nil {
					log.Warn("could not write the activity log", "err", err)
				}
			}}
		log.Info("self-service joining is on", "data", dir)
	}
	if err := h.Reload(ctx); err != nil {
		return err
	}
	s.Provider = h
	s.Usage = h // what senders hand over is kept in the data file, for services with a daily limit

	w := &web.Server{S: s, Hub: h, BaseURL: base, WebhookHeader: webhookHeader,
		WebhookSecret: secret, Log: log, BackgroundCtx: ctx, WebhookURL: os.Getenv("PUBLIC_WEBHOOK_URL"), Backups: backups,
		PublicImmichURL: os.Getenv("PUBLIC_IMMICH_URL"), Version: version}

	go s.ProvisionRetry(ctx, hook) // background: Immich may still be loading its plugins

	// Startup catch-up and a slow daily sweep (also expires the inbox tag). This is a safety net, not polling.
	go func() {
		reason := "startup"
		for {
			h.CheckKeys(ctx)       // finds keys deleted in Immich even when nothing was tagged
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

	// Immich sends no event when a photo is added to an album, so filed photos lose their inbox tag on a short timer.
	if tidyMin > 0 {
		go func() {
			t := time.NewTicker(time.Duration(tidyMin * float64(time.Minute)))
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.Tidy(ctx)
				}
			}
		}()
	}

	// Immich sends no event when a photo is added to an album either, so albums that senders linked to a
	// service are looked at on a timer too. The timer does nothing when no album is linked.
	if albumMin > 0 {
		go func() {
			t := time.NewTicker(time.Duration(albumMin * float64(time.Minute)))
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if s.HasWatchers() {
						s.Run(ctx, "album check", false)
					}
				}
			}
		}()
	}

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

// readStoreKey reads the store key from HANDOFF_STORE_KEY_FILE (a Docker secret) or HANDOFF_STORE_KEY.
func readStoreKey() (string, error) {
	if path := os.Getenv("HANDOFF_STORE_KEY_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv("HANDOFF_STORE_KEY")), nil
}

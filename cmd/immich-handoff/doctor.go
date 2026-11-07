package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/config"
	"github.com/vfuoglio/immich-handoff/internal/doctor"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// runDoctor is `immich-handoff doctor`: it reads the same settings the app starts with, checks them against
// Immich, and prints what is wrong and how to fix it. It changes nothing and returns the exit status:
// 0 when nothing failed (warnings do not count), 1 otherwise.
func runDoctor(ctx context.Context, out io.Writer) int {
	base := strings.TrimRight(env("IMMICH_BASE_URL", "http://immich-server:2283"), "/")
	connect := func(key string) (*immich.API, error) { return immich.New(base, immich.Auth{APIKey: key}, nil) }
	probe, err := connect("")
	if err != nil {
		fmt.Fprintln(out, "FAIL  IMMICH_BASE_URL is not usable:", err)
		return 1
	}
	in := doctor.Input{
		Env: os.Getenv, BaseURL: base, Probe: probe,
		Hook: syncer.Webhook{URL: os.Getenv("PUBLIC_WEBHOOK_URL"), Header: env("WEBHOOK_HEADER", "X-Webhook-Secret"), Secret: os.Getenv("WEBHOOK_SECRET")},
	}
	in.Services, in.StoreNote, in.LoadErr = loadForDoctor(ctx, connect)
	fails, _ := doctor.Run(ctx, in, out)
	if fails > 0 {
		return 1
	}
	return 0
}

// loadForDoctor builds the services the way the app does: from the configuration, plus the people and
// services in the store when self-service joining is on.
func loadForDoctor(ctx context.Context, connect func(string) (*immich.API, error)) ([]*syncer.Service, string, error) {
	static, err := config.Load(os.Getenv, connect)
	if err != nil {
		return nil, "", err
	}
	defaults := config.DefaultsFrom(os.Getenv)
	h := &hub.Hub{Static: static, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options: hub.Options{InboxTag: defaults.InboxTag, RefusedTag: defaults.RefusedTag, SentTag: defaults.SentTag, ExpiryDays: defaults.ExpiryDays}}
	note := ""
	if key, err := readStoreKey(); err != nil {
		return nil, "", err
	} else if key != "" {
		v, err := vault.New(key)
		if err != nil {
			return nil, "", err
		}
		dir := env("DATA_DIR", "/data")
		st, err := store.Open(dir+"/handoff.db", v)
		if err != nil {
			return nil, "", fmt.Errorf("the store in %s: %w", dir, err)
		}
		defer st.Close()
		h.Store = st
		note = "the store in " + dir + " opens with HANDOFF_STORE_KEY"
	}
	if err := h.Reload(ctx); err != nil {
		return nil, "", err
	}
	return h.Services(), note, nil
}

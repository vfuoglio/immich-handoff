package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// Exit statuses of `backup`, so a script can tell them apart.
const (
	exitOK     = 0   // queued (or, with --wait, made)
	exitFailed = 1   // the backup could not be asked for or failed
	exitForced = 2   // with --wait: made, but without a quiet moment (see the log); the file is there
	exitStop   = 130 // with --wait: interrupted; the backup is still queued in the app
)

func backupURL(listen string) string {
	return strings.TrimSuffix(healthURL(listen), "/healthz") + "/backup"
}

// runBackupCommand is `immich-handoff backup ...` and `immich-handoff restore ...`.
func runBackupCommand(ctx context.Context, cmd string, args []string, out, errOut io.Writer) int {
	if cmd == "restore" {
		return runRestore(ctx, args, out, errOut)
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(errOut)
	wait := fs.Bool("wait", false, "wait until the backup is made, showing progress; the exit status says how it went (0 made, 2 made without a quiet moment, 1 failed)")
	list := fs.Bool("list", false, "list the backups in the data folder")
	if err := fs.Parse(args); err != nil {
		return exitFailed
	}
	dir := env("DATA_DIR", "/data")
	if *list {
		return listBackups(ctx, dir, out, errOut)
	}
	listen := env("LISTEN_ADDR", ":8080")
	reply, err := askApp(ctx, http.MethodPost, backupURL(listen))
	switch {
	case err == nil:
		return followBackup(ctx, reply, listen, *wait, out, errOut)
	case errors.Is(err, errAppDown):
		return backupDirect(ctx, dir, out, errOut)
	default:
		fmt.Fprintln(errOut, "backup failed:", err)
		return exitFailed
	}
}

// reply is what the app says about a backup.
type reply struct {
	backup.Job
	Already bool `json:"already"`
}

var errAppDown = errors.New("the app is not answering")

// askApp calls the running app with the shared webhook secret. errAppDown means nothing answered there.
func askApp(ctx context.Context, method, url string) (*reply, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(env("WEBHOOK_HEADER", "X-Webhook-Secret"), os.Getenv("WEBHOOK_SECRET"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", errAppDown, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	switch res.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		var r reply
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		return &r, nil
	case http.StatusUnauthorized:
		return nil, errors.New("the app did not accept WEBHOOK_SECRET: run this inside the app's container, or with the same settings")
	default:
		return nil, fmt.Errorf("the app answered %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
}

func followBackup(ctx context.Context, r *reply, listen string, wait bool, out, errOut io.Writer) int {
	if r.Already {
		fmt.Fprintf(out, "A backup is already queued (%s). It will be at %s\n", r.State, r.Path)
	} else {
		fmt.Fprintf(out, "Backup queued. It starts at the next quiet moment between hand-overs, and is made anyway after 4 hours.\nThe file will be %s (a forced one gets \"-forced\" in its name). The app's log says when it is ready.\n", r.Path)
	}
	if !wait {
		fmt.Fprintln(out, "Not waiting. Use --wait to follow it, or 'backup --list' to see the files.")
		return exitOK
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := time.Now()
	last, lastSaid := r.State, time.Now()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(errOut, "Stopped waiting. The backup is still queued in the app and will be made.")
			return exitStop
		case <-time.After(pollEvery()):
		}
		cur, err := askApp(ctx, http.MethodGet, backupURL(listen)+"/"+r.ID)
		if err != nil {
			fmt.Fprintln(errOut, "lost touch with the app:", err)
			return exitFailed
		}
		switch cur.State {
		case backup.Done:
			if cur.Forced {
				fmt.Fprintf(out, "WARNING: the backup was made without a quiet moment, because hand-overs kept running. It is a consistent copy but may hold a hand-over caught halfway.\n")
			}
			fmt.Fprintf(out, "Backup ready: %s (%d bytes)\n", cur.Path, cur.Size)
			if cur.Forced {
				return exitForced
			}
			return exitOK
		case backup.Failed:
			fmt.Fprintln(errOut, "Backup failed:", cur.Err)
			return exitFailed
		}
		if cur.State != last || time.Since(lastSaid) >= 30*time.Second {
			fmt.Fprintf(out, "Backup %s, %s so far...\n", cur.State, time.Since(started).Round(time.Second))
			last, lastSaid = cur.State, time.Now()
		}
	}
}

// pollEvery is how often --wait asks the app how it is going (a variable so tests are quick).
var pollEvery = func() time.Duration { return 2 * time.Second }

// backupDirect is for when the app is not running: nothing is being handed over, so it copies at once.
func backupDirect(ctx context.Context, dir string, out, errOut io.Writer) int {
	v, err := storeVault()
	if err != nil {
		fmt.Fprintln(errOut, "backup failed:", err)
		return exitFailed
	}
	release, err := backup.Lock(dir)
	if err != nil {
		fmt.Fprintf(errOut, "backup failed: the app is using %s but did not answer at %s: check LISTEN_ADDR and WEBHOOK_SECRET\n", dir, backupURL(env("LISTEN_ADDR", ":8080")))
		return exitFailed
	}
	defer release()
	st, err := store.Open(dir+"/"+backup.DataFile, v)
	if err != nil {
		fmt.Fprintln(errOut, "backup failed:", err)
		return exitFailed
	}
	defer st.Close()
	job, err := backup.Direct(ctx, st, dir+"/backups", time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "backup failed:", err)
		return exitFailed
	}
	fmt.Fprintf(out, "The app is not running, so the backup was made at once: %s (%d bytes)\n", job.Path, job.Size)
	return exitOK
}

func storeVault() (*vault.Vault, error) {
	key, err := readStoreKey()
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("HANDOFF_STORE_KEY is not set: without it nothing is stored, so there is nothing to back up")
	}
	return vault.New(key)
}

func listBackups(ctx context.Context, dir string, out, errOut io.Writer) int {
	files, err := backup.List(dir + "/backups")
	if err != nil {
		fmt.Fprintln(errOut, "cannot read the backups:", err)
		return exitFailed
	}
	if r, err := askApp(ctx, http.MethodGet, backupURL(env("LISTEN_ADDR", ":8080"))); err == nil && (r.State == backup.Queued || r.State == backup.Running) {
		fmt.Fprintf(out, "In progress: %s (%s)\n", r.Path, r.State)
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "No backups yet in", dir+"/backups")
		return exitOK
	}
	for _, f := range files {
		note := ""
		if f.Forced {
			note = "  (forced: made without a quiet moment)"
		}
		fmt.Fprintf(out, "%s  %10d bytes  %s%s\n", f.Name, f.Size, f.When.Local().Format("2006-01-02 15:04"), note)
	}
	return exitOK
}

func runRestore(ctx context.Context, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(errOut)
	force := fs.Bool("force", false, "replace existing data (the old file is kept as handoff.db.before-restore)")
	if err := fs.Parse(args); err != nil {
		return exitFailed
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(errOut, "usage: immich-handoff restore [--force] <backup file>")
		return exitFailed
	}
	v, err := storeVault()
	if err != nil {
		fmt.Fprintln(errOut, "restore failed:", err)
		return exitFailed
	}
	dir := env("DATA_DIR", "/data")
	if err := backup.Restore(ctx, fs.Arg(0), dir, v, *force); err != nil {
		fmt.Fprintln(errOut, "restore failed:", err)
		return exitFailed
	}
	fmt.Fprintf(out, "Restored %s into %s. Start the app.\n", fs.Arg(0), dir)
	return exitOK
}

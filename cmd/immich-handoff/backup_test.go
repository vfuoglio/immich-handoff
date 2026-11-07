package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const testKey = "0123456789abcdef0123"

// fakeApp answers like the running app. states are what successive status calls report.
func fakeApp(t *testing.T, states []backup.State, forced bool, already bool) (*httptest.Server, *atomic.Int32) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	job := func(s backup.State) backup.Job {
		j := backup.Job{ID: "20261106-120000", State: s, Path: "/data/backups/handoff-20261106-120000.db", Forced: forced, Size: 4096}
		if s == backup.Failed {
			j.Err = "disk full"
		}
		return j
	}
	mux.HandleFunc("POST /backup", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Webhook-Secret") != "s3cret" {
			http.Error(w, "bad", 401)
			return
		}
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{"id": "20261106-120000", "state": "queued", "path": job(backup.Queued).Path, "already": already})
	})
	mux.HandleFunc("GET /backup/{id}", func(w http.ResponseWriter, r *http.Request) {
		i := int(polls.Add(1)) - 1
		if i >= len(states) {
			i = len(states) - 1
		}
		json.NewEncoder(w).Encode(job(states[i]))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("LISTEN_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("WEBHOOK_SECRET", "s3cret")
	pollEvery = func() time.Duration { return time.Millisecond }
	t.Cleanup(func() { pollEvery = func() time.Duration { return 2 * time.Second } })
	return srv, &polls
}

func runCmd(t *testing.T, cmd string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runBackupCommand(context.Background(), cmd, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestBackupQueuesAndExitsAtOnceByDefault(t *testing.T) {
	_, polls := fakeApp(t, []backup.State{backup.Queued}, false, false)
	code, out, _ := runCmd(t, "backup")
	if code != 0 || !strings.Contains(out, "Backup queued") || !strings.Contains(out, "handoff-20261106-120000.db") || !strings.Contains(out, "4 hours") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if polls.Load() != 0 {
		t.Error("without --wait the command must not wait or poll")
	}
	_, out2, _ := func() (int, string, string) { fakeApp(t, nil, false, true); return runCmd(t, "backup") }()
	if !strings.Contains(out2, "already queued") {
		t.Errorf("a second request: %s", out2)
	}
}

func TestBackupWaitShowsProgressAndTheExitStatusSaysHowItWent(t *testing.T) {
	fakeApp(t, []backup.State{backup.Queued, backup.Running, backup.Done}, false, false)
	code, out, _ := runCmd(t, "backup", "--wait")
	if code != 0 || !strings.Contains(out, "Backup queued") || !strings.Contains(out, "Backup running") || !strings.Contains(out, "Backup ready: /data/backups/handoff-20261106-120000.db (4096 bytes)") {
		t.Errorf("made: exit %d\n%s", code, out)
	}

	fakeApp(t, []backup.State{backup.Done}, true, false)
	code, out, _ = runCmd(t, "backup", "--wait")
	if code != 2 || !strings.Contains(out, "WARNING") || !strings.Contains(out, "Backup ready") {
		t.Errorf("forced: exit %d\n%s", code, out)
	}

	fakeApp(t, []backup.State{backup.Queued, backup.Failed}, false, false)
	code, _, errOut := runCmd(t, "backup", "--wait")
	if code != 1 || !strings.Contains(errOut, "disk full") {
		t.Errorf("failed: exit %d\n%s", code, errOut)
	}

	srv, _ := fakeApp(t, []backup.State{backup.Queued}, false, false)
	t.Setenv("WEBHOOK_SECRET", "wrong")
	code, _, errOut = runCmd(t, "backup")
	if code != 1 || !strings.Contains(errOut, "WEBHOOK_SECRET") {
		t.Errorf("wrong secret: exit %d\n%s", code, errOut)
	}
	_ = srv
}

func TestBackupWaitCanBeInterruptedAndTheBackupStaysQueued(t *testing.T) {
	fakeApp(t, []backup.State{backup.Queued}, false, false)
	pollEvery = func() time.Duration { return 20 * time.Millisecond }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(60 * time.Millisecond); cancel() }()
	var out, errOut bytes.Buffer
	// the command sets up its own signal handling; a cancelled parent context is the same as Ctrl-C
	code := runBackupCommand(ctx, "backup", []string{"--wait"}, &out, &errOut)
	if code != 130 || !strings.Contains(errOut.String(), "still queued") {
		t.Errorf("exit %d\n%s", code, errOut.String())
	}
}

func dataDir(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("HANDOFF_STORE_KEY", testKey)
	t.Setenv("LISTEN_ADDR", "127.0.0.1:1") // nothing listens: the app is not running
	return dir
}

func seed(t *testing.T, dir string) {
	v, _ := vault.New(testKey)
	st, err := store.Open(filepath.Join(dir, backup.DataFile), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddService(context.Background(), store.Service{Name: "club", Tag: "club", InboxTag: "inbox", Refused: "dup", ExpiryDays: 30, OwnerID: uuid.New(), OwnerName: "Carol", OwnerEmail: "c@x.test", KeyID: uuid.New(), Key: "k", Policy: store.PolicyOpen, Visibility: store.VisListed}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupWithoutTheAppRunningIsMadeAtOnceAndListed(t *testing.T) {
	dir := dataDir(t)
	seed(t, dir)
	code, out, errOut := runCmd(t, "backup")
	if code != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	files, _ := backup.List(filepath.Join(dir, "backups"))
	if len(files) != 1 {
		t.Fatalf("files: %+v", files)
	}
	_, listing, _ := runCmd(t, "backup", "--list")
	if !strings.Contains(listing, files[0].Name) {
		t.Errorf("--list: %s", listing)
	}

	// the app holds the data but does not answer where we look: say so, do not guess
	release, err := backup.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code, _, errOut := runCmd(t, "backup"); code != 1 || !strings.Contains(errOut, "did not answer") {
		t.Errorf("app up but silent: exit %d\n%s", code, errOut)
	}
}

func TestRestoreCommand(t *testing.T) {
	dir := dataDir(t)
	seed(t, dir)
	runCmd(t, "backup")
	files, _ := backup.List(filepath.Join(dir, "backups"))
	file := filepath.Join(dir, "backups", files[0].Name)

	fresh := t.TempDir()
	t.Setenv("DATA_DIR", fresh)
	if code, out, errOut := runCmd(t, "restore", file); code != 0 || !strings.Contains(out, "Start the app") {
		t.Fatalf("restore: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(fresh, backup.DataFile)); err != nil {
		t.Error(err)
	}
	if code, _, errOut := runCmd(t, "restore", file); code != 1 || !strings.Contains(errOut, "--force") {
		t.Errorf("over existing data: exit %d %s", code, errOut)
	}
	if code, _, _ := runCmd(t, "restore", "--force", file); code != 0 {
		t.Errorf("with --force: exit %d", code)
	}
	if code, _, errOut := runCmd(t, "restore"); code != 1 || !strings.Contains(errOut, "usage") {
		t.Errorf("no file: exit %d %s", code, errOut)
	}
	t.Setenv("HANDOFF_STORE_KEY", "a-different-store-key-123")
	if code, _, errOut := runCmd(t, "restore", "--force", file); code != 1 || !strings.Contains(errOut, "store key") {
		t.Errorf("wrong key: exit %d %s", code, errOut)
	}
}

package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

type quiet struct{}

func (quiet) WhenIdle(context.Context, time.Duration, time.Duration) (func(), bool) {
	return func() {}, false
}

func withBackups(t *testing.T) (func(*web.Server), string) {
	dir := filepath.Join(t.TempDir(), "backups")
	var st *store.Store
	hubOpt := withHub(t, &st)
	return func(srv *web.Server) {
		hubOpt(srv)
		srv.Backups = &backup.Manager{Dir: dir, Source: st, Idle: quiet{}, Poll: time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}, dir
}

func call(t *testing.T, method, url, secret string) (int, backup.Job, bool) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var r struct {
		backup.Job
		Already bool `json:"already"`
	}
	json.NewDecoder(res.Body).Decode(&r)
	return res.StatusCode, r.Job, r.Already
}

func TestBackupEndpointsNeedTheSecretAndMakeTheFile(t *testing.T) {
	opt, dir := withBackups(t)
	app, _ := setup(t, opt)
	for _, method := range []string{"POST", "GET"} {
		if code, _, _ := call(t, method, app.URL+"/backup", ""); code != 401 {
			t.Errorf("%s /backup without the secret: %d", method, code)
		}
		if code, _, _ := call(t, method, app.URL+"/backup", "wrong"); code != 401 {
			t.Errorf("%s /backup with a wrong secret: %d", method, code)
		}
	}
	if code, _, _ := call(t, "GET", app.URL+"/backup", "s3cret"); code != 404 {
		t.Errorf("no backup yet: %d", code)
	}
	code, job, already := call(t, "POST", app.URL+"/backup", "s3cret")
	if code != 202 || already || job.ID == "" || !strings.HasPrefix(job.Path, dir) {
		t.Fatalf("queue: %d %+v already=%v", code, job, already)
	}
	deadline := time.Now().Add(5 * time.Second)
	var done backup.Job
	for time.Now().Before(deadline) {
		if _, j, _ := call(t, "GET", app.URL+"/backup/"+job.ID, "s3cret"); j.State == backup.Done {
			done = j
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done.State != backup.Done || done.Size == 0 {
		t.Fatalf("the backup did not finish: %+v", done)
	}
	if _, err := os.Stat(done.Path); err != nil {
		t.Errorf("the file is not there: %v", err)
	}
	if code, _, _ := call(t, "GET", app.URL+"/backup/no-such-id", "s3cret"); code != 404 {
		t.Errorf("an unknown job: %d", code)
	}
}

func TestBackupEndpointsAreAbsentWithoutAStore(t *testing.T) {
	app, _ := setup(t) // no store: nothing is kept, so nothing to back up
	if code, _, _ := call(t, "POST", app.URL+"/backup", "s3cret"); code != 404 {
		t.Errorf("without a store: %d, want 404", code)
	}
}

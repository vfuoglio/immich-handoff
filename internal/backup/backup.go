// Package backup makes consistent copies of the app's data file and restores them.
//
// A backup is asked of the running app, which waits for a quiet moment between hand-overs and then copies the
// file (SQLite's VACUUM INTO, which is safe while the app keeps using the file). If the passes are never
// quiet for Wait, the copy is made anyway and is marked forced. The saved keys stay encrypted with the store key.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type State string

const (
	Queued  State = "queued"  // waiting for a quiet moment between hand-overs
	Running State = "running" // being copied
	Done    State = "done"
	Failed  State = "failed"
)

// Job is one requested backup.
type Job struct {
	ID       string    `json:"id"`
	State    State     `json:"state"`
	Path     string    `json:"path"` // where the file is, or will be (a forced one gets "-forced" in its name)
	Forced   bool      `json:"forced"`
	Err      string    `json:"error,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Queued   time.Time `json:"queued"`
	Finished time.Time `json:"finished,omitempty"`
}

// Source is what is copied (the store).
type Source interface {
	Backup(ctx context.Context, dest string) error
}

// Idler waits for a quiet moment between hand-overs (the syncer).
type Idler interface {
	WhenIdle(ctx context.Context, wait, poll time.Duration) (release func(), forced bool)
}

// Manager takes the backups asked of the running app, one at a time.
type Manager struct {
	Dir    string // where the files go
	Source Source
	Idle   Idler
	Wait   time.Duration // how long to wait for a quiet moment before forcing (default 4 hours)
	Poll   time.Duration // how often to look (default 5 seconds)
	Log    *slog.Logger
	Tell   func(string)            // reaches whoever runs the app (NOTIFY_URL); optional
	Event  func(kind, text string) // the activity log; optional
	Now    func() time.Time

	mu   sync.Mutex
	last *Job
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func name(id string, forced bool) string {
	if forced {
		return "handoff-" + id + "-forced.db"
	}
	return "handoff-" + id + ".db"
}

// Queue asks for a backup. If one is already waiting or running it returns that one (already = true), so a
// second request never adds another. The work is done in the background, until ctx ends.
func (m *Manager) Queue(ctx context.Context) (job Job, already bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last != nil && (m.last.State == Queued || m.last.State == Running) {
		return *m.last, true
	}
	t := m.now()
	id := t.UTC().Format("20060102-150405")
	j := &Job{ID: id, State: Queued, Path: filepath.Join(m.Dir, name(id, false)), Queued: t}
	m.last = j
	m.Log.Info("backup queued: it starts at the next quiet moment between hand-overs", "file", j.Path)
	go m.run(ctx, j)
	return *j, false
}

// Latest is the most recent job, if any.
func (m *Manager) Latest() (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		return Job{}, false
	}
	return *m.last, true
}

// Get is the job with this id, if it is the latest one.
func (m *Manager) Get(id string) (Job, bool) {
	if j, ok := m.Latest(); ok && j.ID == id {
		return j, true
	}
	return Job{}, false
}

func (m *Manager) set(j *Job, f func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(j)
}

func (m *Manager) run(ctx context.Context, j *Job) {
	wait, poll := m.Wait, m.Poll
	if wait <= 0 {
		wait = 4 * time.Hour
	}
	if poll <= 0 {
		poll = 5 * time.Second
	}
	release, forced := m.Idle.WhenIdle(ctx, wait, poll)
	dest := filepath.Join(m.Dir, name(j.ID, forced))
	m.set(j, func(j *Job) { j.State, j.Forced, j.Path = Running, forced, dest })
	err := os.MkdirAll(m.Dir, 0o700)
	if err == nil {
		err = m.Source.Backup(ctx, dest)
	}
	release()
	var size int64
	if err == nil {
		if fi, e := os.Stat(dest); e == nil {
			size = fi.Size()
		}
	}
	m.set(j, func(j *Job) {
		j.Finished, j.Size = m.now(), size
		if err != nil {
			j.State, j.Err = Failed, err.Error()
		} else {
			j.State = Done
		}
	})
	switch {
	case err != nil:
		m.Log.Error("backup failed", "err", err)
		m.tell("backup failed: "+err.Error(), "backup", "A backup failed: "+err.Error())
	case forced:
		msg := fmt.Sprintf("A backup was made without waiting for a quiet moment, because hand-overs kept running for %s. It is a consistent copy but may hold a hand-over caught halfway: %s", wait.Round(time.Minute), dest)
		m.Log.Warn("backup ready, but forced: "+msg, "file", dest, "bytes", size)
		m.tell("backup made anyway (forced): "+dest, "backup", msg)
	default:
		m.Log.Info("backup ready", "file", dest, "bytes", size)
		if m.Event != nil {
			m.Event("backup", "A backup was made: "+filepath.Base(dest))
		}
	}
}

func (m *Manager) tell(note, kind, event string) {
	if m.Tell != nil {
		m.Tell("immich-handoff: " + note)
	}
	if m.Event != nil {
		m.Event(kind, event)
	}
}

// Direct makes a backup at once, for when the app is not running (nothing is then handing anything over).
func Direct(ctx context.Context, src Source, dir string, now time.Time) (Job, error) {
	id := now.UTC().Format("20060102-150405")
	j := Job{ID: id, State: Done, Path: filepath.Join(dir, name(id, false)), Queued: now}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return j, err
	}
	if err := src.Backup(ctx, j.Path); err != nil {
		j.State, j.Err = Failed, err.Error()
		return j, err
	}
	if fi, err := os.Stat(j.Path); err == nil {
		j.Size = fi.Size()
	}
	j.Finished = time.Now()
	return j, nil
}

// File is a backup in the folder.
type File struct {
	Name   string
	Size   int64
	When   time.Time
	Forced bool
}

// List returns the backups in dir, newest first.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "handoff-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, File{Name: e.Name(), Size: fi.Size(), When: fi.ModTime(), Forced: strings.HasSuffix(e.Name(), "-forced.db")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

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
	"strconv"
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
	Auto     bool      `json:"auto,omitempty"` // made on the schedule, not asked for
	Err      string    `json:"error,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Queued   time.Time `json:"queued"`
	Finished time.Time `json:"finished,omitempty"`

	done chan struct{} // closed when the job has finished
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

	// Automatic backups (see Schedule). Every <= 0 means none.
	Every time.Duration // how often one is made
	Keep  int           // how many automatic ones are kept (at least 1)
	Retry time.Duration // how long to wait before trying again after a failure (default 1 hour)
	Delay time.Duration // how long after start the first one is made when none is due (default 30 seconds)

	mu   sync.Mutex
	last *Job
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// autoPrefix starts the name of a backup the app made on its own. Only these are ever removed by the app.
const autoPrefix = "handoff-auto-"

func name(id string, forced, auto bool) string {
	n := "handoff-"
	if auto {
		n = autoPrefix
	}
	n += id
	if forced {
		n += "-forced"
	}
	return n + ".db"
}

// Queue asks for a backup. If one is already waiting or running it returns that one (already = true), so a
// second request never adds another. The work is done in the background, until ctx ends.
func (m *Manager) Queue(ctx context.Context) (job Job, already bool) { return m.queue(ctx, false) }

func (m *Manager) queue(ctx context.Context, auto bool) (job Job, already bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last != nil && (m.last.State == Queued || m.last.State == Running) {
		return *m.last, true
	}
	t := m.now()
	id := t.UTC().Format("20060102-150405")
	j := &Job{ID: id, State: Queued, Auto: auto, Path: filepath.Join(m.Dir, name(id, false, auto)), Queued: t, done: make(chan struct{})}
	m.last = j
	m.Log.Info("backup queued: it starts at the next quiet moment between hand-overs", "file", j.Path, "automatic", auto)
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
	dest := filepath.Join(m.Dir, name(j.ID, forced, j.Auto))
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
			what := "A backup was made: "
			if j.Auto {
				what = "An automatic backup was made: "
			}
			m.Event("backup", what+filepath.Base(dest))
		}
	}
	if err == nil && j.Auto {
		m.prune()
	}
	// the job counts as finished only once everyone has been told, so whoever sees "done" can rely on that
	m.set(j, func(j *Job) {
		j.Finished, j.Size = m.now(), size
		if err != nil {
			j.State, j.Err = Failed, err.Error()
		} else {
			j.State = Done
		}
		if j.done != nil {
			close(j.done)
		}
	})
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
	j := Job{ID: id, State: Done, Path: filepath.Join(dir, name(id, false, false)), Queued: now}
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
	Auto   bool // made on the schedule
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
		out = append(out, File{Name: e.Name(), Size: fi.Size(), When: fi.ModTime(), Forced: strings.HasSuffix(e.Name(), "-forced.db"), Auto: strings.HasPrefix(e.Name(), autoPrefix)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// prune removes the oldest automatic backups beyond Keep. Backups someone asked for are never removed.
func (m *Manager) prune() {
	keep := m.Keep
	if keep < 1 {
		keep = 1
	}
	files, err := List(m.Dir)
	if err != nil {
		m.Log.Warn("could not list the backups to remove old ones", "err", err)
		return
	}
	n := 0
	for _, f := range files { // newest first
		if !f.Auto {
			continue
		}
		if n++; n <= keep {
			continue
		}
		if err := os.Remove(filepath.Join(m.Dir, f.Name)); err != nil {
			m.Log.Warn("could not remove an old automatic backup", "file", f.Name, "err", err)
		} else {
			m.Log.Info("removed an old automatic backup", "file", f.Name)
		}
	}
}

// due is when the next automatic backup is made: Every after the newest automatic one, or soon after start when
// there is none or it is overdue.
func (m *Manager) due() time.Time {
	delay := m.Delay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	now := m.now()
	files, _ := List(m.Dir)
	for _, f := range files {
		if f.Auto {
			if t := f.When.Add(m.Every); t.After(now.Add(delay)) {
				return t
			}
			break
		}
	}
	return now.Add(delay)
}

// Schedule makes a backup every Every, waits for a quiet moment like any other, keeps the newest Keep and
// returns when ctx ends. A failure is reported like any failed backup and tried again after Retry.
func (m *Manager) Schedule(ctx context.Context) {
	if m.Every <= 0 {
		return
	}
	retry := m.Retry
	if retry <= 0 {
		retry = time.Hour
	}
	m.Log.Info("automatic backups are on", "every", m.Every.String(), "keep", m.Keep, "folder", m.Dir)
	next := m.due()
	for {
		if !sleepUntil(ctx, m.now(), next) {
			return
		}
		job, already := m.queue(ctx, true)
		if already { // one asked for is in progress: look again soon
			next = m.now().Add(time.Minute)
			if m.Poll > 0 && m.Poll < time.Minute {
				next = m.now().Add(m.Poll)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-job.done:
		}
		if j, ok := m.Latest(); ok && j.State == Failed {
			next = m.now().Add(retry)
		} else {
			next = m.now().Add(m.Every)
		}
	}
}

func sleepUntil(ctx context.Context, now, t time.Time) bool {
	d := t.Sub(now)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ParseSchedule reads BACKUP_EVERY (a time such as 24h, or "off"; default 24h) and BACKUP_KEEP (default 7).
func ParseSchedule(getenv func(string) string) (every time.Duration, keep int, err error) {
	every = 24 * time.Hour
	switch v := strings.ToLower(strings.TrimSpace(getenv("BACKUP_EVERY"))); v {
	case "":
	case "off", "0", "no", "false":
		every = 0
	default:
		if every, err = time.ParseDuration(v); err != nil || every < time.Hour {
			return 0, 0, fmt.Errorf("BACKUP_EVERY=%q: use a time of one hour or more such as 24h, or off", getenv("BACKUP_EVERY"))
		}
	}
	keep = 7
	if v := strings.TrimSpace(getenv("BACKUP_KEEP")); v != "" {
		if keep, err = strconv.Atoi(v); err != nil || keep < 1 {
			return 0, 0, fmt.Errorf("BACKUP_KEEP=%q: use a whole number of 1 or more", v)
		}
	}
	return every, keep, nil
}

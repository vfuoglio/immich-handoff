package backup_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const storeKey = "0123456789abcdef0123"

func newStore(t *testing.T, dir string, key string) *store.Store {
	t.Helper()
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, backup.DataFile), v)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func fill(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.AddService(ctx, store.Service{Name: "club", Tag: "club", InboxTag: "inbox", Refused: "dup", ExpiryDays: 30, OwnerID: uuid.New(), OwnerName: "Carol", OwnerEmail: "c@x.test", KeyID: uuid.New(), Key: "receiver-secret", Policy: store.PolicyOpen, Visibility: store.VisListed}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLink(ctx, store.Link{Service: "club", Role: store.RoleSender, ImmichUserID: uuid.New(), Name: "Bob", Email: "b@x.test", KeyID: uuid.New(), Key: "sender-secret"}); err != nil {
		t.Fatal(err)
	}
}

// fakeIdle is a syncer that is busy until told otherwise.
type fakeIdle struct {
	mu       sync.Mutex
	busy     bool
	released bool
	waited   time.Duration
}

func (f *fakeIdle) setBusy(b bool) { f.mu.Lock(); f.busy = b; f.mu.Unlock() }

func (f *fakeIdle) WhenIdle(ctx context.Context, wait, poll time.Duration) (func(), bool) {
	deadline := time.Now().Add(wait)
	for {
		f.mu.Lock()
		if !f.busy {
			f.mu.Unlock()
			return func() { f.mu.Lock(); f.released = true; f.mu.Unlock() }, false
		}
		f.mu.Unlock()
		if !time.Now().Before(deadline) {
			return func() { f.mu.Lock(); f.released = true; f.mu.Unlock() }, true
		}
		time.Sleep(poll)
	}
}

// manager returns what the manager told and logged as functions, because it writes them from its own goroutine
func manager(t *testing.T, st backup.Source, idle backup.Idler) (m *backup.Manager, told func() []string, events func() []string) {
	t.Helper()
	var toldList, eventList []string
	var mu sync.Mutex
	m = &backup.Manager{Dir: filepath.Join(t.TempDir(), "backups"), Source: st, Idle: idle, Poll: 5 * time.Millisecond, Wait: 40 * time.Millisecond,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tell: func(s string) { mu.Lock(); toldList = append(toldList, s); mu.Unlock() },
		Event: func(kind, text string) {
			mu.Lock()
			eventList = append(eventList, kind+": "+text)
			mu.Unlock()
		}}
	read := func(l *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *l...) }
	}
	return m, read(&toldList), read(&eventList)
}

func finish(t *testing.T, m *backup.Manager) backup.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := m.Latest(); ok && (j.State == backup.Done || j.State == backup.Failed) {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the backup did not finish")
	return backup.Job{}
}

func TestABackupWaitsForAQuietMomentThenCopiesAConsistentFile(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	fill(t, st)
	idle := &fakeIdle{busy: true}
	m, _, events := manager(t, st, idle)
	m.Wait = time.Minute // long enough that it is not forced

	job, already := m.Queue(context.Background())
	if already || job.State != backup.Queued {
		t.Fatalf("first request: %+v already=%v", job, already)
	}
	time.Sleep(50 * time.Millisecond)
	if j, _ := m.Latest(); j.State != backup.Queued {
		t.Errorf("while hand-overs run it must stay queued, got %s", j.State)
	}
	if _, err := os.Stat(job.Path); err == nil {
		t.Error("nothing must be copied while hand-overs run")
	}
	// a second request while one waits is the same backup
	if again, already := m.Queue(context.Background()); !already || again.ID != job.ID {
		t.Errorf("second request: %+v already=%v", again, already)
	}
	idle.setBusy(false)
	done := finish(t, m)
	if done.State != backup.Done || done.Forced || done.Size == 0 || strings.Contains(done.Path, "forced") {
		t.Fatalf("finished job: %+v", done)
	}
	if !idle.released {
		t.Error("the passes must be let go after the copy")
	}
	if len(events()) != 1 || !strings.HasPrefix((events())[0], "backup:") {
		t.Errorf("events: %v", events())
	}
}

func TestAfterTheWaitTheBackupIsMadeAnywayMarkedForcedAndEveryoneIsTold(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	fill(t, st)
	idle := &fakeIdle{busy: true} // never quiet
	m, told, events := manager(t, st, idle)
	m.Queue(context.Background())
	done := finish(t, m)
	if done.State != backup.Done || !done.Forced || !strings.HasSuffix(done.Path, "-forced.db") {
		t.Fatalf("forced job: %+v", done)
	}
	if _, err := os.Stat(done.Path); err != nil {
		t.Errorf("the forced file must exist: %v", err)
	}
	if len(told()) != 1 || !strings.Contains((told())[0], "forced") {
		t.Errorf("the operator must be told: %v", told())
	}
	if len(events()) != 1 || !strings.Contains((events())[0], "without waiting for a quiet moment") {
		t.Errorf("the activity log must say so: %v", events())
	}
	files, _ := backup.List(m.Dir)
	if len(files) != 1 || !files[0].Forced {
		t.Errorf("the listing must mark it: %+v", files)
	}
}

type failing struct{}

func (failing) Backup(context.Context, string) error { return errors.New("disk full") }

func TestAFailedBackupIsReported(t *testing.T) {
	m, told, events := manager(t, failing{}, &fakeIdle{})
	m.Queue(context.Background())
	done := finish(t, m)
	if done.State != backup.Failed || !strings.Contains(done.Err, "disk full") {
		t.Fatalf("job: %+v", done)
	}
	if len(told()) != 1 || len(events()) != 1 {
		t.Errorf("a failure must be told: %v %v", told(), events())
	}
	// and a new request is accepted after a failure
	if _, already := m.Queue(context.Background()); already {
		t.Error("a failed backup must not block the next one")
	}
	finish(t, m)
}

func TestABackupTakenWhileTheAppWritesIsConsistent(t *testing.T) {
	dir := t.TempDir()
	st := newStore(t, dir, storeKey)
	defer st.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the app keeps adding events
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				st.AddEvent(context.Background(), store.Event{Kind: "x", Text: "busy"})
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	dest := filepath.Join(t.TempDir(), "copy.db")
	err := st.Backup(context.Background(), dest)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// the copy passes the integrity check and opens with the key
	restoreDir := t.TempDir()
	if err := backup.Restore(context.Background(), dest, restoreDir, mustVault(t, storeKey), false); err != nil {
		t.Fatalf("a copy taken while writing must restore: %v", err)
	}
	if err := st.Backup(context.Background(), dest); err == nil {
		t.Error("an existing file must never be overwritten")
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Errorf("a backup must be readable by its owner only: %v", fi.Mode())
	}
}

func mustVault(t *testing.T, key string) *vault.Vault {
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDirectBackupWhenTheAppIsNotRunning(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	fill(t, st)
	job, err := backup.Direct(context.Background(), st, filepath.Join(t.TempDir(), "b"), time.Now())
	if err != nil || job.State != backup.Done || job.Size == 0 {
		t.Fatalf("%+v %v", job, err)
	}
}

func TestRestoreRoundTripsAndKeepsTheOldFile(t *testing.T) {
	src := t.TempDir()
	st := newStore(t, src, storeKey)
	fill(t, st)
	file := filepath.Join(t.TempDir(), "b.db")
	if err := st.Backup(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	st.Close()

	dir := t.TempDir()
	v := mustVault(t, storeKey)
	if err := backup.Restore(context.Background(), file, dir, v, false); err != nil {
		t.Fatal(err)
	}
	got := newStore(t, dir, storeKey)
	svcs, _ := got.Services(context.Background())
	links, err := got.Links(context.Background())
	if err != nil || len(svcs) != 1 || svcs[0].Key != "receiver-secret" || len(links) != 1 || links[0].Key != "sender-secret" || links[0].Email != "b@x.test" {
		t.Fatalf("restored data: %+v %+v %v", svcs, links, err)
	}
	got.Close()

	// data is there now: refuse without --force, replace and keep the old file with it
	if err := backup.Restore(context.Background(), file, dir, v, false); !errors.Is(err, backup.ErrExists) {
		t.Errorf("over existing data without --force: %v", err)
	}
	if err := backup.Restore(context.Background(), file, dir, v, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, backup.DataFile+".before-restore")); err != nil {
		t.Errorf("the old file must be kept: %v", err)
	}
}

func TestRestoreRefusesWhatItCannotTrust(t *testing.T) {
	src := t.TempDir()
	st := newStore(t, src, storeKey)
	fill(t, st)
	good := filepath.Join(t.TempDir(), "good.db")
	st.Backup(context.Background(), good)
	st.Close()
	ctx := context.Background()

	// the wrong store key
	if err := backup.Restore(ctx, good, t.TempDir(), mustVault(t, "a-different-store-key-123"), false); err == nil || !strings.Contains(err.Error(), "store key") {
		t.Errorf("wrong key: %v", err)
	}
	// not a database at all, and a damaged one
	junk := filepath.Join(t.TempDir(), "junk.db")
	os.WriteFile(junk, []byte("this is not a database, just text of some length to look like one"), 0o600)
	if err := backup.Restore(ctx, junk, t.TempDir(), mustVault(t, storeKey), false); err == nil {
		t.Error("junk must be refused")
	}
	raw, _ := os.ReadFile(good)
	damaged := filepath.Join(t.TempDir(), "damaged.db")
	// wreck the pages of the tables the opening check does not read (the first page and the key check are left alone)
	for i := 4096 * 3; i < len(raw); i++ {
		raw[i] ^= 0xFF
	}
	os.WriteFile(damaged, raw, 0o600)
	if err := backup.Restore(ctx, damaged, t.TempDir(), mustVault(t, storeKey), false); err == nil {
		t.Error("a damaged file must be refused")
	}
	// a database from a newer version
	newer := filepath.Join(t.TempDir(), "newer.db")
	raw, _ = os.ReadFile(good)
	copy(raw[60:64], []byte{0, 0, 3, 232}) // PRAGMA user_version = 1000, stored in the header
	os.WriteFile(newer, raw, 0o600)
	if err := backup.Restore(ctx, newer, t.TempDir(), mustVault(t, storeKey), false); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Errorf("newer schema: %v", err)
	}
	// nothing was left half done in the target
	dir := t.TempDir()
	backup.Restore(ctx, junk, dir, mustVault(t, storeKey), false)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != "handoff.lock" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a refused restore left files behind: %v", names)
	}
}

func TestRestoreRefusesWhileTheAppRuns(t *testing.T) {
	dir := t.TempDir()
	st := newStore(t, dir, storeKey)
	fill(t, st)
	file := filepath.Join(t.TempDir(), "b.db")
	st.Backup(context.Background(), file)
	st.Close()

	release, err := backup.Lock(dir) // what the running app holds
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(context.Background(), file, dir, mustVault(t, storeKey), true); !errors.Is(err, backup.ErrInUse) {
		t.Errorf("restore under a running app: %v", err)
	}
	if _, err := backup.Lock(dir); !errors.Is(err, backup.ErrInUse) {
		t.Errorf("a second app on the same data: %v", err)
	}
	release()
	if err := backup.Restore(context.Background(), file, dir, mustVault(t, storeKey), true); err != nil {
		t.Errorf("after the app stopped: %v", err)
	}
}

func autoFiles(t *testing.T, dir string) []backup.File {
	t.Helper()
	files, err := backup.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []backup.File
	for _, f := range files {
		if f.Auto {
			out = append(out, f)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func TestAutomaticBackupsAreMadeOnTheScheduleAndOnlyTheNewestAreKept(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	fill(t, st)
	m, _, events := manager(t, st, &fakeIdle{})
	m.Every, m.Keep, m.Delay = 1100*time.Millisecond, 2, 20*time.Millisecond // the id has one-second resolution

	// a backup someone asked for is never removed
	asked, _ := m.Queue(context.Background())
	finish(t, m)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go m.Schedule(ctx)
	waitFor(t, "three automatic backups", func() bool { return len(events()) >= 4 })
	stop()
	time.Sleep(50 * time.Millisecond)

	autos := autoFiles(t, m.Dir)
	if len(autos) != 2 {
		t.Fatalf("kept %d automatic backups, want 2: %+v", len(autos), autos)
	}
	if _, err := os.Stat(asked.Path); err != nil {
		t.Fatalf("the backup that was asked for was removed: %v", err)
	}
	for _, e := range events() {
		if strings.Contains(e, "automatic") {
			return
		}
	}
	t.Fatalf("the activity log never says a backup was automatic: %v", events())
}

func TestTheNextAutomaticBackupWaitsEveryAfterTheNewestOne(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	m, _, _ := manager(t, st, &fakeIdle{})
	m.Every, m.Keep, m.Delay = time.Hour, 3, 20*time.Millisecond

	ctx, stop := context.WithCancel(context.Background())
	go m.Schedule(ctx)
	waitFor(t, "the first automatic backup", func() bool { j, ok := m.Latest(); return ok && j.State == backup.Done })
	stop()

	// a second run, a moment later, finds the newest one still fresh and makes nothing
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	go m.Schedule(ctx2)
	time.Sleep(300 * time.Millisecond)
	if n := len(autoFiles(t, m.Dir)); n != 1 {
		t.Fatalf("%d automatic backups after a restart within the hour, want 1", n)
	}
}

type countingFailure struct{ calls atomic.Int32 }

func (f *countingFailure) Backup(ctx context.Context, dest string) error {
	f.calls.Add(1)
	return errors.New("the disk is full")
}

func TestAFailedAutomaticBackupIsReportedAndTriedAgainLater(t *testing.T) {
	src := &countingFailure{}
	m, told, _ := manager(t, src, &fakeIdle{})
	m.Every, m.Keep, m.Delay, m.Retry = time.Hour, 3, 10*time.Millisecond, 40*time.Millisecond
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go m.Schedule(ctx)
	waitFor(t, "a second try", func() bool { return src.calls.Load() >= 2 })
	if len(told()) < 2 || !strings.Contains(told()[0], "the disk is full") {
		t.Fatalf("a failed backup was not reported: %v", told())
	}
}

func TestADownloadIsACopyMadeAtOnceThatLeavesNothingBehind(t *testing.T) {
	st := newStore(t, t.TempDir(), storeKey)
	defer st.Close()
	fill(t, st)
	busy := &fakeIdle{busy: true} // a download does not wait for a quiet moment
	m, told, events := manager(t, st, busy)

	f, name, cleanup, err := m.Download(context.Background(), "Alice (a@x.test)")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "handoff-") || !strings.HasSuffix(name, ".db") {
		t.Fatalf("name %q", name)
	}
	head := make([]byte, 15)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "SQLite format 3" {
		t.Fatalf("not a SQLite file: %q %v", head, err)
	}
	if len(told()) != 1 || !strings.Contains(told()[0], "Alice (a@x.test)") {
		t.Fatalf("whoever runs the app was not told: %v", told())
	}
	if len(events()) != 1 || !strings.Contains(events()[0], "downloaded by Alice") {
		t.Fatalf("the activity log: %v", events())
	}
	cleanup()
	entries, _ := os.ReadDir(m.Dir)
	if len(entries) != 0 {
		t.Fatalf("a download left files in the backup folder: %v", entries)
	}
	if files, _ := backup.List(m.Dir); len(files) != 0 {
		t.Fatalf("a download shows up as a backup: %v", files)
	}
}

func TestTheScheduleIsReadFromTheEnvironment(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	every, keep, err := backup.ParseSchedule(get(nil))
	if err != nil || every != 24*time.Hour || keep != 7 {
		t.Errorf("defaults: %v %d %v", every, keep, err)
	}
	every, keep, err = backup.ParseSchedule(get(map[string]string{"BACKUP_EVERY": "6h", "BACKUP_KEEP": "30"}))
	if err != nil || every != 6*time.Hour || keep != 30 {
		t.Errorf("6h/30: %v %d %v", every, keep, err)
	}
	for _, off := range []string{"off", "OFF", "0", "no"} {
		if every, _, err := backup.ParseSchedule(get(map[string]string{"BACKUP_EVERY": off})); err != nil || every != 0 {
			t.Errorf("%q: %v %v, want off", off, every, err)
		}
	}
	for _, bad := range []map[string]string{{"BACKUP_EVERY": "5m"}, {"BACKUP_EVERY": "daily"}, {"BACKUP_KEEP": "0"}, {"BACKUP_KEEP": "many"}} {
		if _, _, err := backup.ParseSchedule(get(bad)); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}

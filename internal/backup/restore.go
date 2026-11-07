package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// DataFile is the name of the database in the data folder.
const DataFile = "handoff.db"

// ErrInUse means a running app holds the data folder.
var ErrInUse = errors.New("the app is running and using this data folder: stop it first")

// ErrExists means there is data already and the person did not say to replace it.
var ErrExists = errors.New("this data folder already has data: pass --force to replace it (the old file is kept as " + DataFile + ".before-restore)")

// Lock marks the data folder as in use. The app holds it for as long as it runs, so a restore can tell that
// replacing the file now would corrupt it. The lock belongs to the process, so a crash never leaves it behind.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "handoff.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	busy, err := tryLock(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if busy {
		f.Close()
		return nil, ErrInUse
	}
	return func() { f.Close() }, nil
}

// Restore puts a backup in place as the data file of dir. It first checks the file: it must pass SQLite's own
// integrity check, open with this store key, and not come from a newer version. It refuses while the app is
// running, and refuses to replace existing data unless force is set, in which case the old file is kept.
func Restore(ctx context.Context, file, dir string, v *vault.Vault, force bool) error {
	release, err := Lock(dir)
	if err != nil {
		return err
	}
	defer release()

	live := filepath.Join(dir, DataFile)
	if _, err := os.Stat(live); err == nil && !force {
		return ErrExists
	}
	// work on a copy next to the real file, so the check migrates and opens the copy, never the backup itself
	tmp := live + ".restoring"
	cleanup := func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(tmp + suffix)
		}
	}
	cleanup()
	defer cleanup()
	if err := copyFile(file, tmp); err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	st, err := store.Open(tmp, v) // runs the schema check and the store-key check
	if err != nil {
		return fmt.Errorf("%s cannot be restored: %w", file, err)
	}
	if err := st.Check(ctx); err != nil {
		st.Close()
		return fmt.Errorf("%s cannot be restored: %w", file, err)
	}
	if err := st.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} { // folded into the file by Close; never carried over
		os.Remove(tmp + suffix)
	}
	if _, err := os.Stat(live); err == nil {
		before := live + ".before-restore"
		os.Remove(before)
		if err := os.Rename(live, before); err != nil {
			return err
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(live + suffix)
	}
	return os.Rename(tmp, live)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

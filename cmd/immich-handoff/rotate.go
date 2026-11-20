package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// runRotateKey is `immich-handoff rotate-key`: seal every stored key again, with a new salt and (when
// HANDOFF_STORE_KEY_NEW or HANDOFF_STORE_KEY_NEW_FILE is set) a new store key. The app must be stopped. A backup is
// made first, and the change is all or nothing.
func runRotateKey(ctx context.Context, out, errOut io.Writer) int {
	dir := env("DATA_DIR", "/data")
	oldKey, err := readStoreKey()
	if err != nil || oldKey == "" {
		fmt.Fprintln(errOut, "rotate-key: HANDOFF_STORE_KEY (the key the data is sealed with now) is not set")
		return exitFailed
	}
	newKey := oldKey
	if path := os.Getenv("HANDOFF_STORE_KEY_NEW_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(errOut, "rotate-key:", err)
			return exitFailed
		}
		newKey = strings.TrimSpace(string(b))
	} else if k := strings.TrimSpace(os.Getenv("HANDOFF_STORE_KEY_NEW")); k != "" {
		newKey = k
	}
	oldV, err := vault.New(oldKey)
	if err != nil {
		fmt.Fprintln(errOut, "rotate-key:", err)
		return exitFailed
	}
	newV, err := vault.New(newKey)
	if err != nil {
		fmt.Fprintln(errOut, "rotate-key: the new key:", err)
		return exitFailed
	}
	release, err := backup.Lock(dir)
	if err != nil {
		fmt.Fprintf(errOut, "rotate-key: the app is using %s: stop it first (docker compose stop immich-handoff), then run this again\n", dir)
		return exitFailed
	}
	defer release()
	st, err := store.Open(dir+"/"+backup.DataFile, oldV)
	if err != nil {
		fmt.Fprintln(errOut, "rotate-key:", err)
		return exitFailed
	}
	defer st.Close()
	job, err := backup.Direct(ctx, st, backupDir(dir), time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "rotate-key: the backup before the change failed, so nothing was changed:", err)
		return exitFailed
	}
	fmt.Fprintf(out, "A backup was made first: %s (it opens with the OLD key)\n", job.Path)
	n, err := st.Rotate(ctx, newV)
	if err != nil {
		fmt.Fprintln(errOut, "rotate-key: nothing was changed:", err)
		return exitFailed
	}
	if newKey == oldKey {
		fmt.Fprintf(out, "%d stored values were sealed again with a new salt. The store key is the same.\n", n)
	} else {
		fmt.Fprintf(out, "%d stored values were sealed again. Set HANDOFF_STORE_KEY to the new key and start the app; links sent by email before the change stop working.\n", n)
	}
	return exitOK
}

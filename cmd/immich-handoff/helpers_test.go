package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeNotifyFile makes a notify.yaml with one JSON destination called "broken".
func writeNotifyFile(t *testing.T, url string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notify.yaml")
	if err := os.WriteFile(p, []byte("destinations:\n  - {name: broken, format: json, url: '"+url+"'}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

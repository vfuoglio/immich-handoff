package store

import "os"

func readAll(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if w, err2 := os.ReadFile(p + "-wal"); err2 == nil {
		b = append(b, w...)
	}
	return b, err
}

//go:build !unix

package backup

import "os"

// There is no file lock here; the check that the app is not running then protects nothing.
func tryLock(f *os.File) (bool, error) { return false, nil }

//go:build unix

package backup

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on the file without waiting. busy is true when someone else holds it. A file
// system that cannot lock (some network shares) is reported as neither held nor failed: the lock then protects nothing.
func tryLock(f *os.File) (busy bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EINVAL):
		return false, nil
	}
	return false, err
}

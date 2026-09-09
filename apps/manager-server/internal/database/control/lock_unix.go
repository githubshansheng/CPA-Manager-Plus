//go:build !windows

package control

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockFile(file *os.File) error   { return unix.Flock(int(file.Fd()), unix.LOCK_EX) }
func unlockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func hardenFilePermissions(path string) error { return os.Chmod(path, 0o600) }

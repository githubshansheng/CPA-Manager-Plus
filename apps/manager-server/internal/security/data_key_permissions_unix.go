//go:build !windows

package security

import "os"

func hardenDataKeyPermissions(path string) error { return os.Chmod(path, 0o600) }

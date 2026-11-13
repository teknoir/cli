//go:build unix

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// ensurePrivateDir makes dir a directory of the caller's own, mode 0700, or refuses it: the
// captures in it are MQTT payloads, and another user reading them is a leak. A symlink is refused
// rather than followed, and so is a directory of another user: the temp dir is shared, and a
// tnctl-captures-<uid> another user made first would take the captures of every session.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link, not a directory", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

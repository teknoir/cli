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
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// checkPrivateDir refuses dir unless it is a directory of the caller's own and not a symbolic link.
// It creates nothing and changes no mode: a sweep checks the dir with it before it reads or removes
// anything there, so that it never follows a link into a dir that a write would refuse.
func checkPrivateDir(dir string) error {
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
	return nil
}

// runtimeDirUsable reports whether dir ($XDG_RUNTIME_DIR) can hold the captures: it exists, is a
// directory and belongs to the caller. One inherited from another user (su without -l) or one that
// was removed is not, and the captures go to the cache dir instead.
func runtimeDirUsable(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

//go:build !unix

package cmd

import (
	"fmt"
	"os"
)

// ensurePrivateDir makes dir. There is no uid to check here, and no unix permissions to set: on
// Windows the temp dir is per user already. A symbolic link or a file at dir is still refused.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkPrivateDir(dir)
}

// checkPrivateDir refuses dir unless it is a directory and not a symbolic link. It creates nothing:
// a sweep checks the dir with it before it reads or removes anything there.
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
	return nil
}

// runtimeDirUsable reports whether dir ($XDG_RUNTIME_DIR) can hold the captures: it exists and is a
// directory.
func runtimeDirUsable(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

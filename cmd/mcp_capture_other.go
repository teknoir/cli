//go:build !unix

package cmd

import "os"

// ensurePrivateDir makes dir. There is no uid to check here, and no unix permissions to set: on
// Windows the temp dir is per user already.
func ensurePrivateDir(dir string) error { return os.MkdirAll(dir, 0o700) }

//go:build !(linux || darwin || freebsd || dragonfly || aix)

package coder

import "errors"

// defaultStatfs reports that this platform does not know its free space: the disk checks are then
// skipped, not failed.
func defaultStatfs(string) (uint64, error) {
	return 0, errors.New("free space is not known on this platform")
}

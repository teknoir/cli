//go:build linux || darwin || freebsd || dragonfly || aix

package coder

import "syscall"

// defaultStatfs is the free space of the volume at path: what is left for an unprivileged writer.
// The platforms that have no syscall.Statfs with Bavail and Bsize (netbsd, solaris, illumos,
// openbsd) have statfs_other.go's "unknown" instead.
func defaultStatfs(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail is signed on some of the platforms below: a negative count is no free space, not a
	// huge one.
	avail, bsize := int64(st.Bavail), int64(st.Bsize)
	if avail <= 0 || bsize <= 0 {
		return 0, nil
	}
	return uint64(avail) * uint64(bsize), nil
}

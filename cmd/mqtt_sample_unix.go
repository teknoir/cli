//go:build unix

package cmd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// mqttNoFollow makes an open refuse a symbolic link and never wait on a FIFO or a device: a FIFO
// with no reader fails with ENXIO at once, and one with a reader opens and is then refused as not a
// regular file. O_NONBLOCK changes nothing for a regular file.
const mqttNoFollow = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// mqttLinkRefused reports whether an open with mqttNoFollow failed because the path is a symbolic
// link: ELOOP on Linux and macOS, EMLINK on FreeBSD.
func mqttLinkRefused(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// mqttSingleLink refuses a file with more than one name: a hard link to a file of someone else's
// would make tnctl write into it.
func mqttSingleLink(st os.FileInfo, name string) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Nlink != 1 {
		return fmt.Errorf("%s has more than one hard link", name)
	}
	return nil
}

//go:build unix

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// mqttNoFollow makes an open refuse a symbolic link.
const mqttNoFollow = syscall.O_NOFOLLOW

// mqttSingleLink refuses a file with more than one name: a hard link to a file of someone else's
// would make tnctl write into it.
func mqttSingleLink(st os.FileInfo, name string) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Nlink != 1 {
		return fmt.Errorf("%s has more than one hard link", name)
	}
	return nil
}

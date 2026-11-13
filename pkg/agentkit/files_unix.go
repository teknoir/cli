//go:build unix

package agentkit

import "syscall"

// dirWritable reports whether a file can be created in dir, without writing anything: access(2) reports
// EACCES for a directory the user cannot write (mode 0555) and EROFS for a read-only mount such as
// /nix/store. A dry run, and a run that changes nothing, must not touch the user's dotfiles directory.
func dirWritable(dir string) bool { return syscall.Access(dir, 0x2 /* W_OK */) == nil }

//go:build unix

package coder

import (
	"os"
	"os/exec"
	"syscall"
)

// termSignal and killSignal are the two steps of a stop.
var (
	termSignal os.Signal = syscall.SIGTERM
	killSignal os.Signal = syscall.SIGKILL
)

// setProcessGroup puts the process at the head of its own process group, so that a stop reaches
// the shells and builds it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the process group whose id was recorded when the process started. The
// group is not looked up again: the leader may be reaped by then, and its pid reused.
func signalGroup(pgid int, sig os.Signal) error {
	if pgid <= 0 {
		return nil
	}
	s, ok := sig.(syscall.Signal)
	if !ok {
		s = syscall.SIGTERM
	}
	return syscall.Kill(-pgid, s)
}

// killGroup ends what is left of a process group: the background processes a finished task left
// behind. ESRCH is the wanted outcome too, and any other error leaves nothing to do.
func killGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	// A group that is already empty returns ESRCH, which is exactly the outcome wanted.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// signalOf is the signal that killed the process, if one did.
func signalOf(ps *os.ProcessState) (string, bool) {
	if ps == nil {
		return "", false
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return ws.Signal().String(), true
}

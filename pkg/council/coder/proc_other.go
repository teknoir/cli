//go:build !unix

package coder

import (
	"os"
	"os/exec"
)

// termSignal and killSignal are both os.Kill: a platform without process groups has no polite stop.
var (
	termSignal os.Signal = os.Kill
	killSignal os.Signal = os.Kill
)

// setProcessGroup does nothing: there are no process groups to join.
func setProcessGroup(*exec.Cmd) {}

// signalGroup stops the process at pid; the children it started are not reached.
func signalGroup(pid int, sig os.Signal) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(sig); err != nil {
		return p.Kill()
	}
	return nil
}

// killGroup does nothing: there is no group to end, and the process is already gone.
func killGroup(int) {}

// signalOf is always false: this platform does not report a killing signal.
func signalOf(*os.ProcessState) (string, bool) { return "", false }

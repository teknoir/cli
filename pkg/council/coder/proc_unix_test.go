//go:build unix

package coder

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// aliveWait is how long processAlive waits for a process it just killed to be gone: a kill lands
// as surely as a reaped process does, and the two are not the same instant.
const aliveWait = 2 * time.Second

// processAlive reports whether a pid is still a live process. A zombie counts as dead: the process
// is gone, and only its parent has not reaped it yet.
func processAlive(t *testing.T, pid string) bool {
	t.Helper()
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		t.Fatalf("pid %q is not a number", pid)
	}
	deadline := time.Now().Add(aliveWait)
	for {
		if !processRunning(n) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processRunning reports whether pid is a live process that is not a zombie.
func processRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, statErr := os.Stat("/proc/self/stat"); statErr == nil {
			// Linux, and the entry is gone: the process is.
			return false
		}
		// No /proc here: ask the kernel.
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	// The state is the field after the parenthesised command name, which may itself hold spaces.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		// Not the shape /proc/pid/stat has: it was read while the process was going, or this is
		// not that file. Either way it is not known to be dead.
		return true
	}
	return data[i+2] != 'Z'
}

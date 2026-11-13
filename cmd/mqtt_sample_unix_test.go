//go:build unix

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --out never follows a link: a dangling symlink is refused and its target is not created; a symlink
// to an existing empty file is refused and the file is untouched; a hard link to an empty file is
// refused.
func TestMqttSampleOutRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	open := func(out string) error {
		var o, e bytes.Buffer
		w, err := newMqttSampleWriter(&o, &e, mqttSampleFlags{out: out})
		if err == nil && w.f != nil {
			_ = w.f.Close()
		}
		return err
	}
	// The link branch itself, not the generic one: ELOOP prints as "too many levels of symbolic
	// links", so "symbolic link" alone would also match "exists and cannot be used".
	wantLinkRefusal := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil || exitCodeOf(err) != codeUsage ||
			!strings.Contains(err.Error(), "is a symbolic link: give a new file") ||
			strings.Contains(err.Error(), "cannot be used") {
			t.Errorf("%s: %v (exit %d), want the symbolic-link refusal", what, err, exitCodeOf(err))
		}
	}

	// Dangling: the target does not exist, and must not be made.
	target := filepath.Join(dir, "victim-profile")
	dangling := filepath.Join(dir, "cam1.jsonl")
	if err := os.Symlink(target, dangling); err != nil {
		t.Fatal(err)
	}
	wantLinkRefusal(t, "a dangling symlink", open(dangling))
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("the target of the dangling link was created: %v", err)
	}

	// A symlink to an existing empty file: refused, and the file keeps its mode.
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(empty, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "cam2.jsonl")
	if err := os.Symlink(empty, link); err != nil {
		t.Fatal(err)
	}
	wantLinkRefusal(t, "a symlink to an empty file", open(link))
	if st, err := os.Stat(empty); err != nil || st.Mode().Perm() != 0o644 || st.Size() != 0 {
		t.Errorf("the target of the symlink was touched: %v %v", st, err)
	}

	// A hard link to an empty file: refused.
	hard := filepath.Join(dir, "cam3.jsonl")
	if err := os.Link(empty, hard); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	if err := open(hard); err == nil {
		t.Error("a hard link to an empty file was accepted")
	}
	if st, err := os.Stat(empty); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("the file behind the hard link was touched: %v %v", st, err)
	}

	// A plain new file and a plain empty one still work.
	if err := open(filepath.Join(dir, "new.jsonl")); err != nil {
		t.Errorf("a new file: %v", err)
	}
}

// --out on an existing directory is a usage error that does not talk of a symbolic link.
func TestMqttSampleOutRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	var o, e bytes.Buffer
	w, err := newMqttSampleWriter(&o, &e, mqttSampleFlags{out: dir})
	if err == nil {
		_ = w.f.Close()
		t.Fatal("a directory was accepted")
	}
	if exitCodeOf(err) != codeUsage || strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("%v (exit %d), want a usage error that names no link", err, exitCodeOf(err))
	}
}

// --out on a FIFO is refused at once, with or without a reader: the open never waits on it.
func TestMqttSampleOutRefusesAFIFO(t *testing.T) {
	for _, reader := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "fifo")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		if reader {
			r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
		}
		done := make(chan error, 1)
		go func() {
			var o, e bytes.Buffer
			w, err := newMqttSampleWriter(&o, &e, mqttSampleFlags{out: path})
			if err == nil && w.f != nil {
				_ = w.f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Errorf("reader %v: %v (exit %d), want a usage refusal", reader, err, exitCodeOf(err))
			}
		case <-time.After(2 * time.Second):
			// Unblock the open so that the goroutine ends.
			if r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
				defer func() { _ = r.Close() }()
			}
			t.Fatalf("reader %v: the open of a FIFO still waits after 2s", reader)
		}
	}
}

//go:build unix

package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A capture dir that is a symbolic link is never swept: neither the sweep of a new store nor
// sweepAll removes anything in the link's target, old or fresh.
func TestSweepSkipsASymlinkedDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(target, "cap-0123456789ab.jsonl")
	fresh := filepath.Join(target, "cap-ba9876543210.jsonl")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * mcpCaptureTTL)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	st := newCaptureStore(link)
	st.sweepAll()
	for _, f := range []string{old, fresh} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was removed through the link: %v", filepath.Base(f), err)
		}
	}
}

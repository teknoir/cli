//go:build unix

package council

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// kitGuidePath makes skills/<name>/ under a new kit dir and returns the dir and the SKILL.md path.
func kitGuidePath(t *testing.T, name string) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", name), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "skills", name, "SKILL.md")
}

// A FIFO where a guide's SKILL.md should be is not read (reading it would block the daemon until a
// writer came): the embedded guide is served, and the book is the embedded kit's.
func TestKitFSSkipsAFIFO(t *testing.T) {
	dir, file := kitGuidePath(t, "teknoir-platform")
	if err := syscall.Mkfifo(file, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	type result struct {
		names  []string
		digest string
	}
	done := make(chan result, 1)
	go func() {
		b := newSkillBook(KitFS(dir))
		done <- result{b.names(), b.digest([]string{"teknoir-platform"})}
	}()
	select {
	case r := <-done:
		if !equal(r.names, skillNames) {
			t.Errorf("names() = %v, want the embedded kit's %v", r.names, skillNames)
		}
		if want := newSkillBook(KitFS("")).digest([]string{"teknoir-platform"}); r.digest != want {
			t.Errorf("digest = %q, want the embedded guide's %q", r.digest, want)
		}
	case <-time.After(2 * time.Second):
		// Unblock the reader so the test binary can exit, then fail.
		if f, err := os.OpenFile(file, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("reading the kit blocked on a FIFO")
	}
}

// A SKILL.md larger than maxKitFile is not read: the embedded guide is served.
func TestKitFSSkipsAHugeGuide(t *testing.T) {
	dir, file := kitGuidePath(t, "teknoir-platform")
	if err := os.WriteFile(file, []byte("---\ndescription: HUGE\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(file, 2<<20); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(KitFS(dir), "skills/teknoir-platform/SKILL.md")
	if err != nil {
		t.Fatalf("read the platform guide: %v", err)
	}
	if string(data) != embeddedGuide(t, "teknoir-platform") {
		t.Error("a 2 MiB SKILL.md was served instead of the embedded guide")
	}
}

// A SKILL.md that is a symlink to a regular file is read through the link, from the dir.
func TestKitFSFollowsASymlinkedGuideFile(t *testing.T) {
	dir, file := kitGuidePath(t, "teknoir-platform")
	target := filepath.Join(dir, "platform.md")
	if err := os.WriteFile(target, []byte("---\nname: teknoir-platform\ndescription: LINKED FILE\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Skip(err)
	}
	if got := newSkillBook(KitFS(dir)).digest([]string{"teknoir-platform"}); got != "teknoir-platform: LINKED FILE" {
		t.Errorf("digest = %q, want the linked file's", got)
	}
}

// A guide's description reaches the digest cut to maxSkillDescription bytes and an ellipsis.
func TestSkillBookCutsALongDescription(t *testing.T) {
	dir, file := kitGuidePath(t, "teknoir-platform")
	long := strings.Repeat("d", 4<<10)
	if err := os.WriteFile(file, []byte("---\nname: teknoir-platform\ndescription: "+long+"\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := newSkillBook(KitFS(dir)).digest([]string{"teknoir-platform"})
	if max := len("teknoir-platform") + len(": ") + maxSkillDescription + len("…"); len(got) > max {
		t.Errorf("the digest line is %d bytes, want at most %d", len(got), max)
	}
	if !strings.HasPrefix(got, "teknoir-platform: ddd") || !strings.HasSuffix(got, "…") {
		t.Errorf("digest = %.80q…, want the description cut with an ellipsis", got)
	}
}

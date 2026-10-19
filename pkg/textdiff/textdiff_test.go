package textdiff

import (
	"fmt"
	"strings"
	"testing"
)

func TestEqualIsEmpty(t *testing.T) {
	if got := Unified("a", "b", "x: 1\n", "x: 1\n"); got != "" {
		t.Errorf("got %q", got)
	}
	if got := Unified("a", "b", "", ""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestChangedLine(t *testing.T) {
	a := "image:\n  repository: ghcr.io/teknoir/notebook\n  tag: main\nreplicas: 1\n"
	b := "image:\n  repository: ghcr.io/teknoir/notebook\n  tag: pr-123\nreplicas: 1\n"
	want := "--- git\n+++ effective\n@@ -1,4 +1,4 @@\n image:\n   repository: ghcr.io/teknoir/notebook\n-  tag: main\n+  tag: pr-123\n replicas: 1\n"
	if got := Unified("git", "effective", a, b); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddedAndRemoved(t *testing.T) {
	got := Unified("a", "b", "", "k: v\n")
	if !strings.Contains(got, "@@ -1,0 +1,1 @@") || !strings.Contains(got, "+k: v") {
		t.Errorf("added: %s", got)
	}
	got = Unified("a", "b", "k: v\n", "")
	if !strings.Contains(got, "-k: v") {
		t.Errorf("removed: %s", got)
	}
}

func TestTwoHunksAreSeparated(t *testing.T) {
	var a []string
	for i := 0; i < 30; i++ {
		a = append(a, fmt.Sprintf("l%d", i))
	}
	b := append([]string{}, a...)
	b[2] = "X2"
	b[27] = "X27"
	got := Unified("a", "b", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	if strings.Count(got, "@@ ") != 2 || !strings.Contains(got, "@@ -1,6 +1,6 @@") || !strings.Contains(got, "@@ -25,6 +25,6 @@") {
		t.Errorf("want two hunks:\n%s", got)
	}
	if strings.Contains(got, "l12") {
		t.Errorf("the unchanged middle must not be printed:\n%s", got)
	}
}

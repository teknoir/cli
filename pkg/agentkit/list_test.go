package agentkit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListCleanAndBroken(t *testing.T) {
	o, home, _ := testOpts(t)
	o.MCP, o.Hooks = true, true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	res, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InSync() {
		t.Fatalf("a fresh install is not in sync: %+v", res.Roots)
	}
	if len(res.Roots) != 3 {
		t.Errorf("%d roots, want 3 (.claude/skills, .agents/skills, .claude/hooks): %+v", len(res.Roots), res.Roots)
	}
	// Every MCP registration is there.
	for _, m := range res.MCP {
		if !m.Registered {
			t.Errorf("%s (%s) not registered: %+v", m.Path, m.Agent, m)
		}
	}
	// A changed file makes it differ.
	skill := filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.InSync() {
		t.Error("a changed file still reports in sync")
	}
	var claude *RootStatus
	for i := range res.Roots {
		if res.Roots[i].Root == filepath.Join(home, ".claude", "skills") {
			claude = &res.Roots[i]
		}
	}
	if claude == nil || len(claude.Changed) != 1 || claude.Changed[0] != skillRel {
		t.Errorf("changed: %+v", claude)
	}
	// A missing file too.
	if err := os.Remove(filepath.Join(home, ".agents", "skills", filepath.FromSlash(skillRel))); err != nil {
		t.Fatal(err)
	}
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, r := range res.Roots {
		missing += len(r.Missing)
	}
	if missing != 1 {
		t.Errorf("missing %d, want 1: %+v", missing, res.Roots)
	}
	// Another kit version too.
	o.KitVersion = "0.2.0"
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Roots {
		if !r.VersionOutdated || r.EmbeddedVersion != "0.2.0" {
			t.Errorf("version: %+v", r)
		}
	}
	if res.InSync() {
		t.Error("another kit version reports in sync")
	}
}

// A root without a manifest was never installed and is not reported.
func TestListNothingInstalled(t *testing.T) {
	o, _, _ := testOpts(t)
	res, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Roots) != 0 || !res.InSync() {
		t.Errorf("an empty home: %+v", res)
	}
	// A .claude.json without the entry is reported as not registered.
	home := o.Home
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.MCP {
		if m.Registered {
			t.Errorf("%s registered in an empty file", m.Path)
		}
	}
}

// The manifest of a root that is not the embedded kit's: a file it no longer has, and one that was
// never installed.
func TestListOutdatedAndExtra(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents = []Agent{AgentClaude}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".claude", "skills")
	m := readManifestFile(t, root)
	m.Files["teknoir-platform/OLD.md"] = hashOf("old\n")
	// A file of the kit the manifest does not know: it is missing from the install.
	delete(m.Files, skillRel)
	if err := writeManifest(t, root, m); err != nil {
		t.Fatal(err)
	}
	res, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Roots) != 1 {
		t.Fatalf("roots %+v", res.Roots)
	}
	r := res.Roots[0]
	if len(r.Extra) != 1 || r.Extra[0] != "teknoir-platform/OLD.md" {
		t.Errorf("extra %v", r.Extra)
	}
	if len(r.Changed) != 1 || r.Changed[0] != skillRel {
		t.Errorf("changed %v", r.Changed)
	}
}

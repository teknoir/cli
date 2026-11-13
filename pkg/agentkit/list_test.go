package agentkit

import (
	"os"
	"path/filepath"
	"strings"
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
	o.KitVersion = "0.3.0"
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Roots {
		if !r.VersionOutdated || r.EmbeddedVersion != "0.3.0" {
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

// A managed install is checked as a whole: on an empty directory every root is reported, with every kit
// file missing, so --check fails when nothing was installed.
func TestListManagedEmpty(t *testing.T) {
	o, _, _ := testOpts(t)
	o.Scope, o.ManagedDir = ScopeManaged, t.TempDir()
	res, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Roots) != 2 {
		t.Fatalf("%d roots, want the skills and the hooks root: %+v", len(res.Roots), res.Roots)
	}
	want := map[string]int{
		filepath.Join(o.ManagedDir, "skills"): len(mustKitFiles(t, o, kindSkills)),
		filepath.Join(o.ManagedDir, "hooks"):  len(mustKitFiles(t, o, kindHooks)),
	}
	for _, r := range res.Roots {
		n, ok := want[r.Root]
		if !ok {
			t.Errorf("unexpected root %+v", r)
			continue
		}
		if len(r.Missing) != n || len(r.Changed) != 0 || len(r.Extra) != 0 || len(r.Outdated) != 0 {
			t.Errorf("%s: missing %v changed %v extra %v outdated %v, want %d missing", r.Root, r.Missing, r.Changed, r.Extra, r.Outdated, n)
		}
		if r.KitVersion != "" || r.VersionOutdated {
			t.Errorf("%s: kit %q outdated %v, want none", r.Root, r.KitVersion, r.VersionOutdated)
		}
		if r.InSync() {
			t.Errorf("%s reports in sync", r.Root)
		}
	}
	if res.InSync() {
		t.Error("an empty managed directory reports in sync")
	}
	// A managed install with the guard removed: the hooks root misses it, and it is not in sync.
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(o.ManagedDir, "hooks", guardName)
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	res, err = List(o)
	if err != nil {
		t.Fatal(err)
	}
	var hooks *RootStatus
	for i := range res.Roots {
		if res.Roots[i].Root == filepath.Join(o.ManagedDir, "hooks") {
			hooks = &res.Roots[i]
		}
	}
	if hooks == nil || len(hooks.Missing) != 1 || hooks.Missing[0] != guardName || res.InSync() {
		t.Errorf("the missing guard: %+v", res.Roots)
	}
}

// mustKitFiles is the kit's files of a root kind, for counting.
func mustKitFiles(t *testing.T, o *Options, kind string) map[string][]byte {
	t.Helper()
	p := newOp(o)
	files, err := p.kitFiles(kind)
	if err != nil {
		t.Fatal(err)
	}
	return files
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

// list says why an older tnctl's Codex entry is not registered, and the command that updates it in
// the scope listed.
func TestListCodexOlderKitEntry(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		fix   string
	}{
		{ScopeUser, "run tnctl agent install --agent codex --mcp --force"},
		{ScopeProject, "run tnctl agent install --project --agent codex --mcp --force"},
	} {
		o, home, project := testOpts(t)
		o.Agents = []Agent{AgentCodex}
		o.Scope = tc.scope
		o.MCP = true
		path := filepath.Join(home, ".codex", "config.toml")
		if tc.scope == ScopeProject {
			path = filepath.Join(project, ".codex", "config.toml")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := List(o)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MCP) != 1 || res.MCP[0].Registered || !strings.HasSuffix(res.MCP[0].Note, tc.fix) {
			t.Errorf("%s: mcp %+v, want a note ending %q", tc.scope, res.MCP, tc.fix)
		}
	}
}

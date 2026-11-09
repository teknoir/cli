package agentkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	kit "github.com/teknoir/cli/agentkit"
)

// testOpts is an install of the embedded kit into a home and a project of the test's own.
func testOpts(t *testing.T) (o *Options, home, project string) {
	t.Helper()
	home, project = t.TempDir(), t.TempDir()
	return &Options{
		FS: kit.FS, KitVersion: kit.Version, TnctlVersion: "test",
		Home: home, ProjectDir: project,
		Agents: []Agent{AgentClaude, AgentCodex, AgentOpencode},
	}, home, project
}

// readTree is every path below root with its mode and content, for before/after comparisons.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[rel] = fmt.Sprintf("%04o/", info.Mode().Perm())
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%04o:%s", info.Mode().Perm(), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readManifestFile(t *testing.T, root string) manifest {
	t.Helper()
	m, err := readManifest(filepath.Join(root, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	return *m
}

func actionsOf(res *Result) []string {
	var out []string
	for _, a := range res.Actions {
		out = append(out, a.Action+" "+filepath.Base(filepath.Dir(a.Path))+"/"+filepath.Base(a.Path))
	}
	return out
}

const skillRel = "teknoir-platform/SKILL.md"

// kitSkill is the embedded SKILL.md.
func kitSkill(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(kit.FS, "skills/"+skillRel)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallAgentsAndScopes(t *testing.T) {
	tests := []struct {
		name   string
		scope  Scope
		agents []Agent
		want   []string // skill roots, with the agent label recorded
	}{
		{"claude user", ScopeUser, []Agent{AgentClaude}, []string{"~/.claude/skills"}},
		{"codex user", ScopeUser, []Agent{AgentCodex}, []string{"~/.agents/skills"}},
		{"opencode user", ScopeUser, []Agent{AgentOpencode}, []string{"~/.agents/skills"}},
		{"all user", ScopeUser, []Agent{AgentClaude, AgentCodex, AgentOpencode}, []string{"~/.claude/skills", "~/.agents/skills"}},
		{"claude project", ScopeProject, []Agent{AgentClaude}, []string{"<project>/.claude/skills"}},
		{"codex project", ScopeProject, []Agent{AgentCodex}, []string{"<project>/.agents/skills"}},
		{"all project", ScopeProject, []Agent{AgentClaude, AgentCodex, AgentOpencode}, []string{"<project>/.claude/skills", "<project>/.agents/skills"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, home, project := testOpts(t)
			o.Scope, o.Agents = tc.scope, tc.agents
			res, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			var roots []string
			for _, a := range res.Actions {
				if a.Action != ActionWrite {
					continue
				}
				dir := filepath.Dir(a.Path)
				if filepath.Base(dir) == "teknoir-platform" {
					dir = filepath.Dir(dir)
				}
				if len(roots) == 0 || roots[len(roots)-1] != dir {
					roots = append(roots, dir)
				}
			}
			if len(roots) != len(tc.want) {
				t.Fatalf("roots %v, want %v", roots, tc.want)
			}
			for i, w := range tc.want {
				want := strings.ReplaceAll(w, "~", home)
				want = strings.ReplaceAll(want, "<project>", project)
				if roots[i] != want {
					t.Errorf("root %d = %s, want %s", i, roots[i], want)
				}
				// The file, and its mode.
				p := filepath.Join(want, skillRel)
				st, err := os.Stat(p)
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				if st.Mode().Perm() != 0o644 {
					t.Errorf("%s mode %04o, want 0644", p, st.Mode().Perm())
				}
				if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0o755 {
					t.Errorf("%s mode: %v %v", filepath.Dir(p), st, err)
				}
				// The manifest: the kit version, the tnctl, and the file's hash.
				m := readManifestFile(t, want)
				if m.KitVersion != kit.Version || m.Tnctl != "test" {
					t.Errorf("%s manifest %+v", want, m)
				}
				if got, want := m.Files[skillRel], hashOf(readFile(t, p)); got != want {
					t.Errorf("manifest hash %s, want %s", got, want)
				}
			}
			// Nothing was installed outside the roots.
			if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
				t.Error("install without --hooks wrote a settings file")
			}
			if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
				t.Error("install without --mcp registered the MCP server")
			}
			// A second install changes nothing.
			before := readTree(t, home)
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range res.Actions {
				if a.Action != ActionKeep {
					t.Errorf("re-run: %s %s", a.Action, a.Path)
				}
			}
			if after := readTree(t, home); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Error("re-run changed the tree")
			}
		})
	}
}

// codex and opencode share one root, and it says so in the manifest.
func TestInstallSharedAgentsRoot(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents = []Agent{AgentCodex, AgentOpencode}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	agents := kitRootAgents(t, o)
	if len(agents) != 1 || agents[0] != "codex,opencode" {
		t.Errorf("root agents %v, want [codex,opencode]", agents)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("a claude directory was written for codex,opencode")
	}
}

// kitRootAgents reads the agent label each root's manifest records.
func kitRootAgents(t *testing.T, o *Options) []string {
	t.Helper()
	res, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res.Roots {
		out = append(out, r.Agent)
	}
	return out
}

// An older kit's file, unchanged since it was installed, is upgraded.
func TestInstallUpgrade(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents = []Agent{AgentClaude}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".claude", "skills")
	target := filepath.Join(root, filepath.FromSlash(skillRel))
	// What an older kit wrote, and recorded.
	old := "---\nname: teknoir-platform\n---\nold\n"
	if err := os.WriteFile(target, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m := readManifestFile(t, root)
	m.Files[skillRel] = hashOf(old)
	m.KitVersion = "0.0.9"
	if err := writeManifest(t, root, m); err != nil {
		t.Fatal(err)
	}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Action != ActionUpgrade {
		t.Fatalf("actions %v, want one upgrade", actionsOf(res))
	}
	if got, want := readFile(t, target), string(kitSkill(t)); got != want {
		t.Errorf("file not upgraded: %q", got)
	}
	if m := readManifestFile(t, root); m.KitVersion != kit.Version || m.Files[skillRel] != hashOf(readFile(t, target)) {
		t.Errorf("manifest not updated: %+v", m)
	}
}

func writeManifest(t *testing.T, root string, m manifest) error {
	t.Helper()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, manifestName), append(b, '\n'), 0o644)
}

// A file the user changed is refused, the exit path is non-zero, and the rest of the install runs.
func TestInstallRefusesChangedFile(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Scope = ScopeManaged
	o.ManagedDir = t.TempDir()
	o.Agents = []Agent{AgentClaude}
	// A kit of two skills, so one can be refused while the other is written.
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: a\n---\nx\n")},
		"skills/b/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: b\n---\ny\n")},
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(o.ManagedDir, "skills", "a", "SKILL.md")
	if err := os.WriteFile(a, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The kit gains a skill while a is refused: the new one is still written.
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: a\n---\nx\n")},
		"skills/b/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: b\n---\ny\n")},
		"skills/c/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: c\n---\nz\n")},
	}
	res, err := Install(o)
	re, ok := Refused(err)
	if !ok {
		t.Fatalf("err %v, want a RefusedError", err)
	}
	if len(re.Paths) != 1 || re.Paths[0] != a {
		t.Errorf("refused %v", re.Paths)
	}
	if got := readFile(t, a); got != "mine\n" {
		t.Errorf("the changed file was written: %q", got)
	}
	if !hasAction(res, ActionRefuse, a) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if !hasAction(res, ActionWrite, filepath.Join(o.ManagedDir, "skills", "c", "SKILL.md")) {
		t.Errorf("the rest of the install did not run: %v", actionsOf(res))
	}
	// --force overwrites it.
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, a); !strings.Contains(got, "name: a") {
		t.Errorf("--force did not overwrite: %q", got)
	}
	_ = home
}

func hasAction(res *Result, action, path string) bool {
	for _, a := range res.Actions {
		if a.Action == action && a.Path == path {
			return true
		}
	}
	return false
}

// A file the kit no longer has is deleted when it is unchanged, kept when the user changed it.
func TestInstallRemovedKitFile(t *testing.T) {
	o, _, _ := testOpts(t)
	o.Scope = ScopeManaged
	o.ManagedDir = t.TempDir()
	o.Agents = []Agent{AgentClaude}
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a\n")},
		"skills/b/SKILL.md": &fstest.MapFile{Data: []byte("b\n")},
		"skills/c/SKILL.md": &fstest.MapFile{Data: []byte("c\n")},
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	// The kit drops b, and the user changes c.
	o.FS = fstest.MapFS{"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a\n")}}
	c := filepath.Join(o.ManagedDir, "skills", "c", "SKILL.md")
	if err := os.WriteFile(c, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(o.ManagedDir, "skills", "b", "SKILL.md")
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Errorf("the unchanged removed file was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(b)); !os.IsNotExist(err) {
		t.Errorf("the empty skill directory was kept: %v", err)
	}
	if got := readFile(t, c); got != "mine\n" {
		t.Errorf("the changed removed file was touched: %q", got)
	}
	if !hasAction(res, ActionDelete, b) || !hasAction(res, ActionKeep, c) {
		t.Errorf("actions %v", actionsOf(res))
	}
}

// --dry-run writes nothing at all.
func TestInstallDryRun(t *testing.T) {
	o, home, project := testOpts(t)
	o.DryRun = true
	o.MCP, o.Hooks = true, true
	before := []map[string]string{readTree(t, home), readTree(t, project)}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	after := []map[string]string{readTree(t, home), readTree(t, project)}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Error("--dry-run wrote something")
	}
	if len(res.Actions) == 0 || !hasNotAction(res, ActionKeep) {
		t.Errorf("the plan %v", actionsOf(res))
	}
	// A project-scope run with --agents-md does not create AGENTS.md either.
	o2, _, project2 := testOpts(t)
	o2.Scope, o2.Agents, o2.AgentsMD, o2.DryRun = ScopeProject, []Agent{AgentClaude}, true, true
	if _, err := Install(o2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project2, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("--dry-run wrote AGENTS.md: %v", err)
	}
}

func hasNotAction(res *Result, action string) bool {
	for _, a := range res.Actions {
		if a.Action == action {
			return false
		}
	}
	return true
}

// --managed installs the skills and the guard, and touches nothing else; --hooks prints the settings
// entry instead of writing it.
func TestInstallManaged(t *testing.T) {
	o, home, _ := testOpts(t)
	dir := t.TempDir()
	o.Scope, o.ManagedDir, o.Hooks = ScopeManaged, dir, true
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "teknoir-platform", "SKILL.md")); err != nil {
		t.Error(err)
	}
	guard := filepath.Join(dir, "hooks", guardName)
	st, err := os.Stat(guard)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("guard mode %04o, want 0755", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", manifestName)); err != nil {
		t.Errorf("no manifest for the guard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Errorf("a managed install wrote settings.json: %v", err)
	}
	var snippet string
	for _, w := range res.Warnings {
		if strings.Contains(w, "PreToolUse") {
			snippet = w
		}
	}
	if !strings.Contains(snippet, `"command":"`+guard+`"`) {
		t.Errorf("the snippet names no guard: %v", res.Warnings)
	}
	// Nothing went to the home directory.
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("a managed install wrote below HOME: %v", err)
	}
}

func TestInstallValidate(t *testing.T) {
	o, _, _ := testOpts(t)
	o.Scope, o.MCP = ScopeManaged, true
	o.ManagedDir = t.TempDir()
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "--mcp") {
		t.Errorf("--managed --mcp: %v", err)
	}
	o, _, _ = testOpts(t)
	o.AgentsMD = true
	o.Scope = ScopeUser
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "--agents-md") {
		t.Errorf("--agents-md in user scope: %v", err)
	}
	if _, err := ResolveAgents([]string{"claude", "nope"}); err == nil {
		t.Error("an unknown agent was accepted")
	}
	got, err := ResolveAgents([]string{"claude,codex", "claude"})
	if err != nil || len(got) != 2 {
		t.Errorf("ResolveAgents: %v %v", got, err)
	}
	if got, err := ResolveAgents(nil); err != nil || len(got) != 3 {
		t.Errorf("ResolveAgents(all): %v %v", got, err)
	}
}

// Uninstall removes exactly what the kit wrote; a file the user changed is kept.
func TestUninstall(t *testing.T) {
	o, home, _ := testOpts(t)
	o.MCP, o.Hooks = true, true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(home, ".claude", "skills", "teknoir-platform", "SKILL.md")
	if err := os.WriteFile(skill, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.MCP, o.Hooks = true, true
	res, err := Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, skill); got != "mine\n" {
		t.Errorf("the changed file was deleted: %q", got)
	}
	for _, p := range []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "hooks", guardName),
		filepath.Join(home, ".claude", "hooks", manifestName),
		filepath.Join(home, ".agents", "skills", manifestName),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
	if !hasAction(res, ActionKeep, skill) {
		t.Errorf("actions %v", actionsOf(res))
	}
	// The MCP entries are gone and the settings entry with them.
	if got := readFile(t, filepath.Join(home, ".claude.json")); strings.Contains(got, "teknoir") {
		t.Errorf(".claude.json still names teknoir: %s", got)
	}
	if got := readFile(t, filepath.Join(home, ".claude", "settings.json")); strings.Contains(got, guardName) {
		t.Errorf("settings.json still has the guard: %s", got)
	}
}

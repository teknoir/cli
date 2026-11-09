package agentkit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// ---------- helpers ----------

// mustSymlink makes a symbolic link at link pointing to target.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// writeAt writes a file, creating its directory.
func writeAt(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// snapshotTree is every path below root with its mode, size, mtime and content: two snapshots of an
// untouched tree are equal, a write of any kind shows up.
func snapshotTree(t *testing.T, root string) map[string]string {
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
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "symlink:" + target
			return nil
		}
		if fi.IsDir() {
			out[rel] = fmt.Sprintf("%04o/", fi.Mode().Perm())
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%04o:%d:%d:%s", fi.Mode().Perm(), fi.Size(), fi.ModTime().UnixNano(), hashBytes(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// tamperManifest adds entries to the manifest of a root.
func tamperManifest(t *testing.T, root string, extra map[string]string) {
	t.Helper()
	m := readManifestFile(t, root)
	for k, v := range extra {
		m.Files[k] = v
	}
	if err := writeManifest(t, root, m); err != nil {
		t.Fatal(err)
	}
}

// managedOpts is a managed install of the embedded kit: the skills below DIR/skills and nothing else.
func managedOpts(t *testing.T) (o *Options, root string) {
	t.Helper()
	o, _, _ = testOpts(t)
	o.Scope, o.ManagedDir, o.Agents = ScopeManaged, t.TempDir(), []Agent{AgentClaude}
	return o, filepath.Join(o.ManagedDir, "skills")
}

// refusalOf runs f and requires a RefusedError naming why.
func refusalOf(t *testing.T, why string, f func() (*Result, error)) *RefusedError {
	t.Helper()
	_, err := f()
	re, ok := Refused(err)
	if !ok {
		t.Fatalf("err %v, want a refusal (%s)", err, why)
	}
	return re
}

// ---------- U1: manifest keys and the root ----------

// A manifest entry that is not a path inside its root is refused as a whole: install and uninstall
// touch nothing, and the file outside the root keeps its bytes.
func TestManifestBadKeysRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  func(base string) string
	}{
		{"parent", func(string) string { return "../victim.txt" }},
		{"climb", func(string) string { return "teknoir-platform/../../victim.txt" }},
		{"absolute", func(base string) string { return filepath.Join(base, "victim.txt") }},
	} {
		for _, op := range []string{"install", "uninstall"} {
			t.Run(tc.name+"/"+op, func(t *testing.T) {
				o, root := managedOpts(t)
				if _, err := Install(o); err != nil {
					t.Fatal(err)
				}
				victim := filepath.Join(o.ManagedDir, "victim.txt")
				writeAt(t, victim, "victim\n", 0o644)
				tamperManifest(t, root, map[string]string{tc.key(o.ManagedDir): hashOf("victim\n")})
				beforeRoot := snapshotTree(t, o.ManagedDir)
				refused := func() (*Result, error) {
					if op == "install" {
						return Install(o)
					}
					return Uninstall(o)
				}
				re := refusalOf(t, "a manifest key outside the root", refused)
				if !strings.Contains(re.Reason, "is not a path inside its root") {
					t.Errorf("the reason does not name the manifest key: %q", re.Reason)
				}
				if got := readFile(t, victim); got != "victim\n" {
					t.Errorf("the file outside the root was touched: %q", got)
				}
				if after := snapshotTree(t, o.ManagedDir); fmt.Sprint(after) != fmt.Sprint(beforeRoot) {
					t.Errorf("the refused %s changed the root:\nbefore %v\nafter  %v", op, beforeRoot, after)
				}
			})
		}
	}
}

// The same refusal in a dry run: it is reported, and nothing at all changes.
func TestManifestBadKeyDryRun(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(o.ManagedDir, "victim.txt")
	writeAt(t, victim, "victim\n", 0o644)
	tamperManifest(t, root, map[string]string{"../victim.txt": hashOf("victim\n")})
	o.DryRun = true
	before := snapshotTree(t, o.ManagedDir)
	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	if !hasAction(res, ActionRefuse, filepath.Join(root, manifestName)) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if after := snapshotTree(t, o.ManagedDir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("the dry run changed the tree")
	}
}

// A manifest entry that leads through a directory symlink out of the root is kept, never deleted: the
// kit no longer has it (the orphan path of an install) and on uninstall.
func TestManifestDirSymlinkEntryKept(t *testing.T) {
	for _, op := range []string{"install", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			o, root := managedOpts(t)
			if _, err := Install(o); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			writeAt(t, filepath.Join(outside, "SKILL.md"), "outside\n", 0o644)
			mustSymlink(t, outside, filepath.Join(root, "evil"))
			// The kit does not have evil/SKILL.md: it is an orphan of the manifest.
			tamperManifest(t, root, map[string]string{"evil/SKILL.md": hashOf("outside\n")})
			var res *Result
			var err error
			if op == "install" {
				res, err = Install(o)
			} else {
				res, err = Uninstall(o)
			}
			if err != nil {
				t.Fatalf("a kept symlink must not be an error: %v", err)
			}
			if got := readFile(t, filepath.Join(outside, "SKILL.md")); got != "outside\n" {
				t.Errorf("the file outside the root was touched: %q", got)
			}
			if !hasAction(res, ActionKeep, filepath.Join(root, "evil", "SKILL.md")) {
				t.Errorf("actions %v", actionsOf(res))
			}
			// A kept orphan stays in the manifest.
			if op == "install" {
				if _, ok := readManifestFile(t, root).Files["evil/SKILL.md"]; !ok {
					t.Error("the kept orphan left the manifest")
				}
			}
		})
	}
}

// A kit file that is itself a symlink out of the root is never written through: install refuses it and
// uninstall keeps it, and the link's target keeps its bytes.
func TestKitFileSymlinkRefused(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "target.md")
	writeAt(t, target, "outside\n", 0o644)
	link := filepath.Join(root, filepath.FromSlash(skillRel))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, target, link)

	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	if !hasAction(res, ActionRefuse, link) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := readFile(t, target); got != "outside\n" {
		t.Errorf("the link's target was written through: %q", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
	// --force does not replace it either.
	o.Force = true
	if _, err := Install(o); !hasRefusal(err) {
		t.Errorf("--force replaced a symlinked kit file: %v", err)
	}
	if got := readFile(t, target); got != "outside\n" {
		t.Errorf("--force wrote through the link: %q", got)
	}
	// Uninstall keeps it, and the target with it.
	res, err = Uninstall(o)
	if err != nil {
		t.Fatalf("uninstall of a symlinked kit file: %v", err)
	}
	if !hasAction(res, ActionKeep, link) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := readFile(t, target); got != "outside\n" {
		t.Errorf("uninstall wrote through the link: %q", got)
	}
}

// hasRefusal reports whether err is a RefusedError.
func hasRefusal(err error) bool {
	_, ok := Refused(err)
	return ok
}

// A kit skill directory that is a symlink out of the root: install refuses it, nothing goes into the
// link's target, and uninstall keeps it.
func TestSkillDirSymlinkRefused(t *testing.T) {
	o, root := managedOpts(t)
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(root, "teknoir-platform"))
	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	target := filepath.Join(root, "teknoir-platform", "SKILL.md")
	if !hasAction(res, ActionRefuse, target) {
		t.Errorf("actions %v", actionsOf(res))
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("install wrote into the link's target: %v", entries)
	}
	// The refusal recorded the kit's hash: a later uninstall keeps the entry.
	res, err = Uninstall(o)
	if err != nil {
		t.Fatalf("uninstall of a symlinked skill directory: %v", err)
	}
	if !hasAction(res, ActionKeep, target) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("uninstall touched the link's target: %v %v", entries, err)
	}
}

// A project that commits .claude or .agents/skills as a symlink is not installed into or uninstalled
// from at all.
func TestProjectRootSymlinkRefused(t *testing.T) {
	for _, op := range []string{"install", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			o, _, project := testOpts(t)
			o.Scope = ScopeProject
			outside := t.TempDir()
			outsideSkills := t.TempDir()
			mustSymlink(t, outside, filepath.Join(project, ".claude"))
			mustSymlink(t, outsideSkills, filepath.Join(project, ".agents", "skills"))
			before := snapshotTree(t, project)
			run := func() (*Result, error) {
				if op == "install" {
					return Install(o)
				}
				return Uninstall(o)
			}
			re := refusalOf(t, "a symlinked project root", run)
			for _, want := range []string{
				filepath.Join(project, ".claude") + " is a symlink",
				filepath.Join(project, ".agents", "skills") + " is a symlink",
			} {
				if !strings.Contains(re.Reason, want) {
					t.Errorf("the reason does not name %s: %q", want, re.Reason)
				}
			}
			if got := len(re.Paths); got != 2 {
				t.Errorf("refused %v, want the two symlinked roots", re.Paths)
			}
			if after := snapshotTree(t, project); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("the tree changed:\nbefore %v\nafter  %v", before, after)
			}
			for _, dir := range []string{outside, outsideSkills} {
				if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
					t.Errorf("%s was written to: %v %v", dir, entries, err)
				}
			}
		})
	}
}

// The same for a dry run: a symlinked project root is still refused.
func TestProjectRootSymlinkDryRun(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.DryRun = ScopeProject, true
	mustSymlink(t, t.TempDir(), filepath.Join(project, ".claude"))
	refusalOf(t, "a symlinked project root", func() (*Result, error) { return Install(o) })
	if _, err := os.Lstat(filepath.Join(project, ".agents")); !os.IsNotExist(err) {
		t.Errorf("the dry run created .agents: %v", err)
	}
}

// agent list reports a symlinked project root as its problem, and reads nothing below it.
func TestListProjectRootSymlink(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope = ScopeProject
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(project, ".claude"))
	// Something below the link that looks installed: it must not be read.
	writeAt(t, filepath.Join(outside, "skills", manifestName), `{"kitVersion":"0.1.0","files":{}}`, 0o644)
	res, err := List(o)
	if err != nil {
		t.Fatalf("list of a symlinked project root: %v", err)
	}
	var found *RootStatus
	for i := range res.Roots {
		if res.Roots[i].Root == filepath.Join(project, ".claude") {
			found = &res.Roots[i]
		} else if atOrBelow(res.Roots[i].Root, filepath.Join(project, ".claude")) {
			t.Errorf("list read below the link: %+v", res.Roots[i])
		}
	}
	if found == nil || !strings.Contains(found.Problem, "is a symlink") {
		t.Fatalf("the symlinked root is not reported: %+v", res.Roots)
	}
	if found.InSync() || res.InSync() {
		t.Error("a symlinked root reports in sync")
	}
}

// agent list reports a manifest with a bad key as the root's problem, and does not fail.
func TestListManifestBadKey(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	tamperManifest(t, root, map[string]string{"../x": hashOf("x\n")})
	res, err := List(o)
	if err != nil {
		t.Fatalf("list with a bad manifest key: %v", err)
	}
	if len(res.Roots) != 1 || !strings.Contains(res.Roots[0].Problem, "is not a path inside its root") {
		t.Fatalf("roots %+v", res.Roots)
	}
	if res.InSync() {
		t.Error("a refused manifest reports in sync")
	}
}

// isBelow counts a directory named "..foo" as below, and only ".." itself (or "../") as a climb out.
func TestIsBelowDotDotFoo(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "r")
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join(root, "..foo"), true},
		{filepath.Join(root, "..foo", "x"), true},
		{filepath.Join(root, "a", "..foo", "x"), true},
		{filepath.Join(root, ".."), false},
		{filepath.Join(root, "..", "x"), false},
		{root, false},
	} {
		if got := isBelow(tc.path, root); got != tc.want {
			t.Errorf("isBelow(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// ---------- U2 and U14: the Codex TOML ----------

// codexConfig is one Codex config of the test's own, and the install options for it.
func codexConfig(t *testing.T, body string) (*Options, string) {
	t.Helper()
	o, _, project := mcpOpts(t, AgentCodex, ScopeProject)
	path := filepath.Join(project, ".codex", "config.toml")
	writeAt(t, path, body, 0o644)
	return o, path
}

const codexOurs = `model = "gpt-5"

[mcp_servers.teknoir]
command = "tnctl"
args = ["mcp"]
`

// An array table right after ours ends it: uninstall leaves it, and so does --force.
func TestCodexArrayTableAfterOurs(t *testing.T) {
	body := codexOurs + `
[[skills.config]]
path = "/a"

[[skills.config]]
path = "/b"
`
	o, path := codexConfig(t, body)
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/uninstall.array.golden.toml", readFile(t, path))

	o, path = codexConfig(t, strings.Replace(body, `command = "tnctl"`, `command = "/other/tnctl"`, 1))
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/force.array.golden.toml", readFile(t, path))
}

// [[hooks.PreToolUse]] after ours ends it, and uninstall leaves both blocks.
func TestCodexHooksArrayTableAfterOurs(t *testing.T) {
	body := codexOurs + `
[[hooks.PreToolUse]]
matcher = "Bash"
command = "/usr/local/bin/check"
`
	o, path := codexConfig(t, body)
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/uninstall.hooks.golden.toml", readFile(t, path))
}

// The comment lines above the next table stay with that table: uninstall keeps them, --force keeps
// them above it.
func TestCodexCommentAboveNextTable(t *testing.T) {
	body := codexOurs + `
# Context7: docs lookup, added by hand.
[mcp_servers.context7]
command = "npx"
args = ["-y", "@upstash/context7-mcp"]
`
	o, path := codexConfig(t, body)
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/uninstall.comment.golden.toml", readFile(t, path))

	o, path = codexConfig(t, strings.Replace(body, `command = "tnctl"`, `command = "/other/tnctl"`, 1))
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/force.comment.golden.toml", readFile(t, path))
}

// ["mcp_servers.teknoir"] is a quoted key of its own, not our table: it is left alone.
func TestCodexQuotedWholeKey(t *testing.T) {
	body := `model = "gpt-5"

["mcp_servers.teknoir"]
command = "not-ours"
`
	o, path := codexConfig(t, body)
	res, err := Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionSkip, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	golden(t, "codex/uninstall.quoted.golden.toml", readFile(t, path))

	o, path = codexConfig(t, body)
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	golden(t, "codex/force.quoted.golden.toml", readFile(t, path))
}

// An [mcp_servers] left empty by the uninstall goes too, and the rest of the file stays.
func TestCodexEmptyParentTable(t *testing.T) {
	o, path := codexConfig(t, "[mcp_servers]\n\n[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\n")
	res, err := Uninstall(o)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !hasAction(res, ActionDelete, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got, want := readFile(t, path), "[mcp_servers]\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A top-level inline mcp_servers table cannot take our [mcp_servers.teknoir]: the edit is refused
// before anything is written, and the file keeps its bytes and its mode.
func TestCodexInlineTableRefused(t *testing.T) {
	body := "model = \"gpt-5\"\nmcp_servers = { x = { command = \"y\" } }\n"
	o, path := codexConfig(t, body)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)
	res, err := Install(o)
	re, ok := Refused(err)
	if !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	if !strings.Contains(re.Reason, "the edit would not parse") {
		t.Errorf("the reason: %q", re.Reason)
	}
	if !hasAction(res, ActionRefuse, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := readFile(t, path); got != before {
		t.Errorf("the file was written:\n%q", got)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", st.Mode().Perm(), err)
	}
}

// The two refusals of a table without its own header (dotted keys under [mcp_servers]), each as a real
// run and as a dry run: register --force replaces a differing one, uninstall removes the kit's own.
func TestCodexDottedKeysRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		run  func(o *Options) (*Result, error)
		why  string
		o    func(o *Options)
	}{
		{"uninstall", "[mcp_servers]\nteknoir.command = \"tnctl\"\nteknoir.args = [\"mcp\"]\n", Uninstall, "remove it by hand", nil},
		{"force", "[mcp_servers]\nteknoir.command = \"/other/tnctl\"\nteknoir.args = [\"mcp\"]\n", Install, "replace it by hand",
			func(o *Options) { o.Force = true }},
	} {
		for _, dry := range []bool{false, true} {
			name := fmt.Sprintf("%s/dry=%v", tc.name, dry)
			t.Run(name, func(t *testing.T) {
				o, path := codexConfig(t, tc.body)
				if tc.o != nil {
					tc.o(o)
				}
				o.DryRun = dry
				res, err := tc.run(o)
				re, ok := Refused(err)
				if !ok {
					t.Fatalf("err %v, want a refusal", err)
				}
				if !strings.Contains(re.Reason, tc.why) {
					t.Errorf("the reason: %q, want %q", re.Reason, tc.why)
				}
				if !hasAction(res, ActionRefuse, path) {
					t.Errorf("actions %v", actionsOf(res))
				}
				if got := readFile(t, path); got != tc.body {
					t.Errorf("the file was written:\n%q", got)
				}
			})
		}
	}
}

// A dry run of a Codex refusal reports a refusal, and the file keeps its bytes, its mode and its mtime.
func TestCodexDryRunRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"inline":  "model = \"gpt-5\"\nmcp_servers = { x = { command = \"y\" } }\n",
		"dotted ": "[mcp_servers]\nteknoir.command = \"/other/tnctl\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			o, path := codexConfig(t, body)
			o.Force, o.DryRun = true, true
			before := snapshotTree(t, filepath.Dir(path))
			res, err := Install(o)
			if _, ok := Refused(err); !ok {
				t.Fatalf("err %v, want a refusal", err)
			}
			if !hasAction(res, ActionRefuse, path) {
				t.Errorf("a dry run does not report the refusal: %v", actionsOf(res))
			}
			if after := snapshotTree(t, filepath.Dir(path)); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Error("the dry run changed the file")
			}
		})
	}
}

// A multi-line string that holds "\n\n\n" keeps it: uninstall trims the seam only.
func TestCodexUninstallKeepsMultilineString(t *testing.T) {
	body := "developer_instructions = \"\"\"\nRule one.\n\n\nRule two after two blank lines.\n\"\"\"\n\n" + codexOurs[strings.Index(codexOurs, "[mcp_servers.teknoir]"):]
	o, path := codexConfig(t, body)
	res, err := Uninstall(o)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !hasAction(res, ActionDelete, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	got := readFile(t, path)
	if !strings.Contains(got, "Rule one.\n\n\nRule two after two blank lines.") || strings.Contains(got, "teknoir") {
		t.Errorf("the file:\n%q", got)
	}
}

// ---------- U3: symlinked config files ----------

// A symlinked settings.json and config.toml keep their link in user scope: the change goes to the
// link's target, for an install and for an uninstall.
func TestSymlinkedConfigUserScope(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.MCP, o.Hooks = []Agent{AgentClaude, AgentCodex}, true, true
	dotfiles := t.TempDir()
	settings := filepath.Join(dotfiles, "settings.json")
	config := filepath.Join(dotfiles, "codex.toml")
	writeAt(t, settings, "{\"model\":\"opus\"}\n", 0o644)
	writeAt(t, config, "model = \"gpt-5\"\n", 0o644)
	settingsLink := filepath.Join(home, ".claude", "settings.json")
	configLink := filepath.Join(home, ".codex", "config.toml")
	mustSymlink(t, settings, settingsLink)
	mustSymlink(t, config, configLink)

	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ link, target, want string }{
		{settingsLink, settings, "teknoir-guard"},
		{configLink, config, "mcp_servers.teknoir"},
	} {
		if fi, err := os.Lstat(tc.link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no longer a link: %v %v", tc.link, fi, err)
		}
		if !strings.Contains(readFile(t, tc.target), tc.want) {
			t.Errorf("the target %s did not get the entry:\n%s", tc.target, readFile(t, tc.target))
		}
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ link, target, want string }{
		{settingsLink, settings, "teknoir-guard"},
		{configLink, config, "mcp_servers.teknoir"},
	} {
		if fi, err := os.Lstat(tc.link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no longer a link after uninstall: %v %v", tc.link, fi, err)
		}
		if strings.Contains(readFile(t, tc.target), tc.want) {
			t.Errorf("the target %s kept the entry:\n%s", tc.target, readFile(t, tc.target))
		}
	}
}

// The same files in project scope are refused: tnctl never writes through a link in a repository.
func TestSymlinkedConfigProjectScope(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.MCP, o.Hooks = ScopeProject, []Agent{AgentClaude, AgentCodex}, true, true
	outside := t.TempDir()
	settings := filepath.Join(outside, "settings.json")
	config := filepath.Join(outside, "codex.toml")
	writeAt(t, settings, "{\"model\":\"opus\"}\n", 0o644)
	writeAt(t, config, "model = \"gpt-5\"\n", 0o644)
	settingsLink := filepath.Join(project, ".claude", "settings.json")
	configLink := filepath.Join(project, ".codex", "config.toml")
	mustSymlink(t, settings, settingsLink)
	mustSymlink(t, config, configLink)
	re := refusalOf(t, "a symlinked project config", func() (*Result, error) { return Install(o) })
	for _, want := range []string{settingsLink + " is a symlink", configLink + " is a symlink"} {
		if !strings.Contains(re.Reason, want) {
			t.Errorf("the reason does not name %s: %q", want, re.Reason)
		}
	}
	if got := readFile(t, settings); got != "{\"model\":\"opus\"}\n" {
		t.Errorf("the settings target was written: %q", got)
	}
	if got := readFile(t, config); got != "model = \"gpt-5\"\n" {
		t.Errorf("the codex target was written: %q", got)
	}
	for _, link := range []string{settingsLink, configLink} {
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no longer a link: %v %v", link, fi, err)
		}
	}
}

// A dangling symlink is refused, with the snippet, in user scope.
func TestSymlinkedConfigDangling(t *testing.T) {
	o, home, _ := testOpts(t)
	o.Agents, o.MCP = []Agent{AgentCodex}, true
	link := filepath.Join(home, ".codex", "config.toml")
	mustSymlink(t, filepath.Join(home, ".codex", "missing.toml"), link)
	re := refusalOf(t, "a dangling symlink", func() (*Result, error) { return Install(o) })
	if !strings.Contains(re.Reason, "is a dangling symlink") || !strings.Contains(re.Reason, tomlEntryBlock) {
		t.Errorf("the reason: %q", re.Reason)
	}
	if _, err := os.Lstat(filepath.Join(home, ".codex", "missing.toml")); !os.IsNotExist(err) {
		t.Errorf("the dangling target was created: %v", err)
	}
}

// A user-scope settings.json that links into a directory tnctl cannot write is refused, with the
// snippet, and nothing is written there.
func TestSymlinkedSettingsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a 0555 directory")
	}
	o, home, _ := testOpts(t)
	o.Agents, o.Hooks = []Agent{AgentClaude}, true
	ro := filepath.Join(t.TempDir(), "store")
	writeAt(t, filepath.Join(ro, "settings.json"), "{\"model\":\"opus\"}\n", 0o644)
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	link := filepath.Join(home, ".claude", "settings.json")
	mustSymlink(t, filepath.Join(ro, "settings.json"), link)
	re := refusalOf(t, "a read-only link target", func() (*Result, error) { return Install(o) })
	if !strings.Contains(re.Reason, "cannot write") || !strings.Contains(re.Reason, settingsSnippet(filepath.Join(home, ".claude", "hooks", guardName))) {
		t.Errorf("the reason: %q", re.Reason)
	}
	if got := readFile(t, filepath.Join(ro, "settings.json")); got != "{\"model\":\"opus\"}\n" {
		t.Errorf("the target was written: %q", got)
	}
}

// AGENTS.md linking to CLAUDE.md in the project: both edit CLAUDE.md and keep the link. A link out of
// the project is refused.
func TestAgentsMDSymlinkInProject(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
	target := filepath.Join(project, "CLAUDE.md")
	writeAt(t, target, "# Project\n", 0o644)
	link := filepath.Join(project, "AGENTS.md")
	mustSymlink(t, "CLAUDE.md", link)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("AGENTS.md is no longer a link: %v %v", fi, err)
	}
	if !strings.Contains(readFile(t, target), agentsMDBegin) {
		t.Errorf("CLAUDE.md did not get the section:\n%s", readFile(t, target))
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("AGENTS.md is no longer a link after uninstall: %v %v", fi, err)
	}
	if got := readFile(t, target); got != "# Project\n" {
		t.Errorf("CLAUDE.md after uninstall:\n%q", got)
	}
}

// A dangling link, and a link out of the project, are refused: nothing outside the project is written.
func TestAgentsMDSymlinkOutsideProject(t *testing.T) {
	for name, link := range map[string]string{
		"outside":  "outside.md",
		"dangling": "missing.md",
	} {
		t.Run(name, func(t *testing.T) {
			o, _, project := testOpts(t)
			o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
			outside := t.TempDir()
			writeAt(t, filepath.Join(outside, "outside.md"), "# Outside\n", 0o644)
			target := filepath.Join(project, link)
			if name == "outside" {
				target = filepath.Join(outside, "outside.md")
			}
			mustSymlink(t, target, filepath.Join(project, "AGENTS.md"))
			re := refusalOf(t, "an AGENTS.md link out of the project", func() (*Result, error) { return Install(o) })
			want := "outside the project"
			if name == "dangling" {
				want = "is a dangling symlink"
			}
			if !strings.Contains(re.Reason, want) || !strings.Contains(re.Reason, agentsMDBegin) {
				t.Errorf("the reason: %q, want %q", re.Reason, want)
			}
			if got := readFile(t, filepath.Join(outside, "outside.md")); got != "# Outside\n" {
				t.Errorf("the file outside the project was written: %q", got)
			}
		})
	}
}

// ---------- U11: damaged AGENTS.md markers ----------

// A damaged marker set (a missing end marker, two begin markers, an end before the begin) is refused
// by install and by uninstall: the file is not written and the block is not appended beside it.
func TestAgentsMDDamagedMarkers(t *testing.T) {
	others := "# Mine\n\nNever deploy on Fridays.\n"
	for name, body := range map[string]string{
		"no end":     others + agentsMDBegin + "\nkit\n",
		"two begins": others + agentsMDBegin + "\nkit\n" + agentsMDBegin + "\nmore\n" + agentsMDEnd + "\n",
		"end before": agentsMDEnd + "\n" + agentsMDBegin + "\n",
		"end only":   others + agentsMDEnd + "\n",
	} {
		for _, op := range []string{"install", "uninstall"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				o, _, project := testOpts(t)
				o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
				path := filepath.Join(project, "AGENTS.md")
				writeAt(t, path, body, 0o644)
				run := func() (*Result, error) {
					if op == "install" {
						return Install(o)
					}
					return Uninstall(o)
				}
				re := refusalOf(t, "damaged markers", run)
				if !strings.Contains(re.Reason, "damaged teknoir section markers in AGENTS.md") {
					t.Errorf("the reason: %q", re.Reason)
				}
				if got := readFile(t, path); got != body {
					t.Errorf("the file was written:\n%q", got)
				}
				if n := strings.Count(readFile(t, path), agentsMDBegin); n != strings.Count(body, agentsMDBegin) {
					t.Errorf("%d begin markers, want %d", n, strings.Count(body, agentsMDBegin))
				}
			})
		}
	}
}

// ---------- U20 ----------

// After a refusal, a second install without --force refuses again: the file keeps the user's bytes and
// the manifest keeps the kit's hash.
func TestInstallRefusalRepeats(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(skillRel))
	kitHash := hashOf(string(kitSkill(t)))
	writeAt(t, target, "mine\n", 0o644)
	for i := 0; i < 2; i++ {
		res, err := Install(o)
		if _, ok := Refused(err); !ok {
			t.Fatalf("run %d: err %v, want a refusal", i+1, err)
		}
		if !hasAction(res, ActionRefuse, target) {
			t.Errorf("run %d: actions %v", i+1, actionsOf(res))
		}
		if got := readFile(t, target); got != "mine\n" {
			t.Errorf("run %d wrote the user's file: %q", i+1, got)
		}
		if got := readManifestFile(t, root).Files[skillRel]; got != kitHash {
			t.Errorf("run %d: the manifest holds %s, want the kit's %s", i+1, got, kitHash)
		}
	}
}

// A dry run over an existing install with an upgrade, a refusal and a dropped kit file changes nothing:
// every file keeps its bytes, its mode and its mtime.
func TestInstallDryRunOverExistingInstall(t *testing.T) {
	o, _ := managedOpts(t)
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a\n")},
		"skills/b/SKILL.md": &fstest.MapFile{Data: []byte("b\n")},
		"skills/c/SKILL.md": &fstest.MapFile{Data: []byte("c\n")},
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(o.ManagedDir, "skills")
	// a is what an older kit wrote (recorded, so it is an upgrade), c the user changed, b is dropped.
	writeAt(t, filepath.Join(root, "a", "SKILL.md"), "a-old\n", 0o644)
	m := readManifestFile(t, root)
	m.Files["a/SKILL.md"] = hashOf("a-old\n")
	if err := writeManifest(t, root, m); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(root, "c", "SKILL.md"), "mine\n", 0o644)
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a-new\n")},
		"skills/c/SKILL.md": &fstest.MapFile{Data: []byte("c\n")},
	}
	o.DryRun = true
	before := snapshotTree(t, o.ManagedDir)
	res, err := Install(o)
	if _, ok := Refused(err); !ok {
		t.Fatalf("err %v, want the refusal of c", err)
	}
	for _, want := range []struct{ action, rel string }{
		{ActionUpgrade, "a/SKILL.md"},
		{ActionRefuse, "c/SKILL.md"},
		{ActionDelete, "b/SKILL.md"},
	} {
		if !hasAction(res, want.action, filepath.Join(root, filepath.FromSlash(want.rel))) {
			t.Errorf("no %s of %s: %v", want.action, want.rel, actionsOf(res))
		}
	}
	if after := snapshotTree(t, o.ManagedDir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the dry run changed the tree:\nbefore %v\nafter  %v", before, after)
	}
}

// A changed orphan stays in the manifest after the run, and a later uninstall keeps it.
func TestChangedOrphanStaysInManifest(t *testing.T) {
	o, _ := managedOpts(t)
	o.FS = fstest.MapFS{
		"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a\n")},
		"skills/b/SKILL.md": &fstest.MapFile{Data: []byte("b\n")},
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(o.ManagedDir, "skills")
	b := filepath.Join(root, "b", "SKILL.md")
	writeAt(t, b, "mine\n", 0o644)
	o.FS = fstest.MapFS{"skills/a/SKILL.md": &fstest.MapFile{Data: []byte("a\n")}}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionKeep, b) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if got := readManifestFile(t, root).Files["b/SKILL.md"]; got != hashOf("b\n") {
		t.Errorf("the kept orphan left the manifest: %q", got)
	}
	res, err = Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(res, ActionKeep, b) {
		t.Errorf("uninstall actions %v", actionsOf(res))
	}
	if got := readFile(t, b); got != "mine\n" {
		t.Errorf("uninstall deleted the changed orphan: %q", got)
	}
}

// A manifest written by someone else with a key that is not a path inside the root leads to a
// refusal that names the manifest, and nothing is deleted anywhere near it.
func TestManifestBadKeyNamesTheManifest(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	tamperManifest(t, root, map[string]string{"../victim.txt": hashOf("victim\n")})
	res, err := Install(o)
	re, ok := Refused(err)
	if !ok {
		t.Fatalf("err %v, want a refusal", err)
	}
	mf := filepath.Join(root, manifestName)
	if !hasAction(res, ActionRefuse, mf) {
		t.Errorf("actions %v", actionsOf(res))
	}
	if !strings.Contains(re.Reason, mf) || !strings.Contains(re.Reason, `entry "../victim.txt"`) {
		t.Errorf("the reason: %q", re.Reason)
	}
	if len(re.Paths) != 1 || re.Paths[0] != mf {
		t.Errorf("refused %v, want the manifest", re.Paths)
	}
}

// The refusal error names the changed kit files first, then every reason.
func TestRefusalErrorReasons(t *testing.T) {
	o, root := managedOpts(t)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(root, filepath.FromSlash(skillRel)), "mine\n", 0o644)
	_, err := Install(o)
	re, ok := Refused(err)
	if !ok {
		t.Fatalf("err %v", err)
	}
	if !strings.HasPrefix(re.Reason, "1 file(s) changed since they were installed: use --force to overwrite; ") {
		t.Errorf("the reason: %q", re.Reason)
	}
	if !strings.Contains(re.Reason, filepath.Join(root, filepath.FromSlash(skillRel))) {
		t.Errorf("the reason does not name the file: %q", re.Reason)
	}
	if got := re.Error(); got != re.Reason {
		t.Errorf("Error() = %q, want the reason", got)
	}
}

// The embedded kit still installs and uninstalls to exactly itself: the new checks read the same files.
func TestInstallUninstallRoundTrip(t *testing.T) {
	o, home, project := testOpts(t)
	o.MCP, o.Hooks = true, true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Errorf(".claude/skills is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills")); !os.IsNotExist(err) {
		t.Errorf(".agents/skills is still there: %v", err)
	}
	_ = project
}

// A project that commits .claude/skills as a symlink out of the repository: install writes nothing
// into the link's target, and uninstall deletes nothing there.
func TestProjectSkillRootSymlinkTargetUntouched(t *testing.T) {
	for _, op := range []string{"install", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			o, _, project := testOpts(t)
			o.Scope = ScopeProject
			outside := t.TempDir()
			// What the link points at already holds a kit-like tree.
			writeAt(t, filepath.Join(outside, "teknoir-platform", "SKILL.md"), "outside\n", 0o644)
			writeAt(t, filepath.Join(outside, manifestName),
				`{"kitVersion":"0.1.0","tnctl":"x","files":{"teknoir-platform/SKILL.md":"`+hashOf("outside\n")+`"}}`, 0o644)
			mustSymlink(t, outside, filepath.Join(project, ".claude", "skills"))
			before := snapshotTree(t, outside)
			run := func() (*Result, error) {
				if op == "install" {
					return Install(o)
				}
				return Uninstall(o)
			}
			refusalOf(t, "a symlinked project root", run)
			if after := snapshotTree(t, outside); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("the link's target changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

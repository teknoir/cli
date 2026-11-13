package agentkit

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	kit "github.com/teknoir/cli/agentkit"
)

const agentsMDOthers = `# Project

This project is a Teknoir demo.

## Rules

- Keep commits small.
`

func kitFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(kit.FS, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// changedSnippetFS is the embedded skills and MCP files, with another AGENTS.snippet.md.
func changedSnippetFS(t *testing.T, snippet string) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	for _, name := range []string{"skills/teknoir-platform/SKILL.md", "hooks/teknoir-guard", ".mcp.json"} {
		m[name] = &fstest.MapFile{Data: kitFile(t, name)}
	}
	m["AGENTS.snippet.md"] = &fstest.MapFile{Data: []byte(snippet)}
	return m
}

func TestAgentsMDCreateAndReplace(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
	path := filepath.Join(project, "AGENTS.md")
	// A missing AGENTS.md is created.
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, agentsMDBegin+"\n") || !strings.HasSuffix(got, agentsMDEnd+"\n") {
		t.Errorf("AGENTS.md:\n%s", got)
	}
	snippet := strings.TrimRight(string(kitFile(t, "AGENTS.snippet.md")), "\n")
	if !strings.Contains(got, snippet) {
		t.Errorf("the snippet is not in AGENTS.md:\n%s", got)
	}
	// A re-run replaces only the block; the text outside it stays.
	text := agentsMDOthers + "\n" + got
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	if !strings.HasPrefix(after, agentsMDOthers+"\n") {
		t.Errorf("the text outside the markers changed:\n%s", after)
	}
	if n := strings.Count(after, agentsMDBegin); n != 1 {
		t.Errorf("%d begin markers, want 1:\n%s", n, after)
	}
	// A kit whose snippet changed rewrites the block in place.
	o.FS = changedSnippetFS(t, "a new snippet\n")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, path)
	if !strings.Contains(after, "a new snippet") || strings.Contains(after, snippet) {
		t.Errorf("the block was not replaced:\n%s", after)
	}
	if !strings.HasPrefix(after, agentsMDOthers+"\n") {
		t.Errorf("the text outside the markers changed:\n%s", after)
	}
}

// A second install over a section that is already the kit's writes nothing: the bytes and the mtime
// stay, and the run records a keep.
func TestAgentsMDSecondInstallKeeps(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
	path := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(path, []byte(agentsMDOthers), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A mtime far enough in the past that any rewrite shows (the file systems round to a second).
	past := st.ModTime().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("the second install changed AGENTS.md:\n%s", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(past) {
		t.Errorf("the second install wrote AGENTS.md: mtime %v, want %v", after.ModTime(), past)
	}
	var keep *Action
	for i := range res.Actions {
		if res.Actions[i].Path == path {
			keep = &res.Actions[i]
		}
	}
	if keep == nil || keep.Action != ActionKeep || keep.Note != "the teknoir section is up to date" {
		t.Errorf("actions %v, want one keep of AGENTS.md", actionsOf(res))
	}
}

// Uninstall removes the section and leaves the rest.
func TestAgentsMDUninstall(t *testing.T) {
	o, _, project := testOpts(t)
	o.Scope, o.Agents, o.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
	path := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(path, []byte(agentsMDOthers), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	res, err := Uninstall(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != agentsMDOthers {
		t.Errorf("AGENTS.md after uninstall:\n%q\nwant %q", got, agentsMDOthers)
	}
	if !hasAction(res, ActionDelete, path) {
		t.Errorf("actions %v", actionsOf(res))
	}
	// A file that held only our section is removed.
	o2, _, project2 := testOpts(t)
	o2.Scope, o2.Agents, o2.AgentsMD = ScopeProject, []Agent{AgentClaude}, true
	if _, err := Install(o2); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(o2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project2, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md with only our section was kept: %v", err)
	}
}

package agentkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The command lines of the kit's two MCP entry forms.
var (
	plainArgv   = []string{"tnctl", "mcp"}
	councilArgv = []string{"tnctl", "mcp", "--council"}
)

// userMCPConfig is where a user-scope install puts an agent's MCP config file.
func userMCPConfig(home string, a Agent) string {
	switch a {
	case AgentClaude:
		return filepath.Join(home, ".claude.json")
	case AgentCodex:
		return filepath.Join(home, ".codex", "config.toml")
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

// projectMCPConfig is where a project-scope install puts an agent's MCP config file.
func projectMCPConfig(project string, a Agent) string {
	switch a {
	case AgentClaude:
		return filepath.Join(project, ".mcp.json")
	case AgentCodex:
		return filepath.Join(project, ".codex", "config.toml")
	}
	return filepath.Join(project, "opencode.json")
}

// writeSeed writes text at path, creating its directory.
func writeSeed(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// anyStrings is a []string as a parsed TOML array holds it.
func anyStrings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// entryArgv is the command line of the teknoir MCP entry of an agent's config file: the command and its
// arguments, however the format keeps them. It is nil when the entry is not there.
func entryArgv(t *testing.T, path string, a Agent) []string {
	t.Helper()
	srv := map[string]any{}
	if a == AgentCodex {
		parsed, err := tomlParse(readFile(t, path))
		if err != nil {
			t.Fatal(err)
		}
		table, ok := tomlServer(parsed)
		if !ok {
			return nil
		}
		srv = table
	} else {
		top := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(readFile(t, path)), &top); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		container := []string{"mcpServers"}
		if a == AgentOpencode {
			container = []string{"mcp", "servers"}
		}
		raw, err := jsonLookup(top, container, mcpServerName)
		if err != nil {
			t.Fatal(err)
		}
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &srv); err != nil {
			t.Fatal(err)
		}
	}
	var argv []string
	switch cmd := srv["command"].(type) {
	case string:
		argv = append(argv, cmd)
		if args, ok := srv["args"].([]any); ok {
			for _, v := range args {
				s, _ := v.(string)
				argv = append(argv, s)
			}
		}
	case []any:
		for _, v := range cmd {
			s, _ := v.(string)
			argv = append(argv, s)
		}
	}
	return argv
}

// actionNote is the note of the first action of that action and path.
func actionNote(res *Result, action, path string) string {
	for _, a := range res.Actions {
		if a.Action == action && a.Path == path {
			return a.Note
		}
	}
	return ""
}

// --mcp --council writes the council form for every agent, into a config that holds other servers, other
// tables and comments; a second run is already registered, a dry run changes nothing, and an install
// switches between the two forms without --force, each way.
func TestMCPCouncilEntry(t *testing.T) {
	tests := []struct {
		agent   Agent
		seed    string
		council string // the file after a council install, byte for byte
	}{
		{
			agent: AgentClaude,
			seed:  `{"mcpServers":{"github":{"type":"http","url":"https://example.com/mcp"}},"numStartups":1}` + "\n",
			council: `{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "https://example.com/mcp"
    },
    "teknoir": {
      "type": "stdio",
      "command": "tnctl",
      "args": [
        "mcp",
        "--council"
      ]
    }
  },
  "numStartups": 1
}
`,
		},
		{
			agent: AgentCodex,
			seed: "# my codex config\nmodel = \"gpt-5\"\n\n[mcp_servers.github]\ncommand = \"npx\"\n" +
				"args = [\"-y\", \"server-github\"]\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n",
			council: "# my codex config\nmodel = \"gpt-5\"\n\n[mcp_servers.github]\ncommand = \"npx\"\n" +
				"args = [\"-y\", \"server-github\"]\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n\n" + tomlCouncilBlock,
		},
		{
			agent: AgentOpencode,
			seed: "{\n  \"theme\": \"tokyonight\",\n" +
				"  \"mcp\": {\"servers\": {\"other\": {\"type\": \"local\", \"command\": [\"other-mcp\", \"serve\"]}}}\n}\n",
			council: `{
  "mcp": {
    "servers": {
      "other": {
        "type": "local",
        "command": [
          "other-mcp",
          "serve"
        ]
      },
      "teknoir": {
        "type": "local",
        "command": [
          "tnctl",
          "mcp",
          "--council"
        ]
      }
    }
  },
  "theme": "tokyonight"
}
`,
		},
	}
	for _, tc := range tests {
		t.Run(string(tc.agent), func(t *testing.T) {
			o, home, _ := mcpOpts(t, tc.agent, ScopeUser)
			o.Council = true
			path := userMCPConfig(home, tc.agent)
			writeSeed(t, path, tc.seed)
			res, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if !hasAction(res, ActionRegister, path) {
				t.Errorf("actions %v", actionsOf(res))
			}
			if got := readFile(t, path); got != tc.council {
				t.Fatalf("the council install:\n%s\nwant:\n%s", got, tc.council)
			}
			if tc.agent == AgentCodex {
				// The table is the kit's, exactly: the command, its arguments and the environment.
				parsed, err := tomlParse(readFile(t, path))
				if err != nil {
					t.Fatal(err)
				}
				srv, _ := tomlServer(parsed)
				want := map[string]any{
					"command":  "tnctl",
					"args":     anyStrings([]string{"mcp", "--council"}),
					"env_vars": anyStrings(codexEnvVars),
				}
				if !reflect.DeepEqual(srv, want) {
					t.Errorf("the table %v, want %v", srv, want)
				}
			}
			// A second run is already registered and changes nothing.
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if !hasAction(res, ActionAlreadyRegister, path) || readFile(t, path) != tc.council {
				t.Errorf("a second council install: %v\n%s", actionsOf(res), readFile(t, path))
			}
			// A dry-run switch plans the read-only form and writes nothing.
			o.Council, o.DryRun = false, true
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != tc.council {
				t.Errorf("a dry-run switch wrote:\n%s", got)
			}
			if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "switched to the read-only tnctl mcp") {
				t.Errorf("the dry-run switch note %q", note)
			}
			o.DryRun = false
			// Back to the read-only form, without --force.
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryArgv(t, path, tc.agent); !reflect.DeepEqual(got, plainArgv) {
				t.Fatalf("entry %v, want %v:\n%s", got, plainArgv, readFile(t, path))
			}
			if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "switched to the read-only tnctl mcp") {
				t.Errorf("the switch note %q", note)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("the switch warned: %v", res.Warnings)
			}
			// And to the council form again.
			o.Council = true
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryArgv(t, path, tc.agent); !reflect.DeepEqual(got, councilArgv) {
				t.Fatalf("entry %v, want %v:\n%s", got, councilArgv, readFile(t, path))
			}
			if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "switched to tnctl mcp --council") {
				t.Errorf("the switch note %q", note)
			}
		})
	}
	// A project-scope council install writes the project's own config file.
	t.Run("project", func(t *testing.T) {
		o, _, project := mcpOpts(t, AgentClaude, ScopeProject)
		o.Council = true
		if _, err := Install(o); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(project, ".mcp.json")
		if got := entryArgv(t, path, AgentClaude); !reflect.DeepEqual(got, councilArgv) {
			t.Errorf("entry %v, want %v:\n%s", got, councilArgv, readFile(t, path))
		}
	})
}

// An entry that is neither form still needs --force, also when --council asks for the council one; an
// entry of an older kit in the council form does too.
func TestMCPCouncilForeignEntryNeedsForce(t *testing.T) {
	for _, tc := range []struct {
		agent   Agent
		foreign string
	}{
		{AgentClaude, `{"mcpServers":{"teknoir":{"type":"stdio","command":"/other/tnctl","args":["mcp"]}}}` + "\n"},
		{AgentCodex, "[mcp_servers.teknoir]\ncommand = \"/other/tnctl\"\nargs = [\"mcp\"]\n"},
		{AgentOpencode, `{"mcp":{"servers":{"teknoir":{"type":"local","command":["/other/tnctl","mcp"]}}}}` + "\n"},
	} {
		t.Run(string(tc.agent), func(t *testing.T) {
			o, home, _ := mcpOpts(t, tc.agent, ScopeUser)
			o.Council = true
			path := userMCPConfig(home, tc.agent)
			writeSeed(t, path, tc.foreign)
			res, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != tc.foreign {
				t.Errorf("a foreign entry was replaced without --force:\n%s", got)
			}
			if !hasAction(res, ActionSkip, path) || len(res.Warnings) == 0 {
				t.Errorf("actions %v, warnings %v", actionsOf(res), res.Warnings)
			}
			o.Force = true
			res, err = Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryArgv(t, path, tc.agent); !reflect.DeepEqual(got, councilArgv) {
				t.Errorf("--force did not write the council entry: %v\n%s", got, readFile(t, path))
			}
			if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "replaced a differing one (--force)") {
				t.Errorf("the --force note %q", note)
			}
		})
	}
	// An older kit's entry with the council command is not the current one: the kit's env_vars are missing.
	t.Run("codex/legacy-council", func(t *testing.T) {
		o, home, _ := mcpOpts(t, AgentCodex, ScopeUser)
		o.Council = true
		path := userMCPConfig(home, AgentCodex)
		legacy := "model = \"gpt-5\"\n\n[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\", \"--council\"]\n"
		writeSeed(t, path, legacy)
		res, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != legacy {
			t.Errorf("the older entry was replaced without --force:\n%s", got)
		}
		if !hasAction(res, ActionSkip, path) || len(res.Warnings) == 0 {
			t.Errorf("actions %v, warnings %v", actionsOf(res), res.Warnings)
		}
		o.Force = true
		res, err = Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != "model = \"gpt-5\"\n\n"+tomlCouncilBlock {
			t.Errorf("--force did not replace the older entry:\n%s", got)
		}
		if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "replaced a differing one (--force)") {
			t.Errorf("the --force note %q", note)
		}
	})
}

// A section of the Codex config that reads as the kit's other form but carries a comment of the user's
// stays as it is without --force: switching must not drop the comment.
func TestMCPCouncilCodexCommentNeedsForce(t *testing.T) {
	o, home, _ := mcpOpts(t, AgentCodex, ScopeUser)
	o.Council = true
	path := userMCPConfig(home, AgentCodex)
	for _, tc := range []struct {
		name   string
		body   string
		forced string // the file after --force replaced the table
	}{
		{
			name: "after the header",
			body: "[mcp_servers.teknoir] # mine\ncommand = \"tnctl\"\nargs = [\"mcp\"]\nenv_vars = " + tomlEnvVars() + "\n",
		},
		{
			name: "between the keys",
			body: "[mcp_servers.teknoir]\ncommand = \"tnctl\"\n# why\nargs = [\"mcp\"]\nenv_vars = " + tomlEnvVars() + "\n",
		},
		{
			name: "at a line's end",
			body: "[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"] # mine\nenv_vars = " + tomlEnvVars() + "\n",
		},
		{
			// A comment below the table, after a blank line, belongs to the table's text: the switch
			// keeps it rather than decide that a person's note is not theirs.
			name:   "below the table",
			body:   "[mcp_servers.teknoir]\ncommand = \"tnctl\"\nargs = [\"mcp\"]\nenv_vars = " + tomlEnvVars() + "\n\n# note\n",
			forced: "model = \"gpt-5\"\n\n" + tomlCouncilBlock + "\n# note\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.forced == "" {
				tc.forced = "model = \"gpt-5\"\n\n" + tomlCouncilBlock
			}
			seed := "model = \"gpt-5\"\n\n" + tc.body
			writeSeed(t, path, seed)
			res, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != seed {
				t.Errorf("the switch dropped the comment:\n%s", got)
			}
			if !hasAction(res, ActionSkip, path) || len(res.Warnings) == 0 ||
				!strings.Contains(actionNote(res, ActionSkip, path), "differs: kept it (use --force to replace it)") {
				t.Errorf("actions %v, warnings %v", actionsOf(res), res.Warnings)
			}
			// --force replaces the table; a line of the file that is not the table stays.
			o.Force = true
			if _, err := Install(o); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != tc.forced {
				t.Errorf("--force did not replace the table:\n%s\nwant:\n%s", got, tc.forced)
			}
			o.Force = false
		})
	}
	// The block the kit wrote itself switches without --force, comments of the file's own aside.
	seed := "# my codex config\nmodel = \"gpt-5\"\n\n" + tomlEntryBlock
	writeSeed(t, path, seed)
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# my codex config\nmodel = \"gpt-5\"\n\n"+tomlCouncilBlock {
		t.Errorf("an untouched kit block was not switched:\n%s", got)
	}
	if note := actionNote(res, ActionRegister, path); !strings.Contains(note, "switched to tnctl mcp --council") {
		t.Errorf("the switch note %q", note)
	}
}

// Uninstall removes either form without a flag, and leaves everything else alone: another server
// survives, a second file of the agent is not touched, and a teknoir entry the user edited is kept.
func TestMCPUninstallBothForms(t *testing.T) {
	other := map[Agent]string{
		AgentClaude:   `{"mcpServers":{"github":{"type":"http","url":"https://example.com/mcp"}}}` + "\n",
		AgentCodex:    "model = \"gpt-5\"\n\n[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"-y\", \"server-github\"]\n",
		AgentOpencode: `{"theme":"tokyonight","mcp":{"servers":{"other":{"type":"local","command":["other-mcp","serve"]}}}}` + "\n",
	}
	// What the survivor of each file leaves behind, after the JSON writer's reformatting.
	survives := map[Agent]string{
		AgentClaude:   "https://example.com/mcp",
		AgentCodex:    "[mcp_servers.github]",
		AgentOpencode: "other-mcp",
	}
	handEdited := map[Agent]string{
		AgentClaude:   `{"mcpServers":{"teknoir":{"type":"stdio","command":"/other/tnctl","args":["mcp"]}}}` + "\n",
		AgentCodex:    "model = \"gpt-5\"\n\n[mcp_servers.teknoir]\ncommand = \"/other/tnctl\"\nargs = [\"mcp\"]\n",
		AgentOpencode: `{"mcp":{"servers":{"teknoir":{"type":"local","command":["/other/tnctl","mcp"]}}}}` + "\n",
	}
	for _, a := range []Agent{AgentClaude, AgentCodex, AgentOpencode} {
		for _, council := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/council=%v", a, council), func(t *testing.T) {
				o, home, project := mcpOpts(t, a, ScopeUser)
				o.Council = council
				path := userMCPConfig(home, a)
				writeSeed(t, path, other[a])
				if _, err := Install(o); err != nil {
					t.Fatal(err)
				}
				// A second file of the same agent holds a hand-edited entry: uninstall never touches it.
				second := projectMCPConfig(project, a)
				writeSeed(t, second, handEdited[a])
				// uninstall takes no --council: it removes whichever form is registered.
				o.Council = false
				res, err := Uninstall(o)
				if err != nil {
					t.Fatal(err)
				}
				if !hasAction(res, ActionDelete, path) {
					t.Errorf("actions %v", actionsOf(res))
				}
				if got := entryArgv(t, path, a); got != nil {
					t.Errorf("the entry survived: %v\n%s", got, readFile(t, path))
				}
				if got := readFile(t, path); !strings.Contains(got, survives[a]) {
					t.Errorf("the other server was lost:\n%s", got)
				}
				if got := readFile(t, second); got != handEdited[a] {
					t.Errorf("the second file was touched:\n%s", got)
				}
			})
		}
		t.Run(string(a)+"/kept", func(t *testing.T) {
			o, home, _ := mcpOpts(t, a, ScopeUser)
			path := userMCPConfig(home, a)
			writeSeed(t, path, handEdited[a])
			res, err := Uninstall(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != handEdited[a] {
				t.Errorf("a hand-edited entry was removed:\n%s", got)
			}
			if !hasAction(res, ActionSkip, path) || len(res.Warnings) == 0 {
				t.Errorf("actions %v, warnings %v", actionsOf(res), res.Warnings)
			}
		})
	}
}

// An OpenCode .jsonc with comments is never edited; a file that already holds the kit's other form is
// asked to replace that entry, a file without one to add it.
func TestMCPOpencodeJSONCCouncilRefusal(t *testing.T) {
	plain := "{\n  // my config\n  \"mcp\": {\"servers\": {\"teknoir\": {\"type\": \"local\", \"command\": [\"tnctl\", \"mcp\"]}}}\n}\n"
	empty := "{\n  // my config\n  \"mcp\": {\"servers\": {}}\n}\n"
	for _, tc := range []struct {
		name, seed, verb string
	}{
		{"with the plain entry", plain, "the file has comments: replace mcp.servers.teknoir with "},
		{"without an entry", empty, "the file has comments: add "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, project := mcpOpts(t, AgentOpencode, ScopeProject)
			o.Council = true
			path := filepath.Join(project, "opencode.jsonc")
			writeSeed(t, path, tc.seed)
			res, err := Install(o)
			if _, ok := Refused(err); !ok {
				t.Fatalf("err %v, want a refusal", err)
			}
			if got := readFile(t, path); got != tc.seed {
				t.Errorf("the file with comments was edited:\n%s", got)
			}
			joined := strings.Join(res.Warnings, "\n")
			if !strings.Contains(joined, tc.verb) || !strings.Contains(joined, `["tnctl","mcp","--council"]`) {
				t.Errorf("the warning reads:\n%s", joined)
			}
		})
	}
}

// list reports the form that is registered, and Registered for both.
func TestMCPListCouncil(t *testing.T) {
	for _, a := range []Agent{AgentClaude, AgentCodex, AgentOpencode} {
		t.Run(string(a), func(t *testing.T) {
			o, _, _ := mcpOpts(t, a, ScopeUser)
			o.Council = true
			if _, err := Install(o); err != nil {
				t.Fatal(err)
			}
			ls, err := List(o)
			if err != nil {
				t.Fatal(err)
			}
			if len(ls.MCP) != 1 || !ls.MCP[0].Registered || !ls.MCP[0].Council {
				t.Errorf("the council entry: %+v", ls.MCP)
			}
			o.Council = false
			if _, err := Install(o); err != nil {
				t.Fatal(err)
			}
			ls, err = List(o)
			if err != nil {
				t.Fatal(err)
			}
			if len(ls.MCP) != 1 || !ls.MCP[0].Registered || ls.MCP[0].Council {
				t.Errorf("the read-only entry: %+v", ls.MCP)
			}
		})
	}
}

// --council without --mcp is refused, and the managed scope still refuses --mcp itself.
func TestInstallCouncilNeedsMCP(t *testing.T) {
	o, _, _ := testOpts(t)
	o.Council = true
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "--council needs --mcp") {
		t.Errorf("--council without --mcp: %v", err)
	}
	o, _, _ = testOpts(t)
	o.Scope, o.ManagedDir, o.MCP, o.Council = ScopeManaged, t.TempDir(), true, true
	if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "needs --user or --project") {
		t.Errorf("--managed --mcp --council: %v", err)
	}
}

// A managed install of another agent alone has no hooks root: install writes the skills only, list
// checks that root and is in sync, and uninstall removes it. --hooks only warns. A directory an older
// install left a guard in keeps it: --agent opencode neither lists nor removes it.
func TestInstallManagedWithoutClaude(t *testing.T) {
	for _, agents := range [][]Agent{{AgentOpencode}, {AgentCodex}, {AgentCodex, AgentOpencode}} {
		t.Run(agentLabel(agents), func(t *testing.T) {
			o, home, _ := testOpts(t)
			dir := t.TempDir()
			o.Scope, o.ManagedDir, o.Agents, o.Hooks = ScopeManaged, dir, agents, true
			res, err := Install(o)
			if err != nil {
				t.Fatal(err)
			}
			skill := filepath.Join(dir, "skills", "teknoir-platform", "SKILL.md")
			if _, err := os.Stat(skill); err != nil {
				t.Error(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "hooks")); !os.IsNotExist(err) {
				t.Errorf("a managed install without Claude Code wrote the hooks root: %v", err)
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "hooks are installed for Claude Code only") {
				t.Errorf("warnings %v", res.Warnings)
			}
			o.Hooks = false
			ls, err := List(o)
			if err != nil {
				t.Fatal(err)
			}
			if len(ls.Roots) != 1 || ls.Roots[0].Root != filepath.Join(dir, "skills") || !ls.InSync() {
				t.Errorf("a fresh managed install of %s: %+v", agentLabel(agents), ls.Roots)
			}
			// A guard an older tnctl left in the directory: --agent without claude keeps it as it is.
			guard := filepath.Join(dir, "hooks", guardName)
			writeSeed(t, guard, "#!/bin/sh\n# the guard of an older install\n")
			before := readFile(t, guard)
			if _, err := Install(o); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, guard); got != before {
				t.Errorf("install touched the older guard:\n%s", got)
			}
			// A changed skill file fails the check, as `agent list --check` does.
			kit := readFile(t, skill)
			writeSeed(t, skill, "mine\n")
			ls, err = List(o)
			if err != nil {
				t.Fatal(err)
			}
			if ls.InSync() || !reflect.DeepEqual(ls.Roots[0].Changed, []string{filepath.Join("teknoir-platform", "SKILL.md")}) {
				t.Errorf("a changed kit file: %+v", ls.Roots)
			}
			writeSeed(t, skill, kit)
			if ls, err = List(o); err != nil || !ls.InSync() {
				t.Errorf("the restored install: %+v %v", ls, err)
			}
			if _, err := Uninstall(o); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
				t.Errorf("the skills root survived the uninstall: %v", err)
			}
			if got := readFile(t, guard); got != before {
				t.Errorf("uninstall touched the older guard:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
				t.Errorf("the uninstall touched the home directory: %v", err)
			}
		})
	}
}

// The default managed install still writes the guard, and with --hooks prints the settings entry to add
// by hand; an install for Claude Code and OpenCode writes it too.
func TestInstallManagedDefaultGuard(t *testing.T) {
	o, _, _ := testOpts(t)
	dir := t.TempDir()
	o.Scope, o.ManagedDir = ScopeManaged, dir
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(dir, "hooks", guardName)
	if _, err := os.Stat(guard); err != nil {
		t.Fatalf("the guard: %v", err)
	}
	ls, err := List(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.Roots) != 2 || !ls.InSync() {
		t.Errorf("roots %+v", ls.Roots)
	}
	// --hooks prints the entry the image adds to Claude Code's managed settings.
	o.Hooks = true
	res, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "managed-settings") || !strings.Contains(joined, guard) {
		t.Errorf("--hooks printed no settings entry for %s: %v", guard, res.Warnings)
	}
	if !strings.Contains(actionNote(res, ActionSkip, guard), "settings entry to add by hand") {
		t.Errorf("the entry is not an action: %v", actionsOf(res))
	}
	// Claude Code beside OpenCode still gets the guard.
	o, _, _ = testOpts(t)
	dir = t.TempDir()
	o.Scope, o.ManagedDir, o.Agents = ScopeManaged, dir, []Agent{AgentClaude, AgentOpencode}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", guardName)); err != nil {
		t.Errorf("an install for claude,opencode wrote no guard: %v", err)
	}
}

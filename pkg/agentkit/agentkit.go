// Package agentkit installs the Teknoir agent kit into the directories a coding agent reads: the Agent
// Skills, the guard hook and the registration of the read-only `tnctl mcp` server. It holds no cobra
// code, so the install rules are testable on their own and the commands in cmd/ only parse flags and
// print the result.
package agentkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Agent is a coding agent the kit installs for.
type Agent string

const (
	AgentClaude   Agent = "claude"
	AgentCodex    Agent = "codex"
	AgentOpencode Agent = "opencode"
)

// Scope is where the kit goes.
type Scope int

const (
	// ScopeUser installs below the home directory: ~/.claude, ~/.agents, ~/.codex, ~/.config/opencode.
	ScopeUser Scope = iota
	// ScopeProject installs below the current directory: .claude, .agents, .codex, opencode.json.
	ScopeProject
	// ScopeManaged installs only skills and the guard, below one directory (an image's home).
	ScopeManaged
)

func (s Scope) String() string {
	switch s {
	case ScopeProject:
		return "project"
	case ScopeManaged:
		return "managed"
	}
	return "user"
}

// ResolveAgents turns the --agent values into agents: "all", repeated names and comma-separated names
// ("codex,opencode" as one value) are accepted, in the order given and without duplicates.
func ResolveAgents(names []string) ([]Agent, error) {
	var out []Agent
	seen := map[Agent]bool{}
	add := func(a Agent) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	var vals []string
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			if p := strings.TrimSpace(part); p != "" {
				vals = append(vals, p)
			}
		}
	}
	if len(vals) == 0 {
		vals = []string{"all"}
	}
	for _, v := range vals {
		switch Agent(strings.ToLower(v)) {
		case "all":
			add(AgentClaude)
			add(AgentCodex)
			add(AgentOpencode)
		case AgentClaude:
			add(AgentClaude)
		case AgentCodex:
			add(AgentCodex)
		case AgentOpencode:
			add(AgentOpencode)
		default:
			return nil, fmt.Errorf("unknown agent %q: use claude, codex, opencode or all", v)
		}
	}
	return out, nil
}

// The action names of an install, a list and an uninstall. They are part of the -o json interface.
const (
	ActionWrite           = "write"
	ActionKeep            = "keep"
	ActionUpgrade         = "upgrade"
	ActionRefuse          = "refuse"
	ActionDelete          = "delete"
	ActionRegister        = "register"
	ActionAlreadyRegister = "already registered"
	ActionSkip            = "skip"
)

// Action is one thing an install or uninstall did, or (with DryRun) would do. Path is absolute.
type Action struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Agent  string `json:"agent,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Options is one install, list or uninstall.
type Options struct {
	// FS is the kit (agentkit.FS in tnctl); KitVersion is its version.
	FS         fs.FS
	KitVersion string
	// TnctlVersion is recorded in the manifest; it is the tnctl that installed the kit.
	TnctlVersion string
	// Home is the home directory of user scope (os.UserHomeDir in tnctl).
	Home string
	// ClaudeConfigDir is the .claude directory of user scope ($CLAUDE_CONFIG_DIR, else Home/.claude).
	ClaudeConfigDir string
	// ClaudeConfigFile is the Claude Code state file of user scope: $CLAUDE_CONFIG_DIR/.claude.json
	// when that variable is set, else Home/.claude.json.
	ClaudeConfigFile string
	// CodexHome is the Codex directory of user scope ($CODEX_HOME, else Home/.codex).
	CodexHome string
	// OpencodeConfigDir is the OpenCode directory of user scope ($XDG_CONFIG_HOME/opencode, else
	// Home/.config/opencode).
	OpencodeConfigDir string
	// ProjectDir is the directory of project scope (the current directory in tnctl).
	ProjectDir string
	// ManagedDir is the --managed directory; set when Scope is ScopeManaged.
	ManagedDir string

	Agents []Agent
	Scope  Scope

	MCP      bool // register the teknoir MCP server
	Hooks    bool // install the guard hook and its PreToolUse entry (Claude Code only)
	AgentsMD bool // add the AGENTS.md snippet (project scope only)
	DryRun   bool // print the plan, write nothing
	Force    bool // overwrite files the user changed, replace a differing MCP entry

	// lockTimeout is how long an edit of the user-scope Claude config waits for Claude Code's lock;
	// zero is the default. Tests shorten it.
	lockTimeout time.Duration
}

// Result is what an install, a list or an uninstall did.
type Result struct {
	Actions  []Action `json:"actions"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *Result) act(action, path, agent, note string) {
	r.Actions = append(r.Actions, Action{Action: action, Path: filepath.Clean(path), Agent: agent, Note: note})
}

func (r *Result) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// RefusedError reports the files an install refused to overwrite, and a file it could not edit at all
// (a JSONC file with comments): the rest of the install ran. The command maps it to exit code 8.
type RefusedError struct {
	Paths []string
	// Reason says what to do about it.
	Reason string
}

func (e *RefusedError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("%d file(s) changed since they were installed: use --force to overwrite", len(e.Paths))
}

// Refused returns the RefusedError in err, if any.
func Refused(err error) (*RefusedError, bool) {
	var re *RefusedError
	return re, errors.As(err, &re)
}

// op carries the options and the actions of one run.
type op struct {
	o *Options
	r *Result
	// refused are the paths of the files that were refused, and the files that could not be edited.
	refused []string
	// reasons says why each of them was refused; it becomes the RefusedError's Reason.
	reasons []string
	// changed are the kit files that were refused because the user changed them: --force replaces them.
	changed []string
}

func newOp(o *Options) *op {
	if o.KitVersion == "" {
		o.KitVersion = "0.1.0"
	}
	if o.TnctlVersion == "" {
		o.TnctlVersion = "dev"
	}
	if len(o.Agents) == 0 {
		o.Agents = []Agent{AgentClaude, AgentCodex, AgentOpencode}
	}
	// The package reads no environment: a caller that sets only Home gets the default directories,
	// exactly as before the variables were honoured.
	if o.Home != "" {
		if o.ClaudeConfigDir == "" {
			o.ClaudeConfigDir = filepath.Join(o.Home, ".claude")
		}
		if o.ClaudeConfigFile == "" {
			o.ClaudeConfigFile = filepath.Join(o.Home, ".claude.json")
		}
		if o.CodexHome == "" {
			o.CodexHome = filepath.Join(o.Home, ".codex")
		}
		if o.OpencodeConfigDir == "" {
			o.OpencodeConfigDir = filepath.Join(o.Home, ".config", "opencode")
		}
	}
	return &op{o: o, r: &Result{Actions: []Action{}}}
}

// finish returns the result, and a RefusedError when something was refused.
func (p *op) finish() (*Result, error) {
	if len(p.refused) > 0 {
		var all []string
		if n := len(p.changed); n > 0 {
			all = append(all, fmt.Sprintf("%d file(s) changed since they were installed: use --force to overwrite", n))
		}
		all = append(all, p.reasons...)
		return p.r, &RefusedError{Paths: p.refused, Reason: strings.Join(all, "; ")}
	}
	return p.r, nil
}

// refuse records that path was refused, with the note that goes on its action (the snippet to add or
// remove by hand, when there is one) and the reason the run reports. A refusal never stops the run: the
// caller returns nil and the next file, root and agent go on.
func (p *op) refuse(path, agent, note, reason string) {
	p.r.act(ActionRefuse, path, agent, note)
	p.refused = append(p.refused, path)
	p.reasons = append(p.reasons, reason)
}

// wasRefused reports whether path was refused in this run.
func (p *op) wasRefused(path string) bool {
	for _, r := range p.refused {
		if filepath.Clean(r) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

// refuseChanged refuses a kit file the user changed since it was installed. --force replaces it.
func (p *op) refuseChanged(path, agent string) {
	note := "changed since it was installed: use --force to overwrite"
	p.refuse(path, agent, note, path+": "+note)
	p.changed = append(p.changed, path)
}

func (p *op) has(a Agent) bool {
	for _, b := range p.o.Agents {
		if a == b {
			return true
		}
	}
	return false
}

// agentLabel names the agents that share a destination ("codex,opencode").
func agentLabel(agents []Agent) string {
	var names []string
	for _, a := range agents {
		names = append(names, string(a))
	}
	return strings.Join(names, ",")
}

// root is one destination root of the kit, shared by one or more agents.
type root struct {
	Dir    string
	Agents []Agent
}

// claudeDir is the .claude directory of Claude Code in this scope (managed: the managed directory).
func (p *op) claudeDir() string {
	switch p.o.Scope {
	case ScopeProject:
		return filepath.Join(p.o.ProjectDir, ".claude")
	case ScopeManaged:
		return p.o.ManagedDir
	}
	return p.o.ClaudeConfigDir
}

// skillRoots are the destinations of the skills, one copy per root: Claude Code reads
// .claude/skills, Codex and OpenCode read .agents/skills.
func (p *op) skillRoots() []root {
	switch p.o.Scope {
	case ScopeManaged:
		return []root{{Dir: filepath.Join(p.o.ManagedDir, "skills")}}
	case ScopeProject:
		var out []root
		if p.has(AgentClaude) {
			out = append(out, root{Dir: filepath.Join(p.o.ProjectDir, ".claude", "skills"), Agents: []Agent{AgentClaude}})
		}
		if p.has(AgentCodex) || p.has(AgentOpencode) {
			var agents []Agent
			for _, a := range []Agent{AgentCodex, AgentOpencode} {
				if p.has(a) {
					agents = append(agents, a)
				}
			}
			out = append(out, root{Dir: filepath.Join(p.o.ProjectDir, ".agents", "skills"), Agents: agents})
		}
		return out
	}
	var out []root
	if p.has(AgentClaude) {
		out = append(out, root{Dir: filepath.Join(p.o.ClaudeConfigDir, "skills"), Agents: []Agent{AgentClaude}})
	}
	if p.has(AgentCodex) || p.has(AgentOpencode) {
		var agents []Agent
		for _, a := range []Agent{AgentCodex, AgentOpencode} {
			if p.has(a) {
				agents = append(agents, a)
			}
		}
		out = append(out, root{Dir: filepath.Join(p.o.Home, ".agents", "skills"), Agents: agents})
	}
	return out
}

// projectRootPaths are the directories a project-scope install writes below, with the agents they serve.
// A project that commits one of them as a symlink is not written through.
func projectRootPaths(project string) []root {
	claude := []Agent{AgentClaude}
	shared := []Agent{AgentCodex, AgentOpencode}
	return []root{
		{Dir: filepath.Join(project, ".claude"), Agents: claude},
		{Dir: filepath.Join(project, ".claude", "skills"), Agents: claude},
		{Dir: filepath.Join(project, ".claude", "hooks"), Agents: claude},
		{Dir: filepath.Join(project, ".agents"), Agents: shared},
		{Dir: filepath.Join(project, ".agents", "skills"), Agents: shared},
		{Dir: filepath.Join(project, ".codex"), Agents: []Agent{AgentCodex}},
	}
}

// projectSymlinks returns the project roots that are symlinks, each with the reason it is refused: tnctl
// never installs a project's kit through a link into (or out of) the repository.
func (p *op) projectSymlinks() map[string]string {
	if p.o.Scope != ScopeProject {
		return nil
	}
	out := map[string]string{}
	for _, r := range projectRootPaths(p.o.ProjectDir) {
		if fi, err := os.Lstat(r.Dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			out[r.Dir] = r.Dir + " is a symlink: tnctl does not install through it"
		}
	}
	return out
}

// atOrBelow reports whether path is dir or below it.
func atOrBelow(path, dir string) bool {
	return filepath.Clean(path) == filepath.Clean(dir) || isBelow(path, dir)
}

// refuseProjectSymlinks refuses every project root that is a symlink. It reports whether it refused any:
// a project with a symlinked root is not installed into or uninstalled from at all.
func (p *op) refuseProjectSymlinks() bool {
	links := p.projectSymlinks()
	if len(links) == 0 {
		return false
	}
	for _, r := range projectRootPaths(p.o.ProjectDir) {
		if why, ok := links[r.Dir]; ok {
			p.refuse(r.Dir, "", why, why)
		}
	}
	return true
}

// allRoots are the roots `tnctl agent list` and `uninstall` visit: the skills of every agent, and the
// guard's hooks directory of Claude Code. A managed install always holds the guard, so its hooks root is
// always visited, --hooks or not.
func (p *op) allRoots(withHooks bool) []root {
	out := p.skillRoots()
	if p.o.Scope == ScopeManaged || (withHooks && p.has(AgentClaude)) {
		out = append(out, root{Dir: filepath.Join(p.claudeDir(), "hooks"), Agents: []Agent{AgentClaude}})
	}
	return out
}

// kitFiles returns the files the kit installs into a root: the skills as <skill>/<file> for a skills
// root, the guard alone for the hooks directory.
func (p *op) kitFiles(kind string) (map[string][]byte, error) {
	if kind == kindHooks {
		b, err := fs.ReadFile(p.o.FS, "hooks/teknoir-guard")
		if err != nil {
			return nil, err
		}
		return map[string][]byte{guardName: b}, nil
	}
	files := map[string][]byte{}
	err := fs.WalkDir(p.o.FS, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("skills", filepath.FromSlash(path))
		if err != nil {
			return err
		}
		b, err := fs.ReadFile(p.o.FS, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// rootKind names what a root holds, so list and uninstall compare it against the right kit files.
func rootKind(dir string) string {
	if filepath.Base(dir) == "hooks" {
		return kindHooks
	}
	return kindSkills
}

const (
	kindSkills = "skills"
	kindHooks  = "hooks"
	// guardName is the path of the guard below the hooks directory.
	guardName = "teknoir-guard"
	// manifestName is the manifest of every root, recording what the kit wrote there.
	manifestName = ".tnctl-agent-kit.json"
)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// modeFor is the mode an installed file gets: executable below hooks/ or scripts/, or with a shebang;
// 0644 otherwise.
func modeFor(rel string, content []byte) os.FileMode {
	switch {
	case strings.HasPrefix(rel, "hooks/"):
		return 0o755
	case strings.HasPrefix(rel, "scripts/"), strings.Contains(rel, "/scripts/"):
		return 0o755
	case strings.HasPrefix(string(content), "#!"):
		return 0o755
	}
	return 0o644
}

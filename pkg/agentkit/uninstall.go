package agentkit

import (
	"os"
	"path/filepath"
)

// Uninstall removes the kit from the scope of the options: the files its manifests record, while they
// still match, and the manifests; with MCP the teknoir MCP server, in either of its forms, with Hooks the
// guard hook and its PreToolUse entry, with AgentsMD the AGENTS.md section. A file the user changed is
// kept and reported. A managed install of Claude Code holds the guard, so it goes with or without
// --hooks.
func Uninstall(o *Options) (*Result, error) {
	p := newOp(o)
	if err := p.validate(); err != nil {
		return p.r, err
	}
	// A project that commits .claude, .agents or .codex as a symlink is not uninstalled from at all:
	// the link, and what it points at, are left as they are.
	if p.refuseProjectSymlinks() {
		return p.finish()
	}
	if p.o.AgentsMD {
		if err := p.uninstallAgentsMD(); err != nil {
			return p.r, err
		}
	}
	// Codex and OpenCode read the same skills root: it is removed only when both are uninstalled, so
	// an agent that is still installed keeps its skills.
	shared := p.sharedSkillsRoot()
	keepShared := shared != "" && (!p.has(AgentCodex) || !p.has(AgentOpencode))
	for _, r := range p.allRoots(p.has(AgentClaude)) {
		if rootKind(r.Dir) == kindHooks && p.o.Scope != ScopeManaged {
			// --hooks removes the guard and its settings entry; the skills go with the loop below.
			continue
		}
		if keepShared && filepath.Clean(r.Dir) == filepath.Clean(shared) {
			if _, err := os.Lstat(manifestPath(r.Dir)); err == nil {
				p.r.act(ActionKeep, r.Dir, agentLabel(r.Agents), "shared by Codex and OpenCode")
				p.r.warn("%s is shared by Codex and OpenCode: uninstall with --agent codex,opencode to remove it", r.Dir)
			}
			continue
		}
		if err := p.uninstallSet(r.Dir, agentLabel(r.Agents)); err != nil {
			return p.r, err
		}
	}
	if p.o.Hooks && p.o.Scope != ScopeManaged {
		if !p.has(AgentClaude) {
			p.r.warn("hooks are installed for Claude Code only")
		} else if err := p.uninstallHooks(); err != nil {
			return p.r, err
		}
	}
	if p.o.MCP {
		if err := p.uninstallMCP(); err != nil {
			return p.r, err
		}
	}
	return p.finish()
}

// sharedSkillsRoot is the skills root Codex and OpenCode read together in this scope; "" for a managed
// install, which has one root of its own.
func (p *op) sharedSkillsRoot() string {
	switch p.o.Scope {
	case ScopeProject:
		return filepath.Join(p.o.ProjectDir, ".agents", "skills")
	case ScopeUser:
		return filepath.Join(p.o.Home, ".agents", "skills")
	}
	return ""
}

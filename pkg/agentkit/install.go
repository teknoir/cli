package agentkit

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Install installs the kit for the agents of the options, in the scope of the options: the skills, and
// with MCP the `teknoir` MCP server, with Hooks the guard hook, with AgentsMD the AGENTS.md section.
// Files the user changed are refused, not overwritten; the rest of the install still runs, and the
// RefusedError comes back with the Result.
func Install(o *Options) (*Result, error) {
	p := newOp(o)
	if err := p.validate(); err != nil {
		return p.r, err
	}
	if err := p.install(); err != nil {
		return p.r, err
	}
	return p.finish()
}

// validate refuses options that cannot work together.
func (p *op) validate() error {
	if p.o.FS == nil {
		return errors.New("agentkit: no kit filesystem")
	}
	if p.o.Council && !p.o.MCP {
		return errors.New("--council needs --mcp")
	}
	switch p.o.Scope {
	case ScopeManaged:
		if p.o.ManagedDir == "" {
			return errors.New("--managed needs a directory")
		}
		if p.o.MCP {
			return errors.New("--mcp needs --user or --project: a managed install writes no MCP entry")
		}
		if p.o.AgentsMD {
			return errors.New("--agents-md needs --project")
		}
	case ScopeProject:
		if p.o.ProjectDir == "" {
			return errors.New("agentkit: no project directory")
		}
	default:
		if p.o.Home == "" {
			return errors.New("agentkit: no home directory")
		}
		if p.o.AgentsMD {
			return errors.New("--agents-md needs --project")
		}
	}
	return nil
}

func (p *op) install() error {
	// A project that commits .claude, .agents or .codex as a symlink is not installed into at all:
	// the link, and what it points at, are left as they are.
	if p.refuseProjectSymlinks() {
		return nil
	}
	files, err := p.kitFiles(kindSkills)
	if err != nil {
		return fmt.Errorf("read the kit: %w", err)
	}
	for _, r := range p.skillRoots() {
		if err := p.installSet(r.Dir, files, agentLabel(r.Agents)); err != nil {
			return err
		}
	}
	switch {
	case p.o.Scope == ScopeManaged:
		// A managed install of Claude Code is the TeknoirBot image's, and the image's managed settings
		// point at the guard: it is always written, and --hooks only adds the entry to add by hand. An
		// image with another agent alone has no Claude Code and gets the skills only.
		if !p.has(AgentClaude) {
			if p.o.Hooks {
				p.r.warn("hooks are installed for Claude Code only")
			}
			break
		}
		if err := p.installManagedGuard(); err != nil {
			return err
		}
	case p.o.Hooks:
		if !p.has(AgentClaude) {
			p.r.warn("hooks are installed for Claude Code only")
		} else if err := p.installHooks(); err != nil {
			return err
		}
	}
	if p.o.MCP {
		if err := p.installMCP(); err != nil {
			return err
		}
	}
	if p.o.AgentsMD {
		if err := p.installAgentsMD(); err != nil {
			return err
		}
	}
	return nil
}

// installManagedGuard writes the guard below the managed directory's hooks root, and with Hooks records
// the settings entry the image adds to Claude Code's managed settings by hand. Each run writes the guard
// exactly once, with or without --hooks. Its caller installs for Claude Code only.
func (p *op) installManagedGuard() error {
	content, err := fs.ReadFile(p.o.FS, "hooks/"+guardName)
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(p.o.ManagedDir, "hooks")
	if err := p.installSet(hooksDir, map[string][]byte{guardName: content}, string(AgentClaude)); err != nil {
		return err
	}
	if !p.o.Hooks {
		return nil
	}
	snippet := settingsSnippet(p.guardCommand())
	p.r.warn("add this entry to Claude Code's managed settings (on Linux /etc/claude-code/managed-settings.json, or a file in /etc/claude-code/managed-settings.d/):")
	p.r.warn("  %s", snippet)
	p.r.act(ActionSkip, filepath.Join(hooksDir, guardName), string(AgentClaude), "settings entry to add by hand: "+snippet)
	return nil
}

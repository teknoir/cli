package agentkit

import (
	"errors"
	"fmt"
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
	files, err := p.kitFiles(kindSkills)
	if err != nil {
		return fmt.Errorf("read the kit: %w", err)
	}
	for _, r := range p.skillRoots() {
		if err := p.installSet(r.Dir, files, agentLabel(r.Agents)); err != nil {
			return err
		}
	}
	if p.o.Hooks {
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

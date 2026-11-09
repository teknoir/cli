package agentkit

// Uninstall removes the kit from the scope of the options: the files its manifests record, while they
// still match, and the manifests; with MCP the teknoir MCP server, with Hooks the guard hook and its
// PreToolUse entry, with AgentsMD the AGENTS.md section. A file the user changed is kept and reported.
func Uninstall(o *Options) (*Result, error) {
	p := newOp(o)
	if err := p.validate(); err != nil {
		return p.r, err
	}
	if p.o.AgentsMD {
		if err := p.uninstallAgentsMD(); err != nil {
			return p.r, err
		}
	}
	for _, r := range p.allRoots(p.has(AgentClaude)) {
		if rootKind(r.Dir) == kindHooks {
			// --hooks removes the guard and its settings entry; the skills go with the loop below.
			continue
		}
		if err := p.uninstallSet(r.Dir, agentLabel(r.Agents)); err != nil {
			return p.r, err
		}
	}
	if p.o.Hooks {
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

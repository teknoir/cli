package agentkit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The lines that mark the snippet in AGENTS.md, so a re-run replaces it and an uninstall removes it
// without touching the rest of the file.
const (
	agentsMDBegin = "<!-- teknoir-agent-kit:begin -->"
	agentsMDEnd   = "<!-- teknoir-agent-kit:end -->"
)

// agentsMDState is what the markers of AGENTS.md say.
type agentsMDState int

const (
	// agentsMDNone: no marker at all.
	agentsMDNone agentsMDState = iota
	// agentsMDOk: exactly one begin marker, exactly one end marker after it.
	agentsMDOk
	// agentsMDDamaged: anything else. The file is refused, because writing it would lose what is
	// inside the markers or after them.
	agentsMDDamaged
)

// agentsMDPath is ./AGENTS.md in the project directory.
func (p *op) agentsMDPath() string { return filepath.Join(p.o.ProjectDir, "AGENTS.md") }

// agentsMDBlock is the kit's section: the snippet between the markers.
func (p *op) agentsMDBlock() (string, error) {
	b, err := fs.ReadFile(p.o.FS, "AGENTS.snippet.md")
	if err != nil {
		return "", err
	}
	return agentsMDBegin + "\n" + strings.TrimRight(string(b), "\n") + "\n" + agentsMDEnd + "\n", nil
}

// refuseAgentsMD refuses AGENTS.md, with the block to add or remove by hand.
func (p *op) refuseAgentsMD(path, why string) {
	block, err := p.agentsMDBlock()
	if err != nil {
		p.refuse(path, "", why, why)
		return
	}
	p.refuse(path, "", why+"\n"+block, why+"\n"+block)
}

// installAgentsMD adds the kit's section to AGENTS.md, replacing an earlier one between the markers and
// keeping the text around it.
func (p *op) installAgentsMD() error {
	path := p.agentsMDPath()
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuseAgentsMD(path, why)
		return nil
	}
	block, err := p.agentsMDBlock()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(data)
	start, end, state := agentsMDRange(text)
	switch state {
	case agentsMDDamaged:
		p.refuseAgentsMD(path, "damaged teknoir section markers in AGENTS.md: fix them by hand")
		return nil
	case agentsMDOk:
		p.r.act(ActionWrite, path, "", "replaced the teknoir section")
		return p.overwriteFile(target, []byte(text[:start]+block+text[end:]), 0o644)
	}
	if text != "" {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n"
	}
	p.r.act(ActionWrite, path, "", "added the teknoir section")
	return p.overwriteFile(target, []byte(text+block), 0o644)
}

// uninstallAgentsMD removes the kit's section from AGENTS.md and leaves the rest.
func (p *op) uninstallAgentsMD() error {
	path := p.agentsMDPath()
	target, why := p.foreignTarget(path)
	if why != "" {
		p.refuseAgentsMD(path, why)
		return nil
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(data)
	start, end, state := agentsMDRange(text)
	switch state {
	case agentsMDDamaged:
		p.refuseAgentsMD(path, "damaged teknoir section markers in AGENTS.md: fix them by hand")
		return nil
	case agentsMDNone:
		return nil
	}
	before, after := text[:start], text[end:]
	// Drop the blank line the section was separated by.
	if strings.HasSuffix(before, "\n\n") {
		before = before[:len(before)-1]
	}
	p.r.act(ActionDelete, path, "", "the teknoir section")
	next := before + after
	if strings.TrimSpace(next) == "" {
		// Only our section was in the file. A symlinked AGENTS.md is emptied, never unlinked: the
		// file it points at is still there for the rest of the repository.
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return p.overwriteFile(target, nil, 0o644)
		}
		return p.remove(path, filepath.Dir(path))
	}
	return p.overwriteFile(target, []byte(next), 0o644)
}

// agentsMDRange returns the byte range of the kit's section: the begin marker line through the line
// after the end marker, so the text around it joins without a blank line left behind. Any other set of
// markers is damaged: a second block is never appended beside them.
func agentsMDRange(text string) (start, end int, state agentsMDState) {
	begins, ends := strings.Count(text, agentsMDBegin), strings.Count(text, agentsMDEnd)
	if begins == 0 && ends == 0 {
		return 0, 0, agentsMDNone
	}
	if begins != 1 || ends != 1 {
		return 0, 0, agentsMDDamaged
	}
	start = strings.Index(text, agentsMDBegin)
	rel := strings.Index(text[start:], agentsMDEnd)
	if rel < 0 {
		return 0, 0, agentsMDDamaged
	}
	end = start + rel + len(agentsMDEnd)
	if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
		end += i + 1
	}
	return start, end, agentsMDOk
}

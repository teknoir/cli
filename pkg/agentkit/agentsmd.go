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

// installAgentsMD adds the kit's section to AGENTS.md, replacing an earlier one between the markers and
// keeping the text around it.
func (p *op) installAgentsMD() error {
	block, err := p.agentsMDBlock()
	if err != nil {
		return err
	}
	path := p.agentsMDPath()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(data)
	if start, end, ok := agentsMDRange(text); ok {
		p.r.act(ActionWrite, path, "", "replaced the teknoir section")
		return p.overwriteFile(path, []byte(text[:start]+block+text[end:]), 0o644)
	}
	if text != "" {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n"
	}
	p.r.act(ActionWrite, path, "", "added the teknoir section")
	return p.overwriteFile(path, []byte(text+block), 0o644)
}

// uninstallAgentsMD removes the kit's section from AGENTS.md and leaves the rest.
func (p *op) uninstallAgentsMD() error {
	path := p.agentsMDPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(data)
	start, end, ok := agentsMDRange(text)
	if !ok {
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
		// Only our section was in the file.
		return p.remove(path, filepath.Dir(path))
	}
	return p.overwriteFile(path, []byte(next), 0o644)
}

// agentsMDRange returns the byte range of the kit's section: the begin marker line through the line
// after the end marker, so the text around it joins without a blank line left behind.
func agentsMDRange(text string) (int, int, bool) {
	start := strings.Index(text, agentsMDBegin)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(text[start:], agentsMDEnd)
	if rel < 0 {
		return 0, 0, false
	}
	end := start + rel + len(agentsMDEnd)
	if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
		end += i + 1
	}
	return start, end, true
}

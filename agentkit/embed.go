// Package agentkit carries the Teknoir agent kit: the Agent Skills, the guard hook and the MCP server
// registration a coding agent needs to operate Teknoir devices and robots. It is embedded in the tnctl
// binary, so `tnctl agent install` works offline and installs exactly the kit of the tnctl that runs it.
package agentkit

import "embed"

// FS is the kit: skills/<name>/SKILL.md, hooks/teknoir-guard, the Claude Code plugin manifest and the
// MCP server entry. agentkit/README.md is documentation and is not embedded.
//
//go:embed all:skills all:hooks .claude-plugin .mcp.json AGENTS.snippet.md
var FS embed.FS

// Version is the version of the embedded kit. The skills carry it in their frontmatter
// (metadata.teknoir-kit-version) and an install records it in its manifest.
const Version = "0.1.0"

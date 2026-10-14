// Package plugins carries the client packages built with this executable.
package plugins

import "embed"

// Assets intentionally lists the distributed file types, including hidden
// manifests. Local files beside a plugin are never swept into the executable.
//
//go:embed LICENSE llewm/.codex-plugin/plugin.json llewm/.mcp.json llewm/hooks/hooks.json llewm/skills/*/SKILL.md claude-code/.claude-plugin/plugin.json claude-code/.mcp.json claude-code/hooks/hooks.json claude-code/skills/*/SKILL.md
var Assets embed.FS

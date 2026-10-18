package main

import (
	"encoding/json"
	"os"
	"testing"
)

// Both clients must discover the same public plugin name and launch the
// installed executable, while retaining the internal Continuity MCP server.
func TestPluginExecutableWiring(t *testing.T) {
	for _, client := range []struct{ root, manifest string }{
		{"../../plugins/llewm", ".codex-plugin/plugin.json"},
		{"../../plugins/claude-code", ".claude-plugin/plugin.json"},
	} {
		t.Run(client.root, func(t *testing.T) {
			read := func(path string, value any) {
				body, err := os.ReadFile(client.root + "/" + path)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, value); err != nil {
					t.Fatal(err)
				}
			}
			var manifest struct{ Name, Version string }
			read(client.manifest, &manifest)
			if manifest.Name != "llewm" {
				t.Fatalf("plugin name: %q", manifest.Name)
			}
			var config struct {
				Servers map[string]struct {
					Command string
					Args    []string
					Env     map[string]string
				} `json:"mcpServers"`
			}
			read(".mcp.json", &config)
			server := config.Servers["continuity"]
			if server.Env["LLEWM_PLUGIN_VERSION"] != manifest.Version || server.Env["LLEWM_PLUGIN_CLIENT"] == "" {
				t.Fatalf("plugin launch identity: %+v", server)
			}
			if len(config.Servers) != 1 || server.Command != "llewm" || len(server.Args) != 1 || server.Args[0] != "mcp" {
				t.Fatalf("MCP launch wiring: %+v", config)
			}
		})
	}
}

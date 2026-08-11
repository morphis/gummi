package agent

import "encoding/json"

// buildGummiMCPServerConfig renders the per-session MCP client config that
// points an agent backend's MCP transport at gummi's own tool server
// (`gummi __mcp`). It is the wire form shared by the stdio-MCP backends:
// claudecode's --mcp-config argv and codex's --mcp-config wrap this same
// JSON blob, so the gummi server's command/env shape stays in one place.
//
// The JSON shape is Claude Code's mcpServers config:
//
//	{"mcpServers":{"gummi":{"command":execPath,"args":["__mcp","--feature",featureID],"env":{"GUMMI_MCP_SOCK":sockPath}}}}
//
// args carries the subcommand verbatim (order is significant — the mcp
// subcommand parses positionally), and the socket path travels in env so
// the __mcp child can reach the server without the parent process scraping
// its own environment.
func buildGummiMCPServerConfig(execPath, featureID, sockPath string) []byte {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"gummi": map[string]any{
				"command": execPath,
				"args":    []string{"__mcp", "--feature", featureID},
				"env":     map[string]string{"GUMMI_MCP_SOCK": sockPath},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// The map is a fixed shape of strings/slices; json.Marshal cannot
		// fail on it, so a panic here would only mask a programming error.
		panic(err)
	}
	return b
}

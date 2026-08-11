package agent

import "encoding/json"

// buildOpencodeConfig renders the per-session OPENCODE_CONFIG file content
// for an opencode-driven session. It emits exactly two blocks: opencode's
// `permission` (always) and `mcp.gummi` (only when a feature id and MCP
// socket are both present). Anything else opencode reads — models, keybinds,
// bash tool policies — stays under operator control in the global
// opencode.jsonc, which this file merges on top of.
//
// The permission block cages opencode's file tools to the worktree: edit
// (which gates both opencode's edit and write tools) is pinned to a
// pattern→action map allowing only `<worktree>/**` and denying everything
// else, and external_directory is denied. When the caller names specific
// extra reads (ExtraReadAllows), external_directory must be opened
// (opencode's deny gates the fs tools generally, so a per-file read
// allowance cannot slip through otherwise) and the named paths are then
// whitelisted under `read`, while edits/writes stay caged by permission.edit.
//
// Note that opencode's shell (`bash`) tool is deliberately NOT caged here:
// its policy is command-string based rather than path based, so a real cage
// requires process-level confinement, which is out of scope for this feature
// (see FD-014 sandbox mode). opencode strips // comments from its JSON config,
// so this note lives in the Go source only.
func buildOpencodeConfig(workdir, mcpSock, featureID, execPath string, extraReadAllows []string) ([]byte, error) {
	worktreeOnly := map[string]string{workdir + "/**": "allow", "*": "deny"}

	permission := map[string]any{
		"edit":               worktreeOnly,
		"write":              worktreeOnly,
		"external_directory": "deny",
	}
	if len(extraReadAllows) > 0 {
		permission["external_directory"] = "allow"
		readOnly := map[string]string{workdir + "/**": "allow", "*": "deny"}
		for _, p := range extraReadAllows {
			readOnly[p] = "allow"
		}
		permission["read"] = readOnly
	}

	out := map[string]any{"permission": permission}
	if featureID != "" && mcpSock != "" {
		out["mcp"] = map[string]any{
			"gummi": map[string]any{
				"type":        "local",
				"command":     []string{execPath, "__mcp", "--feature", featureID},
				"environment": map[string]string{"GUMMI_MCP_SOCK": mcpSock},
			},
		}
	}
	return json.Marshal(out)
}

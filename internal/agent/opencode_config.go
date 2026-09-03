package agent

import "encoding/json"

// buildOpencodeConfig renders the per-session OPENCODE_CONFIG file content
// for an opencode-driven session. It emits exactly two blocks: opencode's
// `permission` (always) and `mcp.gummi` (only when an MCP socket is present
// alongside a feature id or workspace is set — the same "socket plus
// something to bind it to" gate every other adapter uses). Anything else
// opencode reads — models, keybinds, bash tool policies — stays under
// operator control in the global opencode.jsonc, which this file merges on
// top of.
//
// workspace mirrors SessionOpts.Workspace: it swaps the emitted
// mcp.gummi.command's trailing args from ["__mcp","--feature",featureID]
// to ["__mcp","--workspace"], so the spawned child binds the board-level
// endpoint instead of naming a card. featureID is otherwise unused when
// workspace is true — same convention as the claudecode/codex builders in
// gummi_mcp.go, kept here even though this file's config shape (a JSON
// blob wrapping opencode's own schema, not gummi's) is otherwise unrelated
// to theirs.
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
func buildOpencodeConfig(workdir, mcpSock, featureID, execPath string, extraReadAllows []string, readOnly, workspace bool) ([]byte, error) {
	worktreeOnly := map[string]string{workdir + "/**": "allow", "*": "deny"}

	permission := map[string]any{
		"edit":               worktreeOnly,
		"write":              worktreeOnly,
		"external_directory": "deny",
	}
	// A ReadOnly research session runs in the main checkout with no
	// worktree: pin edit and write to "deny" outright so opencode's file
	// tools are structurally absent regardless of the operator's sandbox
	// mode, while read (and external_directory, above) stay open.
	if readOnly {
		permission["edit"] = "deny"
		permission["write"] = "deny"
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
	if mcpSock != "" && (featureID != "" || workspace) {
		command := []string{execPath, "__mcp", "--feature", featureID}
		if workspace {
			command = []string{execPath, "__mcp", "--workspace"}
		}
		out["mcp"] = map[string]any{
			"gummi": map[string]any{
				"type":        "local",
				"command":     command,
				"environment": map[string]string{"GUMMI_MCP_SOCK": mcpSock},
			},
		}
	}
	return json.Marshal(out)
}

// buildHostedOpencodeMCPConfig renders the OPENCODE_CONFIG file content for
// the agent tab's hosted opencode session: just the mcp.gummi block that
// binds the workspace endpoint, with no permission key. This deliberately
// differs from buildOpencodeConfig's scripted-session shape, which always
// cages edits to a worktree — the hosted tab has no equivalent sandboxing
// concept for any backend (see HostedMCPAttach's doc and the feature's
// design notes on why applying a cage to opencode alone here would be a
// backend-specific surprise), so this builder never emits one.
func buildHostedOpencodeMCPConfig(execPath, sockPath string) []byte {
	out := map[string]any{
		"mcp": map[string]any{
			"gummi": map[string]any{
				"type":        "local",
				"command":     []string{execPath, "__mcp", "--workspace"},
				"environment": map[string]string{"GUMMI_MCP_SOCK": sockPath},
			},
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Fixed shape of strings/slices; cannot fail on it.
		panic(err)
	}
	return b
}

package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

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
//
// workspace selects which scope the child binds to: false emits
// ["__mcp","--feature",featureID] (the per-card stage session, the
// original mode); true emits ["__mcp","--workspace"] instead and
// featureID is not consulted, mirroring cmd/gummi/mcp.go's own gate,
// which treats --feature and --workspace as mutually exclusive rather
// than accepting a sentinel feature id that means "workspace". Keeping
// that choice a caller-supplied bool (instead of, say, inferring it from
// an empty featureID) means a caller can never silently slide from one
// scope to the other just because a feature id happened to be blank.
func buildGummiMCPServerConfig(execPath, featureID, sockPath string, workspace bool) []byte {
	args := []string{"__mcp", "--feature", featureID}
	if workspace {
		args = []string{"__mcp", "--workspace"}
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"gummi": map[string]any{
				"command": execPath,
				"args":    args,
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

// buildCodexGummiOverride renders the `-c` argument that registers gummi's
// MCP server for one `codex exec` invocation. codex — unlike claudecode —
// has no --mcp-config argv flag, so the server is injected via a
// per-invocation config override whose value is an inline TOML table. This
// function is the single locus for TOML basic-string value escaping and is
// exercised in isolation by re-parsing its output with a real TOML parser.
//
// workspace mirrors buildGummiMCPServerConfig's flag of the same name:
// false renders args=["__mcp","--feature",featureID] (and featureID must
// quote cleanly, same as always); true renders args=["__mcp","--workspace"]
// and featureID is never even passed through tomlQuote, so a stray control
// character in an unused featureID can't fail a workspace session that
// never needed it.
func buildCodexGummiOverride(execPath, featureID, sockPath string, workspace bool) (string, error) {
	cmd, err := tomlQuote(execPath)
	if err != nil {
		return "", err
	}
	sock, err := tomlQuote(sockPath)
	if err != nil {
		return "", err
	}
	argsTOML := `["__mcp","--workspace"]`
	if !workspace {
		fid, err := tomlQuote(featureID)
		if err != nil {
			return "", err
		}
		argsTOML = "[\"__mcp\",\"--feature\"," + fid + "]"
	}
	return "mcp_servers.gummi=" + "{" +
		"command=" + cmd + "," +
		"args=" + argsTOML + "," +
		"env={GUMMI_MCP_SOCK=" + sock + "}" +
		"}", nil
}

// tomlQuote quotes a single value using TOML basic-string rules: backslashes
// and double quotes are escaped, and any C0/C1 control character makes the
// value unrepresentable in a basic string, so it is rejected.
func tomlQuote(s string) (string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7F {
			return "", errors.New("codex adapter: control character in mcp override value")
		}
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`, nil
}

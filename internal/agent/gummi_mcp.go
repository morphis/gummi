package agent

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// mcpCallTimeout is how long a backend's MCP client is told to wait on one
// call to gummi's server.
//
// Every client bounds a tool call, and the bound is sized for tools that
// compute: a minute on opencode and on codex, and on claude a bound on
// how long a call may go without a response or progress. ask_user does not compute, it
// waits on a person, and a person reading the question for longer than a
// minute had it fail under them — the model was told the call timed out,
// asked again, and ended its turn on a question nobody had answered. A
// week is "as long as it takes" spelled as a number every client accepts:
// it stays under the 2^31-1 ms a JavaScript timer can hold. The engine
// does not depend on it (a turn that ends on an open question keeps the
// question open); this is what keeps the call itself alive, so the answer
// lands as the result the model is waiting for.
const mcpCallTimeout = 7 * 24 * time.Hour

// gummiMCPServerEntry is one mcpServers entry pointing an agent backend's
// MCP transport at gummi's own tool server: the command/env shape every
// stdio-MCP consumer shares (claudecode's --mcp-config blob and the
// antigravity card home's mcp_config.json are both built from it).
//
// timeoutKey/timeoutValue carry the one field backends disagree on:
// Claude Code reads milliseconds under `timeout`, agy reads seconds under
// `timeoutSeconds` — the caller names its consumer's key and value so the
// rest of the shape cannot drift between the two renderers.
func gummiMCPServerEntry(execPath, featureID, sockPath, timeoutKey string, timeoutValue any) map[string]any {
	return map[string]any{
		"command":  execPath,
		"args":     []string{"__mcp", "--feature", featureID},
		"env":      map[string]string{"GUMMI_MCP_SOCK": sockPath},
		timeoutKey: timeoutValue,
	}
}

// buildGummiMCPServerConfig renders the per-session MCP client config that
// points an agent backend's MCP transport at gummi's own tool server
// (`gummi __mcp`). It is the wire form shared by the stdio-MCP backends:
// claudecode's --mcp-config argv and codex's --mcp-config wrap this same
// JSON blob, so the gummi server's command/env shape stays in one place.
//
// The JSON shape is Claude Code's mcpServers config:
//
//	{"mcpServers":{"gummi":{"command":execPath,"args":["__mcp","--feature",featureID],"env":{"GUMMI_MCP_SOCK":sockPath},"timeout":ms}}}
//
// args carries the subcommand verbatim (order is significant — the mcp
// subcommand parses positionally), and the socket path travels in env so
// the __mcp child can reach the server without the parent process scraping
// its own environment.
func buildGummiMCPServerConfig(execPath, featureID, sockPath string) []byte {
	// Claude Code aborts a call that has sent no response or progress
	// for its idle bound, and tells the server nothing; a server's own
	// timeout is what lifts it.
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"gummi": gummiMCPServerEntry(execPath, featureID, sockPath,
				"timeout", mcpCallTimeout.Milliseconds()),
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
// featureID must quote cleanly, same as every value below.
func buildCodexGummiOverride(execPath, featureID, sockPath string) (string, error) {
	cmd, err := tomlQuote(execPath)
	if err != nil {
		return "", err
	}
	sock, err := tomlQuote(sockPath)
	if err != nil {
		return "", err
	}
	fid, err := tomlQuote(featureID)
	if err != nil {
		return "", err
	}
	argsTOML := `["__mcp","--feature",` + fid + `]`
	return "mcp_servers.gummi=" + "{" +
		"command=" + cmd + "," +
		"args=" + argsTOML + "," +
		"env={GUMMI_MCP_SOCK=" + sock + "}," +
		"tool_timeout_sec=" + strconv.FormatInt(int64(mcpCallTimeout/time.Second), 10) +
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

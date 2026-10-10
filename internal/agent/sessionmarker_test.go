package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// sessionMarker is what a gummi process sets for everything it starts
// (cmd/gummi's spawnedMarker); `gummi push` and `gummi pr create` refuse
// under it, so an agent's shell cannot publish.
const sessionMarker = "GUMMI_SPAWNED"

// A backend that built its process's environment from anything but gummi's
// own, or filtered the marker out of it, would hand its agent a shell the
// publish verbs run in.
func TestEveryBackendEnvironmentCarriesTheSessionMarker(t *testing.T) {
	t.Setenv(sessionMarker, "1")
	want := sessionMarker + "=1"
	for name, env := range map[string][]string{
		"claude":           scrubClaudeSessionEnv(os.Environ()),
		"antigravity home": envWithAntigravityHome(os.Environ(), t.TempDir()),
		"antigravity tool": antigravityToolEnv(envWithAntigravityHome(os.Environ(), t.TempDir()), t.TempDir()),
		"opencode session": childEnvFor(0, "sock", "config"),
		"opencode probe":   envWithout("OPENCODE_CONFIG"),
	} {
		if !slices.Contains(env, want) {
			t.Errorf("the %s environment drops %s", name, sessionMarker)
		}
	}

	// every environment a backend hands a process starts from os.Environ();
	// the ones that take it as an argument are the filters checked above
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	assign := regexp.MustCompile(`\bEnv(\s*=|,.*=|:)\s`)
	passed := map[string]bool{
		"opencode_client.go: cmd.Env = env":                                        true, // serveOpencode's callers: childEnvFor, envWithout
		"copilot.go: Env:        opts.Env,":                                        true, // empty inherits; see below
		"pi_extension.go: Env:   map[string]string{\"GUMMI_MCP_SOCK\": sockPath},": true, // an MCP server's extra variables, added to the agent's own
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(src), "\n") {
			code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
			code = strings.TrimSpace(code)
			if !assign.MatchString(code) || strings.Contains(code, "os.Environ()") || strings.Contains(code, "= append(cmd.Env, ") || passed[file+": "+code] {
				continue
			}
			t.Errorf("%s builds an environment that is not gummi's own: %s", file, code)
		}
	}

	// the one backend that takes its environment as an option is started
	// with none, so its CLI inherits gummi's
	main, err := os.ReadFile(filepath.Join("..", "..", "cmd", "gummi", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(main), "\n") {
		if strings.Contains(line, "CopilotOptions{") && strings.Contains(line, "Env:") {
			t.Errorf("copilot is started with an environment of its own: %s", strings.TrimSpace(line))
		}
	}
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// __mcp is registered on the root command and hidden.
func TestMCPHiddenRegistration(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"__mcp"})
	if err != nil {
		t.Fatalf("Find(__mcp): %v", err)
	}
	if cmd == nil {
		t.Fatal("__mcp not registered")
	}
	if !cmd.Hidden {
		t.Fatal("__mcp must be Hidden (not in gummi --help)")
	}
}

// gummi --help does not list __mcp…
func TestMCPHiddenFromParentHelp(t *testing.T) {
	t.Cleanup(func() { resetRootHelpFlag(t) })
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"--help"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute(--help): %v", err)
		}
	})
	if strings.Contains(out, "__mcp") {
		t.Fatalf("root --help lists hidden __mcp:\n%s", out)
	}
}

// …but __mcp --help still describes it (Hidden hides from parents, not
// the leaf), including the workspace-scope flag alongside --feature.
func TestMCPHelpDescribesLeaf(t *testing.T) {
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"__mcp", "--help"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute(__mcp --help): %v", err)
		}
	})
	if !strings.Contains(out, "--feature") {
		t.Fatalf("__mcp --help missing --feature flag:\n%s", out)
	}
	if !strings.Contains(out, "--workspace") {
		t.Fatalf("__mcp --help missing --workspace flag:\n%s", out)
	}
}

// Without the discovery env var, __mcp exits 2 with a diagnostic naming
// the variable.
func TestMCPRequiresDiscoveryEnv(t *testing.T) {
	if v, ok := os.LookupEnv("GUMMI_MCP_SOCK"); ok {
		defer os.Setenv("GUMMI_MCP_SOCK", v)
		os.Unsetenv("GUMMI_MCP_SOCK")
	} else {
		defer os.Unsetenv("GUMMI_MCP_SOCK")
	}
	c := freshMCPCmd(t)
	err := runMCP(c, nil)
	var ec *exitError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("runMCP err = %v, want exitError{2}", err)
	}
}

// The engine-reachable failure paths (dial, feature refusal) also exit 2
// and name the path / id on stderr.
func TestMCPDialFailure(t *testing.T) {
	// a feature id but an unreachable socket: no listener exists there.
	t.Setenv("GUMMI_MCP_SOCK", filepath.Join(t.TempDir(), "no.sock"))
	c := freshMCPCmd(t)
	err := runMCP(c, nil)
	var ec *exitError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("runMCP(dial error) err = %v, want exitError{2}", err)
	}
}

// Neither --feature nor --workspace given: a plain usage error, not a
// dial attempt against an empty/sentinel target.
func TestMCPRequiresFeatureOrWorkspace(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", filepath.Join(t.TempDir(), "no.sock"))
	c := workspaceMCPCmd(t, false)
	err := runMCP(c, nil)
	var ec *exitError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("runMCP(neither flag) err = %v, want exitError{2}", err)
	}
}

// --feature and --workspace together are rejected before any dial is
// attempted — overloading one flag's meaning with the other is refused
// outright rather than picking a silent precedence.
func TestMCPFeatureAndWorkspaceMutuallyExclusive(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", filepath.Join(t.TempDir(), "no.sock"))
	c := freshMCPCmd(t)
	if err := c.Flags().Set("workspace", "true"); err != nil {
		t.Fatal(err)
	}
	err := runMCP(c, nil)
	var ec *exitError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("runMCP(--feature and --workspace) err = %v, want exitError{2}", err)
	}
}

// --workspace against an unreachable socket also exits 2, mirroring
// TestMCPDialFailure's --feature case.
func TestMCPWorkspaceDialFailure(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", filepath.Join(t.TempDir(), "no.sock"))
	c := workspaceMCPCmd(t, true)
	err := runMCP(c, nil)
	var ec *exitError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("runMCP(--workspace dial error) err = %v, want exitError{2}", err)
	}
}

// freshMCPCmd and workspaceMCPCmd both explicitly set *every* flag mcpCmd
// registers, not just the one each test cares about: AddFlagSet copies
// pflag.Flag pointers, not values, so every *cobra.Command built this way
// shares mcpCmd's own underlying flag storage. Leaving a flag at
// whatever a previous test last set it to would make these tests order-
// dependent; pinning both flags on every build keeps them isolated.
func freshMCPCmd(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().AddFlagSet(mcpCmd.Flags())
	if err := c.Flags().Set("feature", "FD-001"); err != nil {
		t.Fatal(err)
	}
	if err := c.Flags().Set("workspace", "false"); err != nil {
		t.Fatal(err)
	}
	return c
}

// workspaceMCPCmd is freshMCPCmd's --workspace-scoped counterpart: no
// --feature, --workspace set to the given value.
func workspaceMCPCmd(t *testing.T, workspace bool) *cobra.Command {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().AddFlagSet(mcpCmd.Flags())
	if err := c.Flags().Set("feature", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Flags().Set("workspace", fmt.Sprintf("%t", workspace)); err != nil {
		t.Fatal(err)
	}
	return c
}

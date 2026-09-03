package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/morphis/gummi/internal/mcp"
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

// TestMCPInitializeInstructionsEndToEnd exercises the real wire: a fake
// engine socket answering the hello handshake, runMCP's actual __mcp
// client dialing it, and a real initialize round trip over stdin/stdout —
// proving the instructions text reaches the response through the whole
// path rather than just NewServer's own unit tests.
func TestMCPInitializeInstructionsEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace bool
		featureID string
		wantSub   string
	}{
		{"feature", false, "FD-042", "FD-042"},
		{"workspace", true, "", "gummi run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sockPath := filepath.Join(t.TempDir(), "engine.sock")
			ln, err := new(net.ListenConfig).Listen(context.Background(), "unix", sockPath)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					req, err := mcp.Decode(sc.Bytes())
					if err != nil {
						continue
					}
					if req.Method == "hello" {
						_ = mcp.Encode(conn, mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: json.RawMessage(`{}`)})
					}
				}
			}()

			t.Setenv("GUMMI_MCP_SOCK", sockPath)
			var c *cobra.Command
			if tc.workspace {
				c = workspaceMCPCmd(t, true)
			} else {
				c = freshMCPCmd(t)
				if err := c.Flags().Set("feature", tc.featureID); err != nil {
					t.Fatal(err)
				}
			}

			stdinR, stdinW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			stdoutR, stdoutW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			origStdin, origStdout := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = stdinR, stdoutW
			t.Cleanup(func() { os.Stdin, os.Stdout = origStdin, origStdout })

			done := make(chan error, 1)
			go func() { done <- runMCP(c, nil) }()

			if _, err := io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"); err != nil {
				t.Fatal(err)
			}

			respLine, err := bufio.NewReader(stdoutR).ReadString('\n')
			if err != nil {
				t.Fatalf("reading response: %v", err)
			}
			if err := stdinW.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("runMCP: %v", err)
			}

			var resp struct {
				Result struct {
					Instructions string `json:"instructions"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(respLine), &resp); err != nil {
				t.Fatalf("response not JSON: %v\n%s", err, respLine)
			}
			if resp.Result.Instructions == "" {
				t.Fatal("instructions empty")
			}
			if !strings.Contains(resp.Result.Instructions, tc.wantSub) {
				t.Errorf("instructions missing %q:\n%s", tc.wantSub, resp.Result.Instructions)
			}
		})
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

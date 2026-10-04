package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The live antigravity tests drive the real agy binary. They spend real
// quota, so they are gated on a dedicated opt-in (the GUMMI_CODEX_TEST /
// GUMMI_CLAUDE_TEST pattern) and never on GUMMI_ANTIGRAVITY_BIN alone: a
// developer setting that var to run the backend on their board must not
// get paid live turns on plain `go test ./internal/agent`. Nothing in CI
// sets the opt-in; the verification stage does.

// antigravityLiveGate skips the caller unless the opt-in is set AND an
// agy binary resolves AND the operator's own home carries a token, which
// is what keeps the spawned sessions from failing auth for want of
// credentials.
func antigravityLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("GUMMI_ANTIGRAVITY_TEST") != "1" {
		t.Skip("set GUMMI_ANTIGRAVITY_TEST=1 to test against the real agy CLI (spends quota)")
	}
	bin := os.Getenv("GUMMI_ANTIGRAVITY_BIN")
	if bin == "" {
		bin = "agy"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not on PATH", bin)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")); err != nil {
		t.Skip("the operator's home carries no agy OAuth token; log in with `agy` first")
	}
}

// antigravityLiveReply sends one turn and returns the joined reply text,
// ending at idle or error.
func antigravityLiveReply(t *testing.T, sess Session, prompt string) string {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatalf("send %q: %v", prompt, err)
	}
	var text strings.Builder
	deadline := time.After(150 * time.Second)
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				return text.String()
			}
			switch e.Kind {
			case EventTextDelta, EventMessage:
				text.WriteString(e.Text)
			case EventIdle:
				return text.String()
			case EventError:
				t.Fatalf("real agy turn errored (%q): %v", prompt, e.Err)
			}
		case <-deadline:
			t.Fatalf("timed out awaiting idle for %q", prompt)
		}
	}
}

// TestAntigravityLiveTurn: one real turn round-trips — text streams, a
// non-zero usage delta lands, the turn ends idle, and agy reports a
// conversation id (the resume anchor).
func TestAntigravityLiveTurn(t *testing.T) {
	antigravityLiveGate(t)
	ag, err := NewAntigravity("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(context.Background(), "Reply with exactly PONG"); err != nil {
		t.Fatal(err)
	}
	var usage bool
	var text strings.Builder
	deadline := time.After(150 * time.Second)
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				t.Fatal("events closed before the turn ended")
			}
			switch e.Kind {
			case EventTextDelta, EventMessage:
				text.WriteString(e.Text)
			case EventUsage:
				if e.Usage.OutputTokens == 0 && e.Usage.InputTokens == 0 && e.Usage.Credits == 0 {
					t.Errorf("usage delta = %+v, want non-zero token counts", e.Usage)
				}
				usage = true
			case EventIdle:
				if !strings.Contains(text.String(), "PONG") {
					t.Errorf("reply = %q, want PONG", text.String())
				}
				if !usage {
					t.Error("the turn ended without an EventUsage")
				}
				if id, ok := sess.(Identified); !ok || id.SessionID() == "" {
					t.Errorf("session id = %v, want agy's conversation id", id)
				}
				return
			case EventError:
				t.Fatalf("live turn errored: %v", e.Err)
			}
		case <-deadline:
			t.Fatal("timed out awaiting the live turn's idle")
		}
	}
}

// TestAntigravityLiveResume: session A answers, closes; session B opens
// with A's conversation id and answers a question about the earlier
// exchange — continuity, not a fresh conversation.
func TestAntigravityLiveResume(t *testing.T) {
	antigravityLiveGate(t)
	ag, err := NewAntigravity("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()

	a, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: scratch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := antigravityLiveReply(t, a, "Remember the codeword MARMALADE and reply with exactly ACK."); !strings.Contains(got, "ACK") {
		t.Fatalf("session A reply = %q", got)
	}
	id := ""
	if i, ok := a.(Identified); ok {
		id = i.SessionID()
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("session A reported no conversation id to resume")
	}

	b, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: scratch, ResumeID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := antigravityLiveReply(t, b, "What was the codeword? Reply with the word only."); !strings.Contains(got, "MARMALADE") {
		t.Errorf("resumed session reply = %q, want the remembered codeword", got)
	}
}

// TestAntigravityLiveResumeUsageBaseline: the unit-level resume-baseline
// check's live sibling — the first post-resume turn emits no EventUsage
// for the pre-restart tokens (the baseline is set silently), and a later
// turn emits a positive delta.
func TestAntigravityLiveResumeUsageBaseline(t *testing.T) {
	antigravityLiveGate(t)
	ag, err := NewAntigravity("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()

	// Session A: one turn whose usage is collected in the same loop that
	// watches for the turn's end (the reply helper below consumes events
	// until idle, which would starve a follow-up loop).
	a, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: scratch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(context.Background(), "Say something brief about the moon."); err != nil {
		t.Fatal(err)
	}
	var prevTotal int64
	var sawUsage bool
	aDeadline := time.After(150 * time.Second)
closed:
	for {
		select {
		case e, ok := <-a.Events():
			if !ok {
				break closed
			}
			switch e.Kind {
			case EventUsage:
				sawUsage = true
				prevTotal += e.Usage.InputTokens + e.Usage.OutputTokens
			case EventIdle:
				break closed
			case EventError:
				t.Fatalf("session A errored: %v", e.Err)
			}
		case <-aDeadline:
			t.Fatal("timed out awaiting session A's idle")
		}
	}
	if !sawUsage {
		t.Fatal("session A emitted no usage; the baseline comparison is meaningless")
	}
	id := ""
	if i, ok := a.(Identified); ok {
		id = i.SessionID()
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: scratch, ResumeID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Send(context.Background(), "Reply with exactly OK."); err != nil {
		t.Fatal(err)
	}
	// The first post-resume result sets the baseline and must emit no
	// EventUsage — any usage here would re-count the pre-restart
	// conversation (prev total above).
	deadline := time.After(150 * time.Second)
	for {
		select {
		case e, ok := <-b.Events():
			if !ok {
				return
			}
			switch e.Kind {
			case EventUsage:
				t.Fatalf("first post-resume turn emitted EventUsage %+v (prev total %d)", e.Usage, prevTotal)
			case EventIdle:
				goto second
			case EventError:
				t.Fatalf("live resumed turn errored: %v", e.Err)
			}
		case <-deadline:
			t.Fatal("timed out awaiting the resumed turn's idle")
		}
	}
second:
	// The next turn's delta is what metering counts from then on.
	if err := b.Send(context.Background(), "Reply with exactly DONE."); err != nil {
		t.Fatal(err)
	}
	var sawDelta bool
	bDeadline := time.After(150 * time.Second)
	for {
		select {
		case e, ok := <-b.Events():
			if !ok {
				if !sawDelta {
					t.Error("the second post-resume turn ended without a usage delta")
				}
				return
			}
			switch e.Kind {
			case EventUsage:
				if e.Usage.InputTokens+e.Usage.OutputTokens <= 0 {
					t.Errorf("second post-resume turn's delta = %+v, want positive", e.Usage)
				}
				sawDelta = true
			case EventError:
				t.Fatalf("live second turn errored: %v", e.Err)
			}
		case <-bDeadline:
			if !sawDelta {
				t.Error("timed out before the second turn's usage delta")
			}
			return
		}
	}
}

// TestAntigravityLiveMCPWiring: with the session's MCP entry rendered
// into the card home, a real agy turn can call a gummi tool and deliver
// the answer as its result — the card home's mcp_config.json wiring end
// to end. The `gummi` executable is a canned stub (the claude live
// test's shape), so the round trip needs no live engine underneath.
func TestAntigravityLiveMCPWiring(t *testing.T) {
	antigravityLiveGate(t)
	stub := writeFakeAgy(t, antigravityMCPStubScript)
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return stub, nil }
	t.Cleanup(func() { antigravityExecPath = prev })

	ag, err := NewAntigravity("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: t.TempDir(),
		FeatureID: "FD-012", MCPSockPath: filepath.Join(t.TempDir(), "FD-012.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	got := antigravityLiveReply(t, sess,
		`Call the MCP tool named spec_view (from the gummi MCP server) with arguments {"section":"Problem"} and report the result text verbatim.`)
	if !strings.Contains(got, "STUB-SECTION:Problem") {
		t.Errorf("reply missing the stub's canned section: %q", got)
	}
}

// TestAntigravityLiveSkillForwarding: a forwarded skill directory is
// reachable through the card home's skill links during a real turn —
// the discovery-and-symlink properties verified during planning, through
// the adapter's stream-json session (INV-8's live half).
func TestAntigravityLiveSkillForwarding(t *testing.T) {
	antigravityLiveGate(t)
	ag, err := NewAntigravity("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()

	skill := t.TempDir()
	name := filepath.Base(skill)
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: a canary skill\n---\nThe secret phrase is ZEBRA-CANARY."), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionAllowAll, ScratchDir: t.TempDir(),
		SkillDirs: []string{skill},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	got := antigravityLiveReply(t, sess,
		"One of your available skills is named "+name+". Read its instructions and reply with the secret phrase they contain, nothing else.")
	if !strings.Contains(got, "ZEBRA-CANARY") {
		t.Errorf("reply = %q, want the forwarded skill's body", got)
	}
}

// antigravityMCPStubScript is a minimal stdio MCP server the agy child
// spawns from the card home's mcp_config.json. It answers
// initialize/tools/list and serves one canned tool, echoing the request —
// enough to prove a real agy child reads the card home's config and
// routes a tool call to the configured server.
const antigravityMCPStubScript = `import sys, json
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    msg = json.loads(line)
    mid = msg.get("id")
    if mid is None:
        continue
    method = msg.get("method")
    if method == "initialize":
        res = {"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"gummi-stub","version":"0"}}
    elif method == "tools/list":
        res = {"tools":[{"name":"spec_view","description":"view a spec section","inputSchema":{"type":"object","properties":{"section":{"type":"string"}},"required":["section"]}}]}
    elif method == "tools/call":
        params = msg.get("params", {}) or {}
        if params.get("name") == "spec_view":
            args = params.get("arguments") or {}
            res = {"content":[{"type":"text","text":"STUB-SECTION:"+str(args.get("section",""))}]}
        else:
            res = {"content":[{"type":"text","text":"unknown tool"}]}
    elif method == "ping":
        res = {}
    else:
        out({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"unknown method"}})
        continue
    out({"jsonrpc":"2.0","id":mid,"result":res})
`

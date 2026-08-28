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

// fakeAgentScript is a minimal headless agent: it reads the JSON protocol
// on stdin and, for each send, emits a tool call, a message echoing the
// init model + cwd + turn text, a usage line, and idle.
const fakeAgentScript = `import sys, json, os
model=""; workdir=""
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    m=json.loads(line)
    t=m.get("type")
    if t=="init":
        model=m.get("model",""); workdir=m.get("workdir","")
    elif t=="send":
        print(json.dumps({"type":"tool","name":"read","detail":"docs/spec.md"}), flush=True)
        print(json.dumps({"type":"message","text":"model=%s cwd=%s text=%s"%(model, os.getcwd(), m.get("text",""))}), flush=True)
        print(json.dumps({"type":"usage","credits":2,"output":10,"model":model}), flush=True)
        print(json.dumps({"type":"idle"}), flush=True)
    elif t=="interrupt":
        print(json.dumps({"type":"idle"}), flush=True)
`

func writeFakeAgent(t *testing.T, body string) []string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 not available: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.py")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{py, path}
}

// collect drains events until an idle/error/stop, with a deadline.
func collect(t *testing.T, sess Session) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				return out
			}
			out = append(out, e)
			if e.Kind == EventIdle || e.Kind == EventError {
				return out
			}
		case <-deadline:
			t.Fatal("timed out collecting events")
		}
	}
}

func TestHeadlessRoundTrip(t *testing.T) {
	argv := writeFakeAgent(t, fakeAgentScript)
	ag, err := NewHeadless(argv)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	caps := ag.Capabilities()
	if !caps.Interrupt || !caps.UsageEvents || !caps.ClientTools {
		t.Errorf("headless capabilities = %+v", caps)
	}

	wd := t.TempDir()
	wd, _ = filepath.EvalSymlinks(wd)
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: wd, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)

	var kinds []EventKind
	var msg string
	var usage Usage
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		if e.Kind == EventMessage {
			msg = e.Text
		}
		if e.Kind == EventUsage {
			usage = e.Usage
		}
	}
	// the mapped event stream: tool → message → usage → idle
	want := []EventKind{EventToolCall, EventMessage, EventUsage, EventIdle}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	if evs[0].Tool != "read" || evs[0].Detail != "docs/spec.md" {
		t.Errorf("tool event = %+v, want read docs/spec.md", evs[0])
	}
	// the message proves init carried the model and the process ran in the
	// worktree, and that the turn text arrived
	if !strings.Contains(msg, "model=test-model") || !strings.Contains(msg, "cwd="+wd) || !strings.Contains(msg, "text=hello") {
		t.Errorf("message did not reflect init/cwd/turn: %q (wd=%s)", msg, wd)
	}
	if usage.Credits != 2 || usage.OutputTokens != 10 || usage.Model != "test-model" {
		t.Errorf("usage = %+v, want credits 2 / 10 out / model test-model", usage)
	}
}

// askAgentScript emits an ask_user client-tool call, then waits for the
// resolve frame and echoes the result back as a message before idling.
const askAgentScript = `import sys, json
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    m=json.loads(line)
    t=m.get("type")
    if t=="send":
        print(json.dumps({"type":"ask","id":"q1","ask":{"question":"Where?","options":[{"label":"a"},{"label":"b"}]}}), flush=True)
    elif t=="resolve":
        print(json.dumps({"type":"message","text":"resolved=%s id=%s"%(m.get("result",""), m.get("id",""))}), flush=True)
        print(json.dumps({"type":"idle"}), flush=True)
`

func TestHeadlessAskResolveRoundTrip(t *testing.T) {
	argv := writeFakeAgent(t, askAgentScript)
	ag, err := NewHeadless(argv)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	if !ag.Capabilities().ClientTools {
		t.Error("headless should advertise ClientTools")
	}

	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir: t.TempDir(), Model: "m",
		Tools: []ToolDef{{Name: "ask_user", Description: "ask"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	// first event is the ask
	var call *ToolCall
	deadline := time.After(5 * time.Second)
awaitAsk:
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventClientToolCall {
				call = e.ToolCall
				break awaitAsk
			}
		case <-deadline:
			t.Fatal("no client-tool call received")
		}
	}
	if call == nil || call.Name != "ask_user" || call.ID != "q1" {
		t.Fatalf("unexpected ask call: %+v", call)
	}
	if !strings.Contains(string(call.Args), "Where?") {
		t.Errorf("ask args missing question: %s", call.Args)
	}

	// resolve it; the child echoes the result back
	r, ok := sess.(ToolResolver)
	if !ok {
		t.Fatal("headless session is not a ToolResolver")
	}
	if err := r.Resolve(ctx, "q1", "a"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	var msg string
	for _, e := range evs {
		if e.Kind == EventMessage {
			msg = e.Text
		}
	}
	if !strings.Contains(msg, "resolved=a") || !strings.Contains(msg, "id=q1") {
		t.Errorf("resolve not echoed: %q", msg)
	}
}

func TestHeadlessMalformedLineIsError(t *testing.T) {
	argv := writeFakeAgent(t, `import sys
sys.stdout.write("not json\n"); sys.stdout.flush()
for line in sys.stdin: pass
`)
	ag, _ := NewHeadless(argv)
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	evs := collect(t, sess)
	if len(evs) == 0 || evs[0].Kind != EventError {
		t.Fatalf("malformed line did not surface as an error: %v", evs)
	}
}

func TestHeadlessEmptyCommand(t *testing.T) {
	if _, err := NewHeadless(nil); err == nil {
		t.Error("empty argv should error")
	}
}

func TestHeadlessCloseClosesEvents(t *testing.T) {
	argv := writeFakeAgent(t, fakeAgentScript)
	ag, _ := NewHeadless(argv)
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	sess.Close()
	select {
	case _, ok := <-sess.Events():
		if ok {
			// drain any buffered event, then it must close
			for range sess.Events() {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events channel did not close after Close")
	}
}

func kindStrings(ks []EventKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// A ReadOnly research session must never run on a backend that cannot
// structurally strip its write tools: headless advertises
// ReadOnlyEnforce:false, so NewSession refuses instead of silently running
// read-write over the main checkout.
func TestHeadlessRejectsReadOnly(t *testing.T) {
	ag, err := NewHeadless([]string{"sh", "-c", "cat"})
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	if _, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), ReadOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReadOnly session error = %v, want a clear read-only rejection", err)
	}
}

// TestHeadlessIgnoresResumePath proves ResumePath — engine-stamped on
// every stage session regardless of backend (FD-104) — is a no-op for a
// backend that never reads it: session start succeeds exactly as without
// it, and nothing is written at the given path.
func TestHeadlessIgnoresResumePath(t *testing.T) {
	ag, err := NewHeadless([]string{"sh", "-c", "cat"})
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	resumePath := filepath.Join(t.TempDir(), "would-be-resume.jsonl")
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "m", ResumePath: resumePath})
	if err != nil {
		t.Fatalf("NewSession with ResumePath set: %v", err)
	}
	defer sess.Close()
	if _, err := os.Stat(resumePath); !os.IsNotExist(err) {
		t.Fatalf("headless adapter touched ResumePath %s: stat err = %v", resumePath, err)
	}
}

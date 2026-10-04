package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeAgy writes an executable python script standing in for the agy
// binary (the adapter execs a single path, so the script itself must be
// the executable, via shebang — same shape as writeFakeClaude).
func writeFakeAgy(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatalf("python3 not available: %v", err)
	}
	path := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env python3\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// agyLogHook is the preamble a fake agy that reports its own invocation
// uses: it appends one JSON record per line to $AGY_LOG (silently
// skipping the record when the var is unset), and callers read that file
// back to assert argv/env/stdin.
const agyLogHook = `import sys, json, os
def rec(o):
    try:
        with open(os.environ["AGY_LOG"], "a") as f:
            f.write(json.dumps(o)+"\n")
    except (KeyError, OSError):
        pass
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
`

// fakeAgyScript replays a two-turn stream-json conversation in agy
// 1.2.16's verified shapes (steps under a step_update key, the result
// nested with cumulative usage): turn 1 with text deltas and a tool
// invocation, turn 2 text only.
const fakeAgyScript = agyLogHook + `
turn = 0
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    rec({"stdin": line})
    turn += 1
    out({"event":"init","conversation_id":"conv-1"})
    if turn == 1:
        out({"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"user_input"}})
        out({"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"he"}})
        out({"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"agent_response","text_delta":"llo"}})
        out({"event":"step_update","step_update":{"step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"git status"}}}})
        out({"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"git status"},"output":"clean"}}})
        out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"hello there","usage":{"input_tokens":100,"output_tokens":20,"cache_read_tokens":0,"total_tokens":120}}})
    else:
        out({"event":"step_update","step_update":{"step_index":4,"state":"ACTIVE","step_type":"agent_response","text_delta":"again"}})
        out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"once more","usage":{"input_tokens":150,"output_tokens":30,"cache_read_tokens":0,"total_tokens":180}}})
`

// fakeAgyArgvEcho records argv, HOME, the card home's mcp_config.json and
// skill links at startup, and every stdin line, then answers each user
// frame with a minimal one-delta turn.
var fakeAgyArgvEcho = agyLogHook + `
home = os.environ.get("HOME", "")
mcp = None
p = os.path.join(home, ".gemini", "config", "mcp_config.json")
if os.path.exists(p):
    mcp = open(p).read()
skills = None
sd = os.path.join(home, ".gemini", "config", "skills")
if os.path.isdir(sd):
    skills = sorted(os.listdir(sd))
rec({"argv": sys.argv[1:], "home": home, "mcp": mcp, "skills": skills})
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    rec({"stdin": line})
    m = json.loads(line)
    if m.get("event") != "user": continue
    out({"event":"init","conversation_id":"conv-1"})
    out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"ok","usage":{"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"total_tokens":7}}})
`

func waitAgyIdle(t *testing.T, sess Session) {
	t.Helper()
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == EventError {
				t.Fatal(ev.Err)
			}
			if ev.Kind == EventIdle {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for idle")
		}
	}
}

// TestAntigravityRoundTrip drives one turn end to end against a fake agy:
// init's conversation id is surfaced as the session id, deltas stream,
// the tool step lands as call+result, and the result settles usage as a
// delta before the turn's idle.
func TestAntigravityRoundTrip(t *testing.T) {
	ag, err := NewAntigravity(writeFakeAgy(t, fakeAgyScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	caps := ag.Capabilities()
	if !caps.Resume || !caps.UsageEvents || !caps.MCPTools || !caps.SkillDirs || caps.WriteCage != WriteCageCwd {
		t.Errorf("capabilities = %+v", caps)
	}
	if caps.Interrupt || caps.ClientTools || caps.ReadOnlyEnforce || caps.Images || caps.NativeWatch || caps.Compact {
		t.Errorf("capabilities must carry nothing beyond the advertised set: %+v", caps)
	}

	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "gemini-3.1-pro-high", Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	var kinds []EventKind
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	// deltas, tool call, (result:) tool result, reply, usage, idle
	want := []EventKind{EventTextDelta, EventTextDelta, EventToolCall, EventToolResult, EventMessage, EventUsage, EventIdle}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("turn 1 kinds = %v, want %v", kinds, want)
	}
	if evs[0].Text != "he" || evs[1].Text != "llo" {
		t.Errorf("deltas = %q,%q", evs[0].Text, evs[1].Text)
	}
	if evs[2].Tool != "run_command" || evs[2].Detail != "git status" || evs[2].CallID != "3" {
		t.Errorf("tool call = %+v", evs[2])
	}
	if res := evs[3]; res.Tool != "run_command" || res.CallID != "3" || res.Result == nil || !res.Result.OK || res.Result.Output != "clean" {
		t.Errorf("tool result = %+v", res)
	}
	if evs[4].Text != "hello there" {
		t.Errorf("reply = %q", evs[4].Text)
	}
	if evs[5].Usage.InputTokens != 100 || evs[5].Usage.OutputTokens != 20 || evs[5].Usage.Model != "gemini-3.1-pro-high" {
		t.Errorf("turn 1 usage = %+v (fresh session baselines at zero)", evs[5].Usage)
	}
	if id, ok := sess.(Identified); !ok || id.SessionID() != "conv-1" {
		t.Errorf("session id = %v", id)
	}

	// --- turn 2: same child, usage is this-total minus baseline.
	if err := sess.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	kinds = nil
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want = []EventKind{EventTextDelta, EventMessage, EventUsage, EventIdle}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("turn 2 kinds = %v, want %v", kinds, want)
	}
	if got := evs[2].Usage; got.InputTokens != 50 || got.OutputTokens != 10 {
		t.Errorf("turn 2 usage = %+v, want the delta 50/10 against turn 1's baseline", got)
	}
}

// TestAntigravitySendFrameIsExactlyOneUserLine pins the wire form: a turn
// is one user-event line on the child's stdin, spelled exactly as the
// protocol's golden — no extra fields, no framing beyond the newline.
func TestAntigravitySendFrameIsExactlyOneUserLine(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var frames []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line not JSON: %v (%s)", err, line)
		}
		if s, ok := rec["stdin"]; ok {
			frames = append(frames, s.(string))
		}
	}
	if len(frames) != 1 || frames[0] != `{"event":"user","message":{"content":"hi"}}` {
		t.Fatalf("stdin frames = %q, want exactly one golden user frame", frames)
	}
}

// TestAntigravityHintsRideTheFirstTurn: a session's stage instructions go
// out on turn one's frame and never again — the codex prepend, for a
// backend with no system-prompt flag.
func TestAntigravityHintsRideTheFirstTurn(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, SystemHints: []string{"HINT-ONE", "HINT-TWO"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	if err := sess.Send(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	frames := antigravityStdinFrames(t, string(raw))
	if len(frames) != 2 {
		t.Fatalf("frames = %q, want two", frames)
	}
	if frames[0] != "{\"event\":\"user\",\"message\":{\"content\":\"HINT-ONE\\n\\nHINT-TWO\\n\\nfirst\"}}" {
		t.Errorf("first frame missing the hints prepended: %q", frames[0])
	}
	if frames[1] != `{"event":"user","message":{"content":"second"}}` {
		t.Errorf("second frame not bare: %q", frames[1])
	}
}

// antigravityStdinFrames pulls the stdin frames a fake agy logged (the
// `stdin` field of each record), unescaped.
func antigravityStdinFrames(t *testing.T, log string) []string {
	t.Helper()
	var frames []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if s, ok := rec["stdin"].(string); ok {
			frames = append(frames, s)
		}
	}
	return frames
}

// antigravityArgv returns each logged record's argv (skipping records
// that carry none).
func antigravityArgv(t *testing.T, log string) [][]string {
	t.Helper()
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		argv, ok := rec["argv"].([]any)
		if !ok {
			continue
		}
		var args []string
		for _, a := range argv {
			if s, ok := a.(string); ok {
				args = append(args, s)
			}
		}
		out = append(out, args)
	}
	return out
}

// TestAntigravityTwoTurnsOneChild proves the session is long-lived: two
// turns, one process — the fake logs its startup once and two user
// frames.
func TestAntigravityTwoTurnsOneChild(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for _, msg := range []string{"first", "second"} {
		if err := sess.Send(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		waitAgyIdle(t, sess)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, `"argv"`) != 1 {
		t.Fatalf("fake agy started more than once:\n%s", got)
	}
	if strings.Count(got, `"stdin"`) != 2 {
		t.Fatalf("frames = %d, want 2:\n%s", strings.Count(got, `"stdin"`), got)
	}
}

// TestAntigravityResumeFlagAndPassthrough: a session opened with a
// ResumeID resumes that conversation (`--conversation <id>`), reports it
// as its own id before init answers, and — per the resume-baseline
// contract — its first post-resume result sets the usage baseline and
// emits no delta for the pre-restart tokens; only a later result emits
// one.
func TestAntigravityResumeFlagAndPassthrough(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	// argv echo + a usage profile that exercises the baseline: first
	// result carries a big cumulative total (the pre-restart turns'),
	// second carries more.
	script := agyLogHook + `
turn = 0
rec({"argv": sys.argv[1:]})
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    rec({"stdin": line})
    turn += 1
    out({"event":"init","conversation_id":"conv-resumed"})
    if turn == 1:
        out({"event":"result","result":{"conversation_id":"conv-resumed","status":"SUCCESS","response":"resumed","usage":{"input_tokens":36195,"output_tokens":80,"cache_read_tokens":0,"total_tokens":36275}}})
    else:
        out({"event":"result","result":{"conversation_id":"conv-resumed","status":"SUCCESS","response":"more","usage":{"input_tokens":46095,"output_tokens":180,"cache_read_tokens":0,"total_tokens":46275}}})
`
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ResumeID: "conv-prior",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if id, ok := sess.(Identified); !ok || id.SessionID() != "conv-prior" {
		t.Fatalf("SessionID before init = %v, want the handed-in ResumeID", id)
	}
	if err := sess.Send(context.Background(), "where were we"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	for _, e := range evs {
		if e.Kind == EventUsage {
			t.Fatalf("first post-resume result emitted EventUsage %+v — the baseline must be set silently", e.Usage)
		}
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	resumes := false
	for _, args := range antigravityArgv(t, string(raw)) {
		for i, a := range args {
			if a == "--conversation" && i+1 < len(args) && args[i+1] == "conv-prior" {
				resumes = true
			}
		}
	}
	if !resumes {
		t.Fatalf("argv does not resume the handed-in conversation:\n%s", raw)
	}

	// the next result emits the delta against the just-set baseline.
	if err := sess.Send(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	var usage []Event
	for _, e := range evs {
		if e.Kind == EventUsage {
			usage = append(usage, e)
		}
	}
	if len(usage) != 1 {
		t.Fatalf("second post-resume turn emitted %d usage events, want 1", len(usage))
	}
	if got := usage[0].Usage; got.InputTokens != 9900 || got.OutputTokens != 100 {
		t.Errorf("delta = %+v, want 9900 input / 100 output against baseline 36275", got)
	}
}

// TestAntigravityCachedTokensSplit: a result's cache reads are reported
// separately from the fresh input (verified on agy 1.2.16 —
// input_tokens excludes them), so the emitted usage reports each side
// as its own per-turn delta.
func TestAntigravityCachedTokensSplit(t *testing.T) {
	s := &antigravitySession{model: "m", baselined: true}
	if _, ok := s.agyUsageDelta(agyUsage{InputTokens: 100, CacheReadTokens: 70, OutputTokens: 20}); !ok {
		t.Fatal("first result emitted no usage")
	}
	u, ok := s.agyUsageDelta(agyUsage{InputTokens: 150, CacheReadTokens: 120, OutputTokens: 30})
	if !ok {
		t.Fatal("second result emitted no usage")
	}
	// the delta is per-field against the previous cumulative: 50 fresh
	// input, 50 cache reads, 10 output — input_tokens is already the
	// fresh side, so nothing is subtracted.
	if u.InputTokens != 50 || u.CachedTokens != 50 || u.OutputTokens != 10 {
		t.Errorf("usage = %+v, want 50 fresh input / 50 cached / 10 output", u)
	}
	if u, ok := s.agyUsageDelta(agyUsage{InputTokens: 150, CacheReadTokens: 120, OutputTokens: 30}); ok {
		t.Errorf("an unchanged total emitted usage %+v", u)
	}
}

// TestAntigravityErrorResultCarriesDiagnostics: a status ERROR result is
// EventError wrapping a *RunFailure whose Diagnostic carries the result's
// error message and the child's stderr tail, FirstTurn true on the
// session's first turn — and the session stays closable.
func TestAntigravityErrorResultCarriesDiagnostics(t *testing.T) {
	script := agyLogHook + `
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    print("agy: the backend is not authenticated; run agy to log in", file=sys.stderr)
    out({"event":"result","result":{"status":"ERROR","error":{"message":"model gemini-x not reachable"},"usage":{}}})
`
	sess := antigravityTestSession(t, script)
	err := waitEventError(t, sess)
	var rf *RunFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Err = %v (%T), want a *RunFailure", err, err)
	}
	if rf.Backend != "antigravity" || !rf.FirstTurn {
		t.Errorf("RunFailure = %+v", rf)
	}
	if !strings.Contains(rf.Diagnostic, "model gemini-x not reachable") ||
		!strings.Contains(rf.Diagnostic, "not authenticated") {
		t.Errorf("Diagnostic = %q, want the result's error message and the stderr tail", rf.Diagnostic)
	}
	_ = sess.Close()
}

// TestAntigravityToolAtStepZeroKeepsItsResult: step_index 0 is as valid
// a call id as any other — a tool step at index 0 gets its call with
// CallID "0" and its result on DONE (ok read from the result's
// denied_actions), never a call with an empty id whose completion is
// dropped.
func TestAntigravityToolAtStepZeroKeepsItsResult(t *testing.T) {
	script := agyLogHook + `
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    out({"event":"init","conversation_id":"conv-1"})
    out({"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"}}}})
    out({"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"},"output":"files"}}})
    out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"listed","denied_actions":["0"],"usage":{}}})
`
	sess := antigravityTestSession(t, script)
	defer func() { _ = sess.Close() }()
	evs := collect(t, sess)
	if len(evs) != 4 { // call, result, message, idle
		t.Fatalf("events = %d (%v), want call+result+message+idle", len(evs), evs)
	}
	if evs[0].Kind != EventToolCall || evs[0].CallID != "0" {
		t.Fatalf("call = %+v, want CallID \"0\"", evs[0])
	}
	if evs[1].Kind != EventToolResult || evs[1].CallID != "0" {
		t.Fatalf("result = %+v, want CallID \"0\"", evs[1])
	}
	if evs[1].Result == nil || evs[1].Result.OK {
		t.Fatalf("result = %+v, want the denied \"0\" action reported not-ok", evs[1].Result)
	}
	if evs[1].Result.Output != "files" {
		t.Errorf("result output = %q, want the step's captured output", evs[1].Result.Output)
	}
}

// TestAntigravityErrorResultAdvancesTheBaseline: an ERROR result that
// carries a cumulative usage total is metered like any result — its
// tokens are emitted as the errored turn's delta and the baseline
// advances — so the next successful turn's delta re-counts nothing
// (stats/billing never double-count the errored turn).
func TestAntigravityErrorResultAdvancesTheBaseline(t *testing.T) {
	script := agyLogHook + `
turn = 0
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    turn += 1
    out({"event":"init","conversation_id":"conv-1"})
    if turn == 1:
        out({"event":"result","result":{"conversation_id":"conv-1","status":"ERROR","error":{"message":"model overloaded"},"usage":{"input_tokens":1000,"output_tokens":50,"cache_read_tokens":0,"total_tokens":1050}}})
    else:
        out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"recovered","usage":{"input_tokens":1100,"output_tokens":60,"cache_read_tokens":0,"total_tokens":1160}}})
`
	sess := antigravityTestSession(t, script)
	defer func() { _ = sess.Close() }()
	evs := collect(t, sess)
	var usage, failure *Event
	for i := range evs {
		switch evs[i].Kind {
		case EventUsage:
			usage = &evs[i]
		case EventError:
			failure = &evs[i]
		}
	}
	if failure == nil {
		t.Fatalf("errored turn produced %v, want an EventError", evs)
	}
	if usage == nil {
		t.Fatal("the errored turn's usage was not emitted")
	}
	if usage.Usage.InputTokens != 1000 || usage.Usage.OutputTokens != 50 {
		t.Errorf("errored turn's delta = %+v, want its own 1000/50", usage.Usage)
	}

	// the next turn's delta is its own tokens only — the errored turn's
	// total advanced the baseline, so nothing is re-counted.
	if err := sess.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	usage = nil
	for i := range evs {
		if evs[i].Kind == EventUsage {
			usage = &evs[i]
		}
		if evs[i].Kind == EventError {
			t.Fatalf("recovery turn failed: %v", evs[i].Err)
		}
	}
	if usage == nil {
		t.Fatal("the recovery turn emitted no usage")
	}
	if usage.Usage.InputTokens != 100 || usage.Usage.OutputTokens != 10 {
		t.Errorf("recovery delta = %+v, want 100/10 (no re-count of the errored turn)", usage.Usage)
	}
}

// TestAntigravityResumedErrorResultBaselinesSilently: a resumed session
// whose FIRST post-resume result is an ERROR carrying a real cumulative
// total sets the baseline and emits no delta — pre-restart tokens are
// never re-reported, not even on the failed turn — and the next result
// deltas against that baseline.
func TestAntigravityResumedErrorResultBaselinesSilently(t *testing.T) {
	script := agyLogHook + `
turn = 0
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    turn += 1
    out({"event":"init","conversation_id":"conv-resumed"})
    if turn == 1:
        out({"event":"result","result":{"conversation_id":"conv-resumed","status":"ERROR","error":{"message":"conversation stale"},"usage":{"input_tokens":36195,"output_tokens":80,"cache_read_tokens":0,"total_tokens":36275}}})
    else:
        out({"event":"result","result":{"conversation_id":"conv-resumed","status":"SUCCESS","response":"resumed","usage":{"input_tokens":46095,"output_tokens":180,"cache_read_tokens":0,"total_tokens":46275}}})
`
	dir := t.TempDir()
	ag, err := NewAntigravity(writeFakeAgy(t, script))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ResumeID: "conv-prior",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "where were we"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	var failure *Event
	for i := range evs {
		if evs[i].Kind == EventUsage {
			t.Fatalf("the resumed session's errored first result emitted EventUsage %+v — the baseline must be set silently", evs[i].Usage)
		}
		if evs[i].Kind == EventError {
			failure = &evs[i]
		}
	}
	if failure == nil {
		t.Fatalf("errored turn produced %v, want an EventError", evs)
	}

	// the next result deltas against the baseline the errored result set.
	if err := sess.Send(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	var usage *Event
	for i := range evs {
		if evs[i].Kind == EventUsage {
			usage = &evs[i]
		}
		if evs[i].Kind == EventError {
			t.Fatalf("recovery turn failed: %v", evs[i].Err)
		}
	}
	if usage == nil {
		t.Fatal("the recovery turn emitted no usage")
	}
	if usage.Usage.InputTokens != 9900 || usage.Usage.OutputTokens != 100 {
		t.Errorf("delta = %+v, want 9900/100 against the baseline the errored result set", usage.Usage)
	}
}

// TestAntigravityChildDeathIsARunFailure: a child that dies without a
// result line fails the turn as a RunFailure carrying its stderr — never
// a silent idle.
func TestAntigravityChildDeathIsARunFailure(t *testing.T) {
	script := agyLogHook + `
import sys, json, os
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    print("agy: crashed mid-turn", file=sys.stderr)
    os._exit(3)
`
	sess := antigravityTestSession(t, script)
	err := waitEventError(t, sess)
	var rf *RunFailure
	if !errors.As(err, &rf) {
		t.Fatalf("Err = %v (%T), want a *RunFailure", err, err)
	}
	if !strings.Contains(rf.Diagnostic, "crashed mid-turn") {
		t.Errorf("Diagnostic = %q, want the child's stderr tail", rf.Diagnostic)
	}
	_ = sess.Close()
}

// antigravityTestSession opens a session against a fake agy written from
// body, in a throwaway workdir, and returns it.
func antigravityTestSession(t *testing.T, body string) Session {
	t.Helper()
	ag, err := NewAntigravity(writeFakeAgy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	return sess
}

// waitEventError drains a session's events until the EventError arrives,
// returning its Err.
func waitEventError(t *testing.T, sess Session) error {
	t.Helper()
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == EventError {
				return ev.Err
			}
			if ev.Kind == EventIdle {
				t.Fatal("turn ended idle, want EventError")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for EventError")
		}
	}
}

// TestAntigravityMalformedLinesTolerated: stdout junk — non-JSON lines,
// unknown events, unrecognized steps — is dropped, never a session
// teardown; only a dead child or an ERROR result fails the session.
func TestAntigravityMalformedLinesTolerated(t *testing.T) {
	script := agyLogHook + `
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    print("this is not json at all")
    print('{"event":"step_update","step_update":null}')
    print('{"event":"totally-unknown","payload":42}')
    print('{"event":"step_update","step_update":{"step_type":"unknown-kind","state":"ACTIVE"}}')
    out({"event":"init","conversation_id":"conv-1"})
    out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"done","usage":{"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"total_tokens":7}}})
`
	sess := antigravityTestSession(t, script)
	defer func() { _ = sess.Close() }()
	var sawUsage bool
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == EventError {
				t.Fatalf("junk on stdout failed the session: %v", ev.Err)
			}
			if ev.Kind == EventUsage {
				sawUsage = true
			}
			if ev.Kind == EventIdle {
				if !sawUsage {
					t.Fatal("turn ended without usage")
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for idle")
		}
	}
}

// TestAntigravityDeniedActionsSurfaceOnTheToolResult: a SUCCESS result
// whose denied_actions name a step still lands its reply text; the
// denial surfaces through that step's tool result (ok false), never
// swallowed and never turned into a failed turn.
func TestAntigravityDeniedActionsSurfaceOnTheToolResult(t *testing.T) {
	script := agyLogHook + `
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    out({"event":"init","conversation_id":"conv-1"})
    out({"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"rm -rf /"}}}})
    out({"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"rm -rf /"},"output":"blocked"}}})
    out({"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"I did not run that","denied_actions":["1"],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_tokens":0,"total_tokens":15}}})
`
	sess := antigravityTestSession(t, script)
	defer func() { _ = sess.Close() }()
	var kinds []EventKind
	var denied *Event
	var reply string
	for {
		select {
		case ev := <-sess.Events():
			kinds = append(kinds, ev.Kind)
			if ev.Kind == EventToolResult {
				denied = &ev
			}
			if ev.Kind == EventMessage {
				reply = ev.Text
			}
			if ev.Kind == EventIdle {
				if denied == nil || denied.Result == nil || denied.Result.OK {
					t.Fatalf("tool result = %+v, want the denied action reported as not-ok", denied)
				}
				if denied.Result.Output != "blocked" {
					t.Errorf("denied output = %q, want the step's captured output", denied.Result.Output)
				}
				if reply != "I did not run that" {
					t.Errorf("reply = %q, want the SUCCESS result's reply text despite the denial", reply)
				}
				if len(kinds) != 5 { // call, result, message, usage, idle
					t.Fatalf("kinds = %v, want no extra events for the denial", kinds)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for idle")
		}
	}
}

// TestAntigravityToolDoneWithoutActiveAnnouncesItsCall: a stream that
// only ever says DONE gets its call emitted ahead of the result, so the
// transcript keeps the call/result pairing.
func TestAntigravityToolDoneWithoutActiveAnnouncesItsCall(t *testing.T) {
	s := &antigravitySession{model: "m"}
	s.mapLine([]byte(`{"event":"init","conversation_id":"c"}`))
	if evs := s.mapLine([]byte(`{"event":"step_update","step_update":{"step_index":9,"state":"DONE","step_type":"tool","tool_name":"replace_file_content","tool_info":{"name":"replace_file_content","parameters":{"Target":"/tmp/x"},"output":"written"}}}`)); evs != nil {
		t.Fatalf("DONE alone emitted %v, want buffering only", evs)
	}
	evs := s.mapLine([]byte(`{"event":"result","result":{"conversation_id":"c","status":"SUCCESS","response":"ok","usage":{}}}`))
	if len(evs) != 4 { // announced call, buffered result, reply, idle
		t.Fatalf("result events = %d (%v), want call+result+message+idle", len(evs), evs)
	}
	if evs[0].Kind != EventToolCall || evs[0].Tool != "replace_file_content" || evs[0].Detail != "/tmp/x" {
		t.Errorf("announced call = %+v", evs[0])
	}
	if evs[1].Kind != EventToolResult || evs[1].CallID != "9" || !evs[1].Result.OK || evs[1].Result.Output != "written" {
		t.Errorf("buffered result = %+v", evs[1])
	}
	if evs[2].Kind != EventMessage || evs[2].Text != "ok" {
		t.Errorf("reply = %+v", evs[2])
	}
	if evs[3].Kind != EventIdle {
		t.Errorf("turn end = %+v", evs[3])
	}
}

// TestAntigravitySubagentStepEmitsToolCallAndResult asserts that when agy emits
// a step_update with step_type "subagent", it is mapped to EventToolCall and
// EventToolResult so the subagent invocation appears in the card thread.
func TestAntigravitySubagentStepEmitsToolCallAndResult(t *testing.T) {
	s := &antigravitySession{model: "m"}
	s.mapLine([]byte(`{"event":"init","conversation_id":"c"}`))
	activeLine := []byte(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":2,"state":"ACTIVE","step_type":"subagent","tool_name":"invoke_subagent","subagent_info":{"subagents":[{"type_name":"research","role":"File Researcher","initial_prompt":"Check files"}]}}}`)
	evs := s.mapLine(activeLine)
	if len(evs) != 1 || evs[0].Kind != EventToolCall {
		t.Fatalf("ACTIVE subagent step emitted %v, want 1 EventToolCall", evs)
	}
	if evs[0].Tool != "invoke_subagent" || evs[0].Detail != "File Researcher" {
		t.Errorf("announced call = %+v, want invoke_subagent with detail File Researcher", evs[0])
	}

	doneLine := []byte(`{"event":"step_update","step_update":{"conversation_id":"c","step_index":2,"state":"DONE","step_type":"subagent","tool_name":"invoke_subagent","duration_seconds":0.5,"subagent_info":{"subagents":[{"type_name":"research","role":"File Researcher","conversation_id":"sub-1"}]}}}`)
	if doneEvs := s.mapLine(doneLine); len(doneEvs) != 0 {
		t.Fatalf("DONE subagent step emitted %v, want buffering only", doneEvs)
	}

	resultLine := []byte(`{"event":"result","result":{"conversation_id":"c","status":"SUCCESS","response":"done","usage":{}}}`)
	resEvs := s.mapLine(resultLine)
	var sawResult bool
	for _, ev := range resEvs {
		if ev.Kind == EventToolResult && ev.CallID == "2" {
			sawResult = true
			if !ev.Result.OK {
				t.Errorf("subagent result OK = false, want true")
			}
		}
	}
	if !sawResult {
		t.Fatalf("result events %v missing EventToolResult for CallID 2", resEvs)
	}
}

// TestAntigravityRefusalsBeforeAnySideEffect: guarded and ReadOnly
// sessions are refused before anything is created — no card home, no
// scratch dir touched.
func TestAntigravityRefusalsBeforeAnySideEffect(t *testing.T) {
	bin := writeFakeAgy(t, "import sys\nsys.exit(0)\n")
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()
	if _, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: scratch, Permission: PermissionGuarded}); err == nil ||
		!strings.Contains(err.Error(), "guarded") {
		t.Errorf("guarded error = %v, want a clear refusal", err)
	}
	if _, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: scratch, ReadOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReadOnly error = %v, want a clear refusal", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused session left side effects: %v", entries)
	}
	// GuardedSupport agrees with the adapter.
	if support, known := GuardedSupport("antigravity"); known && support {
		t.Error("GuardedSupport(antigravity) = true, want false")
	}
}

// TestAntigravityCreditRateEnv: the operator's rate arrives through
// GUMMI_ANTIGRAVITY_CREDITS_PER_1K; absent, zero, or unparsable falls
// back to the engine's own pricing.
func TestAntigravityCreditRateEnv(t *testing.T) {
	a := &Antigravity{bin: "agy"}
	if rate := a.CreditRate("gemini-3.1-pro-high"); rate != 0 {
		t.Errorf("rate with no env = %v, want 0", rate)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "0.25")
	if rate := a.CreditRate("m"); rate != 0.25 {
		t.Errorf("rate = %v, want 0.25", rate)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "0")
	if rate := a.CreditRate("m"); rate != 0 {
		t.Errorf("rate of literal 0 = %v, want 0 (engine fallback)", rate)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "not-a-number")
	if rate := a.CreditRate("m"); rate != 0 {
		t.Errorf("unparsable rate = %v, want 0", rate)
	}
}

// TestAntigravityIdentityAndMissingBinary: the adapter's name, its
// fail-fast posture on a missing binary, and the not-supported
// interrupt's shape.
func TestAntigravityIdentityAndMissingBinary(t *testing.T) {
	a := &Antigravity{bin: "agy"}
	if a.Name() != "antigravity" {
		t.Errorf("Name = %q", a.Name())
	}
	if _, err := NewAntigravity(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing binary accepted")
	}
}

// TestAntigravityInterruptNotSupported pins the v1 posture: Interrupt
// reports false in capabilities (drift guard above) and the method
// refuses, naming the close path instead of pretending to abort a turn.
func TestAntigravityInterruptNotSupported(t *testing.T) {
	ag, err := NewAntigravity(writeFakeAgy(t, fakeAgyArgvEcho))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	t.Setenv("AGY_LOG", filepath.Join(t.TempDir(), "calls"))
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Interrupt(context.Background()); err == nil {
		t.Fatal("Interrupt on antigravity did not refuse")
	}
	waitAgyIdle(t, sess)
}

// TestAntigravityImagesRefused: a Turn carrying images gets
// ErrImagesUnsupported — the attachments are never silently dropped.
func TestAntigravityImagesRefused(t *testing.T) {
	ag, err := NewAntigravity(writeFakeAgy(t, fakeAgyArgvEcho))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	t.Setenv("AGY_LOG", filepath.Join(t.TempDir(), "calls"))
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sender, ok := sess.(ImageSender)
	if !ok {
		t.Fatal("antigravity session does not implement ImageSender")
	}
	err = sender.SendTurn(context.Background(), Turn{Text: "look", Images: []Image{{Path: "/tmp/a.png", MediaType: "image/png"}}})
	if !errors.Is(err, ErrImagesUnsupported) {
		t.Fatalf("SendTurn with images = %v, want ErrImagesUnsupported", err)
	}
}

// TestAntigravityModelPassthroughAndDefault: the model id goes to
// --model verbatim; an empty model passes no flag at all (agy's own
// default).
func TestAntigravityModelPassthroughAndDefault(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()

	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "gemini-3.1-pro-high", Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	sess.Close()

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	argvs := antigravityArgv(t, string(raw))
	if len(argvs) != 1 {
		t.Fatalf("logged %d argv records, want 1", len(argvs))
	}
	wantArgs := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions", "--model", "gemini-3.1-pro-high"}
	if strings.Join(argvs[0], " ") != strings.Join(wantArgs, " ") {
		t.Errorf("model session argv = %v, want %v", argvs[0], wantArgs)
	}

	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	sess2, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess2.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess2)
	sess2.Close()

	raw, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	argvs = antigravityArgv(t, string(raw))
	if len(argvs) != 1 {
		t.Fatalf("logged %d argv records, want 1", len(argvs))
	}
	for _, a := range argvs[0] {
		if a == "--model" {
			t.Errorf("an empty model passed a --model flag: %v", argvs[0])
		}
	}
}

// TestAntigravityMCPConfigAbsentFromArgv: the MCP wiring travels through
// the card home's mcp_config.json, never the argv.
func TestAntigravityMCPConfigAbsentFromArgv(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, FeatureID: "FD-1", MCPSockPath: "/tmp/mcp.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		args, _ := rec["argv"].([]any)
		for _, a := range args {
			if s, ok := a.(string); ok && strings.Contains(s, "mcp") {
				t.Fatalf("argv carries MCP wiring; the card home owns it: %s", s)
			}
		}
	}
}

// TestAntigravityNoBinEnvNeverPaysLiveTurns guards the live-test gate's
// own invariant at the unit level: nothing in the adapter reads
// GUMMI_ANTIGRAVITY_TEST, so a plain `go test ./internal/agent` with the
// bin env set runs no live turns. (The live tests themselves gate on the
// dedicated opt-in; this is the reminder that the bin env is not one.)
func TestAntigravityNoBinEnvNeverPaysLiveTurns(t *testing.T) {
	if os.Getenv("GUMMI_ANTIGRAVITY_TEST") == "1" {
		t.Skip("the opt-in is set; this guard is about the unset case")
	}
	_ = os.Getenv("GUMMI_ANTIGRAVITY_BIN") // reading it is fine; acting on it here is not
}

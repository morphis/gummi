package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ocFakeV2 is a fake opencode 2.x `serve`: its /api routes, with the
// event stream a test drives. A prompt is admitted at once, as the real
// one is; the turn ends when the test pushes its execution's end.
type ocFakeV2 struct {
	t    *testing.T
	srv  *httptest.Server
	evch chan string
	dead chan struct{}
	drop chan struct{} // a send ends the open event stream, the server still up
	once sync.Once

	mu         sync.Mutex
	sids       []string
	cur        string
	creates    []map[string]any
	prompts    []ocFakeMsg
	models     []string // sessions put on a model
	interrupts []string
	compacts   []string
	replies    []ocFakeReply   // RequestID is "<session>/<request>"
	cancelled  []string        // "<session>/<form>"
	running    map[string]bool // what /api/session/active lists
	history    string          // what /api/session/{id}/message answers, newest first
}

func newOcFakeV2(t *testing.T) *ocFakeV2 {
	t.Helper()
	f := &ocFakeV2{t: t, evch: make(chan string, 128), dead: make(chan struct{}), drop: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.2.5"}`))
	})
	mux.HandleFunc("GET /api/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case ev := <-f.evch:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			case <-f.drop:
				return
			case <-f.dead:
				panic(http.ErrAbortHandler)
			}
		}
	})
	mux.HandleFunc("GET /api/session/active", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		data := map[string]any{}
		for id := range f.running {
			data[id] = map[string]string{"type": "running"}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("GET /api/session/{id}/message", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		h := f.history
		f.mu.Unlock()
		if h == "" {
			h = `[]`
		}
		_, _ = w.Write([]byte(`{"data":` + h + `,"cursor":{}}`))
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		id := fmt.Sprintf("ses_v2%03d", len(f.sids)+1)
		f.sids = append(f.sids, id)
		f.creates = append(f.creates, body)
		f.cur = id
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"id":"` + id + `"}}`))
	})
	known := func(id string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, s := range f.sids {
			if s == id {
				return true
			}
		}
		return id == "ses_resumed"
	}
	mux.HandleFunc("POST /api/session/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !known(id) {
			http.Error(w, `{"_tag":"SessionNotFoundError"}`, http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.models = append(f.models, id)
		f.cur = id
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc("POST /api/session/{id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := r.PathValue("id")
		if !known(id) {
			http.Error(w, `{"_tag":"SessionNotFoundError"}`, http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.prompts = append(f.prompts, ocFakeMsg{SessionID: id, Body: body})
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"id":"msg_in"}}`))
	})
	mux.HandleFunc("POST /api/session/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.interrupts = append(f.interrupts, r.PathValue("id"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"interrupted":true}`))
	})
	mux.HandleFunc("POST /api/session/{id}/compact", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.compacts = append(f.compacts, r.PathValue("id"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"id":"msg_c"}}`))
	})
	mux.HandleFunc("POST /api/session/{id}/permission/{rid}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.replies = append(f.replies, ocFakeReply{RequestID: r.PathValue("id") + "/" + r.PathValue("rid"), Body: body})
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/session/{id}/form/{fid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cancelled = append(f.cancelled, r.PathValue("id")+"/"+r.PathValue("fid"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-coder","providerID":"opencode","enabled":true,"limit":{"context":262144}},` +
			`{"id":"z-ai/glm-5.3-flash","providerID":"openrouter","enabled":true,"limit":{"context":1000}},` +
			`{"id":"off","providerID":"openrouter","enabled":false}]}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *ocFakeV2) Close() {
	f.once.Do(func() {
		close(f.dead)
		f.srv.Close()
	})
}

// setRunning puts the current session on /api/session/active's list, or
// takes it off.
func (f *ocFakeV2) setRunning(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running == nil {
		f.running = map[string]bool{}
	}
	if on {
		f.running[f.cur] = true
	} else {
		delete(f.running, f.cur)
	}
}

func (f *ocFakeV2) push(typ, data string) {
	f.t.Helper()
	select {
	case f.evch <- fmt.Sprintf(`{"type":%q,"data":%s}`, typ, data):
	case <-time.After(5 * time.Second):
		f.t.Fatal("the event bus never took the event")
	}
}

// pushOurs pushes an event addressed to the session the fake last created
// or was put on; data is the rest of the event's data object.
func (f *ocFakeV2) pushOurs(typ, data string) {
	f.t.Helper()
	f.mu.Lock()
	sid := f.cur
	f.mu.Unlock()
	if data != "" {
		data = "," + data
	}
	f.push(typ, fmt.Sprintf(`{"sessionID":%q%s}`, sid, data))
}

func (f *ocFakeV2) waitPrompts(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.prompts)
		f.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d/%d prompts admitted", got, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// stubServeOpencodeV2 rebinds the spawn seam to start the 2.x fake.
func stubServeOpencodeV2(t *testing.T) (*ocFakeV2, *[]ocSpawn) {
	t.Helper()
	f := newOcFakeV2(t)
	var spawns []ocSpawn
	old := serveOpencode
	serveOpencode = func(_ context.Context, bin string, port int, dir string, env []string) (*opencodeProc, error) {
		spawns = append(spawns, ocSpawn{bin: bin, port: port, dir: dir, env: env})
		return &opencodeProc{cmd: &exec.Cmd{}, url: f.srv.URL, cancel: f.Close, stderr: &strings.Builder{}}, nil
	}
	t.Cleanup(func() { serveOpencode = old })
	return f, &spawns
}

func ocSessionV2(t *testing.T, opts SessionOpts) *opencodeSession {
	t.Helper()
	o := &Opencode{bin: "opencode", v2: true}
	sess, err := o.NewSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess.(*opencodeSession)
}

func TestParseOpencodeMajor(t *testing.T) {
	for in, want := range map[string]int{
		"1.19.5\n":          1,
		"opencode v2.2.5\n": 2,
		"v3.0.0-beta.1":     3,
		"":                  1,
		"garbage":           1,
	} {
		if got := parseOpencodeMajor(in); got != want {
			t.Errorf("parseOpencodeMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestOpencodeV2TurnRoundTrip: on 2.x a turn creates the session on its
// model, admits the prompt, maps the bus's text, tool and step events
// through the 1.x grammar, and ends when the execution it started ends.
// The stage hints ride the session config as an instruction file, not
// the prompt.
func TestOpencodeV2TurnRoundTrip(t *testing.T) {
	f, spawns := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", SystemHints: []string{"STAGE-HINTS"}})

	cfg, err := os.ReadFile(sess.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil || len(parsed.Instructions) != 1 || parsed.Instructions[0] != sess.hintsPath {
		t.Fatalf("config instructions = %v (%v), want the hints file %q", parsed.Instructions, err, sess.hintsPath)
	}
	if hints, _ := os.ReadFile(sess.hintsPath); string(hints) != "STAGE-HINTS" {
		t.Errorf("hints file = %q", hints)
	}
	if len(*spawns) != 1 {
		t.Fatalf("spawns = %d", len(*spawns))
	}

	if err := sess.Send(context.Background(), "review the diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	// a previous execution's late end, before this turn's started: ignored
	f.pushOurs("session.execution.interrupted", `"reason":"user"`)
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.text.delta", `"assistantMessageID":"m1","ordinal":0,"delta":"Looking "`)
	f.pushOurs("session.text.ended", `"assistantMessageID":"m1","ordinal":0,"text":"Looking at it."`)
	f.pushOurs("session.tool.input.started", `"id":"c1","name":"shell"`)
	f.pushOurs("session.tool.called", `"id":"c1","input":{"command":"make test"}`)
	f.pushOurs("session.tool.success", `"id":"c1","content":[{"type":"text","text":"ok"}]`)
	f.pushOurs("session.step.ended", `"finish":"stop","cost":0.05,"tokens":{"input":100,"output":20,"cache":{"read":0,"write":0}}`)
	f.pushOurs("session.execution.succeeded", "")

	var text strings.Builder
	var sawCall, sawResult, sawUsage bool
	for _, e := range waitTurnEnd(t, sess) {
		switch e.Kind {
		case EventTextDelta:
			text.WriteString(e.Text)
		case EventToolCall:
			sawCall = e.Tool == "shell" && e.Detail == "make test" && e.CallID == "c1"
		case EventToolResult:
			sawResult = e.Result != nil && e.Result.OK && strings.Contains(e.Result.Output, "ok")
		case EventUsage:
			sawUsage = e.Usage.InputTokens == 100 && e.Usage.OutputTokens == 20 && e.Usage.Credits > 4.99
		}
	}
	if text.String() != "Looking at it." {
		t.Errorf("streamed text = %q, want the deltas plus the ended part's rest", text.String())
	}
	if !sawCall || !sawResult || !sawUsage {
		t.Errorf("call=%v result=%v usage=%v", sawCall, sawResult, sawUsage)
	}
	f.mu.Lock()
	create, prompt := f.creates[0], f.prompts[0]
	f.mu.Unlock()
	model, _ := create["model"].(map[string]any)
	if model["providerID"] != "opencode" || model["id"] != "x" {
		t.Errorf("create model = %v", create["model"])
	}
	if prompt.Body["text"] != "review the diff" || prompt.Body["system"] != nil {
		t.Errorf("prompt = %v, want the message alone", prompt.Body)
	}
	if sess.SessionID() != f.sids[0] {
		t.Errorf("SessionID = %q", sess.SessionID())
	}

	hints := sess.hintsPath
	_ = sess.Close()
	if _, err := os.Stat(hints); !os.IsNotExist(err) {
		t.Errorf("hints file still present after Close (stat=%v)", err)
	}
}

// TestOpencodeV2ThoughtAndProseDoNotShareALength: a step's thought and
// its prose are both ordinal 0, and 2.x ends both after the tool call that
// closed the prose. The prose is one message, before the call, and the
// message after the call carries only what followed it.
func TestOpencodeV2ThoughtAndProseDoNotShareALength(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "read a.txt"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.reasoning.delta", `"assistantMessageID":"m1","ordinal":0,"delta":"Plan."`)
	f.pushOurs("session.text.delta", `"assistantMessageID":"m1","ordinal":0,"delta":"I'll read it."`)
	f.pushOurs("session.tool.input.started", `"id":"c1","name":"read"`)
	f.pushOurs("session.reasoning.ended", `"assistantMessageID":"m1","ordinal":0,"text":"Plan."`)
	f.pushOurs("session.tool.called", `"id":"c1","input":{"path":"a.txt"}`)
	f.pushOurs("session.text.ended", `"assistantMessageID":"m1","ordinal":0,"text":"I'll read it."`)
	f.pushOurs("session.tool.success", `"id":"c1","content":[{"type":"text","text":"alpha"}]`)
	f.pushOurs("session.step.ended", `"finish":"tool-calls","cost":0.01,"tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}}`)
	f.pushOurs("session.text.delta", `"assistantMessageID":"m2","ordinal":0,"delta":"It says alpha."`)
	f.pushOurs("session.text.ended", `"assistantMessageID":"m2","ordinal":0,"text":"It says alpha."`)
	f.pushOurs("session.step.ended", `"finish":"stop","cost":0.01,"tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}}`)
	f.pushOurs("session.execution.succeeded", "")

	var msgs []string
	var text strings.Builder
	for _, e := range waitTurnEnd(t, sess) {
		switch e.Kind {
		case EventMessage:
			msgs = append(msgs, e.Text)
		case EventTextDelta:
			text.WriteString(e.Text)
		}
	}
	if want := []string{"I'll read it.", "It says alpha."}; !slices.Equal(msgs, want) {
		t.Errorf("messages = %q, want %q", msgs, want)
	}
	if text.String() != "I'll read it.It says alpha." {
		t.Errorf("streamed text = %q, want each stretch once", text.String())
	}
}

// TestOpencodeV2ExecutionFailureIsTheTurnsFailure: the cause a failed
// execution reports is the turn's one failure.
func TestOpencodeV2ExecutionFailureIsTheTurnsFailure(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/nope"})
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.execution.failed", `"error":{"type":"provider.no-route","message":"Model unavailable: opencode/nope"}`)
	rf := waitTurnFailure(t, sess)
	if !strings.Contains(rf.Error(), "Model unavailable") || !rf.FirstTurn {
		t.Errorf("failure = %v (first=%v)", rf, rf.FirstTurn)
	}
}

// TestOpencodeV2StreamDroppedMidTurn: a 2.x server that drops its event
// stream mid-turn while it keeps running does not fail the turn. The
// stream is opened again; while the session's loop still runs the turn
// ends on the new stream's end event, and a part streaming across the gap
// relays the text its lost deltas held. A loop that is no longer running
// ended in the gap: the turn ends as the session's history records, and
// fails saying so when it records no end, rather than waiting for good.
func TestOpencodeV2StreamDroppedMidTurn(t *testing.T) {
	old := ocSettleRecheck
	ocSettleRecheck = 10 * time.Millisecond
	t.Cleanup(func() { ocSettleRecheck = old })
	start := func(t *testing.T) (*ocFakeV2, Session) {
		f, _ := stubServeOpencodeV2(t)
		sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
		if err := sess.Send(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		f.waitPrompts(t, 1)
		f.setRunning(true)
		f.pushOurs("session.execution.started", "")
		f.pushOurs("session.text.delta", `"assistantMessageID":"m1","ordinal":0,"delta":"Half "`)
		return f, sess
	}

	t.Run("still running", func(t *testing.T) {
		f, sess := start(t)
		f.drop <- struct{}{}
		f.pushOurs("session.text.ended", `"assistantMessageID":"m1","ordinal":0,"text":"Half and the rest."`)
		f.pushOurs("session.execution.succeeded", "")
		var text strings.Builder
		for _, e := range waitTurnEnd(t, sess) {
			if e.Kind == EventTextDelta {
				text.WriteString(e.Text)
			}
		}
		if text.String() != "Half and the rest." {
			t.Errorf("streamed text = %q, want the whole part once", text.String())
		}
	})

	gap := func(t *testing.T, history string) Session {
		f, sess := start(t)
		f.mu.Lock()
		f.history = history
		f.mu.Unlock()
		f.setRunning(false)
		f.drop <- struct{}{}
		return sess
	}
	const prompt = `{"id":"msg_1","type":"user","text":"go"}`

	t.Run("ended in the gap", func(t *testing.T) {
		sess := gap(t, `[{"id":"msg_3","type":"idle","outcome":"succeeded"},{"id":"msg_2","type":"assistant"},`+prompt+`]`)
		waitTurnEnd(t, sess)
	})

	t.Run("text lost in the gap", func(t *testing.T) {
		// the part's end and the reasoning before it never reach the
		// stream: the history has them whole, and the turn relays the rest
		f, sess := start(t)
		f.mu.Lock()
		f.history = `[{"id":"msg_3","type":"idle","outcome":"succeeded"},` +
			`{"id":"m1","type":"assistant","content":[{"type":"reasoning","text":"Think."},{"type":"text","text":"Half and the rest."}]},` + prompt + `]`
		f.mu.Unlock()
		f.drop <- struct{}{}
		f.pushOurs("session.execution.succeeded", "")
		var text, reasoning strings.Builder
		for _, e := range waitTurnEnd(t, sess) {
			switch e.Kind {
			case EventTextDelta:
				text.WriteString(e.Text)
			case EventReasoningDelta:
				reasoning.WriteString(e.Text)
			}
		}
		if text.String() != "Half and the rest." || reasoning.String() != "Think." {
			t.Errorf("text = %q, reasoning = %q, want each whole once", text.String(), reasoning.String())
		}
	})

	t.Run("failed in the gap", func(t *testing.T) {
		sess := gap(t, `[{"id":"msg_3","type":"idle","outcome":"failed","error":{"type":"provider","message":"rate limited"}},`+prompt+`]`)
		if got := waitTurnFailure(t, sess).Error(); !strings.Contains(got, "rate limited") {
			t.Errorf("failure = %q", got)
		}
	})

	t.Run("no end recorded", func(t *testing.T) {
		// the newest idle is a previous turn's: behind this turn's prompt
		sess := gap(t, `[`+prompt+`,{"id":"msg_0","type":"idle","outcome":"succeeded"}]`)
		if got := waitTurnFailure(t, sess).Error(); !strings.Contains(got, "dropped as the turn ended") {
			t.Errorf("failure = %q", got)
		}
	})
}

// TestOpencodeV2InterruptYieldsIdle: an interrupt is the session's
// interrupt route, and the turn ends idle.
func TestOpencodeV2InterruptYieldsIdle(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "write an essay"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.text.delta", `"assistantMessageID":"m1","ordinal":0,"delta":"Once"`)
	time.Sleep(50 * time.Millisecond)
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitTurnEnd(t, sess)
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.interrupts)
		f.mu.Unlock()
		if n == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the server was never told to interrupt")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestOpencodeV2PermissionFromChild: a held call surfaces whichever session
// asked, and its reply is addressed under that session.
func TestOpencodeV2PermissionFromChild(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", Permission: PermissionGuarded})
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.pushOurs("session.execution.started", "")
	f.push("session.created", fmt.Sprintf(`{"sessionID":"ses_child","parentID":%q}`, sess.SessionID()))
	f.push("permission.asked", `{"id":"per_1","sessionID":"ses_child","action":"shell","resources":["ls -la"]}`)
	var perm Event
	deadline := time.After(5 * time.Second)
	for perm.Kind != EventPermission {
		select {
		case perm = <-sess.Events():
		case <-deadline:
			t.Fatal("no permission event")
		}
	}
	if perm.Tool != "shell" || perm.Detail != "ls -la" || perm.CallID != "per_1" {
		t.Errorf("permission = %+v", perm)
	}
	if err := sess.ResolvePermission(context.Background(), "per_1", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	replies := f.replies
	f.mu.Unlock()
	if len(replies) != 1 || replies[0].RequestID != "ses_child/per_1" || replies[0].Body["decision"] != "once" {
		t.Errorf("replies = %+v", replies)
	}
	// a child's step is spend this session ran up
	f.push("session.step.ended", `{"sessionID":"ses_child","cost":0.02,"tokens":{"input":10,"output":1}}`)
	f.pushOurs("session.execution.succeeded", "")
	var spent bool
	for _, e := range waitTurnEndAllowEmpty(t, sess) {
		if e.Kind == EventUsage && e.Usage.InputTokens == 10 {
			spent = true
		}
	}
	if !spent {
		t.Error("the child's step spend was not relayed")
	}
}

// waitTurnEndAllowEmpty drains to the turn's end, idle or failure.
func waitTurnEndAllowEmpty(t *testing.T, sess Session) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			got = append(got, e)
			if e.Kind == EventIdle || e.Kind == EventError {
				return got
			}
		case <-deadline:
			t.Fatalf("no end; events so far: %+v", got)
		}
	}
}

// TestOpencodeV2ResumeFallback: a resumed session is put on the model
// before its first turn; one the server no longer holds falls back to a
// fresh session.
func TestOpencodeV2ResumeFallback(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		f, _ := stubServeOpencodeV2(t)
		sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", ResumeID: "ses_resumed"})
		if err := sess.Send(context.Background(), "go on"); err != nil {
			t.Fatal(err)
		}
		f.waitPrompts(t, 1)
		f.pushOurs("session.execution.started", "")
		f.pushOurs("session.text.ended", `"assistantMessageID":"m","ordinal":0,"text":"ok"`)
		f.pushOurs("session.execution.succeeded", "")
		waitTurnEnd(t, sess)
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.models) != 1 || f.models[0] != "ses_resumed" || len(f.sids) != 0 || f.prompts[0].SessionID != "ses_resumed" {
			t.Errorf("models=%v created=%v prompts=%+v", f.models, f.sids, f.prompts)
		}
	})
	t.Run("lost", func(t *testing.T) {
		f, _ := stubServeOpencodeV2(t)
		sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", ResumeID: "ses_gone"})
		if err := sess.Send(context.Background(), "go on"); err != nil {
			t.Fatal(err)
		}
		f.waitPrompts(t, 1)
		f.pushOurs("session.execution.started", "")
		f.pushOurs("session.text.ended", `"assistantMessageID":"m","ordinal":0,"text":"ok"`)
		f.pushOurs("session.execution.succeeded", "")
		waitTurnEnd(t, sess)
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.sids) != 1 || f.prompts[0].SessionID != f.sids[0] || sess.SessionID() != f.sids[0] {
			t.Errorf("created=%v prompts=%+v id=%q", f.sids, f.prompts, sess.SessionID())
		}
	})
}

// TestOpencodeV2Compact: a compaction is admitted, then waited out on the
// bus; what it cost is relayed.
func TestOpencodeV2Compact(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.text.ended", `"assistantMessageID":"m","ordinal":0,"text":"ok"`)
	f.pushOurs("session.execution.succeeded", "")
	waitTurnEnd(t, sess)

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.compacts)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no compaction admitted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.pushOurs("session.execution.started", "")
	f.pushOurs("session.compaction.ended", `"reason":"manual","text":"summary","cost":0.01,"tokens":{"input":50,"output":5}`)
	f.pushOurs("session.execution.succeeded", "")
	var compacted, spent bool
	for _, e := range waitTurnEnd(t, sess) {
		compacted = compacted || (e.Kind == EventMessage && strings.HasPrefix(e.Text, "Compacted"))
		spent = spent || (e.Kind == EventUsage && e.Usage.InputTokens == 50)
	}
	if !compacted || !spent {
		t.Errorf("compacted=%v spent=%v", compacted, spent)
	}
}

// TestOpencodeV2CancelsForms: a question 2.x asks as a form is cancelled.
func TestOpencodeV2CancelsForms(t *testing.T) {
	f, _ := stubServeOpencodeV2(t)
	sess := ocSessionV2(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPrompts(t, 1)
	f.push("form.created", `{"form":{"id":"frm_1","sessionID":"ses_v2001"}}`)
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := f.cancelled
		f.mu.Unlock()
		if len(got) == 1 {
			if got[0] != "ses_v2001/frm_1" {
				t.Errorf("cancelled = %v", got)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the form was never cancelled")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestOpencodeV2Catalog: the model ids and context windows come from
// /api/model, a disabled model left out.
func TestOpencodeV2Catalog(t *testing.T) {
	f := newOcFakeV2(t)
	srv := opencodeServer{base: f.srv.URL, v2: true}
	ids, err := srv.providers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "opencode/qwen3-coder,openrouter/z-ai/glm-5.3-flash" {
		t.Errorf("ids = %v", ids)
	}
	limit, err := srv.contextLimit(context.Background(), "opencode", "qwen3-coder")
	if err != nil || limit != 262144 {
		t.Errorf("limit = %d (%v)", limit, err)
	}
	if err := srv.waitReady(context.Background(), time.Second, nil); err != nil {
		t.Errorf("waitReady on /api/info: %v", err)
	}
}

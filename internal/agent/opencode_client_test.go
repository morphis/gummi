package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ocFake is a fake `opencode serve`: the HTTP surface the adapter speaks,
// with the event stream a test drives. It stands in for the real server
// through the serveOpencode seam, so the default suite spawns no real
// opencode process.
type ocFake struct {
	t         *testing.T
	srv       *httptest.Server
	base      string
	closeOnce sync.Once
	killed    atomic.Bool // the spawn seam's cancel ran

	evch chan string   // serialized bus event payloads, handed to the stream
	dead chan struct{} // closed at Close: parked handlers abort

	mu        sync.Mutex
	msgGate   chan struct{} // released to let a blocked message POST return
	sumGate   chan struct{} // released to let a blocked summarize POST return
	failNext  atomic.Bool   // the next message POST answers 500
	sids      []string      // sessions created, in order
	msgs      []ocFakeMsg   // message POSTs received
	aborts    []string      // aborted session ids
	responds  []ocFakeReply // permission rulings received
	summaries []string      // summarized session ids
	spawns    []ocSpawn     // what serveOpencode was asked for
	cur       string        // the session id parts are addressed with: the last created one, or a resumed one
	sseTaken  int           // bus events the SSE handler has handed off (debug aid)
}

type ocSpawn struct {
	bin  string
	port int
	dir  string
	env  []string
}

type ocFakeMsg struct {
	SessionID string
	Body      map[string]any
}

type ocFakeReply struct {
	SessionID string
	RequestID string
	Body      map[string]string
}

func newOcFake(t *testing.T) *ocFake {
	t.Helper()
	f := &ocFake{t: t, evch: make(chan string, 128), dead: make(chan struct{})}
	closed := make(chan struct{})
	close(closed)
	f.msgGate = closed
	f.sumGate = closed
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", f.eventStream)
	mux.HandleFunc("POST /session", f.create)
	mux.HandleFunc("POST /session/{id}/message", f.message)
	mux.HandleFunc("POST /session/{id}/abort", f.abort)
	mux.HandleFunc("POST /session/{id}/summarize", f.summarize)
	mux.HandleFunc("POST /session/{id}/permissions/{pid}", f.respond)
	mux.HandleFunc("GET /config/providers", f.providers)
	f.srv = httptest.NewServer(mux)
	f.base = f.srv.URL
	t.Cleanup(f.Close)
	return f
}

// Close stops the fake's server, once — the spawn seam's cancel calls it
// too, so a test that closes the session does not double-close it. The
// dead channel goes first: handlers parked on it abort their connections
// (a server dying under an in-flight request), and Close does not wait on
// them.
func (f *ocFake) Close() {
	f.closeOnce.Do(func() {
		close(f.dead)
		f.srv.Close()
	})
}

func (f *ocFake) eventStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	for {
		select {
		case ev := <-f.evch:
			f.mu.Lock()
			f.sseTaken++
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		case <-f.dead:
			panic(http.ErrAbortHandler)
		}
	}
}

func (f *ocFake) create(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("ses_fake%03d", len(f.sids)+1)
	f.sids = append(f.sids, id)
	f.cur = id
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
}

// message records the POST, then holds until the test releases the gate —
// the real server answers only once the turn resolves server-side, and a
// test pushes its bus events before that. A held POST unblocks when the
// turn's context is canceled (Interrupt, Close), the way a real one's
// connection does.
func (f *ocFake) message(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.msgs = append(f.msgs, ocFakeMsg{SessionID: r.PathValue("id"), Body: body})
	gate := f.msgGate
	f.mu.Unlock()
	if r.PathValue("id") == "ses_gone" {
		http.Error(w, `{"name":"NotFound","data":{"message":"Session not found"}}`, http.StatusNotFound)
		return
	}
	if f.failNext.Load() {
		http.Error(w, `{"name":"UnknownError","data":{"message":"provider not authenticated"}}`, http.StatusInternalServerError)
		return
	}
	select {
	case <-gate:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"info":{"id":"msg_out"},"parts":[]}`))
	case <-r.Context().Done():
	case <-f.dead:
		panic(http.ErrAbortHandler)
	}
}

func (f *ocFake) abort(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.aborts = append(f.aborts, r.PathValue("id"))
	f.mu.Unlock()
	_, _ = w.Write([]byte("true"))
}

func (f *ocFake) summarize(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.summaries = append(f.summaries, r.PathValue("id"))
	gate := f.sumGate
	f.mu.Unlock()
	select {
	case <-gate:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("true"))
	case <-r.Context().Done():
	case <-f.dead:
		panic(http.ErrAbortHandler)
	}
}

func (f *ocFake) respond(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.responds = append(f.responds, ocFakeReply{SessionID: r.PathValue("id"), RequestID: r.PathValue("pid"), Body: body})
	f.mu.Unlock()
	_, _ = w.Write([]byte("true"))
}

func (f *ocFake) providers(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"providers":[{"id":"opencode","models":{"deepseek-v4-flash":{"id":"deepseek-v4-flash"},"qwen3-coder":{"id":"qwen3-coder"}}},{"id":"openrouter","models":{"z-ai/glm-5.3-flash":{"id":"z-ai/glm-5.3-flash"}}}]}`))
}

// push puts one bus event on the stream; it fails the test rather than
// dropping an event, so an assertion never races a full buffer.
func (f *ocFake) push(ev string) {
	f.t.Helper()
	select {
	case f.evch <- ev:
	case <-time.After(5 * time.Second):
		f.t.Fatal("the event bus never took the event")
	}
}

// lastSession is the id the fake last handed out.
func (f *ocFake) lastSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sids) == 0 {
		return ""
	}
	return f.sids[len(f.sids)-1]
}

// partEvent is one finished message-part event's payload, addressed to
// the session the fake is talking to — the last one created, or the one a
// test's resumed session continues.
func (f *ocFake) partEvent(typ, id, body string) string {
	f.mu.Lock()
	sid := f.cur
	f.mu.Unlock()
	return fmt.Sprintf(`{"type":"message.part.updated","properties":{"sessionID":%q,"part":%s}}`, sid,
		partJSON(typ, id, sid, body))
}

func partJSON(typ, id, sid, body string) string {
	switch typ {
	case "text", "reasoning":
		return fmt.Sprintf(`{"id":%q,"sessionID":%q,"messageID":"msg_x","type":%q,"text":%q,"time":{"start":1,"end":2}}`, id, sid, typ, body)
	case "step-finish":
		return fmt.Sprintf(`{"id":%q,"sessionID":%q,"messageID":"msg_x","type":"step-finish","reason":"stop","cost":0.05,"tokens":{"input":100,"output":20}}`, id, sid)
	case "tool":
		return fmt.Sprintf(`{"id":%q,"sessionID":%q,"messageID":"msg_x","type":"tool","tool":"bash","callID":"c1","state":%s}`, id, sid, body)
	}
	return ""
}

// holdTurns makes the next message POST block until releaseTurn: the turn
// then resolves only when the test says so.
func (f *ocFake) holdTurns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgGate = make(chan struct{})
}

func (f *ocFake) releaseTurn() {
	f.mu.Lock()
	gate := f.msgGate
	f.mu.Unlock()
	close(gate)
}

// holdSummarize makes the next summarize POST block until releaseSummarize.
func (f *ocFake) holdSummarize() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sumGate = make(chan struct{})
}

func (f *ocFake) releaseSummarize() {
	f.mu.Lock()
	gate := f.sumGate
	f.mu.Unlock()
	close(gate)
}

// waitPosted blocks until the session's turn has POSTed its message.
func (f *ocFake) waitPosted(t *testing.T) {
	f.waitPostedN(t, 1)
}

// waitPostedN blocks until n message POSTs have been recorded.
func (f *ocFake) waitPostedN(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.msgs)
		f.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d/%d message POSTs recorded", got, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// stubServeOpencode rebinds the spawn seam to start the fake, returning
// what every spawn was asked for (binary, port, dir, env).
func stubServeOpencode(t *testing.T) (*ocFake, *[]ocSpawn) {
	t.Helper()
	f := newOcFake(t)
	old := serveOpencode
	serveOpencode = func(_ context.Context, bin string, port int, dir string, env []string) (*opencodeProc, error) {
		f.mu.Lock()
		f.spawns = append(f.spawns, ocSpawn{bin: bin, port: port, dir: dir, env: env})
		f.mu.Unlock()
		var once sync.Once
		return &opencodeProc{
			cmd:    &exec.Cmd{},
			url:    f.base,
			cancel: func() { once.Do(func() { f.killed.Store(true); f.Close() }) },
			stderr: &strings.Builder{},
		}, nil
	}
	t.Cleanup(func() { serveOpencode = old })
	return f, &f.spawns
}

// ocSession opens an opencode session against the stubbed seam.
func ocSession(t *testing.T, opts SessionOpts) *opencodeSession {
	t.Helper()
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess.(*opencodeSession)
}

// waitTurnEnd drains events until the turn ends idle, failing the test on
// a terminal error (a *RunFailure) — everything before it is returned.
func waitTurnEnd(t *testing.T, sess Session) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			got = append(got, e)
			if e.Kind == EventIdle {
				return got
			}
			if e.Kind == EventError && errors.As(e.Err, new(*RunFailure)) {
				t.Fatalf("turn failed: %v", e.Err)
			}
		case <-deadline:
			t.Fatalf("no idle; events so far: %+v", got)
		}
	}
}

// waitTurnFailure drains events until a *RunFailure arrives and returns
// it — the terminal shape a failed turn ends on, never a trailing idle.
func waitTurnFailure(t *testing.T, sess Session) *RunFailure {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventIdle {
				t.Fatal("turn ended idle; want a failure")
			}
			if e.Kind == EventError {
				var rf *RunFailure
				if errors.As(e.Err, &rf) {
					return rf
				}
			}
		case <-deadline:
			t.Fatal("no failure within the window")
		}
	}
}

// runCleanTurn sends one turn that relays a text part and resolves, and
// waits for its idle.
func runCleanTurn(t *testing.T, f *ocFake, sess *opencodeSession) {
	t.Helper()
	f.holdTurns()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p", "ok"))
	f.releaseTurn()
	waitTurnEnd(t, sess)
}

func TestOpencodeServerSpawn(t *testing.T) {
	f, spawns := stubServeOpencode(t)
	wt := t.TempDir()
	sess := ocSession(t, SessionOpts{WorkDir: wt, Model: "opencode/x", FeatureID: "FD-011"})
	if n := len(*spawns); n != 1 {
		t.Fatalf("spawns = %d, want one at NewSession", n)
	}
	sp := (*spawns)[0]
	if sp.dir != wt {
		t.Errorf("spawn dir = %q, want the session's worktree", sp.dir)
	}
	env := strings.Join(sp.env, "\n")
	if sess.configPath == "" || !strings.Contains(env, "OPENCODE_CONFIG="+sess.configPath) {
		t.Errorf("spawn env does not name the session's config path:\n%s", env)
	}
	if sp.bin != "opencode" {
		t.Errorf("spawn bin = %q", sp.bin)
	}
	if sess.srv.base != f.base {
		t.Errorf("client base = %q, want the fake server's %q", sess.srv.base, f.base)
	}
	if sess.srv.password == "" {
		t.Error("the client carries no password for the server it spawned")
	}
}

func TestOpencodeCloseKillsServer(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	path := sess.configPath
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if !f.killed.Load() {
		t.Error("Close did not kill the server process")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file still present after Close (stat=%v)", err)
	}
}

// TestOpencodeServerTurnRoundTrip: one turn is one message POST; the
// server session's id is read from the create response; the bus's parts
// map through the same grammar the CLI lines did — text, tool, usage,
// idle — and the first turn alone carries the stage hints.
func TestOpencodeServerTurnRoundTrip(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{
		WorkDir: t.TempDir(), Model: "opencode/x",
		SystemHints: []string{"STAGE-HINTS"},
	})
	f.holdTurns()
	ctx := context.Background()
	if err := sess.Send(ctx, "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p1", "Looking at the failure."))
	f.push(f.partEvent("tool", "t1", `{"status":"completed","input":{"command":"make test"},"output":"ok"}`))
	f.push(f.partEvent("step-finish", "s1", ""))
	f.releaseTurn()

	sawIdle := false
	for _, e := range waitTurnEnd(t, sess) {
		switch e.Kind {
		case EventToolCall:
			if e.Tool != "bash" || e.Detail != "make test" {
				t.Errorf("tool call = %s %q", e.Tool, e.Detail)
			}
		case EventToolResult:
			if e.Result == nil || !e.Result.OK {
				t.Errorf("tool result = %+v, want a successful one", e.Result)
			}
		case EventUsage:
			if e.Usage.InputTokens != 100 || e.Usage.OutputTokens != 20 || e.Usage.Credits < 4.99 || e.Usage.Credits > 5.01 {
				t.Errorf("usage = %+v, want in100/out20/credits~5", e.Usage)
			}
		case EventMessage:
			if e.Text != "Looking at the failure." {
				t.Errorf("final message = %q", e.Text)
			}
		case EventIdle:
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Error("turn never ended idle")
	}
	// the conversation id is the create response's, not a scraped one
	if id := sess.SessionID(); id != f.lastSession() || id == "" {
		t.Errorf("SessionID = %q, want the created %q", id, f.lastSession())
	}
	// the first turn's POST carried the model and the hints in front of
	// the prompt
	f.mu.Lock()
	msg := f.msgs[0]
	f.mu.Unlock()
	model, _ := msg.Body["model"].(map[string]any)
	if model == nil || model["providerID"] != "opencode" || model["modelID"] != "x" {
		t.Errorf("message model = %v", msg.Body["model"])
	}
	parts, _ := msg.Body["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %v", msg.Body["parts"])
	}
	text, _ := parts[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(text["text"]), "STAGE-HINTS") ||
		!strings.HasSuffix(fmt.Sprint(text["text"]), "review the staged diff") {
		t.Errorf("first turn's prompt = %q, want the hints prepended", text["text"])
	}

	// a second turn carries the plain message alone and continues the
	// same server session
	f.holdTurns()
	if err := sess.Send(ctx, "go on"); err != nil {
		t.Fatal(err)
	}
	f.waitPostedN(t, 2)
	f.push(f.partEvent("text", "p2", "Done."))
	f.releaseTurn()
	waitTurnEnd(t, sess)
	f.mu.Lock()
	msg2 := f.msgs[1]
	f.mu.Unlock()
	parts2, _ := msg2.Body["parts"].([]any)
	text2, _ := parts2[0].(map[string]any)
	if strings.Contains(fmt.Sprint(text2["text"]), "STAGE-HINTS") {
		t.Errorf("second turn's prompt = %q, want no hints", text2["text"])
	}
	if msg2.SessionID != sess.sessionIDValue() {
		t.Errorf("second turn addressed %q, want the session's own id", msg2.SessionID)
	}
}

// TestOpencodeServerImagesBecomeParts: a turn's images ride the message
// as file parts, inline as data URIs and named.
func TestOpencodeServerImagesBecomeParts(t *testing.T) {
	f, _ := stubServeOpencode(t)
	dir := t.TempDir()
	img := dir + "/shot.png"
	if err := os.WriteFile(img, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := ocSession(t, SessionOpts{WorkDir: dir, Model: "opencode/x"})
	f.holdTurns()
	sender, ok := Session(sess).(ImageSender)
	if !ok {
		t.Fatal("opencode session does not implement ImageSender")
	}
	if err := sender.SendTurn(context.Background(), Turn{
		Text:   "what is this",
		Images: []Image{{Path: img, MediaType: "image/png"}},
	}); err != nil {
		t.Fatal(err)
	}
	f.waitPostedN(t, 1)
	f.push(f.partEvent("text", "p1", "A screenshot."))
	f.releaseTurn()
	waitTurnEnd(t, sess)
	f.mu.Lock()
	parts, _ := f.msgs[0].Body["parts"].([]any)
	f.mu.Unlock()
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want a file part then the text", len(parts))
	}
	file, _ := parts[0].(map[string]any)
	if file["type"] != "file" || file["mime"] != "image/png" || file["filename"] != "shot.png" {
		t.Errorf("file part = %v", file)
	}
	uri, _ := file["url"].(string)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil || string(raw) != "PNGDATA" {
		t.Errorf("file part's payload did not decode to the attachment (err=%v, uri=%q)", err, uri[:40])
	}
}

// TestOpencodeServerInterruptYieldsIdle: "stop" aborts the message
// server-side; the turn ends idle with the partial work the bus already
// relayed — never an error.
func TestOpencodeServerInterruptYieldsIdle(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	ctx := context.Background()
	if err := sess.Send(ctx, "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p1", "working"))
	sawText := false
	deadline := time.After(5 * time.Second)
	for !sawText {
		select {
		case e := <-sess.Events():
			if e.Kind == EventTextDelta {
				sawText = true
			}
			if e.Kind == EventError {
				t.Fatalf("event before interrupt: %v", e.Err)
			}
			if e.Kind == EventIdle {
				t.Fatal("the turn ended before the interrupt")
			}
		case <-deadline:
			t.Fatal("no text event before interrupt")
		}
	}
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	sawIdle := false
	for !sawIdle {
		select {
		case e := <-sess.Events():
			switch e.Kind {
			case EventIdle:
				sawIdle = true
			case EventError:
				t.Fatalf("interrupt surfaced as error: %v", e.Err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("interrupted turn never went idle")
		}
	}
	f.releaseTurn()
	deadline = time.After(5 * time.Second)
	for {
		f.mu.Lock()
		aborts := append([]string(nil), f.aborts...)
		f.mu.Unlock()
		if len(aborts) == 1 && aborts[0] == f.lastSession() {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("aborts = %v, want one against the turn's session", aborts)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestOpencodeServerCloseMidTurnYieldsIdle: a session closed with a turn
// in flight ends that turn interrupted-idle — the same way a deliberate
// abort does, never failed.
func TestOpencodeServerCloseMidTurnYieldsIdle(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p1", "partial work"))
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	sawIdle := false
	for !sawIdle {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				t.Fatal("the event stream closed before the turn ended idle")
			}
			switch e.Kind {
			case EventIdle:
				sawIdle = true
			case EventError:
				t.Fatalf("a close mid-turn surfaced as error: %v", e.Err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the close mid-turn never yielded an idle")
		}
	}
	f.releaseTurn()
}

// The server dying on its own — the stream ends with no abort and no
// close — fails the turn, like today's truncated stream, so the
// orchestrator never advances on partial output.
func TestOpencodeServerDeathMidTurnFails(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p1", "partial"))
	f.Close()
	rf := waitTurnFailure(t, sess)
	if rf.Backend != "opencode" {
		t.Errorf("RunFailure.Backend = %q", rf.Backend)
	}
}

// TestOpencodeServerModelCatalog: the providers endpoint maps 1:1 to
// catalog ids, and the no-adapter probe answers by spawning a transient
// serve with default env and no session config, killing it after.
func TestOpencodeServerModelCatalog(t *testing.T) {
	f, spawns := stubServeOpencode(t)
	ids, err := OpencodeModelCatalog(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opencode/deepseek-v4-flash", "opencode/qwen3-coder", "openrouter/z-ai/glm-5.3-flash"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], w)
		}
	}
	if n := len(*spawns); n != 1 {
		t.Fatalf("probe spawns = %d, want one transient serve", n)
	}
	sp := (*spawns)[0]
	env := strings.Join(sp.env, "\n")
	if strings.Contains(env, "OPENCODE_CONFIG=") {
		t.Errorf("the probe's server ran with a session config:\n%s", env)
	}
	if sp.dir == "" {
		t.Error("the probe spawned with no directory at all")
	}
	if !f.killed.Load() {
		t.Error("the transient server was not killed after it answered")
	}
}

// TestOpencodeServerPermissionEvents: a guarded board's held tool call
// surfaces as an EventPermission named by the server's request id, and
// the answer goes to the server's respond endpoint — "once" for approve,
// "reject" for deny.
func TestOpencodeServerPermissionEvents(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(`{"type":"permission.asked","properties":{"id":"perm_1","sessionID":"` + f.lastSession() +
		`","permission":"bash","patterns":["make test"],"tool":{"messageID":"msg_x","callID":"c9"}}}`)
	var perm Event
	deadline := time.After(10 * time.Second)
	for perm.Kind != EventPermission {
		select {
		case e := <-sess.Events():
			if e.Kind == EventError {
				t.Fatalf("turn errored: %v", e.Err)
			}
			if e.Kind == EventPermission {
				perm = e
			}
		case <-deadline:
			t.Fatal("no permission event")
		}
	}
	if perm.Tool != "bash" || perm.Detail != "make test" || perm.CallID != "perm_1" {
		t.Errorf("permission event = %+v, want bash/make test/perm_1", perm)
	}
	resolver, ok := Session(sess).(PermissionResolver)
	if !ok {
		t.Fatal("opencode session does not implement PermissionResolver")
	}
	if err := resolver.ResolvePermission(context.Background(), "perm_1", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	responds := append([]ocFakeReply(nil), f.responds...)
	f.mu.Unlock()
	if len(responds) != 1 || responds[0].RequestID != "perm_1" || responds[0].Body["response"] != "once" {
		t.Errorf("responds = %+v, want perm_1 approved once", responds)
	}
	if responds[0].SessionID != f.lastSession() {
		t.Errorf("respond targeted session %q, want the session's own %q", responds[0].SessionID, f.lastSession())
	}
	if err := resolver.ResolvePermission(context.Background(), "perm_1", false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	responds = f.responds
	f.mu.Unlock()
	if responds[1].Body["response"] != "reject" {
		t.Errorf("deny reply = %v, want reject", responds[1].Body)
	}
	f.push(f.partEvent("text", "p", "done"))
	f.releaseTurn()
	waitTurnEnd(t, sess)
}

// TestOpencodeServerPermissionFromChildSession: a task tool's child
// session asks under its own session id, and the answer must address that
// session — the respond endpoint is session-scoped, so answering from the
// parent's id would leave the child's call held. The foreign ask still
// surfaces as the card's decision, not dropped like every other
// foreign-session bus event.
func TestOpencodeServerPermissionFromChildSession(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(`{"type":"permission.asked","properties":{"id":"perm_child","sessionID":"ses_child","permission":"bash","patterns":["make test"],"tool":{"messageID":"msg_x","callID":"c9"}}}`)
	var perm Event
	deadline := time.After(10 * time.Second)
	for perm.Kind != EventPermission {
		select {
		case e := <-sess.Events():
			if e.Kind == EventError {
				t.Fatalf("turn errored: %v", e.Err)
			}
			if e.Kind == EventPermission {
				perm = e
			}
		case <-deadline:
			t.Fatal("no permission event for the child session's ask")
		}
	}
	if perm.CallID != "perm_child" {
		t.Errorf("permission event call id = %q, want perm_child", perm.CallID)
	}
	resolver, ok := Session(sess).(PermissionResolver)
	if !ok {
		t.Fatal("opencode session does not implement PermissionResolver")
	}
	if err := resolver.ResolvePermission(context.Background(), "perm_child", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	responds := append([]ocFakeReply(nil), f.responds...)
	f.mu.Unlock()
	if len(responds) != 1 || responds[0].SessionID != "ses_child" || responds[0].RequestID != "perm_child" || responds[0].Body["response"] != "once" {
		t.Errorf("responds = %+v, want perm_child approved against ses_child", responds)
	}
	f.push(f.partEvent("text", "p", "done"))
	f.releaseTurn()
	waitTurnEnd(t, sess)
}

// TestOpencodeServerResumeFallback: a resume handed an id the server no
// longer knows never fails the turn — the first 404 drops the id and
// runs the turn again on a fresh conversation, stage hints back on the
// wire, at most once.
func TestOpencodeServerResumeFallback(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{
		WorkDir: t.TempDir(), Model: "opencode/x",
		ResumeID: "ses_gone", SystemHints: []string{"STAGE-HINTS"},
	})
	if id := sess.SessionID(); id != "ses_gone" {
		t.Fatalf("SessionID = %q, want the handed-in id before the first turn", id)
	}
	f.holdTurns()
	if err := sess.Send(context.Background(), "review the staged diff"); err != nil {
		t.Fatal(err)
	}
	// the lost attempt 404s before the gate; the fresh retry's turn does
	// not, so it resolves only when released — with the retry's own text
	// on the bus first
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.msgs)
		f.mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the fresh retry never POSTed (%d attempts seen)", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.push(f.partEvent("text", "p", "ok"))
	f.releaseTurn()
	waitTurnEnd(t, sess)
	f.mu.Lock()
	msgs := append([]ocFakeMsg(nil), f.msgs...)
	created := append([]string(nil), f.sids...)
	f.mu.Unlock()
	if len(msgs) != 2 {
		t.Fatalf("message POSTs = %d, want the lost attempt and the fresh retry", len(msgs))
	}
	if msgs[0].SessionID != "ses_gone" {
		t.Errorf("first attempt addressed %q, want the handed-in id", msgs[0].SessionID)
	}
	if msgs[1].SessionID != created[0] {
		t.Errorf("retry addressed %q, want the fresh %q", msgs[1].SessionID, created[0])
	}
	if id := sess.SessionID(); id != created[0] {
		t.Errorf("SessionID = %q, want the fresh conversation's id", id)
	}
	first, _ := msgs[0].Body["parts"].([]any)
	second, _ := msgs[1].Body["parts"].([]any)
	fst, _ := first[0].(map[string]any)
	snd, _ := second[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(fst["text"]), "STAGE-HINTS") {
		t.Errorf("the resumed attempt carried no hints: %v", fst["text"])
	}
	if !strings.Contains(fmt.Sprint(snd["text"]), "STAGE-HINTS") {
		t.Errorf("the fresh retry carried no hints: %v", snd["text"])
	}
}

// A turn that resolves with nothing relayed is a backend that died
// silently, not a real empty pass: it surfaces as a failure.
func TestOpencodeServerZeroEventTurn(t *testing.T) {
	_, _ = stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "critique the plan"); err != nil {
		t.Fatal(err)
	}
	rf := waitTurnFailure(t, sess)
	if !rf.FirstTurn {
		t.Error("RunFailure.FirstTurn = false on the session's first turn")
	}
	if !strings.Contains(strings.ToLower(rf.Error()), "no output") {
		t.Errorf("failure = %q, want the silent-backend wording", rf.Error())
	}
}

// A message POST the server refuses carries the server's own reason in
// the failure, not just a status.
func TestOpencodeServerTurnFailureCarriesDiagnostic(t *testing.T) {
	f, _ := stubServeOpencode(t)
	f.failNext.Store(true)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	rf := waitTurnFailure(t, sess)
	if !strings.Contains(rf.Error(), "provider not authenticated") {
		t.Errorf("failure = %q, want the server's own reason", rf.Error())
	}
}

// TestOpencodeServerRejectsConcurrentSend: a second Send while a turn is
// in flight is a refusal (ErrBusy), never a failure.
func TestOpencodeServerRejectsConcurrentSend(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "two"); !errors.Is(err, ErrBusy) {
		t.Errorf("second Send = %v, want ErrBusy", err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p", "ok"))
	f.releaseTurn()
	waitTurnEnd(t, sess)
}

// Pid implements OSProcess against the session's server, so a supervisor
// outside this process can find what the session owns.
func TestOpencodeServerPid(t *testing.T) {
	_, _ = stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	p, ok := Session(sess).(OSProcess)
	if !ok {
		t.Fatal("opencode session does not implement OSProcess")
	}
	// the stub's proc carries no real process: Pid reads 0 there; a real
	// spawn's pid is the server's own (integration coverage)
	if p.Pid() != 0 {
		t.Errorf("Pid = %d, want 0 with no real process behind the stub", p.Pid())
	}
}

// The readiness poll must survive a request the starting server holds
// unanswered: each poll runs on its own clock and asks again.
func TestOpencodeServerWaitReadyOutlivesAHeldRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			<-r.Context().Done() // the first poll is never answered
			return
		}
		if r.URL.Path != "/config" {
			t.Errorf("polled %s", r.URL.Path)
		}
		if u, p, _ := r.BasicAuth(); u != "opencode" || p != "pw" {
			t.Errorf("auth = %q/%q", u, p)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	o := opencodeServer{base: srv.URL, password: "pw"}
	if err := o.waitReady(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// docsBoard is a server over a real board: a git repository, a
// workspace, a store, a worktree pool and an engine on the fake agent,
// with one card at implement whose worktree carries every kind of change
// a diff can show.
type docsBoard struct {
	*harness
	c     *http.Client
	root  string
	wt    string // the card's worktree
	f     domain.Feature
	store *state.Store
	eng   *engine.Engine
	rev1  string // the worktree's first commit
}

const docsSpec = `# Dark mode

## Problem

The board is too bright at night.

## Verification

` + "```gummi-checks" + `
- name: build
  cmd: go build ./...
- name: test
  cmd: go test ./...
` + "```" + `
`

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newDocsBoard(t *testing.T, ag agent.Agent) *docsBoard {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "config", "user.name", "t")
	gitIn(t, root, "config", "user.email", "t@e.invalid")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc a() {}\n\n// end\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "bye\n")
	writeFile(t, filepath.Join(root, "old.md"), "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	writeFile(t, filepath.Join(root, "logo.png"), "\x89PNG\x00\x01\x02")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mgr, err := worktree.NewManager(ctx, root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(mgr)
	eng := engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": ag, ag.Name(): ag}, Store: store, Pool: pool,
		Workspace: ws, Model: "fake-model", Persist: true,
	})
	t.Cleanup(func() { eng.Close() })

	f := domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageImplement}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	// a second card nobody has started: no document, no worktree
	later := domain.Feature{ID: "FD-002", Num: 2, Title: "Later", Slug: "later", Stage: domain.StageTodo}
	if err := store.CreateFeature(ctx, &later); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, f.WorktreePath())
	writeFile(t, filepath.Join(wt, "main.go"), "package main\n\nfunc a() { b() }\nfunc b() {}\n\n// end\n")
	writeFile(t, filepath.Join(wt, "new.txt"), "hello\n")
	gitIn(t, wt, "rm", "-q", "gone.txt")
	gitIn(t, wt, "mv", "old.md", "renamed.md")
	writeFile(t, filepath.Join(wt, "renamed.md"), "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\n")
	writeFile(t, filepath.Join(wt, "logo.png"), "\x89PNG\x00\x09\x09")
	gitIn(t, wt, "add", "-A")
	gitIn(t, wt, "commit", "-q", "-m", "work")
	rev1 := gitIn(t, wt, "rev-parse", "HEAD")

	home, _ := f.ArtifactFile(root)
	writeFile(t, home, docsSpec)

	shell := ui.NewShell(theme.GummiDark(), "v0-test")
	shell.SetCopilotHint(false)
	shell.SetMotion(false)
	shell.Attach(store, pool, ws)
	shell.AttachEngine(eng)
	shell.SetPRThreadFetcher(func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return pr.FetchReviewThreads(ctx, pr.GHBinary(), ref)
	})
	// the server does not exist yet when the hook must be set; it is
	// reached through this once it does
	var publish atomic.Pointer[func(webapi.Change)]
	shell.SetChangeHook(func(c webapi.Change) {
		if p := publish.Load(); p != nil {
			(*p)(c)
		}
	})
	bridge := ui.NewHeadless(shell)
	go func() { _ = bridge.Run() }()
	t.Cleanup(bridge.Stop)

	h := newHarness(t, func(o *Options) { o.Board = bridge })
	pub := h.srv.Publish
	publish.Store(&pub)
	b := &docsBoard{harness: h, c: h.client(), root: root, wt: wt, f: f, store: store, eng: eng, rev1: rev1}
	h.pair(b.c, "Simon")

	// wait for the board's first row load
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, _ := h.do(b.c, http.MethodGet, "/api/cards/FD-001", "")
		if res.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the board never loaded FD-001")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return b
}

// get decodes a GET's JSON body into v and returns the status.
func (b *docsBoard) get(path string, v any) int {
	b.t.Helper()
	return b.send(http.MethodGet, path, "", v)
}

func (b *docsBoard) send(method, path, body string, v any) int {
	b.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, b.http.URL+path, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", b.http.URL)
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if v != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(raw, v); err != nil {
			b.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
	return res.StatusCode
}

func pl(v any) string { raw, _ := json.Marshal(v); return string(raw) }

// The thread pages by seq, and an item that grew after it was delivered
// comes back under the same key.
func TestDocsThreadPagesAndUpsertsByKey(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	impl, verify := domain.StageImplement, domain.StageVerify
	add := func(evs ...state.CardEvent) {
		t.Helper()
		for i := range evs {
			evs[i].Feature = b.f.ID
			at = at.Add(time.Minute)
			evs[i].At = at
		}
		if err := b.store.AppendEvents(ctx, evs); err != nil {
			t.Fatal(err)
		}
	}
	add(
		state.CardEvent{Stage: impl, Kind: state.EventStageEnter, Payload: pl(threadfold.StageEnterPayload{Role: "implementer", Model: "m-1", Flavor: "stage"})},
		state.CardEvent{Stage: impl, Kind: state.EventMessage, Payload: pl(threadfold.MessagePayload{Author: "assistant", Content: "Starting **now**."})},
		state.CardEvent{Stage: impl, Kind: state.EventTool, Status: state.StatusOK, Payload: pl(state.ToolPayload{Label: "Bash  go build", Tool: "Bash", Detail: "go build"})},
	)
	var first webapi.Thread
	if st := b.get("/api/cards/FD-001/thread", &first); st != http.StatusOK {
		t.Fatalf("thread = %d", st)
	}
	if len(first.Items) != 3 || first.Items[0].T != webapi.ItemStage || first.Items[1].T != webapi.ItemMessage || first.Items[2].T != webapi.ItemActivity {
		t.Fatalf("items = %+v", first.Items)
	}
	if first.Items[1].Author != "implementer" || first.Items[1].Text != "Starting **now**." {
		t.Errorf("message = %+v", first.Items[1])
	}
	if first.Items[0].Exited {
		t.Errorf("a running stage's divider says it exited: %+v", first.Items[0])
	}
	divider, tools := first.Items[0].Key, first.Items[2].Key

	// the tool group grows, the stage exits, and verify runs its checks
	add(
		state.CardEvent{Stage: impl, Kind: state.EventTool, Status: state.StatusFail, Output: "boom", Payload: pl(state.ToolPayload{Label: "Bash  go test", Tool: "Bash", Detail: "go test"})},
		state.CardEvent{Stage: impl, Kind: state.EventStageExit, Payload: pl(threadfold.StageExitPayload{Verdict: "done", Credits: 1.5})},
		state.CardEvent{Stage: verify, Kind: state.EventStageEnter, Payload: pl(threadfold.StageEnterPayload{Role: "gummi", Flavor: "stage"})},
		state.CardEvent{Stage: verify, Kind: state.EventTool, Status: state.StatusOK, Payload: pl(state.ToolPayload{Label: "check build: pass", MS: 900})},
		state.CardEvent{Stage: verify, Kind: state.EventTool, Status: state.StatusFail, Output: "FAIL x", Payload: pl(state.ToolPayload{Label: "check test: FAIL (exit 1)"})},
	)
	var next webapi.Thread
	b.get(fmt.Sprintf("/api/cards/FD-001/thread?after=%d", first.LastSeq), &next)
	byKey := map[string]webapi.Item{}
	for _, it := range next.Items {
		if it.Seq <= first.LastSeq {
			t.Errorf("item %s (seq %d) is not newer than after=%d", it.Key, it.Seq, first.LastSeq)
		}
		byKey[it.Key] = it
	}
	if d, ok := byKey[divider]; !ok || !d.Exited || d.Verdict != "done" {
		t.Errorf("the divider did not come back settled under its key: %+v", next.Items)
	}
	if g, ok := byKey[tools]; !ok || len(g.Items) != 1 || len(g.Items[0].Tools) != 2 || g.Items[0].Tools[1].Status != "fail" || g.Items[0].Tools[1].Output != "boom" {
		t.Errorf("the tool group did not come back grown under its key: %+v", g)
	}
	if _, ok := byKey[first.Items[1].Key]; ok {
		t.Error("an item that did not change was delivered again")
	}
	var checks []webapi.CheckRun
	for _, it := range next.Items {
		if it.T == webapi.ItemVerify {
			checks = it.Checks
		}
	}
	if len(checks) != 2 || checks[0].Cmd != "go build ./..." || !checks[0].OK || checks[0].Ms != 900 ||
		checks[1].Cmd != "go test ./..." || checks[1].OK || checks[1].Status != "FAIL (exit 1)" || checks[1].Output != "FAIL x" {
		t.Errorf("checks = %+v, want build and test with the spec's commands joined", checks)
	}
	if next.LastSeq <= first.LastSeq {
		t.Errorf("lastSeq did not move: %d → %d", first.LastSeq, next.LastSeq)
	}
	var none webapi.Thread
	b.get(fmt.Sprintf("/api/cards/FD-001/thread?after=%d", next.LastSeq), &none)
	if len(none.Items) != 0 {
		t.Errorf("paging past the end returned %+v", none.Items)
	}
	if st := b.get("/api/cards/FD-001/thread?after=x", nil); st != http.StatusBadRequest {
		t.Errorf("a bad after = %d", st)
	}

	// the spec's checks carry their last result from the same log
	var sp webapi.Spec
	b.get("/api/cards/FD-001/spec", &sp)
	if len(sp.Checks) != 2 || sp.Checks[0].Last == nil || !sp.Checks[0].Last.OK || sp.Checks[1].Last == nil || sp.Checks[1].Last.OK {
		t.Errorf("spec checks = %+v", sp.Checks)
	}
}

// A running session's turns are not in the log yet: the live block
// carries them, the thread holds back the segment it is writing, and the
// thread's lastSeq stops short of what it held back.
func TestDocsLiveDuringARunningSession(t *testing.T) {
	fake := agent.NewFake("")
	fake.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		if msg != "go on" {
			// the chat's own kickoff, which opens every attached session
			return []agent.Event{{Kind: agent.EventMessage, Text: "hi there"}, {Kind: agent.EventIdle}}
		}
		// a turn that is still going: a tool in flight, a reply arriving
		return []agent.Event{
			{Kind: agent.EventToolCall, CallID: "c1", Tool: "Bash", Detail: "go test ./..."},
			{Kind: agent.EventTextDelta, Text: "Running the te"},
		}
	}
	b := newDocsBoard(t, fake)
	ctx := context.Background()
	stream := b.events(b.c, "")
	if _, err := b.eng.Attach(ctx, b.f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kickoff turn to reach the log", func() bool {
		evs, _ := b.store.Events(ctx, b.f.ID)
		for _, ev := range evs {
			if ev.Kind == state.EventMessage && strings.Contains(ev.Payload, "hi there") {
				return true
			}
		}
		return false
	})
	if err := b.eng.Send(ctx, b.f.ID, "go on"); err != nil {
		t.Fatal(err)
	}
	var live webapi.Live
	waitFor(t, "the live stream", func() bool {
		live = webapi.Live{}
		b.get("/api/cards/FD-001/live", &live)
		return live.Streaming != ""
	})
	if !live.Busy || live.Verb != "running" || live.Streaming != "Running the te" {
		t.Errorf("live = %+v, want busy running with the streaming text", live)
	}
	if live.Tool == nil || live.Tool.Tool != "Bash" || live.Tool.Detail != "go test ./..." || live.Tool.Status != "running" {
		t.Errorf("tool in flight = %+v", live.Tool)
	}
	var said []string
	for _, tn := range live.Turns {
		said = append(said, tn.Author+":"+tn.Text)
	}
	if got := strings.Join(said, "|"); !strings.HasPrefix(got, "gummi:") || !strings.Contains(got, "implementer:hi there|you:go on|tool:") {
		t.Errorf("turns = %q", got)
	}
	if live.Stage != "implement" || live.Session.IsZero() {
		t.Errorf("live head = %q at %v", live.Stage, live.Session)
	}
	stream.until("live")

	var th webapi.Thread
	b.get("/api/cards/FD-001/thread", &th)
	if n := len(th.Items); n == 0 || th.Items[n-1].T != webapi.ItemStage {
		t.Fatalf("thread items = %+v, want to end at the live stage's divider", th.Items)
	}
	evs, _ := b.store.Events(ctx, b.f.ID)
	var held int64
	for _, ev := range evs {
		if ev.Kind == state.EventMessage {
			held = ev.Seq
			break
		}
	}
	if held == 0 || th.LastSeq >= held {
		t.Errorf("lastSeq = %d, want short of the held-back turn at seq %d", th.LastSeq, held)
	}
	if th.Live == nil || th.Live.Streaming == "" {
		t.Errorf("the thread does not carry the live block: %+v", th.Live)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A note written from the page is a person's note to every rule that
// reads markers, it names who wrote it, and resolving it closes it —
// while resolving one that moved is refused.
func TestDocsSpecNotesRoundTrip(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	stream := b.events(b.c, "")
	var sp webapi.Spec
	if st := b.get("/api/cards/FD-001/spec", &sp); st != http.StatusOK {
		t.Fatalf("spec = %d", st)
	}
	if sp.None || sp.Draft || sp.Path != ".gummi/specs/FD-001-dark-mode.md" || sp.Title != "Dark mode" || sp.Rev != spec.Rev([]byte(docsSpec)) {
		t.Errorf("spec head = %+v", sp)
	}
	if len(sp.Sections) != 2 || sp.Sections[0] != (webapi.SpecSection{Name: "Problem", Line: 3}) || sp.Sections[1].Name != "Verification" {
		t.Errorf("sections = %+v", sp.Sections)
	}
	if len(sp.Checks) != 2 || sp.Checks[0] != (webapi.SpecCheck{Name: "build", Cmd: "go build ./..."}) {
		t.Errorf("checks = %+v", sp.Checks)
	}

	var after webapi.Spec
	if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes", `{"line":5,"text":"only after dark?"}`, &after); st != http.StatusOK {
		t.Fatalf("add note = %d", st)
	}
	if len(after.Notes) != 1 {
		t.Fatalf("notes = %+v", after.Notes)
	}
	n := after.Notes[0]
	if n.Line != 6 || n.Anchor != 5 || n.Author != "user" || n.By != "Simon" || n.Text != "only after dark?" || n.Resolved || n.Date == "" {
		t.Errorf("note = %+v", n)
	}
	if after.Rev == sp.Rev {
		t.Error("the revision did not move with the document")
	}
	raw, _ := os.ReadFile(filepath.Join(b.root, sp.Path))
	if d := spec.Parse(string(raw)); len(d.UserOpenThreads()) != 1 {
		t.Errorf("the note does not hold the gate as a person's comment:\n%s", raw)
	}
	if ev := stream.until("card"); !strings.Contains(ev.data, "FD-001") {
		t.Errorf("card change = %q", ev.data)
	}

	moved := fmt.Sprintf(`{"line":%d,"author":"user","date":"1999-01-01"}`, n.Line)
	if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes/resolve", moved, nil); st != http.StatusConflict {
		t.Errorf("resolving a note that is not there = %d, want 409", st)
	}
	var resolved webapi.Spec
	body := fmt.Sprintf(`{"line":%d,"author":%q,"date":%q,"reason":"yes, only then"}`, n.Line, n.Author, n.Date)
	if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes/resolve", body, &resolved); st != http.StatusOK {
		t.Fatalf("resolve = %d", st)
	}
	if len(resolved.Notes) != 2 || !resolved.Notes[0].Resolved || !strings.HasPrefix(resolved.Notes[1].Text, "resolved — yes, only then") || resolved.Notes[1].By != "Simon" {
		t.Errorf("notes after resolve = %+v", resolved.Notes)
	}
	raw, _ = os.ReadFile(filepath.Join(b.root, sp.Path))
	if d := spec.Parse(string(raw)); len(d.UserOpenThreads()) != 0 {
		t.Errorf("the resolution did not close the thread:\n%s", raw)
	}

	if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes", `{"line":999,"text":"x"}`, nil); st != http.StatusBadRequest {
		t.Errorf("a note past the end = %d, want 400", st)
	}
	if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes", `{"line":5,"text":"  "}`, nil); st != http.StatusBadRequest {
		t.Errorf("an empty note = %d, want 400", st)
	}
}

// Reading a card with no document creates nothing, unlike the TUI's
// spec surface, which writes the draft on first open.
func TestDocsSpecWithoutADocumentHasNoSideEffect(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	var sp webapi.Spec
	if st := b.get("/api/cards/FD-002/spec", &sp); st != http.StatusOK || !sp.None || sp.Why == "" {
		t.Fatalf("spec = %d %+v, want none with a reason", st, sp)
	}
	drafts, _ := os.ReadDir(filepath.Join(b.root, ".gummi", "state", "drafts"))
	for _, d := range drafts {
		if strings.HasPrefix(d.Name(), "FD-002") {
			t.Errorf("reading the spec created %s", d.Name())
		}
	}
	if st := b.send(http.MethodPost, "/api/cards/FD-002/spec/notes", `{"line":1,"text":"x"}`, nil); st != http.StatusNotFound {
		t.Errorf("a note on no document = %d, want 404", st)
	}
	var diff webapi.Diff
	if st := b.get("/api/cards/FD-002/diff", &diff); st != http.StatusOK || diff.Why == "" || len(diff.Files) != 0 {
		t.Errorf("diff of a card with no worktree = %d %+v", st, diff)
	}
}

func TestDocsDiffParsesAndAnnotates(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	var d webapi.Diff
	if st := b.get("/api/cards/FD-001/diff", &d); st != http.StatusOK {
		t.Fatalf("diff = %d", st)
	}
	if d.Base != "main" || d.Rev != b.rev1 || d.BaseRev == "" || d.Why != "" {
		t.Errorf("diff head = base %q rev %q baseRev %q why %q", d.Base, d.Rev, d.BaseRev, d.Why)
	}
	files := map[string]webapi.DiffFile{}
	for _, f := range d.Files {
		files[f.Path] = f
	}
	if f := files["main.go"]; f.Status != "modified" || f.Add != 2 || f.Del != 1 || len(f.Hunks) != 1 {
		t.Errorf("main.go = %+v", f)
	}
	if f := files["new.txt"]; f.Status != "added" || f.Add != 1 || f.Hunks[0].Lines[0] != (webapi.DiffLine{T: "+", New: 1, Text: "hello", Idx: f.Hunks[0].Lines[0].Idx}) {
		t.Errorf("new.txt = %+v", f)
	}
	if f := files["gone.txt"]; f.Status != "deleted" || f.Del != 1 || f.Hunks[0].Lines[0].Old != 1 {
		t.Errorf("gone.txt = %+v", f)
	}
	if f := files["renamed.md"]; f.Status != "renamed" || f.OldPath != "old.md" || f.Add != 1 || f.Del != 1 {
		t.Errorf("renamed.md = %+v", f)
	}
	if f := files["logo.png"]; !f.Binary || len(f.Hunks) != 0 {
		t.Errorf("logo.png = %+v", f)
	}
	var target webapi.DiffLine
	for _, l := range files["main.go"].Hunks[0].Lines {
		switch l.Text {
		case "func b() {}":
			target = l
		case "// end":
			if l.T != " " || l.Old != 5 || l.New != 6 {
				t.Errorf("context line = %+v", l)
			}
		case "func a() {}":
			if l.T != "-" || l.Old != 3 || l.New != 0 {
				t.Errorf("deleted line = %+v", l)
			}
		}
	}
	if target.T != "+" || target.New != 4 {
		t.Fatalf("added line = %+v", target)
	}

	// a comment lands on the line it was made on, under the name of who
	// made it
	var after webapi.Diff
	body := fmt.Sprintf(`{"idx":%d,"comment":"why a second func?","text":"func b() {}"}`, target.Idx)
	if st := b.send(http.MethodPost, "/api/cards/FD-001/diff/annotations", body, &after); st != http.StatusOK {
		t.Fatalf("annotate = %d", st)
	}
	if len(after.Annotations) != 1 || after.PendingComments != 1 {
		t.Fatalf("annotations = %+v", after.Annotations)
	}
	a := after.Annotations[0]
	if a.Idx != target.Idx || a.File != "main.go" || a.Excerpt != "+func b() {}" || a.Comment != "why a second func?" || a.Source != "gummi" || a.Resolved || a.By != "Simon" {
		t.Errorf("annotation = %+v", a)
	}
	stale := fmt.Sprintf(`{"idx":%d,"comment":"x","text":"not this line"}`, target.Idx)
	if st := b.send(http.MethodPost, "/api/cards/FD-001/diff/annotations", stale, nil); st != http.StatusConflict {
		t.Errorf("a comment on a moved line = %d, want 409", st)
	}

	var res webapi.Diff
	b.send(http.MethodPost, fmt.Sprintf("/api/cards/FD-001/diff/annotations/%d/resolve", a.ID), "", &res)
	if !res.Annotations[0].Resolved || res.PendingComments != 0 {
		t.Errorf("after resolve = %+v", res.Annotations)
	}
	b.send(http.MethodPost, fmt.Sprintf("/api/cards/FD-001/diff/annotations/%d/resolve", a.ID), `{"resolved":false}`, &res)
	if res.Annotations[0].Resolved || res.PendingComments != 1 {
		t.Errorf("after reopen = %+v", res.Annotations)
	}

	// a new commit: only what it changed is marked since the first
	writeFile(t, filepath.Join(b.wt, "main.go"), "package main\n\nfunc a() { b() }\nfunc b() {}\n\n// end\nfunc c() {}\n")
	gitIn(t, b.wt, "commit", "-qam", "more")
	var since webapi.Diff
	if st := b.get("/api/cards/FD-001/diff?since="+b.rev1, &since); st != http.StatusOK {
		t.Fatalf("diff since = %d", st)
	}
	if since.Since != b.rev1 || since.Rev == b.rev1 {
		t.Errorf("since = %q rev %q", since.Since, since.Rev)
	}
	var marked []string
	for _, f := range since.Files {
		if f.Since {
			marked = append(marked, f.Path)
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Since {
					marked = append(marked, f.Path+":"+l.Text)
				}
			}
		}
	}
	if got := strings.Join(marked, "|"); got != "main.go|main.go:func c() {}" {
		t.Errorf("marked since %s = %q", b.rev1[:7], got)
	}
	// the comment survived the commit on its content anchor
	if len(since.Annotations) != 1 || since.Annotations[0].Idx < 0 {
		t.Errorf("annotation after a commit = %+v", since.Annotations)
	}
	if st := b.get("/api/cards/FD-001/diff?since=--output=x", nil); st != http.StatusBadRequest {
		t.Errorf("a since that is not a commit = %d", st)
	}

	var gone webapi.Diff
	if st := b.send(http.MethodDelete, fmt.Sprintf("/api/cards/FD-001/diff/annotations/%d", a.ID), "", &gone); st != http.StatusOK || len(gone.Annotations) != 0 {
		t.Errorf("delete = %d %+v", st, gone.Annotations)
	}
	if st := b.send(http.MethodDelete, fmt.Sprintf("/api/cards/FD-001/diff/annotations/%d", a.ID), "", nil); st != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", st)
	}
}

func TestDocsPullRequestReadsThroughGH(t *testing.T) {
	gh, err := filepath.Abs("testdata/fake-gh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_GH_CMD", gh)
	logf := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("FAKE_GH_LOG", logf)
	b := newDocsBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	branch := b.f.BranchName()

	var p webapi.PR
	if st := b.get("/api/cards/FD-001/pr", &p); st != http.StatusOK {
		t.Fatalf("pr = %d", st)
	}
	if p.Linked || p.PushCommand != "git push -u origin "+branch || p.Error != "" {
		t.Errorf("unlinked pr = %+v", p)
	}

	ref := domain.PullRequestRef{Repo: "octo/demo", Number: 12, URL: "https://github.com/octo/demo/pull/12"}
	if err := b.store.SetPullRequest(ctx, b.f.ID, ref); err != nil {
		t.Fatal(err)
	}
	gitIn(t, b.root, "config", "branch."+branch+".remote", "origin")
	gitIn(t, b.root, "config", "branch."+branch+".merge", "refs/heads/their-name")
	// the unlinked answer is cached; refresh asks again
	p = webapi.PR{}
	if st := b.get("/api/cards/FD-001/pr?refresh=1", &p); st != http.StatusOK {
		t.Fatalf("pr = %d", st)
	}
	if !p.Linked || p.Ref != "octo/demo#12" || p.State != "OPEN" || p.CommentCount != 2 || p.Error != "" {
		t.Errorf("pr = %+v", p)
	}
	if p.PushCommand != "git push origin "+branch+":their-name" {
		t.Errorf("push = %q, want the tracked upstream", p.PushCommand)
	}
	if len(p.Threads) != 1 || p.Threads[0].Path != "main.go" || p.Threads[0].Line != 3 || len(p.Threads[0].Notes) != 2 || p.Threads[0].Notes[0].Author != "octo" {
		t.Errorf("threads = %+v", p.Threads)
	}
	if len(p.Comments) != 1 || p.Comments[0].Body != "thanks for this" {
		t.Errorf("comments = %+v", p.Comments)
	}
	calls := func() int { raw, _ := os.ReadFile(logf); return strings.Count(string(raw), "\n") }
	n := calls()
	b.get("/api/cards/FD-001/pr", &p)
	if calls() != n {
		t.Errorf("a second read inside the cache window ran gh again (%d → %d calls)", n, calls())
	}

	// pulling the review writes the thread onto the diff as a comment
	if st := b.send(http.MethodPost, "/api/cards/FD-001/pr/pull", "", nil); st != http.StatusAccepted {
		t.Fatalf("pull = %d", st)
	}
	var d webapi.Diff
	waitFor(t, "the pulled thread on the diff", func() bool {
		d = webapi.Diff{}
		b.get("/api/cards/FD-001/diff", &d)
		return len(d.Annotations) == 1
	})
	if a := d.Annotations[0]; a.Source != "pr" || a.By != "octo" || a.Idx < 0 {
		t.Errorf("pulled annotation = %+v", a)
	}

	// gh failing is a field, never a failed read
	t.Setenv("FAKE_GH_FAIL", "1")
	p = webapi.PR{}
	if st := b.get("/api/cards/FD-001/pr?refresh=1", &p); st != http.StatusOK || !strings.Contains(p.Error, "authentication required") || !p.Linked || p.PushCommand == "" {
		t.Errorf("pr with gh failing = %d %+v", st, p)
	}
}

func TestDocsStatsAndFleet(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	now := time.Now().UTC()
	impl := domain.StageImplement
	if err := b.store.AppendEvents(ctx, []state.CardEvent{
		{Feature: b.f.ID, Stage: impl, Kind: state.EventStageEnter, At: now.Add(-time.Hour), Payload: pl(threadfold.StageEnterPayload{Role: "implementer", Model: "m-1", Flavor: "stage"})},
		{Feature: b.f.ID, Stage: impl, Kind: state.EventMessage, At: now.Add(-50 * time.Minute), Payload: pl(threadfold.MessagePayload{Author: "assistant", Content: "done"})},
		{Feature: b.f.ID, Stage: impl, Kind: state.EventStageExit, At: now.Add(-30 * time.Minute), Payload: pl(threadfold.StageExitPayload{Verdict: "done", Credits: 2})},
	}); err != nil {
		t.Fatal(err)
	}
	var st webapi.CardStats
	if code := b.get("/api/cards/FD-001/stats", &st); code != http.StatusOK {
		t.Fatalf("stats = %d", code)
	}
	if st.ID != "FD-001" || st.Stage != "implement" || len(st.Sessions) != 1 || st.Sessions[0].Role != "implementer" || st.Sessions[0].Ended.IsZero() {
		t.Errorf("stats = %+v", st)
	}
	var fl webapi.Fleet
	if code := b.get("/api/fleet", &fl); code != http.StatusOK {
		t.Fatalf("fleet = %d", code)
	}
	if len(fl.Lanes) != 1 || fl.Lanes[0].ID != "FD-001" || len(fl.Lanes[0].Blocks) != 1 || fl.To.Sub(fl.From) != 24*time.Hour {
		t.Errorf("fleet = %+v", fl)
	}
	var old webapi.Fleet
	q := "/api/fleet?from=" + now.Add(-72*time.Hour).Format(time.RFC3339) + "&to=" + now.Add(-48*time.Hour).Format(time.RFC3339)
	if code := b.get(q, &old); code != http.StatusOK || len(old.Lanes) != 0 {
		t.Errorf("an empty window = %d %+v", code, old.Lanes)
	}
	var all webapi.Fleet
	// the whole history starts where the history does, never at the zero time
	if code := b.get("/api/fleet?from=all", &all); code != http.StatusOK || len(all.Lanes) != 1 ||
		!all.From.Equal(all.Lanes[0].Blocks[0].From) || all.AllTimeCards != 2 {
		t.Errorf("all time = %d %+v", code, all)
	}
	if code := b.get("/api/fleet?from=yesterday", nil); code != http.StatusBadRequest {
		t.Errorf("a bad from = %d", code)
	}
}

// "Request changes" is the terminal's R on the spec and diff surfaces:
// with nothing open it refuses in the board's words, and with comments
// open it sends them on and answers with what the board said.
func TestDocsRequestChanges(t *testing.T) {
	t.Run("nothing open", func(t *testing.T) {
		b := newDocsBoard(t, agent.NewFake("ok"))
		for _, what := range []string{"spec", "diff"} {
			if st := b.send(http.MethodPost, "/api/cards/FD-001/"+what+"/changes", "", nil); st != http.StatusConflict {
				t.Errorf("%s changes with nothing open = %d, want 409", what, st)
			}
			if st := b.send(http.MethodPost, "/api/cards/FD-404/"+what+"/changes", "", nil); st != http.StatusNotFound {
				t.Errorf("%s changes on no card = %d, want 404", what, st)
			}
		}
		if st := b.send(http.MethodPost, "/api/cards/FD-002/spec/changes", "", nil); st != http.StatusConflict {
			t.Errorf("spec changes on a card with no document = %d, want 409", st)
		}
	})

	t.Run("spec", func(t *testing.T) {
		b := newDocsBoard(t, agent.NewFake("ok"))
		// a note on the Verification section is nobody's in particular,
		// so the implement stage the card is in takes it
		var sp webapi.Spec
		if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes", `{"line":7,"text":"check it at night too"}`, &sp); st != http.StatusOK {
			t.Fatalf("add note = %d", st)
		}
		if sp.OpenComments != 1 {
			t.Errorf("open comments = %d, want 1", sp.OpenComments)
		}
		var out webapi.Outcome
		if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/changes", "", &out); st != http.StatusOK {
			t.Fatalf("spec changes = %d", st)
		}
		if !out.OK || !strings.Contains(out.Text, "1 review comment") {
			t.Errorf("outcome = %+v", out)
		}
	})

	// A note on the Problem is the design stage's to answer: the card goes
	// back to plan for it, and only once the person has said yes to the
	// question that says so.
	t.Run("spec back to plan", func(t *testing.T) {
		b := newDocsBoard(t, agent.NewFake("ok"))
		if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/notes", `{"line":5,"text":"only after dark?"}`, nil); st != http.StatusOK {
			t.Fatalf("add note = %d", st)
		}
		var ask webapi.Error
		if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/changes", "", &ask); st != http.StatusAccepted {
			t.Fatalf("spec changes = %d, want the 202 question", st)
		}
		if ask.Error != webapi.ConflictConfirm || ask.Confirm == "" ||
			!strings.Contains(ask.Text, "back to plan") || !strings.Contains(ask.Text, "Problem") {
			t.Fatalf("question = %+v", ask)
		}
		if f, _ := b.store.GetFeature(context.Background(), "FD-001"); f.Stage != domain.StageImplement {
			t.Fatalf("asking moved the card to %s", f.Stage)
		}
		var out webapi.Outcome
		body := fmt.Sprintf(`{"confirm":%q}`, ask.Confirm)
		if st := b.send(http.MethodPost, "/api/cards/FD-001/spec/changes", body, &out); st != http.StatusOK {
			t.Fatalf("confirmed spec changes = %d", st)
		}
		if !out.OK || !strings.Contains(out.Text, "sent back to plan") {
			t.Errorf("outcome = %+v", out)
		}
		if f, _ := b.store.GetFeature(context.Background(), "FD-001"); f.Stage != domain.StagePlan {
			t.Errorf("stage = %s, want plan", f.Stage)
		}
	})

	t.Run("diff", func(t *testing.T) {
		b := newDocsBoard(t, agent.NewFake("ok"))
		var d webapi.Diff
		b.get("/api/cards/FD-001/diff", &d)
		var target webapi.DiffLine
		for _, f := range d.Files {
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if l.Text == "func b() {}" {
						target = l
					}
				}
			}
		}
		body := fmt.Sprintf(`{"idx":%d,"comment":"why a second func?","text":"func b() {}"}`, target.Idx)
		if st := b.send(http.MethodPost, "/api/cards/FD-001/diff/annotations", body, nil); st != http.StatusOK {
			t.Fatalf("annotate = %d", st)
		}
		var out webapi.Outcome
		if st := b.send(http.MethodPost, "/api/cards/FD-001/diff/changes", "", &out); st != http.StatusOK {
			t.Fatalf("diff changes = %d", st)
		}
		if !out.OK || !strings.Contains(out.Text, "1 diff comment") {
			t.Errorf("outcome = %+v", out)
		}
	})
}

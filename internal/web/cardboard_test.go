package web

import (
	"context"
	"encoding/json"
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
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// cardBoard is the server over a real board: a git repository with a
// gummi workspace, a store, worktrees, and an engine on the fake agent —
// the board `gummi web` builds, minus the terminal.
type cardBoard struct {
	*harness
	root  string
	ws    state.Workspace
	store *state.Store
	pool  *worktree.Pool
	eng   *engine.Engine
	shell *ui.Shell
	board *ui.Bridge
	c     *http.Client
}

// newCardBoard builds the board; each setup runs on the Shell before its
// program starts and may hand back a change to the server's options.
func newCardBoard(t *testing.T, ag agent.Agent, setup ...func(*ui.Shell) func(*Options)) *cardBoard {
	t.Helper()
	// no test reaches a real opencode: a form read asks every installed
	// backend for its model catalog, and the opencode probe runs a CLI
	// when the board holds no adapter for it. The override names a
	// binary that is not there, so the probe fails fast and the picker
	// falls back to the profile ids — exactly what the assertions below
	// pin.
	t.Setenv("GUMMI_OPENCODE_BIN", "opencode-not-in-this-test")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	eng := engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": ag, ag.Name(): ag},
		Store:  store, Pool: pool, Workspace: ws, Model: "fake-model", Persist: true,
	})
	t.Cleanup(func() { eng.Close() })

	shell := ui.NewShell(theme.GummiDark(), "v0-test")
	shell.Attach(store, pool, ws)
	shell.AttachEngine(eng)
	shell.SetCopilotHint(false)
	shell.SetMotion(false)

	var mutate []func(*Options)
	for _, set := range setup {
		if m := set(shell); m != nil {
			mutate = append(mutate, m)
		}
	}
	var publish atomic.Pointer[func(webapi.Change)]
	shell.SetChangeHook(func(c webapi.Change) {
		if p := publish.Load(); p != nil {
			(*p)(c)
		}
	})
	bridge := ui.NewHeadless(shell)
	go func() { _ = bridge.Run() }()
	t.Cleanup(bridge.Stop)

	bh := &cardBoard{root: root, ws: ws, store: store, pool: pool, eng: eng, shell: shell, board: bridge}
	bh.harness = newHarness(t, func(o *Options) {
		o.Board = bridge
		for _, m := range mutate {
			m(o)
		}
	})
	pub := bh.srv.Publish
	publish.Store(&pub)
	bh.c = bh.client()
	bh.pair(bh.c, "Simon")
	// the board's first row load
	deadline := time.Now().Add(10 * time.Second)
	for {
		var b webapi.Board
		if bh.call(http.MethodGet, "/api/board", nil, &b) == http.StatusOK && b.Rows != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the board never loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return bh
}

// call is a JSON request as the paired page makes it; it returns the
// status and decodes the body into out when given.
func (h *cardBoard) call(method, path string, body any, out any) int {
	h.t.Helper()
	return h.callAs(h.c, method, path, body, out)
}

func (h *cardBoard) callAs(c *http.Client, method, path string, body any, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", h.http.URL)
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decoding %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode
}

// card reads a card, failing the test on anything but 200.
func (h *cardBoard) card(id string) webapi.Card {
	h.t.Helper()
	var c webapi.Card
	if st := h.call(http.MethodGet, "/api/cards/"+id, nil, &c); st != http.StatusOK {
		h.t.Fatalf("GET card %s: %d", id, st)
	}
	return c
}

// waitCard polls a card until ok accepts it.
func (h *cardBoard) waitCard(id, what string, ok func(webapi.Card) bool) webapi.Card {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var c webapi.Card
		st := h.call(http.MethodGet, "/api/cards/"+id, nil, &c)
		if st == http.StatusOK && ok(c) {
			return c
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s never happened to %s: %+v", what, id, c)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// create mints a card through POST /api/cards.
func (h *cardBoard) create(req webapi.CreateCardRequest) webapi.Card {
	h.t.Helper()
	var c webapi.Card
	var e webapi.Error
	var raw json.RawMessage
	st := h.call(http.MethodPost, "/api/cards", req, &raw)
	if st != http.StatusCreated {
		_ = json.Unmarshal(raw, &e)
		h.t.Fatalf("create %+v: %d %+v", req, st, e)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// action runs a menu entry, failing on anything but 200.
func (h *cardBoard) action(id, action string, req webapi.ActionRequest) webapi.Card {
	h.t.Helper()
	if req.Against == "" {
		// as the page does: against the decision it shows, if any
		if d := h.card(id).Decision; d != nil {
			req.Against = d.Against.Token
		}
	}
	var raw json.RawMessage
	st := h.call(http.MethodPost, "/api/cards/"+id+"/actions/"+action, req, &raw)
	if st != http.StatusOK {
		h.t.Fatalf("action %s on %s: %d %s", action, id, st, raw)
	}
	var c webapi.Card
	_ = json.Unmarshal(raw, &c)
	return c
}

// feature reads a card's record straight from the store.
func (h *cardBoard) feature(id string) domain.Feature {
	h.t.Helper()
	f, err := h.store.GetFeature(context.Background(), domain.FeatureID(id))
	if err != nil {
		h.t.Fatal(err)
	}
	return f
}

// draft writes something into every section the card's current stage
// owes, standing in for the stage agent's own output.
func (h *cardBoard) draft(id string, sections ...string) {
	h.t.Helper()
	f := h.feature(id)
	path := spec.LocateArtifact(
		filepath.Join(h.root, f.ArtifactPath()),
		filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f)),
		filepath.Join(h.root, f.WorktreePath(), f.ArtifactPath()),
	)
	if path == "" {
		path = filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f))
		if err := spec.EnsureDraft(path, &f); err != nil {
			h.t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	content := string(raw)
	for _, name := range spec.UndraftedSections(content, sections) {
		body, _ := spec.ViewSection(content, name)
		next, _, err := spec.ReplaceSection(content, name, body+"drafted by the fixture.\n\n")
		if err != nil {
			h.t.Fatal(err)
		}
		content = next
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// option finds a decision's option by id.
func option(t *testing.T, d *webapi.Decision, id string) webapi.Option {
	t.Helper()
	if d == nil {
		t.Fatal("no decision open")
	}
	for _, o := range d.Options {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("decision %s has no option %q: %+v", d.Ref, id, d.Options)
	return webapi.Option{}
}

func errorOf(t *testing.T, raw json.RawMessage) webapi.Error {
	t.Helper()
	var e webapi.Error
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("not an error body: %s", raw)
	}
	return e
}

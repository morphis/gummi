package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentplugins"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// boardHarness is a server over a whole board: a git repository, a
// workspace, a store, a worktree pool and an engine whose agent is the
// in-process fake — everything `gummi web` builds, with no network and no
// real agent anywhere.
type boardHarness struct {
	*harness
	root    string
	store   *state.Store
	eng     *engine.Engine
	ag      *agent.Fake
	plugins *agentplugins.Store
	c       *http.Client
}

// proposalJSON is the decomposition the fake architect hands back to an
// ingest pass.
const proposalJSON = `{"features":[` +
	`{"title":"Row loader","one_liner":"load the rows","source_refs":["§1"],"problem":"rows load slowly","acceptance":"fast","open_questions":["cache?"]},` +
	`{"title":"Row cache","one_liner":"cache the rows","source_refs":["§2"],"depends_on":["Row loader"],"problem":"repeat loads"}],` +
	`"coverage":[{"requirement":"load","feature":"Row loader","status":"mapped"},{"requirement":"offline","status":"unmapped","note":"not covered"}]}`

func newBoardHarness(t *testing.T) *boardHarness {
	t.Helper()
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
	pool, err := worktree.NewPool(context.Background(), root, root, nil, store, false)
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Fake{
		Caps: agent.Capabilities{Interrupt: true, UsageEvents: true},
		Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
			if strings.Contains(msg, "decompose it into a set") {
				return []agent.Event{
					{Kind: agent.EventMessage, Text: "Here it is:\n```gummi-propose\n" + proposalJSON + "\n```\n"},
					{Kind: agent.EventIdle},
				}
			}
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "ack: " + strings.TrimSpace(msg)},
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, OutputTokens: 4}},
				{Kind: agent.EventIdle},
			}
		},
	}
	roles := config.Profile{}
	for _, role := range []string{"architect", "implementer", "reviewer", "scribe"} {
		roles[role] = config.RoleConfig{Backend: "fake", Model: "fake-model"}
	}
	eng := engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": ag, "fake": ag}, Store: store, Pool: pool, Workspace: ws, Model: "fake-model",
		Profiles: config.Profiles{Default: "balanced", Profiles: map[string]config.Profile{"balanced": roles, "thrifty": roles}},
	})
	pool.SetBaseLookup(eng.StackBaseFor)
	t.Cleanup(func() { eng.Close() })

	shell := ui.NewShell(theme.GummiDark(), "v0-test")
	shell.Attach(store, pool, ws)
	shell.AttachCardLocks(state.NewCardLocks(ws))
	shell.AttachEngine(eng)
	shell.SetProfileNames([]string{"balanced", "thrifty"})
	shell.SetEnvelope(500)
	shell.SetCopilotHint(false)
	shell.SetMotion(false)
	bridge := ui.NewHeadless(shell)
	go func() { _ = bridge.Run() }()
	t.Cleanup(bridge.Stop)

	devices, err := OpenDevices(filepath.Join(t.TempDir(), "web", "devices.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := agentplugins.New(root, []agentplugins.Repo{{Name: "default", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, bridge: bridge, pairing: NewPairing(nil), devices: devices}
	srv, err := New(Options{
		Board: bridge, Devices: devices, Pairing: h.pairing, Repo: "demo", Host: "box", Version: "v0-test",
		Plugins:  plugins,
		Coalesce: 5 * time.Millisecond, Log: func(string, ...any) {},
		Doctor: func(r *http.Request) webapi.Doctor {
			return webapi.Doctor{Ready: r.URL.Query().Get("deep") != "1", Checks: []webapi.DoctorCheck{{Name: "repo", Status: "ok", Detail: "git repository"}}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shell.SetChangeHook(srv.Publish)
	h.srv = srv
	h.http = httptestServer(t, srv)
	b := &boardHarness{harness: h, root: root, store: store, eng: eng, ag: ag, plugins: plugins}
	b.c = h.client()
	h.pair(b.c, "Simon")
	return b
}

// call is h.do with a JSON body built from v, decoding the answer into out.
func (b *boardHarness) call(method, path string, v, out any) int {
	b.t.Helper()
	body := ""
	if v != nil {
		raw, err := json.Marshal(v)
		if err != nil {
			b.t.Fatal(err)
		}
		body = string(raw)
	}
	req, err := http.NewRequest(method, b.http.URL+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", b.http.URL)
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			b.t.Fatalf("%s %s: decoding %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode
}

// must is call, failing the test on any status but want.
func (b *boardHarness) must(want int, method, path string, v, out any) {
	b.t.Helper()
	var raw json.RawMessage
	got := b.call(method, path, v, &raw)
	if got != want {
		b.t.Fatalf("%s %s = %d, want %d: %s", method, path, got, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			b.t.Fatal(err)
		}
	}
}

// eventually polls cond until it holds.
func (b *boardHarness) eventually(what string, cond func() bool) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			b.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// card mints a card straight into the store and has the board read it.
func (b *boardHarness) card(num int, title string, stage domain.Stage) domain.Feature {
	b.t.Helper()
	slug, err := domain.Slugify(title)
	if err != nil {
		b.t.Fatal(err)
	}
	f := domain.Feature{ID: domain.FeatureID(fmt.Sprintf("FD-%03d", num)), Num: num, Title: title, Slug: slug,
		Kind: domain.KindFeature, Stage: stage, BranchScheme: domain.BranchSchemeKind}
	if err := b.store.CreateFeature(context.Background(), &f); err != nil {
		b.t.Fatal(err)
	}
	b.reload()
	return f
}

func (b *boardHarness) reload() {
	b.t.Helper()
	if err := b.bridge.Reload(context.Background()); err != nil {
		b.t.Fatal(err)
	}
}

func (b *boardHarness) onBoard(id string) bool {
	var board webapi.Board
	b.must(http.StatusOK, http.MethodGet, "/api/board", nil, &board)
	for _, r := range board.Rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

func TestGoalsThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	ref := filepath.Join(b.root, "prd.md")
	if err := os.WriteFile(ref, []byte("# What done means\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var made webapi.Outcome
	env := 800
	b.must(http.StatusOK, http.MethodPost, "/api/goals", webapi.GoalCreateRequest{
		Description: "Make the board stable", Envelope: &env, References: []string{"prd.md"},
	}, &made)
	if made.ID != "GL-001" || !made.OK {
		t.Fatalf("create = %+v", made)
	}
	// a reference outside the workspace is refused before anything is made
	outside := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := b.call(http.MethodPost, "/api/goals", webapi.GoalCreateRequest{Description: "x", References: []string{outside}}, nil); got != http.StatusBadRequest {
		t.Fatalf("an outside reference = %d, want 400", got)
	}
	if got := b.call(http.MethodPost, "/api/goals", webapi.GoalCreateRequest{}, nil); got != http.StatusBadRequest {
		t.Fatalf("a goal with no objective = %d, want 400", got)
	}

	var goals webapi.Goals
	b.eventually("the goal on the rail", func() bool {
		b.must(http.StatusOK, http.MethodGet, "/api/goals", nil, &goals)
		return len(goals.Goals) == 1
	})
	if g := goals.Goals[0]; g.ID != "GL-001" || g.State != "todo" || g.Envelope != 800 {
		t.Fatalf("goal row = %+v", g)
	}

	var page webapi.Goal
	b.must(http.StatusOK, http.MethodGet, "/api/goals/gl-001", nil, &page)
	if page.Report.ID != "GL-001" || page.State != "todo" {
		t.Fatalf("goal page = %+v", page)
	}
	if len(page.Notebook.References) != 1 || page.Notebook.References[0].Name != "prd.md" {
		t.Fatalf("notebook = %+v, want the reference seeded", page.Notebook)
	}
	if !hasAction(page.Actions, webapi.GoalActionBudget) || hasAction(page.Actions, webapi.GoalActionStop) {
		t.Fatalf("todo goal actions = %+v", page.Actions)
	}
	if got := b.call(http.MethodGet, "/api/goals/FD-404", nil, nil); got != http.StatusNotFound {
		t.Fatalf("a missing goal = %d", got)
	}

	var out webapi.Outcome
	to := 1200
	b.must(http.StatusOK, http.MethodPost, "/api/goals/GL-001/actions/budget", webapi.GoalActionRequest{Envelope: &to}, &out)
	f, err := b.store.GetFeature(context.Background(), "GL-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 1200 {
		t.Fatalf("envelope = %d after raising it to 1200 (%+v)", f.Budget.Envelope, out)
	}
	lower := 100
	if got := b.call(http.MethodPost, "/api/goals/GL-001/actions/budget", webapi.GoalActionRequest{Envelope: &lower}, nil); got != http.StatusConflict {
		t.Fatalf("lowering a goal's budget = %d, want the engine's refusal as 409", got)
	}
	// stop is for a running goal
	if got := b.call(http.MethodPost, "/api/goals/GL-001/actions/stop", nil, nil); got != http.StatusConflict {
		t.Fatalf("stopping a todo goal = %d, want 409", got)
	}

	// running: a note reaches its lead's log and stop wraps it up
	ctx := context.Background()
	for _, st := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
		if _, err := b.store.Transition(ctx, "GL-001", st, "user"); err != nil {
			t.Fatal(err)
		}
	}
	b.reload()
	b.eventually("the goal at implement", func() bool {
		b.must(http.StatusOK, http.MethodGet, "/api/goals/GL-001", nil, &page)
		return page.Report.Stage == domain.StageImplement && hasAction(page.Actions, webapi.GoalActionNote)
	})
	b.must(http.StatusOK, http.MethodPost, "/api/goals/GL-001/actions/note", webapi.GoalActionRequest{Text: "the loader matters most"}, &out)
	b.must(http.StatusOK, http.MethodPost, "/api/goals/GL-001/actions/stop", nil, &out)
	b.must(http.StatusOK, http.MethodGet, "/api/goals/GL-001", nil, &page)
	var noted, wrapped bool
	for _, en := range page.Log {
		noted = noted || strings.Contains(en.Detail, "the loader matters most")
	}
	wrapped = page.Report.WrappingUp || page.State == "wrapping up"
	if !noted || !wrapped {
		t.Fatalf("after a note and a stop: noted=%v wrapping up=%v log=%+v", noted, wrapped, page.Log)
	}
	if got := b.call(http.MethodPost, "/api/goals/GL-001/actions/fly", nil, nil); got != http.StatusNotFound {
		t.Fatalf("an unknown goal action = %d", got)
	}
}

func hasAction(acts []webapi.Action, id string) bool {
	for _, a := range acts {
		if a.ID == id {
			return true
		}
	}
	return false
}

func TestStacksThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	b.card(1, "Tokens", domain.StageTodo)
	b.card(2, "Dark mode", domain.StageTodo)
	b.card(3, "Theme picker", domain.StageTodo)

	var made webapi.Outcome
	b.must(http.StatusOK, http.MethodPost, "/api/stacks", webapi.StackRequest{Card: "FD-001", Name: "theme"}, &made)
	if made.ID != "theme" {
		t.Fatalf("created = %+v", made)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/stacks/theme/cards", webapi.StackRequest{Card: "FD-002"}, nil)
	zero := 0
	b.must(http.StatusOK, http.MethodPost, "/api/stacks/theme/cards", webapi.StackRequest{Card: "fd-003", Pos: &zero}, nil)

	order := func() []string {
		var st webapi.Stack
		b.must(http.StatusOK, http.MethodGet, "/api/stacks/theme", nil, &st)
		ids := []string{}
		for _, m := range st.Members {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if got := strings.Join(order(), ","); got != "FD-003,FD-001,FD-002" {
		t.Fatalf("order = %s", got)
	}
	two := 2
	b.must(http.StatusOK, http.MethodPost, "/api/stacks/theme/move", webapi.StackRequest{Card: "FD-003", Pos: &two}, nil)
	var st webapi.Stack
	b.must(http.StatusOK, http.MethodGet, "/api/stacks/theme", nil, &st)
	if got := strings.Join(order(), ","); got != "FD-001,FD-002,FD-003" {
		t.Fatalf("order after the move = %s", got)
	}
	top := st.Members[2]
	if top.Below != "FD-002" || top.Blocker != "FD-001" || top.Title != "Theme picker" || top.Branch == "" {
		t.Fatalf("top member = %+v", top)
	}

	b.must(http.StatusOK, http.MethodDelete, "/api/stacks/theme/cards/FD-002", nil, nil)
	if got := strings.Join(order(), ","); got != "FD-001,FD-003" {
		t.Fatalf("order after the removal = %s", got)
	}
	if got := b.call(http.MethodDelete, "/api/stacks/theme/cards/FD-002", nil, nil); got != http.StatusNotFound {
		t.Fatalf("removing a card that left = %d, want 404", got)
	}
	if got := b.call(http.MethodPost, "/api/stacks", webapi.StackRequest{Card: "FD-001"}, nil); got != http.StatusConflict {
		t.Fatalf("a second stack on a stacked card = %d, want 409", got)
	}

	var res webapi.Restack
	b.must(http.StatusOK, http.MethodPost, "/api/stacks/theme/restack", nil, &res)
	if len(res.Replayed) != 0 || res.Stack.ID != "theme" || len(res.Stack.Members) != 2 {
		t.Fatalf("restack of a stack with no branches cut = %+v", res)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/stacks/theme/rename", webapi.StackRequest{Name: "theming"}, nil)
	var all webapi.Stacks
	b.must(http.StatusOK, http.MethodGet, "/api/stacks", nil, &all)
	if len(all.Stacks) != 1 || all.Stacks[0].Name != "theming" {
		t.Fatalf("stacks = %+v", all)
	}
	if got := b.call(http.MethodDelete, "/api/stacks/theme", nil, nil); got != http.StatusConflict {
		t.Fatalf("deleting a stack with cards = %d, want 409", got)
	}
	b.eventually("the rail to know the stack", func() bool {
		f, err := b.store.GetFeature(context.Background(), "FD-003")
		return err == nil && f.StackID == "theme" && f.StackPos == 1
	})
}

func TestIngestThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	events := b.events(b.c, "")
	var run webapi.IngestRun
	b.must(http.StatusAccepted, http.MethodPost, "/api/ingest", webapi.IngestRequest{
		Markdown: "# Rows\n\n## §1 load\n\n## §2 cache\n", Name: "Rows PRD",
	}, &run)
	if run.ID != "1" || run.State != webapi.IngestRunning {
		t.Fatalf("started = %+v", run)
	}
	if ev := events.until("ingest"); !strings.Contains(ev.data, `"id":"1"`) {
		t.Fatalf("ingest event = %+v, want it to name the run", ev)
	}
	if got := b.call(http.MethodPost, "/api/ingest", webapi.IngestRequest{Markdown: "# again\n"}, nil); got != http.StatusConflict {
		t.Fatalf("a second pass while one runs = %d, want 409", got)
	}
	b.eventually("the proposals", func() bool {
		b.must(http.StatusOK, http.MethodGet, "/api/ingest/1", nil, &run)
		return run.State == webapi.IngestReview
	})
	if len(run.Proposals) != 2 || run.Proposals[0].Title != "Row loader" || run.Coverage == nil || run.Coverage.Unmapped != 1 {
		t.Fatalf("review = %+v", run)
	}
	if len(run.Unmapped) != 1 || !strings.Contains(run.Unmapped[0], "offline") {
		t.Fatalf("unmapped = %v", run.Unmapped)
	}
	if !strings.HasSuffix(run.Source, "rows-prd.md") {
		t.Fatalf("source = %q, want the pasted document saved under .gummi/ingest", run.Source)
	}
	if run.Profile != "balanced" || run.Envelope != 500 {
		t.Fatalf("options = %s/%d, want the board's defaults", run.Profile, run.Envelope)
	}

	edit := func(req webapi.IngestEditRequest) webapi.IngestRun {
		t.Helper()
		var out webapi.IngestRun
		b.must(http.StatusOK, http.MethodPost, "/api/ingest/1/edit", req, &out)
		return out
	}
	run = edit(webapi.IngestEditRequest{Index: 0, Op: webapi.IngestEditRename, Title: "Fast row loader"})
	run = edit(webapi.IngestEditRequest{Index: 0, Op: webapi.IngestEditOneLiner, OneLiner: "rows in 40ms"})
	run = edit(webapi.IngestEditRequest{Index: 1, Op: webapi.IngestEditDrop})
	if !run.Proposals[1].Dropped {
		t.Fatal("drop did not stick")
	}
	run = edit(webapi.IngestEditRequest{Index: 1, Op: webapi.IngestEditUndrop})
	run = edit(webapi.IngestEditRequest{Index: 1, Op: webapi.IngestEditMerge})
	if len(run.Proposals) != 1 || run.Proposals[0].Title != "Fast row loader" || run.Proposals[0].OneLiner != "rows in 40ms" {
		t.Fatalf("after the edits = %+v", run.Proposals)
	}
	if got := strings.Join(run.Proposals[0].SourceRefs, ","); got != "§1,§2" {
		t.Fatalf("merged refs = %s", got)
	}
	if got := b.call(http.MethodPost, "/api/ingest/1/edit", webapi.IngestEditRequest{Index: 0, Op: webapi.IngestEditRename, Title: "!!!"}, nil); got != http.StatusBadRequest {
		t.Fatalf("a title that cannot be a slug = %d, want 400", got)
	}
	if got := b.call(http.MethodPost, "/api/ingest/2/approve", nil, nil); got != http.StatusNotFound {
		t.Fatalf("approving a pass that is not this one = %d, want 404", got)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/ingest/1/approve", nil, &run)
	if run.State != webapi.IngestMaterialized || len(run.Created) != 1 || run.Created[0].Title != "Fast row loader" {
		t.Fatalf("approved = %+v", run)
	}
	b.eventually("the minted card on the rail", func() bool { return b.onBoard(run.Created[0].ID) })
	if got := b.call(http.MethodPost, "/api/ingest/1/approve", nil, nil); got != http.StatusConflict {
		t.Fatalf("approving twice = %d, want 409", got)
	}

	// a path names a file in the workspace, and nothing outside it
	if err := os.WriteFile(filepath.Join(b.root, "spec.md"), []byte("# Spec\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := b.call(http.MethodPost, "/api/ingest", webapi.IngestRequest{Path: "../../etc/passwd"}, nil); got != http.StatusBadRequest && got != http.StatusNotFound {
		t.Fatalf("a path outside the workspace = %d", got)
	}
	// gummi's own state is not a document, and a refusal names no path the
	// page did not send nor says whether anything is there
	secret := filepath.Join(b.root, ".gummi", "state", "web", "server.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var refusals []string
	for _, p := range []string{".gummi/state/web/server.json", secret, "missing.md", "../../etc/passwd", filepath.Join(t.TempDir(), "x.md")} {
		var e webapi.Error
		if got := b.call(http.MethodPost, "/api/ingest", webapi.IngestRequest{Path: p}, &e); got != http.StatusBadRequest {
			t.Errorf("ingest %s = %d, want 400", p, got)
		}
		refusals = append(refusals, strings.Replace(e.Error, fmt.Sprintf("%q", p), "<path>", 1))
		if strings.Contains(e.Error, b.root) && !strings.Contains(p, b.root) {
			t.Errorf("the refusal of %s names the workspace: %q", p, e.Error)
		}
	}
	for _, r := range refusals[1:] {
		if r != refusals[0] {
			t.Errorf("refusals differ: %q vs %q", r, refusals[0])
		}
	}
	b.must(http.StatusAccepted, http.MethodPost, "/api/ingest", webapi.IngestRequest{Path: "spec.md"}, &run)
	if run.ID != "2" {
		t.Fatalf("second pass = %+v", run)
	}
	b.eventually("the second review", func() bool {
		b.must(http.StatusOK, http.MethodGet, "/api/ingest", nil, &run)
		return run.State == webapi.IngestReview
	})
	b.must(http.StatusOK, http.MethodPost, "/api/ingest/2/discard", nil, &run)
	if run.State != webapi.IngestDiscarded {
		t.Fatalf("discarded = %+v", run)
	}
}

func TestBugImportThroughTheBoard(t *testing.T) {
	gh, err := filepath.Abs(filepath.Join("testdata", "fake-gh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_GH_CMD", gh)
	b := newBoardHarness(t)

	var bugs webapi.Bugs
	b.must(http.StatusOK, http.MethodGet, "/api/bugs?label=bug", nil, &bugs)
	if bugs.Error != "" || len(bugs.Proposals) != 2 {
		t.Fatalf("bugs = %+v", bugs)
	}
	first := bugs.Proposals[0]
	if first.Number != 7 || first.Severity == "" || first.State != "open" {
		t.Fatalf("first proposal = %+v", first)
	}

	// the import fetches again with the list's own filters, its limit
	// included: an issue picked from a longer list than gh's default is
	// still on offer when the import looks for it
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("FAKE_GH_LOG", ghLog)
	var made webapi.BugsCreated
	b.must(http.StatusOK, http.MethodPost, "/api/bugs", webapi.BugsRequest{
		Label: "bug", Limit: 80, Refs: []string{first.Ref, "https://github.com/o/r/issues/99"},
	}, &made)
	if logged, _ := os.ReadFile(ghLog); !strings.Contains(string(logged), "--limit 80") {
		t.Fatalf("the import's fetch ran gh as %q, want the list's --limit 80", logged)
	}
	t.Setenv("FAKE_GH_LOG", "")
	if len(made.Created) != 1 || made.Created[0].Title != "Crash on empty board" || len(made.Missing) != 1 {
		t.Fatalf("created = %+v", made)
	}
	f, err := b.store.GetFeature(context.Background(), domain.FeatureID(made.Created[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != domain.KindBug || f.ExternalRef != first.Ref || f.Stage != domain.StageTodo || f.Budget.Envelope != 500 {
		t.Fatalf("minted bug = %+v", f)
	}

	b.must(http.StatusOK, http.MethodGet, "/api/bugs", nil, &bugs)
	if len(bugs.Proposals) != 1 || len(bugs.Skipped) != 1 || bugs.Skipped[0].Card != made.Created[0].ID {
		t.Fatalf("after the import = %+v", bugs)
	}
	if got := b.call(http.MethodPost, "/api/bugs", webapi.BugsRequest{Refs: []string{first.Ref}}, nil); got != http.StatusConflict {
		t.Fatalf("importing an issue already on the board = %d, want 409", got)
	}

	t.Setenv("FAKE_GH_FAIL", "1")
	b.must(http.StatusOK, http.MethodGet, "/api/bugs", nil, &bugs)
	if !strings.Contains(bugs.Error, "authentication required") || len(bugs.Proposals) != 0 {
		t.Fatalf("a failing gh = %+v, want its words as the answer's error", bugs)
	}
}

func TestBoardAgentThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	var a webapi.Agent
	b.must(http.StatusOK, http.MethodGet, "/api/agent", nil, &a)
	if a.Open || len(a.Profiles) != 2 || len(a.Models) != 1 {
		t.Fatalf("before opening = %+v", a)
	}
	if got := b.call(http.MethodPost, "/api/agent/send", webapi.AgentSendRequest{Text: "hi"}, nil); got != http.StatusConflict {
		t.Fatalf("sending before opening = %d, want 409", got)
	}
	events := b.events(b.c, "")
	b.must(http.StatusOK, http.MethodPost, "/api/agent/open", nil, nil)
	events.until("agent")
	b.must(http.StatusOK, http.MethodGet, "/api/agent", nil, &a)
	if !a.Open || a.Model != "fake-model" {
		t.Fatalf("after opening = %+v", a)
	}

	b.must(http.StatusOK, http.MethodPost, "/api/agent/send", webapi.AgentSendRequest{Text: "what is stuck?"}, nil)
	b.eventually("the agent's answer", func() bool {
		b.must(http.StatusOK, http.MethodGet, "/api/agent", nil, &a)
		for _, it := range a.Items {
			if it.T == webapi.ItemMessage && it.Text == "ack: what is stuck?" {
				return true
			}
		}
		return false
	})
	if a.Items[0].T != webapi.ItemYou || a.Items[0].Text != "what is stuck?" {
		t.Fatalf("items = %+v", a.Items)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/agent/interrupt", nil, nil)

	// a busy backend hands the line back
	b.ag.SendErr = agent.ErrBusy
	var refused webapi.Error
	if got := b.call(http.MethodPost, "/api/agent/send", webapi.AgentSendRequest{Text: "again"}, &refused); got != http.StatusConflict ||
		refused.Error != webapi.ConflictBusy || refused.Text != "again" {
		t.Fatalf("a busy send = %d %+v", got, refused)
	}
	b.ag.SendErr = nil

	// a switch with a conversation to lose asks first
	if got := b.call(http.MethodPost, "/api/agent/profile", webapi.AgentProfileRequest{Profile: "THRIFTY"}, &refused); got != webapi.StatusQuestion || refused.Error != "confirm" || refused.Confirm == "" {
		t.Fatalf("an unconfirmed switch = %d %+v", got, refused)
	}
	if got := b.call(http.MethodPost, "/api/agent/profile", webapi.AgentProfileRequest{Profile: "nope", Confirm: refused.Confirm}, nil); got != http.StatusBadRequest {
		t.Fatalf("an unknown profile = %d, want 400", got)
	}
	// the yes to the switch to thrifty is not a yes to another switch
	var other webapi.Error
	if got := b.call(http.MethodPost, "/api/agent/profile", webapi.AgentProfileRequest{Profile: "THRIFTY", Model: "m2", Confirm: refused.Confirm}, &other); got != webapi.StatusQuestion || other.Error != "confirm" {
		t.Fatalf("a switch confirmed for another question = %d %+v, want 202 confirm", got, other)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/agent/profile", webapi.AgentProfileRequest{Profile: "THRIFTY", Confirm: refused.Confirm}, nil)
	b.must(http.StatusOK, http.MethodGet, "/api/agent", nil, &a)
	if !a.Open || a.Profile != "thrifty" || len(a.Items) != 0 {
		t.Fatalf("after the switch = %+v", a)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/agent/send", webapi.AgentSendRequest{Text: "/clear"}, nil)
}

func TestDoctorThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	var d webapi.Doctor
	b.must(http.StatusOK, http.MethodGet, "/api/doctor", nil, &d)
	if !d.Ready || len(d.Checks) != 1 || d.Checks[0].Name != "repo" || d.Checks[0].Status != "ok" {
		t.Fatalf("doctor = %+v", d)
	}
	// the deep run spends model turns: never on a GET, which passes no
	// same-origin check, and never on a write from another origin
	if got := b.call(http.MethodGet, "/api/doctor?deep=1", nil, nil); got != http.StatusMethodNotAllowed {
		t.Fatalf("GET ?deep=1 = %d, want 405", got)
	}
	if res, _ := b.do(b.c, http.MethodPost, "/api/doctor?deep=1", "{}", "Origin", "http://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin deep run = %d, want 403", res.StatusCode)
	}
	b.must(http.StatusOK, http.MethodPost, "/api/doctor?deep=1", nil, &d)
	if d.Ready {
		t.Fatal("?deep=1 did not reach the checklist's builder")
	}
}

func httptestServer(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		s.Close()
	})
	return s
}

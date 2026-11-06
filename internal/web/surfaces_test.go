package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/credentials"
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
// restarts counts the restarts the harness's server was asked for.
var restarts atomic.Int32

type boardHarness struct {
	*harness
	root  string
	store *state.Store
	eng   *engine.Engine
	ag    *agent.Fake
	c     *http.Client
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
		Agents: map[string]agent.Agent{"": ag, "fake": ag, "claude": ag}, Store: store, Pool: pool, Workspace: ws, Model: "fake-model",
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
	var publish atomic.Pointer[func(webapi.Change)]
	shell.SetChangeHook(func(c webapi.Change) {
		if p := publish.Load(); p != nil {
			(*p)(c)
		}
	})
	bridge := ui.NewHeadless(shell)
	go func() { _ = bridge.Run() }()
	t.Cleanup(bridge.Stop)

	devices, err := OpenDevices(filepath.Join(t.TempDir(), "web", "devices.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, bridge: bridge, pairing: NewPairing(nil), devices: devices}
	srv, err := New(Options{
		Board: bridge, Devices: devices, Pairing: h.pairing, Repo: "demo", Host: "box", Version: "v0-test",
		Coalesce: 5 * time.Millisecond, Log: func(string, ...any) {},
		Restart: func() { restarts.Add(1) },
		Doctor: func(r *http.Request) webapi.Doctor {
			return webapi.Doctor{Ready: r.URL.Query().Get("deep") != "1", Checks: []webapi.DoctorCheck{{Name: "repo", Status: "ok", Detail: "git repository"}}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pub := srv.Publish
	publish.Store(&pub)
	h.srv = srv
	h.http = httptestServer(t, srv)
	b := &boardHarness{harness: h, root: root, store: store, eng: eng, ag: ag}
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
	f := domain.Feature{
		ID: domain.FeatureID(fmt.Sprintf("FD-%03d", num)), Num: num, Title: title, Slug: slug,
		Kind: domain.KindFeature, Stage: stage, BranchScheme: domain.BranchSchemeKind,
	}
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

func TestSettingsNameThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	var got webapi.Settings
	b.must(http.StatusOK, http.MethodPut, "/api/settings", webapi.SettingsRequest{Name: " east "}, &got)
	if got.Name != "east" || got.MaxName == 0 {
		t.Fatalf("settings = %+v", got)
	}
	var board webapi.Board
	b.must(http.StatusOK, http.MethodGet, "/api/board", nil, &board)
	if board.Name != "east" {
		t.Fatalf("board name = %q", board.Name)
	}
	if code := b.call(http.MethodPut, "/api/settings", webapi.SettingsRequest{Name: strings.Repeat("x", 99)}, nil); code != http.StatusBadRequest {
		t.Fatalf("a too long name = %d, want 400", code)
	}
}

// A credential stored from the page is kept on the host and described back,
// never returned; a field left out is left alone, an empty one forgets.
func TestCredentialsThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	t.Cleanup(func() { credentials.Use(credentials.Store{}) })
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key, token := string(pem.EncodeToMemory(block)), "ghp_abcdefghijklmnop1234"

	var raw json.RawMessage
	b.must(http.StatusOK, http.MethodPut, "/api/settings/credentials", webapi.CredentialsRequest{GitHubToken: &token, SSHKey: &key}, &raw)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatalf("the answer carries a secret back: %s", raw)
	}
	var got webapi.Settings
	b.must(http.StatusOK, http.MethodGet, "/api/settings", nil, &raw)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatalf("the settings carry a secret back: %s", raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if c := got.Credentials; !c.TokenSet || c.TokenHint != "1234" || !c.KeySet || c.KeyType != "ssh-ed25519" || c.KeyFingerprint == "" {
		t.Fatalf("credentials = %+v", c)
	}
	if credentials.Current().Token() != token {
		t.Fatal("the board's own gh calls do not read the stored token")
	}

	empty := ""
	b.must(http.StatusOK, http.MethodPut, "/api/settings/credentials", webapi.CredentialsRequest{GitHubToken: &empty}, &got)
	if c := got.Credentials; c.TokenSet || !c.KeySet {
		t.Fatalf("after forgetting the token alone = %+v", c)
	}
	bad := "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n-----END OPENSSH PRIVATE KEY-----"
	if code := b.call(http.MethodPut, "/api/settings/credentials", webapi.CredentialsRequest{SSHKey: &bad}, nil); code != http.StatusBadRequest {
		t.Fatalf("a key that does not parse = %d, want 400", code)
	}
	b.must(http.StatusOK, http.MethodGet, "/api/settings", nil, &got)
	if !got.Credentials.KeySet {
		t.Fatal("a refused key replaced the stored one")
	}

	// a key made on the host replaces the one held, and only its public
	// half is ever said
	held := got.Credentials.KeyFingerprint
	b.must(http.StatusOK, http.MethodPut, "/api/settings/credentials", webapi.CredentialsRequest{GenerateSSHKey: true}, &raw)
	if strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatalf("generating carries the private key back: %s", raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if c := got.Credentials; !c.KeySet || c.KeyFingerprint == held || !strings.HasPrefix(c.KeyPublic, "ssh-ed25519 ") {
		t.Fatalf("after generating = %+v", c)
	}
	if code := b.call(http.MethodPut, "/api/settings/credentials", webapi.CredentialsRequest{GenerateSSHKey: true, SSHKey: &key}, nil); code != http.StatusBadRequest {
		t.Fatalf("generate and store together = %d, want 400", code)
	}
}

// The git identity set from the page is the repository's own: what git
// then writes a commit as there.
func TestGitIdentityThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	var got webapi.Settings
	b.must(http.StatusOK, http.MethodPut, "/api/settings/identity", webapi.IdentityRequest{Name: " Ada Lovelace ", Email: "ada@example.com"}, &got)
	if got.Identity != (webapi.Identity{Name: "Ada Lovelace", Email: "ada@example.com"}) {
		t.Fatalf("identity = %+v", got.Identity)
	}
	b.must(http.StatusOK, http.MethodGet, "/api/settings", nil, &got)
	if got.Identity.Name != "Ada Lovelace" {
		t.Fatalf("settings identity = %+v", got.Identity)
	}
	out, err := exec.CommandContext(context.Background(), "git", "-C", b.root, "config", "--local", "user.email").Output()
	if err != nil || strings.TrimSpace(string(out)) != "ada@example.com" {
		t.Fatalf("the repository's own user.email = %q, %v", out, err)
	}
	for _, bad := range []webapi.IdentityRequest{{Name: "Ada"}, {Name: "Ada", Email: "nope"}} {
		if code := b.call(http.MethodPut, "/api/settings/identity", bad, nil); code != http.StatusBadRequest {
			t.Fatalf("%+v = %d, want 400", bad, code)
		}
	}
}

// The co-authors set from the page are the workspace's config, and what
// this process's commits name from then on.
func TestCoAuthorsThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	t.Cleanup(func() { worktree.UseCoAuthors(nil) })
	var got webapi.Settings
	b.must(http.StatusOK, http.MethodGet, "/api/settings", nil, &got)
	if got.CoAuthors == nil || len(got.CoAuthors) != 0 || got.MaxCoAuthors == 0 {
		t.Fatalf("settings before = %+v", got)
	}
	want := []string{"Ada Lovelace <ada@example.com>", "Grace Hopper <grace@example.com>"}
	b.must(http.StatusOK, http.MethodPut, "/api/settings/coauthors",
		webapi.CoAuthorsRequest{CoAuthors: []string{" Ada Lovelace <ada@example.com> ", "Co-authored-by: Grace Hopper <grace@example.com>"}}, &got)
	if !slices.Equal(got.CoAuthors, want) || !slices.Equal(worktree.CoAuthors(), want) {
		t.Fatalf("co-authors = %q, in use %q", got.CoAuthors, worktree.CoAuthors())
	}
	raw, err := os.ReadFile(filepath.Join(b.root, ".gummi", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "- Grace Hopper <grace@example.com>") {
		t.Fatalf("the workspace config = %q, %v", raw, err)
	}
	if code := b.call(http.MethodPut, "/api/settings/coauthors", webapi.CoAuthorsRequest{CoAuthors: []string{"nobody"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a co-author with no email = %d, want 400", code)
	}
	b.must(http.StatusOK, http.MethodPut, "/api/settings/coauthors", webapi.CoAuthorsRequest{}, &got)
	if len(got.CoAuthors) != 0 || len(worktree.CoAuthors()) != 0 {
		t.Fatalf("after clearing = %q, in use %q", got.CoAuthors, worktree.CoAuthors())
	}
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

func TestRestartThroughTheBoard(t *testing.T) {
	b := newBoardHarness(t)
	before := restarts.Load()
	// a write from another origin must not restart the server
	if res, _ := b.do(b.c, http.MethodPost, "/api/restart", "{}", "Origin", "http://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin restart = %d, want 403", res.StatusCode)
	}
	if restarts.Load() != before {
		t.Fatal("a refused restart reached the host")
	}
	b.must(http.StatusAccepted, http.MethodPost, "/api/restart", nil, nil)
	if got := restarts.Load() - before; got != 1 {
		t.Fatalf("restarts asked of the host = %d, want 1", got)
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

// The workflows setting and a session's own switch go through the board:
// the workspace's lands in its config and the live engine, the session's
// wins over it and reads back on the card.
func TestWorkflowsSettingRoutes(t *testing.T) {
	b := newBoardHarness(t)
	var got webapi.Settings
	b.must(http.StatusOK, http.MethodGet, "/api/settings", nil, &got)
	if got.Workflows {
		t.Fatal("workflows are on in a fresh workspace")
	}
	b.must(http.StatusOK, http.MethodPut, "/api/settings/workflows", webapi.WorkflowsRequest{On: true}, &got)
	if !got.Workflows || !b.eng.WorkflowsDefault() {
		t.Fatalf("settings after on = %+v, engine %v", got, b.eng.WorkflowsDefault())
	}
	raw, err := os.ReadFile(filepath.Join(b.root, ".gummi", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "workflows: on") {
		t.Fatalf("config = %q, %v", raw, err)
	}

	f := domain.Feature{
		ID: "FF-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Kind: domain.KindFreeform,
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := b.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.OpenFreeform(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	b.eventually("the board to load the session", func() bool {
		return b.call(http.MethodGet, "/api/cards/FF-001", nil, nil) == http.StatusOK
	})
	off := false
	var res webapi.SessionWorkflowsResult
	b.must(http.StatusOK, http.MethodPut, "/api/cards/FF-001/workflows", webapi.SessionWorkflowsRequest{On: &off}, &res)
	if res.Error != "" || res.On || res.Override != "off" {
		t.Fatalf("result = %+v", res)
	}
	var card webapi.Card
	b.must(http.StatusOK, http.MethodGet, "/api/cards/FF-001", nil, &card)
	if card.Workflows == nil || card.Workflows.On || card.Workflows.Override != "off" {
		t.Fatalf("card workflows = %+v", card.Workflows)
	}
	res = webapi.SessionWorkflowsResult{}
	b.must(http.StatusOK, http.MethodPut, "/api/cards/FF-001/workflows", webapi.SessionWorkflowsRequest{}, &res)
	if res.Error != "" || !res.On || res.Override != "" {
		t.Fatalf("cleared result = %+v", res)
	}
}

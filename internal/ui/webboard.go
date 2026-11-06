package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/credentials"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// Projections for the web face (DESIGN §20.1): the board as the TUI holds
// it, as plain values. They read the model and never do IO — they run
// inside Update, through the Bridge — so every figure is one the TUI's own
// board already computed or looks up in memory.

// WebBoard is the rail: every card in the board's order, with the header's
// counts. Viewers is left for the server, which is the one that knows who
// is connected.
func (m *Shell) WebBoard() webapi.Board {
	b := webapi.Board{
		Repo:    filepath.Base(m.ws.Root),
		Name:    m.name,
		Viewers: []webapi.Viewer{},
		Rows:    make([]webapi.Row, 0, len(m.rows)),
	}
	if m.baseBranches != nil {
		b.Head = m.baseBranches[""]
	}
	if m.today.measured && m.today.day == m.now().Format(time.DateOnly) {
		b.Today.Spent = m.today.credits
	}
	for _, r := range m.rows {
		row := m.webRow(r)
		switch row.Status {
		case webapi.StatusNeeds:
			b.Counts.Needs++
		case webapi.StatusRunning:
			b.Counts.Running++
		}
		b.Rows = append(b.Rows, row)
	}
	return b
}

// webRow projects one board row.
func (m *Shell) webRow(r featureRow) webapi.Row {
	f := r.F
	row := webapi.Row{
		ID:        string(f.ID),
		Kind:      string(webKind(f)),
		Type:      f.TypeName(),
		Title:     f.Title,
		Stage:     string(f.Stage),
		Envelope:  f.Budget.Envelope,
		Profile:   f.Profile,
		Repo:      f.Repo,
		Home:      webHome(m.ws, f),
		Elsewhere: r.DrivenAbroad,
		Severity:  string(f.Severity),
		Landed:    r.Landed || f.LandedSHA != "",
		PR:        f.PullRequest.Badge(),
	}
	row.Dispatched = r.Dispatched
	if r.DispatchedBy != "" {
		row.DispatchedBy = string(r.DispatchedBy)
		row.DispatchedByTitle = m.cardName(r.DispatchedBy)
	}
	sp, live := m.envelopeSpend(f)
	row.Spend = sp.Credits
	if live > 0 {
		row.Spend = live
	}
	if c := m.liveCardContext(f.ID); c.Tokens > 0 {
		row.Context = &webapi.AgentContext{Tokens: c.Tokens, Limit: c.Limit}
	}
	if st, ok := m.stackRows[f.ID]; ok {
		// the board counts from 1 at the bottom ("2/4"); the contract
		// counts from 0, so the page can index with it.
		row.Stack = &webapi.RowStack{ID: string(st.ID), Name: st.Name, Pos: st.Pos - 1, Of: st.Of, Stale: st.Stale}
	}
	if o := m.freeformObjective(r); o != nil {
		row.Objective = string(o.State)
	}
	it, needs := m.inbox.get(f.ID)
	switch {
	case needs:
		// needs-you outranks busy, the TUI row's own precedence: a person
		// can act on a raised gate, not on a check still running under it.
		row.Status = webapi.StatusNeeds
		row.Needs = webNeeds(it)
	case r.StepAsks != "":
		// a step of its run waits on the person: the run is not working
		// for them, it is waiting on them
		row.Status = webapi.StatusNeeds
		row.Needs = &webapi.RowNeeds{Kind: webapi.NeedsQuestion, Color: "info", Question: "workflow step: " + r.StepAsks, Word: "step asks"}
	case m.cardBusy(r):
		row.Status = webapi.StatusRunning
		row.Running = &webapi.RowRunning{Verb: m.cardBusyWord(r)}
	case r.OpenRun != nil && r.OpenRun.State == domain.RunRunning && r.DecidingRun == nil:
		// its own turn is over, but its run's steps are working in its tree
		row.Status = webapi.StatusRunning
		row.Running = &webapi.RowRunning{Verb: fmt.Sprintf("run %d working", r.OpenRun.N)}
	case m.freeformWatching(r):
		row.Status = webapi.StatusWatching
	case r.DecidingRun != nil:
		// a run's decision is the session's, whether or not the inbox
		// heard of it (a restart re-reads the run, not the queue)
		row.Status = webapi.StatusNeeds
	default:
		switch f.Stage.SuperState() {
		case domain.SuperTodo:
			row.Status = webapi.StatusTodo
		case domain.SuperDone:
			row.Status = webapi.StatusDone
		default:
			row.Status = webapi.StatusIdle
		}
	}
	if row.Status == webapi.StatusIdle {
		row.Word = m.webIdleWord(r)
	}
	if d := r.DecidingRun; d != nil && row.Status == webapi.StatusNeeds {
		// worded as the run's: the inbox's generic "the agent has a
		// question" is about a live ask the run does not have
		row.Needs = &webapi.RowNeeds{Kind: webapi.NeedsQuestion, Color: "info", Question: "workflow: " + d.Pending.Question, Word: workflowDecisionWord(d.Pending)}
	}
	return row
}

// webKind is the card's kind with the empty default spelled out.
// webHome is an agent's home as the page names it: relative to the
// workspace root, "" for every card that is not an agent.
func webHome(ws state.Workspace, f domain.Feature) string {
	if !f.Home {
		return ""
	}
	rel, err := filepath.Rel(ws.Root, ws.HomeDir(f.ID))
	if err != nil {
		return ws.HomeDir(f.ID)
	}
	return filepath.ToSlash(rel)
}

func webKind(f domain.Feature) domain.Kind {
	if f.Kind == "" {
		return domain.KindFeature
	}
	return f.Kind
}

// webNeeds projects a needs-you item, with the word the rail heads it
// with.
func webNeeds(it attnItem) *webapi.RowNeeds {
	n := &webapi.RowNeeds{Question: it.Text}
	switch it.Kind {
	case attnFailure:
		n.Kind, n.Color, n.Word = webapi.NeedsFailure, "err", "failed"
	case attnQuestion:
		n.Kind, n.Color, n.Word = webapi.NeedsQuestion, "info", "question"
	case attnBudget:
		n.Kind, n.Color, n.Word = webapi.NeedsBudget, "warn", "budget"
	default:
		// a stop a retired workflow card left recorded
		n.Kind, n.Color, n.Word = webapi.NeedsGate, "ok", "needs you"
		if it.Escalated {
			n.Color = "warn"
		}
	}
	return n
}

// todaySpend is the board's spend since local midnight, with when it was
// measured and whether a measurement is out.
type todaySpend struct {
	credits  float64
	at       time.Time
	day      string
	measured bool
	running  bool
	// total is the board's all-time spend when it was measured: a row
	// load whose total moved has something new to count.
	total float64
	// due marks a measurement put off by the window, with a tick coming
	// to take it.
	due bool
}

// todayDueMsg is the tick that takes a measurement the window put off.
type todayDueMsg struct{}

// todaySpentMsg lands a measurement of today's spend.
type todaySpentMsg struct {
	credits float64
	at      time.Time
	day     string
	total   float64
	err     error
}

// todayEvery bounds how often a headless board re-measures today's spend:
// row loads and engine events come in bursts, and the figure is a header,
// not a meter. A row load whose spend moved measures at once; anything
// else inside the window is taken once the window ends.
const todayEvery = 3 * time.Second

// measureToday is the header's spend figure, read the way the stats tab
// reads a window (fleetrun's attribution: a pass is charged to the window
// it started in) over [local midnight, now]. Only a headless board asks:
// the TUI has no header that shows it.
func (m *Shell) measureToday() tea.Cmd {
	if !m.headless || m.store == nil || m.today.running {
		return nil
	}
	now := m.now()
	total := 0.0
	for _, r := range m.rows {
		total += r.F.Spend.Credits
	}
	for _, c := range m.children {
		total += c.Spend.Credits
	}
	if wait := todayEvery - now.Sub(m.today.at); m.today.measured && total == m.today.total && wait > 0 && m.today.day == now.Format(time.DateOnly) {
		if m.today.due {
			return nil
		}
		m.today.due = true
		// a subscription, so a web intent never waits on the header
		return subscription(tea.Tick(wait, func(time.Time) tea.Msg { return todayDueMsg{} }))
	}
	m.today.running = true
	store, rows, children := m.store, append([]featureRow(nil), m.rows...), m.children
	return func() tea.Msg {
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		rep, err := buildWsReport(context.Background(), store, rows, children, midnight, now)
		if err != nil {
			return todaySpentMsg{err: err, at: now}
		}
		return todaySpentMsg{credits: rep.Credits, at: now, day: now.Format(time.DateOnly), total: total}
	}
}

func (m *Shell) todaySpent(msg todaySpentMsg) {
	m.today.running = false
	if msg.err != nil {
		return
	}
	m.today = todaySpend{credits: msg.credits, at: msg.at, day: msg.day, measured: true, total: msg.total, due: m.today.due}
}

// WebSettings is GET /api/settings: what the settings dialog holds.
func (m *Shell) WebSettings() webapi.Settings {
	st := m.credentialStore().Status()
	return webapi.Settings{
		Name: m.name, Repo: filepath.Base(m.ws.Root), MaxName: config.MaxNameLen,
		CoAuthors: append([]string{}, worktree.CoAuthors()...), MaxCoAuthors: config.MaxCoAuthors,
		Workflows: m.workflowsDefault(),
		Credentials: webapi.Credentials{
			TokenSet: st.TokenSet, TokenHint: st.TokenHint,
			KeySet: st.KeySet, KeyType: st.KeyType, KeyFingerprint: st.KeyFingerprint, KeyPublic: st.KeyPublic, Signing: st.Signing,
		},
	}
}

// credentialStore is where this board's GitHub token and SSH key are kept
// (DESIGN §22.2); the zero store for a board with no workspace attached.
func (m *Shell) credentialStore() credentials.Store {
	if m.ws.Root == "" {
		return credentials.Store{}
	}
	return credentials.Store{Dir: m.ws.CredentialsDir()}
}

// identityRoots are the repositories a git identity is written to: the
// workspace's default and every named one.
func (m *Shell) identityRoots() []string {
	if m.wt == nil {
		return nil
	}
	var roots []string
	for _, name := range append([]string{""}, m.wt.Names()...) {
		if root, ok := m.wt.RootForName(name); ok && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots
}

// Settings is GET /api/settings. The git identity is read off the loop:
// it asks git.
func (b *Bridge) Settings(ctx context.Context) (webapi.Settings, error) {
	var out webapi.Settings
	var roots []string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { out, roots = m.WebSettings(), m.identityRoots(); return nil }); err != nil {
		return webapi.Settings{}, err
	}
	if len(roots) > 0 {
		out.Identity.Name, out.Identity.Email = worktree.Identity(ctx, roots[0])
	}
	return out, nil
}

// SetIdentity is PUT /api/settings/identity: the name and email written
// to the local git configuration of every repository this workspace
// manages, so a card's commits and a landing are written as that person.
func (b *Bridge) SetIdentity(ctx context.Context, req webapi.IdentityRequest) (webapi.Settings, error) {
	name, email := strings.TrimSpace(req.Name), strings.TrimSpace(req.Email)
	if err := worktree.CheckIdentity(name, email); err != nil {
		return webapi.Settings{}, webErr(WebBadRequest, "%s", err.Error())
	}
	var roots []string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { roots = m.identityRoots(); return nil }); err != nil {
		return webapi.Settings{}, err
	}
	if len(roots) == 0 {
		return webapi.Settings{}, webErr(WebBadRequest, "this workspace has no repository to set an identity in")
	}
	for _, root := range roots {
		if err := worktree.SetIdentity(ctx, root, name, email); err != nil {
			return webapi.Settings{}, err
		}
	}
	return b.Settings(ctx)
}

// SetCoAuthors is PUT /api/settings/coauthors: the people named in a
// Co-authored-by trailer on every commit made in this workspace from here
// on. A list the workspace refuses is a bad request.
func (b *Bridge) SetCoAuthors(ctx context.Context, req webapi.CoAuthorsRequest) (webapi.Settings, error) {
	var refused error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		if err := m.setCoAuthors(req.CoAuthors); err != nil {
			refused = webErr(WebBadRequest, "%s", err.Error())
		}
		return nil
	}); err != nil {
		return webapi.Settings{}, err
	}
	if refused != nil {
		return webapi.Settings{}, refused
	}
	return b.Settings(ctx)
}

// SetWorkflows is PUT /api/settings/workflows: the same write the settings
// dialog's toggle makes.
func (b *Bridge) SetWorkflows(ctx context.Context, req webapi.WorkflowsRequest) (webapi.Settings, error) {
	var refused error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		if err := m.setWorkflows(req.On); err != nil {
			refused = webErr(WebBadRequest, "%s", err.Error())
		}
		return nil
	}); err != nil {
		return webapi.Settings{}, err
	}
	if refused != nil {
		return webapi.Settings{}, refused
	}
	return b.Settings(ctx)
}

// SetSettings is PUT /api/settings: the same write the terminal's
// settings dialog makes. A name the workspace refuses is a bad request.
func (b *Bridge) SetSettings(ctx context.Context, req webapi.SettingsRequest) (webapi.Settings, error) {
	var refused error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		if err := m.setName(req.Name); err != nil {
			refused = webErr(WebBadRequest, "%s", err.Error())
		}
		return nil
	}); err != nil {
		return webapi.Settings{}, err
	}
	if refused != nil {
		return webapi.Settings{}, refused
	}
	return b.Settings(ctx)
}

// SetCredentials is PUT /api/settings/credentials: store, replace or
// forget the GitHub token and the SSH key, and switch signing commits with
// that key on or off. The store is the one this process's gh and git calls
// read from then on, and publishing is detected
// again, since a token is what makes gh signed in.
func (b *Bridge) SetCredentials(ctx context.Context, req webapi.CredentialsRequest) (webapi.Settings, error) {
	var refused error
	if req.GenerateSSHKey && req.SSHKey != nil {
		return webapi.Settings{}, webErr(WebBadRequest, "generate a key or store one, not both")
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		store := m.credentialStore()
		// a request refused part-way has still written what came before
		// the refusal, and this process must read the store as it now is
		defer credentials.Use(store)
		if req.GenerateSSHKey {
			if err := store.GenerateSSHKey(); err != nil {
				refused = webErr(WebBadRequest, "%s", err.Error())
				return nil
			}
		}
		if req.GitHubToken != nil {
			if err := store.SetToken(*req.GitHubToken); err != nil {
				refused = webErr(WebBadRequest, "%s", err.Error())
				return nil
			}
		}
		if req.SSHKey != nil {
			if err := store.SetSSHKey(*req.SSHKey); err != nil {
				refused = webErr(WebBadRequest, "%s", err.Error())
				return nil
			}
		}
		if req.SignCommits != nil {
			if err := store.SetSigning(*req.SignCommits); err != nil {
				refused = webErr(WebBadRequest, "%s", err.Error())
				return nil
			}
		}
		if m.publishEnabled {
			return m.detectPublish
		}
		return nil
	}); err != nil {
		return webapi.Settings{}, err
	}
	if refused != nil {
		return webapi.Settings{}, refused
	}
	return b.Settings(ctx)
}

// webIdleWord is what an idle open session holds, in a few words, for the
// rail to say in place of its type a second time: whether its work is
// ready to land, what holds it, or that it has none yet. Read off the row
// alone (no git on the render path): uncommitted work is not known here.
func (m *Shell) webIdleWord(r featureRow) string {
	f := r.F
	if !f.IsFreeform() || f.Stage != domain.StageOpen || f.Home || f.MainCheckout || !r.HasWorktree {
		return ""
	}
	switch {
	case r.OpenDiffComments > 0:
		return fmt.Sprintf("%d open comment%s", r.OpenDiffComments, plural(r.OpenDiffComments))
	case !r.Ahead:
		return "nothing committed yet"
	case !f.PullRequest.Empty():
		return "in a pull request"
	case m.stackLandBlocker(f.ID) != "":
		return "lands after " + m.cardName(m.stackLandBlocker(f.ID))
	}
	return "ready to land"
}

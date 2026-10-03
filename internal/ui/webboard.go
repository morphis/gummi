package ui

import (
	"context"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
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
		Viewers: []webapi.Viewer{},
		Rows:    make([]webapi.Row, 0, len(m.rows)),
	}
	if m.baseBranches != nil {
		b.Head = m.baseBranches[""]
	}
	if m.today.measured && m.today.day == m.now().Format(time.DateOnly) {
		b.Today.Spent = m.today.credits
	}
	b.Resume = m.webResumeOffer()
	titles := make(map[domain.FeatureID]string, len(m.rows))
	for _, r := range m.rows {
		titles[r.F.ID] = r.F.Title
	}
	for _, r := range m.rows {
		row := m.webRow(r, titles)
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

// webRow projects one board row. titles resolves a goal id to its title.
func (m *Shell) webRow(r featureRow, titles map[domain.FeatureID]string) webapi.Row {
	f := r.F
	row := webapi.Row{
		ID:        string(f.ID),
		Kind:      string(webKind(f)),
		Title:     f.Title,
		Stage:     string(f.Stage),
		Spend:     f.Spend.Credits,
		Envelope:  f.Budget.Envelope,
		Profile:   f.Profile,
		Repo:      f.Repo,
		Autopilot: f.GateApproval == domain.GateAutopilot,
		Elsewhere: r.DrivenAbroad,
		Severity:  string(f.Severity),
		Landed:    r.Landed || f.LandedSHA != "",
		PR:        f.PullRequest.Badge(),
	}
	if live := m.liveCardSpent(f.ID); live > 0 {
		row.Spend = live
	}
	if c := m.liveCardContext(f.ID); c.Tokens > 0 {
		row.Context = &webapi.AgentContext{Tokens: c.Tokens, Limit: c.Limit}
	}
	if f.GoalID != "" {
		row.Goal = &webapi.RowGoal{ID: string(f.GoalID), Title: titles[f.GoalID]}
	}
	for _, id := range r.DepBlockers {
		row.Waits = append(row.Waits, string(id))
	}
	if st, ok := m.stackRows[f.ID]; ok {
		// the board counts from 1 at the bottom ("2/4"); the contract
		// counts from 0, so the page can index with it.
		row.Stack = &webapi.RowStack{ID: string(st.ID), Name: st.Name, Pos: st.Pos - 1, Of: st.Of, Stale: st.Stale}
	}
	sess := m.sessionFor(f.ID)
	it, needs := m.inbox.get(f.ID)
	switch {
	case needs:
		// needs-you outranks busy, the TUI row's own precedence: a person
		// can act on a raised gate, not on a check still running under it.
		row.Status = webapi.StatusNeeds
		row.Needs = webNeeds(it, f.Stage)
	case m.cardBusy(r):
		row.Status = webapi.StatusRunning
		row.Running = &webapi.RowRunning{Verb: m.cardBusyWord(r), Autopilot: r.AutopilotDriving, Pausing: m.pausing[f.ID]}
	case m.freeformWatching(r):
		row.Status = webapi.StatusWatching
	case sess != nil && sess.State() == engine.StatePaused:
		row.Status = webapi.StatusPaused
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
	return row
}

// webKind is the card's kind with the empty default spelled out.
func webKind(f domain.Feature) domain.Kind {
	if f.Kind == "" {
		return domain.KindFeature
	}
	return f.Kind
}

// webNeeds projects a needs-you item, with the word the rail heads it
// with: a failed verify is raised as an escalated gate, and reads as what
// it is rather than as the gate it was raised as.
func webNeeds(it attnItem, stage domain.Stage) *webapi.RowNeeds {
	n := &webapi.RowNeeds{Question: it.Text}
	switch it.Kind {
	case attnFailure:
		n.Kind, n.Color, n.Word = webapi.NeedsFailure, "err", "failed"
		if stage == domain.StageVerify {
			n.Word = "verify failed"
		}
	case attnQuestion:
		n.Kind, n.Color, n.Word = webapi.NeedsQuestion, "info", "question"
	case attnBudget:
		n.Kind, n.Color, n.Word = webapi.NeedsBudget, "warn", "budget"
	default:
		n.Kind, n.Color, n.Word = webapi.NeedsGate, "ok", gateWord(stage)
		if it.Escalated {
			n.Color = "warn"
			if stage == domain.StageVerify {
				n.Word, n.Color = "verify failed", "err"
			}
		}
	}
	return n
}

// gateWord names a stage's gate the way the page heads it.
func gateWord(stage domain.Stage) string {
	if stage == domain.StagePlan {
		return "design gate"
	}
	return string(stage) + " gate"
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
	if wait := todayEvery - now.Sub(m.today.at); m.today.measured && total == m.today.total && wait > 0 && m.today.day == now.Format(time.DateOnly) {
		if m.today.due {
			return nil
		}
		m.today.due = true
		// a subscription, so a web intent never waits on the header
		return subscription(tea.Tick(wait, func(time.Time) tea.Msg { return todayDueMsg{} }))
	}
	m.today.running = true
	store, rows := m.store, append([]featureRow(nil), m.rows...)
	return func() tea.Msg {
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		rep, err := buildWsReport(context.Background(), store, rows, midnight, now)
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

// webResumeOffer projects the held quit-resume question.
func (m *Shell) webResumeOffer() *webapi.ResumeOffer {
	m.settleQuitResume()
	o := m.resumeOffer
	if o == nil {
		return nil
	}
	out := &webapi.ResumeOffer{Since: o.since, Cards: make([]webapi.CardRef, 0, len(o.cards))}
	for _, c := range o.cards {
		out.Cards = append(out.Cards, webapi.CardRef{ID: string(c.Feature.ID), Title: c.Feature.Title, Stage: string(c.Feature.Stage)})
	}
	return out
}

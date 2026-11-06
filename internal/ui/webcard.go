package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
)

// The card page's head as the web face reads it (DESIGN §20.1): the one
// decision the TUI's card page pins, with the answers its picker offers;
// the card's menu; and what the composer would do with a line. Each is
// read from the function the TUI draws the same thing with — openDecision,
// stageActions and decisions.AskOptions, cardActionsFor,
// classifyThreadLine — so the browser and the terminal cannot offer a
// card different answers.
//
// What the TUI does not show is the render-fit check: visibleDecision
// withholds a decision the terminal is too short to draw, and a page has
// no such limit.

// webOpenDecision is the pinned decision with what answering it needs:
// the TUI decision behind it and each option id's place in it.
type webOpenDecision struct {
	api webapi.Decision
	d   *threadDecision
	// index maps an option id to its index in d's picker (an ask's
	// option, or a workflow action).
	index map[string]int
	// rev says what the decision's revision is read from: "spec", the
	// card's artifact; "branch", its branch head; "" the ref alone.
	rev string
}

// Option ids that are not an index or an action id.
const (
	webOptionChat = "chat"
)

// webOpenDecision is the decision the card page pins right now, or nil.
// It must be called with the card entered (enterCard).
func (m *Shell) webOpenDecision(r featureRow) *webOpenDecision {
	d := m.openDecision(r)
	if d == nil {
		return nil
	}
	if d.ask != nil {
		od := m.worded(m.webAskDecision(r, d))
		if d.run != nil {
			od.api.Word, od.api.Tone = workflowDecisionWord(d.run.Pending), "info"
			od.api.RunKind = string(d.run.Pending.Kind)
		}
		return od
	}
	return m.worded(m.webWorkflowDecision(r, d))
}

// worded fills a decision's word and tone.
func (m *Shell) worded(od *webOpenDecision) *webOpenDecision {
	if od != nil {
		od.api.Word, od.api.Tone = m.webDecisionWord(od.api.Kind)
	}
	return od
}

// webAskDecision is an open ask_user question: its options as the picker
// offers them, the "Chat about this" row last.
func (m *Shell) webAskDecision(r featureRow, d *threadDecision) *webOpenDecision {
	ask := d.ask
	id := ask.DecisionID
	if id == "" {
		id = ask.CallID
	}
	ref := "ask:" + id
	anchor := webapi.AnchorSpec
	if !cardHasArtifact(r) {
		anchor = webapi.AnchorThread
	}
	od := &webOpenDecision{
		d:     d,
		index: map[string]int{},
		api: webapi.Decision{
			Ref: ref, Kind: webapi.DecisionAsk, Question: ask.Question, Anchor: anchor,
			// the label is what a "moved" refusal names the stop by: a
			// sentence a person recognises, not the decision's hash
			Against: webapi.Against{Token: ref, Label: "the agent's question"},
			Options: []webapi.Option{},
			Multi:   ask.MultiPick,
		},
	}
	if d.run != nil {
		// a run's decision is the run's: the thread is where it stands
		// (§25.5)
		od.api.Anchor = webapi.AnchorThread
		od.api.Against.Label = "workflow run " + strconv.Itoa(d.run.N) + "'s " + workflowDecisionWord(d.run.Pending)
	}
	for i, o := range d.askOptions() {
		oid := strconv.Itoa(i)
		if o.Chat {
			oid = webOptionChat
		}
		od.index[oid] = i
		od.api.Options = append(od.api.Options, webapi.Option{
			ID: oid, Label: o.Label, Detail: o.Detail, Danger: o.Danger,
			Chat: o.Chat, Words: o.Chat,
		})
	}
	return od
}

// webWorkflowDecision is a stop's answer set: stageActions in the order
// the picker shows it, the word-eating option marked and relabelled the
// way the picker relabels it once a line is typed.
func (m *Shell) webWorkflowDecision(r featureRow, d *threadDecision) *webOpenDecision {
	kind, rev := webDecisionKind(d.kind, r)
	anchor := webapi.AnchorThread
	ref := string(kind) + ":" + string(r.F.ID) + ":" + string(r.F.Stage)
	od := &webOpenDecision{
		d: d, index: map[string]int{}, rev: rev,
		api: webapi.Decision{Ref: ref, Kind: kind, Question: d.question, Anchor: anchor, Options: []webapi.Option{}},
	}
	consumer := d.wordConsumer()
	ids := make([]string, 0, len(d.actions))
	for i, a := range d.actions {
		oid := a.id
		for n := 2; ; n++ {
			if _, taken := od.index[oid]; !taken {
				break
			}
			oid = a.id + "#" + strconv.Itoa(n)
		}
		od.index[oid] = i
		ids = append(ids, oid)
		opt := webapi.Option{ID: oid, Label: a.label, Detail: a.webDetail(), Danger: a.danger}
		if a.id == "merge" {
			// a landing answer offers the method choice the menu's does
			opt.Methods = webLandMethods(r.F)
		}
		if i == consumer {
			opt.Words = true
			opt.Relabel = a.label + " with your words"
		}
		od.api.Options = append(od.api.Options, opt)
	}
	// the options are part of what the answer was given against: a stop
	// whose answers changed (a blocker cleared) is not the one the page
	// showed
	od.api.Against.Token = ref + "#" + webHash(strings.Join(ids, ","))
	return od
}

// webDecisionWord is the word the page heads a decision with, and its
// tone.
func (m *Shell) webDecisionWord(kind webapi.DecisionKind) (word, tone string) {
	switch kind {
	case webapi.DecisionGate:
		return "needs you", ""
	case webapi.DecisionAsk:
		return "question", "info"
	case webapi.DecisionConflict:
		return "conflict", "err"
	case webapi.DecisionBudget:
		return "envelope spent", "warn"
	case webapi.DecisionConfirm:
		return "confirm", "warn"
	case webapi.DecisionFailure:
		return "stage failed", "err"
	case webapi.DecisionClosed:
		return "closed", ""
	}
	return "idle", ""
}

// webDecisionKind maps the picker's kind to the contract's, with what its
// revision is read from: a session's branch. Every such decision is about
// the thread (webapi.AnchorThread).
func webDecisionKind(k decisionKind, r featureRow) (webapi.DecisionKind, string) {
	rev := ""
	if r.F.Stage == domain.StageOpen {
		rev = "branch"
	}
	switch k {
	case decisionBudget:
		return webapi.DecisionBudget, rev
	case decisionFailure:
		return webapi.DecisionFailure, rev
	case decisionClosed:
		return webapi.DecisionClosed, ""
	}
	return webapi.DecisionIdle, rev
}

func webHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// webCardState is WebCard plus what finishing it off the loop needs.
type webCardState struct {
	card webapi.Card
	f    domain.Feature
	rev  string
}

// webCard projects one card's head, decision, menu and composer. The
// decision's revision and the count of decisions behind it are IO, so
// they are left for Bridge.Card to fill off the loop.
func (m *Shell) webCard(id domain.FeatureID) (webCardState, bool) {
	r, ok := m.rowByID(id)
	if !ok {
		return webCardState{}, false
	}
	leave := m.enterCard(id, true)
	defer leave()
	// a page has this card open: watch its branch and artifact for the
	// writes the store never hears of (freshness.go)
	m.watchCard(r.F)
	st := webCardState{f: r.F, card: webapi.Card{
		Row:      m.webRow(r),
		Branch:   r.F.BranchName(),
		Base:     r.baseBranch(),
		Adopted:  r.F.Adopted(),
		OneLiner: r.F.OneLiner,
	}}
	if r.F.Kind == domain.KindResearch {
		// a research card runs in a detached scratch tree and never gets
		// a branch: naming one (and a base it would land "onto") is the
		// head asserting a checkout that does not exist
		st.card.Branch, st.card.Base, st.card.Scratch = "", "", true
	}
	if r.F.MainCheckout {
		// a main-checkout session works in the repository's own checkout:
		// no branch of its own, and nothing it would land onto
		st.card.Branch, st.card.Base = "", ""
	}
	if od := m.webOpenDecision(r); od != nil {
		dec := od.api
		st.card.Decision, st.rev = &dec, od.rev
	}
	st.card.Actions = m.webActions(r)
	st.card.LandHeld, st.card.LandHeldBy = m.webLandHeld(r, st.card.Actions)
	st.card.Composer = m.webComposer(r, "")
	st.card.Session = m.webSessionOf(r.F)
	st.card.Workflows = m.webWorkflowsOf(r.F)
	st.card.Repos = webRepos(r.Repos)
	return st, true
}

// webLandHeld is why an open session that would otherwise offer a landing
// does not right now: its open diff comments, its open workflow run, or
// the card below it in its stack. Each is a hold a person clears, which
// the head shows in place of the landing. A turn in flight is not one:
// the head offers stopping it instead, and it clears by itself.
func (m *Shell) webLandHeld(r featureRow, acts []webapi.Action) (why, by string) {
	if !r.F.IsFreeform() || r.F.Stage != domain.StageOpen || r.F.MainCheckout || !r.HasWorktree || r.Landed || !r.F.PullRequest.Empty() || r.watchOnly() {
		return "", ""
	}
	if slices.ContainsFunc(acts, func(a webapi.Action) bool { return a.ID == "merge" }) {
		return "", ""
	}
	switch {
	case r.OpenDiffComments > 0:
		n := r.OpenDiffComments
		return fmt.Sprintf("%d open comment%s", n, plural(n)), "comments"
	case r.OpenRun != nil:
		return fmt.Sprintf("run %d is %s", r.OpenRun.N, r.OpenRun.State), "run"
	}
	if b := m.stackLandBlocker(r.F.ID); b != "" {
		return "lands after " + m.cardName(b), "stack"
	}
	return "", ""
}

// WebCard is one card's head as the loop knows it. ok is false for a
// card the board does not have. Bridge.Card is the complete read.
func (m *Shell) WebCard(id string) (webapi.Card, bool) {
	st, ok := m.webCard(webID(id))
	return st.card, ok
}

// Card is GET /api/cards/{id}: the head the loop projects, finished off
// the loop with the decision's revision and how many decisions wait
// behind it — both reads of the store and the repository that Update
// must not make.
func (b *Bridge) Card(ctx context.Context, id string) (webapi.Card, error) {
	var (
		st webCardState
		ok bool
		m  *Shell
	)
	// a card whose repository can still move is offered the clones made since
	// launch; the set is read off the loop before the card's actions are built
	b.refreshReposFor(ctx, webID(id))
	if err := b.Do(ctx, func(s *Shell) tea.Cmd { st, ok = s.webCard(webID(id)); m = s; return nil }); err != nil {
		return webapi.Card{}, err
	}
	if !ok {
		return webapi.Card{}, refuse(WebNotFound, "no card "+id+" on this board")
	}
	c := st.card
	if c.Decision != nil && st.rev != "" {
		rev, label := m.webRevision(ctx, st.f, st.rev)
		c.Decision.Against.Token += "@" + rev
		c.Decision.Against.Label = label
	} else if c.Decision != nil && c.Decision.Against.Label == "" {
		c.Decision.Against.Label = "the card at " + c.Stage
	}
	c.DecisionsMore = m.webDecisionsMore(ctx, st.f.ID, c.Decision)
	m.webActionDefaults(ctx, st.f, c.Actions)
	m.webClosePush(ctx, st.f, &c)
	c.Actions = m.webDropCleanCommit(ctx, st.f, c.Actions)
	if c.Decision != nil {
		c.Decision.Proposal = m.webProposal(ctx, st.f)
	}
	if dir, ok := filesDir(ctx, m.wt, st.f); ok {
		// the server, which holds the key, fills in the URL
		c.Files = &webapi.Files{Dir: dir}
	}
	// the server, which knows whether it serves one, keeps this or drops it
	c.Terminal = c.Files != nil || termMakeable(st.f)
	return c, nil
}

// webClosePush names, on the card's landings, the remote its repository
// closes cards on by push: the page then offers the base push as the
// landing's second half. It reads the repository's config, so it runs off
// the loop.
func (m *Shell) webClosePush(ctx context.Context, f domain.Feature, c *webapi.Card) {
	if m.wt == nil || f.Kind == domain.KindGoal {
		return
	}
	mgr, err := m.wt.ManagerFor(ctx, &f)
	if err != nil {
		return
	}
	remote := mgr.ClosePushRemote(ctx)
	if remote == "" {
		return
	}
	landing := func(id string) bool {
		return id == "merge" || (id == "advance" && f.Stage == domain.StageVerify)
	}
	for i := range c.Actions {
		if landing(c.Actions[i].ID) {
			c.Actions[i].PushTo = remote
		}
	}
	if c.Decision != nil {
		for i := range c.Decision.Options {
			if landing(c.Decision.Options[i].ID) {
				c.Decision.Options[i].PushTo = remote
			}
		}
	}
}

// webDropCleanCommit leaves a session's "commit" out of its menu while the
// worktree holds nothing to commit. Asking git is IO, so it is decided here,
// off the loop, rather than when the menu is built.
func (m *Shell) webDropCleanCommit(ctx context.Context, f domain.Feature, acts []webapi.Action) []webapi.Action {
	i := slices.IndexFunc(acts, func(a webapi.Action) bool { return a.ID == "commit" })
	if i < 0 {
		return acts
	}
	if m.wt != nil {
		if dirty, err := m.wt.Dirty(ctx, &f); err == nil && dirty {
			return acts
		}
	}
	return slices.Delete(acts, i, i+1)
}

// webLandingEmpty is what a landing entry says when no message has been
// drafted for it yet: there is no drafted one to use, so an empty message
// has gummi draft one, which the dialog then stops to have read.
const webLandingEmpty = " — leave the message empty and gummi drafts one for you to read first"

// webActionDefaults fills the inputs whose suggested value is a read of
// the store or the repository, which the loop must not make: the landing
// message the merge dialog would open on. Off the loop.
func (m *Shell) webActionDefaults(ctx context.Context, f domain.Feature, acts []webapi.Action) {
	for i := range acts {
		a := &acts[i]
		switch a.ID {
		case "merge", "squash":
			if msg := m.webLandingDraft(ctx, f); msg != "" {
				// the draft is in the box, so there is nothing to leave empty
				a.Default = msg
				a.Detail = strings.TrimSuffix(a.Detail, webLandingEmpty)
			}
		}
	}
}

// webLandingDraft is the message the landing dialog opens on without
// drafting: a goal's merge message, or the landing message the verify
// gate pre-drafted for the branch as it stands (engine.PendingCommitDraft,
// the store half of engine.LandingMessage). "" when there is none, and
// the dialog would draft at the keypress. Off the loop.
func (m *Shell) webLandingDraft(ctx context.Context, f domain.Feature) string {
	if m.engine == nil {
		return ""
	}
	return m.engine.PendingCommitDraft(ctx, f)
}

// webRevision reads what a decision was raised on: the artifact's content
// or the branch head. It does IO and runs off the loop; it touches only
// the Shell's fixed wiring (store, pool, workspace).
func (m *Shell) webRevision(ctx context.Context, f domain.Feature, rev string) (token, label string) {
	switch rev {
	case "spec":
		path := m.artifactFile(&f)
		if path == "" {
			return "none", "no " + artifactNoun(f.Kind) + " yet"
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "unreadable", artifactNoun(f.Kind) + " unreadable"
		}
		rev := spec.Rev(b)
		return rev, artifactNoun(f.Kind) + " " + rev[:7]
	case "branch":
		if m.wt == nil {
			return "none", "no branch"
		}
		head, err := m.wt.Head(ctx, &f)
		if err != nil || head == "" {
			return "none", "no branch yet"
		}
		if len(head) > 7 {
			head = head[:7]
		}
		if f.MainCheckout {
			return head, "main checkout at " + head
		}
		return head, f.BranchName() + " at " + head
	}
	return "", ""
}

// webDecisionsMore counts the card's other open decisions: the durable
// records (§10.18) that are not the one pinned. Off the loop.
func (m *Shell) webDecisionsMore(ctx context.Context, id domain.FeatureID, shown *webapi.Decision) int {
	if m.store == nil {
		return 0
	}
	open, err := m.store.OpenDecisions(ctx)
	if err != nil {
		return 0
	}
	n, matched := 0, false
	for _, d := range open[id] {
		if d.Kind == state.DecisionKindIdle {
			continue
		}
		n++
		if shown != nil && !matched && d.Kind == string(shown.Kind) {
			matched = true
		}
	}
	if matched {
		n--
	}
	return n
}

// webActionHidden are the menu entries the page does not list as actions:
// the ones that only open a surface the page already shows (its tabs, its
// thread) or a terminal the browser has no way to host.
var webActionHidden = map[string]bool{
	"spec": true, "diff": true, "attach": true, "inbox": true,
	"ask": true, "goalpage": true, expandID: true,
}

// webActions is the card's menu: cardActionsFor plus the repository
// picker its o key opens, less what webActionHidden leaves out.
func (m *Shell) webActions(r featureRow) []webapi.Action {
	in := m.nextInputFor(r)
	list := cardActionsFor(in, r)
	if r.F.IsFreeform() {
		// the rows are cardActionsFor's own (one inventory for both
		// faces, the model switch included); the commit is the one
		// web-only append here, listed only while the worktree holds
		// something to commit, which Bridge.Card drops off the loop when
		// it does not. A main-checkout session has no branch to commit
		// to, and sweeping the checkout's loose work into one is not
		// this card's to do.
		if r.F.Stage == domain.StageOpen && !r.watchOnly() && m.engine != nil {
			if !r.F.MainCheckout {
				list = append(list,
					cardAction{id: "commit", label: "commit", why: "commit everything in the worktree to " + r.F.BranchName() + " — the message starts as the session's title; until you commit, Land commits whatever is left as a final checkpoint"})
			}
		}
	}
	if m.repoPickable(r) {
		list = append(list, cardAction{id: "repo", key: "o", label: "repository", why: "choose the repository this card works in"})
	}
	out := make([]webapi.Action, 0, len(list))
	for _, a := range list {
		if webActionHidden[a.id] {
			continue
		}
		// enter on a card another process runs only opens what the page
		// has open already
		if a.id == "run" && a.label == "watch" {
			continue
		}
		act := webapi.Action{ID: a.id, Label: strings.TrimSuffix(a.label, "…"), Key: a.key, Danger: a.danger, Detail: a.why}
		if a.id == "merge" {
			// the page calls it what its head's button does — Land — so the
			// menu row, the dialog and its confirm read as one act; the id
			// and the terminal's m key stay merge
			act.Label = "land"
		}
		m.webActionInput(r, &act)
		out = append(out, act)
	}
	return out
}

// webLandMethods names the landing methods a card's landing offers on the
// page: squash and merge commit for a card that may keep its commits, nil
// otherwise (a goal's card, or one in a goal, lands as a squash only, and a
// squash in place is not a landing). nil is what leaves the page its one
// squash field.
func webLandMethods(f domain.Feature) []string {
	if !f.Offers(domain.LandMerge) {
		return nil
	}
	return []string{string(domain.LandSquash), string(domain.LandMerge)}
}

// webActionInput says what an action's flow will ask for, so the page
// collects it first: the dialog the TUI opens on the way, as a field.
func (m *Shell) webActionInput(r featureRow, a *webapi.Action) {
	if strings.HasPrefix(a.ID, landRepoID) {
		a.Needs = webapi.ActionNeedsMessage
		a.Methods = webLandMethods(r.F)
		return
	}
	switch a.ID {
	case "merge", "squash":
		// the drafted message, when there is one, is the input's default
		// (Bridge.Card fills it off the loop)
		a.Needs = webapi.ActionNeedsMessage
		a.Detail += webLandingEmpty
		if a.ID == "merge" {
			a.Methods = webLandMethods(r.F)
		}
	case "newbug":
		a.Needs = webapi.ActionNeedsMessage
	case "commit":
		// a commit's message opens on the session's title, to be read and
		// edited, rather than as an empty box the dialog then refuses
		a.Needs = webapi.ActionNeedsMessage
		a.Default = r.F.Title
	case "envelope":
		a.Needs = webapi.ActionNeedsNumber
		a.Default = strconv.Itoa(r.F.Budget.Envelope)
	case "prlink":
		// the dialog's field: a URL or a number, or nothing, which finds
		// the one open pull request for the card's branch
		a.Needs = webapi.ActionNeedsText
		a.Detail = "link " + r.F.Name() + " to a GitHub pull request by its URL or number — leave it empty to find the open one for " + r.F.BranchName()
	case repoAttachID:
		a.Needs = webapi.ActionNeedsRepo
		for _, n := range m.repoChoices(r) {
			a.Choices = append(a.Choices, webapi.Choice{Value: n, Label: n})
		}
	case repoDetachID:
		a.Needs = webapi.ActionNeedsRepo
		for _, v := range r.Repos {
			if !v.Primary {
				a.Choices = append(a.Choices, webapi.Choice{Value: v.Repo, Label: v.Repo})
			}
		}
	case "repo":
		a.Needs = webapi.ActionNeedsRepo
		a.Default = r.F.Repo
		for _, n := range m.repoNames {
			a.Choices = append(a.Choices, webapi.Choice{Value: n, Label: n})
		}
	case "objective":
		// the text, or one of the verbs the page's strip sends as it
		a.Needs = webapi.ActionNeedsText
		a.Detail = "what done looks like — the session keeps going until an auditor finds it met. " +
			"Start with --check 'cmd' to make a command the judge; pause, resume, stop or clear act on the one it has"
		if ff := m.engine.Freeform(r.F.ID); ff != nil {
			if o := ff.Snapshot().Objective; o != nil && !o.State.Settled() {
				a.Default = o.Text
			}
		}
	case "rename":
		a.Needs = webapi.ActionNeedsText
		a.Default = r.F.Title
	case "heartbeat":
		// the page opens its own schedule form for this card; the
		// definition is written through /api/schedules, not this action
		a.Needs = webapi.ActionNeedsSchedule
	case "model":
		a.Needs = webapi.ActionNeedsModel
		if s := m.webSessionOf(r.F); s != nil {
			a.Default = s.Backend + " " + s.Model
		}
	case "prcreate", "push", "prready", "prdraft":
		// the page's publish dialog reads the facts itself and confirms
		// through POST /api/cards/{id}/publish, never through this action
		a.Needs = webapi.ActionNeedsPublish
		a.Default = string(publishActs[a.ID])
	case publishSpanID:
		// the page's span dialog reads every repository's facts and
		// confirms through POST /api/cards/{id}/publish/span
		a.Needs = webapi.ActionNeedsPublish
		a.Default = publishSpanAct
	case "delete", "clean", "duplicate", "handoff", "adopt", "prunlink", "goalstop":
		a.Needs = webapi.ActionNeedsConfirm
	case "prchecks":
		// from verify the card goes back to implement: a stage move is
		// confirmed, a message to a session is not
		if r.F.Stage == domain.StageVerify {
			a.Needs = webapi.ActionNeedsConfirm
		}
	}
}

// repoPickable mirrors boardVerb's o: a card whose repository can still
// move, and somewhere else to move it to.
func (m *Shell) repoPickable(r featureRow) bool {
	return m.repoMovable(r) && len(m.repoNames) > 0
}

// repoMovable is repoPickable without the names: a card whose repository can
// still move — no worktree yet, not watch-only.
func (m *Shell) repoMovable(r featureRow) bool {
	return !r.F.Home && !r.HasWorktree && !r.watchOnly()
}

// refreshReposFor reads the repositories off the loop before the card id's
// repository action is offered or run, when the set is discovered and the
// card's repository can still move. A fixed set, a card past that point, and a
// card not on the board pay nothing.
func (b *Bridge) refreshReposFor(ctx context.Context, id domain.FeatureID) {
	var refresh bool
	if err := b.Do(ctx, func(s *Shell) tea.Cmd {
		i := s.rowIndex(id)
		refresh = s.wt != nil && s.wt.Discovering() && i >= 0 && s.repoMovable(s.rows[i])
		return nil
	}); err != nil || !refresh {
		return
	}
	b.RefreshRepos(ctx)
}

// webComposer says what the composer would do with text — an empty line
// reads as prose, which is what the page is about to type.
func (m *Shell) webComposer(r featureRow, text string) webapi.Composer {
	line := strings.TrimSpace(text)
	if line == "" {
		line = "…"
	}
	c := m.classifyThreadLine(r, line, func() *threadDecision { return m.openDecision(r) })
	route, says := m.webLineRoute(r, line, c)
	images := m.engine != nil && m.engine.SessionTakesImages(context.Background(), r.F.ID)
	out := webapi.Composer{Route: route, Says: says, Images: images}
	// the line as typed: "/review " is a command awaiting its arguments,
	// not a word still being completed
	if word, ok := strings.CutPrefix(strings.TrimLeft(text, " \t\n"), "/"); ok {
		// the session's own commands first, then the repository's files
		// and last the backend's own commands and skills
		var session, project, backend []webapi.Completion
		for _, c := range m.projectCommandsPrefixed(r, word, 64) {
			switch {
			case c.Backend != "":
				if len(backend) < 8 {
					backend = append(backend, webapi.Completion{Text: "/" + c.Name + " ", Detail: c.Description, Group: "agent"})
				}
			case c.Source == "":
				session = append(session, webapi.Completion{Text: "/" + c.Name + " ", Detail: c.Description, Group: "session"})
			case len(project) < 8:
				project = append(project, webapi.Completion{Text: "/" + c.Name + " ", Detail: c.Description, Group: "project"})
			}
		}
		out.Completions = append(append(session, project...), backend...)
		// gummi's own words, while the word is still being typed — a
		// completed one is a line for the server to route, not a picker
		if !strings.ContainsAny(word, " \t\n") {
			for _, c := range m.cardSlashCompletions(r, word) {
				// a word the session's own command already offers (/rename,
				// /objective) is that command: the line runs it, not the row
				if slices.ContainsFunc(out.Completions, func(o webapi.Completion) bool { return o.Text == c.Text }) {
					continue
				}
				c.Group = "card"
				out.Completions = append(out.Completions, c)
			}
		}
	} else if word, ok := mentionWord(text); ok {
		if dir, ok := filesDir(context.Background(), m.wt, r.F); ok {
			out.Completions = fileCompletions(dir, word)
		}
	}
	return out
}

// cardSlashMax bounds how many of the card's own words one completion
// offers, beside the project commands' own eight — enough for the bare
// "/" that lists what the card can do, few enough that the picker stays a
// picker. The box scrolls; a vocabulary this wide is what the menu is for.
const cardSlashMax = 12

// cardSlashCompletions is the card's own "/" vocabulary: the words a
// reader can type after a "/" here, each named for the action it runs or
// the menu row it lands on. The words are the ones the TUI's "/" menu
// offers on a card page — cardCommandNames' aliases for the action, plus
// the action's own id — filtered by the partly typed word, with the menu's
// own description as the detail. What the card does not offer right now is
// no word here either, exactly as it is no row in the menu.
//
// A word the verb vocabulary maps to a specific action is claimed only by
// that action ("/land" completes toward the merge the verb fires, never
// toward an advance row that also says "land"), so completing a word and
// sending it routes the way the says line already promised.
func (m *Shell) cardSlashCompletions(r featureRow, word string) []webapi.Completion {
	lower := strings.ToLower(word)
	var out []webapi.Completion
	seen := map[string]bool{}
	for _, a := range m.webActions(r) {
		detail := a.Detail
		if detail == "" {
			detail = a.Label
		}
		for _, w := range append(strings.Fields(cardCommandNames[a.ID]), a.ID) {
			w = strings.ToLower(w)
			if w == "" || seen[w] || !strings.HasPrefix(w, lower) {
				continue
			}
			if id, ok := verbActionIDs[w]; ok && id != a.ID {
				continue
			}
			seen[w] = true
			out = append(out, webapi.Completion{Text: "/" + w + " ", Detail: detail})
			if len(out) >= cardSlashMax {
				return out
			}
		}
	}
	return out
}

// webLineRoute names a classified line's destination in the contract's
// words, and says it the way the page shows it under the composer.
func (m *Shell) webLineRoute(r featureRow, text string, c lineClass) (webapi.Route, string) {
	switch c.route {
	case lineConsult:
		return webapi.RouteConsult, "asks " + r.F.Name() + "'s consult agent — " + r.watchDriver() + " is driving it, so this never steers"
	case lineFreeformTurn:
		if cmd, ok := m.projectCommandFor(r, text); ok {
			if cmd.Backend != "" {
				return webapi.RouteFreeform, "runs " + cmd.Backend + "'s own /" + cmd.Name + " — sent as typed"
			}
			if cmd.Source == "" { // gummi's own /compact, not a file's
				return webapi.RouteFreeform, "/" + cmd.Name + " — " + cmd.Description
			}
			return webapi.RouteFreeform, "runs the project's /" + cmd.Name + " (" + cmd.Source + ")"
		}
		return webapi.RouteFreeform, freeformTurnRoute(r)
	case lineAskAnswer:
		return webapi.RouteAnswer, "answers the question above, in your words"
	case lineWords:
		return webapi.RouteAnswer, "goes with “" + c.d.actions[c.consumer].label + "”"
	}
	parsed := parseInput(text)
	switch parsed.Kind {
	case verbMenu:
		if cmds := m.projectCommandsPrefixed(r, parsed.Remainder, 5); len(cmds) > 0 {
			names := make([]string, len(cmds))
			for i, c := range cmds {
				names[i] = "/" + c.Name
			}
			return webapi.RouteMenu, "opens the card's menu — or finish a command: " + strings.Join(names, " ")
		}
		return webapi.RouteMenu, "opens the card's menu"
	case verbCommand:
		if parsed.Verb == "ask" {
			return webapi.RouteConsult, "asks the card's consult agent — read-only, never steers"
		}
		if m.verbDegrades(r, parsed.Verb) {
			if c.d == nil || len(c.d.actions) == 0 {
				// no decision is pinned: there are no answers above to
				// set it apart from
				return webapi.RouteMenu, "opens the card's menu at “" + parsed.Verb + "”"
			}
			return webapi.RouteMenu, "“" + parsed.Verb + "” is in the card's menu, not one of the answers above"
		}
		return webapi.RouteVerb, "runs " + parsed.Verb
	}
	return m.webMessageRoute(r)
}

// projectCommandFor is the repository command a freeform card's line
// invokes, if it invokes one.
func (m *Shell) projectCommandFor(r featureRow, text string) (engine.ProjectCommand, bool) {
	if !r.F.IsFreeform() || m.engine == nil {
		return engine.ProjectCommand{}, false
	}
	ff := m.engine.Freeform(r.F.ID)
	if ff == nil {
		return engine.ProjectCommand{}, false
	}
	c, _, ok := engine.FindProjectCommand(ff.Commands(), text)
	return c, ok
}

// projectCommandsPrefixed is each repository command a partly typed word
// could still become, at most limit of them.
func (m *Shell) projectCommandsPrefixed(r featureRow, word string, limit int) []engine.ProjectCommand {
	if !r.F.IsFreeform() || m.engine == nil || strings.ContainsAny(word, " \t\n") {
		return nil
	}
	ff := m.engine.Freeform(r.F.ID)
	if ff == nil {
		return nil
	}
	var out []engine.ProjectCommand
	for _, c := range ff.Commands() {
		if strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(word)) && len(out) < limit {
			out = append(out, c)
		}
	}
	return out
}

// webMessageRoute is where sendThreadMessage delivers prose: the card's
// own session, or a retired workflow card's consult session.
func (m *Shell) webMessageRoute(r featureRow) (webapi.Route, string) {
	if r.F.IsFreeform() {
		return webapi.RouteFreeform, freeformTurnRoute(r)
	}
	return webapi.RouteConsult, "asks the card's consult agent — read-only, never steers"
}

// freeformTurnRoute says where a freeform turn goes, in the words of the
// place it works: its own branch, or the main checkout it was minted
// into (DESIGN §19) — where nothing is committed for it and the work
// stays loose.
func freeformTurnRoute(r featureRow) string {
	if r.F.Home {
		return "a turn for this agent"
	}
	if r.F.MainCheckout {
		return "a turn for this card's agent — it works in the main checkout, uncommitted"
	}
	return "a turn for this card's agent — it works on the branch"
}

// answerTo finds the answer the log holds to the decision ref names — an
// ask's by its decision id, a stop's by the crossing out of its stage — so
// a stale answer can be told "someone answered it" only when someone did.
// Off the loop.
func (m *Shell) answerTo(ctx context.Context, id domain.FeatureID, ref string) (by, receipt string, ok bool) {
	if m.store == nil {
		return "", "", false
	}
	parts := strings.SplitN(ref, ":", 3)
	evs, err := m.store.Events(ctx, id)
	if err != nil || len(parts) < 2 {
		return "", "", false
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		switch {
		case parts[0] == "ask" && ev.Kind == state.EventAsk:
			var p state.AskPayload
			if jsonInto(ev.Payload, &p) && p.Answer != "" && p.ID != "" && p.ID == strings.TrimPrefix(ref, "ask:") {
				return threadfold.AskAnswerer(p), threadfold.AskLine(p), true
			}
		case parts[0] != "ask" && len(parts) == 3 && ev.Kind == state.EventGate:
			var p state.GatePayload
			if jsonInto(ev.Payload, &p) && p.From == parts[2] {
				return threadfold.GateCrosser(p), threadfold.GateLine(p), true
			}
		}
	}
	return "", "", false
}

// jsonInto decodes a payload, reporting whether it could.
func jsonInto(payload string, v any) bool { return json.Unmarshal([]byte(payload), v) == nil }

// webRepos is a session's repositories as the page is told them.
func webRepos(in []engine.RepoView) []webapi.CardRepo {
	var out []webapi.CardRepo
	for _, v := range in {
		label := v.Repo
		if label == "" {
			label = "workspace"
		}
		out = append(out, webapi.CardRepo{
			Repo: v.Repo, Label: label, Primary: v.Primary, Branch: v.Branch, Base: v.Base,
			Ahead: v.Ahead, Dirty: v.Dirty, Landed: v.Landed,
		})
	}
	return out
}

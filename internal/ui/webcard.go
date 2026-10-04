package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/decisions"
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
	api  webapi.Decision
	d    *threadDecision
	chip *reentryReading
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
	webOptionGo   = "go"
	webOptionKeep = "keep"
)

// webOpenDecision is the decision the card page pins right now, or nil.
// It must be called with the card entered (enterCard).
func (m *Shell) webOpenDecision(r featureRow) *webOpenDecision {
	d := m.openDecision(r)
	if p := m.chip(r.F.ID); p != nil && (d == nil || d.ask == nil) {
		return m.worded(m.webChipDecision(r, p), r)
	}
	if d == nil {
		return nil
	}
	if d.ask != nil {
		return m.worded(m.webAskDecision(r, d), r)
	}
	return m.worded(m.webWorkflowDecision(r, d), r)
}

// worded fills a decision's word and tone.
func (m *Shell) worded(od *webOpenDecision, r featureRow) *webOpenDecision {
	if od != nil {
		od.api.Word, od.api.Tone = m.webDecisionWord(od.api.Kind, r)
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
			Against: webapi.Against{Token: ref, Label: "question " + webHash(id)},
			Options: []webapi.Option{},
			Multi:   ask.MultiPick,
		},
	}
	for i, o := range decisions.AskOptions(ask) {
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
	in := m.nextInputFor(r)
	kind, anchor, rev := webDecisionKind(d.kind, r)
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
		if i == consumer {
			opt.Words = true
			opt.Relabel = a.label + " with your words"
		}
		// A send-back that lands on implement takes the card's open diff
		// comments with it: every implement run folds them into its hints
		// (engine newAgentSession), which is what "send it back" from a
		// failed verify means.
		if a.sendBack && in.openDiffComments > 0 &&
			(in.stage == domain.StageVerify || in.stage == domain.StageImplement) {
			opt.CarriesComments = true
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
// tone: the kind alone cannot say it — a verify decision is a landing on
// a passed verify and a failure on a failed one.
func (m *Shell) webDecisionWord(kind webapi.DecisionKind, r featureRow) (word, tone string) {
	switch kind {
	case webapi.DecisionGate:
		return gateWord(r.F.Stage), ""
	case webapi.DecisionAsk:
		return "question", "info"
	case webapi.DecisionVerify:
		// the same reading the question under it is worded from
		// (decisionQuestion): the verdict the verify run exited on. It
		// used to key on the verified stamp, which a board-driven pass
		// writes a beat after the gate is raised and a research card
		// never gets at all — so a pass the question called "verification
		// passed" was headed "verify failed" above it.
		in := m.nextInputFor(r)
		if in.verdict == verdictPass {
			return "verify passed", "ok"
		}
		// a pass gummi's own floor refused is not a verify that failed:
		// it ran and passed, and what stopped it is the overrule
		if in.flooredPass {
			return "verify overruled", "warn"
		}
		return "verify failed", "err"
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

// webDecisionKind maps the picker's kind to the contract's, with the tab
// the decision is about and what its revision is read from. A gate is
// about the document its stage wrote — at implement that is the diff; a
// stop's revision is what the stage it stops works on: the spec at the
// design stage, the branch once there is code.
func webDecisionKind(k decisionKind, r featureRow) (webapi.DecisionKind, webapi.Anchor, string) {
	rev := ""
	switch r.F.Stage {
	case domain.StagePlan:
		if cardHasArtifact(r) {
			rev = "spec"
		}
	case domain.StageImplement, domain.StageVerify, domain.StageOpen:
		rev = "branch"
	}
	// A research card never gets a branch: its work stages write the
	// research document in a scratch tree, and that document is what
	// every one of its stops is about and raised on.
	research := r.F.Kind == domain.KindResearch && cardHasArtifact(r)
	if research && rev != "" {
		rev = "spec"
	}
	switch k {
	case decisionGate:
		if !research && (r.F.Stage == domain.StageImplement || !cardHasArtifact(r)) {
			return webapi.DecisionGate, webapi.AnchorDiff, rev
		}
		return webapi.DecisionGate, webapi.AnchorSpec, rev
	case decisionVerify:
		if research {
			return webapi.DecisionVerify, webapi.AnchorSpec, rev
		}
		return webapi.DecisionVerify, webapi.AnchorDiff, rev
	case decisionBudget:
		return webapi.DecisionBudget, webapi.AnchorThread, rev
	case decisionFailure:
		return webapi.DecisionFailure, webapi.AnchorThread, rev
	case decisionClosed:
		return webapi.DecisionClosed, webapi.AnchorThread, ""
	}
	return webapi.DecisionIdle, webapi.AnchorThread, rev
}

// webChipDecision is the re-entry's confirm chip as a decision: the
// reading of a line the person sent, and the two answers the chip takes —
// do it, or keep the line here as a message.
func (m *Shell) webChipDecision(r featureRow, p *reentryReading) *webOpenDecision {
	act := chipAct(r.F, r.baseBranch(), p)
	ref := "confirm:" + string(r.F.ID) + ":" + webHash(p.line)
	details := chipDetails(r, p)
	return &webOpenDecision{
		chip:  p,
		index: map[string]int{webOptionGo: 0, webOptionKeep: 1},
		api: webapi.Decision{
			Ref: ref, Kind: webapi.DecisionConfirm,
			Question: "I read that as " + readingNoun(p.out) + ".",
			Anchor:   webapi.AnchorThread,
			Against:  webapi.Against{Token: ref, Label: "your line"},
			Options: []webapi.Option{
				// a go that spends is never given on enter (chip.go's
				// goOnEnter): the page marks it, and the answer asks first
				{ID: webOptionGo, Label: "go", Detail: strings.TrimSpace(act + " " + strings.Join(details, " ")), Danger: !p.goOnEnter},
				{ID: webOptionKeep, Label: "keep it here", Detail: "send the line as a message instead"},
			},
		},
	}
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
	titles := map[domain.FeatureID]string{}
	if r.F.GoalID != "" {
		if g, ok := m.rowByID(r.F.GoalID); ok {
			titles[g.F.ID] = g.F.Title
		}
	}
	st := webCardState{f: r.F, card: webapi.Card{
		Row:      m.webRow(r, titles),
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
	if od := m.webOpenDecision(r); od != nil {
		dec := od.api
		st.card.Decision, st.rev = &dec, od.rev
	}
	st.card.Actions = m.webActions(r)
	st.card.Composer = m.webComposer(r, "")
	st.card.Session = m.webSessionOf(r.F)
	return st, true
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
	c.Actions = m.webDropCleanCommit(ctx, st.f, c.Actions)
	if dir, ok := filesDir(ctx, m.wt, st.f); ok {
		// the server, which holds the key, fills in the URL
		c.Files = &webapi.Files{Dir: dir}
	}
	return c, nil
}

// writespecDraftable is the guard the writespec-draft fetch and the
// writespec action share: the card is a session this board can drive, at
// the one stage such a card ever holds, on a board that could run the
// handoff brief's turn at all — the same gate the writespec menu row
// applies. It returns the sentence each refusal says.
func writespecDraftable(f domain.Feature, agentWired bool) string {
	switch {
	case !f.IsFreeform():
		return string(f.ID) + " is a " + string(f.Kind) + " card: it runs through its stages"
	case f.Stage != domain.StageOpen:
		return string(f.ID) + " is closed"
	case !agentWired:
		return string(f.ID) + " cannot continue as a spec: this board has no agent to run the handoff brief's turn"
	}
	return ""
}

// WritespecDraft is GET /api/cards/{id}/writespec-draft: the handoff brief
// the writespec dialog opens on, fetched once at open. The loop's part is
// only the guard — the draft itself is an agent turn (envelope spend,
// seconds, an appended transcript exchange), so it runs off the loop, and
// it is never filled into an action's Default: webActionDefaults runs on
// every card read, and a brief turn fired as a side effect of GET
// /api/cards/{id} would spend the session's envelope every time a page
// refetched it. The refusals any send rides (mid-turn, an unanswered
// question) come back from the engine and map to the busy conflict the
// writespec action has always answered with.
func (b *Bridge) WritespecDraft(ctx context.Context, id string) (webapi.WritespecDraft, error) {
	var (
		f      domain.Feature
		reason string
		ok     bool
		m      *Shell
	)
	if err := b.Do(ctx, func(s *Shell) tea.Cmd {
		r, found := s.rowByID(webID(id))
		if !found {
			return nil
		}
		if reason = writespecDraftable(r.F, s.engine != nil); reason != "" {
			return nil
		}
		f, ok, m = r.F, true, s
		return nil
	}); err != nil {
		return webapi.WritespecDraft{}, err
	}
	if !ok {
		if reason != "" {
			return webapi.WritespecDraft{}, refuse(WebBadRequest, reason)
		}
		return webapi.WritespecDraft{}, refuse(WebNotFound, "no card "+id+" on this board")
	}
	brief, source, err := m.engine.SessionHandoffBrief(ctx, f.ID)
	if err != nil {
		if errors.Is(err, agent.ErrBusy) {
			return webapi.WritespecDraft{}, &WebError{
				Code: WebConflict, Reason: webapi.ConflictBusy,
				Text: sanitize(err.Error()),
			}
		}
		return webapi.WritespecDraft{}, err
	}
	return webapi.WritespecDraft{Brief: brief, Source: string(source)}, nil
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
// message the merge dialog would open on, and the dependencies the
// dependency picker would open with ticked. Off the loop.
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
		case "deps":
			if m.store == nil {
				continue
			}
			deps, err := m.store.ListDependencies(ctx, f.ID)
			if err != nil {
				continue
			}
			ids := make([]string, 0, len(deps))
			for _, d := range deps {
				ids = append(ids, string(d))
			}
			a.Default = strings.Join(ids, ",")
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
	if f.IsGoal() {
		return m.engine.GoalMergeMessage(ctx, f)
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
		b, err := os.ReadFile(path) //nolint:gosec // the card's own artifact
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

// webActions is the card's menu: cardActionsFor plus the Shell-level
// entries the TUI appends (the profile switch) and the repository picker
// its o key opens, less what webActionHidden leaves out.
func (m *Shell) webActions(r featureRow) []webapi.Action {
	in := m.nextInputFor(r)
	list := cardActionsFor(in, r)
	if r.F.IsFreeform() {
		// the writespec row is cardActionsFor's own (one inventory for
		// both faces, the model switch included); the commit is the one
		// web-only append here, listed only while the worktree holds
		// something to commit, which Bridge.Card drops off the loop when
		// it does not. A main-checkout session has no branch to commit
		// to, and sweeping the checkout's loose work into one is not
		// this card's to do.
		if r.F.Stage == domain.StageOpen && !r.watchOnly() && m.engine != nil {
			if !r.F.MainCheckout {
				list = append(list,
					cardAction{id: "commit", label: "commit", why: "commit everything in the worktree to " + r.F.BranchName() + " with your message — until you do, the session's work stays uncommitted, and Land commits whatever is left as a final checkpoint"})
			}
		}
	} else {
		list = append(list, m.cardProfileActions(r.F.Stage)...)
	}
	if m.repoPickable(r) {
		list = append(list, cardAction{id: "repo", key: "o", label: "repository", why: "choose the repository this card works in"})
	}
	out := make([]webapi.Action, 0, len(list))
	for _, a := range list {
		if webActionHidden[a.id] {
			continue
		}
		// enter on a card already running, or blocked on a question, only
		// opens what the page has open already
		if a.id == "run" && (a.label == "watch" || in.hasAsk) {
			continue
		}
		act := webapi.Action{ID: a.id, Label: strings.TrimSuffix(a.label, "…"), Key: a.key, Danger: a.danger, Detail: a.why}
		if a.id == "advance" && advanceLands(r.F, r.Landed) {
			// "next stage" out of verify is the landing (advanceStageAs):
			// the menu says it as the decision's own answer does ("land on
			// main", "land anyway" over a failed verify) and marks it as
			// the line it crosses, rather than as a plain step forward
			// (a card with no stop raised yet lands all the same)
			act.Label = "land on " + r.baseBranch()
			for _, s := range stageActions(in) {
				if s.id == "advance" {
					act.Label, act.Detail = s.label, s.why
					break
				}
			}
			act.Danger = true
		}
		m.webActionInput(r, &act)
		out = append(out, act)
	}
	return out
}

// webActionInput says what an action's flow will ask for, so the page
// collects it first: the dialog the TUI opens on the way, as a field.
func (m *Shell) webActionInput(r featureRow, a *webapi.Action) {
	switch a.ID {
	case "merge", "squash":
		// the drafted message, when there is one, is the input's default
		// (Bridge.Card fills it off the loop)
		a.Needs = webapi.ActionNeedsMessage
		a.Detail += webLandingEmpty
	case "changes", "newbug", "commit":
		a.Needs = webapi.ActionNeedsMessage
	case "envelope":
		a.Needs = webapi.ActionNeedsNumber
		a.Default = strconv.Itoa(r.F.Budget.Envelope)
	case "profile":
		a.Needs = webapi.ActionNeedsProfile
		a.Default = r.F.Profile
		if m.engine != nil {
			for _, p := range m.engine.CardProfiles(r.F.Stage) {
				backend, model := labelBackendModel(p.Backend, p.Model)
				a.Choices = append(a.Choices, webapi.Choice{Value: p.Name, Label: p.Name, Detail: backend + " · " + model})
			}
		}
	case "deps":
		// the current dependencies are the default (Bridge.Card fills
		// them off the loop); the choices are the board's other cards.
		// The page's picker is the field itself, so it says what the
		// choice means rather than which dialog it opens.
		a.Needs = webapi.ActionNeedsCards
		a.Detail = "choose the cards " + string(r.F.ID) + " waits for — it starts implementing once each of them is done"
	case "prlink":
		// the dialog's field: a URL or a number, or nothing, which finds
		// the one open pull request for the card's branch
		a.Needs = webapi.ActionNeedsText
		a.Detail = "link " + string(r.F.ID) + " to a GitHub pull request by its URL or number — leave it empty to find the open one for " + r.F.BranchName()
	case "repo":
		a.Needs = webapi.ActionNeedsRepo
		a.Default = r.F.Repo
		for _, n := range m.repoNames {
			a.Choices = append(a.Choices, webapi.Choice{Value: n, Label: n})
		}
	case "writespec":
		a.Needs = webapi.ActionNeedsSpec
		// the title's suggestion is the page's to fall back on (session.js
		// prefills card.title); the brief is not an input Default can
		// carry — it is multiline, and it is fetched per dialog open from
		// the writespec-draft route, never derived here
		if m.engine != nil {
			// named by what runs first: the spec's plan stage, whose
			// architect reads the session's conversation
			for _, p := range m.engine.CardProfiles(domain.StagePlan) {
				backend, model := labelBackendModel(p.Backend, p.Model)
				a.Choices = append(a.Choices, webapi.Choice{Value: p.Name, Label: p.Name, Detail: backend + " · " + model})
			}
		}
	case "model":
		a.Needs = webapi.ActionNeedsModel
		if s := m.webSessionOf(r.F); s != nil {
			a.Default = s.Backend + " " + s.Model
		}
	case "gate":
		a.Default = autopilotSwitchTo(r.F.GateApproval)
	case "delete", "clean", "duplicate", "handoff", "adopt", "prunlink", "goalstop":
		a.Needs = webapi.ActionNeedsConfirm
	}
}

// repoPickable mirrors boardVerb's o: a card whose repository can still
// move — not a goal, no worktree yet, and somewhere else to move it to.
func (m *Shell) repoPickable(r featureRow) bool {
	return !r.F.IsGoal() && !r.HasWorktree && len(m.repoNames) > 0 && !r.watchOnly()
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
	// an answer route is either an ask's reply, which answers at once, or
	// words going with a stop's answer, which are read first (webLineRoute)
	out := webapi.Composer{Route: route, Says: says, Images: images, Read: route == webapi.RouteAnswer && c.route != lineAskAnswer}
	// the line as typed: "/review " is a command awaiting its arguments,
	// not a word still being completed
	if word, ok := strings.CutPrefix(strings.TrimLeft(text, " \t\n"), "/"); ok {
		for _, c := range m.projectCommandsPrefixed(r, word, 8) {
			out.Completions = append(out.Completions, webapi.Completion{Text: "/" + c.Name + " ", Detail: c.Description})
		}
		// gummi's own words, while the word is still being typed — a
		// completed one is a line for the server to route, not a picker
		if !strings.ContainsAny(word, " \t\n") {
			out.Completions = append(out.Completions, m.cardSlashCompletions(r, word)...)
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
		return webapi.RouteConsult, "asks " + string(r.F.ID) + "'s consult agent — " + r.watchDriver() + " is driving it, so this never steers"
	case lineConducted:
		return webapi.RouteBlocked, fmt.Sprintf("%s is conducted by %s — send the line to %s and its lead reads it next turn", r.F.ID, goalDriver(r.F.GoalID), r.F.GoalID)
	case lineGoalNote:
		return webapi.RouteGoalNote, "a note for the goal's lead — it reads it next turn"
	case lineFreeformTurn:
		if cmd, ok := m.projectCommandFor(r, text); ok {
			if cmd.Source == "" { // gummi's own /compact, not a file's
				return webapi.RouteFreeform, "/" + cmd.Name + " — " + cmd.Description
			}
			return webapi.RouteFreeform, "runs the project's /" + cmd.Name + " (" + cmd.Source + ")"
		}
		return webapi.RouteFreeform, freeformTurnRoute(r)
	case lineAskAnswer:
		return webapi.RouteAnswer, "answers the question above, in your words"
	case lineChat:
		return m.webMessageRoute(r)
	case lineChatReentry, lineChatIntent, lineReentry:
		if sess := m.sessionFor(r.F.ID); sess != nil && sess.Live() {
			return m.webMessageRoute(r)
		}
		if c.consumer >= 0 && c.d != nil {
			return webapi.RouteAnswer, "goes with “" + c.d.actions[c.consumer].label + "” — read first, to place it"
		}
		// no answer takes words: the line is sent as a line and read, as
		// the TUI's enter reads it — never given as the highlighted answer
		return webapi.RouteRead, "is read first, to place it — a message if it is not a change"
	}
	parsed := parseInput(text)
	switch parsed.Kind {
	case verbMenu:
		if cmds := m.projectCommandsPrefixed(r, parsed.Remainder, 5); len(cmds) > 0 {
			names := make([]string, len(cmds))
			for i, c := range cmds {
				names[i] = "/" + c.Name
			}
			return webapi.RouteMenu, "opens the card's menu — or finish a project command: " + strings.Join(names, " ")
		}
		return webapi.RouteMenu, "opens the card's menu"
	case verbCommand:
		if parsed.Verb == "ask" {
			return webapi.RouteConsult, "asks the card's consult agent — read-only, never steers"
		}
		if parsed.Remainder != "" && verbCarriesReason(parsed.Verb) {
			return webapi.RouteVerb, "sends it back with your words"
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
// own session when one is live, its consult session otherwise.
func (m *Shell) webMessageRoute(r featureRow) (webapi.Route, string) {
	if r.F.IsFreeform() {
		return webapi.RouteFreeform, freeformTurnRoute(r)
	}
	if sess := m.sessionFor(r.F.ID); sess.Live() {
		return webapi.RouteSteer, "goes to the " + string(r.F.Stage) + " agent as its next turn"
	}
	return webapi.RouteConsult, "asks the card's consult agent — read-only, never steers"
}

// freeformTurnRoute says where a freeform turn goes, in the words of the
// place it works: its own branch, or the main checkout it was minted
// into (DESIGN §19) — where nothing is committed for it and the work
// stays loose.
func freeformTurnRoute(r featureRow) string {
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

package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// Publishing a card from the board (DESIGN §22): the menu's "open pull
// request…", "push to GitHub", "mark PR ready" and "PR back to draft", and
// the web page's PR tab and head button. Both faces read the same facts
// (publishDoer.prepare) and run the same act (publishDoer.run); neither
// decides anything internal/publish does not.

// publishActs maps a card-menu entry to the act it starts.
var publishActs = map[string]publish.Act{
	"prcreate": publish.ActCreate,
	"push":     publish.ActPush,
	"prready":  publish.ActReady,
	"prdraft":  publish.ActDraft,
}

// EnablePublishing turns the publish acts on for this board, running gh
// from ghBinary ("" for gh on the path). They are offered only once
// detection finds gh signed in; a board nobody enabled (a test scaffold)
// never offers them.
func (m *Shell) EnablePublishing(ghBinary string) {
	m.publishGH, m.publishEnabled = ghBinary, true
}

// publishDetectedMsg carries detection's answer: nil when publishing is
// set up on this machine.
type publishDetectedMsg struct{ err *publish.Error }

// detectPublish is the one detection a board makes (DESIGN §22.2): gh on
// the path and signed in. It runs off the loop, once.
func (m *Shell) detectPublish() tea.Msg {
	_, err := publish.Detect(context.Background(), publish.Env{GH: m.publishGH})
	return publishDetectedMsg{err: err}
}

// publishOffered reports whether the publish acts may be drawn at all.
func (m *Shell) publishOffered() bool {
	return m.publishEnabled && m.publishChecked && m.publishWhy == nil
}

// publishDoer is what an act needs, captured on the loop: the card as the
// board has it, whether an agent holds it, and the board's stores.
type publishDoer struct {
	f    domain.Feature
	gh   string
	busy bool
	// by is the person acting, as the event log names them ("" is the
	// terminal's own "you")
	by string
	// root and drafts locate the card's spec, for the threads that hold
	// its gate
	root, drafts string
	store        *state.Store
	pool         *worktree.Pool
	locks        *state.CardLocks
}

func (m *Shell) publishDoerFor(r featureRow) publishDoer {
	return publishDoer{
		f: r.F, gh: m.publishGH, busy: m.cardBusy(r) || m.webCardBusy(r.F.ID),
		root: m.wt.Root(), drafts: m.ws.DraftsDir(), store: m.store, pool: m.wt, locks: m.locks,
	}
}

func (p publishDoer) input(ctx context.Context) (*worktree.Manager, publish.Input, error) {
	f := p.f
	if cur, err := p.store.GetFeature(ctx, f.ID); err == nil {
		f = cur
	}
	mgr, err := p.pool.ManagerFor(ctx, &f)
	if err != nil {
		return nil, publish.Input{}, err
	}
	anns, err := p.store.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		return nil, publish.Input{}, err
	}
	return mgr, publish.InputFor(ctx, mgr, &f, p.busy, anns, OpenSpecThreads(p.root, p.drafts, f)), nil
}

// OpenSpecThreads counts the open spec threads holding f's gate, as its
// landing counts them (Shell.openQuestionsBlockingGate), for a reader off
// the loop: the workspace root and its drafts directory locate the spec.
func OpenSpecThreads(root, drafts string, f domain.Feature) int {
	if f.IsFreeform() {
		return 0
	}
	path := artifactFileIn(root, drafts, &f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(engine.SpecCommentsHolding(domain.CardTypeOf(&f), f.Stage, spec.Parse(string(raw))))
}

func publishErr(e *publish.Error) *webapi.PublishError {
	if e == nil {
		return nil
	}
	return &webapi.PublishError{Code: string(e.Code), Text: e.Text, Fix: e.Fix, PR: e.PR}
}

// prepare resolves the facts for act and the plan the confirm shows, with
// the words a new PR starts from. draft is the person's choice so far, and
// baseRepo the repository they chose for the PR ("" for the recorded one).
func (p publishDoer) prepare(ctx context.Context, act publish.Act, draft bool, baseRepo string) webapi.PublishFacts {
	out := webapi.PublishFacts{Act: string(act)}
	mgr, in, err := p.input(ctx)
	if err != nil {
		out.Error = publishErr(publish.AsError(err))
		return out
	}
	in.BaseRepo = baseRepo
	env := publish.Env{GH: p.gh}
	fx, perr := publish.Resolve(ctx, env, mgr, in)
	if perr != nil {
		out.Error = publishErr(perr)
		return out
	}
	req := publish.Request{Act: act, Draft: draft}
	if act == publish.ActCreate {
		req.Title, req.Body = publish.Text(ctx, env, mgr, in.Card.F, fx)
	}
	plan, perr := publish.PlanFor(fx, req)
	if perr != nil && perr.Code == publish.CodeNotVerified && !draft && (act == publish.ActPush || act == publish.ActUpdate) {
		// a ready PR and an unverified tip: the one push on offer is the
		// one that returns the PR to draft first, and the confirm says so
		req.Draft = true
		if again, aerr := publish.PlanFor(fx, req); aerr == nil && again.ToDraft {
			plan, perr = again, nil
		}
	}
	out.Error = publishErr(perr)
	out.ToDraft = plan.ToDraft
	out.Summary, out.Fingerprint, out.Commands = plan.Summary, fx.Fingerprint(), plan.Commands
	out.Branch, out.Tip, out.TipSubject, out.Ahead, out.Base = fx.Branch, fx.Tip, fx.TipSubject, fx.Ahead, fx.Base
	out.Remote, out.PushURL, out.Push = fx.Remote, fx.PushURL, string(fx.Push)
	out.Head, out.BaseRepo, out.GH, out.Hook = fx.HeadRef(), fx.BaseRepo, fx.GH, fx.Hook
	if act == publish.ActCreate {
		out.BaseRepos = fx.BaseRepos
	}
	out.Draft, out.DraftLocked, out.DraftWhy = plan.Draft, fx.ReadyWhy != "", fx.ReadyWhy
	out.Title, out.Body = req.Title, req.Body
	if fx.PR != nil {
		out.PR = &webapi.PublishPR{Number: fx.PR.Number, URL: fx.PR.URL, State: fx.PR.State, Draft: fx.PR.Draft, HeadSHA: fx.PR.HeadSHA}
	}
	return out
}

// run is the act itself, under the card's lock: the facts are read again
// and must digest as the ones the person confirmed.
func (p publishDoer) run(ctx context.Context, req webapi.PublishRequest) (webapi.PublishResult, *publish.Error) {
	act, ok := webPublishAct(req.Act)
	if !ok {
		return webapi.PublishResult{}, &publish.Error{Code: publish.CodeFailed, Text: "unknown act " + req.Act}
	}
	if p.locks != nil {
		release, err := p.locks.Acquire(p.f.ID)
		if err != nil {
			return webapi.PublishResult{}, &publish.Error{Code: publish.CodeBusy, Text: cardLockedNotice(p.f.ID, err)}
		}
		defer release()
	}
	mgr, in, err := p.input(ctx)
	if err != nil {
		return webapi.PublishResult{}, publish.AsError(err)
	}
	in.BaseRepo = req.BaseRepo
	res, perr := publish.Do(ctx, publish.Env{GH: p.gh}, mgr, in,
		publish.Request{Act: act, Fingerprint: req.Fingerprint, Title: req.Title, Body: req.Body, Draft: req.Draft},
		func(ctx context.Context, ref domain.PullRequestRef) error {
			return p.store.SetPullRequest(ctx, p.f.ID, ref)
		})
	if perr == nil || res.Partial() {
		// the card's thread says what was published — also the part of a
		// failed act that went through; best-effort, as GitHub already
		// has it
		pay := state.PublishPayload{Act: string(res.Act), Pushed: res.Pushed, Repo: res.Repo, Number: res.PR.Number, URL: res.PR.URL, Draft: res.Draft, ToDraft: res.ToDraft, By: p.by}
		if perr != nil {
			pay.Act, pay.Draft = string(publish.ActPush), false
		}
		_ = p.store.AppendPublish(ctx, p.f.ID, in.Card.F.Stage, pay, time.Now())
	}
	if perr != nil {
		return webapi.PublishResult{}, perr
	}
	return webapi.PublishResult{Act: req.Act, Pushed: res.Pushed, URL: res.PR.URL, Number: res.PR.Number, Draft: res.Draft, ToDraft: res.ToDraft, LinkError: res.LinkErr}, nil
}

// webPublishAct reads an act's name off the wire.
func webPublishAct(s string) (publish.Act, bool) {
	for _, a := range []publish.Act{publish.ActPush, publish.ActCreate, publish.ActUpdate, publish.ActReady, publish.ActDraft} {
		if string(a) == s {
			return a, true
		}
	}
	return "", false
}

// publishResultText is what an act did, in one line for a notice.
func publishResultText(id domain.FeatureID, r webapi.PublishResult) string {
	var parts []string
	if r.Pushed != "" {
		parts = append(parts, "pushed "+domain.ShortRev(r.Pushed))
	}
	switch r.Act {
	case string(publish.ActCreate):
		state := "ready"
		if r.Draft {
			state = "draft"
		}
		parts = append(parts, fmt.Sprintf("opened PR #%d (%s) %s", r.Number, state, r.URL))
	case string(publish.ActUpdate):
		parts = append(parts, fmt.Sprintf("updated PR #%d", r.Number))
	case string(publish.ActReady):
		parts = append(parts, fmt.Sprintf("PR #%d is ready for review", r.Number))
	case string(publish.ActDraft):
		parts = append(parts, fmt.Sprintf("PR #%d is a draft again", r.Number))
	}
	if r.ToDraft {
		parts = append(parts, fmt.Sprintf("PR #%d returned to draft", r.Number))
	}
	text := string(id) + ": " + strings.Join(parts, " · ")
	if r.LinkError != "" {
		text += "\nwarning: " + r.LinkError
	}
	return text
}

func publishErrorText(id domain.FeatureID, e *webapi.PublishError) string {
	t := string(id) + ": " + e.Text
	if e.Fix != "" {
		t += " — " + e.Fix
	}
	return t
}

// --- the TUI's face ---

// publishFactsMsg carries prepared facts back to the loop, which opens the
// confirm (or says why it cannot).
type publishFactsMsg struct {
	f     domain.Feature
	facts webapi.PublishFacts
	// baseRepo is the repository the facts were asked for, "" for none
	baseRepo string
}

// openPublish resolves the facts for act off the loop. baseRepo is the
// repository the person chose for the PR, "" for the recorded one.
func (m *Shell) openPublish(r featureRow, act publish.Act, baseRepo string) tea.Cmd {
	p := m.publishDoerFor(r)
	m.notice = noticeMsg{text: string(r.F.ID) + ": reading where the branch goes…"}
	return func() tea.Msg {
		return publishFactsMsg{f: r.F, facts: p.prepare(context.Background(), act, false, baseRepo), baseRepo: baseRepo}
	}
}

func (m *Shell) handlePublishFacts(msg publishFactsMsg) {
	// a fork nobody has chosen a target for is a question, not a refusal:
	// the confirm opens on the choice
	unchosen := msg.facts.Error != nil && msg.facts.Error.Code == string(publish.CodeBaseUnchosen) && len(msg.facts.BaseRepos) > 1
	if e := msg.facts.Error; e != nil && !unchosen {
		m.notice = noticeMsg{text: sanitize(publishErrorText(msg.f.ID, e)), isErr: true}
		return
	}
	m.notice = noticeMsg{}
	d := newPublishDialog(msg.f, msg.facts, func(req webapi.PublishRequest) tea.Cmd {
		r, ok := m.rowByID(msg.f.ID)
		if !ok {
			return nil
		}
		// busy is read on the loop, as the person confirms
		p := m.publishDoerFor(r)
		m.notice = noticeMsg{text: string(msg.f.ID) + ": publishing…"}
		return func() tea.Msg {
			res, err := p.run(context.Background(), req)
			if err != nil {
				return noticeMsg{text: sanitize(publishErrorText(msg.f.ID, publishErr(err))), isErr: true, reload: true}
			}
			text := sanitize(publishResultText(msg.f.ID, res))
			return noticeMsg{text: text, web: text, reload: true}
		}
	})
	d.baseRepo = msg.baseRepo
	// the facts and their fingerprint are the chosen repository's, so a
	// choice reads them again
	d.onRetarget = func(repo string) tea.Cmd {
		r, ok := m.rowByID(msg.f.ID)
		if !ok {
			return nil
		}
		return m.openPublish(r, publish.ActCreate, repo)
	}
	m.Overlay.Push(d)
}

// publishDialog is the confirm (DESIGN §22): one
// sentence of what happens, the PR's title for a create, the draft choice,
// a pre-push hook to acknowledge, and the details on tab.
type publishDialog struct {
	f        domain.Feature
	facts    webapi.PublishFacts
	title    textinput.Model
	draft    bool
	hookOK   bool
	details  bool
	problem  string
	onSubmit func(webapi.PublishRequest) tea.Cmd
	// baseRepo is the repository the person chose in this confirm, sent
	// back with the yes; onRetarget reopens the confirm on another
	baseRepo   string
	onRetarget func(repo string) tea.Cmd
}

func newPublishDialog(f domain.Feature, facts webapi.PublishFacts, onSubmit func(webapi.PublishRequest) tea.Cmd) *publishDialog {
	in := textinput.New()
	in.Placeholder = "pull request title"
	in.CharLimit = 256
	in.SetWidth(52)
	in.SetValue(facts.Title)
	in.Focus()
	return &publishDialog{f: f, facts: facts, title: in, draft: facts.Draft || facts.ToDraft, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *publishDialog) ID() string { return "publish" }

func (d *publishDialog) creates() bool { return d.facts.Act == string(publish.ActCreate) }

// choosing reports a create on a fork: the PR can open in two repositories.
func (d *publishDialog) choosing() bool {
	return d.creates() && len(d.facts.BaseRepos) > 1 && d.onRetarget != nil
}

// retarget reopens the confirm on the next repository the PR can open in.
func (d *publishDialog) retarget() (bool, tea.Cmd) {
	next := d.facts.BaseRepos[0]
	for i, r := range d.facts.BaseRepos {
		if r == d.facts.BaseRepo {
			next = d.facts.BaseRepos[(i+1)%len(d.facts.BaseRepos)]
		}
	}
	return true, d.onRetarget(next)
}

// HandleKey implements overlay.Dialog.
func (d *publishDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab":
		d.details = !d.details
		return false, nil
	case "ctrl+d":
		if d.creates() && !d.facts.DraftLocked {
			d.draft = !d.draft
		}
		return false, nil
	case "ctrl+k":
		d.hookOK = !d.hookOK
		return false, nil
	case "ctrl+t":
		if d.choosing() {
			return d.retarget()
		}
		return false, nil
	case "enter":
		if d.choosing() && d.facts.BaseRepo == "" {
			d.problem = "choose where the pull request opens: ctrl+t"
			return false, nil
		}
		if d.facts.Hook != "" && !d.hookOK {
			d.problem = "read the pre-push hook, then ctrl+k to run it"
			return false, nil
		}
		req := webapi.PublishRequest{Act: d.facts.Act, Fingerprint: d.facts.Fingerprint, Draft: d.draft, BaseRepo: d.baseRepo}
		if d.creates() {
			req.Title, req.Body = strings.TrimSpace(d.title.Value()), d.facts.Body
			if req.Title == "" {
				d.problem = "a pull request needs a title"
				return false, nil
			}
		}
		return true, d.onSubmit(req)
	}
	if d.creates() {
		d.problem = ""
		d.title, _ = d.title.Update(key)
	}
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *publishDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.creates() {
		d.title, _ = d.title.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *publishDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	head := map[string]string{"create": "open pull request", "push": "push to GitHub", "ready": "mark PR ready", "draft": "PR back to draft", "update": "update PR"}[d.facts.Act]
	b.WriteString(s.DialogTitle.Render(head+" · "+string(d.f.ID)) + "\n\n")
	if d.facts.Summary != "" {
		b.WriteString(sanitize(d.facts.Summary) + "\n")
	}
	if d.choosing() {
		// a fork's PR opens in the fork or in its parent, and the person
		// says which: once, remembered when the PR opens
		into := "nowhere yet"
		if d.facts.BaseRepo != "" {
			into = d.facts.BaseRepo
		}
		var others []string
		for _, r := range d.facts.BaseRepos {
			if r != d.facts.BaseRepo {
				others = append(others, r)
			}
		}
		b.WriteString("\nopens in " + sanitize(into) + s.Faint.Render("  ctrl+t for "+sanitize(strings.Join(others, " or "))) + "\n")
	}
	if d.creates() {
		b.WriteString("\n" + d.title.View() + "\n")
		// the description that will be sent, to be read here; it is
		// edited on GitHub or with `gummi pr update`
		if body := strings.TrimSpace(sanitize(d.facts.Body)); body != "" {
			lines := strings.Split(body, "\n")
			if len(lines) > 8 {
				lines = append(lines[:8], fmt.Sprintf("… %d more lines", len(lines)-8))
			}
			b.WriteString(s.Faint.Render(strings.Join(lines, "\n")) + "\n")
		}
		box := "[ ]"
		if d.draft {
			box = "[x]"
		}
		line := box + " open as draft"
		if d.facts.DraftLocked {
			line += s.Faint.Render(" — " + sanitize(d.facts.DraftWhy))
		} else {
			line += s.Faint.Render("  ctrl+d")
		}
		b.WriteString(line + "\n")
	}
	if d.facts.Hook != "" {
		box := "[ ]"
		if d.hookOK {
			box = "[x]"
		}
		b.WriteString("\n" + s.Warning.Render(box+" run "+sanitize(d.facts.Hook)+" with your credential") + s.Faint.Render("  ctrl+k") + "\n")
	}
	if d.details {
		b.WriteString("\n" + s.Faint.Render("push  "+sanitize(d.facts.Remote+" → "+d.facts.PushURL+" ("+d.facts.Push+")")) + "\n")
		if d.facts.BaseRepo != "" {
			b.WriteString(s.Faint.Render("head  "+sanitize(d.facts.Head)+" → "+sanitize(d.facts.BaseRepo+":"+d.facts.Base)) + "\n")
		}
		b.WriteString(s.Faint.Render("gh    "+sanitize(d.facts.GH)) + "\n")
		for _, c := range d.facts.Commands {
			b.WriteString(s.Faint.Render("  "+sanitize(c)) + "\n")
		}
	}
	if d.problem != "" {
		b.WriteString("\n" + s.Error.Render(d.problem) + "\n")
	}
	b.WriteString("\n" + s.Faint.Render("enter confirm · tab details · esc cancel"))
	return s.DialogFrame.Width(max(min(w-4, 76), 30)).Render(b.String())
}

// --- the web page's face ---

// webPublish is the board's publishing state as WebDocs captured it.
type webPublish struct {
	enabled, checked bool
	// watch is a card another gummi drives: this board only watches it
	watch bool
	why   *publish.Error
	gh    string
}

func (d *WebDocs) publishDoer() publishDoer {
	return publishDoer{
		f: d.f, gh: d.publish.gh, busy: d.busy,
		root: d.pool.Root(), drafts: d.ws.DraftsDir(), store: d.store, pool: d.pool, locks: d.locks,
	}
}

// errPublishOff is the web's answer on a board that does not publish.
func (d *WebDocs) publishOff() *webapi.PublishError {
	switch {
	case !d.publish.enabled:
		return &webapi.PublishError{Code: string(publish.CodeGHMissing), Text: "publishing is not available on this board"}
	case !d.publish.checked:
		return &webapi.PublishError{Code: string(publish.CodeGHMissing), Text: "still checking whether gh is set up", Fix: "try again in a moment"}
	case d.publish.why != nil:
		return publishErr(d.publish.why)
	case d.publish.watch:
		return &webapi.PublishError{Code: string(publish.CodeBusy), Text: "another gummi is driving this card", Fix: "publish it from there"}
	}
	return nil
}

// PublishFacts is GET /api/cards/{id}/publish?act=: the facts the publish
// dialog shows and the fingerprint its confirm sends back.
func (d *WebDocs) PublishFacts(ctx context.Context, act string, draft bool, baseRepo string) webapi.PublishFacts {
	a, ok := webPublishAct(act)
	if !ok {
		return webapi.PublishFacts{Act: act, Error: &webapi.PublishError{Code: string(publish.CodeFailed), Text: "unknown act " + act}}
	}
	if off := d.publishOff(); off != nil {
		return webapi.PublishFacts{Act: act, Error: off}
	}
	return d.publishDoer().prepare(ctx, a, draft, baseRepo)
}

// Publish is POST /api/cards/{id}/publish: the act the person confirmed.
// person is who confirmed it, as the thread will name them.
func (d *WebDocs) Publish(ctx context.Context, req webapi.PublishRequest, person string) webapi.PublishResult {
	if off := d.publishOff(); off != nil {
		return webapi.PublishResult{Act: req.Act, Error: off}
	}
	p := d.publishDoer()
	p.by = state.PersonActor(person)
	res, err := p.run(ctx, req)
	if err != nil {
		return webapi.PublishResult{Act: req.Act, Error: publishErr(err)}
	}
	return res
}

// PublishOffer is the PR tab's publish strip: which acts fit the card now.
// It reads GitHub for a linked PR's draft state and head; a card that has
// no branch to publish gets none.
func (d *WebDocs) PublishOffer(ctx context.Context) *webapi.PublishOffer {
	f := d.fresh(ctx)
	// where publishing is not set up, or the card is another gummi's,
	// the strip is simply absent
	if !d.publish.enabled || !d.publish.checked || d.publish.why != nil || d.publish.watch {
		return nil
	}
	if r := publish.Refusal(publish.Card{F: &f}); r != nil && r.Code == publish.CodeNoBranch {
		return nil
	}
	out := &webapi.PublishOffer{Available: true}
	doer := d.publishDoer()
	doer.f = f
	mgr, in, err := doer.input(ctx)
	if err != nil {
		return nil
	}
	if r := publish.Refusal(in.Card); r != nil {
		out.Why = r.Error()
		return out
	}
	tip, err := mgr.Head(ctx, &f)
	if err != nil {
		return nil
	}
	if f.PullRequest.Empty() {
		out.Acts = []string{string(publish.ActCreate)}
		return out
	}
	p, err := publish.ViewPR(ctx, publish.Env{GH: d.publish.gh}, f.PullRequest.Repo, f.PullRequest.Number)
	if err != nil || !p.Open() {
		return out
	}
	out.Draft = p.Draft
	if p.HeadSHA != tip {
		if n, err := mgr.Ahead(ctx, tip, p.HeadSHA); err == nil {
			out.Unpushed = n
		}
		out.Acts = append(out.Acts, string(publish.ActPush))
	}
	if fl := publish.Floor(&f, tip, in.OpenComments+in.OpenSpec); p.Draft && fl != nil {
		// a draft whose tip may not be offered as ready: the strip says
		// why instead of offering an act that would be refused
		out.Why = fl.Error()
	} else if p.Draft {
		out.Acts = append(out.Acts, string(publish.ActReady))
	} else {
		out.Acts = append(out.Acts, string(publish.ActDraft))
	}
	return out
}

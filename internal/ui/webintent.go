package ui

import (
	"context"
	"reflect"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/overlay"
	"github.com/morphis/gummi/internal/webapi"
)

// A web intent is a keypress the browser makes (DESIGN §20.1): it runs
// the code the key runs, inside Update, and then — because a page wants
// an answer, not a status bar — it follows what that code started until
// it has settled, and reports how it ended.
//
// "Settled" is the test harness's own notion (flow_test.go's pump): every
// finite command the intent started has run, and every message those
// commands produced has been handled, recursively. A subscription — the
// engine listener, a re-arming tick — never finishes and is not followed
// (subscription.go's registry is what says which is which).
//
// Where the TUI's flow stops in a dialog, the intent does not stop: the
// dialog is answered with what the request carried (webDialogs.go), by the
// dialog's own submit — the body its key handler runs. A dialog the
// request carried nothing for is closed, and the outcome names what it
// asked, so the page can ask the person and try again.

// Waits bound how long a request follows its intent. A flow that is still
// going when the wait ends keeps going; the request answers with the card
// as it stands, and what happens next reaches the page as events.
const (
	webWait = 30 * time.Second
	// webWaitDraft is for a landing with no message: the dialog waits on
	// a drafting pass, which is a model call.
	webWaitDraft = 150 * time.Second
)

// refuse is a board refusal (websurfaces.go's WebError) whose sentence is
// given whole rather than formatted.
func refuse(code WebErrorCode, text string) error {
	return &WebError{Code: code, Text: text}
}

// webInput is what an intent brings to a dialog its flow opens on the
// way: the value a person would have typed into it.
type webInput struct {
	// actor is who is acting, as the store records it
	// (state.PersonActor).
	actor string
	// card is the card the request is about (empty for a board-level
	// one). A dialog about another card — a stack replay or a goal tick the
	// flow reached downstream — is never this request's to answer.
	card    domain.FeatureID
	message string
	number  *int
	profile string
	repo    string
	// mode is the autopilot dialog's answer; empty takes the one the
	// dialog confirms.
	mode string
	// method is the landing method the request names; empty is squash.
	method domain.LandMethod
	// confirm is the confirmation tokens the request carries
	// (webapi.AnswerRequest.Confirm): each the yes to one question the
	// person was shown, spent by the dialog that asked it (takeConfirm).
	confirm string
	// land: the request is a landing (the menu's land or squash, or the
	// verify gate's landing answer). Only such a request answers the
	// landing message; a landing dialog some other request's flow reached
	// downstream — a goal that verified while an approval was being
	// followed — is a person's to confirm, never the request's.
	land bool
	// autopilot: the request is about the autopilot switch itself, so the
	// switch's dialog is its to answer even without a mode.
	autopilot bool
	// handoverAsked: the person already chose autopilot on a form that
	// said what it does (the new-card form's "Create & autopilot"), so the
	// switch's hand-over is not asked a second time.
	handoverAsked bool
}

// webOutcome is how an intent ended, in the TUI's own words.
type webOutcome struct {
	// refused is the error notice the flow ended on, verbatim.
	refused string
	// needs names a dialog's input the request did not carry, and
	// question what the dialog asked.
	needs    webapi.ActionNeeds
	question string
	// confirm is the token a confirmation's question was issued with.
	confirm string
	// draft is the landing message a landing stopped to have read.
	draft *string
	// restore is a composer line the board handed back unsent (the agent
	// was mid-turn).
	restore string
	// newCard is a line the flow wanted to start a new card with (the
	// re-entry's "not this card's work").
	newCard string
	// created is a card the intent minted.
	created domain.FeatureID
	// notices are what the status bar said along the way, in order.
	notices []string
	settled bool
}

// err turns an outcome that did not go through into the refusal the
// server sends; nil when it went through.
func (o webOutcome) err() error {
	switch {
	case o.restore != "":
		return &WebError{Code: WebConflict, Reason: webapi.ConflictBusy, Text: o.restore}
	case o.needs == webapi.ActionNeedsConfirm:
		// the same word the surfaces' confirmations answer with
		return &WebError{Code: WebConflict, Reason: string(webapi.ActionNeedsConfirm), Needs: string(o.needs), Text: o.question, Confirm: o.confirm}
	case o.needs != "":
		return &WebError{Code: WebConflict, Reason: webapi.ConflictNeeds, Needs: string(o.needs), Text: o.question, Draft: o.draft}
	case o.newCard != "":
		return &WebError{Code: WebConflict, Reason: webapi.ConflictNewCard, Text: o.newCard}
	case o.refused != "":
		return refuse(WebConflict, o.refused)
	}
	return nil
}

// webIntent is one intent in flight.
type webIntent struct {
	card domain.FeatureID
	in   webInput

	// Everything below but pending/done is touched on the Update
	// goroutine only.
	out      webOutcome
	detached bool
	// waiting are dialogs the intent opened and is still answering (a
	// commit message waiting on its draft).
	waiting []overlay.Dialog
	// resetComposer: the intent put a line in the card composer (the TUI
	// routes a line from there); it is cleared once the intent is done,
	// so a headless board's composer never holds a page's leftovers.
	resetComposer bool
	// images are the attachments a POST /api/cards/{id}/send request
	// resolved, for the turn its route ends up delivering (steer, consult
	// or a freeform card's turn) to pick up via intentImages — nil for
	// every TUI-driven intent, which never carries any.
	images []engine.AttachmentRef

	mu      sync.Mutex
	pending int
	done    chan struct{}
}

func newWebIntent(card domain.FeatureID, in webInput) *webIntent {
	if in.actor == "" {
		in.actor = state.ActorUser
	}
	in.card = card
	return &webIntent{card: card, in: in, pending: 1, done: make(chan struct{})}
}

func (t *webIntent) add(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending <= 0 {
		return
	}
	t.pending += n
	if t.pending <= 0 {
		close(t.done)
	}
}

// webTrackedMsg carries a message an intent's command produced back into
// Update, so it is handled on the intent's behalf.
type webTrackedMsg struct {
	intent *webIntent
	inner  tea.Msg
}

var cmdType = reflect.TypeFor[tea.Cmd]()

// wrap follows cmd: its message comes back as a webTrackedMsg, a batch or
// a sequence is followed member by member, and a subscription is left
// alone.
func (t *webIntent) wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil || isSubscription(cmd) {
		return cmd
	}
	t.add(1)
	return func() tea.Msg {
		msg := cmd()
		defer t.add(-1)
		switch v := msg.(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			out := make(tea.BatchMsg, 0, len(v))
			for _, c := range v {
				if w := t.wrap(c); w != nil {
					out = append(out, w)
				}
			}
			return out
		}
		// tea.Sequence's message is unexported; it is a []tea.Cmd all the
		// same, rebuilt here as its own type so the runtime still runs it
		// in order.
		if rv := reflect.ValueOf(msg); rv.Kind() == reflect.Slice && rv.Type().Elem() == cmdType {
			out := reflect.MakeSlice(rv.Type(), 0, rv.Len())
			for i := range rv.Len() {
				c, _ := rv.Index(i).Interface().(tea.Cmd)
				if w := t.wrap(c); w != nil {
					out = reflect.Append(out, reflect.ValueOf(w))
				}
			}
			return out.Interface()
		}
		t.add(1)
		return webTrackedMsg{intent: t, inner: msg}
	}
}

// updateTracked handles a message on its intent's behalf: as the card
// page of the intent's card, noting what the status bar says and
// answering what the handler opens.
func (m *Shell) updateTracked(tm webTrackedMsg) (tea.Model, tea.Cmd) {
	t := tm.intent
	defer t.add(-1)
	if t.detached {
		before := m.notice
		model, cmd := m.Update(tm.inner)
		m.toastDetached(tm.inner, before)
		return model, cmd
	}
	prev := m.intent
	m.intent = t
	defer func() { m.intent = prev }()
	// selected, not opened: what the handlers read about "the open page"
	// is the TUI's page (a narration pass, a thread reload), and the web
	// face has its own.
	leave := m.enterCard(t.card, false)
	defer leave()
	before, notice := m.Overlay.Len(), m.notice
	model, cmd := m.Update(tm.inner)
	if n, ok := tm.inner.(noticeMsg); ok && m.notice == n {
		// a notice this intent's own work sent is its news even when it
		// reads the same as the one already up: the same refusal twice
		// running was not recorded, and the second create that hit it
		// said only that no card was made
		notice = noticeMsg{}
	}
	t.noticed(m, notice)
	if created, ok := tm.inner.(cardCreatedMsg); ok {
		t.out.created = created.f.ID
	}
	return model, tea.Batch(t.wrap(cmd), t.wrap(t.answerDialogs(m, before)))
}

// noticed records what the status bar changed to, if it changed.
func (t *webIntent) noticed(m *Shell, before noticeMsg) {
	n := m.notice
	if n == before || n.text == "" {
		return
	}
	t.out.notices = append(t.out.notices, n.webText())
	if n.restore != "" {
		t.out.restore = n.restore
		return
	}
	if n.isErr && !n.aside {
		t.out.refused = n.webText()
	}
}

// enterCard makes id the selected card — and, with open, the open card
// page — for the length of a web intent or projection: the page the TUI's
// own handlers assume they were reached from. It returns what puts the
// board back.
func (m *Shell) enterCard(id domain.FeatureID, open bool) func() {
	if id == "" {
		return func() {}
	}
	i := m.rowIndex(id)
	if i < 0 {
		return func() {}
	}
	was, wasOpen := m.selectedID(), m.cardOpen
	m.sel, m.cardOpen = i, open
	return func() {
		m.cardOpen = wasOpen
		m.restoreSel(was)
	}
}

// answerDialogs answers what the intent's flow opened: the dialogs pushed
// above before, and any it is still waiting on. It returns the commands
// the answers started.
func (t *webIntent) answerDialogs(m *Shell, before int) tea.Cmd {
	var cands []overlay.Dialog
	for _, d := range t.waiting {
		if m.overlayIndex(d) >= 0 {
			cands = append(cands, d)
		}
	}
	t.waiting = nil
	for i := before; i < m.Overlay.Len(); i++ {
		cands = append(cands, m.Overlay.At(i))
	}
	var cmds []tea.Cmd
	for _, d := range cands {
		i := m.overlayIndex(d)
		if i < 0 {
			continue
		}
		wa, ok := d.(webAnswerable)
		if !ok {
			m.Overlay.RemoveAt(i)
			t.out.refused = "that opens " + d.ID() + ", which only the terminal can answer"
			continue
		}
		res := wa.webAnswer(m, &t.in)
		switch {
		case res.dismiss:
			// not this request's to answer: closed unanswered, and the
			// card stays where the flow left it for a person to decide
			m.Overlay.RemoveAt(i)
			t.out.notices = append(t.out.notices, d.ID()+" left for a person")
			continue
		case res.wait:
			t.waiting = append(t.waiting, d)
			continue
		case res.needs != "":
			t.out.needs, t.out.question, t.out.draft, t.out.confirm = res.needs, res.question, res.draft, res.token
		case res.newCard != "":
			t.out.newCard = res.newCard
		case res.refused != "":
			t.out.refused = res.refused
		default:
			// a caution raised before the dialog (a landing's provenance
			// warning) was the dialog's to show, and it has been answered
			t.out.refused = ""
		}
		if !res.keep {
			if j := m.overlayIndex(d); j >= 0 {
				m.Overlay.RemoveAt(j)
			}
		}
		cmds = append(cmds, res.cmd)
	}
	return tea.Batch(cmds...)
}

// overlayIndex finds a dialog on the stack by identity. Only a pointer
// dialog can be told apart this way (a value dialog holding a slice
// cannot even be compared), and every dialog an intent answers is one.
func (m *Shell) overlayIndex(d overlay.Dialog) int {
	if d == nil || reflect.ValueOf(d).Kind() != reflect.Pointer {
		return -1
	}
	for i := m.Overlay.Len() - 1; i >= 0; i-- {
		if o := m.Overlay.At(i); reflect.ValueOf(o).Kind() == reflect.Pointer && o == d {
			return i
		}
	}
	return -1
}

// sweepOrphanDialogs closes, on a headless board, every dialog no intent
// is answering. Nobody can see one, and one left open would sit on the
// stack answering nothing — or worse, be answered later by an intent it
// was never opened for.
func (m *Shell) sweepOrphanDialogs() {
	if !m.headless || m.intent != nil {
		return
	}
	for i := m.Overlay.Len() - 1; i >= 0; i-- {
		d := m.Overlay.At(i)
		if !m.webOwned(d) {
			m.Overlay.RemoveAt(i)
		}
	}
}

// webOwned reports whether a live intent is still answering d.
func (m *Shell) webOwned(d overlay.Dialog) bool {
	for t := range m.liveIntents {
		for _, w := range t.waiting {
			if reflect.ValueOf(d).Kind() == reflect.Pointer && w == d {
				return true
			}
		}
	}
	return false
}

// intent runs fn as a web intent on card id (empty for a board-level one)
// and follows it until it settles or wait passes. fn runs inside Update,
// with the card open; it returns the commands it started, or a refusal
// that stops the intent before anything ran.
func (b *Bridge) intent(ctx context.Context, id domain.FeatureID, in webInput, wait time.Duration,
	fn func(m *Shell, r featureRow) (tea.Cmd, error),
) (webOutcome, error) {
	t := newWebIntent(id, in)
	var refusal error
	err := b.Do(ctx, func(m *Shell) tea.Cmd {
		defer t.add(-1)
		var r featureRow
		if id != "" {
			row, ok := m.rowByID(id)
			if !ok {
				refusal = refuse(WebNotFound, "no card "+string(id)+" on this board")
				return nil
			}
			r = row
		}
		m.trackIntent(t)
		prev := m.intent
		m.intent = t
		defer func() { m.intent = prev }()
		leave := m.enterCard(id, true)
		defer leave()
		before, notice := m.Overlay.Len(), m.notice
		cmd, werr := fn(m, r)
		if werr != nil {
			refusal = werr
		}
		t.noticed(m, notice)
		return tea.Batch(t.wrap(cmd), t.wrap(t.answerDialogs(m, before)))
	})
	if err != nil {
		return webOutcome{}, err
	}
	if refusal != nil {
		b.release(t)
		return webOutcome{}, refusal
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	var out webOutcome
	if err := b.Do(context.WithoutCancel(ctx), func(m *Shell) tea.Cmd {
		out = m.finishIntent(t)
		return nil
	}); err != nil {
		return webOutcome{}, err
	}
	return out, nil
}

// release forgets an intent that never started anything.
func (b *Bridge) release(t *webIntent) {
	_ = b.Do(context.Background(), func(m *Shell) tea.Cmd { m.finishIntent(t); return nil })
}

// trackIntent registers a live intent, so the orphan sweep leaves the
// dialogs it is still answering alone.
func (m *Shell) trackIntent(t *webIntent) {
	if m.liveIntents == nil {
		m.liveIntents = map[*webIntent]bool{}
	}
	m.liveIntents[t] = true
}

// finishIntent detaches an intent: anything it started keeps running and
// is handled as though no intent had started it, and a dialog it was
// still waiting on is closed, since nobody is left to answer it.
func (m *Shell) finishIntent(t *webIntent) webOutcome {
	delete(m.liveIntents, t)
	t.detached = true
	for _, d := range t.waiting {
		if i := m.overlayIndex(d); i >= 0 {
			m.Overlay.RemoveAt(i)
			if t.out.refused == "" && t.out.needs == "" {
				t.out.refused = "gave up waiting on " + d.ID() + " — try again, or say what it should be"
			}
		}
	}
	t.waiting = nil
	if t.resetComposer && m.chip(t.card) == nil {
		m.threadInput.Reset()
	}
	select {
	case <-t.done:
		t.out.settled = true
	default:
	}
	return t.out
}

// intentImages returns and clears the current web intent's pending
// attachments (set by Bridge.Send before routing the line), so the turn
// its route ends up delivering — steer, consult or a freeform card's
// turn — picks them up exactly once. nil outside a web request, which is
// what every TUI-driven send sees.
func (m *Shell) intentImages() []engine.AttachmentRef {
	if m.intent == nil {
		return nil
	}
	images := m.intent.images
	m.intent.images = nil
	return images
}

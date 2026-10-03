package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// The Bridge runs the board's model without a screen, for `gummi web`
// (DESIGN §20.2). The Shell stays the one conductor — review loop,
// autopilot, the goal and stack ticks, landing, the needs-you queue — and
// a web request reaches it only through Do, which runs a function inside
// the Shell's own Update. That is the whole concurrency story: a read
// through Do sees the model between two messages, never halfway through
// one, and a write through Do runs the same code a keypress would, on the
// same goroutine.

// ErrBridgeStopped is Do's answer once the program has exited.
var ErrBridgeStopped = errors.New("the board is not running")

// headlessWidth and headlessHeight are the window a screenless Shell is
// told it has. Nothing is drawn, but layout code runs on every Update and
// a zero-sized window is a shape no terminal ever reports.
const (
	headlessWidth  = 160
	headlessHeight = 50
)

// Bridge hosts a Shell in a renderer-less tea.Program.
type Bridge struct {
	shell *Shell
	prog  *tea.Program

	stopped chan struct{}
	once    sync.Once
	err     error

	// turns holds one turn per card while anyone holds or waits for it:
	// writes to one card are made one after another (answerTurn), and a
	// card nobody is writing to has no entry.
	turnsMu sync.Mutex
	turns   map[string]*cardTurn
	// answered is the last web answer per card (webintents.go), under
	// turnsMu.
	answered map[string]webAnswerRecord
}

// cardTurn is one card's turn: a chan of one, and how many requests hold
// or wait for it, so the entry goes when the last one leaves.
type cardTurn struct {
	ch    chan struct{}
	users int
}

// answerTurn waits for the card's answer turn and returns what gives it
// back. Two people answering the same decision at once would otherwise
// both read it open, and the second could read it halfway through the
// first's crossing — the spec already stamped, the stage not yet moved —
// and be told the card "moved" when what happened is that someone answered
// it. In turn, the second answer reads the card after the first has
// settled, and is told who answered.
func (b *Bridge) answerTurn(ctx context.Context, id string) (func(), error) {
	b.turnsMu.Lock()
	if b.turns == nil {
		b.turns = map[string]*cardTurn{}
	}
	t := b.turns[id]
	if t == nil {
		t = &cardTurn{ch: make(chan struct{}, 1)}
		b.turns[id] = t
	}
	t.users++
	b.turnsMu.Unlock()
	leave := func() {
		b.turnsMu.Lock()
		if t.users--; t.users == 0 {
			delete(b.turns, id)
		}
		b.turnsMu.Unlock()
	}
	select {
	case t.ch <- struct{}{}:
		return func() { <-t.ch; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

// NewHeadless wraps shell in a program with no renderer, no input and no
// signal handler: the host owns the process's signals and its terminal,
// which is the server's log. Extra options are appended (tests pass a
// context). Call Run to start it.
func NewHeadless(shell *Shell, opts ...tea.ProgramOption) *Bridge {
	base := []tea.ProgramOption{
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(headlessWidth, headlessHeight),
	}
	shell.headless = true
	return &Bridge{
		shell:   shell,
		prog:    tea.NewProgram(shell, append(base, opts...)...),
		stopped: make(chan struct{}),
	}
}

// Run runs the program until Stop (or a tea.Quit from inside), and reports
// why it ended. It blocks; start it on its own goroutine.
func (b *Bridge) Run() error {
	_, err := b.prog.Run()
	b.once.Do(func() {
		b.err = err
		close(b.stopped)
	})
	return err
}

// Stop asks the program to quit and waits for it to have.
func (b *Bridge) Stop() {
	b.prog.Quit()
	<-b.stopped
}

// Done is closed once the program has exited.
func (b *Bridge) Done() <-chan struct{} { return b.stopped }

// Do runs fn inside the Shell's Update loop and returns once it has run.
// The command fn returns is dispatched exactly as one returned by Update
// would be. It must not call Do itself (that would wait on the loop it is
// running in).
//
// If ctx ends before the loop picks fn up, fn never runs and Do returns
// ctx's error; once fn has started, Do waits for it. A panic in fn is
// recovered and returned as an error rather than taking the board down
// with a web request.
func (b *Bridge) Do(ctx context.Context, fn func(m *Shell) tea.Cmd) error {
	req := &bridgeMsg{fn: fn, done: make(chan error, 1)}
	go b.prog.Send(req)
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		if req.state.CompareAndSwap(bridgePending, bridgeCancelled) {
			return ctx.Err()
		}
		return <-req.done
	case <-b.stopped:
		if req.state.CompareAndSwap(bridgePending, bridgeCancelled) {
			return ErrBridgeStopped
		}
		return <-req.done
	}
}

// bridgeMsg carries a Do call into Update.
type bridgeMsg struct {
	fn    func(m *Shell) tea.Cmd
	done  chan error
	state atomic.Int32
}

const (
	bridgePending int32 = iota
	bridgeRunning
	bridgeCancelled
)

// run executes the call on the Update goroutine, unless its caller has
// already given up on it.
func (r *bridgeMsg) run(m *Shell) (cmd tea.Cmd) {
	if !r.state.CompareAndSwap(bridgePending, bridgeRunning) {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			cmd = nil
			r.done <- fmt.Errorf("board: %v", p)
		}
	}()
	cmd = r.fn(m)
	// a request can move what a page shows without a message of its own
	// (a pause asked for, a chip withdrawn): report it like any message
	m.syncWebLive()
	r.done <- nil
	return cmd
}

// PutAttachment stores an uploaded image via the workspace's attachment
// store (internal/attachment), independent of any card — the store is a
// plain directory on disk, not board state, so this bypasses Do rather
// than block the Update loop for the write. Errors are the store's own
// (attachment.ErrNotImage, ErrTooLarge); internal/web maps them to their
// HTTP statuses directly.
func (b *Bridge) PutAttachment(r io.Reader, name string) (webapi.AttachmentRef, error) {
	if b.shell.engine == nil {
		return webapi.AttachmentRef{}, refuse(WebUnavailable, b.shell.noAgent(""))
	}
	ref, err := b.shell.engine.Attachments().Put(r, name)
	if err != nil {
		return webapi.AttachmentRef{}, err
	}
	return webapi.AttachmentRef{ID: ref.ID, Name: ref.Name, MediaType: ref.MediaType, Size: ref.Size}, nil
}

// AttachmentPath resolves id to the stored file's absolute path and media
// type, for GET /api/attachments/{id}. The store's ErrUnknown is returned
// unwrapped for internal/web to map to 404.
func (b *Bridge) AttachmentPath(id string) (path, mediaType string, err error) {
	if b.shell.engine == nil {
		return "", "", refuse(WebUnavailable, b.shell.noAgent(""))
	}
	store := b.shell.engine.Attachments()
	ref, err := store.Get(id)
	if err != nil {
		return "", "", err
	}
	path, err = store.Path(id)
	return path, ref.MediaType, err
}

// SetChangeHook installs the function the Shell calls after it handled
// anything that can change what a viewer sees. The hook is called on the
// Update goroutine (and, for the needs-you queue, from whichever goroutine
// changed it), so it must not block and must not call back into the
// Shell: hand the change to a channel or a hub and return. Set it before
// the program runs.
func (m *Shell) SetChangeHook(fn func(webapi.Change)) {
	m.changeHook = fn
	if fn == nil {
		m.inbox.onChange = nil
		return
	}
	m.inbox.onChange = func(id domain.FeatureID) {
		fn(webapi.Change{Kind: webapi.ChangeBoard})
		fn(webapi.Change{Kind: webapi.ChangeCard, ID: string(id)})
	}
}

// EmitChange reports a change to the hook, if one is installed. Web
// intents that move something the messages below do not cover call it.
func (m *Shell) EmitChange(c webapi.Change) {
	if m.changeHook != nil {
		m.changeHook(c)
	}
}

// emitChanges maps a message Update just handled to what it may have
// changed. It is a whitelist on purpose: the spinner, the polls that found
// nothing and every keystroke-only message change nothing a page shows,
// and a hook fired on each of them would have every open page refetching
// the board several times a second.
func (m *Shell) emitChanges(msg tea.Msg) {
	if m.changeHook == nil {
		return
	}
	m.syncWebIngest(msg)
	// whatever the message was, a card whose busy word, session state or
	// chip moved is reported (freshness.go): the whitelist below covers
	// what a message says, this covers what it did
	defer m.syncWebLive()
	switch msg := msg.(type) {
	case rowsMsg:
		m.emitRowChanges(msg)
	case openDecisionsMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	case sentBackMsg:
		m.emitChanges(msg.notice)
	case landConflictMsg:
		m.emitChanges(msg.notice)
	case noticeMsg:
		if msg.text != "" {
			m.EmitChange(webapi.Change{Kind: webapi.ChangeToast, ID: string(msg.id), Text: msg.webText(), Err: msg.isErr})
		}
	case prPullDoneMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeToast, ID: string(msg.f.ID), Text: msg.notice.text, Err: msg.notice.isErr})
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: string(msg.f.ID)})
	case cardEventsMsg:
		if msg.err == nil {
			m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: string(msg.id)})
		}
	case engineClosedMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	case boardOpenedMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeAgent})
	case pausedMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: string(msg.id)})
		m.emitChanges(msg.inner)
	case autopilotSettledMsg:
		m.emitChanges(msg.inner)
	case todaySpentMsg:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	case blockersMsg:
		// the decision's answers read the counts (a send-back carrying
		// the diff comments), and so does the board's needs line
		m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: string(msg.id)})
	case engineEventMsg:
		m.emitEngineChange(msg.ev)
	}
}

// emitEngineChange maps one engine event. A streaming update moves only
// the card's live block; every other kind can move the card's head, its
// decision and the board row too.
func (m *Shell) emitEngineChange(ev engine.Event) {
	id := string(ev.Feature)
	switch ev.Kind {
	case engine.EventBoard:
		m.EmitChange(webapi.Change{Kind: webapi.ChangeAgent})
		return
	case engine.EventUpdated:
		if id == "" {
			return
		}
		m.EmitChange(webapi.Change{Kind: webapi.ChangeLive, ID: id})
		// A freeform turn ends with nothing but an update (its session
		// clears busy and says so), and so does a live stage conversation's
		// reply. What the card offers turns on that flag — "stop this turn"
		// or the review answers, what enter would do — so the update that
		// flips it moves the card, not only its live block.
		// the open watch is taken alongside the busy flag: a turn that
		// starts and ends a watch can be read after it is already over, so
		// the busy flag alone may never flip where the row's word changes
		busy, watching := m.webCardBusy(ev.Feature), m.webCardWatching(ev.Feature)
		was, seen := m.webBusy[ev.Feature]
		wasWatching := m.webWatching[ev.Feature]
		if !seen || was != busy || wasWatching != watching {
			m.setWebState(ev.Feature, busy, watching)
			m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
			m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: id})
		}
		return
	}
	m.EmitChange(webapi.Change{Kind: webapi.ChangeBoard})
	if id != "" {
		// every other kind moves the card anyway; the busy flag is taken
		// again here too, or a turn that ended in exhaustion or an error
		// would leave it standing and the next turn's start would look
		// like no change at all
		m.setWebState(ev.Feature, m.webCardBusy(ev.Feature), m.webCardWatching(ev.Feature))
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: id})
		m.EmitChange(webapi.Change{Kind: webapi.ChangeLive, ID: id})
	}
}

// setWebState records what the web last saw of card id: busy and watching.
func (m *Shell) setWebState(id domain.FeatureID, busy, watching bool) {
	if m.webBusy == nil {
		m.webBusy = map[domain.FeatureID]bool{}
	}
	if m.webWatching == nil {
		m.webWatching = map[domain.FeatureID]bool{}
	}
	m.webBusy[id] = busy
	m.webWatching[id] = watching
}

// webCardWatching is whether card id has a watch open, gummi's or the
// backend's own, whether or not a turn is running (freeform cards only).
func (m *Shell) webCardWatching(id domain.FeatureID) bool {
	if m.engine == nil {
		return false
	}
	ff := m.engine.Freeform(id)
	return ff != nil && ff.Watching()
}

// webCardBusy is whether card id has an agent mid-turn: its freeform
// session, or its stage session.
func (m *Shell) webCardBusy(id domain.FeatureID) bool {
	if m.engine != nil {
		if ff := m.engine.Freeform(id); ff != nil {
			return ff.Busy()
		}
	}
	if sess := m.sessionFor(id); sess != nil {
		return sess.Busy()
	}
	return false
}

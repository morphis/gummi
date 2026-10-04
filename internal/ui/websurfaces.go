package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// The board's surfaces beyond a single card — goals, stacks, spec ingest,
// bug import — for the web face (DESIGN §20.1). Each
// is a pair: a projection read inside the Shell's loop, and an intent that
// runs the same function the TUI's key or dialog runs, with the dialog's
// answer passed in rather than asked for.
//
// Most of those functions return a command that does the work off the
// loop and reports it as a notice. A web request wants that notice as its
// answer, so Await runs the command on the request's goroutine instead of
// the program's and then hands the message it produced back to the loop —
// exactly the message the program would have delivered, so the board
// reloads and toasts as it does after a keypress.

// WebErrorCode classes a refusal the way the HTTP answer will.
type WebErrorCode int

// The refusal classes.
const (
	// WebBadRequest: the request itself is incomplete or malformed.
	WebBadRequest WebErrorCode = iota + 1
	// WebNotFound: it names something the board does not have.
	WebNotFound
	// WebConflict: the board cannot do it in the state it is in.
	WebConflict
	// WebUnavailable: the board has no engine (no agent configured) or
	// nothing to do it with.
	WebUnavailable
)

// WebError is an intent the board refused before running anything.
type WebError struct {
	Code WebErrorCode
	Text string
	// Reason is a machine word for a refusal the page answers specially:
	// "busy" (a line handed back), "confirm" (ask, then send Confirm),
	// and on a card's answer, send and actions also "answered", "moved",
	// "needs" and "newcard" (webapi's Conflict* words).
	Reason string
	// By and Receipt name who answered first and what they said, on an
	// "answered"; Needs is the input a "needs" (or "confirm") asks for.
	By, Receipt, Needs string
	// Confirm is a "confirm"'s token: the yes to the question in Text,
	// and to nothing else (webConfirmToken).
	Confirm string
	// Draft is the landing message a landing stopped to have read, on a
	// "needs" message; nil when the stop is not a landing's.
	Draft *string
}

func (e *WebError) Error() string { return e.Text }

func webErr(code WebErrorCode, format string, args ...any) error {
	return &WebError{Code: code, Text: fmt.Sprintf(format, args...)}
}

// WebOutcome is what an intent's command reported: the sentence the
// TUI's status band would have shown, and whether it reports a failure.
type WebOutcome struct {
	Text string
	Err  bool
	// ID names what the intent made or touched, when it made something.
	ID string
}

// Await runs fn inside the Shell's loop, then runs the command it returned
// on the caller's goroutine and hands the resulting message back to the
// loop. An error from fn is a refusal: nothing ran.
func (b *Bridge) Await(ctx context.Context, fn func(m *Shell) (tea.Cmd, error)) (WebOutcome, error) {
	var (
		cmd  tea.Cmd
		ferr error
	)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { cmd, ferr = fn(m); return nil }); err != nil {
		return WebOutcome{}, err
	}
	if ferr != nil {
		return WebOutcome{}, ferr
	}
	return b.finish(cmd), nil
}

// finish runs cmd here and delivers its message to the loop.
func (b *Bridge) finish(cmd tea.Cmd) WebOutcome {
	if cmd == nil {
		return WebOutcome{}
	}
	msg := cmd()
	out := outcomeOf(msg)
	b.deliver(msg)
	return out
}

// deliver hands msg to the loop as if a command had returned it.
func (b *Bridge) deliver(msg tea.Msg) {
	if msg == nil {
		return
	}
	select {
	case <-b.stopped:
	default:
		b.prog.Send(msg)
	}
}

// outcomeOf reads what a command's message says happened.
func outcomeOf(msg tea.Msg) WebOutcome {
	switch msg := msg.(type) {
	case noticeMsg:
		return WebOutcome{Text: msg.webText(), Err: msg.isErr}
	}
	return WebOutcome{}
}

// webLoad runs prep inside the loop, where it may read the model and pick
// up the handles it needs, and the IO it returns outside it.
func webLoad[T any](ctx context.Context, b *Bridge, prep func(m *Shell) (func(context.Context) (T, error), error)) (T, error) {
	var (
		zero T
		io   func(context.Context) (T, error)
		perr error
	)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { io, perr = prep(m); return nil }); err != nil {
		return zero, err
	}
	if perr != nil {
		return zero, perr
	}
	return io(ctx)
}

// webID reads a card id the way the CLI does: case does not matter.
func webID(id string) domain.FeatureID {
	return domain.FeatureID(strings.ToUpper(strings.TrimSpace(id)))
}

// webRowFor returns the loaded row for id, or a not-found refusal.
func (m *Shell) webRowFor(id string) (featureRow, error) {
	r, ok := m.rowByID(webID(id))
	if !ok {
		return featureRow{}, webErr(WebNotFound, "no card %s on this board", id)
	}
	return r, nil
}

// webWorkspaceFile resolves a path a page named — relative to the
// workspace root, or absolute — to a regular file inside the workspace.
// A page is a person at the board, but it is a person on another device,
// and "a file in the repo" is what the surfaces that read one promise.
func (m *Shell) webWorkspaceFile(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", webErr(WebBadRequest, "name a file")
	}
	root := m.ws.Root
	if root == "" {
		return "", webErr(WebUnavailable, "this board has no workspace")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", webErr(WebUnavailable, "reading the workspace: %v", err)
	}
	// One answer for every refusal — missing, outside, a directory, gummi's
	// own state — and never the resolved path: a page must not learn what
	// exists on this machine by asking about it.
	refuse := webErr(WebBadRequest, "%q is not a readable file in this workspace", p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", refuse
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refuse
	}
	// .gummi holds the board's machinery — the web server's admin token,
	// the paired devices, the push key, the tailnet identity — and .git
	// the repository's own (its config, credentials a helper left there):
	// none of it a document to hand a model.
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(part, ".gummi") || strings.EqualFold(part, ".git") {
			return "", refuse
		}
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return "", refuse
	}
	return real, nil
}

// IsWebError reports whether err is a board refusal, and which.
func IsWebError(err error) (*WebError, bool) {
	var we *WebError
	if errors.As(err, &we) {
		return we, true
	}
	return nil, false
}

// Reload asks the board to read its rows again, as r does: for a host
// whose store moved behind the board's back.
func (b *Bridge) Reload(ctx context.Context) error {
	return b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows })
}

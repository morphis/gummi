package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/childproc"
	"github.com/morphis/gummi/internal/domain"
)

// A watch is gummi's own Monitor, for the backends that have none (every
// one but Claude Code, agent.Capabilities.NativeWatch): the agent names a
// command, gummi runs it in the card's worktree, and what it prints comes
// back to the agent as a turn of its own. It lives in the freeform
// session, not in the backend, so it outlasts the backend's idle close —
// output that arrives after it is what respawns one.
//
// Every delivery is a turn and every turn costs, so output is batched (a
// burst becomes one turn once it has gone quiet), deliveries to a session
// are spaced, and a watch has a deadline. The tool's description tells the
// agent to filter its command down to the lines worth waking for.

const (
	watchToolName   = "watch"
	unwatchToolName = "unwatch"

	// watchQuiet is how long output must pause before a burst is sent;
	// watchMaxHold caps how long a steady stream is held back for one.
	watchQuiet   = 2 * time.Second
	watchMaxHold = 20 * time.Second
	// watchSpacing is the least time between two deliveries to one
	// session, so a chatty watch cannot spend the envelope a turn a second.
	watchSpacing = 30 * time.Second
	// watchDefault/watchMax bound a watch's life in minutes.
	watchDefault = 60
	watchMax     = int(freeformWatchMax / time.Minute)
	// watchLines/watchLineLen cap what one delivery carries.
	watchLines   = 60
	watchLineLen = 400
)

func watchTool() agent.ToolDef {
	return agent.ToolDef{
		Name: watchToolName,
		Description: "Run a shell command in the background, in the worktree, and be told " +
			"what it prints. Returns at once; each burst of output, and the command's exit, " +
			"reaches you later as a message of its own, even after this turn has ended. " +
			"Every such message is a turn and costs one, so print only what is worth " +
			"waking for — e.g. `tail -f build.log | grep --line-buffered -E 'FAIL|panic'` " +
			"or `until curl -sf localhost:8080/health; do sleep 5; done; echo up`. " +
			"Stop it with unwatch when you no longer need it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to run (sh -c), from the worktree root.",
				},
				"timeout_minutes": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Stop it after this many minutes (default %d, at most %d).", watchDefault, watchMax),
				},
			},
			"required": []any{"command"},
		},
	}
}

func unwatchTool() agent.ToolDef {
	return agent.ToolDef{
		Name:        unwatchToolName,
		Description: "Stop a watch started with the watch tool, by the id it returned.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The watch's id, e.g. w1."},
			},
			"required": []any{"id"},
		},
	}
}

// freeformWatchHint is added to the hints of a session offered the tool.
const freeformWatchHint = `You also have gummi's watch tool (and unwatch): use it where you would
otherwise poll — a build, a test run, a server coming up, a log to keep an
eye on. It returns at once and tells you later, in a message of its own,
what the command printed and when it exited. Filter the command so it
prints only what is worth a turn.`

// freeformWatch is one running watch.
type freeformWatch struct {
	id      string
	command string
	cancel  context.CancelFunc

	mu      sync.Mutex
	pending []string
	dropped int
	first   time.Time // when the oldest pending line arrived
	last    time.Time // when the newest did
	exit    string    // set once the command has ended
}

// WatchInfo is a running watch as a face shows it.
type WatchInfo struct {
	ID      string
	Command string
}

// Watches is the session's running watches, oldest first.
func (ff *FreeformSession) Watches() []WatchInfo {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	out := make([]WatchInfo, 0, len(ff.watches))
	for _, w := range ff.watches {
		out = append(out, WatchInfo{ID: w.id, Command: w.command})
	}
	return out
}

// handleWatchTool answers watch and unwatch for a freeform session; a
// stage session has neither.
func (e *Engine) handleWatchTool(s *Session, tc *agent.ToolCall) {
	ff := e.Freeform(s.Feature.ID)
	if ff == nil || s.Feature.Stage != domain.StageOpen {
		e.resolveNow(s, tc.ID, tc.Name+" is only available in a freeform session")
		return
	}
	if tc.Name == unwatchToolName {
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(tc.Args, &a)
		if ff.stopWatch(a.ID) {
			e.resolveNow(s, tc.ID, "stopped "+a.ID)
		} else {
			e.resolveNow(s, tc.ID, "no running watch "+strconv.Quote(a.ID))
		}
		return
	}
	var a struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_minutes"`
	}
	if err := json.Unmarshal(tc.Args, &a); err != nil || strings.TrimSpace(a.Command) == "" {
		e.resolveNow(s, tc.ID, "watch needs a command")
		return
	}
	id, err := ff.startWatch(a.Command, a.Timeout)
	if err != nil {
		e.resolveNow(s, tc.ID, "watch did not start: "+err.Error())
		return
	}
	e.resolveNow(s, tc.ID, fmt.Sprintf("watching as %s — its output and exit will reach you as messages", id))
}

// startWatch runs command in the card's worktree until it exits, its
// deadline passes, or the session stops it.
func (ff *FreeformSession) startWatch(command string, minutes int) (string, error) {
	dir := ff.WorkDir()
	if dir == "" {
		return "", errors.New("the session has no worktree yet")
	}
	if minutes <= 0 {
		minutes = watchDefault
	}
	minutes = min(minutes, watchMax)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(minutes)*time.Minute)
	cmd := exec.CommandContext(ctx, "sh", "-c", command) // #nosec G204 -- the session's own agent runs commands in its worktree anyway
	cmd.Dir = dir
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	// its own process group, killed whole: a pipeline's children would
	// otherwise outlive sh and hold the pipe open (verify.go has the same)
	childproc.Group(cmd)
	if err := childproc.Start(cmd); err != nil {
		cancel()
		return "", err
	}

	ff.mu.Lock()
	if ff.watchStopped {
		ff.mu.Unlock()
		cancel()
		_ = cmd.Wait()
		return "", errors.New("the session is closing")
	}
	ff.watchSeq++
	w := &freeformWatch{id: "w" + strconv.Itoa(ff.watchSeq), command: command, cancel: cancel}
	ff.watches = append(ff.watches, w)
	ff.watchWG.Add(2)
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})

	go func() {
		defer ff.watchWG.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			w.add(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	go func() {
		defer ff.watchWG.Done()
		err := cmd.Wait()
		_ = pw.Close()
		w.finish(watchExit(ctx, err, minutes))
		ff.dropWatch(w)
		cancel()
	}()
	ff.kickWatchFlusher()
	return w.id, nil
}

func watchExit(ctx context.Context, err error, minutes int) string {
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("stopped after its %d-minute deadline", minutes)
	case errors.Is(ctx.Err(), context.Canceled):
		return "stopped"
	case err == nil:
		return "exited 0"
	case errors.As(err, &exit):
		return "exited " + strconv.Itoa(exit.ExitCode())
	default:
		return "ended: " + err.Error()
	}
}

func (w *freeformWatch) add(line string) {
	if len(line) > watchLineLen {
		line = line[:watchLineLen] + "…"
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		w.first = now
	}
	w.last = now
	if len(w.pending) >= watchLines {
		w.pending = w.pending[1:] // keep the newest: the latest state is what matters
		w.dropped++
	}
	w.pending = append(w.pending, line)
}

func (w *freeformWatch) finish(exit string) {
	w.mu.Lock()
	w.exit = exit
	w.mu.Unlock()
}

// take returns what is ready to send — a burst gone quiet, one held too
// long, or the watch's end — and clears it. done reports the watch ended.
func (w *freeformWatch) take(now time.Time) (lines []string, dropped int, exit string, ready bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.exit == "" {
		if len(w.pending) == 0 || (now.Sub(w.last) < watchQuiet && now.Sub(w.first) < watchMaxHold) {
			return nil, 0, "", false
		}
	}
	lines, dropped, exit = w.pending, w.dropped, w.exit
	w.pending, w.dropped = nil, 0
	return lines, dropped, exit, true
}

func (ff *FreeformSession) dropWatch(w *freeformWatch) {
	ff.mu.Lock()
	for i, x := range ff.watches {
		if x == w {
			ff.watches = append(ff.watches[:i], ff.watches[i+1:]...)
			break
		}
	}
	// an ended watch still has its exit to report
	ff.watchEnded = append(ff.watchEnded, w)
	ff.mu.Unlock()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
}

func (ff *FreeformSession) stopWatch(id string) bool {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	for _, w := range ff.watches {
		if w.id == id {
			w.cancel()
			return true
		}
	}
	return false
}

// stopWatches ends every watch and waits for them, for a session closing.
// Nothing they had left to say is delivered.
func (ff *FreeformSession) stopWatches() {
	ff.mu.Lock()
	ff.watchStopped = true
	for _, w := range ff.watches {
		w.cancel()
	}
	stop := ff.watchStop
	ff.watchStop = nil
	ff.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	ff.watchWG.Wait()
}

// kickWatchFlusher starts the one goroutine that turns watch output into
// turns, if it is not running. It ends once no watch is left to report.
func (ff *FreeformSession) kickWatchFlusher() {
	ff.mu.Lock()
	if ff.watchStop != nil || ff.watchStopped {
		ff.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	ff.watchStop = stop
	ff.watchWG.Add(1)
	ff.mu.Unlock()
	go func() {
		defer ff.watchWG.Done()
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				if !ff.flushWatches(now) {
					ff.mu.Lock()
					if ff.watchStop == stop {
						ff.watchStop = nil
					}
					ff.mu.Unlock()
					return
				}
			}
		}
	}()
}

// flushWatches sends what the watches have ready as one turn, when the
// session can take one. It reports whether any watch is left to report.
func (ff *FreeformSession) flushWatches(now time.Time) bool {
	ff.mu.Lock()
	all := append(append([]*freeformWatch(nil), ff.watches...), ff.watchEnded...)
	spaced := now.Sub(ff.watchSent) >= watchSpacing
	sess := ff.sess
	ff.mu.Unlock()
	if len(all) == 0 {
		return false
	}
	// not mid-turn, not over an open question, and not ahead of what the
	// person queued: theirs goes first, this waits for the turn after
	if !spaced || (sess != nil && (sess.Busy() || sess.Snapshot().PendingAsk != nil)) || len(ff.Queued()) > 0 {
		return true
	}
	var body, notes []string
	var ended []*freeformWatch
	for _, w := range all {
		lines, dropped, exit, ready := w.take(now)
		if !ready {
			continue
		}
		head := fmt.Sprintf("[gummi watch %s: `%s`]", w.id, w.command)
		note := "watch " + w.id
		if len(lines) > 0 {
			head += fmt.Sprintf(" printed %d line(s)", len(lines)+dropped)
			note += fmt.Sprintf(" · %d line(s)", len(lines)+dropped)
		}
		if exit != "" {
			head += " and " + exit
			if len(lines) == 0 {
				head = fmt.Sprintf("[gummi watch %s: `%s`] %s", w.id, w.command, exit)
			}
			note += " · " + exit
			ended = append(ended, w)
		}
		part := head
		if dropped > 0 {
			part += fmt.Sprintf("\n(%d earlier line(s) dropped)", dropped)
		}
		if len(lines) > 0 {
			part += "\n" + strings.Join(lines, "\n")
		}
		body = append(body, part)
		notes = append(notes, note)
	}
	if len(ended) > 0 {
		ff.mu.Lock()
		keep := ff.watchEnded[:0]
		for _, w := range ff.watchEnded {
			gone := false
			for _, e := range ended {
				gone = gone || e == w
			}
			if !gone {
				keep = append(keep, w)
			}
		}
		ff.watchEnded = keep
		ff.mu.Unlock()
	}
	if len(body) > 0 {
		ff.mu.Lock()
		ff.watchSent = now
		ff.mu.Unlock()
		// an error is the session's own and already on it
		_ = ff.sendNote(context.Background(), strings.Join(body, "\n\n"), strings.Join(notes, "; "))
	}
	ff.mu.Lock()
	left := len(ff.watches)+len(ff.watchEnded) > 0
	ff.mu.Unlock()
	return left
}

// sendNote is a turn gummi starts on the session's behalf: the agent hears
// text, and the transcript shows note as an activity line rather than as
// something the person said. A backend that idled out is respawned for it.
func (ff *FreeformSession) sendNote(ctx context.Context, text, note string) error {
	sess, err := ff.ensureBackend(ctx)
	if err != nil {
		return err
	}
	a := sess.agent()
	if a == nil || sess.Busy() {
		return nil
	}
	sess.appendActivity(note)
	ff.engine.persist(sess)
	sess.setBusy(true)
	ff.armIdleTimer()
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	if err := a.Send(ctx, text); err != nil {
		if errors.Is(err, agent.ErrBusy) {
			sess.setBusy(false)
			return nil
		}
		sess.setError(err)
		ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventError, Err: err})
		return err
	}
	return nil
}

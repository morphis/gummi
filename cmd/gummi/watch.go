package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// runWatch implements `gummi watch <id|ref> [--json] [--wait]`: it follows
// the live agent stream of a card another gummi process is driving.
//
// `status` answers "where is this card" by polling the store, which the
// driver updates once per turn. This answers "what is it doing right now"
// — the assistant text, the tool calls, the state changes — off the live
// file the driving process mirrors (internal/livelog). It drives nothing,
// takes no lock, and can run against a card the TUI or a headless run
// already owns, which is the whole point.
func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	jsonOut, wait, once := registerWatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi watch <id|ref> [--json] [--wait] [--once]")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, _ *worktree.Pool, ws state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		path := ws.LiveFile(f.ID)
		st, err := livelog.Stat(path)
		switch {
		case errors.Is(err, livelog.ErrNoLiveFile) && !*wait:
			// Deterministic failure over a silent wait: nothing is
			// streaming, and a watcher parked forever on a run that was
			// never started reads exactly like a hang.
			return fmt.Errorf("%s has no live stream at %s — start it (`gummi run`/`resume`, or the board), or pass --wait to block until one appears", f.ID, path)
		case err != nil && !errors.Is(err, livelog.ErrNoLiveFile):
			return fmt.Errorf("reading %s: %w", path, err)
		}

		// Ctrl-C ends the follow cleanly: this command owns nothing, so
		// there is nothing to unwind but the tail itself.
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		if !*jsonOut {
			renderWatchHeader(os.Stdout, string(f.ID), f.Title, st, err == nil)
		}
		return followLive(ctx, os.Stdout, path, *jsonOut, *once)
	})
}

// registerWatchFlags binds `gummi watch`'s flags onto fs, the single
// registration site the skill's grammar generator also enumerates.
func registerWatchFlags(fs *flag.FlagSet) (jsonOut, wait, once *bool) {
	jsonOut = fs.Bool("json", false, "emit the raw record stream as NDJSON instead of the rendered transcript")
	wait = fs.Bool("wait", false, "block until the card has a live stream instead of failing when none exists")
	once = fs.Bool("once", false, "exit when the current session ends instead of following the card's next one")
	return jsonOut, wait, once
}

// renderWatchHeader prints what is known before the stream starts: who
// owns the session, and whether that owner is still alive. A stopped or
// abandoned file still renders — reading how a stage ended is worth as
// much as watching one run.
func renderWatchHeader(w io.Writer, id, title string, st livelog.Status, found bool) {
	fmt.Fprintf(w, "%s  %s\n", id, title)
	if !found {
		fmt.Fprintln(w, "  waiting for a live stream…")
		return
	}
	owner := fmt.Sprintf("pid %d", st.PID)
	switch {
	case st.Stopped:
		owner += " (session ended)"
	case !state.ProcessAlive(st.PID):
		owner += " (gone — the stream stops here)"
	}
	fmt.Fprintf(w, "  Stage:    %s\n", st.Stage)
	if st.Role != "" {
		fmt.Fprintf(w, "  Role:     %s\n", st.Role)
	}
	if st.Agent != "" {
		fmt.Fprintf(w, "  Agent:    %s %s\n", st.Agent, st.Model)
	}
	fmt.Fprintf(w, "  Driven by: %s\n\n", owner)
}

// followLive tails path until ctx ends, writing either the raw records
// (--json, for a calling agent) or a rendered transcript.
//
// A session ending is not the card ending — a resume takes the same file
// over, and the follower reports that as a reset — so by default the tail
// keeps running until interrupted. --once (stopAtEnd) is the scriptable
// shape: return as soon as the session being watched finishes.
func followLive(ctx context.Context, w io.Writer, path string, jsonOut, stopAtEnd bool) error {
	enc := json.NewEncoder(w)
	var r watchRender
	for rec := range livelog.Follow(ctx, path, 0) {
		if jsonOut {
			if err := enc.Encode(rec); err != nil {
				return err
			}
		} else if _, err := io.WriteString(w, r.line(rec)); err != nil {
			return err
		}
		if stopAtEnd && rec.Kind == livelog.KindStopped {
			return nil
		}
	}
	return nil
}

// watchRender turns records into terminal lines. It tracks whether it is
// mid-assistant-message so streamed deltas print as they arrive — the
// live feel — without the finalizing record printing the same prose a
// second time.
type watchRender struct {
	streaming bool
}

func (r *watchRender) line(rec livelog.Record) string {
	switch rec.Kind {
	case livelog.KindSession:
		r.streaming = false
		return fmt.Sprintf("\n── %s · %s · %s (pid %d)\n", rec.Feature, rec.Stage, rec.Role, rec.PID)
	case livelog.KindReset:
		r.streaming = false
		return "\n── a new session took this card over ──\n"
	case livelog.KindUser:
		return r.close() + "\n» " + rec.Text + "\n"
	case livelog.KindSystem:
		return r.close() + "\n· " + rec.Text + "\n"
	case livelog.KindDelta:
		if !r.streaming {
			r.streaming = true
			return "\n" + rec.Text
		}
		return rec.Text
	case livelog.KindMessage:
		// deltas already printed this prose as it streamed; only a
		// message that arrived whole needs printing here.
		if r.streaming {
			r.streaming = false
			return "\n"
		}
		if rec.Text == "" {
			return ""
		}
		return "\n" + rec.Text + "\n"
	case livelog.KindEdit:
		return "" // an in-place rewrite of prose already shown
	case livelog.KindTool:
		return r.close() + "  → " + rec.Text + "\n"
	case livelog.KindResult:
		if rec.OK {
			return ""
		}
		return r.close() + "  ✗ " + firstLine(rec.Output) + "\n"
	case livelog.KindState:
		return r.close() + "  [" + rec.State + "]\n"
	case livelog.KindSpend:
		return ""
	case livelog.KindBusy:
		return ""
	case livelog.KindAsk:
		if rec.Text == "" {
			return ""
		}
		return r.close() + "\n? " + rec.Text + "\n  (answer it where the run is owned — a watcher cannot)\n"
	case livelog.KindDropped:
		return r.close() + fmt.Sprintf("  … %d record(s) dropped by a busy writer\n", rec.Count)
	case livelog.KindStopped:
		out := r.close() + "\n── session ended"
		if rec.Err != "" {
			out += ": " + rec.Err
		}
		return out + " ──\n"
	}
	return ""
}

// close ends an in-flight streamed message so the next line starts fresh.
func (r *watchRender) close() string {
	if !r.streaming {
		return ""
	}
	r.streaming = false
	return "\n"
}

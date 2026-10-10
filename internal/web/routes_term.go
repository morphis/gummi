package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/morphis/gummi/internal/term"
	"github.com/morphis/gummi/internal/webapi"
)

// A shell in a card's worktree, for the page's Terminal tab. It exists
// only on a server started with --terminal (Options.Terminal): everything
// else here is what a person at the TUI can do, and this is whatever the
// person running the server can do.
//
// The page talks to it over a WebSocket. Binary frames are the terminal's
// bytes, both ways. A text frame from the page is a termResize; one from
// the server is a termExit, sent once, before it closes the socket.
//
// The shell is the card's, not the socket's: it keeps running when the
// page goes away, and the next socket is replayed the tail of its output
// (internal/term). It ends when the person exits it, when its worktree
// goes, after an hour with no page attached, and with the server.

const (
	// termWriteTimeout bounds one write to a page that has stopped reading.
	termWriteTimeout = 10 * time.Second
	// termMaxFrame bounds one frame from the page: a paste.
	termMaxFrame = 1 << 20
)

// termRecheck is how often an open socket is pinged and its device checked
// to be paired still. A variable so a test can shorten it.
var termRecheck = 30 * time.Second

// termResize is the page telling the shell its window's size.
type termResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// termExit is the server saying the shell has ended, and how.
type termExit struct {
	Exit int `json:"exit"`
}

func (s *Server) termRoutes() {
	if s.terms == nil {
		return
	}
	s.api("GET /api/cards/{id}/term", s.handleTerm)
}

// handleTerm is GET /api/cards/{id}/term, upgraded to a WebSocket: the
// card's shell, started if it has none.
func (s *Server) handleTerm(w http.ResponseWriter, r *http.Request) {
	// An upgrade is a GET, which Handler's same-origin check lets through
	// as a read. This one is the most a request here can ask for, so it is
	// held to what a write is: the cookie alone opens nothing.
	if !isSameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin terminal refused")
		return
	}
	id := r.PathValue("id")
	dir, ok := s.worktreeDir(w, r, id)
	if !ok {
		return
	}
	q := r.URL.Query()
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	sess, created, err := s.terms.Open(id, dir, cols, rows)
	switch {
	case errors.Is(err, term.ErrTooMany):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept has answered; a shell started for a socket that never
		// opened has nobody to use it
		if created {
			s.terms.End(id)
		}
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(termMaxFrame)
	if created {
		who, _ := WhoFrom(r.Context())
		s.opt.Log("web: %s on %s opened a terminal in %s (%s)", who.Person, who.Device, id, dir)
		s.hub.publish(webapi.Change{
			Kind: webapi.ChangeToast, ID: id, Except: who.DeviceID,
			Text: who.Person + " on " + who.Device + " opened a terminal in " + id,
		})
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	replay, sub := sess.Attach()
	defer func() { sub.Detach(s.now()) }()

	// what the page types, and its window's size
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if sess.Write(data) != nil {
					return
				}
				continue
			}
			var m termResize
			if json.Unmarshal(data, &m) == nil && m.Cols > 0 && m.Rows > 0 {
				sess.Resize(m.Cols, m.Rows)
			}
		}
	}()

	write := func(typ websocket.MessageType, p []byte) bool {
		wctx, done := context.WithTimeout(ctx, termWriteTimeout)
		defer done()
		return conn.Write(wctx, typ, p) == nil
	}
	if len(replay) > 0 && !write(websocket.MessageBinary, replay) {
		return
	}
	tick := time.NewTicker(termRecheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-sub.C:
			if ok {
				if !write(websocket.MessageBinary, chunk) {
					return
				}
				continue
			}
			select {
			case <-sess.Done():
				msg, _ := json.Marshal(termExit{Exit: sess.ExitCode()})
				write(websocket.MessageText, msg)
				_ = conn.Close(websocket.StatusNormalClosure, "the shell exited")
			default:
				// the page fell too far behind to keep: it comes back
				// and is replayed the scrollback
				_ = conn.Close(websocket.StatusTryAgainLater, "the page fell behind")
			}
			return
		case <-tick.C:
			// a device unpaired since the socket opened keeps no shell
			if who, ok := s.who(r); !ok || who.Pending {
				_ = conn.Close(websocket.StatusPolicyViolation, "this browser is not paired with the board")
				return
			}
			pctx, done := context.WithTimeout(ctx, termWriteTimeout)
			err := conn.Ping(pctx)
			done()
			if err != nil {
				return
			}
		}
	}
}

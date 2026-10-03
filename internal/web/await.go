package web

import (
	"context"
	"errors"
	"io"
	"net/http"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// Helpers for the routes that run an intent to its end and answer with
// what it did (ui.Bridge.Await), rather than the ones that only read.

// fail answers err: a board refusal with its class, a question the flow
// stopped at with 202 (webapi.StatusQuestion — it is not an error, so no
// browser logs it as a failed load), a board that has stopped with 503,
// a browser that went away with nothing.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if we, ok := ui.IsWebError(err); ok {
		status := http.StatusConflict
		switch {
		case we.Code == ui.WebBadRequest:
			status = http.StatusBadRequest
		case we.Code == ui.WebNotFound:
			status = http.StatusNotFound
		case we.Code == ui.WebUnavailable:
			status = http.StatusServiceUnavailable
		case webapi.IsQuestion(we.Reason):
			status = webapi.StatusQuestion
		}
		body := webapi.Error{Error: we.Text}
		if we.Reason != "" {
			// a refusal the page answers specially: the word, and the
			// sentence (or the line handed back) beside it
			body = webapi.Error{Error: we.Reason, Text: we.Text, By: we.By, Receipt: we.Receipt, Needs: webapi.ActionNeeds(we.Needs), Draft: we.Draft, Confirm: we.Confirm}
		}
		writeJSON(w, status, body)
		return
	}
	switch {
	case errors.Is(err, ui.ErrBridgeStopped):
		writeError(w, http.StatusServiceUnavailable, "the board has stopped")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// the browser went away; nobody is left to answer
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// answer writes an intent's outcome: 200 with what it said, or 409 when
// what it said is a failure — the same sentence the TUI's status band
// would have shown, since the board refused it after all.
func (s *Server) answer(w http.ResponseWriter, out ui.WebOutcome) {
	switch {
	case out.Err:
		writeError(w, http.StatusConflict, out.Text)
	default:
		writeJSON(w, http.StatusOK, webapi.Outcome{OK: true, Text: out.Text, ID: out.ID})
	}
}

// intent runs fn through the board and answers with its outcome.
func (s *Server) intent(w http.ResponseWriter, r *http.Request, fn func(m *ui.Shell) (tea.Cmd, error)) (ui.WebOutcome, bool) {
	out, err := s.opt.Board.Await(r.Context(), fn)
	if err != nil {
		s.fail(w, err)
		return out, false
	}
	s.answer(w, out)
	return out, !out.Err
}

// readBody decodes an optional JSON body: an empty one leaves v as it
// is, so a write with nothing to say may send nothing.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := readJSON(w, r, v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

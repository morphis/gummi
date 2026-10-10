package web

import (
	"net/http"
	"strconv"

	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) cardRoutes() {
	s.api("GET /api/cards/{id}", s.handleCard)
	s.api("GET /api/cards/{id}/writespec-draft", s.handleWritespecDraft(false))
	s.api("POST /api/cards/{id}/writespec-draft", s.handleWritespecDraft(true))
	s.api("POST /api/cards/{id}/composer", s.handleComposer)
	s.api("POST /api/cards/{id}/answer", s.handleAnswer)
	s.api("POST /api/cards/{id}/send", s.handleSend)
	s.api("POST /api/cards/{id}/queue/{n}/take", s.handleUnqueue)
	s.api("POST /api/cards/{id}/rewind", s.handleRewind)
	s.api("POST /api/cards/{id}/delegation", s.handleDelegation)
	s.api("POST /api/cards/{id}/actions/{action}", s.handleAction)
}

// person is who a write is recorded as.
func person(r *http.Request) string {
	who, _ := WhoFrom(r.Context())
	return who.Person
}

// handleCard is GET /api/cards/{id}: the card page's head, its pinned
// decision, its menu and what its composer would do.
func (s *Server) handleCard(w http.ResponseWriter, r *http.Request) {
	c, err := s.opt.Board.Card(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if c.Files != nil {
		c.Files.URL = s.filesURL(c.ID)
		c.Terminal = s.terms != nil
	}
	writeJSON(w, http.StatusOK, c)
}

// handleWritespecDraft serves the handoff brief a writespec dialog opens
// on. start is the POST: it starts the session's own brief turn, in the
// background, and answers at once — drafting, or the brief if one is
// already on the record. The GET is a pure read and starts nothing: it
// answers the brief as it stands, pending while it drafts. Either way the
// answer never waits on the turn, so leaving the page cannot cancel it.
func (s *Server) handleWritespecDraft(start bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		draft, err := s.opt.Board.WritespecDraft(r.Context(), r.PathValue("id"), start)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, draft)
	}
}

// handleComposer is POST /api/cards/{id}/composer: what sending text
// would do, asked as a person types (the page debounces it). It changes
// nothing.
func (s *Server) handleComposer(w http.ResponseWriter, r *http.Request) {
	var body webapi.SendRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {\"text\": …}")
		return
	}
	c, err := s.opt.Board.Composer(r.Context(), r.PathValue("id"), body.Text)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleAnswer is POST /api/cards/{id}/answer.
func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body webapi.AnswerRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {\"ref\", \"option\", \"against\"}")
		return
	}
	c, err := s.opt.Board.Answer(r.Context(), r.PathValue("id"), body, person(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleUnqueue is POST /api/cards/{id}/queue/{n}/take.
func (s *Server) handleUnqueue(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a queue index")
		return
	}
	res, err := s.opt.Board.Unqueue(r.Context(), r.PathValue("id"), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDelegation is POST /api/cards/{id}/delegation.
func (s *Server) handleDelegation(w http.ResponseWriter, r *http.Request) {
	var body webapi.DelegationRequest
	if err := readJSON(w, r, &body); err != nil || body.Budget < 0 {
		writeError(w, http.StatusBadRequest, "expected {\"budget\": credits ≥ 0, \"confirm_all\": bool}")
		return
	}
	if err := s.opt.Board.SetDelegation(r.Context(), r.PathValue("id"), body); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

// handleRewind is POST /api/cards/{id}/rewind.
func (s *Server) handleRewind(w http.ResponseWriter, r *http.Request) {
	var body webapi.RewindRequest
	if err := readJSON(w, r, &body); err != nil || body.Back < 1 {
		writeError(w, http.StatusBadRequest, "expected {\"back\": n}, n ≥ 1")
		return
	}
	res, err := s.opt.Board.Rewind(r.Context(), r.PathValue("id"), body.Back)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSend is POST /api/cards/{id}/send: one composer line.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var body webapi.SendRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {\"text\": …}")
		return
	}
	res, err := s.opt.Board.Send(r.Context(), r.PathValue("id"), body, person(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAction is POST /api/cards/{id}/actions/{action}: one menu entry.
// It answers the card as it now stands — or {ok: true} for an action that
// removed it.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var body webapi.ActionRequest
	if r.ContentLength != 0 {
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, bodyError(err, "expected the action's input as JSON"))
			return
		}
	}
	c, err := s.opt.Board.Action(r.Context(), r.PathValue("id"), r.PathValue("action"), body, person(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if c == nil {
		writeJSON(w, http.StatusOK, webapi.OK{OK: true})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

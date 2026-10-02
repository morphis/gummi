package web

import (
	"net/http"

	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) cardRoutes() {
	s.api("GET /api/cards/{id}", s.handleCard)
	s.api("POST /api/cards/{id}/composer", s.handleComposer)
	s.api("POST /api/cards/{id}/answer", s.handleAnswer)
	s.api("POST /api/cards/{id}/send", s.handleSend)
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
	}
	writeJSON(w, http.StatusOK, c)
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
			writeError(w, http.StatusBadRequest, "expected the action's input as JSON")
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

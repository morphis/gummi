package web

import (
	"net/http"

	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) createRoutes() {
	s.api("POST /api/cards", s.handleCreateCard)
	s.api("GET /api/form", s.handleForm)
}

// handleForm is GET /api/form[?repo=]: the new-card form's choices, the
// session model picker's included — with each installed agent's own model
// catalog merged in (Bridge.Form, off the loop).
func (s *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	f, err := s.opt.Board.Form(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleCreateCard is POST /api/cards: the form, submitted. It answers
// the new card.
func (s *Server) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var body webapi.CreateCardRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected the new-card form as JSON")
		return
	}
	c, err := s.opt.Board.CreateCard(r.Context(), body, person(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

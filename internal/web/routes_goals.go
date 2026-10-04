package web

import (
	"net/http"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) goalRoutes() {
	s.api("GET /api/goals", s.handleGoals)
	s.api("POST /api/goals", s.handleCreateGoal)
	s.api("GET /api/goals/{id}", s.handleGoal)
	s.api("POST /api/goals/{id}/actions/{action}", s.handleGoalAction)

	s.api("GET /api/stacks", s.handleStacks)
	s.api("POST /api/stacks", s.handleCreateStack)
	s.api("GET /api/stacks/{id}", s.handleStack)
	s.api("DELETE /api/stacks/{id}", s.handleDeleteStack)
	s.api("POST /api/stacks/{id}/cards", s.handleStackAdd)
	s.api("DELETE /api/stacks/{id}/cards/{card}", s.handleStackRemove)
	s.api("POST /api/stacks/{id}/move", s.handleStackMove)
	s.api("POST /api/stacks/{id}/rename", s.handleStackRename)
	s.api("POST /api/stacks/{id}/restack", s.handleRestack)
}

// handleGoals is GET /api/goals: the goal rows, from the board's memory.
func (s *Server) handleGoals(w http.ResponseWriter, r *http.Request) {
	var g webapi.Goals
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { g = m.WebGoals(); return nil }) {
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleGoal is GET /api/goals/{id}: the goal page.
func (s *Server) handleGoal(w http.ResponseWriter, r *http.Request) {
	g, err := s.opt.Board.Goal(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleCreateGoal is POST /api/goals.
func (s *Server) handleCreateGoal(w http.ResponseWriter, r *http.Request) {
	var req webapi.GoalCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, bodyError(err, "bad request body: "+err.Error()))
		return
	}
	out, err := s.opt.Board.CreateGoal(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.answer(w, out)
}

// handleGoalAction is POST /api/goals/{id}/actions/{action}.
func (s *Server) handleGoalAction(w http.ResponseWriter, r *http.Request) {
	var req webapi.GoalActionRequest
	if !readBody(w, r, &req) {
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	by := person(r)
	s.intent(w, r, func(m *ui.Shell) (tea.Cmd, error) { return m.WebGoalAction(id, action, req, by) })
}

// handleStacks is GET /api/stacks.
func (s *Server) handleStacks(w http.ResponseWriter, r *http.Request) {
	st, err := s.opt.Board.Stacks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleStack is GET /api/stacks/{id}.
func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	st, err := s.opt.Board.Stack(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// stackWrite reads a StackRequest and answers with what the write did.
func (s *Server) stackWrite(w http.ResponseWriter, r *http.Request, write func(webapi.StackRequest) (ui.WebOutcome, error)) {
	var req webapi.StackRequest
	if !readBody(w, r, &req) {
		return
	}
	out, err := write(req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.answer(w, out)
}

// handleCreateStack is POST /api/stacks: `gummi stack new`.
func (s *Server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	s.stackWrite(w, r, func(req webapi.StackRequest) (ui.WebOutcome, error) {
		return s.opt.Board.CreateStack(r.Context(), req)
	})
}

// handleStackAdd is POST /api/stacks/{id}/cards: `gummi stack add`.
func (s *Server) handleStackAdd(w http.ResponseWriter, r *http.Request) {
	s.stackWrite(w, r, func(req webapi.StackRequest) (ui.WebOutcome, error) {
		return s.opt.Board.AddToStack(r.Context(), r.PathValue("id"), req)
	})
}

// handleStackMove is POST /api/stacks/{id}/move: `gummi stack mv`.
func (s *Server) handleStackMove(w http.ResponseWriter, r *http.Request) {
	s.stackWrite(w, r, func(req webapi.StackRequest) (ui.WebOutcome, error) {
		return s.opt.Board.MoveInStack(r.Context(), r.PathValue("id"), req)
	})
}

// handleStackRename is POST /api/stacks/{id}/rename.
func (s *Server) handleStackRename(w http.ResponseWriter, r *http.Request) {
	s.stackWrite(w, r, func(req webapi.StackRequest) (ui.WebOutcome, error) {
		return s.opt.Board.RenameStack(r.Context(), r.PathValue("id"), req.Name)
	})
}

// handleStackRemove is DELETE /api/stacks/{id}/cards/{card}: `gummi stack rm`.
func (s *Server) handleStackRemove(w http.ResponseWriter, r *http.Request) {
	out, err := s.opt.Board.RemoveFromStack(r.Context(), r.PathValue("id"), r.PathValue("card"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.answer(w, out)
}

// handleDeleteStack is DELETE /api/stacks/{id}: an empty stack only.
func (s *Server) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	out, err := s.opt.Board.DeleteStack(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.answer(w, out)
}

// handleRestack is POST /api/stacks/{id}/restack: `gummi stack restack`.
func (s *Server) handleRestack(w http.ResponseWriter, r *http.Request) {
	res, err := s.opt.Board.Restack(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

package web

import (
	"net/http"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) boardRoutes() {
	s.api("GET /api/board", s.handleBoard)
	s.api("POST /api/board/resume", s.handleResume)
}

// handleResume is POST /api/board/resume: the quit-resume question the
// board holds (Board.Resume), answered.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	var body webapi.ResumeRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {\"cards\": [...]} or {\"none\": true}")
		return
	}
	if err := s.opt.Board.Resume(r.Context(), body, person(r)); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

// handleBoard is GET /api/board: the model's rail, and who is looking at
// it.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	// taken before the read, so whatever moves during it is still told
	since := s.hub.head()
	var b webapi.Board
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { b = m.WebBoard(); return nil }) {
		return
	}
	if b.Repo == "" || b.Repo == "." {
		b.Repo = s.opt.Repo
	}
	b.Viewers = s.hub.viewers()
	b.EventID = since
	writeJSON(w, http.StatusOK, b)
}

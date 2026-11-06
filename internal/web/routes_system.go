package web

import (
	"net/http"

	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) systemRoutes() {
	s.api("GET /api/doctor", s.handleDoctor)
	s.api("POST /api/doctor", s.handleDeepDoctor)
	s.api("POST /api/restart", s.handleRestart)
	s.api("GET /api/settings", s.handleSettings)
	s.api("PUT /api/settings", s.handleSetSettings)
	s.api("PUT /api/settings/credentials", s.handleSetCredentials)
	s.api("PUT /api/settings/identity", s.handleSetIdentity)
	s.api("PUT /api/settings/coauthors", s.handleSetCoAuthors)
	s.api("PUT /api/settings/workflows", s.handleSetWorkflows)
}

// handleSettings is GET /api/settings: the workspace's own knobs.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.opt.Board.Settings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSettings is PUT /api/settings: what the terminal's settings
// dialog saves, written to the workspace config. The board's next read
// carries the new name; the page refetches it from the answer's caller.
func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var req webapi.SettingsRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SetSettings(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetCredentials is PUT /api/settings/credentials: the GitHub token
// and SSH key gummi's own gh and push commands use. The answer describes
// what is stored and never carries it back.
func (s *Server) handleSetCredentials(w http.ResponseWriter, r *http.Request) {
	var req webapi.CredentialsRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SetCredentials(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetIdentity is PUT /api/settings/identity: who git writes this
// workspace's commits as, set in its repositories' own configuration.
func (s *Server) handleSetIdentity(w http.ResponseWriter, r *http.Request) {
	var req webapi.IdentityRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SetIdentity(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetCoAuthors is PUT /api/settings/coauthors: who is named in a
// Co-authored-by trailer on this workspace's commits, written to its
// config.
func (s *Server) handleSetCoAuthors(w http.ResponseWriter, r *http.Request) {
	var req webapi.CoAuthorsRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SetCoAuthors(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetWorkflows is PUT /api/settings/workflows: whether sessions are
// offered workflows and specs, written to the workspace's config.
func (s *Server) handleSetWorkflows(w http.ResponseWriter, r *http.Request) {
	var req webapi.WorkflowsRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SetWorkflows(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDoctor is GET /api/doctor: the checklist `gummi doctor --json`
// prints, from the command package's own builder.
//
// The deep run is not a GET. It asks every backend for a model turn, and
// a GET passes no same-origin check — any page on another port of this
// host could spend it with an <img> — so ?deep=1 here is refused and the
// page POSTs it instead.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if s.opt.Doctor == nil {
		notYet(w, r)
		return
	}
	if r.URL.Query().Get("deep") == "1" {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "deep checks spend a model turn per role; POST /api/doctor?deep=1 runs them")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Doctor(r))
}

// handleDeepDoctor is POST /api/doctor?deep=1: `gummi doctor --deep`, the
// live probe of every model the profiles name. As a write it passes the
// same-origin check.
func (s *Server) handleDeepDoctor(w http.ResponseWriter, r *http.Request) {
	if s.opt.Doctor == nil {
		notYet(w, r)
		return
	}
	if r.URL.Query().Get("deep") != "1" {
		writeError(w, http.StatusBadRequest, "POST /api/doctor is the deep run; say ?deep=1")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Doctor(r))
}

// handleRestart is POST /api/restart: the server stops the way a quit does
// and starts again from the same binary, so a new build or a setting read
// only at start takes effect. The answer is written before the host is
// told; the page's event stream reconnects once the server is back.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.opt.Restart == nil {
		notYet(w, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.opt.Restart()
}

package web

import (
	"net/http"

	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) scheduleRoutes() {
	s.api("GET /api/schedules", s.handleSchedules)
	s.api("POST /api/schedules", s.handleCreateSchedule)
	s.api("PATCH /api/schedules/{id}", s.handleUpdateSchedule)
	s.api("DELETE /api/schedules/{id}", s.handleDeleteSchedule)
	s.api("POST /api/schedules/{id}/enable", s.handleEnableSchedule)
	s.api("POST /api/schedules/{id}/disable", s.handleDisableSchedule)
	s.api("POST /api/schedules/{id}/run", s.handleRunSchedule)
	s.api("POST /api/schedules/preview", s.handleSchedulePreview)
	s.api("GET /api/schedules/catalog", s.handleScheduleCatalog)
}

// handleSchedulePreview is POST /api/schedules/preview: what the
// cadence inputs would store and when they would next fire, answered
// before anything is stored. A refused cadence answers 200 with the
// refusal in words — a form typing debounce is not a failed request.
func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	var req webapi.SchedulePreviewRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	out, err := s.opt.Board.SchedulePreview(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleScheduleCatalog is GET /api/schedules/catalog: the session
// picker's catalog, for the schedule form's backend and model pickers.
func (s *Server) handleScheduleCatalog(w http.ResponseWriter, r *http.Request) {
	out, err := s.opt.Board.ScheduleCatalog(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSchedules is GET /api/schedules: every row, oldest first.
func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Board.Schedules(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleCreateSchedule is POST /api/schedules: a definition, stored
// disabled. A mint body's repo is checked against the configured
// repositories before the store write (the Bridge's job), and the mint
// re-checks it at fire time.
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req webapi.ScheduleRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, bodyError(err, "bad request body: "+err.Error()))
		return
	}
	sc, err := s.opt.Board.CreateSchedule(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

// handleUpdateSchedule is PATCH /api/schedules/{id}: a definition edit,
// which leaves the row off until it is re-enabled.
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	var req webapi.ScheduleRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, bodyError(err, "bad request body: "+err.Error()))
		return
	}
	sc, err := s.opt.Board.UpdateSchedule(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// handleEnableSchedule is POST /api/schedules/{id}/enable: on goes with
// the first fire the cadence computes from now.
func (s *Server) handleEnableSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := s.opt.Board.EnableSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// handleDisableSchedule is POST /api/schedules/{id}/disable.
func (s *Server) handleDisableSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := s.opt.Board.DisableSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// handleRunSchedule is POST /api/schedules/{id}/run: the forced fire —
// the one fire a disabled row may take, a person at the board's confirm.
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	fire, err := s.opt.Board.RunScheduleNow(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fire)
}

// handleDeleteSchedule is DELETE /api/schedules/{id}: the row goes; the
// cards it minted stay.
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	out, err := s.opt.Board.DeleteSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.answer(w, out)
}

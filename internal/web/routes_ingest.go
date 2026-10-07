package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// maxIngestDocument bounds an uploaded or pasted spec. A spec is prose; a
// document past this is not one an architect pass should be reading.
const maxIngestDocument = 4 << 20

func (s *Server) ingestRoutes() {
	s.api("POST /api/ingest", s.handleStartIngest)
	s.api("GET /api/ingest", s.handleIngest)
	s.api("GET /api/ingest/{run}", s.handleIngest)
	s.api("POST /api/ingest/{run}/edit", s.handleIngestEdit)
	s.api("POST /api/ingest/{run}/approve", s.handleIngestApprove)
	s.api("POST /api/ingest/{run}/discard", s.handleIngestDiscard)
	s.api("GET /api/bugs", s.handleBugs)
	s.api("POST /api/bugs", s.handleImportBugs)
}

// handleStartIngest is POST /api/ingest: JSON naming a workspace file or
// carrying pasted markdown, or a multipart upload with the document under
// "file" and the other fields beside it.
func (s *Server) handleStartIngest(w http.ResponseWriter, r *http.Request) {
	// a document may take a phone a while to send; the caller is paired
	extendReadDeadline(w, uploadReadTimeout)
	var req webapi.IngestRequest
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		if !readIngestUpload(w, r, &req) {
			return
		}
	} else if err := readIngestJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, bodyError(err, "bad request body: "+err.Error()))
		return
	}
	run, err := s.opt.Board.StartIngest(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// readIngestJSON is readJSON with room for a document: the pasted
// markdown, JSON-escaped, beside the other fields.
func readIngestJSON(w http.ResponseWriter, r *http.Request, req *webapi.IngestRequest) error {
	if err := readJSONLimit(w, r, req, maxIngestDocument+maxBody); err != nil {
		return err
	}
	if len(req.Markdown) > maxIngestDocument {
		return errors.New("that document is too large to ingest")
	}
	return nil
}

// readIngestUpload reads the multipart form.
func readIngestUpload(w http.ResponseWriter, r *http.Request, req *webapi.IngestRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestDocument+maxBody)
	if err := r.ParseMultipartForm(maxIngestDocument); err != nil {
		writeError(w, http.StatusBadRequest, "bad upload: "+err.Error())
		return false
	}
	req.Path = r.FormValue("path")
	req.Name = r.FormValue("name")
	req.Profile = r.FormValue("profile")
	req.Repo = r.FormValue("repo")
	if v := strings.TrimSpace(r.FormValue("envelope")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "the envelope per card must be a whole, non-negative number of credits (0 is uncapped)")
			return false
		}
		req.Envelope = &n
	}
	f, hdr, err := r.FormFile("file")
	switch {
	case err == http.ErrMissingFile:
		req.Markdown = r.FormValue("markdown")
		return true
	case err != nil:
		writeError(w, http.StatusBadRequest, "bad upload: "+err.Error())
		return false
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, maxIngestDocument+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad upload: "+err.Error())
		return false
	}
	if len(body) > maxIngestDocument {
		writeError(w, http.StatusRequestEntityTooLarge, "that document is too large to ingest")
		return false
	}
	req.Markdown = string(body)
	if req.Name == "" {
		req.Name = hdr.Filename
	}
	return true
}

// handleIngest is GET /api/ingest/{run}, and GET /api/ingest for the
// board's current pass.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	var (
		run webapi.IngestRun
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { run, err = m.WebIngest(id); return nil }) {
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleIngestEdit is POST /api/ingest/{run}/edit: one review edit.
func (s *Server) handleIngestEdit(w http.ResponseWriter, r *http.Request) {
	var req webapi.IngestEditRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	id := r.PathValue("run")
	var (
		run webapi.IngestRun
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { run, err = m.WebIngestEdit(id, req); return nil }) {
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleIngestDiscard is POST /api/ingest/{run}/discard.
func (s *Server) handleIngestDiscard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	var (
		run webapi.IngestRun
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { run, err = m.WebIngestDiscard(id); return nil }) {
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleIngestApprove is POST /api/ingest/{run}/approve: mint the kept
// proposals. It answers with the cards it made.
func (s *Server) handleIngestApprove(w http.ResponseWriter, r *http.Request) {
	run, err := s.opt.Board.ApproveIngest(r.Context(), r.PathValue("run"))
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusOK
	if run.State == webapi.IngestFailed {
		status = http.StatusConflict
	}
	writeJSON(w, status, run)
}

// handleBugs is GET /api/bugs?repo=&label=&state=&limit=&in=&comments=.
func (s *Server) handleBugs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bq := ui.BugQuery{
		Repo: q.Get("repo"), Label: q.Get("label"), State: q.Get("state"), In: q.Get("in"),
		Comments: q.Get("comments") == "1",
	}
	switch bq.State {
	case "", "open", "closed", "all":
	default:
		writeError(w, http.StatusBadRequest, "state is open, closed or all")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "limit is a number")
			return
		}
		bq.Limit = n
	}
	bugs, err := s.opt.Board.Bugs(r.Context(), bq)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bugs)
}

// handleImportBugs is POST /api/bugs: mint the chosen issues.
func (s *Server) handleImportBugs(w http.ResponseWriter, r *http.Request) {
	var req webapi.BugsRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	if req.Limit < 0 {
		writeError(w, http.StatusBadRequest, "limit is a number")
		return
	}
	out, err := s.opt.Board.ImportBugs(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// A card's documents — its thread and live block, spec, diff, pull
// request and run — and the fleet's run. The model hands over what the
// reads need (ui.WebDocs, captured through the bridge) and the reads run
// here, on the request's goroutine, since they touch the disk, git and gh
// and the board's loop must not wait on any of those.

// prCacheTTL is how long a pull request's GitHub answer is reused: a
// page refetches a card's tabs on every change to the card, and each of
// those must not be two gh calls.
const prCacheTTL = 30 * time.Second

func (s *Server) docsRoutes() {
	prs := newPRCache(prCacheTTL, s.now)
	s.api("GET /api/cards/{id}/thread", s.handleThread)
	s.api("GET /api/cards/{id}/live", s.handleLive)
	s.api("GET /api/cards/{id}/spec", s.handleSpec)
	s.api("GET /api/cards/{id}/memory", s.handleMemory)
	s.api("POST /api/cards/{id}/spec/notes", s.handleSpecNote)
	s.api("POST /api/cards/{id}/spec/notes/resolve", s.handleSpecResolve)
	s.api("POST /api/cards/{id}/spec/changes", s.handleSpecChanges)
	s.api("GET /api/cards/{id}/diff", s.handleDiff)
	s.api("POST /api/cards/{id}/diff/annotations", s.handleAnnotate)
	s.api("DELETE /api/cards/{id}/diff/annotations/{aid}", s.handleAnnotationDelete)
	s.api("POST /api/cards/{id}/diff/annotations/{aid}/resolve", s.handleAnnotationResolve)
	s.api("POST /api/cards/{id}/diff/changes", s.handleDiffChanges)
	s.api("GET /api/cards/{id}/pr", func(w http.ResponseWriter, r *http.Request) { s.handlePR(w, r, prs) })
	s.api("POST /api/cards/{id}/pr/pull", func(w http.ResponseWriter, r *http.Request) { s.handlePRPull(w, r, prs) })
	s.api("GET /api/cards/{id}/stats", s.handleStats)
	s.api("GET /api/cards/{id}/log", s.handleLog)
	s.api("GET /api/cards/{id}/log/{sha}", s.handleLogCommit)
	s.api("POST /api/cards/{id}/log/plan", s.handleLogPlan)
	s.api("POST /api/cards/{id}/log/rewrite", s.handleLogRewrite)
	s.api("GET /api/fleet", s.handleFleet)
}

// docs captures the card's documents from the model, answering the
// request itself when it cannot.
func (s *Server) docs(w http.ResponseWriter, r *http.Request) (*ui.WebDocs, bool) {
	return s.capture(w, r, (*ui.Shell).WebDocs)
}

func (s *Server) capture(w http.ResponseWriter, r *http.Request, from func(*ui.Shell, string) (*ui.WebDocs, error)) (*ui.WebDocs, bool) {
	id := r.PathValue("id")
	var (
		d   *ui.WebDocs
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { d, err = from(m, id); return nil }) {
		return nil, false
	}
	if err != nil {
		writeDocsError(w, err, id)
		return nil, false
	}
	return d, true
}

// writeDocsError maps a document reader's refusal onto a status.
func writeDocsError(w http.ResponseWriter, err error, id string) {
	var inv *ui.InvalidError
	switch {
	case errors.Is(err, ui.ErrNoCard):
		writeError(w, http.StatusNotFound, "not found on "+id)
	case errors.Is(err, ui.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ui.ErrMoved):
		writeError(w, http.StatusConflict, "moved")
	case errors.Is(err, ui.ErrDetached):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.Msg)
	case errors.Is(err, context.Canceled):
		// the browser went away
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// changed tells every page the card moved, after the model has recounted
// what the write may have changed about its gate (Shell.RefreshBlockers):
// a diff comment or a spec note is a blocker, and a decision's answers
// read the count.
func (s *Server) changed(id string) {
	s.Publish(webapi.Change{Kind: webapi.ChangeCard, ID: id})
	_ = s.opt.Board.Do(context.Background(), func(m *ui.Shell) tea.Cmd { return m.RefreshBlockers(id) })
}

// handleThread is GET /api/cards/{id}/thread?after=<seq>.
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "after must be a sequence number")
			return
		}
		after = n
	}
	d, ok := s.capture(w, r, (*ui.Shell).WebThreadDocs)
	if !ok {
		return
	}
	t, err := d.Thread(r.Context(), after)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleLive is GET /api/cards/{id}/live: a projection, no reads.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var (
		live webapi.Live
		ok   bool
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { live, ok = m.WebLive(id); return nil }) {
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no card "+id+" on this board")
		return
	}
	writeJSON(w, http.StatusOK, live)
}

func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	sp, err := d.Spec(r.Context())
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

// handleMemory is GET /api/cards/{id}/memory: a freeform card's project
// memory — the workspace's global memory and the card's own session
// memory — read the way the spec tab reads a workflow card's document.
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	mem, err := d.Memory()
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, mem)
}

func (s *Server) handleSpecNote(w http.ResponseWriter, r *http.Request) {
	var req webapi.SpecNoteRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	who, _ := WhoFrom(r.Context())
	sp, err := d.AddSpecNote(r.Context(), req.Line, req.Text, who.Person, req.Attachments)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) handleSpecResolve(w http.ResponseWriter, r *http.Request) {
	var req webapi.SpecResolveRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	who, _ := WhoFrom(r.Context())
	sp, err := d.ResolveSpecNote(r.Context(), req, who.Person)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, sp)
}

// handleSpecChanges is POST /api/cards/{id}/spec/changes: the spec
// surface's "request changes", answered with what the board said — or,
// when it would send the card back, with the question to confirm first.
func (s *Server) handleSpecChanges(w http.ResponseWriter, r *http.Request) {
	var req webapi.ChangesRequest
	if !readBody(w, r, &req) {
		return
	}
	id, by := r.PathValue("id"), person(r)
	s.intent(w, r, func(m *ui.Shell) (tea.Cmd, error) { return m.WebRequestSpecChanges(id, by, req.Confirm) })
}

// handleDiff is GET /api/cards/{id}/diff[?since=<commit>].
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	diff, err := d.Diff(r.Context(), r.URL.Query().Get("since"))
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) handleAnnotate(w http.ResponseWriter, r *http.Request) {
	var req webapi.AnnotationRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	diff, err := d.AddAnnotation(r.Context(), req.Idx, req.Comment, req.Text, person(r))
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, diff)
}

// handleDiffChanges is POST /api/cards/{id}/diff/changes: the diff
// surface's "request changes", answered as handleSpecChanges is.
func (s *Server) handleDiffChanges(w http.ResponseWriter, r *http.Request) {
	var req webapi.ChangesRequest
	if !readBody(w, r, &req) {
		return
	}
	id, by := r.PathValue("id"), person(r)
	s.intent(w, r, func(m *ui.Shell) (tea.Cmd, error) { return m.WebRequestDiffChanges(id, by, req.Confirm) })
}

func annotationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil || aid <= 0 {
		writeError(w, http.StatusBadRequest, "not an annotation id: "+r.PathValue("aid"))
		return 0, false
	}
	return aid, true
}

func (s *Server) handleAnnotationDelete(w http.ResponseWriter, r *http.Request) {
	aid, ok := annotationID(w, r)
	if !ok {
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	diff, err := d.DeleteAnnotation(r.Context(), aid)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) handleAnnotationResolve(w http.ResponseWriter, r *http.Request) {
	aid, ok := annotationID(w, r)
	if !ok {
		return
	}
	var req webapi.AnnotationResolveRequest
	if err := readJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resolved := req.Resolved == nil || *req.Resolved
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	diff, err := d.ResolveAnnotation(r.Context(), aid, resolved)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, diff)
}

// handlePR is GET /api/cards/{id}/pr[?refresh=1], answered from a short
// cache unless the page asks for a fresh read.
func (s *Server) handlePR(w http.ResponseWriter, r *http.Request, cache *prCache) {
	id := r.PathValue("id")
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	// a cached read answers only for the pull request it read: linking or
	// unlinking one is seen at once, not a TTL later
	link := d.PRLink(r.Context())
	if r.URL.Query().Get("refresh") != "" {
		cache.drop(id)
	} else if p, ok := cache.get(id); ok && link != "" && p.Ref == link {
		writeJSON(w, http.StatusOK, p)
		return
	}
	p := d.PR(r.Context())
	if r.Context().Err() != nil {
		return
	}
	if p.Linked {
		cache.put(id, p)
	} else {
		cache.drop(id)
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePRPull is POST /api/cards/{id}/pr/pull: the card action that
// reads the PR's review threads onto the diff. It runs on the board;
// its outcome arrives as a toast and a card change.
func (s *Server) handlePRPull(w http.ResponseWriter, r *http.Request, cache *prCache) {
	id := r.PathValue("id")
	var err error
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd {
		var cmd tea.Cmd
		cmd, err = m.WebPullPR(id)
		return cmd
	}) {
		return
	}
	if err != nil {
		writeDocsError(w, err, id)
		return
	}
	cache.drop(id)
	writeJSON(w, http.StatusAccepted, webapi.OK{OK: true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	st, err := d.Stats(r.Context())
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleFleet is GET /api/fleet?from=&to=: RFC 3339 times. from=all is
// the workspace's whole history; no from is the stats tab's default
// window ending at to; no to is now.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var to, from time.Time
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be an RFC 3339 time")
			return
		}
		to = t
	}
	if to.IsZero() {
		to = s.now()
	}
	switch v := q.Get("from"); v {
	case "all":
	case "":
		from = to.Add(-ui.FleetDefaultWindow())
	default:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be an RFC 3339 time or all")
			return
		}
		from = t
	}
	var (
		fl  *ui.WebFleet
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { fl, err = m.WebFleet(); return nil }) {
		return
	}
	if err != nil {
		writeDocsError(w, err, "the board")
		return
	}
	rep, err := fl.Report(r.Context(), from, to)
	if err != nil {
		writeDocsError(w, err, "the board")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// prCache holds each card's last GitHub answer for a short while.
type prCache struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	m  map[string]prCached
}

type prCached struct {
	at time.Time
	pr webapi.PR
}

func newPRCache(ttl time.Duration, now func() time.Time) *prCache {
	return &prCache{ttl: ttl, now: now, m: map[string]prCached{}}
}

func (c *prCache) get(id string) (webapi.PR, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return webapi.PR{}, false
	}
	return e.pr, true
}

func (c *prCache) put(id string, p webapi.PR) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 256 {
		// a long-lived server forgets the cards nobody has looked at
		now := c.now()
		for k, e := range c.m {
			if now.Sub(e.at) >= c.ttl {
				delete(c.m, k)
			}
		}
	}
	c.m[id] = prCached{at: c.now(), pr: p}
}

func (c *prCache) drop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// handleLog is GET /api/cards/{id}/log: the card's own commits, and
// whether they may be rewritten now.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	l, err := d.Log(r.Context())
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// handleLogPlan is POST /api/cards/{id}/log/plan: a dry run of a rewrite.
func (s *Server) handleLogPlan(w http.ResponseWriter, r *http.Request) {
	var req webapi.RewriteRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	prev, err := d.PlanRewrite(r.Context(), req)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, prev)
}

// handleLogRewrite is POST /api/cards/{id}/log/rewrite: the rewrite
// itself, under the card's lock. gummi never pushes what it rewrote; the
// answer carries the command when the branch needs one.
func (s *Server) handleLogRewrite(w http.ResponseWriter, r *http.Request) {
	var req webapi.RewriteRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	res, err := d.Rewrite(r.Context(), req)
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	s.changed(r.PathValue("id"))
	writeJSON(w, http.StatusOK, res)
}

// handleLogCommit is GET /api/cards/{id}/log/{sha}: one commit's patch.
func (s *Server) handleLogCommit(w http.ResponseWriter, r *http.Request) {
	d, ok := s.docs(w, r)
	if !ok {
		return
	}
	c, err := d.CommitDiff(r.Context(), r.PathValue("sha"))
	if err != nil {
		writeDocsError(w, err, r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"os"
	"path"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
)

// A card's worktree files, served for a person to open what an agent
// wrote — a mockup, a report, a picture — in the browser.
//
// What is served is the agent's, not the page's, so it never runs as the
// board: every file is sent under a CSP sandbox, which gives it an origin
// of its own that the device cookie, the same-origin check and the API's
// answers all refuse. That cookie therefore cannot authenticate it, nor
// the stylesheets and scripts it loads by relative path, so the URL
// carries a key instead: an HMAC of the card's id under a secret the
// server draws when it starts. GET /api/cards/{id} hands it to a paired
// device (webapi.Files), the one way to learn it.

// filesPrefix is where a card's files are served: /files/<id>/<key>/<path>.
const filesPrefix = "/files/"

// filesCSP sandboxes a served file: scripts may run, as a mockup's do,
// but with no origin, so nothing they reach can answer as the board.
const filesCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"

func (s *Server) fileRoutes() {
	s.public("GET "+filesPrefix+"{id}/{key}/{path...}", s.handleFile)
}

// filesKey is card id's key under the server's secret.
func (s *Server) filesKey(id string) string {
	mac := hmac.New(sha256.New, s.filesSecret)
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

// filesURL is where card id's worktree is served from.
func (s *Server) filesURL(id string) string {
	return filesPrefix + id + "/" + s.filesKey(id) + "/"
}

// handleFile is GET /files/{id}/{key}/{path...}: one file from the card's
// worktree, read through an os.Root so neither ".." nor a symlink leaves it.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id, rel := r.PathValue("id"), r.PathValue("path")
	if !hmac.Equal([]byte(r.PathValue("key")), []byte(s.filesKey(id))) {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	var (
		d   *ui.WebDocs
		err error
	)
	if !s.do(w, r, func(m *ui.Shell) tea.Cmd { d, err = m.WebDocs(id); return nil }) {
		return
	}
	if err != nil {
		writeDocsError(w, err, id)
		return
	}
	dir, ok := d.FilesDir(r.Context())
	if !ok {
		writeError(w, http.StatusNotFound, id+" has no worktree")
		return
	}
	rel = path.Clean("/" + rel)[1:]
	if rel == "" || rel == ".git" || strings.HasPrefix(rel, ".git/") {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, id+" has no worktree")
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel) // a path that escapes the root is an error too
	if err != nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", filesCSP)
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

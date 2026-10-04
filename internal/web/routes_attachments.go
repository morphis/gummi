package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/morphis/gummi/internal/attachment"
)

// attachmentBodyLimit bounds an upload: attachment.MaxSize plus a little
// slack so the store's own over-limit refusal (not a truncated read) is
// what a one-byte-over upload gets.
const attachmentBodyLimit = attachment.MaxSize + 1024

func (s *Server) attachmentRoutes() {
	s.api("POST /api/attachments", s.handlePutAttachment)
	s.api("GET /api/attachments/{id}", s.handleGetAttachment)
}

// handlePutAttachment is POST /api/attachments: the raw image body, with
// its filename in X-Filename and its media type in Content-Type (sniffed
// from the bytes regardless — the header is display only). Upload is
// independent of any card, so the new-card form can attach before its
// card exists.
func (s *Server) handlePutAttachment(w http.ResponseWriter, r *http.Request) {
	// percent-encoded by the page (api.js's uploadAttachment): header
	// values must stay ASCII, and a pasted screenshot's name is whatever
	// the OS or clipboard gave it. An undecodable header is kept as-is —
	// display only, never trusted for anything else.
	name := r.Header.Get("X-Filename")
	if decoded, err := url.QueryUnescape(name); err == nil {
		name = decoded
	}
	body := http.MaxBytesReader(w, r.Body, attachmentBodyLimit)
	ref, err := s.opt.Board.PutAttachment(body, name)
	if err == nil {
		writeJSON(w, http.StatusCreated, ref)
		return
	}
	switch {
	case errors.Is(err, attachment.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "images are limited to 5 MB")
	case errors.Is(err, attachment.ErrNotImage):
		writeError(w, http.StatusUnsupportedMediaType, "only PNG, JPEG, GIF and WebP images are accepted")
	case isMaxBytesError(err):
		writeError(w, http.StatusRequestEntityTooLarge, "images are limited to 5 MB")
	default:
		s.fail(w, err)
	}
}

// isMaxBytesError reports whether err is http.MaxBytesReader's own refusal
// (its error type is unexported, so this matches by message the way the
// standard library's own callers do).
func isMaxBytesError(err error) bool {
	return err != nil && err.Error() == "http: request body too large"
}

// handleGetAttachment is GET /api/attachments/{id}: the stored bytes,
// same device auth as every other /api route. Content-addressed, so the
// response never changes for a given id — cached accordingly.
func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, mediaType, err := s.opt.Board.AttachmentPath(id)
	if err != nil {
		if errors.Is(err, attachment.ErrUnknown) {
			writeError(w, http.StatusNotFound, "unknown attachment")
			return
		}
		s.fail(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown attachment")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// an id never serves other bytes, but only to a paired browser
	// cookie: no shared cache may keep a copy for anyone else
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

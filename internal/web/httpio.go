package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// maxBody bounds a JSON request body. The largest thing the page sends as
// JSON is a composer line or a spec note; a document goes to ingest as a
// multipart upload with its own limit.
const maxBody = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", noCache)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, webapi.Error{Error: msg})
}

// readJSON decodes a request body into v, refusing unknown fields: a field
// the server does not read is a page and a server that disagree.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return readJSONLimit(w, r, v, maxBody)
}

// readJSONLimit is readJSON for a body allowed to be up to limit bytes.
func readJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON body")
	}
	return nil
}

// bodyError is what a 400 for an undecodable body says: fallback, unless
// the body is JSON whose only fault is a number that is not a whole one (a
// budget of 1.5, or 1e20). That is a person's typing, not a page and a
// server that disagree, so it is answered in the words of the field.
func bodyError(err error, fallback string) string {
	var te *json.UnmarshalTypeError
	if !errors.As(err, &te) || !strings.HasPrefix(te.Value, "number") {
		return fallback
	}
	field := te.Field
	if i := strings.LastIndexByte(field, '.'); i >= 0 {
		field = field[i+1:]
	}
	switch field {
	case "envelope", "number":
		return "Budget must be a whole, non-negative number of credits"
	case "runs":
		return "Runs must be a whole, non-negative number"
	case "minutes":
		return "Minutes must be a whole, non-negative number"
	case "":
		return fallback
	}
	return field + " must be a whole number"
}

// notYet answers a route of the contract nobody has built yet.
func notYet(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not built yet: "+r.Method+" "+r.URL.Path)
}

// do runs fn on the board's model and reports a failure to reach it as the
// response. It returns false when the caller should stop.
func (s *Server) do(w http.ResponseWriter, r *http.Request, fn func(m *ui.Shell) tea.Cmd) bool {
	err := s.opt.Board.Do(r.Context(), fn)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ui.ErrBridgeStopped):
		writeError(w, http.StatusServiceUnavailable, "the board has stopped")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// the browser went away; nobody is left to answer
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}

func clientIP(r *http.Request) string {
	// Deliberately the socket's own peer: gummi is not behind a proxy it
	// configured, so an X-Forwarded-For header here is an attacker's way
	// of picking a fresh rate-limit bucket per guess.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sourceKey is the address pairing counts wrong guesses against: the peer
// itself for IPv4, and its /64 for IPv6, where one subscriber holds a
// whole /64 and could otherwise start afresh at every address in it.
func sourceKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// deviceName turns a User-Agent into something a person recognizes in
// `gummi web devices`. It is a label, never a credential.
func deviceName(ua string) string {
	platform := "browser"
	switch {
	case strings.Contains(ua, "iPhone"):
		platform = "iPhone"
	case strings.Contains(ua, "iPad"):
		platform = "iPad"
	case strings.Contains(ua, "Android"):
		platform = "Android"
	case strings.Contains(ua, "Macintosh"):
		platform = "Mac"
	case strings.Contains(ua, "Windows"):
		platform = "Windows"
	case strings.Contains(ua, "Linux"):
		platform = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	if browser == "" {
		return platform
	}
	return platform + " · " + browser
}

// rateLimit is a token bucket's shape: burst tokens, one back every
// `every`.
type rateLimit struct {
	burst int
	every time.Duration
}

type limiter struct {
	lim rateLimit
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens int
	last   time.Time
}

func newLimiter(l rateLimit, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{lim: l, now: now, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// A bucket per peer, forgotten once it has been full for a while,
		// so a long-lived server cannot be made to remember every address
		// that ever knocked.
		if len(l.buckets) > 1024 {
			l.evictLocked(now)
		}
		b = &bucket{tokens: l.lim.burst, last: now}
		l.buckets[key] = b
	}
	refill := int(now.Sub(b.last) / l.lim.every)
	if refill > 0 {
		b.tokens = min(b.tokens+refill, l.lim.burst)
		b.last = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) evictLocked(now time.Time) {
	idle := time.Duration(l.lim.burst) * l.lim.every * 2
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}

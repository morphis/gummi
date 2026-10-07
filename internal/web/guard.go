package web

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// The server answers only to its own names (DNS rebinding), and gives a
// request only so long to send its body (slow clients).

// checkHost refuses a request whose Host is not one of the server's own
// names, on every route — the page, the public routes and the event
// stream alike.
//
// It is what makes the same-origin check mean anything. A page on
// attacker.example whose name is re-pointed at 127.0.0.1 after it loads
// (DNS rebinding) is, to the browser, same-origin with itself: its writes
// carry Origin and Host both naming attacker.example, and they agree. Only
// the Host tells this server that the browser thinks it is talking to
// somebody else. The names it answers to are ones an attacker cannot
// point anywhere: an address (loopback, or the one the connection
// arrived on), localhost, and the names the server was told are its own
// (Options.Hosts, AllowHosts) — the --addr host, the certificate's names,
// the tailnet's.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r) {
			writeError(w, http.StatusMisdirectedRequest,
				"this board does not answer to that name — open it at the address `gummi web` printed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(r *http.Request) bool {
	host := hostName(r.Host)
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() {
			return true
		}
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			if ap, err := netip.ParseAddrPort(local.String()); err == nil && ap.Addr().Unmap() == ip {
				return true
			}
		}
	}
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	_, ok := s.hosts[host]
	return ok
}

// AllowHosts adds names the server answers to, for a listener whose names
// are known only once it is up (the tailnet's MagicDNS name and
// addresses). A name may carry a port, which is ignored.
func (s *Server) AllowHosts(names ...string) {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	for _, n := range names {
		if h := hostName(n); h != "" {
			s.hosts[h] = struct{}{}
		}
	}
}

// hostName is a Host header or a configured name reduced to what is
// compared: no port, no brackets, no trailing dot, lower case.
func hostName(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// Slow clients. The http.Server bounds the headers (ReadHeaderTimeout) and
// idle keep-alives (IdleTimeout), but it cannot take a whole-request
// ReadTimeout: that deadline also cuts the event stream, and the context
// of any handler still running when it passes. So a request with a body
// gets a deadline for the body alone, lifted once the body has been read.
//
// They are variables only so a test can shorten them.
var (
	// bodyReadTimeout is how long a request may take to send its body.
	// Every JSON body here is small.
	bodyReadTimeout = 30 * time.Second
	// uploadReadTimeout is the same for a document upload (spec ingest),
	// which may be megabytes over a phone's connection. Only a paired
	// device gets it: the route extends the deadline after authenticating.
	uploadReadTimeout = 3 * time.Minute
)

// readDeadline puts bodyReadTimeout on reading a request's body. A request
// without one (every GET, the event stream) is left alone: there is
// nothing to read, and a deadline would only cancel it later.
func readDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil {
				r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// extendReadDeadline gives the rest of a request's body d to arrive.
func extendReadDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
}

// deadlineBody lifts the read deadline once the body is read to its end.
// The connection's next read is the server's own watch for the peer going
// away, which must not time out under a handler that is still working.
// A body that fails part-way keeps the deadline, so whatever the server
// still reads of it afterwards is bounded too.
type deadlineBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	done bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && !b.done {
		b.done = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

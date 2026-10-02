// Package web serves the gummi board to a browser as a page of its own
// (DESIGN §20): the rail of cards, the open card's conversation, and its
// spec, diff, pull request and stats beside it.
//
// The server holds no board of its own. `gummi web` builds the board
// exactly as the TUI does and runs the TUI's model without a screen
// (internal/ui's Bridge); every route here reads a projection of that
// model or runs an intent on it through Bridge.Do, and marshals a value
// from internal/webapi. Nothing in this package derives workflow logic —
// a route that needs a value the model does not compute is a sign the
// value belongs in the shared read model first (§20.1).
//
// What changes reaches the page as server-sent events (hub.go): the model
// reports what a message may have changed, the hub coalesces those into
// invalidations, and the page refetches the JSON the event names.
//
// Access is earned in two steps — a six-digit pairing code printed in the
// terminal running the server, exchanged once for a long-lived device
// token (pair.go) — and every write must also come from the page's own
// origin (auth.go).
package web

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// Rate limits. The pairing surface is the only unauthenticated thing here
// that costs anything, so it is the only thing metered: a guesser gets a
// handful of tries per minute, and code minting cannot be used to spam the
// operator's terminal.
var (
	redeemLimit = rateLimit{burst: 10, every: 6 * time.Second}
	mintLimit   = rateLimit{burst: 3, every: 20 * time.Second}
)

// Options configures a Server.
type Options struct {
	// Board is the running board's model, reached only through Do.
	Board *ui.Bridge
	// Devices is the paired-device store.
	Devices *Devices
	// Pairing holds the live pairing code.
	Pairing *Pairing
	// Log reports operator-facing lines — a minted code, a pairing, a
	// refused guess. It is the terminal running the server, which is the
	// only channel a pairing code may travel on.
	Log func(format string, args ...any)
	// Repo names the workspace, Host the machine, Version the binary: the
	// page's title bar.
	Repo    string
	Host    string
	Version string
	// WebDir is the workspace's .gummi/state/web, for state a route keeps
	// on disk (push subscriptions, the VAPID key).
	WebDir string
	// OpenAccess serves the board without pairing. Only ever set for a
	// loopback-only listener; the caller enforces that.
	OpenAccess bool
	// Hosts are the names the server answers to beyond its listener's own
	// address and loopback (localhost, 127.0.0.1, [::1]): the --addr host
	// when it is a name, the TLS certificate's names, a reverse proxy's
	// name. A request for any other Host is refused (guard.go). The
	// tailnet's names are added once the node is up (AllowHosts).
	Hosts []string
	// Secure marks the cookie Secure on every listener. Without it the
	// cookie is still Secure on a request that arrived over TLS — the
	// tailnet's HTTPS listener beside plain HTTP on loopback — so one
	// server can answer on both.
	Secure bool
	// AdminToken authenticates `gummi web pair` from another terminal on
	// this machine. Empty disables the admin route.
	AdminToken string
	// Push delivers Web Push notifications and holds the subscriptions;
	// nil serves the push routes as not set up.
	Push *Push
	// Doctor runs the readiness checklist `gummi doctor` prints. It lives
	// in the command package, so it is handed in rather than imported.
	Doctor func(*http.Request) webapi.Doctor
	// Now is injectable for tests.
	Now func() time.Time
	// Heartbeat and Coalesce tune the event stream; zero takes the
	// defaults (15s, 100ms). Tests shorten them.
	Heartbeat time.Duration
	Coalesce  time.Duration
}

// Server is the board's HTTP face.
type Server struct {
	opt     Options
	now     func() time.Time
	mux     *http.ServeMux
	redeems *limiter
	mints   *limiter
	assets  map[string]asset
	hub     *hub
	// filesSecret keys the URLs a card's files are served at
	// (routes_files.go); drawn per start, so they lapse with the server.
	filesSecret []byte

	hostsMu sync.RWMutex
	hosts   map[string]struct{}
}

// New builds the server. It does not listen; the caller owns the listeners.
func New(o Options) (*Server, error) {
	if o.Board == nil {
		return nil, errors.New("no board to serve")
	}
	if o.Devices == nil || o.Pairing == nil {
		return nil, errors.New("no pairing store to authenticate against")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	assets, err := loadAssets()
	if err != nil {
		return nil, err
	}
	s := &Server{
		opt:     o,
		now:     o.Now,
		mux:     http.NewServeMux(),
		redeems: newLimiter(redeemLimit, o.Now),
		mints:   newLimiter(mintLimit, o.Now),
		assets:  assets,
		hub:     newHub(o.Now, o.Coalesce),
		hosts:   map[string]struct{}{},
	}
	s.filesSecret = make([]byte, 32)
	if _, err := rand.Read(s.filesSecret); err != nil {
		return nil, err
	}
	s.AllowHosts(o.Hosts...)
	if !o.OpenAccess {
		// The devices file is the operator's, and so writable by whatever
		// runs as the operator: from here on the server honours a device
		// only as it knows it (Devices.Pin), so a row written in behind
		// its back is not a way past being let in.
		o.Devices.Pin(func(d Device) {
			o.Log("web: devices.json gained %s on %s (%s), which this server did not pair; it is not honoured — "+
				"`gummi web unpair %s` removes it", d.Person, d.Name, d.ID, d.ID)
		})
	}
	if o.Push != nil && !o.OpenAccess {
		// A subscription outlives nothing its device does not: a device
		// unpaired while this server was down (or by an older gummi) is
		// dropped the first time the board would have notified it.
		o.Push.Notifier.Paired = o.Devices.Has
	}
	s.routes()
	return s, nil
}

// Handler returns the server's routes behind its security headers, the
// Host check every request passes, the deadline on reading a body, and the
// same-origin check every write passes.
func (s *Server) Handler() http.Handler {
	return secureHeaders(s.checkHost(readDeadline(s.sameOrigin(s.mux))))
}

// secure reports whether the device cookie set on r's response is
// marked Secure: always on a server told so, and on any request that came
// over TLS.
func (s *Server) secure(r *http.Request) bool { return s.opt.Secure || r.TLS != nil }

// Publish fans a change out to every connected page. It never blocks: it
// is the board model's change hook (ui.Shell.SetChangeHook).
func (s *Server) Publish(c webapi.Change) { s.hub.publish(c) }

// Close ends every event stream, so an http.Server.Shutdown that follows
// is not left waiting on connections that never go idle.
func (s *Server) Close() { s.hub.close() }

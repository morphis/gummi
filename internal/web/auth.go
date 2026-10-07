package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/morphis/gummi/internal/webapi"
)

// cookieName carries the device token. It is HttpOnly and SameSite=Strict:
// the page never reads it, and no other site can make the browser send it.
const cookieName = "gummi_web"

// Who is the person behind a request: the paired device's person and the
// device itself. Every write records Person as the actor where the board
// takes one.
//
// Pending is a device paired but still waiting to be let in (approval.go):
// it reaches only the routes registered with s.waiting. Status is where
// the device stands, also for one identify refused (turned away, lapsed).
type Who struct {
	Person   string
	Device   string
	DeviceID string
	Pending  bool
	Status   string
}

// openWho is the synthetic viewer of a --no-pairing board.
var openWho = Who{Person: "local", Device: "unpaired access", DeviceID: "open"}

type whoKey struct{}

// WhoFrom returns the authenticated person a request carries. Only routes
// registered through Server.api have one.
func WhoFrom(ctx context.Context) (Who, bool) {
	w, ok := ctx.Value(whoKey{}).(Who)
	return w, ok
}

// who authenticates a request by its cookie. An open-access server
// (loopback only) reports a synthetic device so the rest of the code has
// one path.
func (s *Server) who(r *http.Request) (Who, bool) {
	who, ok, _ := s.identify(r)
	return who, ok
}

// identify is who, also reporting the token to renew the cookie with when
// the device's last-seen just slid forward (empty otherwise). A device
// waiting to be let in is identified, with Pending set; one whose wait
// ended without it is not, and comes back with its id and Status only.
func (s *Server) identify(r *http.Request) (_ Who, _ bool, renew string) {
	if s.opt.OpenAccess {
		return openWho, true, ""
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return Who{}, false, ""
	}
	// honoured only on the origin it was issued for: a cookie reaches
	// every port on its host, and a token another server received there
	// is no key to this board by another name or port
	dev, ok, touched := s.opt.Devices.VerifyAt(c.Value, requestOrigin(r))
	if !ok {
		return Who{DeviceID: dev.ID, Status: dev.Status}, false, ""
	}
	if touched {
		renew = c.Value
	}
	person := dev.Person
	if person == "" {
		// a device paired before pairing asked for a name
		person = dev.Name
	}
	return Who{Person: person, Device: dev.Name, DeviceID: dev.ID, Pending: dev.Status == StatusPending, Status: dev.Status}, true, renew
}

// setDeviceCookie sets (or, with an empty token, clears) the device
// cookie. The cookie lives as long as the device does unseen, and is set
// again whenever the server slides the device's last-seen, so the two
// expire together.
func (s *Server) setDeviceCookie(w http.ResponseWriter, r *http.Request, token string) {
	maxAge := int(deviceTTL.Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the connection: a plain-HTTP loopback listener cannot carry it
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

// errAwaitingApproval is what every route but the few a waiting device may
// reach answers it.
const errAwaitingApproval = "waiting for approval on a paired device"

// authed wraps a handler so it runs only for a paired device, with the
// person on the request's context. A device still waiting to be let in is
// refused 403 unless waitingOK.
func (s *Server) authed(h http.HandlerFunc, waitingOK bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok, renew := s.identify(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "this browser is not paired with the board")
			return
		}
		if who.Pending && !waitingOK {
			writeJSON(w, http.StatusForbidden, webapi.Error{Error: errAwaitingApproval, Approval: webapi.ApprovalPending})
			return
		}
		if renew != "" {
			s.setDeviceCookie(w, r, renew)
		}
		h(w, r.WithContext(context.WithValue(r.Context(), whoKey{}, who)))
	})
}

// api registers an authenticated route: a device with the board only.
func (s *Server) api(pattern string, h http.HandlerFunc) { s.mux.Handle(pattern, s.authed(h, false)) }

// waiting registers an authenticated route a device still waiting to be
// let in may reach too: its event stream, which tells it only about
// itself, and unpairing, which withdraws its request. Nothing else.
func (s *Server) waiting(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.authed(h, true))
}

// public registers a route that answers without a cookie.
func (s *Server) public(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// adminPath is exempt from the same-origin check: it is called by `gummi
// web pair` from a terminal, which has no origin, and it carries its own
// proof (loopback plus a bearer token only this machine's user can read).
const adminPath = "/api/admin/pair"

// sameOrigin refuses every write that did not come from the page itself
// (DESIGN §20.3). A JSON API has many doors, and the cookie alone would
// open each of them to any page the browser visits that can make a
// request; SameSite=Strict narrows that, and this closes it.
//
// A write is let through when the browser says it is same-origin
// (Sec-Fetch-Site, which a page cannot forge), or when its Origin names
// the host the request was sent to. A request with neither — curl, a
// script — is refused: nothing but the page has business writing here.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == adminPath || isSameOrigin(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusForbidden, "cross-origin write refused")
	})
}

// isSameOrigin is the Origin half of the check: the page's scheme and
// host must be the ones the request arrived on. The host is one the
// server answers to (checkHost runs first), so this is a same-origin
// check against a known name, not against whatever Host the request
// claimed.
func isSameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, requestScheme(r)) && strings.EqualFold(u.Host, r.Host)
}

// requestOrigin is where a request reached this server: its Host, port
// included — what a cookie does not tell apart, since a browser sends it
// to every port of the host that set it. The Host is one the server
// answers to (checkHost runs first).
func requestOrigin(r *http.Request) string {
	return strings.ToLower(r.Host)
}

// requestScheme is the scheme the page used to reach this request:
// https over TLS, and https too behind a TLS-terminating proxy on this
// machine (`tailscale serve`) that says so. Only a loopback peer is
// believed: a browser cannot set that header on a cross-origin request
// without a preflight this server never answers, and nothing off the
// machine is a proxy gummi was put behind.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") && isLoopback(clientIP(r)) {
		return "https"
	}
	return "http"
}

// viewer is who as the presence list shows them.
func (w Who) viewer() webapi.Viewer {
	return webapi.Viewer{Person: w.Person, Device: w.Device, DeviceID: w.DeviceID}
}

package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/web/push"
	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) sessionRoutes() {
	s.public("GET /api/session", s.handleSession)
	s.public("POST /api/pair", s.handlePair)
	s.public("POST /api/pair/request", s.handlePairRequest)
	s.public("POST "+adminPath, s.handleAdminPair)
	s.waiting("POST /api/unpair", s.handleUnpair)
	s.waiting("GET /api/events", s.handleEvents)
	s.approvalRoutes()
}

// handleSession is GET /api/session. It answers without a cookie: it is
// how the page decides between the pairing form and the board.
//
// A browser that is not paired is told only what the pairing form needs —
// that it is not paired, and whether a code is live. What the board is,
// where it runs and who a code was printed for are for a paired device.
// One paired but still waiting to be let in is told that, as whom, and
// for how long; one turned away (or whose wait lapsed) is told that too.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	who, authed, renew := s.identify(r)
	if !authed {
		un := unpairedSession{PairingLive: s.opt.Pairing.Live()}
		switch who.Status {
		case StatusRejected:
			un.Approval = webapi.ApprovalRejected
		case StatusExpired:
			un.Approval = webapi.ApprovalExpired
		}
		writeJSON(w, http.StatusOK, un)
		return
	}
	if renew != "" {
		s.setDeviceCookie(w, r, renew)
	}
	if who.Pending {
		writeJSON(w, http.StatusOK, webapi.Session{
			Approval:      webapi.ApprovalPending,
			ExpiresInSecs: s.pendingLeft(who.DeviceID),
			Person:        who.Person,
			Device:        who.Device,
			DeviceID:      who.DeviceID,
		})
		return
	}
	writeJSON(w, http.StatusOK, webapi.Session{
		Authed:     true,
		OpenAccess: s.opt.OpenAccess,
		Version:    s.opt.Version,
		Repo:       s.opt.Repo,
		Host:       s.opt.Host,
		Person:     who.Person,
		Device:     who.Device,
		DeviceID:   who.DeviceID,
	})
}

// unpairedSession is webapi.Session as a browser that is not paired gets
// it: the same field names, and nothing else.
type unpairedSession struct {
	Authed      bool   `json:"authed"`
	PairingLive bool   `json:"pairingLive"`
	Approval    string `json:"approval,omitempty"`
}

// maxPersonName bounds the name given when pairing: it is shown beside
// every receipt, so it is a name, not a paragraph.
const maxPersonName = 40

// reservedNames are the actors the board records that are not a named
// person: the terminal's bare "user", the unattended "autopilot", a
// goal's own "goal", and "local", the viewer of a board served without
// pairing. A person named one of them would read as that actor wherever
// the name is shown bare.
var reservedNames = []string{state.ActorUser, state.ActorAutopilot, "goal", openWho.Person}

// personName validates the name a person pairs under.
func personName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	switch {
	case name == "":
		return "", errors.New("say who you are: pairing needs a name")
	case len([]rune(name)) > maxPersonName:
		return "", fmt.Errorf("a name is at most %d characters", maxPersonName)
	case strings.Contains(name, ":"):
		// an actor is "kind:detail" ("user:Simon"); a colon in a name
		// would let it pass for another kind of actor
		return "", errors.New("a name cannot contain a colon")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", errors.New("a name cannot carry control characters")
		}
	}
	for _, reserved := range reservedNames {
		if strings.EqualFold(name, reserved) {
			return "", fmt.Errorf("%q is a name the board uses for itself; pick another", name)
		}
	}
	return name, nil
}

// handlePair is POST /api/pair: redeem the code printed in the server's
// terminal for a device token, under the name of the person pairing.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.opt.OpenAccess {
		writeError(w, http.StatusConflict, "this board is served without pairing")
		return
	}
	if !s.redeems.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many pairing attempts; wait a minute and try again")
		return
	}
	var body webapi.PairRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON body with a code and a name")
		return
	}
	// The name is checked before the code is spent: a typo in a name must
	// not cost a guess. A code minted for a person needs no name.
	person, err := personName(body.Name)
	if err != nil && (strings.TrimSpace(body.Name) != "" || s.opt.Pairing.LiveFor() == "") {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// So is room to wait: a pairing that would have to wait while too
	// many already do is refused before its code is spent.
	if s.opt.Pairing.LiveOrigin() != OriginTerminal {
		if err := s.opt.Devices.Room(); err != nil {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
	}
	src := sourceKey(clientIP(r))
	redeemed, err := s.opt.Pairing.RedeemFrom(src, strings.TrimSpace(body.Code))
	if err != nil {
		var (
			wrong  *WrongCodeError
			locked *LockedError
		)
		switch {
		case errors.As(err, &wrong):
			s.opt.Log("web: wrong pairing code from %s (%s left)", clientIP(r), tries(wrong.Remaining))
			remaining := wrong.Remaining
			writeJSON(w, http.StatusForbidden, webapi.Error{Error: err.Error(), Remaining: &remaining})
		case errors.Is(err, ErrCodeBurned):
			s.opt.Log("web: pairing code burned by wrong guesses from %s", clientIP(r))
			writeError(w, http.StatusForbidden, err.Error())
		case errors.As(err, &locked):
			switch {
			case locked.Started && locked.Source:
				s.opt.Log("web: %s is locked out of pairing until %s after %d wrong guesses; `gummi web pair` still pairs a browser",
					clientIP(r), locked.Until.Local().Format("Jan 2 15:04"), sourceGuessBudget)
			case locked.Started:
				s.opt.Log("web: pairing is locked for codes a browser asks for until %s after %d wrong guesses (the last from %s); `gummi web pair` still pairs a browser",
					locked.Until.Local().Format("Jan 2 15:04"), wrongGuessBudget, clientIP(r))
			}
			writeError(w, http.StatusTooManyRequests, err.Error())
		default:
			writeError(w, http.StatusForbidden, err.Error())
		}
		return
	}
	if redeemed.Person != "" {
		person = redeemed.Person
	}
	if person == "" {
		writeError(w, http.StatusBadRequest, "say who you are: pairing needs a name")
		return
	}
	token, dev, err := s.opt.Devices.Request(person, deviceName(r.UserAgent()), Arrival{
		Origin:    requestOrigin(r),
		Via:       redeemed.Origin,
		Source:    clientIP(r),
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, ErrTooManyPending):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setDeviceCookie(w, r, token)
	if dev.Status == StatusPending {
		s.announceRequest(dev)
		writeJSON(w, http.StatusOK, webapi.PairResponse{
			Person: dev.Person, Device: dev.Name, DeviceID: dev.ID,
			Pending: true, ExpiresInSecs: s.pendingLeft(dev.ID),
		})
		return
	}
	s.announcePairing(dev, redeemed.Origin, clientIP(r))
	writeJSON(w, http.StatusOK, webapi.PairResponse{Person: dev.Person, Device: dev.Name, DeviceID: dev.ID})
}

// announcePairing tells everyone already on the board that a device was
// added with the board at once — the first device, or one paired with the
// code printed at start: a line in the terminal, a notice on every open
// page, and a notification on every device subscribed to them. Any other
// pairing waits to be let in and is announced as a request instead
// (announceRequest).
func (s *Server) announcePairing(dev Device, origin CodeOrigin, from string) {
	line := "new device paired: " + dev.Person + " on " + dev.Name + " " + origin.ViaPage()
	s.opt.Log("web: paired %s on %s (%s) from %s %s", dev.Person, dev.Name, dev.ID, from, origin.Via())
	// the device that just paired is the one place the warning is not for
	s.hub.publish(webapi.Change{
		Kind: webapi.ChangeToast, Except: dev.ID,
		Text: line + ". If that was not you, unpair it on the machine hosting the board: gummi web unpair " + dev.ID,
	})
	if s.opt.Push != nil && !s.opt.OpenAccess {
		s.opt.Push.Notifier.Post(push.Message{Title: "New device paired", Body: line, URL: "/", Tag: "paired-" + dev.ID})
	}
}

// handlePairRequest is POST /api/pair/request: a browser asking for a
// fresh code, which is printed in the server's terminal and never
// returned — a code handed to whoever asked would authenticate them.
//
// Asking is not logging in, so it cannot take anything from a code that
// is already out: while one is live the answer is only that it is (the
// terminal shows it), with its guesses and its name left as they were. A
// new code comes once that one is used, expired or burned, and not at all
// while too many wrong guesses have pairing locked.
func (s *Server) handlePairRequest(w http.ResponseWriter, r *http.Request) {
	if s.opt.OpenAccess {
		writeError(w, http.StatusConflict, "this board is served without pairing")
		return
	}
	if who, authed := s.who(r); authed {
		if who.Pending {
			writeError(w, http.StatusConflict, "this browser is paired and waiting to be let in from a paired device")
			return
		}
		writeError(w, http.StatusConflict, "this browser is already paired")
		return
	}
	if !s.mints.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "a code was just printed; check the terminal running gummi web")
		return
	}
	code, expires, err := s.opt.Pairing.RequestFrom(sourceKey(clientIP(r)))
	var locked *LockedError
	switch {
	case errors.Is(err, ErrCodeLive):
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"live":          true,
			"expiresInSecs": int(expires.Sub(s.now()).Seconds()),
		})
		return
	case errors.As(err, &locked):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.opt.Log("web: pairing code %s (asked for by %s, good for %s)", code, clientIP(r), codeTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"expiresInSecs": int(expires.Sub(s.now()).Seconds()),
	})
}

// handleUnpair is POST /api/unpair: this browser forgets itself, and the
// board forgets it back — its notifications stop and its open event
// streams (other tabs) close, so it leaves the viewer list at once.
func (s *Server) handleUnpair(w http.ResponseWriter, r *http.Request) {
	if s.opt.OpenAccess {
		writeError(w, http.StatusConflict, "this board is served without pairing")
		return
	}
	who, _ := WhoFrom(r.Context())
	if _, err := s.opt.Devices.Forget(who.DeviceID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if who.Pending {
		// a request withdrawn: its banner goes from every page at once
		s.hub.publish(webapi.Change{Kind: webapi.ChangePairing, ID: who.DeviceID})
		s.setDeviceCookie(w, r, "")
		s.opt.Log("web: %s on %s (%s) withdrew its request to be let in", who.Person, who.Device, who.DeviceID)
		writeJSON(w, http.StatusOK, webapi.OK{OK: true})
		return
	}
	if s.opt.Push != nil {
		if err := s.opt.Push.Store.Remove(who.DeviceID); err != nil {
			s.opt.Log("web: dropping %s's notifications: %v", who.DeviceID, err)
		}
	}
	s.hub.dropDevice(who.DeviceID)
	s.setDeviceCookie(w, r, "")
	s.opt.Log("web: unpaired %s on %s (%s)", who.Person, who.Device, who.DeviceID)
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

// handleAdminPair backs `gummi web pair`: a second terminal on this
// machine asks the running server for a code. It is loopback-only and
// carries a token written 0600 beside the devices file, so reaching it
// means already being on the machine with the operator's own permissions.
func (s *Server) handleAdminPair(w http.ResponseWriter, r *http.Request) {
	if s.opt.AdminToken == "" {
		http.NotFound(w, r)
		return
	}
	if !isLoopback(clientIP(r)) {
		writeError(w, http.StatusForbidden, "the admin route only answers on loopback")
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.opt.AdminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "bad admin token")
		return
	}
	// Every code minted here is a notice on every open page, and whatever
	// runs as the operator can mint them: metered like a browser asking.
	if !s.mints.allow("admin") {
		writeError(w, http.StatusTooManyRequests, "a code was just minted; wait a little before asking for another")
		return
	}
	var body webapi.AdminPairRequest
	if r.ContentLength != 0 {
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "expected an empty body or {\"name\": …}")
			return
		}
	}
	person := ""
	if strings.TrimSpace(body.Name) != "" {
		var err error
		if person, err = personName(body.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	code, expires, err := s.opt.Pairing.MintFor(person)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Said in the terminal and on every open page: the admin route is the
	// operator's, and whatever else runs as the operator can call it too.
	who := "a browser"
	if person != "" {
		who = person
	}
	s.opt.Log("web: `gummi web pair` asked for a pairing code for %s", who)
	s.hub.publish(webapi.Change{Kind: webapi.ChangeToast, Text: "a pairing code was minted for " + who + " on the machine hosting the board"})
	writeJSON(w, http.StatusOK, webapi.AdminPairResponse{
		Code:          code,
		ExpiresInSecs: int(expires.Sub(s.now()).Seconds()),
	})
}

// NewAdminToken mints the token that authenticates `gummi web pair`.
func NewAdminToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating the admin token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

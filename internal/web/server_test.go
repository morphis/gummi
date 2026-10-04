package web

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/web/push"
	"github.com/morphis/gummi/internal/webapi"
)

// harness is a server over a real, screenless board model — a Shell with
// no workspace, which is all the session, pairing and event routes need.
type harness struct {
	t       *testing.T
	srv     *Server
	http    *httptest.Server
	bridge  *ui.Bridge
	pairing *Pairing
	devices *Devices

	mu  sync.Mutex
	log []string
}

func newHarness(t *testing.T, mutate ...func(*Options)) *harness {
	t.Helper()
	shell := ui.NewShell(theme.GummiDark(), "v0-test")
	shell.SetCopilotHint(false)
	shell.SetMotion(false)
	bridge := ui.NewHeadless(shell)
	go func() { _ = bridge.Run() }()
	t.Cleanup(bridge.Stop)

	devices, err := OpenDevices(filepath.Join(t.TempDir(), "web", "devices.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, bridge: bridge, pairing: NewPairing(nil), devices: devices}
	opt := Options{
		Board:      bridge,
		Devices:    devices,
		Pairing:    h.pairing,
		Repo:       "demo",
		Host:       "box",
		Version:    "v0-test",
		AdminToken: "admin-secret",
		Coalesce:   5 * time.Millisecond,
		Log: func(format string, args ...any) {
			h.mu.Lock()
			h.log = append(h.log, fmt.Sprintf(format, args...))
			h.mu.Unlock()
		},
	}
	for _, m := range mutate {
		m(&opt)
	}
	srv, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	shell.SetChangeHook(srv.Publish)
	h.srv = srv
	h.http = httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		h.http.Close()
	})
	return h
}

// client is a browser: a cookie jar, and the page's own origin on writes.
func (h *harness) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func (h *harness) do(c *http.Client, method, path, body string, hdr ...string) (*http.Response, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", h.http.URL)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1] // the client sends req.Host, not a Host header
			continue
		}
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	return res, out
}

// pair pairs c under name with a freshly minted code.
func (h *harness) pair(c *http.Client, name string) {
	h.t.Helper()
	code, _, err := h.pairing.Mint()
	if err != nil {
		h.t.Fatal(err)
	}
	res, body := h.do(c, http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":%q}`, code, name))
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("pairing failed: %d %v", res.StatusCode, body)
	}
}

func TestThePageIsServedLockedDown(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP %q allows inline code", csp)
	}
	if got := res.Header.Get("Cache-Control"); got != noCache {
		t.Errorf("the page is cached (%q)", got)
	}
	// the page loads its stylesheet and its module, versioned
	for _, re := range []string{
		`<link rel="stylesheet" href="/assets/app\.css\?v=[0-9a-f]{12}">`,
		`<script type="module" src="/assets/app\.js\?v=[0-9a-f]{12}"></script>`,
	} {
		if !regexp.MustCompile(re).Match(page) {
			t.Errorf("the page does not match %s", re)
		}
	}
	if strings.Contains(string(page), assetVersionToken) {
		t.Error("the page still carries the unreplaced version token")
	}
	for _, path := range []string{"/assets/app.js", "/assets/app.css", "/assets/icon.svg", "/manifest.webmanifest", "/sw.js"} {
		res, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, res.StatusCode)
		}
	}
	res, err = http.Get(h.http.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", res.StatusCode)
	}
}

func TestAssetsRevalidateAndCarryAnETag(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.http.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	etag := res.Header.Get("ETag")
	if etag == "" || res.Header.Get("Cache-Control") != assetCache {
		t.Fatalf("asset headers: etag %q, cache %q", etag, res.Header.Get("Cache-Control"))
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("app.js served as %q; a module needs a JavaScript type", ct)
	}
	req, _ := http.NewRequest(http.MethodGet, h.http.URL+"/assets/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", res.StatusCode)
	}
}

// Pairing takes a code and a name; the cookie that comes back is the
// browser's key, and the session then names the person.
func TestPairingWithANameIssuesAnHttpOnlyCookie(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	_, body := h.do(c, http.MethodGet, "/api/session", "")
	if body["authed"] != false {
		t.Fatalf("unpaired session = %v", body)
	}
	// what the board is and where it runs is for a paired browser
	for _, k := range []string{"repo", "host", "version", "pairingFor"} {
		if _, ok := body[k]; ok {
			t.Errorf("unpaired session carries %s: %v", k, body)
		}
	}
	if res, _ := h.do(c, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/board unpaired = %d, want 401", res.StatusCode)
	}

	code, _, _ := h.pairing.Mint()
	if res, body := h.do(c, http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q}`, code)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("pairing without a name = %d %v, want 400", res.StatusCode, body)
	}
	res, body := h.do(c, http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"  Simon  "}`, code),
		"User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1")
	if res.StatusCode != http.StatusOK || body["person"] != "Simon" || body["device"] != "iPhone · Safari" {
		t.Fatalf("pair = %d %v", res.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("device cookie = %+v, want HttpOnly SameSite=Strict", cookie)
	}
	if strings.Contains(fmt.Sprint(body), cookie.Value) {
		t.Error("the device token travelled in the JSON body")
	}

	_, body = h.do(c, http.MethodGet, "/api/session", "")
	if body["authed"] != true || body["person"] != "Simon" {
		t.Errorf("paired session = %v", body)
	}
	if d := h.devices.List(); len(d) != 1 || d[0].Person != "Simon" {
		t.Errorf("devices = %+v, want one device for Simon", d)
	}
}

// `gummi web --tailscale --ts-tls` serves one handler on plain HTTP on
// loopback and on HTTPS on the tailnet. The cookie must be Secure where the
// connection is, and must still work where it is not.
func TestTheCookieIsSecureOnATLSListener(t *testing.T) {
	h := newHarness(t)
	tlsSrv := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(tlsSrv.Close)

	pairCookie := func(base string, client *http.Client) *http.Cookie {
		t.Helper()
		code, _, err := h.pairing.Mint()
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, base+"/api/pair", strings.NewReader(fmt.Sprintf(`{"code":%q,"name":"Ana"}`, code)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", base)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("pair on %s = %d", base, res.StatusCode)
		}
		for _, ck := range res.Cookies() {
			if ck.Name == cookieName {
				return ck
			}
		}
		t.Fatalf("pair on %s set no device cookie", base)
		return nil
	}
	if ck := pairCookie(tlsSrv.URL, tlsSrv.Client()); !ck.Secure {
		t.Errorf("cookie over TLS = %+v, want Secure", ck)
	}
	if ck := pairCookie(h.http.URL, h.http.Client()); ck.Secure {
		t.Errorf("cookie over plain HTTP = %+v, want not Secure (the browser would drop it)", ck)
	}
}

func TestPairingReportsRemainingTries(t *testing.T) {
	h := newHarness(t)
	code, _, _ := h.pairing.Mint()
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	res, body := h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"x"}`, wrong))
	if res.StatusCode != http.StatusForbidden || body["remaining"] != float64(codeAttempts-1) {
		t.Errorf("wrong code = %d %v, want 403 with remaining %d", res.StatusCode, body, codeAttempts-1)
	}
}

// Every write must come from the page's own origin, cookie or no cookie.
func TestWritesMustBeSameOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")

	for _, tc := range []struct {
		name string
		hdr  []string
		want int
	}{
		{"no origin", []string{"Origin", ""}, http.StatusForbidden},
		{"another origin", []string{"Origin", "https://evil.example"}, http.StatusForbidden},
		{"null origin", []string{"Origin", "null"}, http.StatusForbidden},
		{"cross-site fetch", []string{"Origin", "", "Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		// through the check to the handler, which has no such card
		{"same-origin fetch", []string{"Origin", "", "Sec-Fetch-Site", "same-origin"}, http.StatusNotFound},
		{"matching origin", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, body := h.do(c, http.MethodPost, "/api/cards/FD-001/send", `{"text":"hi"}`, tc.hdr...)
			if res.StatusCode != tc.want {
				t.Errorf("POST = %d %v, want %d", res.StatusCode, body, tc.want)
			}
		})
	}
	// pairing is a write too
	code, _, _ := h.pairing.Mint()
	if res, _ := h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"x"}`, code), "Origin", "https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin pairing = %d, want 403", res.StatusCode)
	}
}

func TestAPairedBrowserCanUnpairItself(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")
	if res, _ := h.do(c, http.MethodPost, "/api/unpair", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("unpair = %d", res.StatusCode)
	}
	if h.devices.Count() != 0 {
		t.Error("the device is still paired")
	}
	if res, _ := h.do(c, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("board after unpair = %d, want 401", res.StatusCode)
	}
}

func TestAdminPairNeedsTheToken(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if res, _ := h.do(c, http.MethodPost, adminPath, "", "Origin", "", "Authorization", "Bearer nope"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("admin pair with a bad token = %d, want 401", res.StatusCode)
	}
	res, body := h.do(c, http.MethodPost, adminPath, "", "Origin", "", "Authorization", "Bearer admin-secret")
	if res.StatusCode != http.StatusOK || len(fmt.Sprint(body["code"])) != 6 {
		t.Errorf("admin pair = %d %v", res.StatusCode, body)
	}
	if !h.pairing.Live() {
		t.Error("the admin route did not mint a live code")
	}
}

func TestOpenAccessSkipsPairing(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.OpenAccess = true })
	c := h.client()
	_, body := h.do(c, http.MethodGet, "/api/session", "")
	if body["authed"] != true || body["openAccess"] != true {
		t.Errorf("open session = %v", body)
	}
	if res, _ := h.do(c, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusOK {
		t.Errorf("open board = %d", res.StatusCode)
	}
}

func TestBoardAndCardAnswerFromTheModel(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")
	res, body := h.do(c, http.MethodGet, "/api/board", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("board = %d %v", res.StatusCode, body)
	}
	if rows, ok := body["rows"].([]any); !ok || len(rows) != 0 || body["repo"] != "demo" {
		t.Errorf("board = %v, want an empty rail for repo demo", body)
	}
	if res, _ := h.do(c, http.MethodGet, "/api/cards/FD-404", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown card = %d, want 404", res.StatusCode)
	}
	if res, _ := h.do(c, http.MethodGet, "/api/cards/FD-404/thread", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown card's thread = %d, want 404", res.StatusCode)
	}
}

// sseReader reads a text/event-stream response event by event.
type sseReader struct {
	t   *testing.T
	res *http.Response
	sc  *bufio.Scanner
}

type sseMsg struct{ id, event, data string }

func (h *harness) events(c *http.Client, lastID string) *sseReader {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.http.URL+"/api/events", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		h.t.Fatalf("GET /api/events = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	h.t.Cleanup(func() { _ = res.Body.Close() })
	return &sseReader{t: h.t, res: res, sc: bufio.NewScanner(res.Body)}
}

// next returns the next event (comments and retry lines skipped).
func (r *sseReader) next() sseMsg {
	r.t.Helper()
	done := make(chan sseMsg, 1)
	go func() {
		var m sseMsg
		for r.sc.Scan() {
			line := r.sc.Text()
			switch {
			case line == "":
				if m.event != "" || m.data != "" {
					done <- m
					return
				}
			case strings.HasPrefix(line, "id: "):
				m.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				m.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				m.data = strings.TrimPrefix(line, "data: ")
			}
		}
		close(done)
	}()
	select {
	case m, ok := <-done:
		if !ok {
			r.t.Fatal("the event stream ended")
		}
		return m
	case <-time.After(5 * time.Second):
		r.t.Fatal("no event within 5s")
	}
	return sseMsg{}
}

// until skips events until one named event arrives.
func (r *sseReader) until(event string) sseMsg {
	r.t.Helper()
	for {
		if m := r.next(); m.event == event {
			return m
		}
	}
}

// The whole path: pair, open the stream, change something inside the
// board's model, and see the page told — then reconnect and resume.
func TestAChangeInTheModelReachesAnOpenPage(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")
	stream := h.events(c, "")

	// joining is itself news: the viewer list now has Simon on it
	v := stream.until("viewers")
	var ch webapi.Change
	if err := json.Unmarshal([]byte(v.data), &ch); err != nil || len(ch.Viewers) != 1 || ch.Viewers[0].Person != "Simon" {
		t.Fatalf("viewers event = %q (%v)", v.data, err)
	}
	_, body := h.do(c, http.MethodGet, "/api/board", "")
	if vs, _ := body["viewers"].([]any); len(vs) != 1 {
		t.Errorf("board viewers = %v, want Simon", body["viewers"])
	}

	// a notice raised inside Update is a toast on the page
	if err := h.bridge.Do(context.Background(), func(m *ui.Shell) tea.Cmd {
		m.EmitChange(webapi.Change{Kind: webapi.ChangeCard, ID: "FD-007"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := stream.until("card")
	if err := json.Unmarshal([]byte(got.data), &ch); err != nil || ch.ID != "FD-007" || got.id == "" {
		t.Fatalf("card event = %+v", got)
	}

	// a page that drops and comes back with its last id gets what it missed
	h.srv.Publish(webapi.Change{Kind: webapi.ChangeLive, ID: "FD-007"})
	missed := stream.until("live")
	resumed := h.events(c, got.id)
	if m := resumed.until("live"); m.id != missed.id {
		t.Errorf("resumed live event id %s, want %s", m.id, missed.id)
	}
	// and one whose id the server cannot vouch for is told to resync
	stale := h.events(c, "1")
	if m := stale.next(); m.event != webapi.EventResync {
		t.Errorf("a stale Last-Event-ID got %+v, want a resync", m)
	}
}

func TestTheStreamNeedsAPairedBrowser(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.http.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unpaired stream = %d, want 401", res.StatusCode)
	}
}

func TestPersonNames(t *testing.T) {
	for in, want := range map[string]string{"  Simon  Fels ": "Simon Fels", "ü": "ü"} {
		if got, err := personName(in); err != nil || got != want {
			t.Errorf("personName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", maxPersonName+1), "a\x00b"} {
		if _, err := personName(bad); err == nil {
			t.Errorf("personName(%q) accepted", bad)
		}
	}
}

func TestClientIPIgnoresForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:4242"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Errorf("clientIP = %q, want the socket peer", got)
	}
}

// `gummi web pair --name Ana` mints a code that pairs as Ana: the browser
// is told so, and needs only the code.
func TestANamedCodePairsAsItsPerson(t *testing.T) {
	h := newHarness(t)
	admin := h.client()
	res, body := h.do(admin, http.MethodPost, adminPath, `{"name":"Ana"}`, "Origin", "", "Authorization", "Bearer admin-secret")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin pair = %d %v", res.StatusCode, body)
	}
	code := fmt.Sprint(body["code"])

	c := h.client()
	// the name a code was printed for is not told to whoever asks
	_, sess := h.do(c, http.MethodGet, "/api/session", "")
	if _, ok := sess["pairingFor"]; ok || sess["pairingLive"] != true {
		t.Errorf("session = %v, want a live code and no name", sess)
	}
	res, body = h.do(c, http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"Mallory"}`, code))
	if res.StatusCode != http.StatusOK || body["person"] != "Ana" {
		t.Errorf("pair with a named code = %d %v, want Ana", res.StatusCode, body)
	}

	// an unnamed code still needs a name, and asking for one costs no guess
	code, _, _ = h.pairing.Mint()
	if res, _ := h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q}`, code)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unnamed code without a name = %d, want 400", res.StatusCode)
	}
	if res, _ := h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"Bo"}`, code)); res.StatusCode != http.StatusOK {
		t.Errorf("the code did not survive a missing name: %d", res.StatusCode)
	}
}

// ends reports whether the stream closes within d.
func (r *sseReader) ends(d time.Duration) bool {
	r.t.Helper()
	done := make(chan struct{})
	go func() {
		for r.sc.Scan() {
		}
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func (h *harness) deviceOf(person string) Device {
	h.t.Helper()
	for _, d := range h.devices.List() {
		if d.Person == person {
			return d
		}
	}
	h.t.Fatalf("no device paired as %s", person)
	return Device{}
}

func (h *harness) logged(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.log {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// Asking for a code from the page while one is live changes nothing about
// it: not its guesses, not the name it was printed for.
func TestAskingForACodeNeverReplacesALiveOne(t *testing.T) {
	h := newHarness(t)
	res, body := h.do(h.client(), http.MethodPost, adminPath, `{"name":"Ana"}`, "Origin", "", "Authorization", "Bearer admin-secret")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin pair = %d %v", res.StatusCode, body)
	}
	code := fmt.Sprint(body["code"])
	res, body = h.do(h.client(), http.MethodPost, "/api/pair/request", "{}")
	if res.StatusCode != http.StatusOK || body["live"] != true {
		t.Fatalf("asking over a live code = %d %v, want 200 live", res.StatusCode, body)
	}
	if h.logged("asked for by") {
		t.Error("a code was minted over the live one")
	}
	res, body = h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q}`, code))
	if res.StatusCode != http.StatusOK || body["person"] != "Ana" {
		t.Errorf("the named code after a request = %d %v, want it to pair as Ana", res.StatusCode, body)
	}
	// with no code live, asking prints one
	res, body = h.do(h.client(), http.MethodPost, "/api/pair/request", "{}")
	if res.StatusCode != http.StatusOK || body["live"] != nil || !h.logged("asked for by") {
		t.Errorf("asking with none live = %d %v", res.StatusCode, body)
	}
}

// Too many wrong guesses from one address lock that address out, say so
// in the terminal, and refuse both its guessing and its asking for a code
// — but not the operator's own code (`gummi web pair`), which pairs even
// from that address, and whose pairing every page already on the board
// is told about.
func TestTooManyWrongGuessesLockPairing(t *testing.T) {
	h := newHarness(t)
	watcher := h.client()
	h.pair(watcher, "Simon")
	events := h.events(watcher, "")
	if _, _, err := h.pairing.Request(); err != nil {
		t.Fatal(err)
	}
	var last *http.Response
	var lastBody map[string]any
	for range sourceGuessBudget {
		last, lastBody = h.do(h.client(), http.MethodPost, "/api/pair", `{"code":"not-a-code","name":"Mallory"}`)
	}
	if last.StatusCode != http.StatusTooManyRequests || !strings.Contains(fmt.Sprint(lastBody["error"]), "locked") {
		t.Fatalf("the guess that spent the budget = %d %v, want 429 locked", last.StatusCode, lastBody)
	}
	if !h.logged("is locked out of pairing") {
		t.Errorf("the terminal was not told: %v", h.log)
	}
	if res, _ := h.do(h.client(), http.MethodPost, "/api/pair/request", "{}"); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("asking for a code while locked = %d, want 429", res.StatusCode)
	}
	// the operator can still pair somebody, from that very address
	res, body := h.do(h.client(), http.MethodPost, adminPath, "", "Origin", "", "Authorization", "Bearer admin-secret")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin pair while locked = %d %v", res.StatusCode, body)
	}
	res, body = h.do(h.client(), http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":"Ana"}`, body["code"]))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the operator's code from a locked-out address = %d %v", res.StatusCode, body)
	}
	if body["pending"] != true {
		t.Errorf("a second device paired with `gummi web pair`'s code = %v, want it waiting to be let in", body)
	}
	if !h.logged("via the local CLI") {
		t.Errorf("the pairing was not logged with where its code came from: %v", h.log)
	}
	// and the device already on the board hears of it, as a request
	ev := events.until(string(webapi.ChangePairing))
	if !strings.Contains(ev.data, fmt.Sprint(body["deviceId"])) {
		t.Errorf("pairing event = %s, want it about %v", ev.data, body["deviceId"])
	}
}

// Unpairing from the page ends the device's notifications and its open
// streams, and the others see it leave.
func TestUnpairingClosesStreamsAndDropsNotifications(t *testing.T) {
	pusher, err := OpenPush(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Push = pusher })
	c, other := h.client(), h.client()
	h.pair(c, "Simon")
	h.pair(other, "Ana")
	dev := h.deviceOf("Simon")
	sub := testSubscription(t, dev.ID)
	if err := pusher.Store.Add(sub); err != nil {
		t.Fatal(err)
	}
	mine, theirs := h.events(c, ""), h.events(other, "")
	theirs.until(string(webapi.ChangeViewers))

	if res, _ := h.do(c, http.MethodPost, "/api/unpair", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("unpair = %d", res.StatusCode)
	}
	if !mine.ends(2 * time.Second) {
		t.Error("the unpaired device's stream stayed open")
	}
	if _, ok := pusher.Store.Get(dev.ID); ok {
		t.Error("the unpaired device is still subscribed to notifications")
	}
	for {
		m := theirs.until(string(webapi.ChangeViewers))
		if !strings.Contains(m.data, dev.ID) {
			break
		}
	}
}

// A device unpaired behind the server's back (`gummi web unpair` edits the
// file) loses its stream on the next heartbeat.
func TestAStreamClosesOnceItsDeviceIsUnpairedElsewhere(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Heartbeat = 20 * time.Millisecond })
	c := h.client()
	h.pair(c, "Simon")
	stream := h.events(c, "")
	if stream.ends(100 * time.Millisecond) {
		t.Fatal("the stream of a paired device closed")
	}
	if _, err := h.devices.Forget(h.deviceOf("Simon").ID); err != nil {
		t.Fatal(err)
	}
	if !stream.ends(2 * time.Second) {
		t.Error("the stream outlived its device")
	}
}

// The same, at the default heartbeat: a device revoked from the terminal
// stops hearing about the board within a couple of seconds, not at the
// next heartbeat a quarter-minute away.
func TestARevokedDevicesStreamClosesPromptly(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")
	stream := h.events(c, "")
	stream.until(string(webapi.ChangeViewers))
	if stream.ends(300 * time.Millisecond) {
		t.Fatal("the stream of a paired device closed")
	}
	if _, err := h.devices.Forget(h.deviceOf("Simon").ID); err != nil {
		t.Fatal(err)
	}
	if !stream.ends(3 * time.Second) {
		t.Error("the stream outlived its device by more than 3s")
	}
}

// A device's token is honoured only on the host and port it was paired
// on: the same cookie presented under another name the server answers to
// (or, what the name stands for, another port on this machine that
// received it) is not a paired browser.
func TestADeviceTokenIsBoundToWhereItPaired(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.pair(c, "Simon")
	if _, body := h.do(c, http.MethodGet, "/api/session", ""); body["authed"] != true {
		t.Fatalf("the paired browser is not authed: %v", body)
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(h.http.URL, "http://"))
	if _, body := h.do(c, http.MethodGet, "/api/session", "", "Host", "localhost:"+port); body["authed"] == true {
		t.Errorf("the token was honoured under another host: %v", body)
	}
}

// An active device's cookie slides with its last-seen, so it is not
// logged out at day 90 while the server still counts it as fresh.
func TestTheCookieSlidesWithLastSeen(t *testing.T) {
	c := newClock()
	devices, err := OpenDevices(filepath.Join(t.TempDir(), "devices.json"), c.now)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Devices = devices })
	h.devices = devices
	b := h.client()
	h.pair(b, "Simon")
	renewed := func() *http.Cookie {
		t.Helper()
		res, _ := h.do(b, http.MethodGet, "/api/session", "")
		for _, ck := range res.Cookies() {
			if ck.Name == cookieName {
				return ck
			}
		}
		return nil
	}
	if ck := renewed(); ck != nil {
		t.Errorf("a cookie was set again within the hour: %+v", ck)
	}
	c.add(lastSeenResolution)
	ck := renewed()
	if ck == nil || ck.MaxAge != int(deviceTTL.Seconds()) || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("after an hour the cookie = %+v, want it set again for the full TTL", ck)
	}
	// an authenticated route slides it too
	c.add(lastSeenResolution)
	res, _ := h.do(b, http.MethodGet, "/api/board", "")
	if res.StatusCode != http.StatusOK || len(res.Cookies()) == 0 {
		t.Errorf("GET /api/board an hour on = %d with cookies %v", res.StatusCode, res.Cookies())
	}
}

// A name cannot pass for another kind of actor.
func TestNamesCannotSpoofAnActor(t *testing.T) {
	for _, bad := range []string{"user:Ana", "user", "User", "autopilot", "goal", "local", "a:b"} {
		if _, err := personName(bad); err == nil {
			t.Errorf("personName(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"Ana", "Simon Fels", "local hero"} {
		if _, err := personName(ok); err != nil {
			t.Errorf("personName(%q) = %v", ok, err)
		}
	}
}

// testSubscription is a browser's push subscription for device, with keys
// that validate.
func testSubscription(t *testing.T, device string) push.Subscription {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	b64 := base64.RawURLEncoding
	return push.Subscription{
		Endpoint: "https://push.example/" + device,
		Keys:     push.Keys{P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)},
		Device:   device,
	}
}

// The "new device paired" notice is for everyone else at the board: the
// device that just paired is the one place its "if that was not you" is
// never shown — not live, and not in the backlog a reconnect resumes.
func TestTheNewDeviceNoticeSkipsTheDeviceItIsAbout(t *testing.T) {
	h := newHarness(t)
	c, other := h.client(), h.client()
	h.pair(c, "Simon")
	mine := h.events(c, "")
	since := mine.until(string(webapi.ChangeViewers))

	h.pair(other, "Ana")
	toast := mine.until(string(webapi.ChangeToast))
	if !strings.Contains(toast.data, "new device paired: Ana") || strings.Contains(toast.data, "`") {
		t.Fatalf("the notice others see = %s", toast.data)
	}
	theirs := h.events(other, since.id)
	for {
		m := theirs.next()
		if m.event == string(webapi.ChangeToast) {
			t.Fatalf("the device that just paired was told: %s", m.data)
		}
		if m.event == string(webapi.ChangeViewers) {
			break
		}
	}
}

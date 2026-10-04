package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/webapi"
)

// adminCode mints a code the way `gummi web pair` does.
func (h *harness) adminCode() string {
	h.t.Helper()
	res, body := h.do(h.client(), http.MethodPost, adminPath, "", "Origin", "", "Authorization", "Bearer admin-secret")
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("admin pair = %d %v", res.StatusCode, body)
	}
	return fmt.Sprint(body["code"])
}

// pairWith redeems code on c as name and returns the answer.
func (h *harness) pairWith(c *http.Client, name, code string) (int, map[string]any) {
	h.t.Helper()
	res, body := h.do(c, http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":%q,"name":%q}`, code, name), "User-Agent", "Mozilla/5.0 (iPhone) Safari/1")
	return res.StatusCode, body
}

// waiter pairs a second browser with `gummi web pair`'s code while
// "Simon" already has the board, and returns both.
func (h *harness) waiter() (at, waiting *http.Client, id string) {
	h.t.Helper()
	at, waiting = h.client(), h.client()
	h.pair(at, "Simon")
	status, body := h.pairWith(waiting, "Ana", h.adminCode())
	if status != http.StatusOK || body["pending"] != true {
		h.t.Fatalf("second pairing = %d %v, want it waiting", status, body)
	}
	return at, waiting, fmt.Sprint(body["deviceId"])
}

// The very first device has nobody to ask, so it is let in whatever code
// it used — here the one `gummi web pair` minted.
func TestTheFirstDeviceIsLetInWhateverItsCode(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	status, body := h.pairWith(c, "Simon", h.adminCode())
	if status != http.StatusOK || body["pending"] != nil {
		t.Fatalf("first pairing = %d %v, want it let in", status, body)
	}
	if _, sess := h.do(c, http.MethodGet, "/api/session", ""); sess["authed"] != true {
		t.Errorf("first device's session = %v", sess)
	}
}

// A code `gummi web pair` minted — which anything running as the operator
// can mint — pairs a second device only as far as waiting: it learns
// where it stands, and every other route refuses it.
func TestADeviceWaitingToBeLetInReachesNothing(t *testing.T) {
	h := newHarness(t)
	_, waiting, id := h.waiter()

	_, sess := h.do(waiting, http.MethodGet, "/api/session", "")
	if sess["authed"] != false || sess["approval"] != webapi.ApprovalPending || sess["deviceId"] != id || sess["person"] != "Ana" {
		t.Errorf("waiting session = %v", sess)
	}
	if n, _ := sess["expiresInSecs"].(float64); n <= 0 || n > pendingTTL.Seconds() {
		t.Errorf("expiresInSecs = %v", sess["expiresInSecs"])
	}
	for _, k := range []string{"repo", "host", "version"} {
		if v, ok := sess[k]; ok && v != "" {
			t.Errorf("a waiting device was told %s = %v", k, v)
		}
	}
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/board", ""},
		{http.MethodPost, "/api/cards", `{"kind":"freeform","title":"x"}`},
		{http.MethodGet, "/api/devices/pending", ""},
		{http.MethodPost, "/api/devices/" + id + "/approve", ""},
		{http.MethodGet, "/api/push/key", ""},
	} {
		res, body := h.do(waiting, r.method, r.path, r.body)
		if res.StatusCode != http.StatusForbidden || body["approval"] != webapi.ApprovalPending || body["error"] != errAwaitingApproval {
			t.Errorf("%s %s from a waiting device = %d %v, want 403 waiting", r.method, r.path, res.StatusCode, body)
		}
	}
	if h.devices.Has(id) {
		t.Error("Has is true for a waiting device (it would be notified)")
	}
}

// Every page at the board sees the request, with what the person
// deciding needs; letting it in takes effect at once — the waiting page's
// stream says so and ends, and the device has the board.
func TestApprovingLetsTheDeviceIn(t *testing.T) {
	h := newHarness(t)
	at, waiting, id := h.waiter()
	board := h.events(at, "")
	board.until(string(webapi.ChangeViewers))
	stream := h.events(waiting, "")

	res, body := h.do(at, http.MethodGet, "/api/devices/pending", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pending = %d %v", res.StatusCode, body)
	}
	b, _ := json.Marshal(body["devices"])
	var list []webapi.PendingDevice
	_ = json.Unmarshal(b, &list)
	if len(list) != 1 || list[0].ID != id || list[0].Person != "Ana" || list[0].Code != string(OriginCLI) ||
		!strings.Contains(list[0].Via, "minted on the machine hosting the board") || list[0].Source == "" || !strings.Contains(list[0].UserAgent, "iPhone") ||
		list[0].Device != "iPhone · Safari" || list[0].RequestedAt.IsZero() || list[0].ExpiresInSecs <= 0 {
		t.Fatalf("pending devices = %+v", list)
	}
	// the waiting device is not a viewer of the board
	if v := h.srv.hub.viewers(); len(v) != 1 || v[0].Person != "Simon" {
		t.Errorf("viewers = %+v, want only Simon", v)
	}

	if res, body := h.do(at, http.MethodPost, "/api/devices/"+id+"/approve", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %v", res.StatusCode, body)
	}
	if m := stream.until(string(webapi.ChangePairing)); !strings.Contains(m.data, id) {
		t.Errorf("the waiting page was told %s", m.data)
	}
	if !stream.ends(3 * time.Second) {
		t.Error("the waiting stream outlived the wait")
	}
	board.until(string(webapi.ChangePairing))
	if _, sess := h.do(waiting, http.MethodGet, "/api/session", ""); sess["authed"] != true || sess["approval"] != nil {
		t.Errorf("session once let in = %v", sess)
	}
	if res, _ := h.do(waiting, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusOK {
		t.Errorf("board once let in = %d", res.StatusCode)
	}
	if !h.logged("approved Ana") {
		t.Errorf("the approval was not logged: %v", h.log)
	}
	dev := h.deviceOf("Ana")
	if dev.Status != StatusApproved || !strings.HasPrefix(dev.DecidedBy, "Simon") || dev.DecidedAt.IsZero() {
		t.Errorf("devices.json records %+v", dev)
	}
	if res, _ := h.do(at, http.MethodPost, "/api/devices/"+id+"/approve", ""); res.StatusCode != http.StatusConflict {
		t.Errorf("approving twice = %d, want 409", res.StatusCode)
	}
}

// Turning a device away ends it: its page is told, its token opens
// nothing, and the record says who did it.
func TestRejectingTurnsTheDeviceAway(t *testing.T) {
	h := newHarness(t)
	at, waiting, id := h.waiter()
	stream := h.events(waiting, "")
	if res, body := h.do(at, http.MethodPost, "/api/devices/"+id+"/reject", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d %v", res.StatusCode, body)
	}
	if m := stream.until(string(webapi.ChangePairing)); !strings.Contains(m.data, id) {
		t.Errorf("the waiting page was told %s", m.data)
	}
	_, sess := h.do(waiting, http.MethodGet, "/api/session", "")
	if sess["authed"] != false || sess["approval"] != webapi.ApprovalRejected || sess["person"] != nil {
		t.Errorf("session once turned away = %v", sess)
	}
	if res, _ := h.do(waiting, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("board once turned away = %d, want 401", res.StatusCode)
	}
	if res, _ := h.do(waiting, http.MethodGet, "/api/events", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("events once turned away = %d, want 401", res.StatusCode)
	}
	dev := h.deviceOf("Ana")
	if dev.Status != StatusRejected || !strings.HasPrefix(dev.DecidedBy, "Simon") {
		t.Errorf("devices.json records %+v", dev)
	}
	if !h.logged("rejected Ana") {
		t.Errorf("the rejection was not logged: %v", h.log)
	}
}

// The code printed when the server started never crosses a file, so a
// device paired with it is let in even when others have the board; a
// code a browser asked for is printed in that same terminal, which an
// agent may be able to read, and waits.
func TestWhichCodesWait(t *testing.T) {
	h := newHarness(t)
	h.pair(h.client(), "Simon")
	code, _, _ := h.pairing.Mint()
	if status, body := h.pairWith(h.client(), "Bo", code); status != http.StatusOK || body["pending"] != nil {
		t.Errorf("terminal code = %d %v, want let in", status, body)
	}
	code, _, err := h.pairing.RequestFrom("198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	if status, body := h.pairWith(h.client(), "Cy", code); status != http.StatusOK || body["pending"] != true {
		t.Errorf("browser-asked code = %d %v, want waiting", status, body)
	}
}

// A waiting device can withdraw its own request: its banner goes from
// every page, and its cookie with it.
func TestAWaitingDeviceCanWithdraw(t *testing.T) {
	h := newHarness(t)
	at, waiting, id := h.waiter()
	board := h.events(at, "")
	if res, body := h.do(waiting, http.MethodPost, "/api/unpair", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("withdraw = %d %v", res.StatusCode, body)
	}
	if m := board.until(string(webapi.ChangePairing)); !strings.Contains(m.data, id) {
		t.Errorf("pairing event = %s", m.data)
	}
	if got := h.devices.Pending(); len(got) != 0 {
		t.Errorf("still pending after withdrawing: %+v", got)
	}
}

// Requests are bounded: a few may wait at once, and one that would be
// refused is refused before its code is spent.
func TestWaitingRequestsAreBounded(t *testing.T) {
	h := newHarness(t)
	h.pair(h.client(), "Simon")
	for i := range maxPending {
		code, _, _ := h.pairing.MintFor("")
		if status, body := h.pairWith(h.client(), fmt.Sprintf("P%d", i), code); status != http.StatusOK || body["pending"] != true {
			t.Fatalf("request %d = %d %v", i, status, body)
		}
	}
	code, _, _ := h.pairing.MintFor("")
	status, body := h.pairWith(h.client(), "Spam", code)
	if status != http.StatusTooManyRequests || !strings.Contains(fmt.Sprint(body["error"]), "waiting") {
		t.Errorf("one request too many = %d %v, want 429", status, body)
	}
	if !h.pairing.Live() {
		t.Error("the refused request spent its code")
	}
	// the operator's own code at the terminal is never held up by them
	code, _, _ = h.pairing.Mint()
	if status, body := h.pairWith(h.client(), "Operator", code); status != http.StatusOK || body["pending"] != nil {
		t.Errorf("terminal code while requests wait = %d %v", status, body)
	}
}

// `gummi web pair` is metered: every code it mints is a notice on every
// open page.
func TestAdminMintsAreMetered(t *testing.T) {
	h := newHarness(t)
	var last int
	for range mintLimit.burst + 1 {
		res, _ := h.do(h.client(), http.MethodPost, adminPath, "", "Origin", "", "Authorization", "Bearer admin-secret")
		last = res.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("a burst of admin mints ended %d, want 429", last)
	}
}

// devices.json is the operator's file, and whatever runs as the operator
// can write it. While the server runs, a device written in behind its
// back is not honoured, a waiting device written in as let in still
// waits — and a device taken out (`gummi web unpair`) is out at once.
func TestTheServerHonoursOnlyDevicesItKnows(t *testing.T) {
	h := newHarness(t)
	_, waiting, id := h.waiter()

	other, err := OpenDevices(h.devices.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	forged, _, err := other.Pair("Mallory", "curl")
	if err != nil {
		t.Fatal(err)
	}
	// flip the waiting device to let in, in the file
	b, err := os.ReadFile(h.devices.path)
	if err != nil {
		t.Fatal(err)
	}
	var f devicesFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for i := range f.Devices {
		f.Devices[i].Status = StatusApproved
	}
	b, _ = json.Marshal(f)
	time.Sleep(10 * time.Millisecond) // a new mtime on a coarse clock
	if err := os.WriteFile(h.devices.path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	c := h.client()
	u := h.http.URL
	req, _ := http.NewRequest(http.MethodGet, u+"/api/board", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: forged})
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a device written into devices.json = %d, want 401", res.StatusCode)
	}
	if !h.logged("which this server did not pair") {
		t.Errorf("the forged row was not reported: %v", h.log)
	}
	if res, _ := h.do(waiting, http.MethodGet, "/api/board", ""); res.StatusCode != http.StatusForbidden {
		t.Errorf("a waiting device let in by the file = %d, want 403", res.StatusCode)
	}
	if _, err := other.Forget(id); err != nil {
		t.Fatal(err)
	}
	if _, sess := h.do(waiting, http.MethodGet, "/api/session", ""); sess["approval"] != nil || sess["authed"] != false {
		t.Errorf("session after unpairing from the file = %v", sess)
	}
}

// A request nobody answers lapses; a lapsed or answered one is kept a
// day for the record, then dropped.
func TestAWaitLapses(t *testing.T) {
	c := newClock()
	d, err := OpenDevices(devicesPath(t), c.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Request("Simon", "Mac", Arrival{Via: OriginTerminal}); err != nil {
		t.Fatal(err)
	}
	token, dev, err := d.Request("Ana", "iPhone", Arrival{Via: OriginCLI, Source: "127.0.0.1"})
	if err != nil || dev.Status != StatusPending {
		t.Fatalf("second = %+v %v, want pending", dev, err)
	}
	if got, ok, _ := d.VerifyAt(token, ""); !ok || got.Status != StatusPending {
		t.Errorf("verify while waiting = %+v %v", got, ok)
	}
	c.add(pendingTTL)
	if got, ok, _ := d.VerifyAt(token, ""); ok || got.Status != StatusExpired {
		t.Errorf("verify after the wait = %+v %v, want expired", got, ok)
	}
	if _, err := d.Approve(dev.ID, "Simon"); !errors.Is(err, ErrNotPending) {
		t.Errorf("approving a lapsed request = %v", err)
	}
	if len(d.Pending()) != 0 {
		t.Error("a lapsed request is still pending")
	}
	c.add(decidedKeep + time.Minute)
	if _, err := d.Forget("nothing"); err == nil {
		t.Fatal("forgot nothing?")
	}
	for _, x := range d.List() {
		if x.ID == dev.ID {
			t.Errorf("a lapsed request outlived its day: %+v", x)
		}
	}
}

// At most maxPending wait at once, and at most pendingBudget are made in
// a window, however many are answered in between.
func TestRequestsAreRateLimited(t *testing.T) {
	c := newClock()
	d, err := OpenDevices(devicesPath(t), c.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Request("Simon", "Mac", Arrival{Via: OriginTerminal}); err != nil {
		t.Fatal(err)
	}
	for i := range pendingBudget {
		_, dev, err := d.Request("P", "x", Arrival{Via: OriginBrowser})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if _, err := d.Reject(dev.ID, "Simon"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.Request("P", "x", Arrival{Via: OriginBrowser}); !errors.Is(err, ErrTooManyPending) {
		t.Errorf("one past the budget = %v, want ErrTooManyPending", err)
	}
	c.add(pendingWindow)
	if _, _, err := d.Request("P", "x", Arrival{Via: OriginBrowser}); err != nil {
		t.Errorf("after the window = %v", err)
	}
}

// --no-pairing is unchanged: nobody waits, and there is nothing to answer.
func TestOpenAccessHasNothingToApprove(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.OpenAccess = true })
	c := h.client()
	_, body := h.do(c, http.MethodGet, "/api/devices/pending", "")
	if list, ok := body["devices"].([]any); !ok || len(list) != 0 {
		t.Errorf("pending on an open board = %v", body)
	}
	if res, _ := h.do(c, http.MethodPost, "/api/devices/abc/approve", ""); res.StatusCode != http.StatusConflict {
		t.Errorf("approve on an open board = %d, want 409", res.StatusCode)
	}
}

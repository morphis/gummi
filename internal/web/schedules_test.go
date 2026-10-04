package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// scheduleMintBody is a mint definition's body; repo is the configured
// name or empty for the workspace default.
func scheduleMintBody(repo string) string {
	return `{"name":"nightly triage","kind":"mint","repo":` + quoteJSON(repo) + `,"every":"1h",` +
		`"prompt":"triage new issues","envelope":50}`
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// scheduleRow reads a row straight from the store, the way a test
// asserts what the route did.
func scheduleRow(t *testing.T, store *state.Store, id string) domain.Schedule {
	t.Helper()
	sc, err := store.Schedule(context.Background(), domain.ScheduleID(id))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// TestScheduleRoutesCreateDisabled: a POST stores the definition off —
// off by default is the feature's own rule, and the page must see it too.
func TestScheduleRoutesCreateDisabled(t *testing.T) {
	b := newBoardHarness(t)

	var row map[string]any
	code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(scheduleMintBody("")), &row)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, row)
	}
	if row["enabled"] != false || row["cron"] != "0 * * * *" {
		t.Fatalf("created row = %v; want disabled with the compiled cron", row)
	}

	// The list reads it back the same way.
	var list struct {
		Schedules []map[string]any `json:"schedules"`
	}
	code = b.call(http.MethodGet, "/api/schedules", nil, &list)
	if code != http.StatusOK || len(list.Schedules) != 1 {
		t.Fatalf("list = %d %v", code, list.Schedules)
	}
	if list.Schedules[0]["id"] != "nightly-triage" {
		t.Errorf("list row id = %v", list.Schedules[0]["id"])
	}
}

// TestScheduleRoutesEnableRun: enabling arms the first fire; a forced
// run fires through the board's engine and answers with the outcome.
func TestScheduleRoutesEnableRun(t *testing.T) {
	b := newBoardHarness(t)

	// A heartbeat against a card the store has.
	f := persistFreeform(t, b.store, 1, "watch the deploy")
	body := `{"name":"hourly","kind":"heartbeat","target":"` + string(f.ID) + `","every":"5m","prompt":"keep going"}`
	var row map[string]any
	code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(body), &row)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, row)
	}

	var armed map[string]any
	code = b.call(http.MethodPost, "/api/schedules/hourly/enable", json.RawMessage(`{}`), &armed)
	if code != http.StatusOK {
		t.Fatalf("enable = %d %v", code, armed)
	}
	if armed["enabled"] != true {
		t.Fatalf("enabled row = %v; want enabled", armed)
	}
	if next, ok := armed["nextRun"].(string); !ok || next == "" {
		t.Fatalf("enabled row = %v; want a next run", armed)
	}

	var fire map[string]any
	code = b.call(http.MethodPost, "/api/schedules/hourly/run", json.RawMessage(`{}`), &fire)
	if code != http.StatusOK {
		t.Fatalf("run = %d %v", code, fire)
	}
	// A heartbeat's outcome names no card: the target is on the row.
	if fire["status"] != "ok" {
		t.Fatalf("run outcome = %v; want ok against the target", fire)
	}

	// And the same for a mint: a forced fire of a disabled row fires and
	// stays disabled.
	code = b.call(http.MethodPost, "/api/schedules", json.RawMessage(scheduleMintBody("")), &row)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, row)
	}
	var forced map[string]any
	code = b.call(http.MethodPost, "/api/schedules/nightly-triage/run", json.RawMessage(`{}`), &forced)
	if code != http.StatusOK {
		t.Fatalf("forced run = %d %v", code, forced)
	}
	if forced["forced"] != true || forced["status"] != "ok" {
		t.Fatalf("forced outcome = %v", forced)
	}
	if sc := scheduleRow(t, b.store, "nightly-triage"); sc.Enabled {
		t.Error("the forced fire left the row enabled")
	}
}

// TestScheduleRoutesRefusals: a mint body naming a repository the board
// does not configure is refused and stores nothing; the same routes
// behind no device token or a foreign origin are refused before the
// board is asked anything.
func TestScheduleRoutesRefusals(t *testing.T) {
	b := newBoardHarness(t)

	var refused map[string]any
	code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(scheduleMintBody("nope")), &refused)
	if code < 400 || code >= 500 {
		t.Fatalf("unknown repo = %d %v, want a 4xx", code, refused)
	}

	// The store's structural rules front-run the store write, so a mint
	// without a brake and a heartbeat without a target are refused here —
	// in the store's own words — and store nothing.
	for _, tc := range []struct {
		name, body, want string
	}{
		{"brakeless mint", `{"name":"brakeless","kind":"mint","every":"1h","prompt":"p"}`, "brake"},
		{"targetless heartbeat", `{"name":"targetless","kind":"heartbeat","every":"1h","prompt":"p"}`, "names the freeform card"},
	} {
		var out map[string]any
		code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(tc.body), &out)
		if code < 400 || code >= 500 {
			t.Errorf("%s = %d %v, want a 4xx", tc.name, code, out)
		}
		if !strings.Contains(fmt.Sprint(out["error"]), tc.want) {
			t.Errorf("%s error = %v, want %q in it", tc.name, out["error"], tc.want)
		}
	}

	var list struct {
		Schedules []map[string]any `json:"schedules"`
	}
	b.call(http.MethodGet, "/api/schedules", nil, &list)
	if len(list.Schedules) != 0 {
		t.Fatalf("the refused create stored %v", list.Schedules)
	}

	// An unauthenticated POST: no device token, 401.
	unpaired := b.client()
	if code := postStatus(t, b, unpaired, "/api/schedules", scheduleMintBody(""), b.http.URL); code != http.StatusUnauthorized {
		t.Fatalf("unpaired POST = %d, want 401", code)
	}

	// A cross-origin POST: refused by the same-origin check before the
	// board is asked anything.
	paired := b.client()
	b.pair(paired, "Cross")
	if code := postStatus(t, b, paired, "/api/schedules", scheduleMintBody(""), "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-origin POST = %d, want 403", code)
	}
}

// TestScheduleRouteDeleteAndEdit: an edit disables the row until it is
// re-enabled, and a delete removes it.
func TestScheduleRouteDeleteAndEdit(t *testing.T) {
	b := newBoardHarness(t)

	var row map[string]any
	code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(scheduleMintBody("")), &row)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, row)
	}
	// Enable, then edit: the edit forces the row off.
	var armed map[string]any
	code = b.call(http.MethodPost, "/api/schedules/nightly-triage/enable", json.RawMessage(`{}`), &armed)
	if code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
	var edited map[string]any
	code = b.call(http.MethodPatch, "/api/schedules/nightly-triage",
		json.RawMessage(`{"name":"nightly triage","kind":"mint","cron":"0 6 * * *","prompt":"triage new issues first","envelope":60}`), &edited)
	if code != http.StatusOK {
		t.Fatalf("patch = %d %v", code, edited)
	}
	if edited["enabled"] != false {
		t.Fatalf("the edited row = %v; an edit forces it off", edited)
	}
	if sc := scheduleRow(t, b.store, "nightly-triage"); sc.Cron != "0 6 * * *" {
		t.Errorf("the edited row's cron = %q", sc.Cron)
	}

	if code := b.call(http.MethodDelete, "/api/schedules/nightly-triage", nil, nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if _, err := b.store.Schedule(context.Background(), "nightly-triage"); err == nil {
		t.Fatal("the row survived its delete")
	}
}

// TestScheduleRouteRunNowOnDisabledRowRefusedNothing: the route's own
// contract is "a person at the board confirms"; a fire of a disabled row
// goes through, stays disabled, and the row never arms.
func TestScheduleRouteRunNowOnDisabledRow(t *testing.T) {
	b := newBoardHarness(t)
	f := persistFreeform(t, b.store, 1, "watch the deploy")
	body := `{"name":"hourly","kind":"heartbeat","target":"` + string(f.ID) + `","every":"1h","prompt":"keep going"}`
	var row map[string]any
	if code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(body), &row); code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, row)
	}
	var fire map[string]any
	code := b.call(http.MethodPost, "/api/schedules/hourly/run", json.RawMessage(`{}`), &fire)
	if code != http.StatusOK || fire["status"] != "ok" {
		t.Fatalf("forced run = %d %v", code, fire)
	}
	if sc := scheduleRow(t, b.store, "hourly"); sc.Enabled || !sc.NextRun.IsZero() {
		t.Errorf("after the forced fire the row reads enabled=%v next=%v", sc.Enabled, sc.NextRun)
	}
}

// TestSchedulePreview: the preview route answers the canonical cron the
// store would hold and its coming fires for a valid cadence, and a
// refusal in words — with 200 — for one it would reject at save, so the
// page's typing debounce is not a failed request. It is the same
// question the terminal's dialog answers locally, asked over JSON.
func TestSchedulePreview(t *testing.T) {
	b := newBoardHarness(t)

	var out struct {
		Cron         string   `json:"cron"`
		Fires        []string `json:"fires"`
		Error        string   `json:"error"`
		EnvelopeHint string   `json:"envelopeHint"`
	}
	code := b.call(http.MethodPost, "/api/schedules/preview", map[string]any{
		"every": "@daily", "timezone": "UTC",
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("preview = %d %v", code, out)
	}
	if out.Cron != "0 0 * * *" {
		t.Errorf("cron = %q, want the compiled preset", out.Cron)
	}
	if len(out.Fires) != 3 {
		t.Fatalf("fires = %v, want three", out.Fires)
	}
	now := time.Now().UTC()
	prev := now
	for _, raw := range out.Fires {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatalf("fire %q: %v", raw, err)
		}
		if at.Location() != time.UTC || at.Hour() != 0 || at.Minute() != 0 {
			t.Errorf("fire %v is not a UTC midnight", at)
		}
		if !at.After(prev) {
			t.Errorf("fire %v is not strictly after %v", at, prev)
		}
		prev = at
	}
	if out.Error != "" {
		t.Errorf("valid cadence answered %q", out.Error)
	}
	if out.EnvelopeHint == "" {
		t.Error("the preview carries no envelope hint")
	}

	// A cadence the package refuses: February 30th never exists, and the
	// answer says so instead of storing a schedule that never fires.
	var bad struct {
		Cron  string `json:"cron"`
		Fires []any  `json:"fires"`
		Error string `json:"error"`
	}
	code = b.call(http.MethodPost, "/api/schedules/preview", map[string]any{
		"cron": "0 0 30 2 *",
	}, &bad)
	if code != http.StatusOK {
		t.Fatalf("refused preview = %d", code)
	}
	if bad.Error == "" || bad.Cron != "0 0 30 2 *" {
		t.Errorf("refused preview = %+v, want the refusal and the expression echoed", bad)
	}
	if len(bad.Fires) != 0 {
		t.Errorf("a refused cadence answered fires %v", bad.Fires)
	}

	// Both filled: the cron wins, the same precedence the store write
	// applies, so a both-filled form previews what it saves.
	var both struct {
		Cron  string `json:"cron"`
		Error string `json:"error"`
	}
	b.call(http.MethodPost, "/api/schedules/preview", map[string]any{
		"every": "1h", "cron": "0 5 * * *", "timezone": "UTC",
	}, &both)
	if both.Error != "" || both.Cron != "0 5 * * *" {
		t.Errorf("both-filled preview = %+v, want the cron to win", both)
	}
}

// TestScheduleCatalogRoute: the schedule form's pickers are fed by the
// session picker's catalog — the same rows, served on their own route so
// the page does not drag the card form's payload for one picker pair.
func TestScheduleCatalogRoute(t *testing.T) {
	t.Setenv("GUMMI_AGENT_CMD", "true") // headless answers installed
	b := newBoardHarness(t)

	var cat struct {
		Default struct {
			Backend string `json:"backend"`
			Model   string `json:"model"`
		} `json:"default"`
		Agents []struct {
			Name      string   `json:"name"`
			Installed bool     `json:"installed"`
			Models    []string `json:"models"`
		} `json:"agents"`
	}
	code := b.call(http.MethodGet, "/api/schedules/catalog", nil, &cat)
	if code != http.StatusOK {
		t.Fatalf("catalog = %d", code)
	}
	if len(cat.Agents) != len(engine.SessionBackends) {
		t.Fatalf("catalog offers %d agents, want %d", len(cat.Agents), len(engine.SessionBackends))
	}
	seen := map[string]bool{}
	for _, a := range cat.Agents {
		seen[a.Name] = true
		if a.Name == "headless" {
			if !a.Installed {
				t.Error("headless reads uninstalled with GUMMI_AGENT_CMD set")
			}
			if a.Models == nil {
				t.Error("headless models is null, want [] — the merge ran")
			}
		} else if a.Models == nil {
			t.Errorf("%s models is null, want []", a.Name)
		}
	}
	for _, name := range engine.SessionBackends {
		if !seen[name] {
			t.Errorf("catalog does not offer %s", name)
		}
	}
	if cat.Default.Backend == "" || cat.Default.Model == "" {
		t.Errorf("catalog default = %+v, want the default profile's implementer", cat.Default)
	}
}

// TestScheduleRoutesRefuseAPairNoSessionCouldRun: the pre-save refusal
// the session picker applies is applied here too — a pair the engine
// would refuse is refused at save, not at the first fire.
func TestScheduleRoutesRefuseAPairNoSessionCouldRun(t *testing.T) {
	b := newBoardHarness(t)

	var refused map[string]any
	code := b.call(http.MethodPost, "/api/schedules", json.RawMessage(
		`{"name":"wrong pair","kind":"mint","every":"1h","prompt":"p","envelope":10,"backend":"claude","model":"gpt-5"}`), &refused)
	if code < 400 || code >= 500 {
		t.Fatalf("foreign model = %d %v, want a 4xx", code, refused)
	}
	var list struct {
		Schedules []map[string]any `json:"schedules"`
	}
	b.call(http.MethodGet, "/api/schedules", nil, &list)
	if len(list.Schedules) != 0 {
		t.Fatalf("the refused pair stored %v", list.Schedules)
	}

	// A heartbeat naming a pair carries neither: its target's session is
	// the spender, and the store's validation would refuse the rest.
	f := persistFreeform(t, b.store, 1, "watch the deploy")
	var row map[string]any
	code = b.call(http.MethodPost, "/api/schedules", json.RawMessage(
		`{"name":"hb","kind":"heartbeat","target":"`+string(f.ID)+`","every":"1h","prompt":"p","backend":"claude","model":"gpt-5","envelope":9}`), &row)
	if code != http.StatusCreated {
		t.Fatalf("heartbeat with a pair = %d %v", code, row)
	}
	if row["backend"] != nil || row["envelope"] != nil {
		t.Errorf("the stored heartbeat carries %v", row)
	}
}

// postStatus is one POST with a raw string body and a chosen Origin,
// answered with its status: the origin is a header choice, not the
// client's cookie, so the origin checks are asserted on it directly.
func postStatus(t *testing.T, b *boardHarness, c *http.Client, path, body, origin string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, b.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// persistFreeform stores an FF card, so a heartbeat has a target.
func persistFreeform(t *testing.T, store *state.Store, num int, title string) domain.Feature {
	t.Helper()
	id, err := domain.NewID(domain.KindFreeform, num)
	if err != nil {
		t.Fatal(err)
	}
	slug, err := domain.Slugify(title)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: title, Slug: slug,
		Stage: domain.StageOpen, Budget: domain.Budget{Envelope: 400},
		BranchScheme: domain.BranchSchemeKind, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

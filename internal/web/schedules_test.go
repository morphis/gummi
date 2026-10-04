package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
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
		json.RawMessage(`{"name":"nightly triage","cron":"0 6 * * *","prompt":"triage new issues first","envelope":60}`), &edited)
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

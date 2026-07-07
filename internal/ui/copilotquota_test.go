package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// tokenPayload is the live shape as of mid-2026: token-based billing
// reported under the legacy premium_interactions key.
const tokenPayload = `{
  "quota_snapshots": {
    "premium_interactions": {
      "entitlement": 45000,
      "has_quota": true,
      "unlimited": false,
      "percent_remaining": 89.7,
      "quota_remaining": 40389.1,
      "token_based_billing": true
    },
    "chat": {
      "has_quota": true,
      "unlimited": true,
      "percent_remaining": 100,
      "token_based_billing": false
    }
  }
}`

func TestParseCopilotQuota(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		pct  float64
		ok   bool
	}{
		{"token billing", tokenPayload, 89.7, true},
		{
			// legacy request-based annual subscribers keep the old model
			"legacy billing",
			`{"quota_snapshots":{"premium_interactions":{"has_quota":true,"percent_remaining":42.5}}}`,
			42.5, true,
		},
		{
			// a renamed key must not lose the reading
			"renamed snapshot key",
			`{"quota_snapshots":{"ai_credits":{"has_quota":true,"percent_remaining":63.2,"token_based_billing":true}}}`,
			63.2, true,
		},
		{
			// token-billed snapshots win over legacy ones, then the
			// smallest remainder is the binding constraint
			"mixed classes, minimum wins",
			`{"quota_snapshots":{
				"legacy":{"has_quota":true,"percent_remaining":5},
				"a":{"has_quota":true,"percent_remaining":70,"token_based_billing":true},
				"b":{"has_quota":true,"percent_remaining":30,"token_based_billing":true}}}`,
			30, true,
		},
		{
			"unlimited only",
			`{"quota_snapshots":{"chat":{"has_quota":true,"unlimited":true,"percent_remaining":100}}}`,
			0, false,
		},
		{"no snapshots", `{"quota_snapshots":{}}`, 0, false},
		{"missing snapshots", `{"access_type":"none"}`, 0, false},
		{"malformed", `not json`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pct, ok := parseCopilotQuota([]byte(tc.raw))
			if ok != tc.ok || pct != tc.pct {
				t.Fatalf("got (%v, %v), want (%v, %v)", pct, ok, tc.pct, tc.ok)
			}
		})
	}
}

// TestCopilotQuotaFlow drives fetch → update → status pill through the
// gh seam, including the alert flip and the poll re-arm.
func TestCopilotQuotaFlow(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.ghCopilotUser = func(context.Context) ([]byte, error) {
		return []byte(tokenPayload), nil
	}

	msg := m.fetchCopilotQuota()()
	qm, ok := msg.(copilotQuotaMsg)
	if !ok || !qm.quota.ok || qm.quota.percent != 89.7 || !qm.retry {
		t.Fatalf("unexpected fetch result: %#v", msg)
	}
	_, cmd := m.Update(qm)
	if cmd == nil {
		t.Fatal("a successful reading did not re-arm the poll")
	}
	if bar := ansi.Strip(m.statusView(120)); !strings.Contains(bar, "copilot 90%") {
		t.Fatalf("status bar missing quota pill: %q", bar)
	}

	// low quota flips the pill to alert, which statusView encodes via
	// KindAlert; assert at the data level to avoid styling assumptions
	m.copilot = copilotQuota{percent: 3.2, ok: true}
	if !m.copilot.low() {
		t.Error("3.2%% not reported as low")
	}
	if bar := ansi.Strip(m.statusView(120)); !strings.Contains(bar, "copilot 3%") {
		t.Fatalf("status bar missing low-quota pill: %q", bar)
	}

	// a gh failure hides the pill but keeps polling (auth may heal)
	m.ghCopilotUser = func(context.Context) ([]byte, error) {
		return nil, errors.New("gh: not authenticated")
	}
	qm = m.fetchCopilotQuota()().(copilotQuotaMsg)
	if qm.quota.ok || !qm.retry {
		t.Fatalf("failed fetch should hide but retry, got %#v", qm)
	}
	_, cmd = m.Update(qm)
	if cmd == nil {
		t.Fatal("failed reading did not re-arm the poll")
	}
	if bar := ansi.Strip(m.statusView(120)); strings.Contains(bar, "copilot") {
		t.Fatalf("pill survived a failed fetch: %q", bar)
	}

	// the tick message triggers the next fetch
	_, cmd = m.Update(copilotQuotaTickMsg{})
	if cmd == nil {
		t.Fatal("tick did not schedule a fetch")
	}
}

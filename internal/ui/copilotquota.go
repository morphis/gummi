package ui

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The Copilot quota hint: a quiet status-bar pill showing the percent
// of the account's Copilot allowance left, read via the gh CLI from
// /copilot_internal/user — the undocumented endpoint gh copilot itself
// uses. Because the endpoint is internal, any surprise (gh missing,
// unauthenticated, no Copilot plan, changed payload shape) hides the
// pill; nothing here may ever surface as an error.

// copilotQuotaInterval paces the refresh. Quota moves with agent
// usage, not wall clock, so a slow poll is plenty.
const copilotQuotaInterval = 5 * time.Minute

// copilotQuotaLowPct is the alert threshold: below this the pill
// switches from neutral to the warning color.
const copilotQuotaLowPct = 15

// copilotQuota is the latest reading. ok is false until a fetch
// parses, and the pill stays hidden.
type copilotQuota struct {
	percent float64
	ok      bool
}

// pill renders the status-bar text, e.g. "copilot 90%".
func (q copilotQuota) pill() string {
	return "copilot " + strconv.Itoa(int(math.Round(max(q.percent, 0)))) + "%"
}

// low reports whether the reading deserves the alert color.
func (q copilotQuota) low() bool { return q.ok && q.percent < copilotQuotaLowPct }

// copilotQuotaMsg carries a reading (or its absence) into Update.
type copilotQuotaMsg struct {
	quota copilotQuota
	retry bool // keep polling; false means gh is missing, stop for good
}

// copilotQuotaTickMsg fires the next scheduled refresh.
type copilotQuotaTickMsg struct{}

func copilotQuotaTick() tea.Cmd {
	return subscription(tea.Tick(copilotQuotaInterval, func(time.Time) tea.Msg { return copilotQuotaTickMsg{} }))
}

// fetchCopilotQuota reads the quota off the render loop. m.ghCopilotUser
// is the test seam; when nil the real gh CLI runs (and a missing binary
// ends the poll loop rather than retrying a permanent condition).
func (m *Shell) fetchCopilotQuota() tea.Cmd {
	run := m.ghCopilotUser
	return func() tea.Msg {
		if run == nil {
			if _, err := exec.LookPath("gh"); err != nil {
				return copilotQuotaMsg{}
			}
			run = ghCopilotUser
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := run(ctx)
		if err != nil {
			// auth or network trouble may heal (e.g. gh auth login in
			// another terminal), so keep the slow poll alive
			return copilotQuotaMsg{retry: true}
		}
		pct, ok := parseCopilotQuota(out)
		return copilotQuotaMsg{quota: copilotQuota{percent: pct, ok: ok}, retry: true}
	}
}

func ghCopilotUser(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", "api", "/copilot_internal/user").Output()
}

// parseCopilotQuota picks the binding percent_remaining out of the
// quota_snapshots map. The snapshot keys are mid-rename at GitHub
// (premium requests → AI tokens/credits), so no key is hardcoded:
// token-billed snapshots win over legacy request-billed ones, and
// within a class the smallest remainder — the constraint the user will
// actually hit — is reported. Unlimited and quota-less snapshots never
// produce a pill.
func parseCopilotQuota(raw []byte) (float64, bool) {
	var payload struct {
		QuotaSnapshots map[string]struct {
			PercentRemaining float64 `json:"percent_remaining"`
			Unlimited        bool    `json:"unlimited"`
			HasQuota         bool    `json:"has_quota"`
			TokenBilling     bool    `json:"token_based_billing"`
		} `json:"quota_snapshots"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, false
	}
	var tokenMin, legacyMin float64
	var tokenOK, legacyOK bool
	for _, s := range payload.QuotaSnapshots {
		if !s.HasQuota || s.Unlimited {
			continue
		}
		if s.TokenBilling {
			if !tokenOK || s.PercentRemaining < tokenMin {
				tokenMin, tokenOK = s.PercentRemaining, true
			}
		} else if !legacyOK || s.PercentRemaining < legacyMin {
			legacyMin, legacyOK = s.PercentRemaining, true
		}
	}
	if tokenOK {
		return tokenMin, true
	}
	return legacyMin, legacyOK
}

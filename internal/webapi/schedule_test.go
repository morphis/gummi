package webapi

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
)

var scheduleAt = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

// TestScheduleShapes pins the field names the page reads; a rename is a
// broken page and has to show up here as a golden diff first.
func TestScheduleShapes(t *testing.T) {
	envelope := 50
	golden.RequireEqual(t, marshal(t, struct {
		List    Schedules              `json:"list"`
		Create  ScheduleRequest        `json:"create"`
		Edit    ScheduleRequest        `json:"edit"`
		Run     ScheduleFire           `json:"run"`
		Forced  ScheduleFire           `json:"forced"`
		Ask     SchedulePreviewRequest `json:"ask"`
		Preview SchedulePreview        `json:"preview"`
		Refused SchedulePreview        `json:"refused"`
	}{
		List: Schedules{Schedules: []Schedule{{
			ID: "nightly", Name: "nightly triage", Kind: "mint",
			Cron: "0 5 * * *", Timezone: "America/New_York", Prompt: "triage new issues",
			Backend: "claude", Model: "claude-sonnet", Envelope: 50,
			Enabled: true, RunRequested: true,
			LastRun: scheduleAt.Add(-time.Hour), NextRun: scheduleAt,
			LastStatus: "ok", LastDetail: "", LastCard: "FF-012", OrphanCard: "FF-014",
			CreatedAt: scheduleAt.Add(-26 * time.Hour),
		}, {
			ID: "hourly", Name: "hourly", Kind: "heartbeat",
			Target: "FF-007",
			Cron:   "0 * * * *", Prompt: "check CI, keep going",
			Enabled: false,
		}}},
		Create: ScheduleRequest{
			Name: "nightly triage", Kind: "mint", Repo: "gummi", Every: "1h",
			Timezone: "America/New_York", Prompt: "triage new issues",
			Backend: "claude", Model: "claude-sonnet", Envelope: &envelope,
		},
		Edit: ScheduleRequest{Cron: "0 6 * * *", Prompt: "triage new issues first"},
		Run: ScheduleFire{
			ID: "hourly", Name: "hourly", Kind: "heartbeat",
			Status: "ok", Card: "FF-007", At: scheduleAt,
		},
		Forced: ScheduleFire{
			ID: "nightly", Name: "nightly triage", Kind: "mint", Forced: true,
			Status: "paused-exhausted", Detail: "FF-012 has spent its envelope of 50 credits; raise it to carry on",
			Card: "FF-012", Orphan: "FF-012", At: scheduleAt,
		},
		Ask: SchedulePreviewRequest{
			Every: "1h", Timezone: "America/New_York", Backend: "claude", Model: "claude-sonnet",
		},
		Preview: SchedulePreview{
			Cron: "0 * * * *", Fires: []time.Time{scheduleAt, scheduleAt.Add(time.Hour), scheduleAt.Add(2 * time.Hour)},
			EnvelopeHint: "the envelope caps what one minted card may spend",
		},
		Refused: SchedulePreview{Error: "invalid cron \"0 0 30 2 *\": the day-of-month never exists in the months given"},
	}))
}

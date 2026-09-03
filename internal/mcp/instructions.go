package mcp

import "fmt"

// WorkspaceInstructions is the initialize "instructions" content for a
// board-level session (`gummi __mcp --workspace`): a hosted agent tab
// driving the same gummi process it lives inside, rather than a scripted
// per-card stage turn. It exists because that hosted CLI otherwise has a
// socket path and no way to know what it is for — see
// TestBuildGummiMCPServerConfigWorkspace and HostedMCPAttach for the wiring
// that connects it here.
func WorkspaceInstructions() string {
	return "This connection bridges gummi's board-level engine, the same process the parent TUI is driving. " +
		"The parent TUI holds every card's per-card lock for as long as it runs, so never shell out to a second " +
		"`gummi run` or `gummi resume` for any card — it would contend for a lock already held and fail " +
		"guaranteed. board_list, card_status, card_spec, and card_diff are read-only and safe to call anytime. " +
		"card_run, card_resume, and card_new are the safe way to drive a card through this same engine instead. " +
		"Your role here is to assist whoever is at the board, not to replace its own driving loop."
}

// FeatureInstructions is the initialize "instructions" content for a
// per-feature stage session (`gummi __mcp --feature <id>`): the scripted
// turn a stage adapter (claude/codex/opencode/zz) runs while implementing,
// reviewing, or verifying one card.
func FeatureInstructions(featureID string) string {
	return fmt.Sprintf(
		"This connection is card %s's own stage session. The engine already holds that card's lock for the "+
			"life of this session, so never shell out to a second `gummi run` or `gummi resume` for this same "+
			"card. The tool set on this connection depends on the current stage — call tools/list to see what "+
			"is live right now.",
		featureID)
}

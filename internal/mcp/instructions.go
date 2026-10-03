package mcp

import "fmt"

// FeatureInstructions is the initialize "instructions" content for a
// per-feature stage session (`gummi __mcp --feature <id>`): the scripted
// turn a stage adapter (claude/codex/opencode) runs while implementing,
// reviewing, or verifying one card.
func FeatureInstructions(featureID string) string {
	return fmt.Sprintf(
		"This connection is card %s's own stage session. The engine already holds that card's lock for the "+
			"life of this session, so never shell out to a second `gummi run` or `gummi resume` for this same "+
			"card. The tool set on this connection depends on the current stage — call tools/list to see what "+
			"is live right now.",
		featureID)
}

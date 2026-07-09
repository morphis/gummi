package domain

// Check is one named verify command, run from the worktree root. Checks
// live in the design artifact's Verification section as a fenced
// ```gummi-checks``` block (a YAML list): auto-discovered into the spec
// at approval, editable like any other spec content, and executed by
// the Verify stage (DESIGN §3, decision 7).
type Check struct {
	Name string `yaml:"name"`
	Cmd  string `yaml:"cmd"`
}

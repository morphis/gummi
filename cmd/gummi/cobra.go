package main

import (
	"github.com/spf13/cobra"

	"github.com/morphis/gummi/internal/driver"
)

// This file wires the top-level commands and their nested subcommands onto
// the cobra tree. Each is a thin adapter in the "Option A" shape: cobra owns
// routing, help, flags, and completion; the underlying runXxx(args []string)
// error implementations are unchanged and are re-entered with the flag slice
// buildFlagArgs reconstructs from the parsed cobra flags.

// runCmd implements `gummi run [flags] "<description>"`.
var runCmd = &cobra.Command{
	Use:   "run [flags] \"<description>\"",
	Short: "Headlessly drive one feature to a verified branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRun(buildFlagArgs(cmd, args))
	},
}

// resumeCmd implements `gummi resume <id|ref> [decision]`.
var resumeCmd = &cobra.Command{
	Use:   "resume <id|ref> [decision]",
	Short: "Rehydrate a parked feature and drive it on",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResume(buildFlagArgs(cmd, args))
	},
}

// verifyCmd implements `gummi verify <id|ref>`.
var verifyCmd = &cobra.Command{
	Use:   "verify <id|ref>",
	Short: "Re-run the checks on a verified branch and finalize its card",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runVerify(buildFlagArgs(cmd, args))
	},
}

// statusCmd implements `gummi status <id|ref> [--json]`.
var statusCmd = &cobra.Command{
	Use:   "status <id|ref> [--json]",
	Short: "Show a feature's stage, spend, and branch state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(buildFlagArgs(cmd, args))
	},
}

// specCmd implements `gummi spec <id|ref>`.
var specCmd = &cobra.Command{
	Use:   "spec <id|ref>",
	Short: "Dump a work item's current design artifact",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpec(buildFlagArgs(cmd, args))
	},
}

// diffCmd implements `gummi diff <id|ref>`.
var diffCmd = &cobra.Command{
	Use:   "diff <id|ref>",
	Short: "Dump a feature's worktree diff against main",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDiff(buildFlagArgs(cmd, args))
	},
}

// doctorCmd implements `gummi doctor [--json]`.
var doctorCmd = &cobra.Command{
	Use:   "doctor [--json]",
	Short: "Run a readiness checklist for the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDoctor(buildFlagArgs(cmd, args))
	},
}

// ingestCmd implements `gummi ingest [flags] <spec-file>`.
var ingestCmd = &cobra.Command{
	Use:   "ingest [flags] <spec-file>",
	Short: "Decompose a spec into feature proposals and materialize them",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIngest(buildFlagArgs(cmd, args))
	},
}

// bugsCmd groups bug ingestion (ingest) and manual entry (new).
var bugsCmd = &cobra.Command{
	Use:   "bugs",
	Short: "Ingest and manage bugs",
}

var bugsIngestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Import open bugs from a GitHub repo",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBugIngest(buildFlagArgs(cmd, args))
	},
}

var bugsNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create one bug by hand",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBugNew(buildFlagArgs(cmd, args))
	},
}

// skillCmd groups the skill file operations.
var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Show, install, or list the gummi agent skill",
}

var skillShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the rendered SKILL.md",
	RunE: func(cmd *cobra.Command, args []string) error {
		return skillShow(buildFlagArgs(cmd, args))
	},
}

var skillInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the SKILL.md for an agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		return skillInstall(buildFlagArgs(cmd, args))
	},
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "Report each install target's state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return skillList(buildFlagArgs(cmd, args))
	},
}

func init() {
	bindRunFlags(runCmd)
	bindResumeFlags(resumeCmd)
	statusCmd.Flags().Bool("json", false, "emit machine-readable JSON instead of the text summary")
	doctorCmd.Flags().Bool("json", false, "emit the readiness checklist as JSON (the skill's setup path)")

	ingestCmd.Flags().String("profile", "", "profile the new features adopt (default: first configured)")
	ingestCmd.Flags().Int("envelope", 0, "credit envelope per feature (0 = none; falls back to GUMMI_ENVELOPE)")
	ingestCmd.Flags().Bool("yes", false, "materialize without the confirmation prompt")

	bindBugsIngestFlags(bugsIngestCmd)
	bindBugsNewFlags(bugsNewCmd)
	bindSkillInstallFlags(skillInstallCmd)

	bugsCmd.AddCommand(bugsIngestCmd, bugsNewCmd)
	skillCmd.AddCommand(skillShowCmd, skillInstallCmd, skillListCmd)
}

// bindRunFlags mirrors the flags registerRunFlags defines on runRun's
// FlagSet, so cobra parses the same surface (runRun still re-parses the
// reconstructed slice, and the SKILL grammar stays sourced from
// registerRunFlags).
func bindRunFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Int("envelope", 0, "credit envelope for the feature (required; falls back to GUMMI_ENVELOPE)")
	f.String("profile", "", "profile mapping roles to models (default: first configured)")
	f.Bool("full", false, "run the full route (brainstorm + plan), not the quick route")
	f.String("gate-approval", driver.GateAuto, "who approves design gates: auto|caller (persisted on the card; resume keeps it)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	f.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream and persisted for status/resume lookup")
	f.String("acceptance", "", "acceptance criteria to seed the spec draft's Verification plan (a file path, or - for stdin)")
	f.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)")
}

// bindResumeFlags mirrors registerResumeFlags.
func bindResumeFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("answer", "", "answer a delegated ask_user question")
	f.Int("envelope", 0, "raise the credit envelope before resuming (required to clear an exhausted stage; never lowers it)")
	f.Bool("approve", false, "approve a caller design gate")
	f.String("request-changes", "", "send a caller design gate back with a note")
	f.Bool("bounce", false, "rewind a verify/review-fail escalation to the work stage and continue (the TUI's b key)")
	f.String("note", "", "addendum to the reborn implement/fix kickoff (used with --bounce)")
	f.String("gate-approval", driver.GateAuto, "who approves later design gates: auto|caller (inherits the run's mode when omitted; pass to change it)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	f.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream")
	f.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)")
}

// bindBugsIngestFlags mirrors runBugIngest's flag set.
func bindBugsIngestFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("repo", "", "owner/repo to import from (default: this repo's origin remote)")
	f.String("label", "bug", "issue label filter (\"\" imports all issues)")
	f.String("state", "open", "issue state: open|closed|all")
	f.String("profile", "", "profile the new bugs adopt (default: first configured)")
	f.Int("envelope", 0, "credit envelope per bug (0 = none; falls back to GUMMI_ENVELOPE)")
	f.Bool("yes", false, "materialize without the confirmation prompt")
	f.Bool("comments", false, "fetch issue comments into the report's Discussion section")
}

// bindBugsNewFlags mirrors runBugNew's flag set.
func bindBugsNewFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("title", "", "bug title (required)")
	f.String("one-liner", "", "short one-line summary")
	f.String("severity", "", "severity: critical|high|medium|low")
	f.String("repro", "", "reproduction steps")
	f.String("expected", "", "expected behavior")
	f.String("actual", "", "actual behavior")
	f.String("env", "", "environment (versions, OS, config)")
	f.String("desc", "", "summary of what's broken")
	f.String("profile", "", "profile the bug adopts (default: first configured)")
	f.Int("envelope", 0, "credit envelope (0 = none; falls back to GUMMI_ENVELOPE)")
	f.Bool("yes", false, "create without the confirmation prompt")
}

// bindSkillInstallFlags mirrors skillInstall's flag set.
func bindSkillInstallFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("agent", "", "target a specific agent: claude|codex|opencode|copilot (default: detect)")
	f.String("scope", "", "install scope: project|user (default: project, or ask when interactive)")
	f.Bool("force", false, "overwrite an existing SKILL.md (default: refuse and warn on drift)")
	f.Bool("dry-run", false, "print what would be written, change nothing")
}

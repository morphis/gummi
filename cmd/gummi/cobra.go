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

// researchCmd implements `gummi research [flags] "<brief>"`.
var researchCmd = &cobra.Command{
	Use:   "research [flags] \"<brief>\"",
	Short: "Headlessly drive one research card through decompose",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResearch(buildFlagArgs(cmd, args))
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

// mergeCmd implements `gummi merge <id|ref> -m <message|->`.
var mergeCmd = &cobra.Command{
	Use:   "merge <id|ref> -m <message|->",
	Short: "Headlessly land a verified branch as one squash commit",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMerge(buildFlagArgs(cmd, args))
	},
}

// squashCmd implements `gummi squash <id|ref> -m <message|->`.
var squashCmd = &cobra.Command{
	Use:   "squash <id|ref> -m <message|->",
	Short: "Collapse a card's branch to one commit, in place",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSquash(buildFlagArgs(cmd, args))
	},
}

// cleanCmd implements `gummi clean <id|ref>`.
var cleanCmd = &cobra.Command{
	Use:   "clean <id|ref>",
	Short: "Remove a landed card's worktree and branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClean(buildFlagArgs(cmd, args))
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

// doctorCmd implements `gummi doctor [--json] [--deep]`.
var doctorCmd = &cobra.Command{
	Use:   "doctor [--json] [--deep]",
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

// depsCmd groups the dependency-edge operations (add/rm/list).
var depsCmd = &cobra.Command{
	Use:   "deps",
	Short: "Manage dependency edges between cards",
}

var depsAddCmd = &cobra.Command{
	Use:   "add <dependent> <depends-on>",
	Short: "Record that a card depends on another",
	RunE: func(_ *cobra.Command, args []string) error {
		return runDepsAdd(args)
	},
}

var depsRmCmd = &cobra.Command{
	Use:   "rm <dependent> <depends-on>",
	Short: "Remove a dependency edge",
	RunE: func(_ *cobra.Command, args []string) error {
		return runDepsRm(args)
	},
}

var depsListCmd = &cobra.Command{
	Use:   "list <id>",
	Short: "List a card's dependencies",
	RunE: func(_ *cobra.Command, args []string) error {
		return runDepsList(args)
	},
}

// prCmd groups the outbound-PR operations (link/unlink/status).
var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Link, unlink, and check the outbound PR a card lands through",
}

var prLinkCmd = &cobra.Command{
	Use:   "link <card> <url|number> [--auto]",
	Short: "Link a card to an existing PR",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRLink(buildFlagArgs(cmd, args))
	},
}

var prUnlinkCmd = &cobra.Command{
	Use:   "unlink <card>",
	Short: "Clear a card's linked PR",
	RunE: func(_ *cobra.Command, args []string) error {
		return runPRUnlink(args)
	},
}

var prStatusCmd = &cobra.Command{
	Use:   "status <card> [--json]",
	Short: "Show a card's linked PR state and comment count",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRStatus(buildFlagArgs(cmd, args))
	},
}

var prCommentsCmd = &cobra.Command{
	Use:   "comments <card> [--ingest] [--json]",
	Short: "List or ingest a linked PR's unresolved review threads as diff annotations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRComments(buildFlagArgs(cmd, args))
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
	bindResearchFlags(researchCmd)
	bindResumeFlags(resumeCmd)
	mergeCmd.Flags().StringP("message", "m", "", "landing commit message (required; - reads from stdin)")
	squashCmd.Flags().StringP("message", "m", "", "collapsed commit message (required; - reads from stdin)")
	squashCmd.Flags().Bool("force", false, "proceed even if the linked PR has open review threads")
	statusCmd.Flags().Bool("json", false, "emit machine-readable JSON instead of the text summary")
	doctorCmd.Flags().Bool("json", false, "emit the readiness checklist as JSON (the skill's setup path)")
	doctorCmd.Flags().Bool("deep", false, "probe per-role model reachability with a live backend turn (TTL-cached)")

	bindIngestFlags(ingestCmd)
	bindBugsIngestFlags(bugsIngestCmd)
	bindBugsNewFlags(bugsNewCmd)
	bindSkillInstallFlags(skillInstallCmd)

	prLinkCmd.Flags().Bool("auto", false, "resolve the PR whose head branch matches the card's branch (via gh pr list --head)")
	prStatusCmd.Flags().Bool("json", false, "emit machine-readable JSON instead of the text summary")
	prCommentsCmd.Flags().Bool("ingest", false, "write an annotation per unresolved review thread onto the card's diff")
	prCommentsCmd.Flags().Bool("json", false, "emit machine-readable JSON instead of the text summary")

	bugsCmd.AddCommand(bugsIngestCmd, bugsNewCmd)
	depsCmd.AddCommand(depsAddCmd, depsRmCmd, depsListCmd)
	prCmd.AddCommand(prLinkCmd, prUnlinkCmd, prStatusCmd, prCommentsCmd)
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
	f.String("repo", "", "managed repository to create the card in (a configured `repos:` name; default: the workspace default repo)")
	f.String("acceptance", "", "acceptance criteria to seed the spec draft's Verification plan (a file path, or - for stdin)")
	f.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)")
}

// bindResearchFlags mirrors the flags registerResearchFlags defines on
// runResearch's FlagSet, so cobra parses the same surface (runResearch
// still re-parses the reconstructed slice, and the SKILL grammar stays
// sourced from registerResearchFlags). No --full or --acceptance: RS has
// no brainstorm/plan and no Verification-plan section to seed.
func bindResearchFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Int("envelope", 0, "credit envelope for the research card (required; falls back to GUMMI_ENVELOPE)")
	f.String("profile", "", "profile mapping roles to models (default: first configured)")
	f.String("gate-approval", driver.GateAuto, "who approves design gates: auto|caller (persisted on the card; resume keeps it)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	f.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream and persisted for status/resume lookup")
	f.String("repo", "", "managed repository to create the card in (a configured `repos:` name; default: the workspace default repo)")
	f.String("until", "", `stop cleanly before crossing the gate that leaves this stage (only "shape" is a valid stop on RS's route)`)
}

// bindIngestFlags mirrors the flags registerIngestFlags defines on
// runIngest's FlagSet, so cobra parses the same surface (runIngest still
// re-parses the reconstructed slice).
func bindIngestFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("profile", "", "profile the new features adopt (default: first configured)")
	f.Int("envelope", 0, "credit envelope per feature (0 = none; falls back to GUMMI_ENVELOPE)")
	f.Bool("yes", false, "materialize without the confirmation prompt")
	f.String("repo", "", "managed repository to create the cards in (a configured `repos:` name; default: the workspace default repo)")
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
	f.String("target-repo", "", "managed repository to create the bugs in (a configured `repos:` name; default: the workspace default repo)")
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
	f.String("repo", "", "managed repository to create the bug in (a configured `repos:` name; default: the workspace default repo)")
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

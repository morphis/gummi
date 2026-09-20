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
	Short: "Headlessly drive one card to a verified branch",
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

// diagnoseCmd implements `gummi diagnose [flags] "<symptom>"`.
var diagnoseCmd = &cobra.Command{
	Use:   "diagnose [flags] \"<symptom>\"",
	Short: "Headlessly drive one diagnosis card through decompose",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDiagnose(buildFlagArgs(cmd, args))
	},
}

// resumeCmd implements `gummi resume <id|ref> [decision]`.
var resumeCmd = &cobra.Command{
	Use:   "resume <id|ref> [decision]",
	Short: "Pick a parked card back up and drive it on",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResume(resumeArgv(cmd, args))
	},
}

// resumeArgv is buildFlagArgs plus one exception for `resume`:
// buildFlagArgs drops a flag whose explicit value equals its cobra default,
// since it can't tell "never passed" from "passed the default value" apart.
// --gate-approval attended is a legitimate no-op re-affirmation (it
// overrides a persisted autopilot mode), so re-add it here when that's
// exactly what buildFlagArgs dropped. The exception lives at this one call site rather
// than in buildFlagArgs itself, which stays generic for the ~20 other
// commands routed through it.
func resumeArgv(cmd *cobra.Command, args []string) []string {
	argv := buildFlagArgs(cmd, args)
	if f := cmd.Flags().Lookup("gate-approval"); f != nil && f.Changed && f.Value.String() == f.DefValue {
		argv = append([]string{"--gate-approval", f.Value.String()}, argv...)
	}
	return argv
}

// goalCmd implements `gummi goal [flags] "<objective>"`.
var goalCmd = &cobra.Command{
	Use:   `goal [flags] "<objective>"`,
	Short: "Agree a goal, then let it run its cards on one branch until it is ready for you",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGoal(buildFlagArgs(cmd, args))
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

// handoffCmd implements `gummi handoff <id|ref>`.
var handoffCmd = &cobra.Command{
	Use:   "handoff <id|ref>",
	Short: "Close a verified card and keep its branch — nothing lands",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHandOff(buildFlagArgs(cmd, args))
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

// commitCmd implements `gummi commit <id|ref> -m <message|->`.
var commitCmd = &cobra.Command{
	Use:   "commit <id|ref> -m <message|->",
	Short: "Commit a card's own uncommitted worktree changes onto its branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCommit(buildFlagArgs(cmd, args))
	},
}

// statusCmd implements `gummi status <id|ref> [--json] [--stats]`.
var statusCmd = &cobra.Command{
	Use:   "status <id|ref> [--json] [--stats]",
	Short: "Show a card's stage, spend, and branch state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(buildFlagArgs(cmd, args))
	},
}

// watchCmd implements `gummi watch <id|ref> [--json] [--wait]`.
var watchCmd = &cobra.Command{
	Use:   "watch <id|ref> [--json] [--wait] [--once]",
	Short: "Follow the live agent stream of a card another gummi is driving",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWatch(buildFlagArgs(cmd, args))
	},
}

// specCmd implements `gummi spec <id|ref>`.
var specCmd = &cobra.Command{
	Use:   "spec <id|ref>",
	Short: "Dump a card's current design artifact",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpec(buildFlagArgs(cmd, args))
	},
}

// diffCmd implements `gummi diff <id|ref>`.
var diffCmd = &cobra.Command{
	Use:   "diff <id|ref>",
	Short: "Dump a card's worktree diff against its base branch",
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

// initCmd implements `gummi init`.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create and seed the .gummi workspace in the current directory",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInit(buildFlagArgs(cmd, args))
	},
}

// ingestCmd implements `gummi ingest [flags] <spec-file>`.
var ingestCmd = &cobra.Command{
	Use:   "ingest [flags] <spec-file>",
	Short: "Split a document into cards",
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

// stackCmd groups the stack operations. A stack is a chain of cards
// whose branches fork from one another: slice one piece of work into
// several reviewable branches, land them bottom-first, and let gummi
// replay the ones above whenever a card below them changes.
var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Chain cards so each one's branch forks from the one below it",
}

var stackNewCmd = &cobra.Command{
	Use:   "new <bottom-card>",
	Short: "Start a stack from the card that sits at its bottom",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackNew(args) },
}

var stackAddCmd = &cobra.Command{
	Use:   "add <stack> <card>",
	Short: "Put a card into a stack",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackAdd(args) },
}

var stackRmCmd = &cobra.Command{
	Use:   "rm <card>",
	Short: "Take a card out of its stack",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackRm(args) },
}

var stackMvCmd = &cobra.Command{
	Use:   "mv <card> <position>",
	Short: "Move a card within its stack (0 is the bottom)",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackMv(args) },
}

var stackListCmd = &cobra.Command{
	Use:   "list [<stack>]",
	Short: "List the stacks, or one stack's cards bottom-first",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackList(args) },
}

var stackRestackCmd = &cobra.Command{
	Use:   "restack <stack|card>",
	Short: "Replay every card in a stack onto its current base now",
	RunE:  func(_ *cobra.Command, args []string) error { return runStackRestack(args) },
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
	// diagnose is the same card in the other mode, so it is the same flag
	// surface — bound from the one definition rather than restated.
	bindResearchFlags(diagnoseCmd)
	bindResumeFlags(resumeCmd)
	bindGoalFlags(goalCmd)
	mergeCmd.Flags().StringP("message", "m", "", "landing commit message (required; - reads from stdin)")
	squashCmd.Flags().StringP("message", "m", "", "collapsed commit message (required; - reads from stdin)")
	squashCmd.Flags().Bool("force", false, "proceed even if the linked PR has open review threads")
	commitCmd.Flags().StringP("message", "m", "", "commit message for the card's uncommitted worktree changes (required; - reads from stdin)")
	statusCmd.Flags().Bool("json", false, "emit machine-readable JSON instead of the text summary")
	statusCmd.Flags().Bool("stats", false, "report where the card's credits and hours went instead of where it stands")
	watchCmd.Flags().Bool("json", false, "emit the raw record stream as NDJSON instead of the rendered transcript")
	watchCmd.Flags().Bool("wait", false, "block until the card has a live stream instead of failing when none exists")
	watchCmd.Flags().Bool("once", false, "exit when the current session ends instead of following the card's next one")
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
	stackCmd.AddCommand(stackNewCmd, stackAddCmd, stackRmCmd, stackMvCmd, stackListCmd, stackRestackCmd)
	prCmd.AddCommand(prLinkCmd, prUnlinkCmd, prStatusCmd, prCommentsCmd)
	skillCmd.AddCommand(skillShowCmd, skillInstallCmd, skillListCmd)
}

// bindRunFlags mirrors the flags registerRunFlags defines on runRun's
// FlagSet, so cobra parses the same surface (runRun still re-parses the
// reconstructed slice, and the SKILL grammar stays sourced from
// registerRunFlags).
func bindRunFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Int("envelope", 0, "spend budget for the card, in credits (required; falls back to GUMMI_ENVELOPE)")
	f.String("profile", "", "profile mapping roles to models (default: first configured)")
	f.String("gate-approval", driver.GateAttended, "who crosses this card's gates: attended|autopilot (retired spellings off/gates/caller/full still accepted; persisted on the card; resume keeps it)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	f.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream and persisted for status/resume lookup")
	f.String("repo", "", "managed repository to create the card in (a configured `repos:` name; required when `repos:` is configured)")
	f.String("base", "", "branch the card's work forks from and lands on (default: whatever the repository has checked out)")
	f.String("acceptance", "", "acceptance criteria to seed the spec draft's Verification plan (a file path, or - for stdin)")
	f.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)")
}

// bindResearchFlags mirrors the flags registerResearchFlags defines on
// runResearch's FlagSet, so cobra parses the same surface (runResearch
// still re-parses the reconstructed slice, and the SKILL grammar stays
// sourced from registerResearchFlags). No --acceptance: RS has no
// Verification-plan section to seed.
func bindResearchFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Int("envelope", 0, "spend budget for the research card, in credits (required; falls back to GUMMI_ENVELOPE)")
	f.String("profile", "", "profile mapping roles to models (default: first configured)")
	f.String("gate-approval", driver.GateAttended, "who crosses this card's gates: attended|autopilot (retired spellings off/gates/caller/full still accepted; persisted on the card; resume keeps it)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	f.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream and persisted for status/resume lookup")
	f.String("repo", "", "managed repository to create the card in (a configured `repos:` name; required when `repos:` is configured)")
	f.String("base", "", "branch the card's work forks from and lands on (default: whatever the repository has checked out)")
	f.String("until", "", `stop cleanly before crossing the gate that leaves this stage (only "shape" is a valid stop on RS's route)`)
}

// bindIngestFlags mirrors the flags registerIngestFlags defines on
// runIngest's FlagSet, so cobra parses the same surface (runIngest still
// re-parses the reconstructed slice).
func bindIngestFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("profile", "", "profile the new features adopt (default: first configured)")
	f.Int("envelope", 0, "spend budget per card, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	f.Bool("yes", false, "materialize without the confirmation prompt")
	f.String("repo", "", "managed repository to create the cards in (a configured `repos:` name; required when `repos:` is configured)")
}

// bindGoalFlags mirrors registerGoalFlags.
func bindGoalFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Int("envelope", 0, "the goal's whole budget in credits — its cards, its lead and its own review all spend inside it (required; falls back to GUMMI_ENVELOPE)")
	f.String("profile", "", "profile mapping roles to models, the lead included (default: first configured)")
	f.String("gate-approval", driver.GateAttended, "who approves the goal's plan: attended|autopilot (past its plan a goal always runs itself)")
	f.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout for the goal and each of its cards (0 disables)")
	f.Bool("autonomous", false, "let the architect take its recommended answer instead of asking during the plan conversation")
	f.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	f.String("ref", "", "external correlation id, echoed in the stream and persisted for `status`/`resume` lookup")
	f.String("base", "", "branch the goal branch forks from and lands on in the goal's home repository (default: whatever it has checked out)")
	f.String("plan-file", "", "a complete goal doc to start the plan conversation from (a file path, or - for stdin)")
	f.String("after", "", "the goal this one continues (GL-NNN): what it came to know — reference, decided constants, findings, its hand-over — comes with it, and this goal's plan cannot be approved until that one has landed")
	f.String("reference", "", "documents the goal is agreed against — a design, a table, a spec — as comma-separated paths; copied into the goal's notebook, pinned at the plan gate, and listed in every card's kickoff")
	f.String("until", "", "stop cleanly before the goal's plan is approved (only \"plan\" is a valid stop)")
}

// bindResumeFlags mirrors registerResumeFlags.
func bindResumeFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("goal-note", "", "goals: add a note to a running goal; its lead reads it on its next turn")
	f.String("reverse", "", "goals: reverse a decision for review (D-N) and send the goal back; --request-changes adds why")
	f.Bool("wrap-up", false, "goals: finish now — nothing new starts, verified work lands, the rest is dropped")
	f.Int("runs", 0, "goals: raise the substrate budget to this many experiment runs before resuming (never lowers it)")
	f.Int("minutes", 0, "goals: raise the substrate budget to this many substrate minutes before resuming (never lowers it)")
	f.String("answer", "", "answer a delegated ask_user question")
	f.Int("envelope", 0, "raise the spend budget before resuming, in credits (required to clear a card that ran out; never lowers it)")
	f.Bool("approve", false, "approve a design gate handed back by --gate-approval=attended")
	f.String("request-changes", "", "send a design gate back with a note")
	f.Bool("bounce", false, "rewind one rerun edge — a verify-fail escalation to the work stage, an implement-stage card back to plan — and continue (the TUI's b key)")
	f.String("note", "", "addendum to the reborn stage's kickoff (used with --bounce)")
	f.String("say", "", "read a line the way the card page would and report what it would do, as a `say` event, without acting")
	f.String("gate-approval", driver.GateAttended, "who crosses this card's later gates: attended|autopilot (retired spellings still accepted; inherits the run's mode when omitted; pass to change it)")
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
	f.String("target-repo", "", "managed repository to create the bugs in (a configured `repos:` name; required when `repos:` is configured)")
	f.String("label", "bug", "issue label filter (\"\" imports all issues)")
	f.String("state", "open", "issue state: open|closed|all")
	f.String("profile", "", "profile the new bugs adopt (default: first configured)")
	f.Int("envelope", 0, "spend budget per bug, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	f.Int("issue", 0, "import exactly this GitHub issue number from the fetched set (0 = batch import, all fresh proposals)")
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
	f.Int("envelope", 0, "spend budget, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	f.String("repo", "", "managed repository to create the bug in (a configured `repos:` name; required when `repos:` is configured)")
	f.String("base", "", "branch the fix forks from and lands on (default: whatever the repository has checked out)")
	f.Bool("yes", false, "create without the confirmation prompt")
}

// bindSkillInstallFlags mirrors skillInstall's flag set.
func bindSkillInstallFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("agent", "", "target a specific agent: claude|codex|opencode|copilot (default: detect)")
	f.String("scope", "", "install scope: project|user (default: project, or ask when interactive)")
	f.Bool("force", false, "overwrite an existing SKILL.md (default: refuse and warn on drift)")
	f.Bool("dry-run", false, "print what would be written, change nothing")
	f.Bool("check", false, "verify every target is up to date; write nothing, fail if any is absent/foreign/drifted")
}

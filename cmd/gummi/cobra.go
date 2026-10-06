package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/morphis/gummi/internal/domain"
)

// This file wires the top-level commands and their nested subcommands onto
// the cobra tree. Cobra owns routing, help, completion AND parsing: each
// command's flags are declared once, in the bind functions at the bottom,
// and its RunE hands the body a cliFlags view of what cobra parsed. See
// flags.go for why there is exactly one declaration site.

// runCmd implements `gummi run [flags] "<description>"`.
var runCmd = &cobra.Command{
	Use:   "run [flags] \"<description>\"",
	Short: "Headlessly drive one card to a verified branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRun(cmdFlags(cmd), args)
	},
}

// researchCmd implements `gummi research [flags] "<brief>"`.
var researchCmd = &cobra.Command{
	Use:   "research [flags] \"<brief>\"",
	Short: "Headlessly drive one research card through decompose",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResearchCard(cmdFlags(cmd), args, domain.CardType{Kind: domain.KindResearch}, "brief")
	},
}

// diagnoseCmd implements `gummi diagnose [flags] "<symptom>"`.
var diagnoseCmd = &cobra.Command{
	Use:   "diagnose [flags] \"<symptom>\"",
	Short: "Headlessly drive one diagnosis card through decompose",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResearchCard(cmdFlags(cmd), args,
			domain.CardType{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}, "symptom")
	},
}

// resumeCmd implements `gummi resume <id|ref> [decision]`.
var resumeCmd = &cobra.Command{
	Use:   "resume <id|ref> [decision]",
	Short: "Pick a parked card back up and drive it on",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runResume(cmdFlags(cmd), args)
	},
}

// goalCmd implements `gummi goal [flags] "<objective>"`.
var goalCmd = &cobra.Command{
	Use:   `goal [flags] "<objective>"`,
	Short: "Agree a goal, then let it run its cards on one branch until it is ready for you",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGoal(cmdFlags(cmd), args)
	},
}

// verifyCmd implements `gummi verify <id|ref>`.
var verifyCmd = &cobra.Command{
	Use:   "verify <id|ref>",
	Short: "Re-run the checks on a verified branch and finalize its card",
	RunE:  func(_ *cobra.Command, args []string) error { return runVerify(args) },
}

// mergeCmd implements `gummi merge <id|ref> -m <message|->`.
var mergeCmd = &cobra.Command{
	Use:   "merge <id|ref> -m <message|->",
	Short: "Headlessly land a verified branch as one squash commit",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMerge(cmdFlags(cmd), args)
	},
}

// squashCmd implements `gummi squash <id|ref> -m <message|->`.
var squashCmd = &cobra.Command{
	Use:   "squash <id|ref> -m <message|->",
	Short: "Collapse a card's branch to one commit, in place",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSquash(cmdFlags(cmd), args)
	},
}

// handoffCmd implements `gummi handoff <id|ref>`.
var handoffCmd = &cobra.Command{
	Use:   "handoff <id|ref>",
	Short: "Close a verified card and keep its branch — nothing lands",
	RunE:  func(_ *cobra.Command, args []string) error { return runHandOff(args) },
}

// cleanCmd implements `gummi clean <id|ref>`.
var cleanCmd = &cobra.Command{
	Use:   "clean <id|ref>",
	Short: "Remove a landed card's worktree and branch",
	RunE:  func(_ *cobra.Command, args []string) error { return runClean(args) },
}

// commitCmd implements `gummi commit <id|ref> -m <message|->`.
var commitCmd = &cobra.Command{
	Use:   "commit <id|ref> -m <message|->",
	Short: "Commit a card's own uncommitted worktree changes onto its branch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCommit(cmdFlags(cmd), args)
	},
}

// statusCmd implements `gummi status <id|ref> [--json] [--stats]`.
var statusCmd = &cobra.Command{
	Use:   "status <id|ref> [--json] [--stats]",
	Short: "Show a card's stage, spend, and branch state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmdFlags(cmd), args)
	},
}

// watchCmd implements `gummi watch <id|ref> [--json] [--wait] [--once]`.
var watchCmd = &cobra.Command{
	Use:   "watch <id|ref> [--json] [--wait] [--once]",
	Short: "Follow the live agent stream of a card another gummi is driving",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWatch(cmdFlags(cmd), args)
	},
}

// specCmd implements `gummi spec <id|ref>`.
var specCmd = &cobra.Command{
	Use:   "spec <id|ref>",
	Short: "Dump a card's current design artifact",
	RunE:  func(_ *cobra.Command, args []string) error { return runSpec(args) },
}

// diffCmd implements `gummi diff <id|ref>`.
var diffCmd = &cobra.Command{
	Use:   "diff <id|ref>",
	Short: "Dump a card's worktree diff against its base branch",
	RunE:  func(_ *cobra.Command, args []string) error { return runDiff(args) },
}

// logCmd implements `gummi log <id|ref> [--json]`.
var logCmd = &cobra.Command{
	Use:   "log <id|ref>",
	Short: "List a card's own commits, oldest first",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLog(cmdFlags(cmd), args)
	},
}

// rewriteCmd implements `gummi rewrite <id|ref> --plan <file|->`.
var rewriteCmd = &cobra.Command{
	Use:   "rewrite <id|ref> --plan <file|->",
	Short: "Reword or squash a card's commits in place; its content never changes",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRewrite(cmdFlags(cmd), args)
	},
}

// doctorCmd implements `gummi doctor [--json] [--deep]`.
var doctorCmd = &cobra.Command{
	Use:   "doctor [--json] [--deep]",
	Short: "Run a readiness checklist for the workspace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDoctor(cmdFlags(cmd))
	},
}

// initCmd implements `gummi init`.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create and seed the .gummi workspace in the current directory",
	RunE:  func(_ *cobra.Command, _ []string) error { return runInit() },
}

// ingestCmd implements `gummi ingest [flags] <spec-file>`.
var ingestCmd = &cobra.Command{
	Use:   "ingest [flags] <spec-file>",
	Short: "Split a document into cards",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIngest(cmdFlags(cmd), args)
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
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBugIngest(cmdFlags(cmd))
	},
}

var bugsNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create one bug by hand",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBugNew(cmdFlags(cmd))
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
	RunE:  func(_ *cobra.Command, args []string) error { return runDepsAdd(args) },
}

var depsRmCmd = &cobra.Command{
	Use:   "rm <dependent> <depends-on>",
	Short: "Remove a dependency edge",
	RunE:  func(_ *cobra.Command, args []string) error { return runDepsRm(args) },
}

var depsListCmd = &cobra.Command{
	Use:   "list <id>",
	Short: "List a card's dependencies",
	RunE:  func(_ *cobra.Command, args []string) error { return runDepsList(args) },
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
	Use:   "new <bottom-card> [--name <name>]",
	Short: "Start a stack from the card that sits at its bottom",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStackNew(cmdFlags(cmd), args)
	},
}

var stackAddCmd = &cobra.Command{
	Use:   "add <stack> <card> [--pos N]",
	Short: "Put a card into a stack",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStackAdd(cmdFlags(cmd), args)
	},
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

// scheduleCmd groups the timed triggers for freeform sessions
// (DESIGN §19.9): a schedule mints a freeform card on a cron cadence, a
// heartbeat sends a recurring turn into one session. Store verbs — they
// run with or without a board; none of them fires a session from this
// process.
var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Schedules and heartbeats — freeform sessions that come back on a clock",
}

var scheduleListCmd = &cobra.Command{
	Use:   "list [--json]",
	Short: "List the schedules and their last outcome",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScheduleList(cmdFlags(cmd), args)
	},
}

var scheduleAddCmd = &cobra.Command{
	Use:   `add --name <name> (--cron "<cron>"|--every <preset>) --prompt "<prompt>" (--envelope N | --heartbeat <FF-id>)`,
	Short: "Define a schedule or a heartbeat, stored off until enabled",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScheduleAdd(cmdFlags(cmd), args)
	},
}

var scheduleEnableCmd = &cobra.Command{
	Use:   "enable <id|name>",
	Short: "Turn a schedule on; its first fire is computed from now",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScheduleEnable(cmdFlags(cmd), args)
	},
}

var scheduleDisableCmd = &cobra.Command{
	Use:   "disable <id|name>",
	Short: "Turn a schedule off",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScheduleDisable(cmdFlags(cmd), args)
	},
}

var scheduleRunNowCmd = &cobra.Command{
	Use:   "run-now <id|name>",
	Short: "Ask the running board to fire a schedule once, off-cadence",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScheduleRunNow(cmdFlags(cmd), args)
	},
}

var scheduleRmCmd = &cobra.Command{
	Use:   "rm <id|name>",
	Short: "Delete a schedule (the cards it minted stay)",
	RunE:  func(_ *cobra.Command, args []string) error { return runScheduleRm(args) },
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
		return runPRLink(cmdFlags(cmd), args)
	},
}

var prUnlinkCmd = &cobra.Command{
	Use:   "unlink <card>",
	Short: "Clear a card's linked PR",
	RunE:  func(_ *cobra.Command, args []string) error { return runPRUnlink(args) },
}

var prStatusCmd = &cobra.Command{
	Use:   "status <card> [--json]",
	Short: "Show a card's linked PR state and comment count",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRStatus(cmdFlags(cmd), args)
	},
}

var prCommentsCmd = &cobra.Command{
	Use:   "comments <card> [--ingest] [--json]",
	Short: "List or ingest a linked PR's unresolved review threads as diff annotations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRComments(cmdFlags(cmd), args)
	},
}

// skillCmd groups the skill file operations.
var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Show, install, or list the gummi agent skill",
}

var skillShowCmd = &cobra.Command{
	Use:   "show [<file>]",
	Short: "Print the rendered SKILL.md, or one of its reference files",
	RunE:  func(_ *cobra.Command, args []string) error { return skillShow(args) },
}

var skillInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the skill bundle for an agent",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return skillInstall(cmdFlags(cmd))
	},
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "Report each install target's state",
	RunE:  func(_ *cobra.Command, _ []string) error { return skillList() },
}

// webCmd implements `gummi web`: the board in a browser, hosted by this
// process with the TUI's own model running without a screen.
var webCmd = &cobra.Command{
	Use:   "web [--addr host:port] [--allow-host names] [--tls-cert file --tls-key file] [--tailscale [--ts-hostname name] [--ts-authkey key] [--ts-tls] [--verbose]] [--no-pairing]",
	Short: "Serve the board to a browser",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWeb(cmdFlags(cmd), args)
	},
}

var webPairCmd = &cobra.Command{
	Use:   "pair [--name person]",
	Short: "Print a pairing code from the running web board",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runWebPair(cmdFlags(cmd))
	},
}

var webDevicesCmd = &cobra.Command{
	Use:   "devices [--json]",
	Short: "List the browsers paired with this board, and any waiting to be let in",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runWebDevices(cmdFlags(cmd))
	},
}

var webUnpairCmd = &cobra.Command{
	Use:   "unpair <id> | --all",
	Short: "Revoke a paired browser, or withdraw one waiting to be let in",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWebUnpair(cmdFlags(cmd), args)
	},
}

func init() {
	bindRunFlags(runCmd.Flags())
	bindResearchFlags(researchCmd.Flags())
	// diagnose is the same card in the other mode, so it is the same flag
	// surface — bound from the one definition rather than restated.
	bindResearchFlags(diagnoseCmd.Flags())
	bindGoalFlags(goalCmd.Flags())
	bindResumeFlags(resumeCmd.Flags())
	bindMergeFlags(mergeCmd.Flags())
	bindSquashFlags(squashCmd.Flags())
	bindCommitFlags(commitCmd.Flags())
	jsonFlag(logCmd.Flags(), "emit the commits as JSON (the shape the board's web page reads)")
	bindRewriteFlags(rewriteCmd.Flags())
	bindStatusFlags(statusCmd.Flags())
	bindWatchFlags(watchCmd.Flags())
	bindDoctorFlags(doctorCmd.Flags())
	bindIngestFlags(ingestCmd.Flags())
	bindBugsIngestFlags(bugsIngestCmd.Flags())
	bindBugsNewFlags(bugsNewCmd.Flags())
	bindStackNewFlags(stackNewCmd.Flags())
	bindStackAddFlags(stackAddCmd.Flags())
	bindScheduleAddFlags(scheduleAddCmd.Flags())
	bindScheduleEnableFlags(scheduleEnableCmd.Flags())
	bindScheduleDisableFlags(scheduleDisableCmd.Flags())
	bindScheduleRunNowFlags(scheduleRunNowCmd.Flags())
	bindScheduleListFlags(scheduleListCmd.Flags())
	bindPRLinkFlags(prLinkCmd.Flags())
	bindPRStatusFlags(prStatusCmd.Flags())
	bindPRCommentsFlags(prCommentsCmd.Flags())
	bindSkillInstallFlags(skillInstallCmd.Flags())
	bindWebFlags(webCmd.Flags())
	bindWebPairFlags(webPairCmd.Flags())
	jsonFlag(webDevicesCmd.Flags(), "emit the paired devices as JSON")
	webUnpairCmd.Flags().Bool("all", false, "unpair every device")

	bugsCmd.AddCommand(bugsIngestCmd, bugsNewCmd)
	depsCmd.AddCommand(depsAddCmd, depsRmCmd, depsListCmd)
	stackCmd.AddCommand(stackNewCmd, stackAddCmd, stackRmCmd, stackMvCmd, stackListCmd, stackRestackCmd)
	scheduleCmd.AddCommand(scheduleListCmd, scheduleAddCmd, scheduleEnableCmd, scheduleDisableCmd, scheduleRunNowCmd, scheduleRmCmd)
	prCmd.AddCommand(prLinkCmd, prUnlinkCmd, prStatusCmd, prCommentsCmd)
	skillCmd.AddCommand(skillShowCmd, skillInstallCmd, skillListCmd)
	webCmd.AddCommand(webPairCmd, webDevicesCmd, webUnpairCmd)
}

// bindRunFlags declares `gummi run`'s flags: the shared driving surface
// plus the three only a feature card takes.
func bindRunFlags(fs *pflag.FlagSet) {
	stdDriveFlags().bind(fs)
	fs.String("acceptance", "", "acceptance criteria to seed the spec draft's Verification plan (a file path, or - for stdin)")
	adoptionFlags(fs)
}

// bindResearchFlags declares the flags `gummi research` and `gummi
// diagnose` share. No --acceptance: RS has no Verification-plan section to
// seed, and it never gets a branch, so neither adoption flag applies.
func bindResearchFlags(fs *pflag.FlagSet) {
	d := stdDriveFlags()
	d.envelope = "spend budget for the research card, in credits (required; falls back to GUMMI_ENVELOPE)"
	d.until = `stop cleanly before crossing the gate that leaves this stage (only "plan" is a valid stop)`
	d.bind(fs)
}

// bindGoalFlags declares `gummi goal`'s flags. A goal spends, plans and
// stops differently enough from a card that most of the shared surface
// needs its own wording; no --repo, because a goal is not in a repository
// (its cards name their own, DESIGN §17.2).
func bindGoalFlags(fs *pflag.FlagSet) {
	d := stdDriveFlags()
	d.envelope = "the goal's whole budget in credits — its cards, its lead and its own review all spend inside it (required; falls back to GUMMI_ENVELOPE)"
	d.profile = "profile mapping roles to models, the lead included (default: first configured)"
	d.gate = "who approves the goal's plan: attended|autopilot (past its plan a goal always runs itself)"
	d.timeout = "per-stage inactivity timeout for the goal and each of its cards (0 disables)"
	d.autonomous = "let the architect take its recommended answer instead of asking during the plan conversation"
	d.base = "branch the goal branch forks from and lands on in the goal's home repository (default: whatever it has checked out)"
	d.until = `stop cleanly before the goal's plan is approved (only "plan" is a valid stop)`
	d.repo = ""
	d.bind(fs)
	fs.String("plan-file", "", "a complete goal doc to start the plan conversation from (a file path, or - for stdin)")
	fs.String("after", "", "the goal this one continues (GL-NNN): what it came to know — reference, decided constants, findings, its hand-over — comes with it, and this goal's plan cannot be approved until that one has landed")
	fs.String("reference", "", "documents the goal is agreed against — a design, a table, a spec — as comma-separated paths; copied into the goal's notebook, pinned at the plan gate, and listed in every card's kickoff")
}

// bindResumeFlags declares `gummi resume`'s flags: the shared driving
// surface, the decision flags that say why the run stopped, and the
// goal-only levers.
func bindResumeFlags(fs *pflag.FlagSet) {
	d := stdDriveFlags()
	d.envelope = "raise the spend budget before resuming, in credits (required to clear a card that ran out; never lowers it)"
	d.gate = "who crosses this card's later gates: attended|autopilot (retired spellings still accepted; inherits the run's mode when omitted; pass to change it)"
	d.ref = "external correlation id, echoed in the stream"
	// A resumed card already knows its repository, its base branch and the
	// profile it was minted with; none of the three can be changed now.
	d.profile, d.repo, d.base = "", "", ""
	d.bind(fs)

	fs.String("answer", "", "answer a delegated ask_user question")
	fs.Bool("approve", false, "approve a design gate handed back by --gate-approval=attended")
	fs.String("request-changes", "", "send a design gate back with a note")
	fs.Bool("bounce", false, "rewind one rerun edge — a verify-fail escalation to the work stage, an implement-stage card back to plan — and continue (the TUI's b key)")
	fs.String("note", "", "addendum to the reborn stage's kickoff (used with --bounce)")
	fs.String("say", "", "read a line the way the card page would and report what it would do, as a say event, without acting")

	for _, name := range goalResumeFlagNames {
		switch name {
		case "goal-note":
			fs.String(name, "", "goals: add a note to a running goal; its lead reads it on its next turn")
		case "reverse":
			fs.String(name, "", "goals: reverse a decision for review (D-N) and send the goal back; --request-changes adds why")
		case "wrap-up":
			fs.Bool(name, false, "goals: finish now — nothing new starts, verified work lands, the rest is dropped")
		case "runs":
			fs.Int(name, 0, "goals: raise the substrate budget to this many experiment runs before resuming (never lowers it)")
		case "minutes":
			fs.Int(name, 0, "goals: raise the substrate budget to this many substrate minutes before resuming (never lowers it)")
		case "retake":
			fs.String(name, "", `goals: declare the evidence of an experiment's conclusive runs stale ("*" for all), so the goal takes them again — for when the substrate, not the code, was what failed`)
		}
	}
}

// goalResumeFlagNames are the `resume` flags that only ever apply to a
// goal. They are named here so the skill's grammar generator can keep them
// out of the core SKILL.md — an agent shipping one card cannot use any of
// them — and print them in the goals reference instead.
var goalResumeFlagNames = []string{"goal-note", "reverse", "wrap-up", "runs", "minutes", "retake"}

func bindMergeFlags(fs *pflag.FlagSet) {
	fs.StringP("message", "m", "", "landing commit message (required for a squash; - reads from stdin)")
	fs.Bool("no-squash", false, "land as a merge commit keeping the branch's commits (default: one squash commit); -m is optional and git's merge message is used without it")
}

func bindCommitFlags(fs *pflag.FlagSet) {
	messageFlag(fs, "commit message for the card's uncommitted worktree changes")
}

func bindSquashFlags(fs *pflag.FlagSet) {
	messageFlag(fs, "collapsed commit message")
	fs.Bool("force", false, "proceed even if the linked PR has open review threads")
}

// bindRewriteFlags declares `gummi rewrite`'s flags.
func bindRewriteFlags(fs *pflag.FlagSet) {
	fs.String("plan", "", `the branch as it should read, oldest first (a file path, or - for stdin): {"head":"<tip>","groups":[{"commits":["<sha>",…],"message":"…"},…]} — every commit in exactly one group`)
	fs.Bool("dry-run", false, "say what the plan would do and move nothing")
	fs.Bool("allow-pushed", false, "rewrite commits the remote already has; the branch will then need a force push, which gummi prints and never runs")
}

// bindStatusFlags declares `gummi status`'s flags.
//
// --stats is opt-in rather than always on because it reads the card's
// whole event log, and status is a thing callers poll. Where the card
// stands stays a cheap question; how it got there is the expensive one.
func bindStatusFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit machine-readable JSON instead of the text summary")
	fs.Bool("stats", false, "report where the card's credits and hours went instead of where it stands")
}

func bindWatchFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the raw record stream as NDJSON instead of the rendered transcript")
	fs.Bool("wait", false, "block until the card has a live stream instead of failing when none exists")
	fs.Bool("once", false, "exit when the current session ends instead of following the card's next one")
}

func bindDoctorFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the readiness checklist as JSON (the skill's setup path)")
	fs.Bool("deep", false, "probe per-role model reachability with a live backend turn (TTL-cached)")
}

func bindIngestFlags(fs *pflag.FlagSet) {
	fs.String("profile", "", "profile the new features adopt (default: first configured)")
	fs.Int("envelope", 0, "spend budget per card, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	fs.String("repo", "", "managed repository to create the cards in (a configured repos: name; required when repos: is configured)")
	fs.Bool("yes", false, "materialize without the confirmation prompt")
}

func bindBugsIngestFlags(fs *pflag.FlagSet) {
	fs.String("repo", "", "owner/repo to import from (default: this repo's origin remote)")
	fs.String("target-repo", "", "managed repository to create the bugs in (a configured repos: name; required when repos: is configured)")
	fs.String("label", "bug", `issue label filter ("" imports all issues)`)
	fs.String("state", "open", "issue state: open|closed|all")
	fs.String("profile", "", "profile the new bugs adopt (default: first configured)")
	fs.Int("envelope", 0, "spend budget per bug, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	fs.Int("issue", 0, "import exactly this GitHub issue number from the fetched set (0 = batch import, all fresh proposals)")
	fs.Bool("comments", false, "fetch issue comments into the report's Discussion section")
	fs.Bool("yes", false, "materialize without the confirmation prompt")
}

func bindBugsNewFlags(fs *pflag.FlagSet) {
	fs.String("title", "", "bug title (required)")
	fs.String("one-liner", "", "short one-line summary")
	fs.String("severity", "", "severity: critical|high|medium|low")
	fs.String("repro", "", "reproduction steps")
	fs.String("expected", "", "expected behavior")
	fs.String("actual", "", "actual behavior")
	fs.String("env", "", "environment (versions, OS, config)")
	fs.String("desc", "", "summary of what's broken")
	fs.String("profile", "", "profile the bug adopts (default: first configured)")
	fs.Int("envelope", 0, "spend budget, in credits (0 = uncapped; falls back to GUMMI_ENVELOPE)")
	fs.String("repo", "", "managed repository to create the bug in (a configured repos: name; required when repos: is configured)")
	fs.String("base", "", "branch the fix forks from and lands on (default: whatever the repository has checked out)")
	adoptionFlags(fs)
	fs.Bool("yes", false, "create without the confirmation prompt")
}

func bindStackNewFlags(fs *pflag.FlagSet) {
	fs.String("name", "", "the stack's display name (default: the bottom card's slug)")
}

func bindStackAddFlags(fs *pflag.FlagSet) {
	fs.Int("pos", -1, "position in the stack, 0 at the bottom (default: the top)")
}

// bindScheduleAddFlags declares `gummi schedule add`'s flags. A mint and
// a heartbeat are the same command's two shapes: the mint takes repo,
// agent/model and its envelope (the brake); the heartbeat names the one
// card it sends turns to.
func bindScheduleAddFlags(fs *pflag.FlagSet) {
	fs.String("name", "", "the schedule's display name; its id is the slug of it (required)")
	fs.String("cron", "", "the cadence, as a 5-field cron expression (minute hour day month weekday)")
	fs.String("every", "", "the cadence, as a preset compiled to cron: 5m, 15m, 1h, 6h, @hourly, @daily, @weekly")
	fs.String("tz", "", "the cadence's timezone (IANA name; default: this host's)")
	fs.String("prompt", "", "what fires: a mint's opening turn, or the heartbeat's recurring turn (required)")
	fs.String("heartbeat", "", "heartbeat: the freeform card (FF-NNN) to send the prompt to, instead of minting")
	fs.String("repo", "", "mint: managed repository the card is minted in (a configured repos: name; default: the workspace default)")
	fs.String("agent", "", "mint: the session's backend (default: the profile's implementer)")
	fs.String("model", "", "mint: the session's model (default: the profile's)")
	fs.Int("envelope", 0, "mint: the minted card's spend brake, in credits (required; every card mints with one)")
	jsonFlag(fs, "emit the stored row as JSON (the shape the board's web page reads)")
}

func bindScheduleEnableFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the row as JSON (the shape the board's web page reads)")
}

func bindScheduleDisableFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the row as JSON (the shape the board's web page reads)")
}

func bindScheduleRunNowFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the row as JSON (the shape the board's web page reads)")
}

func bindScheduleListFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit the rows as JSON (the shape the board's web page reads)")
}

func bindPRLinkFlags(fs *pflag.FlagSet) {
	fs.Bool("auto", false, "resolve the PR whose head branch matches the card's branch (via gh pr list --head)")
}

func bindPRStatusFlags(fs *pflag.FlagSet) {
	jsonFlag(fs, "emit machine-readable JSON instead of the text summary")
}

func bindPRCommentsFlags(fs *pflag.FlagSet) {
	fs.Bool("ingest", false, "write an annotation per unresolved review thread onto the card's diff")
	jsonFlag(fs, "emit machine-readable JSON instead of the text summary")
}

func bindSkillInstallFlags(fs *pflag.FlagSet) {
	fs.String("agent", "", "target a specific agent: "+skillAgentList+" (default: detect)")
	fs.String("scope", "", "install scope: project|user (default: project, or ask when interactive)")
	fs.Bool("force", false, "overwrite an existing skill bundle (default: refuse and warn on drift)")
	fs.Bool("dry-run", false, "print what would be written, change nothing")
	fs.Bool("check", false, "verify every target is up to date; write nothing, fail if any is absent/foreign/drifted")
}

// bindWebFlags declares `gummi web`'s flags.
func bindWebFlags(fs *pflag.FlagSet) {
	fs.String("addr", "", "address to serve the board on (default "+defaultWebAddr+"; falls back to GUMMI_WEB_ADDR)")
	fs.String("allow-host", "", "comma-separated names the board also answers to, e.g. a reverse proxy's (`tailscale serve`); "+
		"it always answers to its own address, localhost, the --addr name, the certificate's names and the tailnet's")
	fs.String("tls-cert", "", "serve HTTPS with this certificate (PEM; needs --tls-key)")
	fs.String("tls-key", "", "the private key for --tls-cert (PEM)")
	fs.Bool("tailscale", false, "also serve on your tailnet as its own node (embedded tsnet; no tailscaled, no port forwarding)")
	fs.String("ts-hostname", defaultTSHostname, "the board's node name on your tailnet (with --tailscale)")
	fs.String("ts-authkey", "", "tailnet auth key instead of a browser login (with --tailscale); a flag shows in ps, "+
		"so prefer TS_AUTHKEY in the environment, read when the flag is not given")
	fs.Bool("ts-tls", false, "serve HTTPS on 443 with a tailnet certificate (with --tailscale; needs MagicDNS and HTTPS enabled for the tailnet)")
	fs.Bool("verbose", false, "log the tailnet node's own messages (with --tailscale)")
	fs.Bool("no-pairing", false, "serve without pairing, to anything that can reach the listener (refused unless every listener is loopback)")
}

func bindWebPairFlags(fs *pflag.FlagSet) {
	fs.String("name", "", "pair the browser that redeems the code as this person")
}

// gateApproval normalizes the --gate-approval value every driving verb
// takes, reporting the one error message all of them used to spell out
// separately (and differently).
func gateApproval(raw string) (string, error) {
	norm, ok := domain.NormalizeGateApproval(raw)
	if !ok {
		return "", fmt.Errorf(
			"--gate-approval must be %q or %q (the retired %q/%q/%q/%q spellings are still accepted), got %q",
			domain.GateAttended, domain.GateAutopilot, "off", "gates", "caller", "auto", raw)
	}
	return norm, nil
}

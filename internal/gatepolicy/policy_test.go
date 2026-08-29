package gatepolicy

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/verdict"
)

// This table is the rule set's single specification: one row per rule in
// the shared review/verify loop policy, plus the boundary cases. Read it
// top to bottom as the spec, not as a generic fuzz of Decide.
func TestDecide(t *testing.T) {
	const workStage = domain.StageImplement // representative WorkStage for a feature

	tests := []struct {
		name string
		in   Input
		want Outcome
	}{
		// --- review ---------------------------------------------------
		{
			name: "review pass advances to verify",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Pass, WorkStage: workStage,
			},
			want: Outcome{Action: Advance, Stage: domain.StageVerify, Reason: "review-pass"},
		},
		{
			name: "review changes under cap bounces to work and burns",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Changes, Corrective: 0, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: BounceToWork, Stage: workStage, Reason: "review-changes", Burns: true},
		},
		{
			name: "review changes one under cap still bounces",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Changes, Corrective: 2, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: BounceToWork, Stage: workStage, Reason: "review-changes", Burns: true},
		},
		{
			name: "review changes exactly at cap parks, escalated, no burn",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Changes, Corrective: 3, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageReview, Reason: "review-changes-cap"},
		},
		{
			name: "review changes past cap also parks",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Changes, Corrective: 4, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageReview, Reason: "review-changes-cap"},
		},
		{
			name: "review unclear parks — never guess",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Unclear, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageReview, Reason: "review-unclear"},
		},

		// --- verify -----------------------------------------------------
		{
			name: "verify pass raises the landing gate",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Pass, WorkStage: workStage,
			},
			want: Outcome{Action: RaiseGate, Stage: domain.StageVerify, Reason: "verify-pass"},
		},
		{
			name: "research verify pass still raises the gate, never auto-mints",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindResearch,
				Verdict: verdict.Pass, WorkStage: domain.StageInvestigate,
			},
			want: Outcome{Action: RaiseGate, Stage: domain.StageVerify, Reason: "verify-pass"},
		},
		{
			name: "verify blocked always parks, even well under the cap",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Blocked, Corrective: 0, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-blocked"},
		},
		{
			name: "verify blocked at every gate mode",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Blocked, Gate: domain.GateCaller, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-blocked"},
		},
		{
			name: "verify fail under cap still parks today: VerifyMayBounce is dormant",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Fail, Corrective: 0, CorrectiveMax: 3, WorkStage: workStage,
				VerifyMayBounce: false,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-fail"},
		},
		{
			name: "verify fail under cap bounces once VerifyMayBounce is set",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Fail, Corrective: 0, CorrectiveMax: 3, WorkStage: workStage,
				VerifyMayBounce: true,
			},
			want: Outcome{Action: BounceToWork, Stage: workStage, Reason: "verify-fail-bounce", Burns: true},
		},
		{
			name: "verify fail exactly at cap parks even with VerifyMayBounce set",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Fail, Corrective: 3, CorrectiveMax: 3, WorkStage: workStage,
				VerifyMayBounce: true,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-fail"},
		},
		{
			name: "verify changes (findings, not a hard fail) follows the same fail path",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Changes, Corrective: 0, CorrectiveMax: 3, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-fail"},
		},
		{
			name: "verify unclear parks",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Unclear, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "verify-unclear"},
		},

		// --- open threads / diff comments: block every gate --------------
		{
			name: "open spec threads hold an otherwise-passing review gate open",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Verdict: verdict.Pass, WorkStage: workStage, OpenThreads: 2,
			},
			want: Outcome{Action: RaiseGate, Stage: domain.StageReview, Reason: "open-threads"},
		},
		{
			name: "open diff comments hold an otherwise-passing verify gate open",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Pass, WorkStage: workStage, OpenComments: 1,
			},
			want: Outcome{Action: RaiseGate, Stage: domain.StageVerify, Reason: "open-threads"},
		},
		{
			name: "open threads hold the gate even over a failing verdict",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Fail, WorkStage: workStage, OpenThreads: 1,
			},
			want: Outcome{Action: RaiseGate, Stage: domain.StageVerify, Reason: "open-threads"},
		},

		// --- environment halts: never a retry target ----------------------
		{
			name: "sandbox refusal parks under gate-auto",
			in: Input{
				Stage: domain.StageImplement, Kind: domain.KindFeature,
				Halt: HaltSandboxRefusal, Gate: domain.GateAuto, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageImplement, Reason: "sandbox-refusal"},
		},
		{
			name: "sandbox refusal parks under gate-caller too — at every gate mode",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Verdict: verdict.Pass, Halt: HaltSandboxRefusal, Gate: domain.GateCaller, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageVerify, Reason: "sandbox-refusal"},
		},
		{
			name: "sandbox refusal on the review stage parks the same way",
			in: Input{
				Stage: domain.StageReview, Kind: domain.KindFeature,
				Halt: HaltSandboxRefusal, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StageReview, Reason: "sandbox-refusal"},
		},
		{
			name: "rebase conflict hands to the conflict session and burns a round",
			in: Input{
				Stage: domain.StageVerify, Kind: domain.KindFeature,
				Halt: HaltRebaseConflict, WorkStage: workStage,
			},
			want: Outcome{Action: BounceToWork, Stage: workStage, Reason: "rebase-conflict", Burns: true},
		},

		// --- an unhandled stage parks rather than guessing -----------------
		{
			name: "a stage Decide doesn't drive parks",
			in: Input{
				Stage: domain.StagePlan, Kind: domain.KindFeature,
				Verdict: verdict.Pass, WorkStage: workStage,
			},
			want: Outcome{Action: Park, Stage: domain.StagePlan, Reason: "unhandled-stage"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if got != tc.want {
				t.Errorf("Decide(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

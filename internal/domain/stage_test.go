package domain

import "testing"

// TestAtOrPastCoding: the coding stage and everything after it is "at or
// past coding" (dependencies settled); the stages before it are not. Kind
// is orthogonal — one list covers both feature and bug stages.
func TestAtOrPastCoding(t *testing.T) {
	at := map[Stage]bool{
		StageTodo: false, StageBrainstorm: false, StageSpec: false, StagePlan: false,
		StageTriage: false, StageDiagnose: false,
		StageImplement: true, StageFix: true, StageReview: true, StageVerify: true, StageDone: true,
	}
	for st, want := range at {
		if got := AtOrPastCoding(st); got != want {
			t.Errorf("AtOrPastCoding(%s) = %v, want %v", st, got, want)
		}
	}
}

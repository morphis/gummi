package verdict

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Verdict
	}{
		{"pass line", "looks good\nVERDICT: pass", Pass},
		{"changes line", "issues found\nVERDICT: changes\n", Changes},
		{"uppercase label", "VERDICT: PASS", Pass},
		{"loose case and whitespace", "  verdict:   changes  ", Changes},
		{"no verdict", "no verdict here", Unclear},
		{"last line wins", "VERDICT: pass\nthen later\nVERDICT: changes", Changes},
		{"mid-sentence only", "the word verdict: pass appears mid-sentence only", Unclear},
		{"fail line", "rock build broken\nVERDICT: fail", Fail},
		{"uppercase fail", "VERDICT: FAIL", Fail},
		{"blocked line", "no pip in this sandbox\nVERDICT: blocked", Blocked},
		{"uppercase blocked", "VERDICT: BLOCKED", Blocked},
		{"glued tail", "…making check 16 redundant.VERDICT: changes", Changes},
		{"mid-sentence tail miss", "we agreed VERDICT: pass was too generous.", Unclear},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.in); got != tc.want {
				t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFromTool(t *testing.T) {
	cases := []struct {
		in   string
		want Verdict
	}{
		{"pass", Pass},
		{"changes", Changes},
		{"fail", Fail},
		{"blocked", Blocked},
		{"", Unclear},
		{"garbage", Unclear},
	}
	for _, tc := range cases {
		if got := FromTool(tc.in); got != tc.want {
			t.Errorf("FromTool(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, v := range []Verdict{Unclear, Pass, Changes, Fail, Blocked} {
		if FromTool(v.String()) != v {
			t.Errorf("FromTool(%q) = %v, want %v (String round-trip)", v.String(), FromTool(v.String()), v)
		}
	}
	if got := Verdict(99).String(); got != "unclear" {
		t.Errorf("Verdict(99).String() = %q, want unclear", got)
	}
}

func TestLastAssistant(t *testing.T) {
	snap := engine.Snapshot{Transcript: []engine.Message{
		{Author: engine.AuthorTool, Content: "edit foo.go"},
		{Author: engine.AuthorAssistant, Content: "first"},
		{Author: engine.AuthorAssistant, Content: "second"},
	}}
	if got := LastAssistant(snap); got != "second" {
		t.Errorf("LastAssistant = %q, want second", got)
	}
	if got := LastAssistant(engine.Snapshot{}); got != "" {
		t.Errorf("LastAssistant(empty) = %q, want empty", got)
	}
}

func TestSessionVerdict(t *testing.T) {
	// tool result wins over the tail
	snap := engine.Snapshot{
		Verdict: "changes",
		Transcript: []engine.Message{
			{Author: engine.AuthorAssistant, Content: "VERDICT: pass"},
		},
	}
	if got := SessionVerdict(snap); got != Changes {
		t.Errorf("SessionVerdict with tool = %v, want Changes", got)
	}
	// no tool result: fall back to the tail in the last assistant message
	snap.Verdict = ""
	if got := SessionVerdict(snap); got != Pass {
		t.Errorf("SessionVerdict tail fallback = %v, want Pass", got)
	}
	// tool result that parses to Unclear also falls back
	snap.Verdict = "garbage"
	if got := SessionVerdict(snap); got != Pass {
		t.Errorf("SessionVerdict unclear-tool fallback = %v, want Pass", got)
	}
}

func TestMaxRounds(t *testing.T) {
	if got := MaxRounds(domain.RoundKindPlan); got != 2 {
		t.Errorf("MaxRounds(RoundKindPlan) = %d, want 2", got)
	}
	if got := MaxRounds(domain.RoundKindReview); got != 3 {
		t.Errorf("MaxRounds(RoundKindReview) = %d, want 3", got)
	}
}

func TestSessionVerdictFloor(t *testing.T) {
	cases := []struct {
		name    string
		verdict string
		tail    string
		floor   string
		want    Verdict
	}{
		{"blocked floor downgrades raw pass", "", "VERDICT: pass", "blocked", Blocked},
		{"blocked floor leaves raw fail", "", "VERDICT: fail", "blocked", Fail},
		{"blocked floor leaves raw changes", "", "VERDICT: changes", "blocked", Changes},
		{"blocked floor leaves raw blocked", "", "VERDICT: blocked", "blocked", Blocked},
		{"blocked floor leaves unclear", "", "no verdict", "blocked", Unclear},
		{"tool pass downgraded by floor", "pass", "", "blocked", Blocked},
		{"tool fail not downgraded", "fail", "", "blocked", Fail},
		{"no floor returns raw pass", "", "VERDICT: pass", "", Pass},
		{"unknown floor returns raw pass", "", "VERDICT: pass", "something-else", Pass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := engine.Snapshot{
				Verdict:      tc.verdict,
				VerdictFloor: tc.floor,
				Transcript:   []engine.Message{{Author: engine.AuthorAssistant, Content: tc.tail}},
			}
			if got := SessionVerdict(snap); got != tc.want {
				t.Errorf("SessionVerdict = %v, want %v", got, tc.want)
			}
		})
	}
}

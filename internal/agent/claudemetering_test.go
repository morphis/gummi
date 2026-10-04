package agent

import (
	"math"
	"testing"
)

// newMeteringSession is a claudeSession with only the metering state a
// mapLine-driven test needs.
func newMeteringSession() *claudeSession {
	return &claudeSession{prevCostUSD: map[string]float64{}, estimated: map[string]float64{}}
}

// feed runs lines through mapLine and returns the usage and context
// events they produced, in order.
func feed(s *claudeSession, lines ...string) (usages []Usage, ctxs []Context) {
	for _, l := range lines {
		for _, e := range s.mapLine([]byte(l)) {
			switch e.Kind {
			case EventUsage:
				usages = append(usages, e.Usage)
			case EventContext:
				ctxs = append(ctxs, e.Context)
			}
		}
	}
	return usages, ctxs
}

// The CLI names a model by the id it was given in init and modelUsage
// (claude-haiku-4-5) but by the API's dated id in message_start
// (claude-haiku-4-5-20251001). Mid-turn estimates and the settle that
// retires them must land on one key: the engine retires a model's pending
// estimates by name, so a split key booked every such turn twice.
func TestClaudeDatedModelIDSettlesItsOwnEstimates(t *testing.T) {
	s := newMeteringSession()
	const given, dated = "claude-haiku-4-5", "claude-haiku-4-5-20251001"
	turn := func(delta, result string) ([]Usage, []Context) {
		return feed(s,
			`{"type":"system","subtype":"init","session_id":"x","model":"`+given+`"}`,
			`{"type":"stream_event","event":{"type":"message_start","message":{"model":"`+dated+`"}}}`,
			`{"type":"stream_event","event":{"type":"message_delta","usage":`+delta+`}}`,
			`{"type":"result","subtype":"success","modelUsage":{"`+given+`":`+result+`}}`,
		)
	}

	us, ctxs := turn(`{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":70}`,
		`{"inputTokens":10,"outputTokens":20,"cacheReadInputTokens":70,"costUSD":0.01,"contextWindow":200000}`)
	if len(us) != 2 || us[0].Model != given || us[1].Model != given || !us[1].Settled {
		t.Fatalf("turn 1 usage = %+v, want a mid-turn sample and its settle both keyed %q", us, given)
	}
	if len(ctxs) != 1 || ctxs[0].Tokens != 80 {
		t.Errorf("turn 1 context = %+v, want 80 tokens (the dated request is the main model's)", ctxs)
	}

	// turn 2 is priced mid-turn at the realized rate; the settle must
	// retire exactly that estimate, under the same name.
	us, _ = turn(`{"input_tokens":20,"output_tokens":30,"cache_read_input_tokens":50}`,
		`{"inputTokens":30,"outputTokens":50,"cacheReadInputTokens":120,"costUSD":0.025,"contextWindow":200000}`)
	if len(us) != 2 || !us[0].Estimate || us[0].Model != given || us[1].Model != given {
		t.Fatalf("turn 2 usage = %+v, want an estimate and its settle both keyed %q", us, given)
	}
	if got := us[0].Credits + us[1].Credits; math.Abs(got-1.5) > 1e-9 {
		t.Errorf("turn 2 credits = %v, want the CLI's actual 1.5", got)
	}
	if len(s.estimated) != 0 {
		t.Errorf("estimates left unsettled: %v", s.estimated)
	}
}

func TestClaudeModelKey(t *testing.T) {
	for in, want := range map[string]string{
		"claude-haiku-4-5-20251001":                   "claude-haiku-4-5",
		"claude-haiku-4-5":                            "claude-haiku-4-5",
		"claude-opus-5-5":                             "claude-opus-5-5",
		"us.anthropic.claude-haiku-4-5-20251001-v1:0": "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	} {
		if got := claudeModelKey(in); got != want {
			t.Errorf("claudeModelKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// After --resume the CLI's modelUsage is cumulative over the whole saved
// conversation. The first result must charge only this process's share,
// not re-book what the earlier process already settled; later results
// are plain deltas again.
func TestClaudeResumedSessionDoesNotRebookPriorSpend(t *testing.T) {
	s := newMeteringSession()
	s.resumeBase = true
	const m = "claude-sonnet-5"
	// this process's request: 10 tokens. The result folds in 20 tokens and
	// $0.02 the previous process spent, at the same $0.001/token.
	us, _ := feed(s,
		`{"type":"system","subtype":"init","session_id":"x","model":"`+m+`"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"`+m+`"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":10}}}`,
		`{"type":"result","subtype":"success","modelUsage":{"`+m+`":{"inputTokens":30,"costUSD":0.03}}}`,
	)
	var settled float64
	for _, u := range us {
		settled += u.Credits
	}
	if math.Abs(settled-1.0) > 1e-9 {
		t.Errorf("first resumed turn booked %v credits, want 1.0 (this process's 10 tokens, not the conversation's 30)", settled)
	}
	if s.resumeBase {
		t.Error("the baseline was not taken")
	}

	us, _ = feed(s,
		`{"type":"system","subtype":"init","session_id":"x","model":"`+m+`"}`,
		`{"type":"result","subtype":"success","modelUsage":{"`+m+`":{"inputTokens":40,"costUSD":0.045}}}`,
	)
	if len(us) != 1 || math.Abs(us[0].Credits-1.5) > 1e-9 {
		t.Errorf("second turn = %+v, want a plain 1.5-credit delta", us)
	}
}

// An API failure comes back as subtype "success" with is_error set; the
// error names the CLI's message, not "(success)".
func TestClaudeErrorResultNamesTheFailure(t *testing.T) {
	s := newMeteringSession()
	evs := s.mapLine([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"There's an issue with the selected model (claude-opus-4.8).\nRun --model to pick another."}`))
	if len(evs) != 1 || evs[0].Kind != EventError {
		t.Fatalf("events = %+v, want one error", evs)
	}
	rf, ok := evs[0].Err.(*RunFailure)
	if !ok {
		t.Fatalf("err = %T, want *RunFailure", evs[0].Err)
	}
	if got, want := rf.Err.Error(), "turn failed: There's an issue with the selected model (claude-opus-4.8)."; got != want {
		t.Errorf("cause = %q, want %q", got, want)
	}
	// a subtype that does say what happened is kept
	evs = s.mapLine([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true}`))
	if rf, ok := evs[0].Err.(*RunFailure); !ok || rf.Err.Error() != "turn failed (error_max_turns)" {
		t.Errorf("max-turns error = %v", evs[0].Err)
	}
}

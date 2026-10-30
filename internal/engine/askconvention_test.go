package engine

import "testing"

// TestAnAskBlockIsReadAsJSON: a gummi-ask block's body is one JSON value,
// on its own lines or inline; a question or an option holding a code
// fence does not end the block, and a malformed one is left as prose.
func TestAnAskBlockIsReadAsJSON(t *testing.T) {
	for _, tc := range []struct {
		name, text, question, stripped string
		ok                             bool
	}{
		{
			"block", "Before.\n```gummi-ask\n{\"question\": \"Which?\", \"options\": [{\"label\": \"a\"}, {\"label\": \"b\"}]}\n```\nAfter.",
			"Which?", "Before.\n\nAfter.", true,
		},
		{"inline", "```gummi-ask {\"question\": \"Which?\", \"options\": [{\"label\": \"a\"}]}```", "Which?", "", true},
		{
			"fence in a question", "```gummi-ask\n{\"question\": \"Keep the ```go fence?\", \"options\": [{\"label\": \"yes ```\"}]}\n```",
			"Keep the ```go fence?", "", true,
		},
		{"malformed", "```gummi-ask\n{\"question\": \n```", "", "", false},
		{"no block", "Just prose.", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, stripped, ok := parseAskConvention(tc.text)
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				if stripped != tc.text {
					t.Errorf("a refused block changed the text: %q", stripped)
				}
				return
			}
			if a.Question != tc.question || stripped != tc.stripped {
				t.Errorf("question %q, stripped %q", a.Question, stripped)
			}
		})
	}
}

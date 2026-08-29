package ui

import "testing"

func TestParseInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want parsedInput
	}{
		{
			name: "empty line is verbNone with empty Text",
			in:   "",
			want: parsedInput{Kind: verbNone},
		},
		{
			name: "whitespace-only line is verbNone with empty Text",
			in:   "   \t  ",
			want: parsedInput{Kind: verbNone},
		},
		{
			name: "plain prose is a message",
			in:   "looks good to me",
			want: parsedInput{Kind: verbNone, Text: "looks good to me"},
		},
		{
			name: "a verb only as a later word stays prose",
			in:   "looks good, but verify the padding",
			want: parsedInput{Kind: verbNone, Text: "looks good, but verify the padding"},
		},
		{
			name: "leading/trailing whitespace is trimmed before matching",
			in:   "  approve  ",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "approve"},
		},
		{
			name: "matching is case-insensitive: Approve",
			in:   "Approve",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "Approve"},
		},
		{
			name: "matching is case-insensitive: APPROVE",
			in:   "APPROVE",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "APPROVE"},
		},
		{
			name: "trailing punctuation on the first word is prose, not a verb",
			in:   "approve.",
			want: parsedInput{Kind: verbNone, Text: "approve."},
		},
		{
			name: "a verb with a remainder carries it, trimmed",
			in:   "changes the pill needs the dim token",
			want: parsedInput{Kind: verbCommand, Verb: "changes", Remainder: "the pill needs the dim token", Text: "changes the pill needs the dim token"},
		},
		{
			name: "extra internal whitespace before the remainder is trimmed",
			in:   "verify    the csv path",
			want: parsedInput{Kind: verbCommand, Verb: "verify", Remainder: "the csv path", Text: "verify    the csv path"},
		},
		{
			name: "a verb alone has no remainder",
			in:   "diff",
			want: parsedInput{Kind: verbCommand, Verb: "diff", Text: "diff"},
		},
		{
			name: "bare slash is verbMenu with no remainder",
			in:   "/",
			want: parsedInput{Kind: verbMenu, Text: "/"},
		},
		{
			name: "slash plus text is verbMenu with the rest as Remainder",
			in:   "/foo",
			want: parsedInput{Kind: verbMenu, Remainder: "foo", Text: "/foo"},
		},
		{
			name: "slash with a space before the filter still trims into Remainder",
			in:   "/ foo bar",
			want: parsedInput{Kind: verbMenu, Remainder: "foo bar", Text: "/ foo bar"},
		},
		{
			name: "leading/trailing whitespace around a slash line is trimmed first",
			in:   "  /foo  ",
			want: parsedInput{Kind: verbMenu, Remainder: "foo", Text: "/foo"},
		},
		{
			name: "every vocabulary word matches on its own",
			in:   "bounce",
			want: parsedInput{Kind: verbCommand, Verb: "bounce", Text: "bounce"},
		},
		{
			name: "autopilot matches on its own",
			in:   "autopilot",
			want: parsedInput{Kind: verbCommand, Verb: "autopilot", Text: "autopilot"},
		},
		{
			name: "park matches on its own",
			in:   "park",
			want: parsedInput{Kind: verbCommand, Verb: "park", Text: "park"},
		},
		{
			name: "land matches on its own",
			in:   "land",
			want: parsedInput{Kind: verbCommand, Verb: "land", Text: "land"},
		},
		{
			name: "rebase matches on its own",
			in:   "rebase",
			want: parsedInput{Kind: verbCommand, Verb: "rebase", Text: "rebase"},
		},
		{
			name: "squash matches on its own",
			in:   "squash",
			want: parsedInput{Kind: verbCommand, Verb: "squash", Text: "squash"},
		},
		{
			name: "clean matches on its own",
			in:   "clean",
			want: parsedInput{Kind: verbCommand, Verb: "clean", Text: "clean"},
		},
		{
			name: "spec matches on its own",
			in:   "spec",
			want: parsedInput{Kind: verbCommand, Verb: "spec", Text: "spec"},
		},
		{
			name: "a near-miss word is prose, not fuzzy-matched",
			in:   "approving the plan now",
			want: parsedInput{Kind: verbNone, Text: "approving the plan now"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInput(tc.in)
			if got != tc.want {
				t.Errorf("parseInput(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

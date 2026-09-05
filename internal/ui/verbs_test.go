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

		// The sigil rule: a bare word is ALWAYS prose. These are the cases
		// the confirm chip used to exist for — every one of them is an
		// ordinary English sentence that the old bare-word vocabulary read
		// as a state-changing command.
		{
			name: "a bare verb is prose, not a command",
			in:   "approve",
			want: parsedInput{Kind: verbNone, Text: "approve"},
		},
		{
			name: "verify the CSV path sends as prose",
			in:   "verify the CSV path is right",
			want: parsedInput{Kind: verbNone, Text: "verify the CSV path is right"},
		},
		{
			name: "changes leading a sentence is prose",
			in:   "changes the pill needs the dim token",
			want: parsedInput{Kind: verbNone, Text: "changes the pill needs the dim token"},
		},
		{
			name: "a verb only as a later word stays prose",
			in:   "looks good, but verify the padding",
			want: parsedInput{Kind: verbNone, Text: "looks good, but verify the padding"},
		},
		{
			name: "capitalised bare verb is still prose",
			in:   "Approve",
			want: parsedInput{Kind: verbNone, Text: "Approve"},
		},

		// "/verb" is ALWAYS a verb.
		{
			name: "slash plus a verb is a command",
			in:   "/approve",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "/approve"},
		},
		{
			name: "leading/trailing whitespace is trimmed before matching",
			in:   "  /approve  ",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "/approve"},
		},
		{
			name: "matching is case-insensitive: /Approve",
			in:   "/Approve",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "/Approve"},
		},
		{
			name: "matching is case-insensitive: /APPROVE",
			in:   "/APPROVE",
			want: parsedInput{Kind: verbCommand, Verb: "approve", Text: "/APPROVE"},
		},
		{
			name: "a slashed verb with a remainder carries it, trimmed",
			in:   "/changes the pill needs the dim token",
			want: parsedInput{Kind: verbCommand, Verb: "changes", Remainder: "the pill needs the dim token", Text: "/changes the pill needs the dim token"},
		},
		{
			name: "extra internal whitespace before the remainder is trimmed",
			in:   "/verify    the csv path",
			want: parsedInput{Kind: verbCommand, Verb: "verify", Remainder: "the csv path", Text: "/verify    the csv path"},
		},
		{
			name: "a space between the slash and the verb still resolves it",
			in:   "/ verify the csv path",
			want: parsedInput{Kind: verbCommand, Verb: "verify", Remainder: "the csv path", Text: "/ verify the csv path"},
		},

		// "/" plus anything else opens the menu, pre-filtered.
		{
			name: "bare slash is verbMenu with no remainder",
			in:   "/",
			want: parsedInput{Kind: verbMenu, Text: "/"},
		},
		{
			name: "slash plus a non-verb is verbMenu with the rest as Remainder",
			in:   "/foo",
			want: parsedInput{Kind: verbMenu, Remainder: "foo", Text: "/foo"},
		},
		{
			name: "slash plus a near-miss is the menu, not a fuzzy verb match",
			in:   "/appro",
			want: parsedInput{Kind: verbMenu, Remainder: "appro", Text: "/appro"},
		},
		{
			name: "trailing punctuation on a slashed verb is the menu, not the verb",
			in:   "/approve.",
			want: parsedInput{Kind: verbMenu, Remainder: "approve.", Text: "/approve."},
		},
		{
			name: "slash with a space before a non-verb filter still trims into Remainder",
			in:   "/ foo bar",
			want: parsedInput{Kind: verbMenu, Remainder: "foo bar", Text: "/ foo bar"},
		},
		{
			name: "leading/trailing whitespace around a slash line is trimmed first",
			in:   "  /foo  ",
			want: parsedInput{Kind: verbMenu, Remainder: "foo", Text: "/foo"},
		},
	}

	// every word in the vocabulary resolves under "/" and stays prose bare
	for verb := range verbs {
		cases = append(cases,
			struct {
				name string
				in   string
				want parsedInput
			}{
				name: "/" + verb + " is a command",
				in:   "/" + verb,
				want: parsedInput{Kind: verbCommand, Verb: verb, Text: "/" + verb},
			},
			struct {
				name string
				in   string
				want parsedInput
			}{
				name: "bare " + verb + " is prose",
				in:   verb,
				want: parsedInput{Kind: verbNone, Text: verb},
			},
		)
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

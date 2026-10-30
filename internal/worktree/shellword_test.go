package worktree

import "testing"

// TestShellCommandQuotesWhatAShellWouldRead: a plain word stands bare, and
// a branch git allows but a shell reads — a comment, a substitution, a
// quote — is single-quoted.
func TestShellCommandQuotesWhatAShellWouldRead(t *testing.T) {
	for words, want := range map[[3]string]string{
		{"git", "push", "ff/x"}:     "git push ff/x",
		{"git", "push", "fix#12"}:   "git push 'fix#12'",
		{"git", "push", "a$(id)"}:   "git push 'a$(id)'",
		{"git", "push", "it's"}:     `git push 'it'\''s'`,
		{"git", "push", "main:o#1"}: "git push 'main:o#1'",
		{"git", "push", ""}:         "git push ''",
	} {
		if got := ShellCommand(words[:]...); got != want {
			t.Errorf("ShellCommand(%q) = %s, want %s", words, got, want)
		}
	}
}

package worktree

import "strings"

// ShellCommand joins words into a command line gummi prints for a person
// to run — a push it never runs itself. A word holding anything but plain
// characters is single-quoted: git allows a branch named fix#12 or a$(id),
// and pasted bare, "#12" is a comment (the push names another branch) and
// "$(id)" runs.
func ShellCommand(words ...string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = shellWord(w)
	}
	return strings.Join(quoted, " ")
}

func shellWord(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_-") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

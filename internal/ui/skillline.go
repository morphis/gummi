package ui

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/agentplugins"
)

// "/skill <name> [message]" — a turn that asks the agent to use one
// library skill. It is not a verb: verbs act on the card, and this is a
// message to the agent. The line is rewritten into plain prose before
// anything classifies it, so it routes exactly as the message it becomes
// would (a steer, a freeform turn, a consult), and the agent receives an
// instruction it can act on rather than a sigil it would have to guess
// at.

// skillCommand is the word after the "/" that marks a skill line.
const skillCommand = "skill"

// isSkillLine reports whether text is a "/skill" line: the word, alone
// or followed by whitespace. "/skills" and "/skillful" are not.
func isSkillLine(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(strings.ToLower(t), "/"+skillCommand) {
		return false
	}
	rest := t[len(skillCommand)+1:]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n'
}

// expandSkillLine rewrites a "/skill <name> [message]" line into the
// message the agent receives, and names the skill's id. ok is false for a line that is not a skill
// line, which is returned unchanged. problem is non-empty when it is one
// but cannot be sent: no name, or a name the library does not hold.
//
// The name matches a skill's id or its name, case-insensitively, so
// what the composer completes and what a person types from memory both
// land on the same skill.
func expandSkillLine(text, workspace string, lib []agentplugins.Item) (out, id string, ok bool, problem string) {
	if !isSkillLine(text) {
		return text, "", false, ""
	}
	rest := strings.TrimSpace(strings.TrimSpace(text)[len(skillCommand)+1:])
	if rest == "" {
		return text, "", true, "name a skill: /skill <name> [what to do with it]"
	}
	name, msg := rest, ""
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		name, msg = rest[:i], rest[i+1:]
	}
	msg = strings.TrimSpace(msg)
	i := slices.IndexFunc(lib, func(s agentplugins.Item) bool {
		return strings.EqualFold(s.ID, name) || strings.EqualFold(s.Name, name)
	})
	if i < 0 {
		if len(lib) == 0 {
			return text, "", true, "the agent plugins library has no skills — add one first"
		}
		return text, "", true, "no skill " + strconv.Quote(name) + " in the agent plugins library"
	}
	id = lib[i].ID
	// the message leads, so a session started by this line takes its
	// title from what the person asked rather than from the instruction.
	// Backends name a forwarded skill differently (by its SKILL.md name,
	// by its directory, under a namespace), so the instruction carries
	// all three handles: the name, the library id and the file itself.
	out = skillInstruction(lib[i], workspace)
	if msg != "" {
		out = msg + "\n\n" + out
	}
	return out, id, true, ""
}

// expandSkill is expandSkillLine against this workspace's library.
func (m *Shell) expandSkill(text string) (out, id string, ok bool, problem string) {
	if !isSkillLine(text) {
		return text, "", false, ""
	}
	return expandSkillLine(text, m.ws.Root, m.librarySkills())
}

// skillInstruction is the sentence that asks the agent to use skill.
func skillInstruction(skill agentplugins.Item, workspace string) string {
	where := "library id `" + skill.ID + "`"
	if workspace != "" {
		where += ", instructions in `" + filepath.Join(agentplugins.ItemDir(workspace, skill.ID), "SKILL.md") + "`"
	}
	return "Use the \"" + skill.Name + "\" skill for this (" + where + ")."
}

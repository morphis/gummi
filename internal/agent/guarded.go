package agent

// guarded.go is the static, adapter-free view of which backends can honor
// permissions: guarded. Doctor and startup config validation use
// GuardedSupport to answer "will this backend actually enforce guarded?"
// without constructing a session. The map mirrors exactly what each
// adapter's NewSession does when handed PermissionGuarded — claude and zz
// reject it outright (TestClaudeCodeRejectsGuarded, TestZZRefusesGuarded),
// while copilot, opencode, and codex accept it. headless is deliberately
// absent: it wraps an arbitrary operator command that never inspects
// Permission, so gummi has no way to know whether the wrapped tool honors
// guarded, and it must never be flagged either way.
var guardedBase = map[string]bool{
	"claude":   false,
	"zz":       false,
	"copilot":  true,
	"opencode": true,
	"codex":    true,
}

// GuardedSupport reports whether the named backend honors permissions:
// guarded. known is false when name is absent from the matrix (headless, or
// any unrecognized name) — callers must treat that as "cannot tell" and
// skip it, not as a mismatch.
func GuardedSupport(name string) (support, known bool) {
	support, known = guardedBase[name]
	return support, known
}

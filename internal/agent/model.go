package agent

import "strings"

// foreignModelPrefixes maps a known non-Anthropic model-id prefix to the
// provider it names. It is a denylist, not a claude-* allowlist: adapters
// forward opaque ids the CLI resolves itself (fake ids in tests, aliases
// like "opus"/"sonnet", future Anthropic names), so only ids that clearly
// belong to another provider are rejected. Match is case-insensitive on
// the id's leading token.
var foreignModelPrefixes = []struct{ prefix, provider string }{
	{"gpt-", "OpenAI"},
	{"o1-", "OpenAI"},
	{"o3-", "OpenAI"},
	{"o4-", "OpenAI"},
	{"chatgpt", "OpenAI"},
	{"gemini-", "Google"},
	{"llama", "Meta Llama"},
	{"mistral", "Mistral"},
	{"mixtral", "Mistral"},
	{"qwen", "Qwen"},
	{"deepseek", "DeepSeek"},
	{"grok-", "xAI"},
	{"command-", "Cohere"},
}

// ForeignModel reports whether id names a model from a provider the
// Anthropic-only Claude Code CLI cannot route to, and if so which provider.
// It is a denylist by design (see foreignModelPrefixes): an empty id, the
// bare Claude aliases ("opus"/"sonnet"/"haiku"), and any "claude-*" id pass
// as non-foreign, as do unknown/opaque ids the CLI resolves on its own.
func ForeignModel(id string) (foreign bool, provider string) {
	lower := strings.ToLower(strings.TrimSpace(id))
	for _, p := range foreignModelPrefixes {
		if strings.HasPrefix(lower, p.prefix) {
			return true, p.provider
		}
	}
	return false, ""
}

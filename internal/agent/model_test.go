package agent

import "testing"

func TestForeignModel(t *testing.T) {
	foreign := map[string]string{
		"gpt-5-mini":     "OpenAI",
		"gpt-5-codex":    "OpenAI",
		"o1-preview":     "OpenAI",
		"o3-mini":        "OpenAI",
		"o4-mini":        "OpenAI",
		"chatgpt-4o":     "OpenAI",
		"gemini-1.5-pro": "Google",
		"llama3.1-70b":   "Meta Llama",
		"mistral-large":  "Mistral",
		"mixtral-8x7b":   "Mistral",
		"qwen2.5-coder":  "Qwen",
		"deepseek-chat":  "DeepSeek",
		"grok-2":         "xAI",
		"command-r-plus": "Cohere",
		"GPT-5":          "OpenAI", // case-insensitive
	}
	for id, wantProvider := range foreign {
		got, provider := ForeignModel(id)
		if !got || provider != wantProvider {
			t.Errorf("ForeignModel(%q) = (%v, %q), want (true, %q)", id, got, provider, wantProvider)
		}
	}

	// Anthropic ids, aliases, an empty id, and the opaque fake ids the
	// adapter tests forward must all pass as non-foreign (denylist, not a
	// claude-* allowlist).
	for _, id := range []string{
		"", "opus", "sonnet", "haiku", "claude-sonnet-5", "claude-opus-4.8",
		"claude-test-1", "test-model", "m",
	} {
		if foreign, _ := ForeignModel(id); foreign {
			t.Errorf("ForeignModel(%q) = true, want false (must pass)", id)
		}
	}
}

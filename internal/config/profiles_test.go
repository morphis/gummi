package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesMissingIsEmpty(t *testing.T) {
	p, err := LoadProfiles(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing profiles should be empty, not error: %v", err)
	}
	if len(p.Profiles) != 0 {
		t.Errorf("empty profiles has entries: %+v", p)
	}
}

func TestLoadProfiles(t *testing.T) {
	p := writeProfiles(t, `default: thrifty
profiles:
  premium:
    architect: { model: claude-opus-4.8 }
    reviewer: { model: gpt-5-codex }
  thrifty:
    implementer:
      model: qwen
      byok: { type: openai, base_url: http://127.0.0.1:8080/v1, api_key_env: LLAMA_API_KEY }
`)
	if p.Default != "thrifty" {
		t.Errorf("default = %q", p.Default)
	}
	if p.Profiles["premium"]["architect"].Model != "claude-opus-4.8" {
		t.Errorf("premium architect wrong: %+v", p.Profiles["premium"]["architect"])
	}
	if p.Profiles["premium"]["reviewer"].Model != "gpt-5-codex" {
		t.Error("cross-model reviewer not parsed")
	}
	byok := p.Profiles["thrifty"]["implementer"].Provider
	if byok == nil || byok.BaseURL != "http://127.0.0.1:8080/v1" || byok.APIKeyEnv != "LLAMA_API_KEY" {
		t.Errorf("byok not parsed: %+v", byok)
	}
}

func TestLoadProfilesRejectsLiteralKey(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe:
      model: m
      byok: { base_url: http://h/v1, api_key: sk-secret-literal }
`))
	if err == nil {
		t.Fatal("literal byok api_key should be rejected")
	}
}

func TestLoadProfilesRejectsUnsafeKeyEnv(t *testing.T) {
	// a committed profile must not be able to name an arbitrary secret
	for _, bad := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "NPM_TOKEN", "HOME"} {
		body := "profiles:\n  x:\n    scribe:\n      model: m\n      byok: { base_url: http://evil/v1, api_key_env: " + bad + " }\n"
		if _, err := LoadProfiles(profilesPath(t, body)); err == nil {
			t.Errorf("api_key_env %q should be rejected (secret exfiltration)", bad)
		}
	}
	// conventional API-key names and the gummi namespace are allowed
	for _, ok := range []string{"OPENAI_API_KEY", "LLAMA_API_KEY", "GUMMI_PROVIDER_KEY"} {
		body := "profiles:\n  x:\n    scribe:\n      model: m\n      byok: { base_url: http://h/v1, api_key_env: " + ok + " }\n"
		if _, err := LoadProfiles(profilesPath(t, body)); err != nil {
			t.Errorf("api_key_env %q should be allowed: %v", ok, err)
		}
	}
}

func TestLoadProfilesRejectsMissingModel(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, "profiles:\n  x:\n    architect: {}\n"))
	if err == nil {
		t.Error("role without a model should error")
	}
}

func TestLoadProfilesRejectsByokWithoutBaseURL(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe:
      model: m
      byok: { api_key_env: K }
`))
	if err == nil {
		t.Error("byok without base_url should error")
	}
}

func TestProfilesTemplateParses(t *testing.T) {
	p := writeProfiles(t, ProfilesTemplate)
	if p.Default != "thrifty" {
		t.Errorf("template default = %q", p.Default)
	}
	if _, ok := p.Profiles["premium"]; !ok {
		t.Error("template missing premium profile")
	}
	if p.Profiles["premium"]["reviewer"].Model == p.Profiles["premium"]["implementer"].Model {
		t.Error("template premium should use cross-model review")
	}
}

func profilesPath(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeProfiles(t *testing.T, body string) Profiles {
	t.Helper()
	p, err := LoadProfiles(profilesPath(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

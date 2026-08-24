package config

import (
	"os"
	"path/filepath"
	"strings"
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
    architect: { backend: claude, model: claude-opus-4.8, output_token_max: 128000 }
    reviewer: { model: gpt-5-codex }
  thrifty:
    implementer: { backend: headless, model: qwen2.5-coder-32b }
    scribe: { model: gpt-5-mini }
`)
	if p.Default != "thrifty" {
		t.Errorf("default = %q", p.Default)
	}
	if p.Profiles["premium"]["architect"].Model != "claude-opus-4.8" {
		t.Errorf("premium architect wrong: %+v", p.Profiles["premium"]["architect"])
	}
	if p.Profiles["premium"]["architect"].Backend != "claude" {
		t.Errorf("premium architect backend = %q, want claude", p.Profiles["premium"]["architect"].Backend)
	}
	if got := p.Profiles["premium"]["architect"].OutputTokenMax; got != 128000 {
		t.Errorf("premium architect output_token_max = %d, want 128000", got)
	}
	if got := p.Profiles["premium"]["reviewer"].OutputTokenMax; got != 0 {
		t.Errorf("premium reviewer output_token_max = %d, want 0 (unset)", got)
	}
	if p.Profiles["premium"]["reviewer"].Backend != "" {
		t.Errorf("premium reviewer backend = %q, want empty (default)", p.Profiles["premium"]["reviewer"].Backend)
	}
	if p.Profiles["thrifty"]["implementer"].Backend != "headless" {
		t.Errorf("thrifty implementer backend = %q, want headless", p.Profiles["thrifty"]["implementer"].Backend)
	}
}

func TestLoadProfilesRejectsUnknownBackend(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe: { backend: made-up, model: m }
`))
	if err == nil {
		t.Fatal("unknown backend should be rejected")
	}
	if !strings.Contains(err.Error(), "made-up") {
		t.Errorf("error should mention the bad backend, got: %v", err)
	}
}

func TestLoadProfilesRejectsNegativeOutputTokenMax(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe: { model: m, output_token_max: -1 }
`))
	if err == nil {
		t.Fatal("negative output_token_max should be rejected")
	}
	if !strings.Contains(err.Error(), "output_token_max") {
		t.Errorf("error should mention output_token_max, got: %v", err)
	}
}

func TestLoadProfilesAcceptsCodex(t *testing.T) {
	p := writeProfiles(t, "profiles:\n  x:\n    implementer: { backend: codex, model: gpt-5 }\n")
	if got := p.Profiles["x"]["implementer"].Backend; got != "codex" {
		t.Fatalf("backend = %q", got)
	}
}

func TestProfilesAcceptZZBackend(t *testing.T) {
	p := writeProfiles(t, "profiles:\n  x:\n    implementer: { backend: zz, model: qwen2.5-coder-32b }\n")
	if got := p.Profiles["x"]["implementer"].Backend; got != "zz" {
		t.Fatalf("backend = %q", got)
	}
}

func TestProfilesRejectUnknownBackendMentionsZZ(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe: { backend: made-up, model: m }
`))
	if err == nil {
		t.Fatal("unknown backend should be rejected")
	}
	if !strings.Contains(err.Error(), "zz") {
		t.Errorf("error should name zz in the accepted list, got: %v", err)
	}
}

func TestLoadProfilesRejectsLegacyByok(t *testing.T) {
	// stale profiles from before the migration must fail with a pointer,
	// not silently ignore the removed field.
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    scribe:
      model: m
      byok: { base_url: http://h/v1 }
`))
	if err == nil {
		t.Fatal("legacy byok: block should be rejected")
	}
	if !strings.Contains(err.Error(), "byok") || !strings.Contains(err.Error(), "backend") {
		t.Errorf("error should point at the byok → backend migration, got: %v", err)
	}
}

func TestLoadProfilesRejectsMissingModel(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, "profiles:\n  x:\n    architect: {}\n"))
	if err == nil {
		t.Error("role without a model should error")
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
	// premium routes implementer and reviewer through different backends
	// (cross-backend review — same model, distinct provider infrastructure).
	if p.Profiles["premium"]["reviewer"].Backend == p.Profiles["premium"]["implementer"].Backend {
		t.Error("template premium should route reviewer through a different backend than implementer")
	}
	// every backend named in the template must be a known one.
	for name, prof := range p.Profiles {
		for role, rc := range prof {
			if rc.Backend == "" {
				continue
			}
			if _, ok := knownBackends[rc.Backend]; !ok {
				t.Errorf("template profile %q role %q references unknown backend %q", name, role, rc.Backend)
			}
		}
	}
}

// TestParseProfilesRejectsMalformedYAML: a document with a YAML syntax
// error must fail loudly rather than silently loading zero profiles (the
// probe round-trip must not swallow the parse error).
func TestParseProfilesProviderRoundTrip(t *testing.T) {
	p := writeProfiles(t, "profiles:\n  x:\n    implementer: { backend: zz, model: m, provider: mab }\n")
	if got := p.Profiles["x"]["implementer"].Provider; got != "mab" {
		t.Errorf("provider = %q, want mab", got)
	}
}

func TestParseProfilesProviderAcceptedWithoutBackend(t *testing.T) {
	p := writeProfiles(t, "profiles:\n  x:\n    implementer: { model: m, provider: mab }\n")
	if got := p.Profiles["x"]["implementer"].Provider; got != "mab" {
		t.Errorf("provider = %q, want mab", got)
	}
	if got := p.Profiles["x"]["implementer"].Backend; got != "" {
		t.Errorf("backend = %q, want empty", got)
	}
}

func TestParseProfilesProviderRejectedForNonZZ(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    implementer: { backend: claude, model: m, provider: mab }
`))
	if err == nil {
		t.Fatal("provider: on a non-zz backend should be rejected")
	}
	for _, want := range []string{"provider", "claude", "x", "implementer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestParseProfilesRejectsMalformedYAML(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, "profiles:\n  x:\n    architect: { model: m\n"))
	if err == nil {
		t.Fatal("malformed YAML should be rejected")
	}
	if !strings.Contains(err.Error(), "parsing") {
		t.Errorf("error should carry the parsing prefix, got: %v", err)
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

func TestParseProfilesSandbox(t *testing.T) {
	p := writeProfiles(t, `default: premium
profiles:
  premium:
    sandbox: enforce
    architect: { backend: claude, model: claude-opus-4.8 }
    implementer: { model: gpt-5 }
  thrifty:
    architect: { model: somemodel }
`)
	if got := p.Sandboxes["premium"]; got != "enforce" {
		t.Errorf("premium sandbox = %q, want enforce", got)
	}
	if got, ok := p.Sandboxes["thrifty"]; ok && got != "" {
		t.Errorf("thrifty sandbox = %q, want unset", got)
	}
	// the role still parses even though a sibling `sandbox:` key exists.
	if p.Profiles["premium"]["architect"].Backend != "claude" {
		t.Errorf("premium architect backend = %q, want claude", p.Profiles["premium"]["architect"].Backend)
	}
}

func TestParseProfilesRejectsBadSandbox(t *testing.T) {
	_, err := LoadProfiles(profilesPath(t, `profiles:
  x:
    sandbox: enfrce
    architect: { model: m }
`))
	if err == nil {
		t.Fatal("unknown per-profile sandbox should be rejected")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error should mention sandbox, got: %v", err)
	}
}

// TestParseProfilesSandboxDoesNotShadowRoles: a profile carrying
// `sandbox:` still parses its four roles correctly (the field is stripped
// before the roles map is unmarshalled, never misread as a role).
func TestParseProfilesSandboxDoesNotShadowRoles(t *testing.T) {
	p := writeProfiles(t, `profiles:
  x:
    sandbox: enforce
    architect: { backend: opencode, model: a }
    implementer: { backend: opencode, model: b }
    reviewer: { backend: opencode, model: c }
    scribe: { backend: opencode, model: d }
`)
	for _, role := range []string{"architect", "implementer", "reviewer", "scribe"} {
		if got := p.Profiles["x"][role].Model; got == "" {
			t.Errorf("role %q model empty — sandbox shadowed the roles map", role)
		}
	}
	if p.Sandboxes["x"] != "enforce" {
		t.Errorf("sandbox = %q, want enforce", p.Sandboxes["x"])
	}
}

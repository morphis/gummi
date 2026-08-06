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
    architect: { backend: claude, model: claude-opus-4.8 }
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

func TestLoadProfilesAcceptsCodex(t *testing.T) {
	p := writeProfiles(t, "profiles:\n  x:\n    implementer: { backend: codex, model: gpt-5 }\n")
	if got := p.Profiles["x"]["implementer"].Backend; got != "codex" {
		t.Fatalf("backend = %q", got)
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

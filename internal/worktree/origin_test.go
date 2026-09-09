package worktree

import "testing"

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in        string
		host, own string
		ok        bool
	}{
		{"git@github.com:canonical/lxd.git", "github.com", "canonical/lxd", true},
		{"ssh://git@github.com/canonical/lxd", "github.com", "canonical/lxd", true},
		{"https://github.com/canonical/lxd.git", "github.com", "canonical/lxd", true},
		{"https://github.com/canonical/lxd/", "github.com", "canonical/lxd", true},
		{"https://gitlab.example.com/team/proj.git", "gitlab.example.com", "team/proj", true},
		{"/srv/git/bare.git", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		host, own, ok := ParseRemote(c.in)
		if host != c.host || own != c.own || ok != c.ok {
			t.Errorf("ParseRemote(%q) = %q, %q, %v; want %q, %q, %v", c.in, host, own, ok, c.host, c.own, c.ok)
		}
	}
}

func TestRootForName(t *testing.T) {
	p := &Pool{defaultRoot: "/ws/repo", byName: map[string]string{"lxd": "/ws/git/lxd"}}
	if r, ok := p.RootForName("lxd"); !ok || r != "/ws/git/lxd" {
		t.Errorf("named = %q, %v", r, ok)
	}
	if r, ok := p.RootForName(""); !ok || r != "/ws/repo" {
		t.Errorf("default = %q, %v", r, ok)
	}
	if _, ok := p.RootForName("nope"); ok {
		t.Error("unknown name resolved")
	}
	if _, ok := (&Pool{byName: map[string]string{}}).RootForName(""); ok {
		t.Error("missing default resolved")
	}
}

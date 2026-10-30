package domain

import "testing"

func TestTreeSetsRoundTrip(t *testing.T) {
	if got := EncodeTrees("abc", nil); got != "abc" {
		t.Errorf("a session that spans nothing keeps its bare hash, got %q", got)
	}
	enc := EncodeTrees("abc", map[string]string{"lib": "111", "app": "222"})
	if enc != "abc\napp=222\nlib=111" {
		t.Errorf("encoded %q", enc)
	}
	p, a := DecodeTrees(enc)
	if p != "abc" || a["lib"] != "111" || a["app"] != "222" || len(a) != 2 {
		t.Errorf("decoded %q %v", p, a)
	}
	if p, a := DecodeTrees("abc"); p != "abc" || a != nil {
		t.Errorf("bare decode %q %v", p, a)
	}
	if PrimaryTree(enc) != "abc" {
		t.Error("PrimaryTree")
	}
}

// TestATreeSetKeepsARepositoryNamedWithAnEquals: a hash holds no "=", so
// a repository's name may.
func TestATreeSetKeepsARepositoryNamedWithAnEquals(t *testing.T) {
	primary, attached := DecodeTrees(EncodeTrees("p", map[string]string{"a=b": "h"}))
	if primary != "p" || len(attached) != 1 || attached["a=b"] != "h" {
		t.Fatalf("read back %q, %q", primary, attached)
	}
}

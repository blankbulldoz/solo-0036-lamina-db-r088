package index

import (
	"lamina/internal/datum"
	"testing"
)

func text(s string) datum.Value { return datum.Value{Kind: "text", Text: s} }

func TestNocaseTreeEqualityCandidates(t *testing.T) {
	tree := New("nocase")
	tree.Add(text("Alpha"), "k1")
	tree.Add(text("alpha"), "k2")
	tree.Add(text("ALPHA"), "k3")
	tree.Add(text("beta"), "k4")

	got := tree.Candidates("=", text("AlPhA"))
	want := map[string]bool{"k1": true, "k2": true, "k3": true}
	if len(got) != len(want) {
		t.Fatalf("equality candidates = %v, want all three alpha keys", got)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected candidate %q", k)
		}
	}
	if tree.Size() != 4 {
		t.Fatalf("history size = %d, want 4 (equivalent values must not collapse rows)", tree.Size())
	}
}

func TestNocaseTreeRangeCandidates(t *testing.T) {
	tree := New("nocase")
	tree.Add(text("apple"), "a")
	tree.Add(text("Banana"), "b")
	tree.Add(text("cherry"), "c")

	ge := tree.Candidates(">=", text("banana"))
	seen := map[string]bool{}
	for _, k := range ge {
		seen[k] = true
	}
	if !seen["b"] || !seen["c"] || seen["a"] {
		t.Fatalf("range >= banana = %v, want banana, cherry", ge)
	}
	lt := tree.Candidates("<", text("BANANA"))
	if len(lt) != 1 || lt[0] != "a" {
		t.Fatalf("range < BANANA = %v, want only apple", lt)
	}
}

func TestByteTreeKeepsCaseDistinct(t *testing.T) {
	tree := New("byte")
	tree.Add(text("Alpha"), "k1")
	tree.Add(text("alpha"), "k2")
	if got := tree.Candidates("=", text("Alpha")); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("byte equality = %v, want exactly k1", got)
	}
}

func TestTreeIsSortedUnderRule(t *testing.T) {
	tree := New("nocase")
	for _, v := range []string{"Zoo", "apple", "Banana", "aardvark"} {
		tree.Add(text(v), v)
	}
	for i := 1; i < len(tree.entries); i++ {
		if datum.RuleOrder(tree.entries[i-1].Value, tree.entries[i].Value, "nocase") > 0 {
			t.Fatalf("tree not sorted at %d: %q then %q", i, tree.entries[i-1].Value.Text, tree.entries[i].Value.Text)
		}
	}
}

func TestTreeIgnoresNulls(t *testing.T) {
	tree := New("byte")
	tree.Add(datum.Value{Kind: "null"}, "k1")
	if tree.Size() != 0 {
		t.Fatalf("null values must not enter the index, size=%d", tree.Size())
	}
}

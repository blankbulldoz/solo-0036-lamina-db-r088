package collate

import "testing"

func TestRegistryIsFrozenAndDiscoverable(t *testing.T) {
	if !Known(Byte) || !Known(Nocase) {
		t.Fatalf("built-in rules missing: %v", Specs())
	}
	if v, ok := Version(Byte); !ok || v != 1 {
		t.Fatalf("byte rule version = %d/%v", v, ok)
	}
	if v, ok := Version(Nocase); !ok || v != 1 {
		t.Fatalf("nocase rule version = %d/%v", v, ok)
	}
	if Known("nope") {
		t.Fatalf("unknown rule must not resolve")
	}
	if _, ok := Version("nope"); ok {
		t.Fatalf("unknown rule must fail closed")
	}
	if Effective("") != Byte {
		t.Fatalf("empty declaration must default to byte, got %q", Effective(""))
	}
}

func TestByteRulePreservesHistoricalSemantics(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"abc", "abd", -1},
		{"B", "a", -1}, // raw byte order: uppercase before lowercase
		{"abc", "abc", 0},
		{"é", "e", 1},
	}
	for _, c := range cases {
		got, err := Compare(Byte, c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(byte,%q,%q) = %d,%v want %d", c.a, c.b, got, err, c.want)
		}
		eq, _ := Equal(Byte, c.a, c.b)
		if eq != (c.want == 0) {
			t.Errorf("Equal(byte,%q,%q) = %v want %v", c.a, c.b, eq, c.want == 0)
		}
		idA, _ := Identity(Byte, c.a)
		if idA != c.a {
			t.Errorf("byte identity rewrote payload %q -> %q", c.a, idA)
		}
	}
}

func TestNocaseCaseFolding(t *testing.T) {
	equivalents := [][]string{
		{"Straße", "STRASSE", "strasse", "sTrAsSe"},
		{"ABC", "abc", "Abc"},
		{"ﬁle", "file"}, // U+FB01 ligature folds to "fi"
	}
	for _, group := range equivalents {
		base := group[0]
		baseID, err := Identity(Nocase, base)
		if err != nil {
			t.Fatal(err)
		}
		for _, other := range group[1:] {
			eq, err := Equal(Nocase, base, other)
			if err != nil {
				t.Fatal(err)
			}
			if !eq {
				t.Errorf("expected %q ~ %q under nocase", base, other)
			}
			otherID, _ := Identity(Nocase, other)
			if baseID != otherID {
				t.Errorf("identities diverge: %q -> %q, %q -> %q", base, baseID, other, otherID)
			}
		}
	}
}

func TestNocaseUnicodeEquivalence(t *testing.T) {
	// "é" precomposed (U+00E9) vs "e" + combining acute (U+0065 U+0301).
	precomposed := "café"
	decomposed := "café"
	if precomposed == decomposed {
		t.Fatal("raw strings should differ byte-wise")
	}
	eq, err := Equal(Nocase, precomposed, decomposed)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatalf("NFC-equivalent spellings must share identity: %q vs %q", precomposed, decomposed)
	}
	eqByte, _ := Equal(Byte, precomposed, decomposed)
	if eqByte {
		t.Fatal("byte rule must keep the distinct spellings separate")
	}
}

func TestNocaseIsTotalOrderWithRawTieBreak(t *testing.T) {
	values := []string{"banana", "Banana", "apple", "Apple", "cherry", "apple", "BANANA"}
	sorted := make([]string, len(values))
	copy(sorted, values)
	// Stable sort by folded identity; equivalent spellings keep raw-byte order.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0; j-- {
			n, err := Compare(Nocase, sorted[j-1], sorted[j])
			if err != nil {
				t.Fatal(err)
			}
			if n <= 0 {
				break
			}
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	for i := 1; i < len(sorted); i++ {
		n, err := Compare(Nocase, sorted[i-1], sorted[i])
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			t.Fatalf("not sorted at %d: %q before %q", i, sorted[i-1], sorted[i])
		}
	}
	// All apple/banana spellings group by folded identity regardless of case.
	ids := map[string]bool{}
	for _, s := range sorted {
		id, _ := Identity(Nocase, s)
		ids[id] = true
	}
	if len(ids) != 3 {
		t.Fatalf("expected three folded identities (apple, banana, cherry), got %d: %v", len(ids), sorted)
	}
	// Equivalent spellings: folded identity equal, raw bytes decide direction.
	n, err := Compare(Nocase, "ABC", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("distinct raw payloads must not compare equal even when equivalent")
	}
	reverse, _ := Compare(Nocase, "abc", "ABC")
	if n != -reverse {
		t.Fatal("tie-break ordering must be antisymmetric")
	}
	self, _ := Compare(Nocase, "abc", "abc")
	if self != 0 {
		t.Fatal("identical payload must compare equal")
	}
}

func TestUnknownRuleRejected(t *testing.T) {
	if _, err := Identity("nope", "x"); err == nil {
		t.Fatal("unknown rule identity must error")
	}
	if _, err := Compare("nope", "x", "y"); err == nil {
		t.Fatal("unknown rule compare must error")
	}
}

// Package collate owns the frozen, explicitly versioned text comparison rules
// used by LaminaDB. A rule is chosen per text column at relation creation and
// is immutable for the lifetime of that relation.
//
// Two facts are kept separate on purpose:
//
//   - Identity answers "do these two texts denote the same value?" and drives
//     primary-key encoding, unique constraints and equality matching.
//   - Compare answers a total order question and drives ordering and range
//     index bounds. When two texts share an identity but differ in raw bytes,
//     Compare breaks the tie on the original payload bytes so the result stays
//     a deterministic total order; equal raw inputs still compare equal.
//
// Rules are never silently redefined. A name resolves to one frozen
// implementation plus a version number that is recorded in every portable
// image. A reader that does not know a recorded rule/version refuses the
// image instead of guessing at a different equivalence relation.
package collate

import (
	"fmt"
	"sort"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// Byte preserves the historical byte-for-byte text semantics: identity is
	// the raw UTF-8 payload and ordering is the raw byte order.
	Byte = "byte"
	// Nocase is the text-retrieval rule: Unicode NFC normalization followed by
	// full, language-independent case folding, then NFC again. Texts that only
	// differ in case or in equivalent Unicode spellings (for example precomposed
	// vs combining-mark forms) share an identity.
	Nocase = "nocase"
)

// Spec describes one frozen rule for external discovery and image manifests.
type Spec struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Meaning string `json:"meaning"`
}

type rule struct {
	spec  Spec
	fold  func(string) string
	order func(a, b string) int
}

var (
	nfc         = norm.NFC
	caseless    = cases.Fold() // language-independent full Unicode case folding
	byteOrderer = func(a, b string) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	registry = map[string]rule{
		Byte: {
			spec:  Spec{Name: Byte, Version: 1, Meaning: "raw UTF-8 byte identity and byte ordering"},
			fold:  func(s string) string { return s },
			order: byteOrderer,
		},
		Nocase: {
			spec: Spec{Name: Nocase, Version: 1, Meaning: "NFC plus full Unicode case folding; equivalent spellings share identity, raw bytes break ordering ties"},
			fold: func(s string) string {
				// Normalize first so canonical/decomposed spellings converge,
				// fold case across the whole string, then normalize again so
				// folding cannot leave a non-canonical sequence behind.
				return nfc.String(caseless.String(nfc.String(s)))
			},
			order: byteOrderer,
		},
	}
)

// Effective resolves an optional declaration to the rule that must be used.
// An empty declaration means Byte, which keeps every relation created before
// collation existed on its original byte-wise semantics.
func Effective(name string) string {
	if name == "" {
		return Byte
	}
	return name
}

// Known reports whether name is a supported, frozen rule.
func Known(name string) bool {
	_, ok := registry[name]
	return ok
}

// Version returns the frozen implementation version of a rule. Unknown rules
// report false so callers fail closed rather than defaulting.
func Version(name string) (int, bool) {
	r, ok := registry[Effective(name)]
	if !ok {
		return 0, false
	}
	return r.spec.Version, true
}

// Specs lists all supported rules sorted by name for stable API output.
func Specs() []Spec {
	out := make([]Spec, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Identity maps a raw text to the value used for matching, deduplication and
// primary-key encoding under the named rule. The returned string is only an
// identity token; callers must keep storing and returning the original text.
func Identity(ruleName, text string) (string, error) {
	r, ok := registry[Effective(ruleName)]
	if !ok {
		return "", fmt.Errorf("unknown text collation %q", ruleName)
	}
	return r.fold(text), nil
}

// Equal reports whether two raw texts share an identity under the rule.
func Equal(ruleName, a, b string) (bool, error) {
	idA, err := Identity(ruleName, a)
	if err != nil {
		return false, err
	}
	idB, err := Identity(ruleName, b)
	if err != nil {
		return false, err
	}
	return idA == idB, nil
}

// Compare is a deterministic total order under the named rule: equivalent
// texts group together (ordered by their shared identity), and texts sharing
// an identity are disambiguated by their raw payload bytes.
func Compare(ruleName, a, b string) (int, error) {
	r, ok := registry[Effective(ruleName)]
	if !ok {
		return 0, fmt.Errorf("unknown text collation %q", ruleName)
	}
	idA := r.fold(a)
	idB := r.fold(b)
	if idA != idB {
		return r.order(idA, idB), nil
	}
	return r.order(a, b), nil
}

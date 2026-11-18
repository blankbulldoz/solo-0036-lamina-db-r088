package index

import (
	"lamina/internal/collate"
	"lamina/internal/datum"
	"sort"
)

type Entry struct {
	Value datum.Value
	Key   string
}

// Tree is an ordered history of non-null column values to primary-key
// candidates. Rule is the frozen collation that governs text ordering and
// equality; non-text columns carry the byte rule and behave exactly as before.
//
// Placement and range bounds are decided on the rule identity so an equality
// probe brackets every spelling that shares an identity. Entries inside one
// identity group are tie-broken on raw payload and primary key, which keeps
// the slice a deterministic total order without collapsing distinct rows.
type Tree struct {
	rule    string
	entries []Entry
	seen    map[string]bool
}

func New(rule string) *Tree {
	if rule == "" {
		rule = collate.Byte
	}
	return &Tree{rule: rule, seen: map[string]bool{}}
}

// Rule reports the collation governing this tree.
func (t *Tree) Rule() string { return t.rule }

// rank returns the ordering signal used for placement and bounds: the folded
// identity for text, the raw comparable value otherwise.
func (t *Tree) rank(v datum.Value) string {
	if v.Kind == "text" {
		if id, err := collate.Identity(t.rule, v.Text); err == nil {
			return id
		}
	}
	return v.Key()
}

// order is the total order between two stored entries: identity first, then
// raw payload, then primary key so duplicates are never ambiguous.
func (t *Tree) order(a Entry, b Entry) int {
	ra, rb := t.rank(a.Value), t.rank(b.Value)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	if a.Value.Kind == "text" && b.Value.Kind == "text" && a.Value.Text != b.Value.Text {
		if a.Value.Text < b.Value.Text {
			return -1
		}
		return 1
	}
	if a.Key < b.Key {
		return -1
	}
	if a.Key > b.Key {
		return 1
	}
	return 0
}

func (t *Tree) Add(value datum.Value, key string) {
	if value.Null() {
		return
	}
	token := t.rank(value) + "\x00" + key
	if t.seen[token] {
		return
	}
	t.seen[token] = true
	entry := Entry{value, key}
	at := sort.Search(len(t.entries), func(i int) bool { return t.order(t.entries[i], entry) >= 0 })
	t.entries = append(t.entries, Entry{})
	copy(t.entries[at+1:], t.entries[at:])
	t.entries[at] = entry
}

// Candidates returns the primary-key candidates matching op against value.
// Bounds use the probe's rule identity, so "=" returns every equivalent
// spelling and range operators include whole identity groups at the edges.
func (t *Tree) Candidates(op string, value datum.Value) []string {
	if value.Null() {
		return nil
	}
	probeRank := t.rank(value)
	// Lower bound: first entry whose identity is not before the probe.
	lo := sort.Search(len(t.entries), func(i int) bool { return t.rank(t.entries[i].Value) >= probeRank })
	// Upper bound: first entry whose identity is after the probe.
	hi := sort.Search(len(t.entries), func(i int) bool { return t.rank(t.entries[i].Value) > probeRank })
	begin, end := 0, len(t.entries)
	switch op {
	case "=":
		begin, end = lo, hi
	case ">":
		begin = hi
	case ">=":
		begin = lo
	case "<":
		end = lo
	case "<=":
		end = hi
	default:
		return nil
	}
	out := make([]string, 0, end-begin)
	seen := map[string]bool{}
	for _, e := range t.entries[begin:end] {
		if !seen[e.Key] {
			seen[e.Key] = true
			out = append(out, e.Key)
		}
	}
	return out
}
func (t *Tree) Size() int { return len(t.entries) }

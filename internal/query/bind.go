package query

import (
	"fmt"
	"lamina/internal/catalog"
	"lamina/internal/collate"
	"lamina/internal/datum"
)

func Bind(r catalog.Relation, q Request) error {
	if q.Limit < 0 || q.Limit > 10000 {
		return fmt.Errorf("limit must be 0..10000")
	}
	if len(q.Where) > 32 || len(q.Order) > 8 {
		return fmt.Errorf("query expression limit exceeded")
	}
	if q.Aggregate != nil && len(q.Columns) > 0 {
		return fmt.Errorf("projection and aggregate cannot be combined")
	}
	for _, name := range q.Columns {
		if _, ok := r.Column(name); !ok {
			return fmt.Errorf("unknown projection %s", name)
		}
	}
	for _, o := range q.Order {
		if _, ok := r.Column(o.Column); !ok {
			return fmt.Errorf("unknown ordering %s", o.Column)
		}
	}
	for _, p := range q.Where {
		c, ok := r.Column(p.Column)
		if !ok {
			return fmt.Errorf("unknown predicate %s", p.Column)
		}
		switch p.Op {
		case "=", "!=", "<", ">", "<=", ">=", "is_null", "not_null":
		default:
			return fmt.Errorf("unsupported comparison %s", p.Op)
		}
		if p.Op != "is_null" && p.Op != "not_null" && !p.Value.Null() && p.Value.Kind != c.Type {
			return fmt.Errorf("predicate type mismatch for %s", p.Column)
		}
	}
	if a := q.Aggregate; a != nil {
		if a.Function != "count" && a.Function != "sum" {
			return fmt.Errorf("unsupported aggregate")
		}
		if a.Column == "*" || a.Column == "" {
			if a.Function != "count" {
				return fmt.Errorf("sum needs integer column")
			}
		} else {
			c, ok := r.Column(a.Column)
			if !ok {
				return fmt.Errorf("unknown aggregate column")
			}
			if a.Function == "sum" && c.Type != "int" {
				return fmt.Errorf("sum requires integer column")
			}
		}
	}
	return nil
}
func Matches(r catalog.Relation, row datum.Row, predicates []Predicate) bool {
	for _, p := range predicates {
		v := row[p.Column]
		if p.Op == "is_null" {
			if !v.Null() {
				return false
			}
			continue
		}
		if p.Op == "not_null" {
			if v.Null() {
				return false
			}
			continue
		}
		if v.Null() || p.Value.Null() {
			return false
		}
		rule := r.RuleOf(p.Column)
		// Equality and its negation are identity questions. Text columns use the
		// rule's folded identity so equivalent Unicode spellings match; non-text
		// kinds keep their typed value comparison.
		if p.Op == "=" || p.Op == "!=" {
			var eq bool
			var err error
			if v.Kind == "text" {
				eq, err = collate.Equal(rule, v.Text, p.Value.Text)
			} else {
				var cmp int
				cmp, err = v.Compare(p.Value)
				eq = cmp == 0
			}
			if err != nil {
				return false
			}
			if (p.Op == "=") != eq {
				return false
			}
			continue
		}
		// Ordering predicates compare identities for the inclusive boundary so
		// the whole equivalent group is kept, matching the index bounds. Strict
		// inequalities use the total order, whose raw-byte tie-break only orders
		// spellings inside an identity group.
		n, err := datum.TextCompare(v, p.Value, rule)
		if err != nil {
			return false
		}
		idCmp, err := identityCompare(rule, v, p.Value)
		if err != nil {
			return false
		}
		pass := false
		switch p.Op {
		case "<":
			pass = n < 0
		case ">":
			pass = n > 0
		case "<=":
			pass = idCmp <= 0
		case ">=":
			pass = idCmp >= 0
		}
		if !pass {
			return false
		}
	}
	return true
}

// identityCompare orders two values purely on their rule identities (-1, 0,
// +1), without the raw-payload tie-break used by the display total order.
func identityCompare(rule string, a, b datum.Value) (int, error) {
	if a.Kind != "text" || b.Kind != "text" {
		return a.Compare(b)
	}
	idA, err := collate.Identity(rule, a.Text)
	if err != nil {
		return 0, err
	}
	idB, err := collate.Identity(rule, b.Text)
	if err != nil {
		return 0, err
	}
	if idA < idB {
		return -1, nil
	}
	if idA > idB {
		return 1, nil
	}
	return 0, nil
}

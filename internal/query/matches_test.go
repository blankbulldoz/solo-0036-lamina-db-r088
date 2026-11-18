package query

import (
	"lamina/internal/catalog"
	"lamina/internal/datum"
	"testing"
)

func intRelation() catalog.Relation {
	return catalog.Relation{
		Name:    "nums",
		Primary: "n",
		Columns: []catalog.Column{
			{Name: "n", Type: "int"},
			{Name: "tag", Type: "text", Nullable: true, Collation: "nocase"},
		},
	}
}

func TestNonTextEqualityNotAffectedByTextRule(t *testing.T) {
	r := intRelation()
	row := datum.Row{"n": datum.Integer(42), "tag": datum.Value{Kind: "text", Text: "Alpha"}}
	if !Matches(r, row, []Predicate{{Column: "n", Op: "=", Value: datum.Integer(42)}}) {
		t.Fatal("int equality must match equal integers")
	}
	if Matches(r, row, []Predicate{{Column: "n", Op: "=", Value: datum.Integer(7)}}) {
		t.Fatal("int equality must not match distinct integers")
	}
	if Matches(r, row, []Predicate{{Column: "n", Op: "!=", Value: datum.Integer(42)}}) {
		t.Fatal("int != must be false for equal integers")
	}
	if !Matches(r, row, []Predicate{{Column: "n", Op: ">=", Value: datum.Integer(42)}}) {
		t.Fatal("int >= boundary must include equal value")
	}
	if !Matches(r, row, []Predicate{{Column: "n", Op: "<", Value: datum.Integer(43)}}) {
		t.Fatal("int < must work")
	}
}

func TestMixedTextAndIntPredicates(t *testing.T) {
	r := intRelation()
	row := datum.Row{"n": datum.Integer(1), "tag": datum.Value{Kind: "text", Text: "Banana"}}
	// A nocase text predicate and an int predicate combine correctly.
	good := []Predicate{
		{Column: "tag", Op: "=", Value: datum.Value{Kind: "text", Text: "BANANA"}},
		{Column: "n", Op: "=", Value: datum.Integer(1)},
	}
	if !Matches(r, row, good) {
		t.Fatal("combined folded-text and int equality must both pass")
	}
	bad := []Predicate{
		{Column: "tag", Op: "=", Value: datum.Value{Kind: "text", Text: "BANANA"}},
		{Column: "n", Op: "=", Value: datum.Integer(2)},
	}
	if Matches(r, row, bad) {
		t.Fatal("distinct int must fail the combined predicate")
	}
}

func TestTextRangeGroupBoundary(t *testing.T) {
	r := intRelation()
	row := datum.Row{"n": datum.Integer(1), "tag": datum.Value{Kind: "text", Text: "APPLE"}}
	// Inclusive <= boundary keeps the whole equivalent group.
	if !Matches(r, row, []Predicate{{Column: "tag", Op: "<=", Value: datum.Value{Kind: "text", Text: "apple"}}}) {
		t.Fatal("<= must include the equivalent identity group at the boundary")
	}
	if !Matches(r, row, []Predicate{{Column: "tag", Op: ">=", Value: datum.Value{Kind: "text", Text: "apple"}}}) {
		t.Fatal(">= must include the equivalent identity group at the boundary")
	}
	// Strict ordering distinguishes spellings inside the group via raw bytes:
	// uppercase APPLE is byte-before lowercase apple, so it is strictly less.
	if !Matches(r, row, []Predicate{{Column: "tag", Op: "<", Value: datum.Value{Kind: "text", Text: "apple"}}}) {
		t.Fatal("strict < should order APPLE before apple within the identity group")
	}
}

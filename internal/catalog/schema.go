package catalog

import (
	"fmt"
	"lamina/internal/collate"
	"lamina/internal/datum"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func ValidName(s string) bool { return identifier.MatchString(s) }

type Column struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Collation string `json:"collation,omitempty"`
}
type Relation struct {
	Name    string   `json:"name"`
	Primary string   `json:"primary"`
	Columns []Column `json:"columns"`
	Created uint64   `json:"created"`
}

// Rule resolves the effective collation of a text column. Empty declarations
// mean the frozen byte rule, so relations serialized before collation existed
// keep their exact byte-wise identity and ordering.
func (c Column) Rule() string { return collate.Effective(c.Collation) }

func (r Relation) Validate() error {
	if !ValidName(r.Name) || !ValidName(r.Primary) {
		return fmt.Errorf("invalid relation or primary name")
	}
	if len(r.Columns) == 0 || len(r.Columns) > 64 {
		return fmt.Errorf("relation needs 1..64 columns")
	}
	seen := map[string]bool{}
	found := false
	for _, c := range r.Columns {
		if !ValidName(c.Name) || seen[c.Name] {
			return fmt.Errorf("invalid or duplicate column %q", c.Name)
		}
		seen[c.Name] = true
		if c.Type != "int" && c.Type != "text" && c.Type != "bool" {
			return fmt.Errorf("unsupported type %q", c.Type)
		}
		if c.Collation != "" {
			if c.Type != "text" {
				return fmt.Errorf("collation is only valid on text columns (%s)", c.Name)
			}
			if !collate.Known(c.Collation) {
				return fmt.Errorf("unknown text collation %q on %s", c.Collation, c.Name)
			}
		}
		if c.Name == r.Primary {
			found = true
			if c.Nullable || c.Type == "bool" {
				return fmt.Errorf("primary must be non-null int or text")
			}
		}
	}
	if !found {
		return fmt.Errorf("primary column does not exist")
	}
	return nil
}
func (r Relation) Column(name string) (Column, bool) {
	for _, c := range r.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}
func (r Relation) Normalize(row datum.Row) (datum.Row, error) {
	for k := range row {
		if _, ok := r.Column(k); !ok {
			return nil, fmt.Errorf("unknown column %q", k)
		}
	}
	out := make(datum.Row, len(r.Columns))
	for _, c := range r.Columns {
		v, exists := row[c.Name]
		if !exists || v.Null() {
			if !c.Nullable {
				return nil, fmt.Errorf("column %s cannot be null", c.Name)
			}
			v = datum.Value{Kind: "null"}
		}
		if !v.Null() && v.Kind != c.Type {
			return nil, fmt.Errorf("column %s requires %s", c.Name, c.Type)
		}
		if v.Kind == "text" && len(v.Text) > 65536 {
			return nil, fmt.Errorf("text exceeds 64KiB")
		}
		out[c.Name] = v
	}
	return out, nil
}

// Key derives the stored identity token of a row's primary value under the
// primary column's declared collation.
func (r Relation) Key(row datum.Row) (string, error) {
	col, _ := r.Column(r.Primary)
	return row[r.Primary].IdentityKey(col.Rule())
}

// Token derives the identity token of any column value for unique constraint
// and index purposes under that column's declared collation.
func (r Relation) Token(column string, v datum.Value) (string, error) {
	col, ok := r.Column(column)
	if !ok {
		return "", fmt.Errorf("unknown column %q", column)
	}
	return v.IdentityKey(col.Rule())
}

// RuleOf returns the collation governing a column, or the byte rule when the
// column is absent or non-text.
func (r Relation) RuleOf(column string) string {
	if col, ok := r.Column(column); ok {
		return col.Rule()
	}
	return collate.Byte
}

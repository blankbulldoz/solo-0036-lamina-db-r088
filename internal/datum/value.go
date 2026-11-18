package datum

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lamina/internal/collate"
	"strconv"
)

type Value struct {
	Kind string
	Int  int64
	Text string
	Bool bool
}

func (v Value) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case "", "null":
		return []byte("null"), nil
	case "int":
		return []byte(strconv.FormatInt(v.Int, 10)), nil
	case "text":
		return json.Marshal(v.Text)
	case "bool":
		return json.Marshal(v.Bool)
	default:
		return nil, fmt.Errorf("unknown value kind %q", v.Kind)
	}
}
func (v *Value) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var raw any
	if err := d.Decode(&raw); err != nil {
		return err
	}
	*v = Value{Kind: "null"}
	switch x := raw.(type) {
	case nil:
	case string:
		v.Kind = "text"
		v.Text = x
	case bool:
		v.Kind = "bool"
		v.Bool = x
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return fmt.Errorf("only signed 64-bit integers are supported")
		}
		v.Kind = "int"
		v.Int = n
	default:
		return fmt.Errorf("scalar value required")
	}
	return nil
}
func (v Value) Null() bool { return v.Kind == "" || v.Kind == "null" }
func (v Value) Compare(w Value) (int, error) {
	if v.Null() || w.Null() {
		return 0, fmt.Errorf("null has no predicate ordering")
	}
	if v.Kind != w.Kind {
		return 0, fmt.Errorf("incompatible types %s and %s", v.Kind, w.Kind)
	}
	switch v.Kind {
	case "int":
		if v.Int < w.Int {
			return -1, nil
		}
		if v.Int > w.Int {
			return 1, nil
		}
	case "text":
		if v.Text < w.Text {
			return -1, nil
		}
		if v.Text > w.Text {
			return 1, nil
		}
	case "bool":
		if !v.Bool && w.Bool {
			return -1, nil
		}
		if v.Bool && !w.Bool {
			return 1, nil
		}
	}
	return 0, nil
}
func (v Value) Key() string {
	b, _ := v.MarshalJSON()
	return v.Kind + ":" + string(b)
}
func Integer(n int64) Value { return Value{Kind: "int", Int: n} }

// IdentityKey is the stable identity token of a value under a declared text
// collation. Non-text values keep their historical Key encoding; text values
// are encoded from the rule's normalized identity instead of the raw payload.
// The original payload stays stored in the row, so this never rewrites user
// data, it only decides which rows count as the same key.
func (v Value) IdentityKey(ruleName string) (string, error) {
	if v.Kind != "text" {
		return v.Key(), nil
	}
	id, err := collate.Identity(ruleName, v.Text)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(id)
	if err != nil {
		return "", err
	}
	return "text:" + string(b), nil
}

// TextCompare compares two non-null values under a declared text collation.
// Non-text kinds fall back to the typed byte-compatible comparison.
func TextCompare(a, b Value, ruleName string) (int, error) {
	if a.Kind != "text" || b.Kind != "text" {
		return a.Compare(b)
	}
	return collate.Compare(ruleName, a.Text, b.Text)
}

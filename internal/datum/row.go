package datum

import "sort"

type Row map[string]Value

func Clone(row Row) Row {
	if row == nil {
		return nil
	}
	c := make(Row, len(row))
	for k, v := range row {
		c[k] = v
	}
	return c
}
func Keys(rows map[string]Row) []string {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func Order(a, b Value) int {
	if a.Null() && b.Null() {
		return 0
	}
	if a.Null() {
		return -1
	}
	if b.Null() {
		return 1
	}
	n, _ := a.Compare(b)
	return n
}

// RuleOrder orders values under a declared text collation while keeping the
// null-first convention shared by every query and index. A rule lookup can
// only fail for text values; such failures are impossible on validated
// catalogs, where callers resolve the rule from the owning column.
func RuleOrder(a, b Value, ruleName string) int {
	if a.Null() && b.Null() {
		return 0
	}
	if a.Null() {
		return -1
	}
	if b.Null() {
		return 1
	}
	n, err := TextCompare(a, b, ruleName)
	if err != nil {
		return Order(a, b)
	}
	return n
}

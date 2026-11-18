package engine

import (
	"lamina/internal/catalog"
	"lamina/internal/datum"
	"lamina/internal/index"
	"lamina/internal/journal"
)

type seenValue struct {
	key string
	v   datum.Value
}

func (d *DB) unique(table string, writes map[string]map[string]staged) error {
	rows := d.store.Scan(table, d.sequence)
	overlay(rows, writes, table)
	r, err := d.catalog.Relation(table)
	if err != nil {
		return invalid(err)
	}
	for _, i := range d.catalog.Indexes {
		if i.Relation == table && i.Unique {
			if err := checkUnique(r, rows, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkUnique enforces uniqueness on rule identities rather than raw payloads.
// Nulls remain exempt so several nulls can coexist, matching the documented
// unique-index semantics. Conflict errors name both raw values and the rule
// that made them equivalent, so callers can see why their distinct strings
// were treated as the same value.
func checkUnique(r catalog.Relation, rows map[string]datum.Row, i catalog.Index) error {
	values := map[string]seenValue{}
	for key, row := range rows {
		v := row[i.Column]
		if v.Null() {
			continue
		}
		token, err := r.Token(i.Column, v)
		if err != nil {
			return invalid(err)
		}
		if prior, exists := values[token]; exists && prior.key != key {
			return fault("conflict", "unique constraint %s violated: values %s and %s share identity under collation %q",
				i.Name, displayValue(prior.v), displayValue(v), r.RuleOf(i.Column))
		}
		values[token] = seenValue{key: key, v: v}
	}
	return nil
}

// displayValue renders a non-null value for conflict messages across all kinds.
func displayValue(v datum.Value) string {
	b, err := v.MarshalJSON()
	if err != nil {
		return "<unencodable>"
	}
	return string(b)
}

func (d *DB) CreateRelation(r catalog.Relation) (catalog.Relation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.usable(); err != nil {
		return r, err
	}
	if err := r.Validate(); err != nil {
		return r, invalid(err)
	}
	if _, exists := d.catalog.Relations[r.Name]; exists {
		return r, fault("conflict", "relation exists")
	}
	r.Created = d.sequence + 1
	if err := d.publish(journal.Record{Kind: "relation", Relation: &r}); err != nil {
		return r, err
	}
	return r, nil
}
func (d *DB) CreateIndex(i catalog.Index) (catalog.Index, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.usable(); err != nil {
		return i, err
	}
	if err := d.catalog.CheckIndex(i); err != nil {
		return i, invalid(err)
	}
	if i.Unique {
		r, err := d.catalog.Relation(i.Relation)
		if err != nil {
			return i, invalid(err)
		}
		if err := checkUnique(r, d.store.Scan(i.Relation, d.sequence), i); err != nil {
			return i, err
		}
	}
	i.Created = d.sequence + 1
	if err := d.publish(journal.Record{Kind: "index", Index: &i}); err != nil {
		return i, err
	}
	d.indexes = index.Build(d.catalog, d.store)
	return i, nil
}

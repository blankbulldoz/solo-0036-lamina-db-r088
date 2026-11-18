package snapshot

import (
	"lamina/internal/catalog"
	"lamina/internal/collate"
	"lamina/internal/datum"
	"lamina/internal/versions"
	"strings"
	"testing"
)

func nocaseCatalog() *catalog.Catalog {
	rel := catalog.Relation{
		Name:    "tags",
		Primary: "code",
		Columns: []catalog.Column{{Name: "code", Type: "text", Collation: "nocase"}, {Name: "note", Type: "text", Nullable: true}},
		Created: 1,
	}
	cat := catalog.New()
	cat.Relations["tags"] = rel
	return cat
}

func TestRoundTripCarriesRuleManifest(t *testing.T) {
	cat := nocaseCatalog()
	store := versions.New()
	store.Ensure("tags")
	store.Apply(2, []versions.Mutation{
		{Relation: "tags", Key: mustKey(t, cat.Relations["tags"], datum.Row{"code": datum.Value{Kind: "text", Text: "Alpha"}}),
			Row: datum.Row{"code": datum.Value{Kind: "text", Text: "Alpha"}, "note": datum.Value{Kind: "null"}}},
	})
	image := Image{Sequence: 2, Catalog: cat, Store: store}
	encoded, err := Encode(image)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("current image must decode: %v", err)
	}
	if decoded.Format != 2 {
		t.Fatalf("expected current image format 2, got %d", decoded.Format)
	}
	rules := map[string]int{}
	for _, entry := range decoded.Rules {
		rules[entry.Name] = entry.Version
	}
	if rules["nocase"] != 1 || rules["byte"] != 1 || len(rules) != 2 {
		t.Fatalf("rule manifest not carried correctly: %+v", decoded.Rules)
	}
}

func TestRejectsUnknownRuleVersion(t *testing.T) {
	image := Image{
		Format:   2,
		Sequence: 2,
		Rules:    []RuleEntry{{Name: "nocase", Version: 99}},
		Catalog:  nocaseCatalog(),
		Store:    emptyStore(),
	}
	encoded, err := EncodeRaw(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(encoded); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("expected version-mismatch refusal, got %v", err)
	}
}

func TestRejectsUnknownRuleName(t *testing.T) {
	image := Image{
		Format:   2,
		Sequence: 2,
		Rules:    []RuleEntry{{Name: "mystery", Version: 1}},
		Catalog:  nocaseCatalog(),
		Store:    emptyStore(),
	}
	encoded, err := EncodeRaw(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(encoded); err == nil || !strings.Contains(err.Error(), "unsupported collation") {
		t.Fatalf("expected unknown-rule refusal, got %v", err)
	}
}

func TestRejectsMissingManifestEntry(t *testing.T) {
	image := Image{Format: 2, Sequence: 2, Rules: nil, Catalog: nocaseCatalog(), Store: emptyStore()}
	encoded, err := EncodeRaw(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(encoded); err == nil || !strings.Contains(err.Error(), "missing manifest") {
		t.Fatalf("expected missing-manifest refusal, got %v", err)
	}
}

func TestRejectsSilentIdentityMerge(t *testing.T) {
	// Simulate an image written by a hypothetical binary whose nocase folded
	// two stored keys differently: store rows under byte-distinct keys that a
	// current nocase rule would collapse. The audit must refuse rather than
	// alias the primary keys.
	cat := nocaseCatalog()
	store := versions.New()
	store.Ensure("tags")
	store.Apply(2, []versions.Mutation{
		{Relation: "tags", Key: "text:\"Alpha\"", Row: datum.Row{"code": datum.Value{Kind: "text", Text: "Alpha"}, "note": datum.Value{Kind: "null"}}},
		{Relation: "tags", Key: "text:\"alpha\"", Row: datum.Row{"code": datum.Value{Kind: "text", Text: "alpha"}, "note": datum.Value{Kind: "null"}}},
	})
	image := Image{Format: 2, Sequence: 2, Rules: []RuleEntry{
		{Name: collate.Nocase, Version: 1},
		{Name: collate.Byte, Version: 1},
	}, Catalog: cat, Store: store}
	encoded, err := EncodeRaw(image)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decode(encoded)
	if err == nil || !strings.Contains(err.Error(), "rule conflict") {
		t.Fatalf("expected rule conflict refusal instead of silent merge, got %v", err)
	}
}

func TestLegacyImageDefaultsToByteRule(t *testing.T) {
	rel := catalog.Relation{Name: "old", Primary: "code", Columns: []catalog.Column{{Name: "code", Type: "text"}}, Created: 1}
	cat := catalog.New()
	cat.Relations["old"] = rel
	store := versions.New()
	store.Ensure("old")
	store.Apply(2, []versions.Mutation{
		{Relation: "old", Key: "text:\"A\"", Row: datum.Row{"code": datum.Value{Kind: "text", Text: "A"}}},
		{Relation: "old", Key: "text:\"a\"", Row: datum.Row{"code": datum.Value{Kind: "text", Text: "a"}}},
	})
	image := Image{Format: 1, Sequence: 2, Catalog: cat, Store: store}
	encoded, err := EncodeLegacy(image)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("legacy image must validate under byte semantics: %v", err)
	}
	if len(decoded.Store.Tables["old"]) != 2 {
		t.Fatal("byte semantics must preserve both case variants")
	}
}

func emptyStore() *versions.Store {
	s := versions.New()
	s.Ensure("tags")
	return s
}

func mustKey(t *testing.T, r catalog.Relation, row datum.Row) string {
	t.Helper()
	k, err := r.Key(row)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

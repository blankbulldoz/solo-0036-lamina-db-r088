package engine

import (
	"lamina/internal/catalog"
	"lamina/internal/datum"
	"lamina/internal/query"
	"lamina/internal/snapshot"
	"lamina/internal/versions"
	"path/filepath"
	"testing"
)

func nocaseRelation() catalog.Relation {
	return catalog.Relation{
		Name:    "tags",
		Primary: "code",
		Columns: []catalog.Column{
			{Name: "code", Type: "text", Collation: "nocase"},
			{Name: "label", Type: "text", Nullable: true, Collation: "nocase"},
		},
	}
}

func openTempDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db, dir
}

func mustCommit(t *testing.T, db *DB, fn func(tx string)) {
	t.Helper()
	tx, err := db.Begin(false)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	fn(tx.ID)
	if _, err := db.Commit(tx.ID); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestNocasePrimaryIdentityAndOrdering(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	if _, err := db.CreateRelation(nocaseRelation()); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "tags", datum.Row{"code": textV("Alpha"), "label": textV("first")}, false); err != nil {
			t.Fatal(err)
		}
	})
	// A differently-cased primary is the same identity: this replaces the row.
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "tags", datum.Row{"code": textV("ALPHA"), "label": textV("second")}, false); err != nil {
			t.Fatal(err)
		}
	})
	res, err := db.Query(query.Request{Relation: "tags"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("case variants must be one primary, got %d rows: %v", len(res.Rows), res.Rows)
	}
	// The stored raw payload is the latest spelling, never a folded rewrite.
	if res.Rows[0]["code"].Text != "ALPHA" {
		t.Fatalf("raw payload was rewritten: %q", res.Rows[0]["code"].Text)
	}
	// Equality retrieval works under the rule regardless of requested case.
	res, err = db.Query(query.Request{Relation: "tags", Where: []query.Predicate{{Column: "code", Op: "=", Value: textV("alpha")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("case-insensitive equality lookup failed: %v", res.Rows)
	}
	// Unicode-equivalent primary is the same key.
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "tags", datum.Row{"code": textV("café"), "label": textV("nfc")}, false); err != nil {
			t.Fatal(err)
		}
	})
	lookup, err := db.Query(query.Request{Relation: "tags", Where: []query.Predicate{{Column: "code", Op: "=", Value: textV("café")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(lookup.Rows) != 1 {
		t.Fatalf("NFC-equivalent primary lookup failed: %v", lookup.Rows)
	}
}

func TestNocaseUniqueContention(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	rel := nocaseRelation()
	if _, err := db.CreateRelation(rel); err != nil {
		t.Fatal(err)
	}
	idx := catalog.Index{Name: "by_label", Relation: "tags", Column: "label", Unique: true}
	if _, err := db.CreateIndex(idx); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "tags", datum.Row{"code": textV("k1"), "label": textV("shared")}, false); err != nil {
			t.Fatal(err)
		}
	})
	// Snapshot isolation means two staged writers cannot see each other's
	// private writes. The first to commit wins; the second must be rejected at
	// commit time against the committed final state for the folded identity.
	txA, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(txA.ID, "tags", datum.Row{"code": textV("k2"), "label": textV("Contended")}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Commit(txA.ID); err != nil {
		t.Fatalf("first contender should commit: %v", err)
	}
	txB, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(txB.ID, "tags", datum.Row{"code": textV("k3"), "label": textV("CONTENDED")}, false); err != nil {
		t.Fatal(err)
	}
	_, err = db.Commit(txB.ID)
	if err == nil {
		t.Fatal("second contender for the folded identity must lose")
	}
	if e, ok := err.(*Error); !ok || e.Code != "conflict" {
		t.Fatalf("expected conflict code, got %v", err)
	}
	// A single transaction cannot itself insert two keys sharing one identity.
	txC, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(txC.ID, "tags", datum.Row{"code": textV("k4"), "label": textV("dup-a")}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(txC.ID, "tags", datum.Row{"code": textV("k5"), "label": textV("DUP-A")}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Commit(txC.ID); err == nil {
		t.Fatal("same-transaction equivalent unique values must be rejected")
	}
	// Index creation on a fresh relation must reject pre-existing equivalence
	// duplicates that were inserted without a unique index present.
	dupRel := catalog.Relation{
		Name:    "dupcheck",
		Primary: "code",
		Columns: []catalog.Column{
			{Name: "code", Type: "text", Collation: "nocase"},
			{Name: "label", Type: "text", Nullable: true, Collation: "nocase"},
		},
	}
	if _, err := db.CreateRelation(dupRel); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "dupcheck", datum.Row{"code": textV("k6"), "label": textV("uniq-a")}, false); err != nil {
			t.Fatal(err)
		}
		if err := db.Put(tx, "dupcheck", datum.Row{"code": textV("k7"), "label": textV("UNIQ-A")}, false); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := db.CreateIndex(catalog.Index{Name: "dup_idx", Relation: "dupcheck", Column: "label", Unique: true}); err == nil {
		t.Fatal("unique index build over equivalent values must be rejected")
	}
	// A non-unique index over the same data must build successfully.
	if _, err := db.CreateIndex(catalog.Index{Name: "dup_idx2", Relation: "dupcheck", Column: "label", Unique: false}); err != nil {
		t.Fatalf("non-unique index should build: %v", err)
	}
}

func TestByteRelationKeepsCaseDistinct(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	rel := catalog.Relation{
		Name:    "raw",
		Primary: "code",
		Columns: []catalog.Column{{Name: "code", Type: "text"}, {Name: "v", Type: "text", Nullable: true}},
	}
	if _, err := db.CreateRelation(rel); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "raw", datum.Row{"code": textV("Alpha"), "v": textV("x")}, false); err != nil {
			t.Fatal(err)
		}
		if err := db.Put(tx, "raw", datum.Row{"code": textV("alpha"), "v": textV("y")}, false); err != nil {
			t.Fatal(err)
		}
	})
	res, err := db.Query(query.Request{Relation: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("byte relation must keep case variants as two rows, got %d", len(res.Rows))
	}
	res, err = db.Query(query.Request{Relation: "raw", Where: []query.Predicate{{Column: "code", Op: "=", Value: textV("Alpha")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["v"].Text != "x" {
		t.Fatalf("byte equality lost precision: %v", res.Rows)
	}
}

func TestNocaseBackupRestoreRoundTrip(t *testing.T) {
	db, dir := openTempDB(t)
	defer db.Close()
	if _, err := db.CreateRelation(nocaseRelation()); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		for _, code := range []string{"Alpha", "beta", "Café"} {
			if err := db.Put(tx, "tags", datum.Row{"code": textV(code), "label": textV("v-" + code)}, false); err != nil {
				t.Fatal(err)
			}
		}
	})
	backup, err := db.Backup()
	if err != nil {
		t.Fatal(err)
	}
	restoreDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(restoreDir, backup); err != nil {
		t.Fatalf("restore: %v", err)
	}
	reopened, err := Open(restoreDir)
	if err != nil {
		t.Fatalf("reopen restored: %v", err)
	}
	defer reopened.Close()
	res, err := reopened.Query(query.Request{Relation: "tags", Where: []query.Predicate{{Column: "code", Op: "=", Value: textV("ALPHA")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("restored image lost collation identity: %v", res.Rows)
	}
	// Restore continues to accept writes under the same identity rule.
	mustCommit(t, reopened, func(tx string) {
		if err := reopened.Put(tx, "tags", datum.Row{"code": textV("ALPHA"), "label": textV("overwritten")}, false); err != nil {
			t.Fatal(err)
		}
	})
	res, _ = reopened.Query(query.Request{Relation: "tags"})
	if len(res.Rows) != 3 {
		t.Fatalf("post-restore write changed row count: %d", len(res.Rows))
	}
	_ = dir
}

func TestLegacyImageReadsWithByteSemantics(t *testing.T) {
	// Hand-build a format-1 (pre-collation) image whose store holds two rows
	// that only differ by case. A current binary must read them as distinct
	// byte identities and rebuild indexes without dropping either row.
	rel := catalog.Relation{
		Name:    "legacy",
		Primary: "code",
		Columns: []catalog.Column{{Name: "code", Type: "text"}, {Name: "note", Type: "text", Nullable: true}},
		Created: 1,
	}
	cat := catalog.New()
	cat.Relations["legacy"] = rel
	store := versions.New()
	store.Ensure("legacy")
	store.Apply(2, []versions.Mutation{
		{Relation: "legacy", Key: textV("Alpha").Key(), Row: datum.Row{"code": textV("Alpha"), "note": textV("note-alpha")}},
		{Relation: "legacy", Key: textV("alpha").Key(), Row: datum.Row{"code": textV("alpha"), "note": textV("note-alpha2")}},
	})
	image := snapshot.Image{Format: 1, Sequence: 2, Horizon: 0, Catalog: cat, Store: store}
	encoded, err := snapshot.EncodeLegacy(image)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "legacy-restore")
	if err := Restore(dir, encoded); err != nil {
		t.Fatalf("legacy restore: %v", err)
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	defer db.Close()
	res, err := db.Query(query.Request{Relation: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("legacy byte semantics must keep both case variants, got %d rows", len(res.Rows))
	}
	if _, err := db.CreateIndex(catalog.Index{Name: "by_note", Relation: "legacy", Column: "note"}); err != nil {
		t.Fatal(err)
	}
	res, err = db.Query(query.Request{Relation: "legacy", Where: []query.Predicate{{Column: "code", Op: "=", Value: textV("Alpha")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("legacy exact-case lookup failed: %v", res.Rows)
	}
}

func textV(s string) datum.Value { return datum.Value{Kind: "text", Text: s} }

func TestNocaseIndexScanConsistencyAndOrdering(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	rel := catalog.Relation{
		Name:    "words",
		Primary: "id",
		Columns: []catalog.Column{
			{Name: "id", Type: "int"},
			{Name: "word", Type: "text", Collation: "nocase"},
		},
	}
	if _, err := db.CreateRelation(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateIndex(catalog.Index{Name: "by_word", Relation: "words", Column: "word"}); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		for id, w := range map[int]string{1: "Banana", 2: "apple", 3: "APPLE", 4: "cherry"} {
			if err := db.Put(tx, "words", datum.Row{"id": datum.Integer(int64(id)), "word": textV(w)}, false); err != nil {
				t.Fatal(err)
			}
		}
	})
	pred := []query.Predicate{{Column: "word", Op: "=", Value: textV("aPpLe")}}
	indexed, err := db.Query(query.Request{Relation: "words", Where: pred})
	if err != nil {
		t.Fatal(err)
	}
	scanned, err := db.Query(query.Request{Relation: "words", Where: pred, ForceScan: true})
	if err != nil {
		t.Fatal(err)
	}
	if indexed.Plan.Access != "index" {
		t.Fatalf("expected index access, got %s", indexed.Plan.Access)
	}
	if len(indexed.Rows) != 2 || len(scanned.Rows) != 2 {
		t.Fatalf("equality must find both apple spellings via both paths: index=%d scan=%d", len(indexed.Rows), len(scanned.Rows))
	}
	// Range probe must include whole identity groups and respect identity order:
	// >= folded "banana" excludes both apples and includes banana and cherry.
	ge, err := db.Query(query.Request{Relation: "words", Where: []query.Predicate{{Column: "word", Op: ">=", Value: textV("BANANA")}}})
	if err != nil {
		t.Fatal(err)
	}
	if ge.Matched != 2 {
		t.Fatalf("range >= banana should match banana+cherry (2), got %d", ge.Matched)
	}
	// <= folded "apple" keeps the whole apple identity group (both spellings).
	le, err := db.Query(query.Request{Relation: "words", Where: []query.Predicate{{Column: "word", Op: "<=", Value: textV("Apple")}}})
	if err != nil {
		t.Fatal(err)
	}
	if le.Matched != 2 {
		t.Fatalf("range <= apple should keep the whole equivalent group (2), got %d", le.Matched)
	}
	// Rule-aware ordering groups case variants together.
	ordered, err := db.Query(query.Request{Relation: "words", Order: []query.Ordering{{Column: "word"}}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(ordered.Rows))
	for _, row := range ordered.Rows {
		got = append(got, row["word"].Text)
	}
	// Both apples share a folded identity and group first; inside the group raw
	// bytes decide (uppercase APPLE before lowercase apple), then banana, cherry.
	want := []string{"APPLE", "apple", "Banana", "cherry"}
	if len(got) != len(want) {
		t.Fatalf("ordering length mismatch: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordering = %v, want %v", got, want)
		}
	}
}

func TestNocasePrimaryRejectsInsertOnlyEquivalent(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	if _, err := db.CreateRelation(nocaseRelation()); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, db, func(tx string) {
		if err := db.Put(tx, "tags", datum.Row{"code": textV("Gamma"), "label": textV("x")}, false); err != nil {
			t.Fatal(err)
		}
	})
	tx, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Put(tx.ID, "tags", datum.Row{"code": textV("GAMMA"), "label": textV("y")}, true)
	if err == nil {
		t.Fatal("insert_only must reject an equivalent existing primary")
	}
	if e, ok := err.(*Error); !ok || e.Code != "conflict" {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestUnknownCollationRejectedAtDDL(t *testing.T) {
	db, _ := openTempDB(t)
	defer db.Close()
	bad := catalog.Relation{
		Name:    "bad",
		Primary: "code",
		Columns: []catalog.Column{{Name: "code", Type: "text", Collation: "no-such-rule"}},
	}
	if _, err := db.CreateRelation(bad); err == nil {
		t.Fatal("unknown collation must be rejected at relation creation")
	}
	badType := catalog.Relation{
		Name:    "bad2",
		Primary: "n",
		Columns: []catalog.Column{{Name: "n", Type: "int", Collation: "nocase"}},
	}
	if _, err := db.CreateRelation(badType); err == nil {
		t.Fatal("collation on a non-text column must be rejected")
	}
}

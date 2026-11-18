package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"lamina/internal/catalog"
	"lamina/internal/collate"
	"lamina/internal/versions"
	"os"
)

const (
	MaxImage      = 128 << 20
	formatLegacy  = 1 // images written before collation existed; all text is byte
	formatCurrent = 2 // images carrying an explicit collation rule manifest
)

// RuleEntry records the frozen implementation version of a collation rule as
// it was used when the image was captured.
type RuleEntry struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type Image struct {
	Format   int              `json:"format"`
	Sequence uint64           `json:"sequence"`
	Horizon  uint64           `json:"horizon"`
	Rules    []RuleEntry      `json:"rules,omitempty"`
	Catalog  *catalog.Catalog `json:"catalog"`
	Store    *versions.Store  `json:"store"`
}
type Envelope struct {
	Format  int             `json:"format"`
	Digest  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func Encode(image Image) ([]byte, error) {
	image.Format = formatCurrent
	image.Rules = manifest(image.Catalog)
	b, err := json.Marshal(image)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return json.Marshal(Envelope{Format: formatCurrent, Digest: hex.EncodeToString(h[:]), Payload: b})
}

// EncodeLegacy writes an image using the historical format-1 envelope and no
// collation manifest. It exists so recovery tests can prove that images
// produced by pre-collation binaries still load under byte semantics. It is
// not used by the live write path.
func EncodeLegacy(image Image) ([]byte, error) {
	image.Format = formatLegacy
	image.Rules = nil
	b, err := json.Marshal(image)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return json.Marshal(Envelope{Format: formatLegacy, Digest: hex.EncodeToString(h[:]), Payload: b})
}
func Decode(b []byte) (Image, error) {
	var image Image
	var env Envelope
	if len(b) > MaxImage {
		return image, fmt.Errorf("image exceeds limit")
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return image, err
	}
	h := sha256.Sum256(env.Payload)
	if env.Format != formatLegacy && env.Format != formatCurrent {
		return image, fmt.Errorf("unsupported image envelope format %d", env.Format)
	}
	if hex.EncodeToString(h[:]) != env.Digest {
		return image, fmt.Errorf("image digest mismatch")
	}
	if err := json.Unmarshal(env.Payload, &image); err != nil {
		return image, err
	}
	if image.Format != formatLegacy && image.Format != formatCurrent {
		return image, fmt.Errorf("unsupported image format %d", image.Format)
	}
	// A legacy image predates collation: every text column implicitly uses the
	// frozen byte rule, which reproduces its exact original semantics.
	if image.Format == formatLegacy {
		image.Rules = nil
	}
	if err := Validate(image); err != nil {
		return image, err
	}
	return image, nil
}

// EncodeRaw marshals an image exactly as supplied inside a valid envelope,
// without imposing the current format or regenerating the manifest. It exists
// solely so tests can construct deliberately malformed historical images and
// prove the decoder refuses them.
func EncodeRaw(image Image) ([]byte, error) {
	b, err := json.Marshal(image)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return json.Marshal(Envelope{Format: image.Format, Digest: hex.EncodeToString(h[:]), Payload: b})
}

func Read(path string) (Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxImage+1))
	if err != nil {
		return Image{}, err
	}
	return Decode(b)
}

// manifest collects the frozen version of every collation rule referenced by
// any text column in the catalog.
func manifest(c *catalog.Catalog) []RuleEntry {
	seen := map[string]bool{}
	out := []RuleEntry{}
	for _, r := range c.List() {
		for _, col := range r.Columns {
			if col.Type != "text" {
				continue
			}
			name := col.Rule()
			if seen[name] {
				continue
			}
			seen[name] = true
			version, ok := collate.Version(name)
			if !ok {
				continue
			}
			out = append(out, RuleEntry{Name: name, Version: version})
		}
	}
	return out
}

// verifyRules refuses images whose declared rule versions this binary does not
// implement, instead of silently reinterpreting them under a newer equivalence
// relation.
func verifyRules(image Image) error {
	if image.Format == formatLegacy {
		return nil
	}
	declared := map[string]int{}
	for _, entry := range image.Rules {
		if !collate.Known(entry.Name) {
			return fmt.Errorf("image uses unsupported collation rule %q", entry.Name)
		}
		current, ok := collate.Version(entry.Name)
		if !ok || current != entry.Version {
			return fmt.Errorf("image requires collation %q version %d, binary implements version %d; refusing to reinterpret stored identities",
				entry.Name, entry.Version, current)
		}
		declared[entry.Name] = entry.Version
	}
	// Every rule actually referenced in the catalog must be present in the
	// manifest, otherwise the image was produced by an unknown writer.
	for _, r := range image.Catalog.List() {
		for _, col := range r.Columns {
			if col.Type != "text" {
				continue
			}
			name := col.Rule()
			if _, ok := declared[name]; !ok {
				return fmt.Errorf("image missing manifest entry for collation %q on %s.%s", name, r.Name, col.Name)
			}
		}
	}
	return nil
}

// auditIdentities recomputes every stored primary identity from raw payloads
// under the declared collation. It fails closed if the recomputation changes a
// key or collapses two distinct stored keys into one, so a rule implementation
// change can never silently merge rows or lose primary keys.
func auditIdentities(image Image) error {
	for name, rows := range image.Store.Tables {
		r, ok := image.Catalog.Relations[name]
		if !ok {
			return fmt.Errorf("image references unknown relation")
		}
		byIdentity := map[string]string{}
		for storedKey, chain := range rows {
			if len(chain) == 0 {
				return fmt.Errorf("empty version chain")
			}
			for _, v := range chain {
				if v.Deleted {
					continue
				}
				row, err := r.Normalize(v.Row)
				if err != nil {
					return err
				}
				derived, err := r.Key(row)
				if err != nil {
					return err
				}
				if derived != storedKey {
					return fmt.Errorf("rule conflict: stored key %q for relation %s no longer matches identity %q under collation %q; refusing to merge identities",
						storedKey, name, derived, r.RuleOf(r.Primary))
				}
				if other, exists := byIdentity[derived]; exists && other != storedKey {
					return fmt.Errorf("rule conflict: collation %q would merge distinct primary keys %q and %q in %s",
						r.RuleOf(r.Primary), other, storedKey, name)
				}
				byIdentity[derived] = storedKey
			}
		}
	}
	return nil
}

func Validate(image Image) error {
	if (image.Format != formatLegacy && image.Format != formatCurrent) || image.Horizon > image.Sequence || image.Catalog == nil || image.Store == nil || image.Catalog.Relations == nil || image.Catalog.Indexes == nil || image.Store.Tables == nil {
		return fmt.Errorf("invalid image header")
	}
	if err := verifyRules(image); err != nil {
		return err
	}
	for name, r := range image.Catalog.Relations {
		if name != r.Name || r.Created == 0 || r.Created > image.Sequence {
			return fmt.Errorf("invalid catalog sequence")
		}
		if err := r.Validate(); err != nil {
			return err
		}
	}
	for name, definition := range image.Catalog.Indexes {
		relation, ok := image.Catalog.Relations[definition.Relation]
		if !ok || name != definition.Name || !catalog.ValidName(name) || definition.Created < relation.Created || definition.Created > image.Sequence {
			return fmt.Errorf("invalid index metadata")
		}
		if _, ok := relation.Column(definition.Column); !ok {
			return fmt.Errorf("invalid index column")
		}
	}
	// Audit rule identities before the structural key check so a rule change
	// that would merge distinct stored keys reports the explicit rule conflict
	// instead of a generic tuple error.
	if err := auditIdentities(image); err != nil {
		return err
	}
	for name, rows := range image.Store.Tables {
		r, ok := image.Catalog.Relations[name]
		if !ok {
			return fmt.Errorf("image references unknown relation")
		}
		for key, chain := range rows {
			var last uint64
			if len(chain) == 0 {
				return fmt.Errorf("empty version chain")
			}
			for _, v := range chain {
				if v.Sequence <= last || v.Sequence > image.Sequence || v.Sequence <= r.Created {
					return fmt.Errorf("invalid version order")
				}
				last = v.Sequence
				if !v.Deleted {
					row, err := r.Normalize(v.Row)
					if err != nil {
						return err
					}
					derived, err := r.Key(row)
					if err != nil {
						return err
					}
					if derived != key {
						return fmt.Errorf("invalid stored tuple")
					}
				}
			}
		}
	}
	for _, definition := range image.Catalog.Indexes {
		if !definition.Unique {
			continue
		}
		relation := image.Catalog.Relations[definition.Relation]
		seen := map[string]bool{}
		for _, chain := range image.Store.Tables[definition.Relation] {
			version, exists := versions.Visible(chain, image.Sequence)
			if !exists {
				continue
			}
			value := version.Row[definition.Column]
			if value.Null() {
				continue
			}
			token, err := relation.Token(definition.Column, value)
			if err != nil {
				return err
			}
			if seen[token] {
				return fmt.Errorf("image violates unique index %s under collation %q", definition.Name, relation.RuleOf(definition.Column))
			}
			seen[token] = true
		}
	}

	return nil
}

// RuleManifest exposes the manifest that would be embedded in a new image.
func RuleManifest(c *catalog.Catalog) []RuleEntry { return manifest(c) }

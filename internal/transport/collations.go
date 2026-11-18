package transport

import (
	"lamina/internal/collate"
	"net/http"
)

// collations exposes the frozen, versioned text comparison rules so callers
// know exactly which rule names and versions a binary supports before declaring
// them on a relation.
func (s *Server) collations(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"rules": collate.Specs()})
}

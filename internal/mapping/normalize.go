// Package mapping applies the mapping configs to ingested rows: rule selection
// (exact > wildcard > fallback), record_ref construction and sign-based summary routing.
package mapping

import (
	"strings"
	"unicode"
)

// Wildcard is the normalised config value that matches any description / amount_description.
const Wildcard = "ANY"

// Normalize maps a config key or a source value onto the shared key space:
// trim, en/em dash -> "-", upper-case, every whitespace run -> "_".
// Hyphens, colons, parentheses and other punctuation are kept because config keys use them
// (OTHER-TRANSACTION, LOST:WAREHOUSE, (FBA)). The same function is applied to both sides,
// so the comparison is symmetric; any other spelling difference is a config defect, not
// something to patch here.
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("–", "-", "—", "-").Replace(s)
	s = strings.ToUpper(s)
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte('_')
			}
			inSpace = true
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

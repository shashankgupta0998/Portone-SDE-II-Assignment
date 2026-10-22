package recon

import "strings"

// splitStatements splits a script on semicolons that are outside single-quoted strings and
// drops empty statements. The scripts in this package contain no dollar-quoting.
func splitStatements(script string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for _, r := range script {
		switch {
		case r == '\'':
			inQuote = !inQuote
			b.WriteRune(r)
		case r == ';' && !inQuote:
			if s := strings.TrimSpace(b.String()); s != "" {
				out = append(out, s)
			}
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" && !onlyComments(s) {
		out = append(out, s)
	}
	return out
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}

// containsParam reports whether a statement references the placeholder p (e.g. "$2").
func containsParam(stmt, p string) bool {
	for i := strings.Index(stmt, p); i >= 0; {
		end := i + len(p)
		if end >= len(stmt) || stmt[end] < '0' || stmt[end] > '9' {
			return true
		}
		j := strings.Index(stmt[end:], p)
		if j < 0 {
			return false
		}
		i = end + j
	}
	return false
}

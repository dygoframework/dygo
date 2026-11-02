package migrate

import "strings"

// Split cuts a migration file into statements at the semicolons outside
// strings, quoted identifiers and comments. Statements that hold only
// comments or white space are dropped. mysql selects MySQL lexing (backslash
// escapes in strings, # comments, backtick identifiers); otherwise Postgres
// lexing applies ($tag$ dollar quoted bodies, E'...' escapes).
//
// MySQL's DELIMITER command belongs to the mysql client, not the server, so
// it is not supported: write a stored routine's body without semicolons, or
// run it from a -- dygo:no-transaction file with the multiStatements DSN
// option.
func Split(src string, mysql bool) []string {
	var out []string
	start, i := 0, 0
	content := false // the current statement has more than comments
	flush := func(end int) {
		if content {
			out = append(out, strings.TrimSpace(src[start:end]))
		}
		start, content = end+1, false
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == ';':
			flush(i)
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-', mysql && c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				i = len(src)
			} else {
				i += end + 4
			}
		case c == '\'':
			content = true
			esc := mysql || (i > 0 && (src[i-1] == 'E' || src[i-1] == 'e') && !identChar(src, i-2))
			i = skipQuoted(src, i, '\'', esc)
		case c == '"', mysql && c == '`':
			content = true
			i = skipQuoted(src, i, c, false)
		case !mysql && c == '$':
			content = true
			if tag, ok := dollarTag(src, i); ok {
				end := strings.Index(src[i+len(tag):], tag)
				if end < 0 {
					i = len(src)
				} else {
					i += len(tag) + end + len(tag)
				}
			} else {
				i++
			}
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				content = true
			}
			i++
		}
	}
	flush(len(src))
	return out
}

// skipQuoted returns the index after the quote that closes the one at i. A
// doubled quote is a literal quote; esc also allows backslash escapes.
func skipQuoted(src string, i int, q byte, esc bool) int {
	i++
	for i < len(src) {
		switch {
		case esc && src[i] == '\\':
			i += 2
		case src[i] == q && i+1 < len(src) && src[i+1] == q:
			i += 2
		case src[i] == q:
			return i + 1
		default:
			i++
		}
	}
	return i
}

// dollarTag reports the $tag$ or $$ that starts at i.
func dollarTag(src string, i int) (string, bool) {
	if identChar(src, i-1) { // $ inside a name, such as a$b
		return "", false
	}
	j := i + 1
	for j < len(src) && (src[j] == '_' || isAlpha(src[j]) || (j > i+1 && isDigit(src[j]))) {
		j++
	}
	if j < len(src) && src[j] == '$' {
		return src[i : j+1], true
	}
	return "", false
}

func identChar(src string, i int) bool {
	if i < 0 || i >= len(src) {
		return false
	}
	c := src[i]
	return c == '_' || isAlpha(c) || isDigit(c) || c == '$'
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

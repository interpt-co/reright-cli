package hookcheck

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

// parseJSString decodes the JavaScript string literal at the start of s and
// returns how many bytes of s it used. Template literals are accepted only
// without ${} interpolation.
func parseJSString(s string) (string, int, bool) {
	if s == "" {
		return "", 0, false
	}
	quote := s[0]
	if quote != '\'' && quote != '"' && quote != '`' {
		return "", 0, false
	}
	var sb strings.Builder
	rs := []rune(s[1:])
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == rune(quote):
			return sb.String(), 1 + len(string(rs[:i+1])), true
		case c == '\n' && quote != '`':
			return "", 0, false
		case c == '$' && quote == '`' && i+1 < len(rs) && rs[i+1] == '{':
			return "", 0, false
		case c != '\\':
			sb.WriteRune(c)
			continue
		}
		i++
		if i >= len(rs) {
			return "", 0, false
		}
		switch e := rs[i]; e {
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'v':
			sb.WriteByte('\v')
		case '0':
			sb.WriteByte(0)
		case '\n':
			// line continuation
		case 'x':
			if i+2 >= len(rs) {
				return "", 0, false
			}
			n, err := strconv.ParseUint(string(rs[i+1:i+3]), 16, 8)
			if err != nil {
				return "", 0, false
			}
			sb.WriteRune(rune(n))
			i += 2
		case 'u':
			r, used, ok := unicodeEscape(rs[i+1:])
			if !ok {
				return "", 0, false
			}
			i += used
			if utf16.IsSurrogate(r) && i+2 < len(rs) && rs[i+1] == '\\' && rs[i+2] == 'u' {
				if low, used2, ok := unicodeEscape(rs[i+3:]); ok {
					if pair := utf16.DecodeRune(r, low); pair != '�' {
						r = pair
						i += 2 + used2
					}
				}
			}
			sb.WriteRune(r)
		default:
			sb.WriteRune(e)
		}
	}
	return "", 0, false
}

// unicodeEscape reads the part after \u: either XXXX or {X...}.
func unicodeEscape(rs []rune) (rune, int, bool) {
	if len(rs) > 0 && rs[0] == '{' {
		end := -1
		for j, c := range rs {
			if c == '}' {
				end = j
				break
			}
		}
		if end < 2 {
			return 0, 0, false
		}
		n, err := strconv.ParseUint(string(rs[1:end]), 16, 32)
		if err != nil {
			return 0, 0, false
		}
		return rune(n), end + 1, true
	}
	if len(rs) < 4 {
		return 0, 0, false
	}
	n, err := strconv.ParseUint(string(rs[:4]), 16, 16)
	if err != nil {
		return 0, 0, false
	}
	return rune(n), 4, true
}

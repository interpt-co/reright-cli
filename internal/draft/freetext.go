package draft

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	fencedCode = regexp.MustCompile("(?s)```.*?(```|$)|~~~.*?(~~~|$)")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
)

// listMarker is a token that only starts a list item or a heading, such as -, *, 1., ## or [x].
func listMarker(tok string) bool {
	switch tok {
	case "[x]", "[X]", "[ ]", "[]":
		return true
	}
	return strings.Trim(tok, "-*+>#|.)0123456789[]") == ""
}

// plainWord reports whether a token, already stripped of surrounding
// punctuation, is an ordinary word: letters only, with at most inner hyphens
// or apostrophes, not camelCase and not ALL CAPS.
func plainWord(w string) bool {
	if w == "" {
		return false
	}
	letters, upper := 0, 0
	prevLower := false
	for i, r := range w {
		switch {
		case unicode.IsLetter(r):
			letters++
			if unicode.IsUpper(r) {
				upper++
				if prevLower && i > 0 {
					return false // camelCase
				}
			}
			prevLower = unicode.IsLower(r)
		case r == '-' || r == '\'' || r == '’':
			prevLower = false
		default:
			return false // digits, _ . / : \ and anything else make an identifier
		}
	}
	if letters == 0 {
		return false
	}
	return !(letters >= 2 && upper == letters)
}

// IsFreeText reports whether text is something a person writes in their own
// words, as opposed to a URL, a version, an id, a file or branch name, code, a
// list of names or anything else with nothing to rewrite. Code, links, email
// addresses, mentions, refs, numbers and identifiers are set aside; capitalised
// words in the middle of a sentence count as names. The text is free text when
// at least 3 plain words remain and they are more than a third of its tokens.
// It has no dependencies outside the standard library so the client hook can use it.
func IsFreeText(text string) bool {
	total := len(strings.Fields(text))
	if total == 0 {
		return false
	}
	stripped := fencedCode.ReplaceAllString(text, " ")
	stripped = inlineCode.ReplaceAllString(stripped, " ")
	words := 0
	for _, line := range strings.Split(stripped, "\n") {
		start := true
		// pending is a capitalised word that opens a sentence. It counts only
		// when another plain word follows in the same sentence, so a list of
		// names, one per item, does not pass as prose.
		pending := 0
		for _, tok := range strings.Fields(line) {
			if listMarker(tok) {
				start, pending = true, 0
				continue
			}
			endsSentence := strings.ContainsAny(tok[len(tok)-1:], ".!?:")
			low := strings.ToLower(tok)
			if strings.Contains(low, "://") || strings.HasPrefix(low, "www.") || strings.ContainsAny(tok, "@#") {
				if endsSentence {
					start, pending = true, 0
				} else {
					start = false
				}
				continue
			}
			w := strings.TrimFunc(tok, func(r rune) bool {
				return unicode.IsPunct(r) || unicode.IsSymbol(r)
			})
			if plainWord(w) {
				first, _ := firstRune(w)
				switch {
				case start && unicode.IsUpper(first) && w != "I":
					pending = 1
				case !unicode.IsUpper(first) || w == "I":
					words += 1 + pending
					pending = 0
				}
			}
			if endsSentence {
				start, pending = true, 0
			} else {
				start = false
			}
		}
	}
	return words >= 3 && words*3 > total
}

func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

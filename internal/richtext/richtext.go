// Package richtext finds links and simple formatting in message text:
// *bold*, _italic_ and web addresses. The desktop app, the CLI and the
// Android app all use it, so a message looks the same everywhere.
//
// The rules follow chat apps: a marker opens only at the start of the text
// or after a space or punctuation, and closes only before the end, a space
// or punctuation, on the same line, around at least one character that is
// not a space. So "snake_case_name" and "2*3*4" stay as they are. Inside a
// web address nothing is formatting.
package richtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is a run of text shown one way.
type Span struct {
	Text   string
	Bold   bool
	Italic bool
	URL    string // set for a link; Text is what it shows
}

// Parse splits text into spans. Joining the spans' Text gives text without
// the formatting markers.
func Parse(text string) []Span {
	var out []Span
	for _, seg := range splitLinks(text) {
		if seg.URL != "" {
			out = append(out, seg)
			continue
		}
		out = append(out, format(seg.Text, false, false)...)
	}
	return merge(out)
}

// Plain is text without its formatting markers.
func Plain(text string) string {
	var b strings.Builder
	for _, s := range Parse(text) {
		b.WriteString(s.Text)
	}
	return b.String()
}

// splitLinks cuts text into links and the text between them.
func splitLinks(text string) []Span {
	var out []Span
	rest := text
	for {
		i := linkStart(rest)
		if i < 0 {
			if rest != "" {
				out = append(out, Span{Text: rest})
			}
			return out
		}
		end := i
		for end < len(rest) {
			r, size := utf8.DecodeRuneInString(rest[end:])
			if unicode.IsSpace(r) {
				break
			}
			end += size
		}
		link := trimLink(rest[i:end])
		if i > 0 {
			out = append(out, Span{Text: rest[:i]})
		}
		url := link
		if strings.HasPrefix(strings.ToLower(link), "www.") {
			url = "https://" + link
		}
		out = append(out, Span{Text: link, URL: url})
		rest = rest[i+len(link):]
	}
}

// linkStart is the index of the first web address in s, or -1. An address
// starts with http://, https:// or www., at the start or after a space or
// an opening bracket, and has something after the prefix.
func linkStart(s string) int {
	lower := strings.ToLower(s)
	best := -1
	for _, prefix := range []string{"https://", "http://", "www."} {
		from := 0
		for {
			j := strings.Index(lower[from:], prefix)
			if j < 0 {
				break
			}
			j += from
			from = j + len(prefix)
			if j > 0 {
				prev, _ := utf8.DecodeLastRuneInString(s[:j])
				if !unicode.IsSpace(prev) && !strings.ContainsRune("([{<\"'", prev) {
					continue
				}
			}
			if after := s[j+len(prefix):]; after == "" || unicode.IsSpace(firstRune(after)) {
				continue
			}
			if best < 0 || j < best {
				best = j
			}
			break
		}
	}
	return best
}

// trimLink drops punctuation that ends a sentence rather than the address,
// and a closing bracket the address did not open.
func trimLink(link string) string {
	for link != "" {
		r, size := utf8.DecodeLastRuneInString(link)
		switch {
		case strings.ContainsRune(".,;:!?'\"", r):
			link = link[:len(link)-size]
		case strings.ContainsRune(")]}>", r):
			open := map[rune]rune{')': '(', ']': '[', '}': '{', '>': '<'}[r]
			if strings.Count(link, string(open)) >= strings.Count(link, string(r)) {
				return link
			}
			link = link[:len(link)-size]
		default:
			return link
		}
	}
	return link
}

// format finds *bold* and _italic_ in text without links.
func format(text string, bold, italic bool) []Span {
	var out []Span
	start := 0 // of the plain text not yet emitted
	for i := 0; i < len(text); {
		c := text[i]
		if (c == '*' || c == '_') && opens(text, i) {
			if j := closing(text, i); j > 0 {
				if i > start {
					out = append(out, Span{Text: text[start:i], Bold: bold, Italic: italic})
				}
				inner := text[i+1 : j]
				out = append(out, format(inner, bold || c == '*', italic || c == '_')...)
				i = j + 1
				start = i
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	if start < len(text) {
		out = append(out, Span{Text: text[start:], Bold: bold, Italic: italic})
	}
	return out
}

// opens reports whether the marker at i may open: after nothing, a space or
// punctuation, and before something that is not a space.
func opens(text string, i int) bool {
	if i > 0 {
		prev, _ := utf8.DecodeLastRuneInString(text[:i])
		if !unicode.IsSpace(prev) && !unicode.IsPunct(prev) {
			return false
		}
	}
	return i+1 < len(text) && !unicode.IsSpace(firstRune(text[i+1:]))
}

// closing is the index of the marker closing the one at i on the same line,
// or -1: the same character, after something that is not a space, and
// before nothing, a space or punctuation.
func closing(text string, i int) int {
	c := text[i]
	for j := i + 2; j < len(text); j++ {
		switch text[j] {
		case '\n':
			return -1
		case c:
			prev, _ := utf8.DecodeLastRuneInString(text[:j])
			if unicode.IsSpace(prev) {
				continue
			}
			if j+1 < len(text) {
				next := firstRune(text[j+1:])
				if !unicode.IsSpace(next) && !unicode.IsPunct(next) {
					continue
				}
			}
			return j
		}
	}
	return -1
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// merge joins neighbouring spans that look the same.
func merge(spans []Span) []Span {
	var out []Span
	for _, s := range spans {
		if s.Text == "" {
			continue
		}
		if n := len(out); n > 0 && s.URL == "" && out[n-1].URL == "" &&
			out[n-1].Bold == s.Bold && out[n-1].Italic == s.Italic {
			out[n-1].Text += s.Text
			continue
		}
		out = append(out, s)
	}
	return out
}

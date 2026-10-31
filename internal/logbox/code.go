package logbox

import (
	"crypto/rand"
	"regexp"
)

// crockford is Crockford's base32 alphabet: no I, L, O or U, so a code
// read aloud or copied by hand is hard to get wrong.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var codeRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// newCode returns a random report code, 40 bits as XXXX-XXXX.
func newCode() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported systems
	}
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	out := make([]byte, 0, 9)
	for i := 7; i >= 0; i-- {
		out = append(out, crockford[(v>>(5*uint(i)))&31])
		if i == 4 {
			out = append(out, '-')
		}
	}
	return string(out)
}

// validCode reports whether s is a report code, so it is safe in a file name.
func validCode(s string) bool { return codeRe.MatchString(s) }

package richtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	b := func(s string) Span { return Span{Text: s, Bold: true} }
	i := func(s string) Span { return Span{Text: s, Italic: true} }
	p := func(s string) Span { return Span{Text: s} }
	link := func(text, url string) Span { return Span{Text: text, URL: url} }

	for _, tc := range []struct {
		in   string
		want []Span
	}{
		{"hello", []Span{p("hello")}},
		{"", nil},
		{"a *bold* move", []Span{p("a "), b("bold"), p(" move")}},
		{"_so_ good", []Span{i("so"), p(" good")}},
		{"*very _nice_ day*", []Span{b("very "), {Text: "nice", Bold: true, Italic: true}, b(" day")}},
		{"(*yes*)", []Span{p("("), b("yes"), p(")")}},
		{"*wow*!", []Span{b("wow"), p("!")}},
		{"snake_case_name", []Span{p("snake_case_name")}},
		{"2*3*4", []Span{p("2*3*4")}},
		{"a * b * c", []Span{p("a * b * c")}},
		{"* not bold*", []Span{p("* not bold*")}},
		{"*not\nbold*", []Span{p("*not\nbold*")}},
		{"**", []Span{p("**")}},
		{"*unclosed", []Span{p("*unclosed")}},
		{"see https://example.com/a_b_c.", []Span{p("see "), link("https://example.com/a_b_c", "https://example.com/a_b_c"), p(".")}},
		{"www.twocan.app", []Span{link("www.twocan.app", "https://www.twocan.app")}},
		{"(https://en.wikipedia.org/wiki/Toucan_(bird))", []Span{p("("), link("https://en.wikipedia.org/wiki/Toucan_(bird)", "https://en.wikipedia.org/wiki/Toucan_(bird)"), p(")")}},
		{"*look* http://a.b *here*", []Span{b("look"), p(" "), link("http://a.b", "http://a.b"), p(" "), b("here")}},
		{"mailto:x and xhttp://no", []Span{p("mailto:x and xhttp://no")}},
		{"https:// alone", []Span{p("https:// alone")}},
		{"Ünïcode *ăîș* ok", []Span{p("Ünïcode "), b("ăîș"), p(" ok")}},
	} {
		assert.Equal(t, tc.want, Parse(tc.in), "%q", tc.in)
	}
}

func TestPlain(t *testing.T) {
	assert.Equal(t, "a bold and italic link https://x.y/_z_", Plain("a *bold* and _italic_ link https://x.y/_z_"))
}

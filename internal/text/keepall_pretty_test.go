package text

import (
	"slices"
	"strings"
	"testing"
)

// TestKeepAllKeepsWordsWhenPretty checks that Pretty, wrapping the last two
// lines of a paragraph again, breaks them only where KeepAll lets it: the
// words of a Hangul sentence stay whole at every width.
func TestKeepAllKeepsWordsWhenPretty(t *testing.T) {
	const sentence = "사용자가 설정한 값은 다시 시작해도 그대로 유지됩니다."
	words := strings.Fields(sentence)
	s := Shared()
	s.UsePretty()
	p := Params{Text: sentence, Style: Style{Size: 14}, KeepAll: true}
	full := s.Layout(p).Width
	moved := false
	for width := full - 2; width > full/4; width -= 3 {
		p.Width, p.Pretty = width, false
		plain := wrappedLines(s.Layout(p))
		p.Pretty = true
		l := s.Layout(p)
		pretty := wrappedLines(l)
		moved = moved || !slices.Equal(plain, pretty)
		for _, line := range pretty {
			for _, part := range strings.Fields(line) {
				if !contains(words, part) {
					t.Fatalf("at %v the line %q breaks a word: %q, without Pretty %q", width, line, pretty, plain)
				}
			}
		}
		for _, line := range l.Lines {
			if line.Width > width+1e-2 {
				t.Fatalf("at %v a line is %v wide: %q", width, line.Width, pretty)
			}
		}
	}
	if !moved {
		t.Error("Pretty moved no word at any width: this test shows nothing")
	}
}

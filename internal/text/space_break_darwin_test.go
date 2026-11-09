package text

import (
	"strings"
	"testing"
)

// TestCoreTextBreaksAtEverySpace checks that a space is a break opportunity
// before any word, also one that opens with a mark of punctuation, which Core
// Text alone keeps with the word before it, as a browser breaks the text.
func TestCoreTextBreaksAtEverySpace(t *testing.T) {
	for _, letter := range []string{"x", "ж"} {
		head := strings.Repeat(letter, 31) + " opens"
		for _, next := range []string{".lprobe", "lprobe0"} {
			text := []rune(head + " " + next + " " + strings.Repeat("y", 30))
			s := newSystem()
			s.mu.Lock()
			e := s.engine().(*coreText)
			style := Style{Size: 11, Family: "monospace"}
			whole := e.shape([]rune(head), style, nil, 0, false, false)
			width := whole[0].runs[0].glyphs[len(whole[0].runs[0].glyphs)-1].X + whole[0].runs[0].glyphs[len(whole[0].runs[0].glyphs)-1].Advance + 0.5
			// The kept text wraps from its one line after the first shaping;
			// whole words wrap at the breaks of the text.
			for _, wholeWords := range []bool{false, true} {
				for pass := range 2 {
					e.resetScratch()
					lines := e.shape(text, style, nil, width, false, wholeWords)
					if len(lines) < 2 || lines[0].end != len([]rune(head))+1 {
						t.Errorf("%s… %s, whole words %v, pass %d, at %v: lines end at %v, not first after %q", letter, next, wholeWords, pass, width, ends(lines), "opens ")
					}
				}
			}
			s.mu.Unlock()
		}
	}
}

func ends(lines []shapedLine) []int {
	out := make([]int, len(lines))
	for i, l := range lines {
		out[i] = l.end
	}
	return out
}

// TestCoreTextBreaksAtTheSpaceBeforeAPointWord checks a whole paragraph whose
// line Core Text alone would end before "opens", keeping it with ".lprobe".
func TestCoreTextBreaksAtTheSpaceBeforeAPointWord(t *testing.T) {
	text := []rune("Window check fixture that reports its tab reports, start values, surface size and a document region, opens .lprobe files, and declares sections, settings and a right sidebar override.")
	for _, family := range []string{"ui-monospace,SFMono-Regular,Menlo,monospace", "Menlo"} {
		for _, wholeWords := range []bool{false, true} {
			s := newSystem()
			s.mu.Lock()
			e := s.engine().(*coreText)
			for pass := range 2 {
				e.resetScratch()
				for _, l := range e.shape(text, Style{Size: 11, Family: family}, nil, 259, false, wholeWords) {
					if l.end < len(text) && string(text[l.end:l.end+7]) == ".lprobe" {
						break
					}
					if l.end < len(text) && string(text[l.end:l.end+5]) == "opens" {
						t.Errorf("%s, whole words %v, pass %d: a line ends before %q though %q fits after it", family, wholeWords, pass, "opens", "opens")
					}
				}
			}
			s.mu.Unlock()
		}
	}
}

package ui

import (
	"strings"
	"testing"
)

// TestKeepAll checks that the text inside an element with KeepAll keeps
// Hangul words whole, and that the text beside it does not inherit it.
func TestKeepAll(t *testing.T) {
	const sentence = "사용자가 설정한 값은 그대로 유지됩니다"
	words := strings.Fields(sentence)
	var inherited, plain bool
	var wrapped *node
	coreNewTester(func(c *context) {
		coreColumn(c).KeepAll().Width(120).AlignItems(Stretch).Children(func() {
			wrapped = coreText(c, sentence)
			inherited = coreText(c, "x").textParams(0).KeepAll
		})
		plain = coreText(c, "x").textParams(0).KeepAll
	}, 400, 300)
	if !inherited || plain {
		t.Errorf("the text inside has it: %v, the text beside: %v", inherited, plain)
	}
	l := wrapped.tl
	if len(l.Lines) < 2 {
		t.Fatalf("one line: %q", string(l.Runes))
	}
	for _, line := range l.Lines {
		for _, part := range strings.Fields(string(l.Runes[line.Start:line.End])) {
			if !strings.Contains(" "+strings.Join(words, " ")+" ", " "+part+" ") {
				t.Errorf("the line %q breaks a word", string(l.Runes[line.Start:line.End]))
			}
		}
	}
}

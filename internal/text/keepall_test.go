package text

import (
	"strconv"
	"strings"
	"testing"
)

// wrappedLines returns the text of each line of l.
func wrappedLines(l *Layout) []string {
	var out []string
	for _, line := range l.Lines {
		out = append(out, string(l.Runes[line.Start:line.End]))
	}
	return out
}

// TestKeepAllKeepsHangulWords checks that KeepAll breaks a line of Hangul
// only between its words, where the line would break inside one without it.
func TestKeepAllKeepsHangulWords(t *testing.T) {
	const sentence = "사용자가 설정한 값은 다시 시작해도 그대로 유지됩니다."
	words := strings.Fields(sentence)
	s := Shared()
	p := Params{Text: sentence, Style: Style{Size: 14}}
	full := s.Layout(p).Width
	broken := false
	for width := full - 2; width > full/4; width -= 3 {
		p.Width, p.KeepAll = width, false
		for _, line := range wrappedLines(s.Layout(p)) {
			for _, part := range strings.Fields(line) {
				broken = broken || !contains(words, part)
			}
		}
		p.KeepAll = true
		l := s.Layout(p)
		for _, line := range wrappedLines(l) {
			for _, part := range strings.Fields(line) {
				if !contains(words, part) {
					t.Fatalf("at %v the line %q breaks a word: %q", width, line, wrappedLines(l))
				}
			}
		}
		for _, line := range l.Lines {
			if line.Width > width+1e-2 {
				t.Fatalf("at %v a line is %v wide: %q", width, line.Width, wrappedLines(l))
			}
		}
	}
	if !broken {
		t.Error("no width breaks a word without KeepAll: this test shows nothing")
	}
}

func contains(words []string, part string) bool {
	for _, w := range words {
		if w == part {
			return true
		}
	}
	return false
}

// TestKeepAllAmongOtherText checks that KeepAll keeps Han and kana whole as
// it does Hangul, and that Latin words, digits and what follows punctuation
// break as they do without it.
func TestKeepAllAmongOtherText(t *testing.T) {
	s := Shared()
	for _, tt := range []struct {
		name, text string
		width      float32
		want       []string
	}{
		{"han", "漢字仮名 交じり文", 70, []string{"漢字仮名 ", "交じり文"}},
		{"latin between hangul", "한글 English 한글", 60, []string{"한글 ", "English ", "한글"}},
		{"digits", "버전 12345 입니다", 60, []string{"버전 ", "12345 ", "입니다"}},
		{"after a comma", "하나,둘,셋", 30, []string{"하나,", "둘,셋"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := s.Layout(Params{Text: tt.text, Style: Style{Size: 14}, Width: tt.width, KeepAll: true})
			if got := wrappedLines(l); strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("lines %q, want %q", got, tt.want)
			}
		})
	}
}

// TestKeepAllLongWord checks what a word wider than the line does: it breaks
// by default, as a word of any script, which is overflow-wrap: break-word in
// CSS, and overflows with NoBreakWords, which is CSS's word-break: keep-all
// alone.
func TestKeepAllLongWord(t *testing.T) {
	s := Shared()
	word := strings.Repeat("가", 30)
	p := Params{Text: "앞 " + word + " 뒤", Style: Style{Size: 14}, Width: 100, KeepAll: true}
	l := s.Layout(p)
	if len(l.Lines) < 4 {
		t.Fatalf("a long word took lines %q", wrappedLines(l))
	}
	for _, line := range l.Lines {
		if line.Width > 100+1e-2 {
			t.Errorf("a line is %v wide: %q", line.Width, wrappedLines(l))
		}
	}
	if got := string(l.Runes[l.Lines[0].Start:l.Lines[0].End]); got != "앞 " {
		// What fits before a long word stays there, as the page does.
		t.Logf("the first line is %q", got)
	}
	p.NoBreakWords = true
	l = s.Layout(p)
	if got := wrappedLines(l); strings.Join(got, "|") != "앞 |"+word+" |뒤" {
		t.Errorf("lines %q", got)
	}
	if l.Lines[1].Width <= 100 {
		t.Errorf("the long word is %v wide", l.Lines[1].Width)
	}
}

// TestKeepAllMinContent checks that the narrowest a text can wrap, which a
// row measures with a width of 1 and NoBreakWords, is its widest word.
func TestKeepAllMinContent(t *testing.T) {
	s := Shared()
	p := Params{Text: "한글 있습니다", Style: Style{Size: 14}, Width: 1, NoBreakWords: true, KeepAll: true}
	l := s.Layout(p)
	if got := wrappedLines(l); strings.Join(got, "|") != "한글 |있습니다" {
		t.Fatalf("lines %q", got)
	}
	word := s.Layout(Params{Text: "있습니다", Style: Style{Size: 14}}).Width
	if l.Width != word {
		t.Errorf("the narrowest is %v wide, the widest word %v", l.Width, word)
	}
}

// TestKeepAllWithTheOtherBreaks checks KeepAll with what else re-breaks lines:
// an indent, a maximum of lines, balance and a span.
func TestKeepAllWithTheOtherBreaks(t *testing.T) {
	s := Shared()
	const text = "사용자가 설정한 값은 다시 시작해도 그대로 유지됩니다"
	words := strings.Fields(text)
	full := s.Layout(Params{Text: text, Style: Style{Size: 14}}).Width
	whole := func(t *testing.T, l *Layout) {
		t.Helper()
		for _, line := range wrappedLines(l) {
			for _, part := range strings.Fields(line) {
				if !contains(words, part) {
					t.Fatalf("the line %q breaks a word: %q", line, wrappedLines(l))
				}
			}
		}
	}
	base := Params{Text: text, Style: Style{Size: 14}, Width: full / 2.5, KeepAll: true}
	t.Run("indent", func(t *testing.T) {
		p := base
		p.Indent = 23
		whole(t, s.Layout(p))
	})
	t.Run("balance", func(t *testing.T) {
		p := base
		p.Balance = true
		whole(t, s.Layout(p))
	})
	t.Run("spans", func(t *testing.T) {
		p := base
		p.Spans = EncodeSpans([]Span{{End: 8, Weight: 700}, {End: len([]rune(text))}})
		whole(t, s.Layout(p))
	})
	t.Run("maximum of lines", func(t *testing.T) {
		p := base
		p.MaxLines = 2
		l := s.Layout(p)
		if len(l.Lines) != 2 || !l.Truncated {
			t.Fatalf("%d lines, truncated %v", len(l.Lines), l.Truncated)
		}
		first := string(l.Runes[l.Lines[0].Start:l.Lines[0].End])
		for _, part := range strings.Fields(first) {
			if !contains(words, part) {
				t.Errorf("the first line %q breaks a word", first)
			}
		}
	})
}

// BenchmarkKeepAll wraps a long paragraph of Hangul, each of whose lines the
// system's engine would break inside a word.
func BenchmarkKeepAll(b *testing.B) {
	text := strings.Repeat("사용자가 설정한 값은 다시 시작해도 그대로 유지됩니다 ", 200)
	for _, bb := range []struct {
		name         string
		width        float32
		keepAll      bool
		noBreakWords bool
	}{
		{"plain", 300, false, false},
		{"keepall", 300, true, false},
		{"keepall-mincontent", 1, true, true},
	} {
		b.Run(bb.name, func(b *testing.B) {
			s := Shared()
			i := 0
			for b.Loop() {
				i++
				// Another text each time, which the system has no layout of.
				s.Layout(Params{Text: text + strconv.Itoa(i), Style: Style{Size: 14}, Width: bb.width, KeepAll: bb.keepAll, NoBreakWords: bb.noBreakWords})
			}
		})
	}
}

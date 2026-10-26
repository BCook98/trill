package demoterm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRenderSanitizesControlsAndBoundsViewport(t *testing.T) {
	evil := "\x1b]52;c;c2VjcmV0\a\x1b[31m\x9b\u202e\r\b\t"
	var e editor
	e.insert(evil)
	rendered := render(snapshot{Text: strings.Repeat("line\n", 10000) + evil}, &e, 80, 24, evil)
	if strings.Contains(rendered, "\x1b]52") || strings.Contains(rendered, "\x1b[31m") || strings.Contains(rendered, "\u202e") || strings.Contains(rendered, "\a") || strings.Contains(rendered, "\b") {
		t.Fatalf("unsafe controls passed through: %q", rendered)
	}
	if !strings.Contains(rendered, "^[]52") || !strings.Contains(rendered, "[U+202E]") || len(rendered) > 5000 {
		t.Fatalf("unsafe or unbounded viewport: %q", rendered)
	}
	if strings.Count(rendered, "line") > 24 {
		t.Fatal("transcript escaped viewport")
	}
}

func TestRenderCursorInBoundsAtAllDimensions(t *testing.T) {
	var e editor
	e.insert("a雪e\u0301🦉\nlonger line\n" + strings.Repeat("x", 100))
	re := regexp.MustCompile(`\x1b\[(\d+);(\d+)H\x1b\[\?25h$`)
	for columns := 1; columns <= 40; columns++ {
		for rows := 1; rows <= 25; rows++ {
			for cursor := 0; cursor <= len(e.text); cursor++ {
				e.cursor = cursor
				got := render(snapshot{Text: "雪\nhello"}, &e, columns, rows, "Demo")
				groups := re.FindStringSubmatch(got)
				if groups == nil {
					t.Fatalf("missing cursor: %q", got)
				}
				row, _ := strconv.Atoi(groups[1])
				col, _ := strconv.Atoi(groups[2])
				if row < 1 || row > rows || col < 1 || col > columns {
					t.Fatal(fmt.Sprintf("%dx%d, cursor %d -> %d,%d", columns, rows, cursor, row, col))
				}
			}
		}
	}
}

func TestLayoutCommonUnicodeWidths(t *testing.T) {
	if got := layoutText("a雪e\u0301🦉", 4); len(got) != 2 || got[0] != "a雪e\u0301" || got[1] != "🦉" {
		t.Fatal(got)
	}
}

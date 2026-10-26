package demoterm

import (
	"fmt"
	"strings"
)

type position struct{ row, column int }

type textLayout struct {
	columns int
	lines   []string
	line    strings.Builder
	column  int
}

func (l *textLayout) newline() {
	l.lines = append(l.lines, l.line.String())
	l.line.Reset()
	l.column = 0
}

func (l *textLayout) append(s string) {
	for _, r := range s {
		width := runeWidth(r)
		if width > l.columns {
			r, width = '?', 1
		}
		if l.column+width > l.columns {
			l.newline()
		}
		l.line.WriteRune(r)
		l.column += width
	}
}

func (l *textLayout) point() position {
	if l.column == l.columns {
		return position{len(l.lines) + 1, 0}
	}
	return position{len(l.lines), l.column}
}

func (l *textLayout) finish() []string {
	l.lines = append(l.lines, l.line.String())
	return l.lines
}

func layoutText(text string, columns int) []string {
	l := textLayout{columns: columns}
	for _, r := range text {
		switch r {
		case '\n':
			l.newline()
		case '\t':
			l.append(strings.Repeat(" ", 4-l.column%4))
		default:
			l.append(safeRune(r))
		}
	}
	return l.finish()
}

func layoutEditor(e *editor, columns int) ([]string, position) {
	l := textLayout{columns: columns}
	l.append("> ")
	cursor := l.point()
	for i, r := range e.text {
		if i == e.cursor {
			cursor = l.point()
		}
		if r == '\n' {
			l.newline()
			l.append("  ")
		} else {
			l.append(safeRune(r))
		}
	}
	if e.cursor == len(e.text) {
		cursor = l.point()
	}
	lines := l.finish()
	if cursor.row == len(lines) {
		lines = append(lines, "")
	}
	return lines, cursor
}

// render produces a fixed viewport, not an ever-growing terminal scrollback.
// All untrusted content passes through safeRune; only this function emits ANSI.
func render(state snapshot, e *editor, columns, rows int, status string) string {
	columns, rows = max(1, min(columns, 1000)), max(1, min(rows, 1000))
	entry, cursor := layoutEditor(e, columns)
	footerHeight := 0
	if rows > 1 {
		footerHeight = 1
	}
	editorHeight := min(len(entry), max(1, rows/3))
	editorHeight = min(editorHeight, rows-footerHeight)
	start := max(0, min(cursor.row-editorHeight+1, len(entry)-editorHeight))
	transcriptHeight := rows - footerHeight - editorHeight
	transcript := layoutText(state.Text, columns)
	if len(transcript) > transcriptHeight {
		transcript = transcript[len(transcript)-transcriptHeight:]
	}
	visible := make([]string, 0, rows)
	for len(visible)+len(transcript) < transcriptHeight {
		visible = append(visible, "")
	}
	visible = append(visible, transcript...)
	visible = append(visible, entry[start:start+editorHeight]...)
	if footerHeight != 0 {
		visible = append(visible, layoutText(status, columns)[0])
	}
	var out strings.Builder
	out.WriteString("\x1b[?25l\x1b[H\x1b[2J")
	for row, line := range visible {
		fmt.Fprintf(&out, "\x1b[%d;1H%s", row+1, line)
	}
	fmt.Fprintf(&out, "\x1b[%d;%dH\x1b[?25h", transcriptHeight+cursor.row-start+1, cursor.column+1)
	return out.String()
}

package demoterm

import "time"

const maxLineBytes = 8 * 1024

type snapshot struct {
	Text          string
	Live          bool
	Columns, Rows int
}

// StreamEditor keeps the demo's literal text editor independent of a PTY.
// It never interprets shell commands; submitted lines go to the demo allowlist.
type StreamEditor struct {
	editor  editor
	decoder decoder
}

func (stream *StreamEditor) Input(data []byte) ([]string, bool) {
	var submitted []string
	for _, key := range stream.decoder.feed(data, time.Now()) {
		switch key.name {
		case "exit":
			return submitted, true
		case "interrupt":
			stream.editor.clear()
		case "enter":
			if !stream.editor.overflow && len(stream.editor.text) > 0 {
				submitted = append(submitted, string(stream.editor.text))
				stream.editor.clear()
			}
		default:
			stream.editor.edit(key)
		}
	}
	return submitted, false
}
func (stream *StreamEditor) Render(text string, live bool, columns, rows int) string {
	return "\x1b[?2004h" + render(snapshot{Text: text, Live: live, Columns: columns, Rows: rows}, &stream.editor, columns, rows, "")
}

package demoterm

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type key struct{ name, text string }

type decoder struct {
	pending []byte
	paste   bool
	skipLF  bool
	since   time.Time
}

const escapeDelay = 100 * time.Millisecond

// feed is incremental: Unicode and escape sequences may span arbitrary reads.
// Paste is literal, including control characters; only the exact end marker
// leaves paste mode. Buffering stays bounded even for unknown escape sequences.
func (d *decoder) feed(input []byte, now time.Time) []key {
	d.pending = append(d.pending, input...)
	var keys []key
	for len(d.pending) != 0 {
		p := d.pending
		if p[0] == 0x1b {
			if d.paste {
				end := "\x1b[201~"
				if len(p) < len(end) && strings.HasPrefix(end, string(p)) {
					break
				}
				if strings.HasPrefix(string(p), end) {
					keys = append(keys, key{name: "paste-end"})
					d.paste = false
					d.pending = p[len(end):]
					d.skipLF = false
					continue
				}
				// Other escapes inside paste are literal data, never key actions.
			} else {
				if len(p) == 1 {
					break
				}
				n := 2
				if p[1] == '[' || p[1] == 'O' {
					n = 0
					for i := 2; i < len(p); i++ {
						if p[i] >= 0x40 && p[i] <= 0x7e {
							n = i + 1
							break
						}
						if i >= 63 {
							n = i + 1
							break
						}
					}
					if n == 0 {
						break
					}
				}
				sequence := string(p[:n])
				k := escapeKey(sequence)
				if k.name == "paste-start" {
					d.paste = true
				}
				keys = append(keys, k)
				d.pending = p[n:]
				d.skipLF = false
				continue
			}
		}
		if !utf8.FullRune(p) {
			break
		}
		r, size := utf8.DecodeRune(p)
		d.pending = p[size:]
		if r == '\n' && d.skipLF {
			d.skipLF = false
			continue
		}
		d.skipLF = r == '\r'
		if d.paste {
			if r == '\r' {
				r = '\n'
			}
			keys = append(keys, key{name: "text", text: string(r)})
			continue
		}
		switch r {
		case '\r', '\n':
			keys = append(keys, key{name: "enter"})
		case 0x7f, '\b':
			keys = append(keys, key{name: "backspace"})
		case 1:
			keys = append(keys, key{name: "home"})
		case 5:
			keys = append(keys, key{name: "end"})
		case 3:
			keys = append(keys, key{name: "interrupt"})
		case 4:
			keys = append(keys, key{name: "exit"})
		case 0x1d:
			keys = append(keys, key{name: "diagnostics"})
		case '\t':
			keys = append(keys, key{name: "note", text: "Tab"})
		default:
			if r < 32 || (r >= 0x80 && r <= 0x9f) {
				keys = append(keys, key{name: "note", text: fmt.Sprintf("Control U+%04X", r)})
			} else {
				keys = append(keys, key{name: "text", text: string(r)})
			}
		}
	}
	if len(d.pending) == 0 {
		d.since = time.Time{}
	} else if len(input) != 0 || d.since.IsZero() {
		d.since = now
	}
	return keys
}

func (d *decoder) flush(now time.Time) []key {
	// Only an Escape-prefixed sequence has an ambiguity timeout. A partial
	// UTF-8 rune must survive idle reads until its remaining bytes arrive.
	if d.paste || len(d.pending) == 0 || d.pending[0] != 0x1b || now.Sub(d.since) < escapeDelay {
		return nil
	}
	p := d.pending
	d.pending = nil
	d.since = time.Time{}
	if string(p) == "\x1b" {
		return []key{{name: "note", text: "Escape"}}
	}
	return []key{{name: "note", text: "Incomplete key " + fmt.Sprintf("%q", string(p))}}
}

func escapeKey(s string) key {
	switch s {
	case "\x1b[A", "\x1bOA":
		return key{name: "up"}
	case "\x1b[B", "\x1bOB":
		return key{name: "down"}
	case "\x1b[C", "\x1bOC":
		return key{name: "right"}
	case "\x1b[D", "\x1bOD":
		return key{name: "left"}
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
		return key{name: "home"}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
		return key{name: "end"}
	case "\x1b[3~":
		return key{name: "delete"}
	case "\x1b[200~":
		return key{name: "paste-start"}
	case "\x1b[201~":
		return key{name: "note", text: "Unexpected paste end"}
	case "\x1b[13;2u", "\x1b[27;2;13~", "\x1b\r":
		return key{name: "newline"}
	default:
		return key{name: "note", text: "Key " + fmt.Sprintf("%q", s)}
	}
}

type editor struct {
	text     []rune
	cursor   int
	bytes    int
	overflow bool
}

func (e *editor) clear() { e.text = nil; e.cursor = 0; e.bytes = 0; e.overflow = false }

func (e *editor) insert(s string) bool {
	if e.overflow || len(s) > maxLineBytes-e.bytes {
		e.overflow = true
		return false
	}
	runes := []rune(s)
	e.text = append(e.text, make([]rune, len(runes))...)
	copy(e.text[e.cursor+len(runes):], e.text[e.cursor:len(e.text)-len(runes)])
	copy(e.text[e.cursor:], runes)
	e.cursor += len(runes)
	e.bytes += len(s)
	return true
}

func (e *editor) edit(k key) (note string) {
	switch k.name {
	case "text":
		if !e.insert(k.text) {
			return "Input limit reached (8 KiB); Ctrl-C clears the draft."
		}
	case "newline":
		if !e.insert("\n") {
			return "Input limit reached (8 KiB)."
		}
	case "backspace":
		if e.cursor > 0 {
			e.bytes -= utf8.RuneLen(e.text[e.cursor-1])
			e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
			e.cursor--
		}
	case "delete":
		if e.cursor < len(e.text) {
			e.bytes -= utf8.RuneLen(e.text[e.cursor])
			e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
		}
	case "left":
		e.cursor = max(0, e.cursor-1)
	case "right":
		e.cursor = min(len(e.text), e.cursor+1)
	case "up", "down":
		e.vertical(k.name == "up")
	case "home":
		e.cursor = 0
	case "end":
		e.cursor = len(e.text)
	}
	return ""
}

func keyDescription(k key) string {
	if k.name == "text" {
		return fmt.Sprintf("Text %q", k.text)
	}
	if k.name == "note" {
		return k.text
	}
	return k.name
}

// width deliberately handles code points, not a full grapheme/shaping engine.
// Combining marks and common wide CJK/emoji are laid out without splitting UTF-8.
func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}

// safeRune converts every terminal control and formatting control to visible
// text. Parent text is plain text: ANSI is never passed through from the API.
func safeRune(r rune) string {
	if r < 32 {
		return "^" + string(r+64)
	}
	if r == 127 {
		return "^?"
	}
	if (r >= 0x80 && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
		return fmt.Sprintf("[U+%04X]", r)
	}
	return string(r)
}

func (e *editor) vertical(up bool) {
	start := e.cursor
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	column := e.cursor - start
	if up {
		if start == 0 {
			return
		}
		end := start - 1
		start = end
		for start > 0 && e.text[start-1] != '\n' {
			start--
		}
		e.cursor = min(start+column, end)
	} else {
		end := e.cursor
		for end < len(e.text) && e.text[end] != '\n' {
			end++
		}
		if end == len(e.text) {
			return
		}
		start = end + 1
		end = start
		for end < len(e.text) && e.text[end] != '\n' {
			end++
		}
		e.cursor = min(start+column, end)
	}
}

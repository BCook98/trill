package demoterm

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func decodeChunks(s string, chunk int) []key {
	var d decoder
	var got []key
	for len(s) != 0 {
		n := min(chunk, len(s))
		got = append(got, d.feed([]byte(s[:n]), time.Now())...)
		s = s[n:]
	}
	return got
}

func TestDecoderChunkBoundaries(t *testing.T) {
	input := "雪🦉\x1b[D\x1b[3~\x1b[H\x1b[F\x1b[A\x1b[B\r\n\t\x03\x04\x1d\x1b[13;2u"
	want := []key{
		{"text", "雪"}, {"text", "🦉"}, {name: "left"}, {name: "delete"}, {name: "home"}, {name: "end"},
		{name: "up"}, {name: "down"}, {name: "enter"}, {"note", "Tab"}, {name: "interrupt"}, {name: "exit"},
		{name: "diagnostics"}, {name: "newline"},
	}
	for chunk := 1; chunk <= len(input); chunk++ {
		if got := decodeChunks(input, chunk); !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk %d: %#v", chunk, got)
		}
	}
}

func TestPasteIsLiteralUntilDeliberateReturn(t *testing.T) {
	input := "\x1b[200~help\r\n雪\n\x03\x04\t\x1b[31m\x1b[201~\r"
	for chunk := 1; chunk < len(input); chunk++ {
		keys := decodeChunks(input, chunk)
		var pasted strings.Builder
		enters := 0
		for _, k := range keys {
			switch k.name {
			case "text":
				pasted.WriteString(k.text)
			case "enter":
				enters++
			case "paste-start", "paste-end":
			default:
				t.Fatalf("chunk %d: paste became action %#v", chunk, k)
			}
		}
		if pasted.String() != "help\n雪\n\x03\x04\t\x1b[31m" || enters != 1 {
			t.Fatalf("chunk %d: %q, %d submits", chunk, pasted.String(), enters)
		}
	}
}

func TestEscapeTimeoutAndUnknownSequences(t *testing.T) {
	now := time.Now()
	var d decoder
	if got := d.feed([]byte("\x1b"), now); len(got) != 0 {
		t.Fatal(got)
	}
	if got := d.flush(now.Add(escapeDelay / 2)); len(got) != 0 {
		t.Fatal(got)
	}
	if got := d.flush(now.Add(escapeDelay)); !reflect.DeepEqual(got, []key{{"note", "Escape"}}) {
		t.Fatal(got)
	}
	got := d.feed([]byte("\x1b[999;2z"), now)
	if len(got) != 1 || got[0].name != "note" {
		t.Fatal(got)
	}
	d.feed([]byte("\x1b["), now)
	if got := d.flush(now.Add(escapeDelay)); len(got) != 1 || got[0].name != "note" {
		t.Fatal(got)
	}
	// An unfinished paste must never time out into normal command input.
	d.feed([]byte("\x1b[200~\x1b[20"), now)
	if got := d.flush(now.Add(time.Hour)); len(got) != 0 || !d.paste {
		t.Fatal(got)
	}
}

func TestEditorUTF8EditingAndBounds(t *testing.T) {
	var e editor
	e.insert("a雪🦉z")
	e.edit(key{name: "left"})
	e.edit(key{name: "backspace"})
	e.edit(key{name: "delete"})
	e.edit(key{name: "home"})
	e.edit(key{name: "delete"})
	e.edit(key{name: "end"})
	e.insert("é")
	if string(e.text) != "雪é" || e.cursor != 2 || e.bytes != 5 {
		t.Fatalf("%+v", e)
	}
	e.clear()
	if !e.insert(strings.Repeat("a", maxLineBytes-2)) || e.insert("雪") || !e.overflow {
		t.Fatal("byte bound incorrect")
	}
	e.clear()
	if !e.insert(strings.Repeat("a", maxLineBytes)) {
		t.Fatal("clear did not reset overflow")
	}
	if e.edit(key{name: "text", text: "x"}) == "" {
		t.Fatal("missing limit diagnostic")
	}
	if e.bytes != maxLineBytes {
		t.Fatal(e.bytes)
	}
}

func TestEditorMultilineArrows(t *testing.T) {
	var e editor
	e.insert("abc\n雪山\nq")
	e.edit(key{name: "up"})
	if e.cursor != 5 {
		t.Fatal(e.cursor)
	}
	e.edit(key{name: "up"})
	if e.cursor != 1 {
		t.Fatal(e.cursor)
	}
	e.edit(key{name: "down"})
	if e.cursor != 5 {
		t.Fatal(e.cursor)
	}
	e.edit(key{name: "down"})
	if e.cursor != 8 {
		t.Fatal(e.cursor)
	}
}

func TestRandomDecoderRemainsBoundedAndEditorValid(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	var d decoder
	var e editor
	for i := 0; i < 10000; i++ {
		data := make([]byte, rng.Intn(80))
		rng.Read(data)
		for _, k := range d.feed(data, time.Now()) {
			e.edit(k)
		}
		if len(d.pending) > 64 || e.bytes > maxLineBytes || !utf8.ValidString(string(e.text)) || e.cursor < 0 || e.cursor > len(e.text) {
			t.Fatalf("unbounded or invalid state at iteration %d", i)
		}
	}
}

func TestDecoderPreservesDelayedUTF8(t *testing.T) {
	for _, text := range []string{"é", "雪", "🦉"} {
		for split := 1; split < len(text); split++ {
			t.Run(text+"/split-"+string(rune('0'+split)), func(t *testing.T) {
				var d decoder
				now := time.Now()
				prefix := []byte(text[:split])
				if got := d.feed(prefix, now); len(got) != 0 {
					t.Fatalf("incomplete rune emitted keys: %#v", got)
				}
				// Transport stalls must not turn an incomplete Unicode scalar into an
				// expired escape diagnostic, even after repeated idle-loop flushes.
				for _, delay := range []time.Duration{escapeDelay, 2 * escapeDelay, time.Hour} {
					if got := d.flush(now.Add(delay)); len(got) != 0 {
						t.Fatalf("UTF-8 expired after %s: %#v", delay, got)
					}
					if string(d.pending) != string(prefix) {
						t.Fatalf("UTF-8 prefix discarded after %s: %x", delay, d.pending)
					}
				}
				got := d.feed(append([]byte(text[split:]), '\r'), now.Add(time.Hour))
				want := []key{{"text", text}, {name: "enter"}}
				if !reflect.DeepEqual(got, want) || len(d.pending) != 0 {
					t.Fatalf("completed delayed rune = %#v, pending %x", got, d.pending)
				}
			})
		}
	}
}

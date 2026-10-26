# Restricted demo terminal child

Call the child from the parent CLI's internal mode:

```go
err := demoterm.Run(ctx, absoluteSocketPath, opaqueSessionID, os.Stdin, os.Stdout)
```

This package only edits and renders. The parent validates and executes every demo command. There is no shell, subprocess launcher, project file reader, TCP transport, proxy lookup, or DNS lookup in this package. No Go dependencies are added.

## Parent IPC contract

The socket is an absolute Unix-domain HTTP socket path. Its containing directory and socket permissions must be private to the companion user. The child sends only these requests, serially:

- `GET /terminal?session=<URL-encoded ID>`
- `POST /terminal` with JSON `{"session":"...","line":"literal editor contents"}`
- `POST /resize` with JSON `{"session":"...","columns":80,"rows":24}`

Every successful response must be HTTP 200 and contain all fields:

```json
{"text":"parent-owned transcript","revision":1,"live":true,"columns":80,"rows":24}
```

The session ID must be nonempty valid UTF-8, at most 256 bytes, without NUL/CR/LF. Text is **plain text**, valid UTF-8 and at most 64 KiB. The complete JSON response is limited to 128 KiB; headers to 16 KiB. Dimensions must be between 1 and 1000. Additional JSON fields are tolerated. Responses, errors and redirects cannot choose a different destination. Redirects are rejected.

An initial snapshot is fetched before commands are enabled. An initial resize follows it; subsequent `SIGWINCH` events coalesce to the most recent local PTY dimensions. Uninitialized zero-size PTYs use 80 × 24; dimensions larger than 1000 are clamped. Idle GET polling runs about every 250 ms and redraws only changed content. The parent must persist its text and task state across child exits: a reconnect starts with a fresh GET and never sends previous input.

A command is sent once as a single POST, including any literal embedded newlines. The child never splits a pasted block into commands. The parent decides whether multiline input is supported. There is no automatic POST retry, including after timeout, connection loss, invalid JSON or a lost response. The draft clears when queued; uncertain results display a warning and state is recovered by GET. Any subsequent submission requires fresh explicit input. Resize retries are safe and bounded; they never replay terminal commands.

`live:false` retains the transcript and polling, displays the stopped state and blocks submissions. A later live snapshot permits input again.

## Keys and display

- UTF-8 insertion, Left/Right, Backspace/Delete and Home/End edit the draft. Up/Down move between logical lines; there is no command history.
- Return submits a nonempty draft. CRLF is treated as one Return.
- Bracketed paste is advertised. Pasted newlines, control bytes and escape sequences remain literal until the exact closing marker and a deliberate Return. CRLF is normalized to LF. Shift+Return (`CSI 13;2u`, `CSI 27;2;13~`, or Escape+Return) inserts a newline without submitting.
- Drafts are limited to 8 KiB. Once exceeded, the entire draft is blocked from submission until Ctrl-C clears it; a truncated command is never sent.
- Ctrl-C clears only the local draft. It does **not** send an interrupt command or stop a parent-owned task.
- Ctrl-D outside bracketed paste exits. It cancels queued/in-flight IPC without retrying it; cancellation cannot undo a mutation already committed by the parent.
- Ctrl-] toggles local key diagnostics. Keys and dimensions appear in an eight-entry bounded display, and no commands are sent in this mode. Ctrl-D still exits. Escape, Tab and unsupported controls otherwise show a diagnostic without executing them.

Output uses a bounded viewport sized to the PTY. All parent text and editor text have control/format characters rendered visibly; ANSI, OSC clipboard/link commands, C1 controls and bidi formatting cannot pass through as terminal instructions. Only renderer-generated ANSI controls are emitted. Unicode editing uses code points, with combining-mark and common CJK/emoji width handling; it is not a full grapheme shaping engine.

## Lifetime and errors

Linux and macOS are supported. Input must be an exclusively owned PTY descriptor. `Run` restores its original termios before cleanup output and disables bracketed paste on ordinary exit, context cancellation, errors, SIGHUP, SIGINT and SIGTERM. It does not close the caller's input/output. Ctrl-D/EOF return nil; cancellation returns an error matching `context.Canceled`/the context deadline; invalid arguments, PTY, initial IPC or output errors return errors. Later IPC failures remain visible while polling continues. Restoration errors are joined with the original error.

One cancellable worker serializes HTTP requests, each with a two-second timeout. Input uses a 25 ms descriptor wait rather than a permanently blocked reader goroutine. Worker and HTTP connections are cleaned up before return. As with any `io.Writer`, a custom writer whose `Write` never returns cannot be interrupted by the context; callers should provide the real Mosh PTY output or a promptly returning writer.

## Checks

From `sidecar`:

```sh
GOTOOLCHAIN=go1.26.10 go test ./demoterm
GOTOOLCHAIN=go1.26.10 go test -race ./demoterm
GOTOOLCHAIN=go1.26.10 go vet ./demoterm
```

Tests use private temporary Unix sockets, pipes and newly allocated PTYs. They never start a shell, Mosh, a device session or a service belonging to the user. Real Mosh and app end-to-end checks belong to the parent integration suite.

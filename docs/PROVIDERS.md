# Herdr integration

Herdr is Trill’s only session provider. All terminal input, frames, resize and scroll commands travel over an authenticated WebSocket on the companion’s HTTPS origin. There is no transport picker or fallback. Update the app and companion together; the app requires the `terminalStream` capability.

## Configure a computer

Install and run Herdr before setting up Trill. The default server is `default`. Trill does not install, start, restart, update or take over a Herdr server.

```sh
./scripts/setup-companion.sh
trill providers --herdr-sessions default,work
trill providers
```

`--herdr-path /absolute/path/herdr` selects an executable. `--herdr-config-home /absolute/path/config-home` pins the directory containing `herdr/`; otherwise setup saves the effective `XDG_CONFIG_HOME` or `~/.config`. Up to sixteen existing named servers can be selected. Configuration location and server name form part of terminal identity.

Changing selection preserves pairings and control permission. It restarts only the owned companion when loaded, releasing phone attachments while Herdr terminals keep running. `trill start` recovers interrupted managed changes.

Existing installations configured for removed providers fail with an explicit migration error. Select `--backends herdr` with the previously installed CLI before upgrading. Preserve rejected old state; use a fresh private setup directory if migration was missed. Removed transport and terminal-lab flags are rejected.

## Available actions

- Browse Spaces in Herdr order, with linked worktrees nested under their primary space, branch labels and desktop focus.
- Open existing terminals directly by selecting a session. The first attachment still asks for terminal consent.
- Rename pane labels and create terminal tabs using native Herdr operations, optionally choosing their starting folder with the Mac folder browser.
- Close a terminal from its menu or long-press menu after confirming that its running program will also close.
- Create a space in an existing absolute Mac directory, or a linked Git worktree from a primary repository space. Herdr chooses checkout placement; repository trust checks remain in force.
- Browse authoritative project/terminal files, upload photos, and preview local web servers.
- Detect coding agents running inside terminals. Launch agents from the terminal rather than through a separate session-creation API.

Archive, resume-history and stop-session actions are absent. Closing a terminal uses native pane closure; Disconnect leaves its program running. Manage history in Herdr. A finished agent turn remains a live idle terminal. Closing a pane does not create a resumable archive.

Native mutations are sent once and confirmed by correlated metadata readback. An uncertain result asks you to inspect Spaces before retrying. Trill never passes `--trust-repository` or runs direct Git mutations against personal repositories.

## Ownership and recovery

Herdr permits one direct controller per terminal. Trill starts its structured controller without takeover. An occupied terminal returns HTTP 409. The same paired device can replace its own tracked lease after a lost response or app restart; another device or external controller cannot be displaced. The desktop Herdr TUI can coexist with the direct controller.

Leaving session detail or backgrounding Trill releases the controller so desktop sizing returns. Programs keep running. Trill retains the last screen and unsent draft in memory. Returning reconnects after cleanup at the phone’s current size; explicit Disconnect cancels that intent. Input is never replayed.

Cleanup has a bounded background allowance. Network loss can prevent confirmation, which stays visible as a warning. Transient failures retry only while foreground and connection intent remains active. A busy terminal requires its current controller to disconnect.

## Metadata

Metadata comes from the selected Herdr servers’ native Unix sockets, with bounded CLI fallback. Socket events invalidate catalog snapshots; periodic refresh covers events not emitted by the installed Herdr version. A failed named server produces a warning while healthy servers remain available; if all fail the refresh fails.

Terminal IDs remain stable when panes move. File roots come from current authoritative cwd and repository provenance, never from caller-supplied paths or matching folder names. The disposable `--herdr-lab-root` mode confines integration probes to a declared temporary directory.

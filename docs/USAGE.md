# Using Trill

After [setup and pairing](SETUP.md), Trill shows work owned by the active computer's providers. Sessions and programs run on the Mac. Opening a terminal attaches to that work; disconnecting leaves it running.

## Computers and connection details

Choose **More → Computers** to see saved computers. **Add computer** opens pairing for another Mac; create its invitation on that Mac with `trill pair`. Select a saved computer to switch. The browser shows its computer switcher only when more than one computer is saved. Trill connects to one computer at a time, and the previous computer's sessions continue running.

Pairings and the active choice are stored in the device-only Keychain. Drafts stay separate by computer and session while the app remains open. **Forget computer** removes that phone's local pairing and drafts and disconnects it; it does not stop sessions or revoke the credential on the Mac. To invalidate that credential, use `trill devices` and `trill revoke DEVICE_ID` on the Mac.

Under **More → Connection details**, you can check the connection, see the address and access mode, adjust terminal text size, and reset remembered terminal/photo approvals. Run `trill status` on the Mac for service status and certificate expiry.

## Find and manage sessions

**Spaces** (or **Sessions** for Xirp-only computers) shows current work grouped by project or Herdr space, with linked worktrees nested beneath their repository. Search by session, project, or branch. Switch to **Agents** to browse detected coding agents across the active computer’s spaces and worktrees, grouped with **Needs attention** first, then **Working**, **Idle**, and other states. Agent rows include their space/worktree and branch; search also matches the agent and parent space. Shell terminals and archived sessions stay out of the Agents view. Selecting an agent opens its existing session; terminal connection and consent work as usual. Pull to refresh. Provider warnings identify unavailable sources; healthy providers remain browsable. Previously loaded metadata may be marked stale when refreshing fails.

Select a session to view details, then tap **Open terminal** to attach. Browsing the list does not open terminals automatically. Read-only installations show metadata; run `trill control --enable` on the Mac to enable terminal access and session changes.

Touch and hold a session or open **Session actions** for the actions its provider supports:

| Action | Effect |
| --- | --- |
| Rename | Changes the Xirp session name or Herdr pane label. |
| Disconnect | Closes Trill's transport; the underlying session keeps running. |
| Archive session | For Xirp, removes it from current lists in Trill and Xirp without stopping it. |
| Stop session | For supported Xirp sessions, asks for confirmation before stopping work. |
| Reopen / Resume | For Xirp, returns an archived live session or resumes ended work when history supports it. |

Herdr does not expose Trill archive, stop, or resume actions. A completed agent turn can still have a live terminal. See the full [provider capability matrix](PROVIDERS.md#available-actions).

To find older Xirp work, open **More → Projects and archive → your project → Archived**. Archived, completed, stopped, and failed sessions appear there. Expand a session and choose **Reopen session**. A live archived session returns without restarting; an ended session requires resumable history. After reopening, tap **Open terminal** to connect.

Projects remain browsable even without current sessions. Project search also matches session and branch context.

### Create work

Choose **New session**, select a project/workspace, and choose an available session type:

- **Coding agent** uses the agent configured for that Xirp project. Enter an optional name and a goal, then choose **New worktree** for a separate branch/checkout or **Project folder** to share its current branch and files. Review the agent approval before starting.
- **Terminal** opens a plain shell without starting a coding agent. Xirp uses the project folder; Herdr creates a native terminal tab in the workspace's default directory. You can start an agent there yourself.

Xirp supports coding-session creation. Herdr supports terminal tabs, new spaces in existing Mac folders, and new worktrees from primary repository spaces through the **+** menu. If an action reports an unconfirmed outcome, refresh and check Sessions before retrying so you do not accidentally duplicate work.

## Terminals, writing, and dictation

On the first terminal connection, review the phone's access prompt. You can remember permission for that pairing, or reset it under **Connection details → Permissions → Ask before opening terminals**. The Mac's control setting is a separate requirement.

For Herdr, leaving a session's detail view or backgrounding Trill automatically returns terminal control and sizing to the Mac. Your programs keep running, and Trill retains its last screen and your draft. Returning to a previously opened session reconnects automatically; **Disconnect** cancels automatic return. If another direct controller has connected meanwhile, Trill reports that the terminal is busy. A cleanup warning means Trill could not confirm that the phone attachment closed.

Use **Keyboard** for direct input, including a hardware keyboard. The key strip provides Return, Escape, Tab, arrows, Ctrl, and one-shot Shift. Tap Shift, then a character or special key. Modifiers reset when the connection pauses or disconnects.

**Write** opens a local draft with the native iPhone keyboard. Use its microphone to dictate, then review the text. If the microphone is missing, enable **Settings → General → Keyboard → Enable Dictation** on the phone.

**Insert** puts the reviewed text into the terminal without adding Return. Use **Return** when ready to submit. Quotes and dashes remain literal. Multiline insertion needs bracketed-paste support from the remote terminal.

Shift+Return sends a bracketed newline when supported, or the negotiated extended key. If neither is available, Trill explains that it did not send Return. It does not change tmux settings.

**Done** keeps your draft while Trill is open and cancels unfinished photo uploads. Drafts are held in memory per computer and session. After an interruption, Trill never automatically retries insertion; check the remote terminal before inserting again.

For Xirp terminal history beyond Mosh's current screen, use tmux copy mode. Available scrollback and copy behavior also depend on your terminal/provider configuration.

### Backgrounding and recovery

When you return from the background, Trill first tries to resume the Mosh connection for each terminal you left open. If it cannot confirm a response, it obtains a fresh connection to the same terminal. Input stays disabled until recovery succeeds. Temporary failures retry with increasing delays while the app is active; **Retry now** can request an earlier attempt.

Disconnecting, archiving, stopping, or forgetting the computer cancels recovery. An access rejection or a session that is no longer attachable needs user action. Unsent drafts are not inserted during recovery.

After iOS evicts the entire app process, explicitly open the terminal again. There is no push service. The Mac and provider must remain running and reachable; physical-device network roaming is still part of release validation.

## Files, images, and videos

Open **Files and browser** in the session toolbar, or **Session actions → Browse files**. The panel appears beside the terminal on a wide screen and in a sheet on a compact screen. Tap the toolbar control again to hide it.

The **Files** tab's **Project** picker browses the session's project/worktree. Herdr file access depends on the authoritative working directory and checkout metadata it supplies. Files are read-only in this interface; use the terminal or your desktop tools to edit them.

Tap UTF-8 source files to read them or images to view them. Entries outside the allowed root cannot be browsed. Switch the picker to **Photos** for images uploaded to this session, including archived sessions.

Tap an MP4, MOV, or M4V file, or touch and hold and choose **Play video**. Native iOS controls provide playback, seeking, and speed. Trill downloads the video over the authenticated connection before playback, with a **100 MiB** limit, then removes the temporary copy when you leave the preview. Codec support follows iOS; H.264 or HEVC in MP4 is recommended. Use matching updated app and companion versions for video support.

## Browser previews

### Static HTML and SVG

Touch and hold an HTML, HTM, or SVG file and choose **Preview in browser**, or use the globe button while reading its source. Static preview reads local styles and images from that file's folder without starting a server.

Scripts and network resources are disabled in static preview. For an interactive app, use a development server instead.

### A development server on your Mac

Start your project's development server in the terminal yourself, then switch the panel to **Browser** and enter an address such as:

```text
localhost:3000
localhost:3000/settings?tab=profile
```

Trill forwards the page and its same-origin assets through the paired connection; the server can stay bound to localhost. The proxy connects to IPv4 loopback (`127.0.0.1`), so a server listening only on `::1` must also accept IPv4 loopback.

Back, Forward, and Reload work within the panel. You can enter an HTTPS URL or choose **Open in preview** on a terminal link. Local previews forward same-origin assets and WebSockets, but do not rewrite hard-coded URLs inside HTML/JavaScript or support external authentication redirects.

Hiding and reopening the panel can retain its page and temporary preview access. Explicit workspace teardown or navigating to an external page releases that access. Local preview authorization lasts 30 minutes; **Reload** renews it when expired. If the server is unavailable, verify its port and startup output on the Mac, then Reload. Reload keeps the failed page's path and query. Opening a preview does not start a development server.

## Add photos to a terminal draft

In **Write**, choose **Add photo**. After approval for the paired computer, the system picker lets you choose an image. Trill:

1. Downsamples it to at most 2048 pixels on its longest edge.
2. Re-encodes it as JPEG without location/EXIF metadata.
3. Uploads it to private companion storage on the Mac.
4. Appends its quoted file path to your draft.

Review the path and message, tap **Insert**, then **Return** when ready. This sends terminal text containing a local file path. The program running in that terminal decides whether and how to use the image.

Photo permission is remembered for that pairing. Reset it under **Connection details → Permissions → Ask before uploading photos**. Resetting permission does not delete existing uploads.

Uploaded files remain on the Mac under `~/.local/share/trill/attachments` by default. They are not deleted when you hide the panel, forget a computer, or uninstall its LaunchAgents. Use the session's **Photos** view to find them and review files on the Mac before manually removing any you no longer need.

For connectivity, pairing, and installation problems, see [setup troubleshooting](SETUP.md#troubleshooting).

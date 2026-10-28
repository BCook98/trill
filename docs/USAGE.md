# Using Trill

After [setup and pairing](SETUP.md), Trill shows work owned by the active computer's providers. Sessions and programs run on the Mac. Opening a terminal attaches to that work; disconnecting leaves it running.

## Computers and connection details

Choose **More → Computers** to see saved computers. **Add computer** opens pairing for another Mac; create its invitation on that Mac with `trill pair`. Select a saved computer to switch. The browser shows its computer switcher only when more than one computer is saved. Trill connects to one computer at a time, and the previous computer's sessions continue running.

Pairings and the active choice are stored in the device-only Keychain. Drafts stay separate by computer and session while the app remains open. **Forget computer** removes that phone's local pairing and drafts and disconnects it; it does not stop sessions or revoke the credential on the Mac. To invalidate that credential, use `trill devices` and `trill revoke DEVICE_ID` on the Mac.

Under **More → Connection details**, you can check the connection, see the address and access mode, adjust terminal text size, and reset remembered terminal/photo approvals. Run `trill status` on the Mac for service status and certificate expiry.

## Find and manage sessions

**Spaces** (or **Sessions** for Xirp-only computers) shows current work grouped by project or Herdr space, with linked worktrees nested beneath their repository. Search by session, project, or branch. Switch to **Agents** to browse detected coding agents across the active computer’s spaces and worktrees, grouped with **Needs attention** first, then **Working**, **Idle**, and other states. Agent rows include their space/worktree and branch; search also matches the agent and parent space. Shell terminals and archived sessions stay out of the Agents view. Selecting an agent opens its existing session; terminal connection and consent work as usual. Pull to refresh. Provider warnings identify unavailable sources; healthy providers remain browsable. Previously loaded metadata may be marked stale when refreshing fails.

Select a live session to open its terminal directly. Read-only installations show metadata; run `trill control --enable` on the Mac to allow terminal access and changes.

Touch and hold a session to open, rename, or close it. **Close terminal…** asks for confirmation and ends that terminal’s running program. **Disconnect** closes the phone attachment and leaves work running. Manage history in Herdr. Spaces remain browsable even without terminals.

### Create work

The **+** menu offers **New session**, **New space**, and **New worktree** when supported. A session is a terminal tab in an existing space. Tap **Folder** to browse folders on the Mac and choose its starting directory, or keep **Space default**. New space uses the same picker. Start a coding agent in that terminal. A space uses an existing absolute directory on the Mac; a worktree uses a primary repository space and a branch name. If creation is unconfirmed, inspect Spaces before trying again.

## Ideas queue

Open **Write & queue → Ideas** in a terminal, or **Spaces → your Space → Ideas queue**, to capture a follow-up without sending input to the agent. Write or dictate a message, optionally add photos, then choose **Queue idea** in the composer (or **Add to queue** from the Space). Photo-only ideas are supported. Ideas are saved on this device per saved computer and Space, including separate queues for worktrees, and survive closing Trill. They are not synced to the Mac or other devices. Forgetting a computer makes its queues inaccessible to a new pairing.

Use **Edit** to drag ideas into order. Tap an idea to edit its text, tap its circle to mark it done, or tap a completed checkmark to return it to the queue. Each idea’s menu also offers **Delete**.

From a terminal’s queue, choose an idea’s **Use in draft** action to append its text and photos to that terminal’s existing draft and return to the Write tab. Review and insert it as usual. The idea remains queued until you mark it done; nothing runs automatically.

## Terminals, writing, and dictation

On the first terminal connection, review the phone's access prompt. You can remember permission for that pairing, or reset it under **Connection details → Permissions → Ask before opening terminals**. The Mac's control setting is a separate requirement.

For Herdr, leaving a session's detail view keeps its connection open for 30 seconds so you can switch back without reconnecting. After that, Trill returns terminal control and sizing to the Mac. Backgrounding Trill or choosing **Disconnect** returns control immediately. Your programs keep running, and Trill retains its last screen and your draft. Returning after the connection closes reconnects automatically; **Disconnect** cancels automatic return. If another direct controller has connected meanwhile, Trill reports that the terminal is busy. A cleanup warning means Trill could not confirm that the phone attachment closed.

Use **Keyboard** for direct input, including a hardware keyboard. The key strip provides Return, Escape, Tab, arrows, Ctrl, and one-shot Shift. Hold Backspace, Tab, or an arrow in the key strip to repeat it using the native button behavior. Tap Shift, then a character or special key. Modifiers reset when the connection pauses or disconnects.

**Write & queue** opens a local draft with the native iPhone keyboard, word suggestions, and dictation. Choose **Insert** to use it now or **Queue idea** to save it for later; the **Ideas** tab holds this Space’s queue. Use its microphone to dictate, then review the text. If the microphone is missing, enable **Settings → General → Keyboard → Enable Dictation** on the phone.

**Insert** puts the reviewed text into the terminal without adding Return. Use **Return** when ready to submit. Quotes and dashes remain literal. Multiline insertion needs bracketed-paste support from the remote terminal.

Shift+Return sends a bracketed newline when supported, or the negotiated extended key. If neither is available, Trill explains that it did not send Return.

**Done** keeps your draft text and selected photos while Trill is open. Queuing an idea saves them persistently on this device. Wait for an active upload to finish before closing the composer. Drafts are held in memory per computer and session. After an interruption, Trill never automatically retries insertion; check the remote terminal before inserting again.

Swipe the terminal to scroll through Herdr’s history. Gestures send explicit scroll commands with their touched cell, preserving mouse-aware applications’ behavior.

### Backgrounding and recovery

Leaving the terminal or backgrounding the app returns control and sizing to Herdr. Returning obtains a fresh HTTPS stream after cleanup. Input remains disabled until connected. Temporary failures retry while foreground with increasing delays; **Retry now** requests an earlier attempt.

Explicit Disconnect or forgetting a computer cancels recovery. An access rejection or closed terminal requires user action. Unsent drafts and terminal input are never replayed. After process eviction, select the live terminal again. Your Mac and Herdr must remain reachable. There is no push service.

Panel visibility follows your last choice while resizing or returning from the background. Opening Files and browser does not automatically hide Sessions.

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

In **Write & queue**, choose **Add photo**. The system picker lets you choose up to six images. Trill downsamples each image to at most 2048 pixels on its longest edge and re-encodes it as JPEG without location/EXIF metadata. Photos stay on this device until you choose **Insert**. **Queue idea** saves the text and photos locally without uploading them.

When you choose **Insert**, Trill requests photo-upload approval for the paired computer if needed, uploads photos to private companion storage, and inserts the draft with their quoted file paths. Use **Return** when ready to submit. The program running in that terminal decides whether and how to use each image.

Photo permission is remembered for that pairing. Reset it under **Connection details → Permissions → Ask before uploading photos**. Resetting permission does not delete existing uploads.

Uploaded files remain on the Mac under `~/.local/share/trill/attachments` by default. They are not deleted when you hide the panel, forget a computer, or uninstall its LaunchAgents. Use the session's **Photos** view to find them and review files on the Mac before manually removing any you no longer need.

For connectivity, pairing, and installation problems, see [setup troubleshooting](SETUP.md#troubleshooting).

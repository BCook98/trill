# Isolated demo session creation

Use a dedicated macOS demo account with a fresh login environment, authorized provider sign-in, disposable projects, and separate companion identity. A different database directory or daemon-port variable alone does not establish isolation from the normal account's provider or tmux state.

## Prepare the environment

1. Sign in to the dedicated account normally. Use supported Xirp/Herdr setup and authorize the real coding agent there. Do not copy another account's credentials, databases, hooks, session state, or daemon discovery files.
2. Register only a disposable copy of the [demo fixtures](../demos/fixtures/README.md). Choose neutral computer, project, and session names. Select nonconflicting provider, companion, preview, and UDP ports; TCP/UDP ports are shared across accounts.
3. Run the normal companion in that account with dedicated TLS and pairing storage. For a real New session demonstration, omit `--tmux-socket`: that flag selects Trill's terminal-lab backend, which rejects session creation.
4. Before recording, verify the provider CLI and companion list only the intended demo projects/sessions. Verify the selected phone/simulator is paired to that companion and the file root is disposable.
5. Create through Trill's real UI only after these checks. Confirm the resulting provider metadata and terminal belong to the intended project. Do not retry an uncertain creation automatically; inspect Sessions first.

Do not extract or customize a packaged provider daemon to bypass normal setup, endpoint discovery, or authorization. Provider compatibility is defined by the installed supported CLI and Trill's [provider boundaries](PROVIDERS.md).

## Choose an honest scenario

If real creation is not validated in the isolated account, begin with an existing authorized disposable session and demonstrate task entry and preview. A genuine shell can demonstrate terminal functionality; show coding-agent work only when that agent is actually running. Do not fabricate backend metadata, progress, transcripts, or a successful creation screen.

The private terminal lab is suitable for transport and UI checks, not proof that provider session creation works. Use [DEMOS.md](DEMOS.md) for current portable runner commands, explicit terminal-input authorization, capture targets, and cleanup.

## Capture and review

Prepare a neutral prompt, tmux status, harmless project files, and demo-only scrollback before recording. Exclude credentials, pairing invitations/QRs, notifications, private repository URLs, personal photos, and unrelated command history. Capture clean actual states instead of retouching app output.

Keep the source/build revision, executable hash, device/runtime, appearance, scenario, and simulator-versus-hardware status with each run. Inspect screenshots and decoded video frames before publishing. Export provenance belongs in the media manifest; publication review results belong in [PUBLICATION_AUDIT.md](PUBLICATION_AUDIT.md). Stop only helpers created for that demo environment.

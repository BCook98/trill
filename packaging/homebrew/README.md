# Trill Homebrew tap

The release builder emits `trill.rb` only after both architecture archives have
been signed, notarized and checksummed. Publish that file as `Casks/trill.rb` in
`BCook98/homebrew-tap`. The release tag remains the same tag used for iOS.

Install the companion with:

```sh
brew install --cask bcook98/tap/trill
trill setup
trill doctor
```

The cask installs the HTTPS-only Trill companion. Users install and run
Herdr separately. Setup records stable Homebrew `bin`
paths; it does not record the versioned Caskroom directory. Homebrew controls
binary upgrades, and `trill update` directs users to `brew upgrade`.

Upgrade with `brew update` followed by `brew upgrade --cask bcook98/tap/trill`.
After an upgrade, restart only the owned companion with `trill stop` and
`trill start`, including the usual `--state-dir` on both commands. Before
removing the cask, run `trill uninstall` with that same state directory to remove
its LaunchAgents, then run `brew uninstall --cask bcook98/tap/trill`. Configuration, pairings and tsnet identity are retained.

Source and standalone installations are not silently converted. Use their
recorded CLI until an explicit migration has been completed. Installing a cask
does not enroll a new tailnet node or create a pairing automatically.

## Publish the tap

Create `BCook98/homebrew-tap` containing the generated `Casks/trill.rb` and a copy
of the updater workflow in this directory. Keep private keys and machine state
out of the tap. The cask is release metadata; all executables stay attached to
the Trill release in `BCook98/trill`.

For later releases, run the tap's **Update Trill cask** workflow after the signed
release is published:

```sh
gh workflow run update-trill.yml --repo BCook98/homebrew-tap
```

It downloads only `trill.rb` from the latest published Trill release and commits
an actual change. Its repository-scoped GitHub token updates its own tap; no new
Apple credentials or cross-repository publishing token are needed. There is no
second release tag and no duplicate Go/Python validation in the tap.

Validate a generated cask with `brew style --cask Casks/trill.rb` and
`brew audit --cask --strict Casks/trill.rb`. First-publication validation must
also exercise install, stable executable links and upgrade guidance using the
signed release that contains Homebrew-aware CLI support.

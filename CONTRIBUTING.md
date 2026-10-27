# Contributing to Trill

Start with [the development guide](docs/DEVELOPMENT.md) for the toolchains, clean-clone commands, native tests, and isolated lab. [README.md](README.md), [SETUP.md](docs/SETUP.md), and [USAGE.md](docs/USAGE.md) describe the product and user setup.

For version preparation and release validation, use [RELEASING.md](docs/RELEASING.md).

Main history was flattened on 25 October 2026, with `5a4fc50` as the new root. If you contributed before the reset, preserve any uncommitted work first, then make a fresh clone and replay only your intended changes. Do not merge an old branch or its history back into `main`.

Keep changes focused and include a regression test when behavior changes. Run the checks for the areas you touched and report the exact commands, results, and skipped integration checks in your pull request. Go, Python tooling, and website checks run in GitHub Actions after a push to `main`, or a maintainer’s manual run on `main`. Native tests run in Xcode Cloud on `main` with the required SDK. Pull requests do not trigger either service; contributors should run the relevant local checks and include their results. Maintainers must review code before merging it, especially workflow changes, build scripts and dependency updates.

Use synthetic files and the owned demo lab for integration work. Do not send input to existing personal agent or Herdr sessions, reuse pairing credentials, or commit local state, invitations, logs with secrets, signing material, or capture artifacts. A lab start, real agent launch, and demo capture are separate opt-in operations; unit tests require none of them.

Website tooling is local to `website/`. Commit both `package.json` and `package-lock.json` when changing dependencies; verify the chosen stable versions against npm and use `npm ci` for validation. Project generation uses the root Gemfile and lockfile. Keep local signing and toolchain selection out of tracked project configuration.

For a bug report, include the revision, OS, tool versions, expected behavior, and a minimal redacted reproduction. For native changes, include the selected SDK/runtime and simulator test result, or explicitly say why native checks could not run.

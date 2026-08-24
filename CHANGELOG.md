# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Added a WASM-embeddable core with exported budget, headroom, and canonical-ID checks.

### Fixed

- `pr-agent` lane: fork-triggered `/` commands are now refused, and the AI
  call's budget fits inside its step. Three defects, one of them only visible
  once the first was fixed.

  The job-level `if:` refused forks on the `pull_request` arm and could not on
  `issue_comment` — fork status is absent from that payload, so there was never
  an expression to write. A `fork gate` step now asks the pulls endpoint and
  fails closed: only a literal `false` proceeds, so a 404, a rate limit or a
  deleted fork all skip. The lane runs no `actions/checkout`, so fork code was
  never executed and no exfiltration path existed; what this closes is the
  comment claiming forks were already skipped, which was true of one arm only.

  `CONFIG__AI_TIMEOUT` was 600s inside a 360s step, so the runner killed the
  step before pr-agent could reach its own timeout or fall back to a secondary
  model. Now 300s.

  Fixing the first exposed a third: `stamp attempt 2 end` runs under
  `if: always()`, so when attempt 2 never ran the verdict subtracted from zero
  and reported a 1787580408-second attempt as a confident TIMED OUT.

  Contributors on forks are affected: a maintainer's `/review` on a fork PR is
  now declined with a warning rather than silently running.
  (wave-av/wave-foundation-public#73)

### Changed

- OTA integrity checking now fails closed: an update whose manifest omits or
  malforms the expected SHA-256 digest is refused instead of installed
  unverified, and the `update_agent` cloud command now requires a `sha256`
  parameter.
- OTA downloads (manifests, components, agent binaries) are confined to
  `https://releases.wave.online/edge` over HTTPS; redirects are re-validated
  against the same origin, so update URLs hosted elsewhere are now rejected.
- OTA downloads are capped at 512 MiB (manifest documents at 1 MiB) and abort
  when the transfer stalls for 2 minutes or exceeds an absolute 1-hour ceiling,
  replacing the previous whole-request timeout so slow but progressing
  downloads can complete while a trickling server cannot park the update loop.
- Component names and versions from manifests and cloud commands are validated
  against the existing identifier allowlist before being used as staging file
  names, so updates with traversal or otherwise unsafe identifiers are rejected.
- Delta updates advertised in a manifest are no longer selected: no patch
  applier exists, so the patch blob would have been installed as if it were the
  full component. The full artifact is always downloaded instead.
- The update staging directory is created root-only (0700) and staged files are
  always created fresh, never written through an existing symlink.

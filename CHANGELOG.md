# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- OTA integrity checking now fails closed: an update whose manifest omits or
  malforms the expected SHA-256 digest is refused instead of installed
  unverified, and the `update_agent` cloud command now requires a `sha256`
  parameter.
- OTA downloads (manifests, components, agent binaries) are confined to
  `https://releases.wave.online/edge` over HTTPS; redirects are re-validated
  against the same origin, so update URLs hosted elsewhere are now rejected.
- OTA downloads are capped at 512 MiB and abort when the transfer stalls for
  2 minutes, replacing the previous whole-request timeout so slow but
  progressing downloads can complete.
- Component names and versions from manifests and cloud commands are validated
  against the existing identifier allowlist before being used as staging file
  names, so updates with traversal or otherwise unsafe identifiers are rejected.

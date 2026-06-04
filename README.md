# wave-agent

**WAVE Agent** is a single-binary, stdlib-only Go daemon that manages WAVE streaming edge devices (Raspberry Pi, RK3328 SBCs, x86_64 servers).

It runs on each device and handles device identity, module lifecycle, configuration, health/metrics, cloud sync, and over-the-air updates.

## Features

- **Device identity** — deterministic MAC-based device ID, persisted across reboots
- **Module lifecycle** — install, start, stop, and health-check WAVE modules (see [wave-modules](https://github.com/wave-av/wave-modules))
- **HTTP API + metrics** — local control API and Prometheus metrics endpoint
- **Cloud connector** — heartbeat, telemetry, and remote commands over a WebSocket to `edge.wave.online`
- **OTA updates** — delta updates, A/B partition swap, SHA-256 verification, rollback
- **Embedded web UI** — built-in dashboard served from the binary

No external Go dependencies — the daemon is stdlib-only to keep the binary small.

## Build

Cross-compile for the supported edge platforms:

```bash
make build-all      # arm64 + armv7 + amd64 → dist/
make build-arm64    # Raspberry Pi 5, Pi Zero 2W (64-bit), RK3328 SBC
make build-armv7    # older 32-bit Pi devices
make build-amd64    # x86_64 servers
make build-darwin   # macOS (development only)
```

Binaries are written to `dist/`. Run `make sizes` to list them.

## Install

On the target device:

```bash
make install        # builds arm64, installs to /usr/local/bin, sets up systemd
sudo systemctl start wave-agent
```

This installs the binary and the [`wave-agent.service`](wave-agent.service) systemd unit, then enables it on boot.

Default runtime layout: config in `/etc/wave`, state in `/var/lib/wave`, modules in `/opt/wave/modules`. The health/metrics endpoint listens on `:9090` and the web UI on `:8080`.

## Status

Version 0.1.0 — early. Core daemon, HTTP API, cloud connector, and OTA paths are in place; expect changes as the edge platform matures.

## See also

- [AGENTS.md](AGENTS.md) — repository conventions
- [CHANGELOG.md](CHANGELOG.md)

## Links
- [wave.online](https://wave.online) · [Docs](https://docs.wave.online) · [Developer portal](https://dev.wave.online)

Operated by WAVE Online, LLC.

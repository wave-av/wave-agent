# wave-agent

Go-based edge device management daemon for WAVE streaming hardware.

## Features

- Device identity (MAC-based deterministic ID)
- Module lifecycle management (install, start, stop, health)
- HTTP API + Prometheus metrics
- Cloud connector (heartbeat, telemetry, remote commands)
- OTA updates (delta, A/B partition, SHA256 verify, rollback)
- Embedded web UI dashboard

## Build

```bash
make build-all    # Cross-compile for arm64/armv7/amd64
make install      # Install on current device + systemd
```

## License

MIT — WAVE Online, LLC

# Runic

Your terminals. Everywhere. A lightweight Go daemon for secure remote terminal access.

## Requirements

Go 1.26 or later.

## Quick start

```bash
make build
./runic setup
./runic start
open https://localhost:8765
```

## How it works

Runic starts a local daemon that exposes PTY-backed shell sessions over authenticated WebSocket connections. It serves a bundled web terminal UI and relays terminal input/output in real time. TLS is enabled by default with self-signed certificates for secure local/remote access.

## Configuration

See [config.example.yaml](./config.example.yaml) and copy values into `~/.config/runic/config.yaml`.

## License

MIT

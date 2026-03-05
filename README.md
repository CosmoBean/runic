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

Detached background mode without installing launchd/systemd:

```bash
./runic start --detach
./runic daemon status
./runic daemon stop
```

## How it works

Runic starts a local daemon that exposes PTY-backed shell sessions over authenticated WebSocket connections. It serves a bundled web terminal UI and relays terminal input/output in real time. TLS is enabled by default with self-signed certificates for secure local/remote access.

## Configuration

See [config.example.yaml](./config.example.yaml) and copy values into `~/.config/runic/config.yaml`.

Session backend defaults to `auto` (uses `tmux` when available, otherwise PTY). You can force `pty` or `tmux` via `sessions.session_mode`.

Security hardening supports:

- `security.allowed_origins` for strict WebSocket origin checks
- `security.trust_proxy_headers` and `security.trusted_proxy_cidrs` for safe `X-Forwarded-For` handling
- optional `auth.require_token` toggle (enabled by default)

## OAuth CLI

Run browser-based OAuth from the CLI:

```bash
runic oauth github --client-id <id> --client-secret <secret>
runic oauth google --client-id <id> --client-secret <secret>
runic oauth status github
runic oauth refresh github --client-id <id> --client-secret <secret>
runic oauth logout github
```

Tokens are stored in the OS keychain by default (use `--no-store` to disable).

You can also use env vars: `RUNIC_GITHUB_CLIENT_ID`, `RUNIC_GITHUB_CLIENT_SECRET`, `RUNIC_GOOGLE_CLIENT_ID`, `RUNIC_GOOGLE_CLIENT_SECRET`.

## Background Service (macOS/Linux)

```bash
runic service install
runic service status
runic service restart
runic service uninstall
```

macOS uses `launchd` (user agent). Linux uses `systemd --user`.

For ad-hoc local background runs (without service install), use:

```bash
runic daemon start
runic daemon status
runic daemon restart
runic daemon stop
```

## Ops Commands

```bash
runic status --json
runic token rotate
runic doctor internet
```

`runic doctor internet` validates localhost bind + internet-facing hardening settings.

## JS Wrapper (npm)

An npm wrapper is available under `npm/` and proxies all CLI commands to the Go binary:

```bash
cd npm
npm install
npx @cosmobean/runic-cli version
npx @cosmobean/runic-cli start
```

The wrapper uses a local `./runic` binary when present; otherwise it downloads the platform binary from releases.

## Cloudflare Access + TOTP

For internet exposure with TOTP while keeping Runic private, use the Cloudflare guide:

- [deploy/cloudflare/README.md](./deploy/cloudflare/README.md)

## License

MIT

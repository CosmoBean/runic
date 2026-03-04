# Cloudflare Internet Setup (Runic)

This setup keeps the Runic daemon private on localhost and exposes access through Cloudflare Access with TOTP.

## 1. Runic hardening baseline

Set these in `~/.config/runic/config.yaml`:

```yaml
server:
  host: 127.0.0.1
  port: 8765

tls:
  mode: none

auth:
  require_token: true

security:
  allowed_origins:
    - https://terminal.example.com
  trust_proxy_headers: true
  trusted_proxy_cidrs:
    - 127.0.0.1/32
    - ::1/128
```

Then restart:

```bash
runic service install
runic service restart
runic doctor internet
```

## 2. Create Cloudflare tunnel

Install and authenticate `cloudflared`, then create tunnel and route DNS:

```bash
cloudflared tunnel login
cloudflared tunnel create runic
cloudflared tunnel route dns runic terminal.example.com
```

Copy `cloudflared-config.example.yml` to your cloudflared config location and fill tunnel ID.

## 3. Enforce Cloudflare Access + TOTP

In Cloudflare Zero Trust:

1. Create a self-hosted application for `terminal.example.com`.
2. Add policy allowing only your identity.
3. Require TOTP/MFA in the policy.
4. Set session duration according to your risk tolerance.

## 4. Run cloudflared as a service

```bash
cloudflared service install
sudo systemctl enable --now cloudflared  # Linux
```

On macOS, use LaunchAgent/service management provided by cloudflared installer.

## 5. Validation

1. `runic status --json` shows localhost bind + token enabled.
2. `runic doctor internet` passes required checks.
3. Visiting `https://terminal.example.com` prompts Cloudflare login + TOTP before Runic UI.
4. Runic token is still required after Access auth.

# Runic JS Wrapper

This package wraps the native `runic` Go daemon binary.

## Usage

```bash
npx @cosmobean/runic-cli version
npx @cosmobean/runic-cli setup
npx @cosmobean/runic-cli start
```

## Environment variables

- `RUNIC_BINARY_PATH`: use an explicit binary path
- `RUNIC_BINARY_URL`: override binary download URL
- `RUNIC_RELEASE_BASE_URL`: override release base URL (`.../latest/download/<asset>`)
- `RUNIC_BINARY_SHA256`: optional checksum verification
- `RUNIC_SKIP_POSTINSTALL=1`: skip postinstall download
- `RUNIC_CACHE_DIR`: override binary cache directory

# Backup Management Center — Deployment

## Layout
- `bin/backup-center-server` — control plane (HTTP API + Web UI, gRPC channel, scheduler)
- `bin/backup-center-agent` — runs on each managed Linux host; outbound-only connection to the server

## Server requirements
- Linux x86_64, systemd
- SQLite database lives in `BMC_DATA_DIR`; back up this directory (it contains the DB, the server instance ID and — when provided at install time — the master key) as an offline copy.

## Server install
```sh
sudo useradd --system --home /var/lib/bmc --shell /usr/sbin/nologin bmc || true
sudo mkdir -p /var/lib/bmc /etc/bmc
# 32-byte master key:
sudo sh -c 'head -c 32 /dev/urandom > /etc/bmc/master.key && chmod 600 /etc/bmc/master.key && chown bmc:bmc /etc/bmc/master.key'
# TLS certificate/key for both HTTPS and agent gRPC:
sudo cp server.crt server.key /etc/bmc/ && sudo chown bmc:bmc /etc/bmc/*
sudo cp deploy/systemd/bmc-server.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now bmc-server
```

Environment (systemd `Environment=` or drop-in):
- `BMC_LISTEN_ADDR` (default `:8080`) HTTP+UI
- `BMC_GRPC_ADDR` (default `:9090`) agent channel
- `BMC_METRICS_ADDR` (default `127.0.0.1:9100`) Prometheus, loopback only
- `BMC_DATA_DIR` (default `./data`)
- `BMC_PUBLIC_URL` external URL
- `BMC_MASTER_KEY_FILE` required in production (32 bytes)
- `BMC_TLS_CERT_FILE` / `BMC_TLS_KEY_FILE` required in production

First boot: open `https://<server>/`, create the single admin under `/setup`
(the endpoint disappears forever afterwards).

## Agent install
Pre-install on the host: `restic`, `rclone`, and the database clients you plan
to back up (`postgresql-client`, `mysql-client`, `mongodb-database-tools`,
`sqlite3`). The agent probes but never downloads tools itself.

Create an enrollment token in the web UI (Agents → 生成注册令牌), then:
```sh
sudo useradd --system --home /var/lib/bmc-agent --shell /usr/sbin/nologin bmc-agent || true
sudo mkdir -p /var/lib/bmc-agent
sudo cp deploy/systemd/bmc-agent.service /etc/systemd/system/
sudo systemctl edit bmc-agent   # set BMC_SERVER_GRPC_URL + one-time BMC_ENROLLMENT_TOKEN
sudo systemctl enable --now bmc-agent
```

Agent environment:
- `BMC_SERVER_GRPC_URL` host:port of the server gRPC endpoint
- `BMC_ENROLLMENT_TOKEN` one-time, only needed on first boot
- `BMC_AGENT_STATE_DIR` default `/var/lib/bmc-agent`
- `BMC_AGENT_DATA_DIR` scratch space for dumps/restores (default `<state>/scratch`) — size for your largest logical dump ×1.2
- `BMC_SERVER_TLS=0` and/or `BMC_DEV_INSECURE=1` only for local development

The agent holds its identity in `<state>/identity.json` (mode 0600). If it is
lost, revoke the old agent in the UI and re-enroll; identities are never
rebuilt automatically.

The unit ships with `NoNewPrivileges=true` and `UMask=0077`. Because backups
must read arbitrary user-chosen paths, no filesystem sandbox is enabled by
default — grant the service user read access to the paths you intend to back
up instead.

## Versioning & upgrades
Server/agent handshake uses semver: major mismatch refuses the stream, minor
mismatch connects with a warning in the UI. Upgrade order: server first, then
roll agents.

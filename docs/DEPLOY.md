# Deploying Scrimshaw

Scrimshaw is a single static binary plus a data directory. It has no external
dependencies — no database server, no Redis, no build step. Run it behind a
reverse proxy that terminates TLS.

## 1. Docker Compose (recommended)

```sh
git clone https://github.com/tiagojct/scrimshaw.git
cd scrimshaw
cp .env.example .env
# edit .env: set SCRIMSHAW_BASE_URL to your public HTTPS URL,
# and (recommended) SCRIMSHAW_SESSION_SECRET
docker compose up -d --build
```

The compose file builds the image locally and stores the database, snapshots,
image cache, and exports under `./data`. The container listens on `8080`;
`docker compose ps` should show it `healthy` within a few seconds (the image
health-probes itself — there is no shell to `curl` from).

Once the image is published to `ghcr.io/<owner>/scrimshaw` (see the
`Docker publish` workflow), replace `build: .` with
`image: ghcr.io/<owner>/scrimshaw:latest` and use `docker compose pull`.

## 2. Reverse proxy and TLS

Do **not** expose `:8080` directly. Put Scrimshaw behind a proxy that terminates
HTTPS and forwards to it. Caddy is the least effort (automatic certificates):

```caddyfile
# Caddyfile
reader.example.com {
    reverse_proxy localhost:8080
}
```

nginx equivalent:

```nginx
server {
    listen 443 ssl;
    server_name reader.example.com;
    # ssl_certificate / ssl_certificate_key ... (e.g. from certbot)

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

Set `SCRIMSHAW_BASE_URL=https://reader.example.com` to match. That value drives
the session cookie's `Secure` flag and the bookmarklet/iOS snippets, and the
proxy's `X-Forwarded-Proto` lets Scrimshaw detect HTTPS.

## 3. First run

Open the site. The first visit prompts you to create the admin account — there
is no default password (minimum 12 characters). Then add a feed or save a link.
Migrations run automatically on startup and are versioned and append-only.

## 4. Backups

The SQLite database and the `snapshots/` directory must sit on **local disk** —
a bind mount or a local named volume, never NFS/SMB. Scrimshaw is the only
process that should write to the database; do not point the `sqlite3` CLI or
another tool at it while it runs.

Back up with SQLite's online backup rather than copying the live file. Simplest,
with the app stopped so the copy is consistent:

```sh
docker compose stop
tar czf scrimshaw-backup-$(date +%F).tar.gz data/
docker compose start
```

Without stopping, take a consistent database snapshot with `VACUUM INTO` and
archive the snapshots directory alongside it:

```sh
sqlite3 data/scrimshaw.db "VACUUM INTO 'backup-$(date +%F).db'"
tar czf snapshots-$(date +%F).tar.gz data/snapshots/
```

To restore: stop Scrimshaw, put the database and `snapshots/` back together into
the data directory, then start it again. Never overwrite a live SQLite file.

## 5. Updates

```sh
git pull
docker compose up -d --build   # or `docker compose pull && up -d` for a published image
```

Migrations apply on startup. Take a backup first.

## 6. Configuration

All settings are environment variables; see [`.env.example`](../.env.example).
The essentials:

- `SCRIMSHAW_BASE_URL` — your public HTTPS origin.
- `SCRIMSHAW_SESSION_SECRET` — set it so logins survive redeploys.
- `SCRIMSHAW_FETCH_TIMEOUT` — outbound fetch timeout (default `30s`).

Per-feed settings (refresh interval, full-article fetch, auto-snapshot) live in
the app under **Feeds**.

## 7. Running the binary directly (systemd)

```sh
go build -o scrimshaw ./cmd/scrimshaw
```

`scrimshaw.service` is a hardened example unit (`NoNewPrivileges`,
`ProtectSystem=strict`, a dedicated `scrimshaw` user, `ReadWritePaths` limited to
the data dir). Install the binary, create the user and data directory, drop the
unit in `/etc/systemd/system/`, set `SCRIMSHAW_*` in the unit's `Environment=`,
then `systemctl enable --now scrimshaw`. Front it with the same reverse proxy.

## 8. Hardening notes

- The container runs as root but ships only the static binary and CA
  certificates on a `scratch` base — no shell, no package manager. To run
  non-root, add `user: "65534:65534"` in compose and ensure the data volume is
  writable by that uid (a bind mount needs `chown 65534 data`).
- Login is rate-limited with per-IP lockout; passwords are bcrypt-hashed.
- All fetched HTML is sanitized before render; every outbound URL passes an SSRF
  guard. Keep Scrimshaw on a host that cannot reach sensitive internal services
  if you can, as defence in depth.

## Troubleshooting

- **Feeds/images fail with a TLS/certificate error** — the image bundles CA
  certificates; if you built a custom image, make sure
  `/etc/ssl/certs/ca-certificates.crt` is present.
- **Sessions drop on every deploy** — set `SCRIMSHAW_SESSION_SECRET` (otherwise a
  new one is generated when the data dir is fresh).
- **Container never becomes healthy** — check `docker compose logs`; the probe
  hits `/healthz` on `SCRIMSHAW_ADDR`.

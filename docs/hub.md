# Running the hub

The hub is a single Go binary (`beszel`) that serves the web UI, the API, public status pages, and the endpoint agents connect to. It stores everything in a data directory (`beszel_data` by default).

> **Release status:** InfraScope hasn't published a GitHub release yet. Until it does, the release-based options (install script, prebuilt binaries, published Docker images) aren't available. Use **[build from source](#option-1-build-from-source)** or **[Docker built locally](#option-2-docker-compose)** instead.

## Requirements

- Linux, macOS, Windows or FreeBSD (amd64, arm64 or arm).
- For building from source: Go (the version in `go.mod`) and Bun or Node.js.
- One TCP port for the web UI and agents (default `8090`).

## Option 1: build from source

```bash
git clone https://github.com/AxiomOperator/infrascope.git
cd infrascope

make build-web-ui   # builds the web UI into internal/site/dist (embedded in the binary)
make build-hub      # writes ./build/beszel_<os>_<arch>

./build/beszel_linux_amd64 serve --http 0.0.0.0:8090
```

`make build-web-ui` runs `lingui extract`, which rewrites the translation files under `internal/site/src/locales`. To build without touching tracked files:

```bash
cd internal/site && bun install && ./node_modules/.bin/lingui compile && ./node_modules/.bin/vite build && cd ../..
```

Then open `http://<server>:8090` and create the first admin account. You can also create it non-interactively with `USER_EMAIL` and `USER_PASSWORD` on first start.

Useful flags:

| Flag | Meaning |
|---|---|
| `serve --http 0.0.0.0:8090` | Address and port to listen on |
| `--dir /path/to/beszel_data` | Data directory (default `./beszel_data`) |
| `superuser upsert <email> <password>` | Create or reset a superuser for the admin panel at `/_/` |
| `health --url http://localhost:8090` | Health check, for containers and monitoring |
| `--version` | Print the version |

## Option 2: Docker Compose

Published images will be at `ghcr.io/axiomoperator/infrascope/beszel` once the first release is tagged. Until then, build the image locally from the repository root, after building the web UI as in option 1:

```bash
docker build -f internal/dockerfile_hub -t infrascope-hub .
```

`docker-compose.yml`:

```yaml
services:
  infrascope:
    image: infrascope-hub            # or ghcr.io/axiomoperator/infrascope/beszel:<version>
    container_name: infrascope
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./beszel_data:/beszel_data
    environment:
      APP_URL: "https://infrascope.example.com"
    healthcheck:
      test: ["CMD", "/beszel", "health", "--url", "http://localhost:8090"]
      interval: 120s
      start_period: 10s
      timeout: 5s
```

The container starts as root, takes ownership of `/beszel_data`, and then switches to uid/gid `1000`. Override this with `PUID`/`PGID`, or set `PUID=0` to keep running as root.

## Option 3: systemd service with the install script

Once releases exist, `supplemental/scripts/install-hub.sh` installs the hub as a systemd service with its own `beszel` user:

```bash
curl -sL https://raw.githubusercontent.com/AxiomOperator/infrascope/main/supplemental/scripts/install-hub.sh -o install-hub.sh
chmod +x install-hub.sh
sudo ./install-hub.sh            # -p <port> to change the port, --auto-update for daily updates, -u to uninstall
```

To run a binary you built yourself as a service, use the unit in [`supplemental/guides/systemd.md`](../supplemental/guides/systemd.md) and point `ExecStart` at your binary.

## Reverse proxy and HTTPS

Put the hub behind a reverse proxy that terminates TLS, and set `APP_URL` to the public URL. Notification links, ack links, email subscriptions and push notifications depend on it.

- **WebSockets:** agents and the live UI use them on the same port. Make sure the proxy forwards the `Upgrade`/`Connection` headers. Caddy does this automatically; nginx needs `proxy_http_version 1.1` plus the upgrade headers.
- **Client IP:** if you rely on IP-based rate limits (push monitors, status pages), set PocketBase's trusted proxy headers in the admin panel: **`/_/` → Settings → Application → "User IP proxy headers"**.
- **Status pages on their own domain:** see [`supplemental/guides/status-page-custom-domain.md`](../supplemental/guides/status-page-custom-domain.md).

Caddy example:

```
infrascope.example.com {
    reverse_proxy 127.0.0.1:8090
}
```

## Email (SMTP)

Configure SMTP in the admin panel: **`/_/` → Settings → Mail settings**. Email is used by:

- email notification channels
- password reset
- status page subscriptions

Status page subscriptions also need `APP_URL` and a sender address.

## Environment variables

Every variable can also be given with a `BESZEL_HUB_` prefix, for example `BESZEL_HUB_APP_URL`.

| Variable | Default | Description |
|---|---|---|
| `APP_URL` | — | Public URL of the hub. Used in links, emails, push notifications and CORS. |
| `USER_EMAIL` / `USER_PASSWORD` | — | Create the first user on first start. |
| `USER_CREATION` | `false` | `true` lets new users sign up through OAuth/OIDC. |
| `DISABLE_PASSWORD_AUTH` | `false` | `true` disables email/password login (OAuth/OIDC only). |
| `MFA_OTP` | — | `true` requires an emailed one-time code for all users; `superusers` requires it for superusers only. |
| `OAUTH_DISABLE_POPUP` | `false` | `true` uses a redirect instead of a popup for OAuth login. |
| `SHARE_ALL_SYSTEMS` | `false` | `true` makes every system visible to all users (read-only users still can't edit). |
| `AUTO_LOGIN` | — | Email of a user to log in automatically. **Only for trusted, private networks.** When set, CORS is limited to `APP_URL`. |
| `TRUSTED_AUTH_HEADER` | — | Header set by an authenticating proxy that carries the user's email. |
| `TRUSTED_PROXY_IPS` | — | Comma-separated IPs/CIDRs allowed to send `TRUSTED_AUTH_HEADER`. **Set this whenever you use the header**; otherwise any client can send it. |
| `CHECK_UPDATES` | `false` | `true` shows an "update available" notice from InfraScope releases. |
| `CONTAINER_DETAILS` | `true` | `false` disables container logs and inspect in the UI. |
| `SYNC_SYSTEM_NAMES` | `false` | `true` renames systems to their reported hostname. |
| `HUB_MONITORS` | `true` | `false` disables monitors that run on the hub (agent monitors still work). |
| `HUB_MONITOR_MIN_INTERVAL` | `10` | Minimum interval in seconds for hub-run monitors. |
| `CSP` | — | Custom `Content-Security-Policy` header for the web UI. |
| `DATA_DIR`, `PUID`, `PGID` | `/beszel_data`, `1000`, `1000` | Docker image only: data directory and the user the hub runs as. |

## Data and backups

All state lives in the data directory:

- `data.db` and `auxiliary.db`: SQLite databases.
- `id_ed25519`: the hub's SSH key. Agents trust its public key, and it encrypts stored secrets.
- `vapid_private.pem`: the browser push key.
- `storage/`: uploaded files.

**Back up the whole directory.** If you lose `id_ed25519`, encrypted secrets (monitor HTTP headers, notification URLs) can't be decrypted, and every agent has to be reconfigured with a new key.

PocketBase's built-in backups (admin panel → Settings → Backups, local or S3) cover the databases and storage.

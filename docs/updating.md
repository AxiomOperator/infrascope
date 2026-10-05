# Updating

Update the **hub first**, then the agents. A newer hub works with older agents, and new monitor options are only sent to agents that support them (0.21.0 and later). An older hub may not understand a newer agent.

Database migrations run automatically when the hub starts. **Back up the data directory before upgrading** (see [hub.md](hub.md#data-and-backups)).

## Self-update (release builds)

Release binaries can update themselves from [InfraScope's GitHub releases](https://github.com/AxiomOperator/infrascope/releases):

```bash
sudo beszel update          # hub
sudo beszel-agent update    # agent
sudo systemctl restart beszel-hub beszel-agent
```

- Updates are **signed**. A binary only installs an archive whose ed25519 signature matches the public key embedded at build time. Builds without a key (local and CI development builds) refuse to self-update.
- `--china-mirrors` is accepted for old scripts but does nothing.
- Agents installed with `--auto-update` check for updates daily through a systemd timer.
- To see update notices in the UI, set `CHECK_UPDATES=true` on the hub.

## Docker

```bash
docker compose pull      # published images
docker compose up -d
```

For locally built images, rebuild them (`docker build -f internal/dockerfile_hub …`) and run `docker compose up -d`.

## Manual update (binaries built from source or CI)

1. Get the new binary:
   - **Hub:** `git pull && make build-web-ui && make build-hub`.
   - **Agent:** `git pull && make build-agent`, or download the latest **Build agent (linux-amd64)** artifact ([agent.md](agent.md#option-a-linux-amd64-binary-from-ci)).
2. Replace the binary and restart the service:

   ```bash
   sudo install -m 0755 beszel-agent /usr/local/bin/beszel-agent
   sudo systemctl restart beszel-agent
   ```

3. Check that the agent reconnected: the system goes back to **up** in the UI, and `journalctl -u beszel-agent` shows no errors.

## What changes on upgrade to 0.21.0

- **Notifications:** existing email addresses and webhook URLs become notification channels automatically, so delivery is unchanged.
- **Encryption:** stored monitor HTTP secrets and notification URLs are encrypted on first start, using the hub key in the data directory.
- **Docker image:** the hub image now runs as uid 1000 after fixing data-directory ownership. Set `PUID=0` to keep running as root.
- **Agent socket:** agents listening on a unix socket now make it connectable by the non-root hub. SSH key authentication still applies.
- **SSH host keys:** SSH-mode agents now keep a persistent ed25519 host key, which the hub pins on first connect.

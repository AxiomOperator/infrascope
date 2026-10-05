# InfraScope

InfraScope is a self-hosted server monitoring and uptime platform. It tracks system and container metrics, runs uptime checks from the hub or from your own machines, alerts you through the channels you choose, and publishes public status pages.

InfraScope is a fork of [Beszel](https://github.com/henrygd/beszel).

## Features

- **System metrics:** CPU, memory (including swap and ZFS ARC), disks, disk I/O, network, load, temperatures, fans, GPUs (NVIDIA, AMD, Intel, Apple), battery and Wi-Fi.
- **Containers:** Docker and Podman stats, health, logs and image updates.
- **Disks and services:** S.M.A.R.T. (including eMMC and mdraid), ZFS/storage pools, systemd services, OS package updates.
- **Uptime monitoring:**
  - **Check types:** HTTP (methods, headers, status codes, keyword/JSON checks), TCP, ICMP, DNS records, SSH, PostgreSQL, MySQL, Redis, SMTP, IMAP, gRPC, game servers, Docker containers, and push/heartbeat monitors.
  - **Locations:** run each check from the hub, from agents, or from several locations with a quorum.
  - **Results:** retries, uptime %, incident history, certificate expiry and packet-loss/latency alerts.
  - **Setup:** monitors can be created from Docker labels or imported from Uptime Kuma.
- **Alerts and notifications:**
  - **Delivery:** named channels (email, 24 Shoutrrr services with a guided setup form, and browser push), routed by severity, with message templates.
  - **Controls:** quiet hours, maintenance windows, dependency-aware alerts, acknowledgements and reminders.
- **Status pages and incidents:** public pages with groups, branding, custom domains, badges and RSS/Atom feeds; incident timelines and email subscriptions.
- **Multi-user:** roles, OAuth/OIDC, MFA, and sharing systems between users.

## Architecture

- **Hub:** a single Go binary built on [PocketBase](https://pocketbase.io/). It serves the web UI and API, runs hub-side checks, and stores data in SQLite.
- **Agent:** a lightweight Go binary on each monitored machine. It connects to the hub over WebSocket (outbound) or accepts SSH connections from the hub.

## Getting started

| | |
|---|---|
| [Run the hub](docs/hub.md) | Build from source, Docker Compose, systemd, reverse proxy, environment variables |
| [Deploy agents](docs/agent.md) | linux-amd64 builds from CI, install script, Docker, systemd, agent settings |
| [Update](docs/updating.md) | Signed self-updates, Docker and manual updates |
| [Develop and release](docs/development.md) | Tests, builds, CI workflows, publishing signed releases |

Quick start from source:

```bash
git clone https://github.com/AxiomOperator/infrascope.git && cd infrascope
make build-web-ui && make build-hub
./build/beszel_linux_amd64 serve --http 0.0.0.0:8090
```

Then open `http://localhost:8090`, create your account, and click **Add system** to get the agent's install details.

> Prebuilt, signed binaries, `.deb` packages and Docker images (`ghcr.io/axiomoperator/infrascope/beszel`, `…/beszel-agent`) come with each [release](https://github.com/AxiomOperator/infrascope/releases).

## Issues

Report bugs and request features in [GitHub issues](https://github.com/AxiomOperator/infrascope/issues).

## License

InfraScope is licensed under the MIT License. It's based on Beszel © 2024 henrygd. See [LICENSE](LICENSE).

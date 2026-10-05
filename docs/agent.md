# Deploying the agent

The agent (`beszel-agent`) runs on each machine you want to monitor. It collects:

- system metrics, Docker/Podman containers, systemd services, SMART, ZFS and package updates
- uptime checks that run on that machine

It sends everything to the hub.

## How agents connect

Each agent supports two connection modes, and can use both at once:

| Mode | Direction | Needs | When to use |
|---|---|---|---|
| **WebSocket** (recommended) | agent → hub | `HUB_URL` + `TOKEN` + `KEY` | Agent behind NAT/firewall; no inbound port on the agent |
| **SSH** | hub → agent | `KEY` + `LISTEN` (port `45876`) open to the hub | Hub can reach the agent directly |

- **`KEY`** is the hub's public key. The agent uses it to verify the hub (WebSocket mode) and to authorize the hub (SSH mode).
- **`TOKEN`** identifies the system. It's shown when you add a system in the UI, or comes from a **universal token** (Settings → Tokens & Fingerprints), which lets any agent register itself.
- In SSH mode the hub pins the agent's host key on first connect. If you reinstall an agent and its data directory is lost, use **Reset SSH host key** in the system's menu.

## Step 1: add the system in the UI

1. Click **Add system**.
2. Enter a name and the host/IP (and the port, for SSH mode).
3. The dialog shows the `KEY`, `TOKEN` and `HUB_URL` values plus ready-made install commands.

## Step 2: install the agent

> For most installs, use **[the install script](#option-b-install-script-linux)** or **[Docker](#option-c-docker-compose)**; both use [InfraScope's releases](https://github.com/AxiomOperator/infrascope/releases). To install a prebuilt binary directly, download `beszel-agent_<os>_<arch>.tar.gz` from the latest release. **CI builds (option A)** carry the latest unreleased code from `main`.

### Option A: linux-amd64 binary from CI

Every push to `main` that changes agent code runs the **Build agent (linux-amd64)** workflow. It uploads two archives:

| Archive | Use on |
|---|---|
| `beszel-agent_linux_amd64.tar.gz` | Any linux/amd64 system (static, works with glibc or musl/Alpine) |
| `beszel-agent_linux_amd64_glibc.tar.gz` | glibc distributions (Debian, Ubuntu, RHEL, …); adds **NVIDIA GPU monitoring via NVML** |

Download them:

- **From GitHub:** **Actions → Build agent (linux-amd64)** → the latest successful run → **Artifacts**.
- **With the GitHub CLI:**

  ```bash
  gh run download --repo AxiomOperator/infrascope --name "beszel-agent_linux_amd64-$(gh api repos/AxiomOperator/infrascope/commits/main --jq .sha)"
  ```

Verify the archives and install the binary:

```bash
sha256sum -c checksums.txt
tar -xzf beszel-agent_linux_amd64.tar.gz
sudo install -m 0755 beszel-agent /usr/local/bin/beszel-agent
```

Then [run it as a systemd service](#run-as-a-systemd-service).

### Option B: install script (Linux)

```bash
curl -sL https://raw.githubusercontent.com/AxiomOperator/infrascope/main/supplemental/scripts/install-agent.sh -o install-agent.sh
chmod +x install-agent.sh
sudo ./install-agent.sh -k "<KEY>" -t "<TOKEN>" -url "<HUB_URL>"
```

| Flag | Meaning |
|---|---|
| `-k` | Hub public key (required) |
| `-t`, `-url` | Token and hub URL for WebSocket mode |
| `-p` | SSH listen port (default `45876`) |
| `-v` | Version to install (default: latest) |
| `--auto-update [true\|false]` | Daily automatic updates |
| `--mirror URL` | Download through a GitHub proxy |
| `-u` | Uninstall |

The script installs to `/opt/beszel-agent`, creates a `beszel` system user, and sets up the `beszel-agent` systemd service.

### Option C: Docker Compose

Each release publishes `ghcr.io/axiomoperator/infrascope/beszel-agent`. There are also `beszel-agent-nvidia` and `beszel-agent-intel` variants for GPU monitoring. Tags are `latest`, the full version (e.g. `0.21.0`), `0.21` and `0`. To build it yourself:

```bash
docker build -f internal/dockerfile_agent -t infrascope-agent .
```

```yaml
services:
  infrascope-agent:
    image: ghcr.io/axiomoperator/infrascope/beszel-agent:latest   # or a version tag, e.g. 0.21.0
    container_name: infrascope-agent
    restart: unless-stopped
    network_mode: host               # needed for accurate network stats
    volumes:
      - ./agent_data:/var/lib/beszel-agent
      - /var/run/docker.sock:/var/run/docker.sock:ro
      # extra disks: mount a folder from each into /extra-filesystems
      # - /mnt/disk1/.infrascope:/extra-filesystems/disk1:ro
    environment:
      KEY: "<KEY>"
      TOKEN: "<TOKEN>"
      HUB_URL: "<HUB_URL>"
      LISTEN: 45876                  # only needed for SSH mode
    healthcheck:
      test: ["CMD", "/agent", "health"]
      interval: 120s
```

There are GPU-specific images too: `internal/dockerfile_agent_nvidia` and `internal/dockerfile_agent_intel`.

### Option D: build from source

```bash
make build-agent                      # ./build/beszel-agent_<os>_<arch>
# or a specific target:
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o beszel-agent ./internal/cmd/agent
```

Add `-tags=glibc` on linux/amd64 for NVIDIA NVML support.

## Run as a systemd service

```ini
# /etc/systemd/system/beszel-agent.service
[Unit]
Description=InfraScope Agent
Wants=network-online.target
After=network-online.target

[Service]
EnvironmentFile=/etc/beszel-agent.conf
ExecStart=/usr/local/bin/beszel-agent
User=beszel
Restart=on-failure
StateDirectory=beszel-agent
KeyringMode=private
LockPersonality=yes
ProtectClock=yes
ProtectHome=read-only
ProtectHostname=yes
ProtectKernelLogs=yes
ProtectSystem=strict
RemoveIPC=yes
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
```

```bash
# /etc/beszel-agent.conf  (chmod 600)
KEY="ssh-ed25519 AAAA..."
TOKEN="..."
HUB_URL="https://infrascope.example.com"
# LISTEN=45876
```

```bash
sudo useradd --system --home-dir /nonexistent --shell /bin/false beszel
sudo usermod -aG docker beszel        # container stats (if Docker is installed)
sudo systemctl daemon-reload
sudo systemctl enable --now beszel-agent
journalctl -u beszel-agent -f
```

Some collectors need extra access:

- **SMART:** the `beszel` user needs to run `smartctl` (e.g. `CAP_SYS_RAWIO` / `CAP_SYS_ADMIN` via `AmbientCapabilities=`), or the service must run as root.
- **systemd service stats:** needs D-Bus access.

## Agent environment variables

Every variable can also be given with a `BESZEL_AGENT_` prefix.

**Connection**

| Variable | Description |
|---|---|
| `KEY` / `KEY_FILE` | Hub public key(s), or a file containing them |
| `TOKEN` / `TOKEN_FILE` | System or universal token (WebSocket mode) |
| `HUB_URL` | Hub URL (WebSocket mode) |
| `LISTEN` | SSH listen address or port, or a unix socket path (default `45876`; `PORT` is the legacy name) |
| `NETWORK` | Force `tcp`, `tcp4`, `tcp6` or `unix` for `LISTEN` |
| `DISABLE_SSH` | `true` disables the SSH server (WebSocket only) |
| `CA_CERT_FILE` | Extra CA certificate(s) to trust for a private HTTPS hub |
| `EXIT_ON_DNS_ERROR` | `true` exits on DNS failures so a supervisor restarts the agent |
| `DATA_DIR` | Agent data directory (fingerprint, SSH host key, update caches) |
| `SYSTEM_NAME` | Name used when registering with a universal token |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error` |

**Disks and memory**

| Variable | Description |
|---|---|
| `FILESYSTEM` | Root filesystem device or mount to use (`device__Name` sets a display name) |
| `EXTRA_FILESYSTEMS` | Comma-separated extra disks/mounts (optionally `device__Name`) |
| `DISK_USAGE_CACHE` | Cache disk usage for a duration (e.g. `15m`) so sleeping disks stay asleep |
| `MEM_CALC` | Alternative memory calculation (e.g. `htop`) |

**Network and sensors**

| Variable | Description |
|---|---|
| `NICS` | Comma-separated interfaces to monitor; `*` wildcards allowed. Start the list with `-` to exclude those interfaces instead |
| `SENSORS` | Temperature sensors to include/exclude; empty disables sensors |
| `SYS_SENSORS` | Alternative sysfs path for sensors (containers) |
| `PRIMARY_SENSOR` | Sensor shown as the dashboard temperature |
| `SENSORS_TIMEOUT` | Timeout for sensor reads |

**GPU**

| Variable | Description |
|---|---|
| `GPU_COLLECTOR` | GPU collectors to use, in priority order (e.g. `nvml,nvidia-smi`) |
| `SKIP_GPU` | `true` disables GPU collection |
| `NVML` | `true` reads NVIDIA GPUs through NVML (glibc builds) before falling back to `nvidia-smi` |
| `AMD_SYSFS` | `true` reads AMD GPUs from sysfs instead of `rocm-smi` |
| `INTEL_GPU_DEVICE` | Intel GPU device to monitor |

**Containers**

| Variable | Description |
|---|---|
| `DOCKER_HOST` | Docker/Podman socket or TCP address |
| `DOCKER_TIMEOUT` | Docker API timeout |
| `EXCLUDE_CONTAINERS` | Comma-separated container name patterns to ignore (also hides their logs and inspect output) |
| `DOCKER_IMAGE_CHECK` | `false` disables image update checks |

**SMART**

| Variable | Description |
|---|---|
| `SMART_DEVICES`, `SMART_DEVICES_SEPARATOR` | Explicit SMART devices to read |
| `EXCLUDE_SMART` | SMART devices to skip |
| `SMART_INTERVAL` | SMART refresh interval (e.g. `1h`) |

**systemd, storage pools and packages**

| Variable | Description |
|---|---|
| `SKIP_SYSTEMD` | `true` disables systemd service monitoring |
| `SERVICE_PATTERNS` | Comma-separated unit patterns to monitor (default `*service`) |
| `ZFS_INTERVAL` | Storage pool detail refresh interval |
| `PACKAGE_UPDATES_INTERVAL` | OS package update check interval (e.g. `6h`; `0` disables) |

## Monitors from Docker labels

Agents can create uptime monitors from container labels. Turn it on per system in the system's settings; the label reference is in [`supplemental/guides/docker-label-monitors.md`](../supplemental/guides/docker-label-monitors.md).

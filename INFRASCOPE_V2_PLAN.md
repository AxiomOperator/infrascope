# InfraScope v2: Rewrite Plan

> Status: **planning only.** No code has been written.
> The current InfraScope repository is **reference only**. v2 is a new project and nothing is migrated from v1.

---

## 0. Decisions

| Area | Decision |
|---|---|
| Frontend | Vite + React + TypeScript, shadcn/ui, Tailwind CSS v4 |
| Backend (hub) | Go. REST API with an OpenAPI 3.1 spec, and Server-Sent Events (SSE) for realtime |
| Agent | Go, rewritten from scratch (clean break). It connects **outbound** to the hub over **gRPC with mutual TLS (mTLS)** as one bidirectional stream |
| Database | **PostgreSQL 18 + TimescaleDB**. Plain tables hold relational data; hypertables with continuous aggregates, compression and retention hold metrics |
| DB access | **No Drizzle.** Go owns the schema: `goose` migrations, `sqlc` generated queries, `pgx/v5` driver |
| Accounts | Organizations with roles (owner, admin, member, viewer). Email + password, TOTP, OIDC single sign-on, API tokens |
| Scope | Full parity with InfraScope v1, delivered in phases. A usable core ships first |
| Deployment | Docker Compose (hub + TimescaleDB). Agents ship as static binaries or containers. Helm chart later |
| Repository | New monorepo |
| Data from v1 | None. Fresh start; v1 is a reference implementation |

### Proposed libraries (confirm in Phase 0)

**Frontend**
- **Routing:** TanStack Router (file-based, typed routes and search params).
- **Server state:** TanStack Query.
- **Tables:** TanStack Table.
- **Forms:** react-hook-form + zod.
- **UI:** shadcn/ui (Radix) + Tailwind v4 + lucide icons.
- **Charts:**
  - shadcn charts (Recharts) for most charts.
  - **uPlot** for high-frequency realtime and large series. Decide with a benchmark in Phase 4.
- **API:** OpenAPI → `openapi-typescript` + **Orval**, which generates the typed client and TanStack Query hooks.
- **i18n:** Lingui, carrying over the v1 approach.
- **Tooling:** Biome for lint and format; Vitest + Testing Library for unit tests; Playwright for end-to-end tests.

**Hub (Go)**
- **HTTP:** `net/http` + **Huma v2**, which generates the OpenAPI spec from Go types and handles validation.
- **Database:** `pgx/v5`, `sqlc`, `goose`.
- **Background jobs:** **River**, a Postgres-backed job queue for notifications, emails, reminders and rollup maintenance.
- **Agent link:** `google.golang.org/grpc` + `buf` for protobuf.
- **Auth:**
  - `coreos/go-oidc` for single sign-on.
  - `pquerna/otp` for TOTP.
  - `golang.org/x/crypto/argon2` for password hashing.
- **Notifications:** `nicholas-fedor/shoutrrr` for webhook and push services, plus a Web Push implementation written with the standard library (ported from v1).
- **Logging:** `log/slog`.

**Agent (Go)**
- gRPC client.
- `gopsutil` for host metrics.
- Docker/Podman over the HTTP API with no SDK (as in v1).
- NVML via `purego`.
- `smartctl`, ZFS and D-Bus integrations ported from v1.

---

## 1. Goals and non-goals

**Goals**
- Rebuild InfraScope's full feature set on a stack that scales to thousands of agents and years of metrics.
- **Security by default:**
  - Agents authenticate with mTLS and hub-issued certificates.
  - Sessions protected with CSRF tokens and signed links.
  - Secrets encrypted at rest.
  - Organizations isolate everything.
- **Type safety end to end:**
  - Go types generate the OpenAPI spec, which generates TypeScript types.
  - Protobuf types are shared between hub and agent.
- **One command to run:** `docker compose up` for the hub and database; one install command per agent.
- **Build on the lessons from v1's code review:**
  - no unsynchronized shared state;
  - no work on a connection's read loop;
  - rollups weighted per item;
  - alerting driven by explicit state machines.

**Non-goals (for now)**
- Log aggregation or storage beyond on-demand container logs.
- APM or tracing.
- A hosted multi-region SaaS control plane. The design keeps multi-replica hubs possible (Phase 10), but launch targets a single replica.

---

## 2. Architecture

```
                    ┌──────────────────────────── Docker Compose ───────────────────────────┐
 Browser ── HTTPS ──►  Hub (Go)                                                            │
   │  REST + SSE    │   ├─ HTTP API (Huma, OpenAPI 3.1)  /api/v1/*                          │
   │                │   ├─ SSE hub  /api/v1/events (per-org topics)                        │
   │                │   ├─ Static SPA (embedded Vite build)                                  │
   │                │   ├─ Public: status pages, badges, feeds, push/heartbeat URLs          │
   │                │   ├─ gRPC server :8443 (mTLS) ◄──────────── Agents (outbound only)     │
   │                │   ├─ Monitor runner (hub-side checks)                                  │
   │                │   ├─ Engines: uptime state machine, alert evaluator, incident mgr     │
   │                │   └─ River workers: notify, email, web push, reminders, maintenance   │
   │                │                 │ pgx                                                  │
   │                │   PostgreSQL 18 + TimescaleDB (relational + hypertables)               │
   │                └────────────────────────────────────────────────────────────────────────┘
   └── Public status page (custom domain) ── reverse proxy (TLS) ──► Hub
```

**Key flows**
- **Agent → hub:**
  - The agent opens `Connect(stream AgentMessage) returns (stream HubMessage)`.
  - The hub pushes configuration (collection intervals, monitors, discovery settings) and on-demand requests (logs, SMART refresh, inspect).
  - The agent streams metric batches, check results, inventory changes and responses tagged with a correlation id.
  - Backpressure and flow control come from HTTP/2. Batches are acknowledged, so the agent keeps an on-disk spool for when the hub is unreachable (bounded, e.g. 24h).
- **Realtime to the browser:**
  - SSE with per-organization topics, delivered through an in-process broker. Postgres LISTEN/NOTIFY fans out when running multiple replicas later.
  - 1-second live metrics use a "live session": the browser asks for live mode on a system, the hub tells that agent to stream at 1s while the session is active, and the session times out without a heartbeat.
- **Metrics storage:** 1-minute raw rows go into hypertables. Continuous aggregates build 10m, 1h and 1d views. Compression starts after 7 days. Retention policies have per-organization defaults.
- **Alerting:**
  - Evaluators consume ingested metrics and check results.
  - Explicit state machines (ok → pending → firing → resolved) record transitions in `alert_events`.
  - Notifications are queued as River jobs, then routed to channels by severity.

---

## 3. Repository layout (new monorepo)

```
infrascope/
├─ apps/web/                 # Vite + React SPA
│  ├─ src/routes/            # TanStack Router file routes
│  ├─ src/components/ui/     # shadcn components
│  ├─ src/features/<domain>/ # systems, monitors, alerts, status-pages, …
│  ├─ src/api/               # generated client + hooks (Orval) — do not edit
│  └─ src/locales/           # Lingui catalogs
├─ cmd/hub/                  # hub main
├─ cmd/agent/                # agent main
├─ cmd/infractl/             # admin CLI (create user, reset 2FA, rotate CA, …)
├─ internal/
│  ├─ hub/{api,auth,orgs,systems,metrics,monitors,alerts,notify,status,incidents,discovery,realtime,jobs,secrets,audit}/
│  ├─ agent/{collectors/*,monitors,docker,smart,zfs,systemd,spool,link}/
│  ├─ proto/                 # generated protobuf Go code
│  └─ shared/                # probe engine shared by hub + agent (ported netmon), crypto helpers
├─ db/
│  ├─ migrations/            # goose SQL migrations (source of truth)
│  └─ queries/               # sqlc .sql files
├─ proto/infrascope/agent/v1/*.proto   # buf module
├─ deploy/{compose,helm,systemd}/
├─ scripts/                  # dev, codegen, release
├─ docs/                     # docs site (Astro Starlight or VitePress)
└─ .github/workflows/
```

---

## 4. Data model (initial draft)

**Relational tables (regular Postgres):**
- **Tenancy:**
  - `organizations`
  - `users`
  - `memberships (org, user, role)`
  - `invitations`
  - `sessions`
  - `api_tokens` (hashed, scoped, expiring)
  - `oidc_providers`
  - `user_totp`
  - `recovery_codes`
  - `audit_log`
- **Agents:**
  - `enrollment_tokens` (one-time or reusable, expiry, org, default labels)
  - `agents` (cert fingerprint, version, last seen, capabilities)
  - `agent_certificates` (serial, not_after, revoked)
- **Systems:**
  - `systems` (org, name, agent_id, labels/tags, status, down_reason, depends_on)
  - `system_details` (OS, kernel, CPU model, cores, memory, …)
  - `system_status_events` (segments)
- **Inventory**, updated from the agent: `containers`, `systemd_services`, `smart_devices`, `smart_attributes`, `zfs_pools`, `storage_pools`, `package_updates`.
- **Monitors:**
  - `monitors` (org, name, type, target, options jsonb, secrets encrypted, interval, timeout, retries, quorum, notify, severity, thresholds, cert days, push token hash, managed_by/key, depends_on)
  - `monitor_locations` (monitor, location: hub | agent)
  - `monitor_status` (current per monitor/location)
  - `monitor_status_events` (segments)
- **Maintenance:** `maintenance_windows` (one-time, recurring via RRULE) and `maintenance_targets`.
- **Alerting:**
  - `alert_rules` (org, scope: system/tag/all, metric, operator, threshold, duration, severity, channels)
  - `alert_states` (current state per rule and target)
  - `alert_events` (history: fired/resolved, ack, notes)
  - `quiet_hours`
- **Notifications:**
  - `notification_channels` (type: email/shoutrrr/webpush/…, config encrypted, min severity, default)
  - `notification_templates`
  - `push_subscriptions`
  - `notification_log` (delivery attempts)
- **Status pages and incidents:**
  - `status_pages` (slug, custom domain, branding, visibility, auto incidents, subscriptions)
  - `status_page_components` (grouped, ordered: monitor or system)
  - `status_page_groups`
  - `incidents`
  - `incident_updates`
  - `incident_components`
  - `status_subscribers` (double opt-in, hashed tokens)
- **Settings:** `settings` (org-level key/value) and `user_preferences`.

**Hypertables (TimescaleDB):**
- `system_metrics` (time, system_id, cpu, cpu breakdown, mem, swap, load, disk io, net, temps jsonb, gpu jsonb, battery, …). Chunk interval 1 day. Compressed after 7 days, segmented by `system_id`.
- `disk_metrics`, `network_interface_metrics`, `sensor_metrics` and `gpu_metrics`: narrow tables for per-item series, **replacing v1's JSON blobs**, so averaging per item is correct by construction.
- `container_metrics` (time, system_id, container_id, cpu, mem, net rx/tx, …).
- `monitor_checks` (time, monitor_id, location, ok, response_ms, status_code, error_code):
  - **Stores every check.** TimescaleDB compression makes this affordable, replacing v1's `recent` JSON ring and the 60s aggregates.
  - Error text goes to a separate deduplicated table so the hypertable stays narrow.
- **Continuous aggregates:**
  - `*_10m`, `*_1h`, `*_1d` with avg, min, max and count for each metric.
  - Uptime per day per monitor, built from `monitor_checks` and status events.
- **Retention policies:**
  - Defaults: raw 30 days; 10m for 90 days; 1h for 1 year; 1d forever.
  - Configurable per organization.

**Conventions**
- UUIDv7 primary keys, generated in the database with PostgreSQL 18's built-in `uuidv7()` as the column default.
- Every tenant table has `org_id`, and every query is scoped by it. Row-level security is an optional defence-in-depth (Phase 10).
- `created_at` / `updated_at` everywhere. Soft delete only where auditing needs it.
- Secrets are sealed with AES-256-GCM:
  - The key comes from `INFRASCOPE_SECRET_KEY`, or a key file generated on first boot, via HKDF with a separate "info" label for each purpose.
  - Sealing uses envelope versioning (`enc:v1:`).

---

## 5. Security model

**Browser sessions**
- HttpOnly, Secure, SameSite=Lax cookie.
- CSRF double-submit token on state-changing requests.
- Session rotation at login.
- Absolute and idle timeouts.
- Rate limits on login and one-time-password endpoints.

**Accounts**
- Passwords hashed with argon2id, with a breach-list check (optional, k-anonymity).
- Two-factor login with TOTP and recovery codes. WebAuthn/passkeys in Phase 10.
- OIDC single sign-on with configurable group → role mapping.
- API tokens:
  - Prefixed (`isk_…`), shown once, stored hashed.
  - Scoped (read, write, admin) and bound to an org.

**Authorization**
- One policy layer in Go: `authz.Can(principal, action, resource)`.
- Called from every handler and job. Tests assert that every route has a check.

**Agent identity**
1. The hub runs an internal CA whose key is sealed in the database.
2. Enrolment: the agent presents a one-time token together with a CSR.
3. The hub issues a short-lived client certificate (30 days) that the agent renews automatically.
4. Revocation list checked at connect.
5. The agent pins the hub CA, which prevents hub impersonation (fixing v1's replay issue by design).

**Outbound requests from the hub** (webhooks, hub-side checks)
- One guarded dialer for webhooks and push endpoints that blocks private addresses.
- Hub-side checks may reach internal targets by policy (an org setting), but never return response bodies.

**Public surfaces** (status pages, badges, feeds, push-monitor URLs, subscriptions, ack links)
- Sanitized DTOs, with no ids or targets unless the page opts in.
- Rate limits.
- Links that change something always show a confirm page first (GET is safe, POST acts).

**Custom domains**
- A Host allowlist.
- Only the status-page surface is reachable, and auth headers are stripped.

**Supply chain**
- Signed releases (cosign or minisign) and SBOMs.
- Reproducible builds.
- Non-root containers for the hub. The agent container documents why it needs its capabilities.

**Audit log**
- Records auth events, membership changes, agent enrolment and revocation, channel, secret and status-page changes, and exports.

---

## 6. Cross-cutting requirements

**Performance targets for the reference box** (4 vCPU / 8 GB):
- 1,000 agents at a 60s interval, plus 5,000 monitors at a 60s interval.
- p95 API latency under 200 ms.
- Metric ingest under 50 ms per batch.
- Dashboard load under 1.5 s.

**Reliability**
- Agents spool metrics when disconnected.
- Hub restarts don't lose alert state, because it's persisted in `alert_states` and `monitor_status`.
- River retries notifications with backoff and a dead-letter queue.

**Observability of the hub itself**
- `/metrics` in Prometheus format: ingest rate, queue depth, gRPC streams, DB pool.
- Structured logs.
- `/healthz` and `/readyz`.

**Accessibility**
- WCAG 2.1 AA on core flows.
- Full keyboard navigation.
- Chart data available as tables.

**i18n**
- Every user-facing string goes through Lingui, with an English source catalog.
- Pseudo-locale in CI to catch hard-coded strings.

**Compatibility**
- Agent support: Linux (amd64, arm64, armv7), macOS, Windows, FreeBSD.
- Browsers: the last two versions of evergreen browsers.

**Documentation:** every feature ships with docs (user guide + API reference generated from OpenAPI).

---

## 7. Testing strategy

| Layer | Tooling | What |
|---|---|---|
| Go unit | `go test`, `-race` required in CI | engines (uptime/alert state machines), probes, crypto, authz policy |
| Go integration | testcontainers-go (TimescaleDB image) | migrations up/down, sqlc queries, continuous aggregates, retention, API handlers end-to-end |
| Agent | fake collectors + recorded fixtures per OS; gRPC in-process server | protocol, spool/replay, reconnect, config push |
| Contract | OpenAPI diff check in CI; `buf breaking` for proto | no accidental breaking changes |
| Web unit | Vitest + Testing Library | hooks, formatters, form schemas, components |
| E2E | Playwright against `docker compose` stack + a real agent container | enrol agent → metrics appear → alert fires → notification captured (Mailpit + webhook sink) |
| Load | k6 (API/SSE) + agent simulator (1k fake agents over gRPC) | performance targets in §6 |
| Security | gosec, govulncheck, `npm audit`/osv-scanner, ZAP baseline on public surfaces | per PR / nightly |

**CI gates:**
- lint (golangci-lint, Biome)
- typecheck
- unit tests
- integration tests
- e2e smoke
- contract checks
- vulnerability scan
- reproducible build check

---

## 8. Phases and checklists

Sizes: S ≈ 1 week, M ≈ 2–3 weeks, L ≈ 4–6 weeks (one full-stack engineer). Each phase ends with an **exit gate**.

### Phase 0 — Foundations and tooling (M)
- [ ] Create the monorepo with the layout in §3, a README, CONTRIBUTING and LICENSE (decide the license).
- [ ] Go workspace, pinned Go version, `golangci-lint` config, Makefile or Taskfile.
- [ ] `apps/web`:
  - [ ] Vite + React + TS (strict), Tailwind v4, shadcn init (theme tokens, dark mode)
  - [ ] TanStack Router, TanStack Query and Biome
  - [ ] Vitest and Playwright scaffolding
- [ ] `docker compose` dev stack:
  - [ ] TimescaleDB on PostgreSQL 18 (`timescale/timescaledb-ha:pg18`, pinned to an exact tag)
  - [ ] Mailpit (SMTP sink) and a webhook sink
  - [ ] hub with live reload (air) and Vite dev server with proxy
- [ ] Database tooling: goose migrations folder, sqlc config (pgx/v5, `engine: postgresql`), `make db-migrate`, `make db-reset`, a seed command.
- [ ] Pin PostgreSQL 18 across dev, CI (testcontainers) and the Compose bundle, and confirm the chosen TimescaleDB release supports PostgreSQL 18.
- [ ] Enable PostgreSQL 18's asynchronous I/O (`io_method = worker`, the default) in the Compose config, and document tuning for large installs.
- [ ] Code generation:
  - [ ] Huma OpenAPI export → `openapi.json` → Orval client and query hooks
  - [ ] `buf generate` for proto
  - [ ] `make gen`, with a CI check that fails when generated code is out of date
- [ ] Hub skeleton:
  - [ ] config loading (env and file) and slog logging
  - [ ] `/healthz` and `/readyz`, graceful shutdown
  - [ ] SPA embedding (with base path support at runtime)
- [ ] CI: lint, typecheck, unit, integration (testcontainers), build, SBOM. Release workflow stubbed.
- [ ] Architecture decision records (ADRs) for every decision in §0.
- [ ] **Exit gate:** `docker compose up` serves a "Hello InfraScope" SPA from the hub; `make gen` and CI are green.

### Phase 1 — Identity, orgs and access (L)
- [ ] Schema for users, organizations, memberships, invitations, sessions, API tokens, OIDC providers, TOTP, recovery codes and the audit log.
- [ ] First-run setup: create the owner and the first org (only when no users exist), plus a CLI fallback `infractl user create`.
- [ ] Email + password login:
  - [ ] argon2id hashing
  - [ ] rate limiting
  - [ ] lockout policy
  - [ ] password reset by email (signed, single-use, 1h tokens)
- [ ] Sessions: cookie, CSRF, rotation, list/revoke active sessions in the UI.
- [ ] TOTP two-factor: enrol with QR code, verify, recovery codes, and an admin-enforced "require 2FA" org policy.
- [ ] OIDC:
  - [ ] multiple providers
  - [ ] PKCE and nonce
  - [ ] just-in-time provisioning
  - [ ] group claim → role mapping
  - [ ] "SSO only" org option
- [ ] Organizations:
  - [ ] create, switch, rename, delete (grace period)
  - [ ] invite members by email with a role
  - [ ] remove members, transfer ownership
- [ ] Roles and policy: `owner`, `admin`, `member` and `viewer`, with a documented permission matrix. `authz` package, plus route-coverage tests.
- [ ] API tokens: create (scopes, expiry), list, revoke. Bearer auth on `/api/v1`.
- [ ] Audit log for auth and org events, with a UI to view and filter it.
- [ ] Web:
  - [ ] login, 2FA, reset and invite-accept pages
  - [ ] app shell with navbar, org switcher, user menu, theme toggle and command palette (cmdk)
  - [ ] settings: profile, security, sessions, tokens, org members
- [ ] **Exit gate:** Playwright covers first-run → invite a member → member logs in with TOTP → viewer can't reach admin actions; authz route coverage is 100%.

### Phase 2 — Agent protocol, enrolment and connectivity (L)
- [ ] Protobuf API `infrascope.agent.v1`:
  - [ ] `Enroll` (token + CSR → certificate + hub CA)
  - [ ] `RenewCertificate`
  - [ ] `Connect` bidirectional stream
- [ ] Message types:
  - [ ] Hello/capabilities (OS, arch, version, features) and Config (intervals, enabled collectors, monitors, discovery)
  - [ ] MetricBatch, CheckResults and InventoryDelta (containers, services, disks)
  - [ ] Request/Response pairs with correlation ids and deadlines; Ping
  - [ ] LiveSession start/stop
- [ ] Hub internal CA: key sealed in the DB, certificate issuing, automatic renewal before 2/3 of the lifetime, revocation, `infractl ca rotate`.
- [ ] Enrolment tokens: org-scoped, one-time or reusable with a limit, expiry, default labels. UI to create them and copy the install command.
- [ ] gRPC server:
  - [ ] dedicated port with mTLS
  - [ ] keepalives, max message size, per-agent stream registry
  - [ ] ownership check on every message (the agent cert maps to exactly one system)
- [ ] Agent link:
  - [ ] reconnect with jittered backoff
  - [ ] **bounded on-disk spool** (e.g. 24h or 200 MB) with replay after reconnect
  - [ ] clock-skew detection
  - [ ] clean shutdown
- [ ] Request handling on the agent: a worker pool per request type with timeouts. **Nothing blocks the stream reader** (a lesson from v1).
- [ ] Proxy support: documented configs for Caddy, nginx (`grpc_pass`) and Traefik (h2c/TLS passthrough); `HTTPS_PROXY` support in the agent.
- [ ] Systems registry: an enrolled agent creates or binds a system, status up/down from stream liveness, `down_reason`, and a system status events hypertable.
- [ ] Install artefacts:
  - [ ] install scripts (Linux, macOS, Windows, FreeBSD) that verify signatures
  - [ ] systemd, launchd and Windows service definitions
  - [ ] agent Docker image
- [ ] **Exit gate:** 1,000 simulated agents hold streams for 1h with stable memory; kill the hub for 10 minutes → spooled data replays on reconnect; a revoked cert is refused.

### Phase 3 — Metrics collection and storage (L)
- [ ] Port and redesign the agent collectors from v1 behind a common `Collector` interface, each with a per-interval baseline and capability detection:
  - [ ] CPU (total + breakdown: user/system/iowait/steal/…, per-core optional), load average, uptime
  - [ ] Memory: used, cached, buffers, ZFS ARC, swap
  - [ ] Filesystems (root + extra) and disk I/O (read/write bytes, ops, utilisation)
  - [ ] Network interfaces (rx/tx bytes, errors), Wi-Fi signal and link
  - [ ] Temperatures and fans (Linux hwmon, macOS SMC, Windows LibreHardwareMonitor, FreeBSD sysctl)
  - [ ] Battery
  - [ ] GPU: NVIDIA (NVML via purego → nvidia-smi fallback), AMD (sysfs + rocm-smi), Intel (sysfs / intel_gpu_top JSON), Apple (powermetrics), nvtop and Jetson tegrastats
  - [ ] mdraid, eMMC health, storage pools (ZFS/btrfs I/O)
- [ ] Timing: one 1-minute sampling window on the agent, sent as a single batch. Live mode (1s) runs as a separate pipeline with its own baselines.
- [ ] Schema:
  - [ ] hypertables for metrics, per-item tables (disk, net interface, sensor, GPU, container)
  - [ ] continuous aggregates 10m/1h/1d (avg/min/max/count) with refresh policies
  - [ ] compression and retention policies (org-configurable)
- [ ] Ingest: batched COPY or multi-row insert, idempotent (batch id), per-agent rate limiting, validation (drop out-of-range values), and backfill from spool replay.
- [ ] Query API:
  - [ ] `/systems/{id}/metrics?from&to&resolution=auto`, which picks the right aggregate
  - [ ] per-item series endpoints
  - [ ] downsampling to at most ~1,000 points
- [ ] Live mode:
  - [ ] SSE `live` topic with a session start/stop heartbeat
  - [ ] the hub tells the agent to stream at 1s while any viewer is watching (reference counted)
  - [ ] hard cap per org
- [ ] **Exit gate:** a week of simulated data queries under 150 ms at p95 for any range; rollup averages are correct for items that appear partway through a window (a test covering v1's bug); compression ratio at least 8×.

### Phase 4 — Web: dashboards and system pages (L)
- [ ] Design system:
  - [ ] shadcn theme tokens (light/dark, accent)
  - [ ] typography, spacing and chart palette tokens
  - [ ] empty, loading (skeleton) and error states
- [ ] Charts:
  - [ ] benchmark Recharts against uPlot; pick per use case
  - [ ] shared `<TimeSeriesChart>` (pinned axes, legend filter, tooltip, stacked or overlay, units: bytes/bits, °C/°F)
  - [ ] RTL-safe, with memoisation that tracks every input (a v1 lesson)
- [ ] Home / systems overview:
  - [ ] table and grid views with sparklines
  - [ ] filters (status, tags, OS), search, saved views
  - [ ] realtime status via SSE
- [ ] System page:
  - [ ] header (status, uptime, OS/kernel/CPU/agent version, down reason)
  - [ ] time-range picker (live, 1h, 12h, 24h, 7d, 30d, 1y, custom)
  - [ ] chart sections: CPU, memory, disk, disk I/O, network, temperatures, fans, GPU, battery, load, Wi-Fi, storage pools
- [ ] Live mode UI: a 1-minute rolling window at 1s. Pauses when the tab is hidden; the session is released on unmount.
- [ ] Units and user preferences: temperature, bytes vs bits, 12/24h clock, language.
- [ ] Lingui setup (extract/compile in CI, pseudo-locale test). Start translations with the v1 language list.
- [ ] Accessibility: axe checks in Playwright; chart "view as table".
- [ ] **Exit gate:** Lighthouse over 90 for performance and accessibility on the dashboard; the system page renders 30 days × 20 series without jank.

### Phase 5 — Containers, services, disks and packages (L)
- [ ] **Containers:**
  - [ ] Docker and Podman (socket, TCP, rootless)
  - [ ] inventory (image, status, health, ports, labels) and per-container metrics
  - [ ] container list page and a detail sheet with charts
  - [ ] on-demand logs with follow over SSE, capped, with ANSI → HTML via Shiki
  - [ ] inspect with Env, Cmd, Args and Entrypoint redacted
  - [ ] image update availability (registry digest checks with backoff that keeps the last good result)
  - [ ] include/exclude filters
- [ ] **Systemd services:** inventory, state/sub-state, CPU and memory per unit, details on demand (D-Bus with timeouts), and a failed-units view.
- [ ] **SMART:**
  - [ ] smartctl JSON: devices, attributes, health, self-test status
  - [ ] per-device and fleet pages
  - [ ] refresh on demand (async on the agent, timeouts)
- [ ] **ZFS / storage pools:** pools, vdev tree, datasets, scrub status and pool I/O; pool pages.
- [ ] **OS package updates:** apt, dnf, pacman, apk, zypper, brew and winget where possible. Counts and a detail list, with backoff when a check fails.
- [ ] **Exit gate:** end-to-end tests with fixture containers, services, disks and pools; the agent never blocks its stream during slow smartctl or D-Bus calls (tested with a fake 120s hang).

### Phase 6 — Alerting and notifications (L)
- [ ] **Alert rules model:**
  - [ ] scopes: system, tag or all systems in an org
  - [ ] metrics: CPU, memory, disk, temperature, bandwidth, GPU, load 1/5/15, battery, iowait, steal, container health, failed systemd units, SMART health, pool health, agent status
  - [ ] operator, threshold, "for" duration, severity, channels
- [ ] **Evaluator:**
  - [ ] a state machine per rule and target (ok → pending → firing → resolved), persisted in `alert_states`
  - [ ] runs on ingest, plus a periodic sweep for "no data"
  - [ ] **status alerts fire when the confirmed state changes, whatever came before (e.g. up → pending → down)**
- [ ] **Suppression:**
  - [ ] quiet hours (global and per target), maintenance windows (one-time and RRULE recurrence)
  - [ ] dependencies: a parent monitor or system that's down marks dependents "unreachable"
  - [ ] a transition held back by any of these notifies **once** after the hold ends, if it still applies
- [ ] **Alert events:**
  - [ ] history, acknowledge (with note), notes thread, reminders until acknowledged (per-user interval, capped)
  - [ ] ack links in notifications that open a confirm page and POST
- [ ] **Notification channels:**
  - [ ] email (SMTP settings per org)
  - [ ] Shoutrrr form builder for all 24 services in the v1 guide, with a raw URL fallback (port v1's parser, tests and examples)
  - [ ] Web Push (VAPID, RFC 8291; port v1's implementation and its test vectors)
  - [ ] generic webhook with an HMAC-signed body
  - [ ] Slack/Discord/Teams/Telegram presets via Shoutrrr
- [ ] **Routing and templates:**
  - [ ] severity routing (each channel's minimum severity plus default channels)
  - [ ] per-rule channel override
  - [ ] per-org and per-channel templates (Go `text/template`, restricted functions, size and time limits, live preview)
  - [ ] "critical bypasses quiet hours" option
- [ ] **Delivery:** River jobs with retries and backoff, a delivery log per attempt, a dead-letter view, and test sends.
- [ ] **Web UI:**
  - [ ] alert rules editor (bulk apply to tags)
  - [ ] active alerts banner and sheet
  - [ ] alert history (filters, CSV export)
  - [ ] channels, templates, quiet hours and maintenance pages
- [ ] **Exit gate:** a table-driven state machine test suite (retries, flapping, maintenance and dependency release, restart recovery); end-to-end: CPU spike → email in Mailpit + webhook + (mocked) push, ack link → reminders stop.

### Phase 7 — Uptime monitoring (L)
- [ ] **Shared probe engine** (`internal/shared/probe`, ported from v1 `netmon`):
  - [ ] HTTP (method, headers, body, basic auth, accepted codes, keyword/invert, JSON path, redirects, ignore TLS, timeout, certificate info)
  - [ ] TCP (+banner/TLS), ICMP, DNS (record type + expected value), SSH, PostgreSQL, MySQL, Redis, SMTP, IMAP, gRPC health
  - [ ] Minecraft, Source A2S, Docker container (agent-only), push/heartbeat
  - [ ] **blind by design:** fixed error strings, response bodies never returned
- [ ] **Locations:** hub runner (with a concurrency limit) and agent runners (config pushed over the stream). Multi-location with a quorum, per-location status, and a combined status.
- [ ] **Storage:**
  - [ ] every check in the `monitor_checks` hypertable, status segments, and uptime continuous aggregates (1h/1d)
  - [ ] 24h/7d/30d/90d uptime that leaves maintenance and unknown time out of the percentage
- [ ] **State machine:** retries with a retry interval, pending/down/up/unknown/paused/maintenance, and one notification per confirmed transition.
- [ ] **Alerts:** down/up, certificate expiry (N days), loss/latency thresholds; routing via Phase 6 channels.
- [ ] **Push monitors:**
  - [ ] Uptime Kuma-compatible URL `?status=&msg=&ping=`
  - [ ] per-token and per-IP limits
  - [ ] a missed-deadline failure for each missed interval
  - [ ] token regeneration
- [ ] **Docker label auto-discovery:**
  - [ ] `infrascope.monitor.*` and Traefik routers
  - [ ] opt-in per system
  - [ ] managed monitors with fields read-only while labels control them
  - [ ] removal 24h after the container disappears, with discovery errors shown on the system
- [ ] **Uptime Kuma import:** backup JSON → monitors, with a dry run that reports what would be created, adjusted or skipped.
- [ ] **UI:**
  - [ ] monitors page (status badges, recent-checks bar, uptime columns, locations)
  - [ ] create/edit dialog with per-type options and secrets kept when not visible
  - [ ] detail sheet (charts, per-location status, incidents list, 90-day bar)
  - [ ] bulk add
- [ ] **Exit gate:** each probe type is tested against in-process fake servers; 5,000 monitors at 60s held for 1h under the CPU/DB budget; quorum and flapping tests.

### Phase 8 — Status pages and incidents (M/L)
- [ ] **Status pages:**
  - [ ] slug, public/private, components (monitors and systems) in ordered collapsible groups
  - [ ] branding: logo (SVG sandboxed with CSP), accent colour, footer, hide "powered by"
  - [ ] show targets / response times toggles
  - [ ] 90-day bars and overall status
  - [ ] **public DTOs with no ids or targets unless opted in**
- [ ] **Custom domains:**
  - [ ] Host → page map; only the status-page surface is served on those hosts
  - [ ] documented TLS via a reverse proxy
  - [ ] optional ACME/HTTP-01 helper (Phase 10)
- [ ] **Badges and feeds:** SVG status and uptime badges (Shields-style) and Atom/RSS feeds, with caching tuned for CDNs and embeds.
- [ ] **Incidents:**
  - [ ] statuses (investigating → identified → monitoring → resolved), impact, affected components
  - [ ] a timeline of updates (markdown-lite, sanitized)
  - [ ] optional automatic incidents per page (open on down, resolve on recovery, deduplicated)
  - [ ] scheduled maintenance announcements
- [ ] **Subscriptions:**
  - [ ] email with double opt-in and confirm pages
  - [ ] List-Unsubscribe one-click
  - [ ] rate limits, honeypot, cap per page
  - [ ] incident and component emails (debounced)
  - [ ] optional webhook and RSS subscribers
- [ ] **UI:** status page editor (live preview), incidents management, and a public page (SSR-free SPA route, fast first paint, `noindex` option for private pages).
- [ ] **Exit gate:** a ZAP baseline scan on public routes shows no highs; no-leak tests on DTOs; a custom-domain host test matrix.

### Phase 9 — Operations, packaging and release (M)
- [ ] **Releases:**
  - [ ] GoReleaser for hub and agent (all OS/arch)
  - [ ] **signing:** cosign keyless or minisign, verified by `update` and the install scripts
  - [ ] SBOMs; Docker images for hub and agent on GHCR (multi-arch)
- [ ] **Self-update** for hub and agent: signature verified, rollback on a failed health check, org-controlled update channels (stable/beta).
- [ ] **Compose bundle:** `docker-compose.yml` (hub + TimescaleDB + optional Caddy), `.env` template, first-boot secret generation, healthchecks.
- [ ] **Backups:** documented `pg_dump`/`pg_basebackup` and TimescaleDB-aware restore, plus an `infractl backup` wrapper; restore test in CI.
- [ ] **Hub self-monitoring:**
  - [ ] `/metrics` (Prometheus)
  - [ ] outbound heartbeat to an external monitor (v1 feature)
  - [ ] a "system health" page in the UI
- [ ] **Admin tooling (`infractl`):** users, orgs, reset 2FA, rotate CA/secrets, revoke agent, re-run migrations, export/import configuration (YAML).
- [ ] **Docs site:** getting started, agent install per OS, reverse proxy guides (HTTP and gRPC), alerting, monitors, status pages, API reference, and security.
- [ ] **Exit gate:** a fresh VM → `docker compose up` → first agent enrolled in under 10 minutes by following the docs.

### Phase 10 — Hardening, scale-out and launch (M)
- [ ] Security review:
  - [ ] a threat model per surface (browser, agent link, public pages, outbound requests)
  - [ ] fix findings
  - [ ] a third-party pen test if budget allows
- [ ] Optional Postgres row-level security as defence in depth for `org_id`.
- [ ] Passkeys (WebAuthn) as a second factor or a passwordless login option.
- [ ] Multi-replica hub:
  - [ ] Postgres advisory locks for singleton loops
  - [ ] LISTEN/NOTIFY SSE fanout
  - [ ] agent streams spread across replicas
  - [ ] River for distributed jobs
- [ ] Load and soak tests at 2× targets; chaos tests (DB restart, network partitions to agents).
- [ ] Accessibility audit fixes, translation completeness pass, performance budget enforced in CI.
- [ ] Beta programme → 1.0 release checklist (below).

---

## 9. Feature parity checklist (v1 → v2)

| v1 feature | v2 phase |
|---|---|
| System metrics: CPU/mem/swap/disk/disk I/O/net/load/temps/fans/battery/GPU (NVIDIA, AMD, Intel, Apple, Jetson)/Wi-Fi/eMMC/mdraid | 3, 4 |
| Realtime 1s charts | 3, 4 |
| Containers (Docker/Podman) stats, logs, info, health, image updates | 5 |
| systemd services, SMART, ZFS / storage pools, package updates | 5 |
| System alerts (thresholds, status), quiet hours, alert history | 6 |
| Notification channels (email, Shoutrrr 24 services with form builder, browser push), severity routing, templates | 6 |
| Acknowledge, notes, reminders, signed ack links | 6 |
| Uptime monitors: HTTP (+options), TCP, ICMP, DNS, SSH, DBs, mail, gRPC, games, Docker, push | 7 |
| Hub-run + agent-run checks, multi-location quorum, retries, cert expiry, loss/latency thresholds | 7 |
| Maintenance windows, dependency-aware alerts | 6, 7 |
| Docker label auto-discovery, Uptime Kuma import | 7 |
| Status pages (groups, branding, custom domains, badges, feeds), systems on pages | 8 |
| Incidents (manual + auto), email subscriptions | 8 |
| Users/roles, OIDC, MFA, trusted-header/auto-login (→ replaced by OIDC + API tokens) | 1 |
| Universal tokens / fingerprints (→ replaced by enrolment tokens + mTLS certs) | 2 |
| Self-update, install scripts, heartbeat, config YAML export | 9 |
| i18n (30+ languages), PWA install, dark mode | 4, 9 |

## 10. Lessons from v1 turned into v2 requirements

- [ ] No unsynchronized shared state. Every shared struct has an owner goroutine or a documented lock, and `-race` must be clean in CI.
- [ ] Never run slow work on a connection's read loop. Use worker pools with deadlines.
- [ ] Rollups are computed per item (enforced by narrow per-item tables).
- [ ] Alert transitions are explicit state machines with persisted state, never "previous status == X" checks.
- [ ] One authorization layer, with a test proving every route calls it.
- [ ] GET requests never change anything (ack, confirm and unsubscribe all show a confirm page, then POST).
- [ ] Secrets are encrypted at rest from day one; client DTOs never echo secrets to non-owners.
- [ ] Public DTOs are built explicitly (allowlist fields), never by serializing records.
- [ ] Test fixtures never contain secret-looking strings (build them at runtime), so GitHub push protection doesn't block pushes.
- [ ] Generated code is checked for freshness in CI; typecheck with the real tsconfig (v1 checked nothing with `tsc -p .`).

## 11. Risks and mitigations

| Risk | Mitigation |
|---|---|
| gRPC through reverse proxies/CDNs is fiddly | Dedicated gRPC port with passthrough docs; a WebSocket-tunnelled fallback transport designed in Phase 2 as an ADR (build only if needed) |
| TimescaleDB support for PostgreSQL 18 lags new PostgreSQL releases | Pin a TimescaleDB release certified for PostgreSQL 18; test PostgreSQL minor-version upgrades in CI; don't move to a new major PostgreSQL version until TimescaleDB supports it |
| TimescaleDB licensing/features (Apache vs TSL) | Use the `-ha` image with TSL features for self-hosting; document that compression and continuous aggregates need the Community licence; plain-Postgres fallback isn't supported in v2.0 |
| Scope is large (parity is ~10 phases) | Ship a usable core after Phase 4 as an internal alpha; each later phase is a vertical slice that can ship on its own |
| Every-check storage volume | Compression plus a narrow schema and deduplicated error text; retention per org; load-tested in Phase 7 |
| Agent platform matrix (4 OSes, GPU vendors) | Capability detection, fixture-based tests per OS, community beta for rare hardware |
| OIDC/SSO edge cases | Use a certified library; test against Authentik, Keycloak, Google and Entra ID in CI (containers where possible) |

## 12. Definition of done for 1.0

- [ ] Every item in §9 is implemented, documented and covered by tests.
- [ ] The performance targets in §6 are met in the load-test report.
- [ ] No open high or critical security findings; release signing is in place.
- [ ] Install, upgrade and backup/restore are verified on a clean VM for Compose.
- [ ] Every UI string is translated in the English source catalog, and the pseudo-locale test passes.
- [ ] Docs site published; CHANGELOG and upgrade notes written.

## 13. Open questions (to confirm before or during Phase 0)

- [ ] Project license (MIT like v1, Apache-2.0 or AGPL).
- [ ] Charting library, after the Phase 4 benchmark (Recharts vs uPlot mix).
- [ ] Docs site generator (Starlight or VitePress).
- [ ] Whether a hosted/cloud edition is ever planned, since that affects billing, quotas and org limits.
- [ ] Default retention periods and per-org overrides UI.
- [ ] Whether agents may also run on Kubernetes as a DaemonSet in 1.0 (Helm chart timing).

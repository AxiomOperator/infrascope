# Development, CI and releases

## Local development

You need Go (version in `go.mod`; `GOTOOLCHAIN=auto` downloads it if your local Go is older) and Bun (or Node.js).

```bash
make dev-hub      # hub with live reload on :8090 (development build tag)
make dev-server   # Vite dev server for the web UI
make dev-agent    # agent
```

### Tests

```bash
GOTOOLCHAIN=auto go test -tags='testing no_ui' ./...            # hub, alerts, records, migrations, …
GOTOOLCHAIN=auto go test -tags=testing ./agent/... ./internal/netmon/...
cd internal/site && bun test                                     # web UI unit tests
cd internal/site && ./node_modules/.bin/tsc --noEmit -p tsconfig.app.json
```

Notes:

- **Agent tests need `-tags=testing`.** Without it, a guard test fails with a reminder.
- **Hub packages embed `internal/site/dist`.** With `-tags=no_ui` they compile without a web build. Otherwise run `make build-web-ui` first, or create `internal/site/dist/.placeholder`.
- **Type-check with `-p tsconfig.app.json`.** The root `tsconfig.json` has no files of its own, so `tsc -p .` checks nothing.

### Builds

| Command | Output |
|---|---|
| `make build-web-ui` | `internal/site/dist` (also runs `lingui extract`, which updates the `.po` translation files) |
| `make build-hub` | `./build/beszel_<os>_<arch>` |
| `make build-agent` | `./build/beszel-agent_<os>_<arch>` |
| `make build` | both |

## GitHub Actions

| Workflow | Trigger | What it does |
|---|---|---|
| **Build agent (linux-amd64)** (`agent-linux-amd64.yml`) | push to `main` or a pull request that changes agent code; manual run | Vets and tests the agent, then builds static and glibc (NVML) linux/amd64 agents. Uploads `beszel-agent_linux_amd64.tar.gz`, `beszel-agent_linux_amd64_glibc.tar.gz` and `checksums.txt` as an artifact kept for 30 days. |
| **Make release and binaries** (`release.yml`) | tag `v*` | GoReleaser builds every hub/agent platform, signs the archives and publishes a GitHub release. |
| **Make docker images** (`docker-images.yml`) | tag `v*` | Builds and pushes hub and agent images to `ghcr.io/axiomoperator/infrascope/…`. |
| **VulnCheck** (`vulncheck.yml`) | push to `main` | `govulncheck` on the Go code |
| **Helm charts** | chart changes | Lints and publishes the Helm charts |

To run the agent build by hand: **Actions → Build agent (linux-amd64) → Run workflow**, or `gh workflow run agent-linux-amd64.yml --repo AxiomOperator/infrascope`.

If the repository variable `INFRASCOPE_RELEASE_PUBLIC_KEY` is set, the CI agent embeds it. That lets CI builds verify signed self-updates later.

## Publishing a release

Releases must be signed, so set up signing once:

1. Generate a signing key pair:

   ```bash
   GOTOOLCHAIN=auto go run ./internal/ghupdate/signtool -generate
   ```

2. In **GitHub → Settings → Secrets and variables → Actions**, add:
   - **Secret** `INFRASCOPE_RELEASE_SIGNING_KEY`: the private key (keep a secure offline copy too).
   - **Variable** `INFRASCOPE_RELEASE_PUBLIC_KEY`: the public key.

3. Set `Version` in `beszel.go` (and `internal/site/package.json`), commit, then tag and push:

   ```bash
   git tag v0.21.0
   git push origin v0.21.0
   ```

The release workflow fails on purpose if the signing secrets are missing, because unsigned releases could not be installed by self-update.

**Docker Hub:** `docker-images.yml` still lists upstream `henrygd/*` Docker Hub image names. Those pushes are skipped without a Docker Hub secret. Change them to your own namespace before adding one.

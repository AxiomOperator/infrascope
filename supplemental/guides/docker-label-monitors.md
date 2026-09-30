# Monitors from Docker labels

InfraScope can create uptime monitors from the labels of the containers a system runs. Turn it on per system in the system's edit dialog: **Create monitors from Docker labels** (and optionally **Also monitor Traefik router hosts**). It requires agent 0.21.0 or newer with access to the Docker or Podman socket.

The agent reports the labels of running containers once a minute; containers hidden by `EXCLUDE_CONTAINERS` are ignored. The hub creates the monitors, updates them when labels change, disables them when the container disappears and deletes them 24 hours later. Monitors you created yourself are never touched.

## Labels

One monitor per container:

```yaml
labels:
  infrascope.monitor.type: http
  infrascope.monitor.name: My app
  infrascope.monitor.keyword: Welcome
```

Several monitors per container use an id of your choice (letters, digits, `-`, `_`):

```yaml
labels:
  infrascope.monitor.web.type: http
  infrascope.monitor.db.type: postgres
  infrascope.monitor.db.port: "5432"
```

| Field | Value |
| --- | --- |
| `type` | `http`, `tcp`, `icmp`, `dns`, `docker`, `ssh`, `postgres`, `mysql`, `redis`, `smtp`, `imap`, `grpc`, `minecraft`, `a2s`. Optional when `target` is an `http(s)://` URL. |
| `name` | Monitor name. Default: the container name (`<container>/<id>` for monitors with an id). |
| `target` | URL or host. See defaults below. |
| `port` | Port to check. A container port published to the host becomes its host port. |
| `interval` | Seconds (`60`) or a duration (`2m`). Default 60. |
| `retries` | 0 to 10. Default 1. |
| `timeout` | Seconds or a duration. Default: the protocol default. |
| `keyword` | http only: text the response must contain. |
| `accepted_codes` | http only: comma-separated codes or ranges, e.g. `200-299,301`. |
| `location` | `agent` (default): checked by the system's agent. `hub`: checked by the hub; the monitor belongs to the system's users. |
| `notify` | `true` or `false`. Default true. |

Switches:

- `infrascope.monitor.enable=false`: no monitors for this container.
- `infrascope.monitor.<id>.enable=false`: skip one monitor.
- `infrascope.monitor.traefik=false`: no monitors from this container's Traefik routers.

### Default targets

Without `target`:

- `docker`: the container itself.
- `http`: `http://<host>:<port>`.
- `tcp`, `ssh`, `postgres`, ... and `icmp`: `<host>`.

`<host>` is `localhost` for agent monitors (run the agent with host networking) and the system's host address for hub monitors. `<port>` is the `port` label or, without it, the container's lowest published TCP port.

## Traefik

With **Also monitor Traefik router hosts** on, every `Host(...)` of a `traefik.http.routers.<router>.rule` label becomes an http monitor, including a `Path`/`PathPrefix` of the rule. It uses https when the router has `tls=true`, TLS options or a `websecure`/`https` entrypoint.

## Editing and deleting

Discovered monitors show a **Docker** badge. Fields set by labels are read-only in the monitor dialog; other settings (notifications, thresholds, dependencies, ...) stay editable and are kept when labels change. A deleted monitor is recreated while its labels exist; use `infrascope.monitor.enable=false` instead.

Label sets that cannot be turned into a monitor are listed under **Discovery errors** in the system's edit dialog.

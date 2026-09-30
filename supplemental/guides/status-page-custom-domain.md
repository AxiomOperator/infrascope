# Serving a status page on a custom domain

A public status page can be served at the root of its own hostname, for
example `https://status.example.com/`, instead of `/status/<slug>` on the hub.

## Setup

1. Edit the status page (Settings > Status Pages), open **Branding and
   domain** and enter the hostname under **Custom domain**, e.g.
   `status.example.com`. Enter only the hostname: no `https://`, port or
   path. Each domain can be used by one page, and it must differ from the
   hub's own domain (`APP_URL`).
2. Create a DNS record (`A`/`AAAA` or `CNAME`) for the hostname that points
   to your reverse proxy.
3. Configure the reverse proxy to terminate TLS for the hostname and forward
   requests to the hub, **keeping the original `Host` header**. The hub picks
   the page by the `Host` header (case-insensitive, port ignored). Forward
   all paths to the hub root: the page loads `/assets/...`, `/static/...`
   and `/api/beszel/status-pages/<slug>/...` from the same host.

The hub does not obtain certificates itself. Use your proxy's automatic
HTTPS (Caddy, Traefik) or a certificate from your usual source.

### Caddy

Caddy keeps the `Host` header and obtains a certificate automatically:

```caddyfile
status.example.com {
	reverse_proxy localhost:8090
}
```

### nginx

```nginx
server {
	listen 443 ssl;
	server_name status.example.com;
	# ssl_certificate / ssl_certificate_key ...

	location / {
		proxy_pass http://127.0.0.1:8090;
		proxy_set_header Host $host;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;
	}
}
```

If the hub runs below a base path on its main domain (e.g. `APP_URL` is
`https://example.com/infrascope`), the custom domain must still forward to
the hub root, without that prefix.

## What is served on the custom domain

- `/` (and `/status/<slug>`) shows the status page. The page must be
  **public**; private pages are not shown there, not even to their owner.
- The page's public API: its data, logo, badges and feeds under
  `/api/beszel/status-pages/<slug>/`, and `/api/beszel/status-pages/by-host`,
  which returns `{"slug": "<slug>"}` for the requested host.
- Static assets (`/assets/`, `/static/`) and `/api/health`.

Everything else returns 404 on the custom domain: the login page and the rest
of the app, the PocketBase admin UI (`/_/`), `/api/collections/...`,
`/api/files/...` and all other API routes. Requests to the custom domain are
never authenticated (`Authorization` headers are dropped, and `AUTO_LOGIN` or
trusted header logins do not grant access to anything there).

Links in the incident feeds of the page point to the custom domain (with
`https://`) when the feed is requested there.

## Rate limits

Requests for the page, its logo, badges and feeds share the per-client-IP
limit of public status pages (60 requests per minute). If the hub sees the
proxy's address instead of the client's, configure the trusted proxy headers
as described for push monitors, so clients are limited separately.

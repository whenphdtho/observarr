# Observarr

Self-hosted ops dashboard for *arr and media-server stacks: finds the *arr apps you run (nothing is added without
your explicit approval), shows their health and queues live, and shows host and container figures. Status: early
development.

Licence: GPL-3.0. Writing an adapter: [`docs/adapters.md`](docs/adapters.md). Host and container figures
(the System view), with a socket proxy and host mounts: [`docs/deploy.md`](docs/deploy.md).

## Quick start

```sh
docker build -t observarr .
mkdir -p data && sudo chown 65532 data
docker run -d --name observarr -p 9898:9898 -v "$PWD/data:/data" -e OBSERVARR_INSECURE_COOKIES=1 observarr
docker logs observarr   # shows: setup token: <token> (also in data/setup-token)
```

Open `http://<host>:9898` and create the admin account, entering the setup token the log line (or the
`setup-token` file in the data folder) showed — it proves whoever is creating the account has access to the
host, not just to the page. The token and the file are both deleted once setup succeeds.

Logging in needs HTTPS, and Observarr itself serves plain HTTP only. Pick one: put a reverse proxy in front of
it over HTTPS and set `OBSERVARR_TRUSTED_PROXIES` to the proxy's address (see "Reverse proxy" below); or, on a
plain-HTTP LAN with no proxy at all, set `OBSERVARR_INSECURE_COOKIES=1` as above, which allows the login
cookie over plain HTTP. Drop `OBSERVARR_INSECURE_COOKIES` as soon as a proxy serves Observarr over HTTPS.

For the System view's host and container figures and Docker discovery, use
[`deploy/docker-compose.yml`](deploy/docker-compose.yml) instead — see [`docs/deploy.md`](docs/deploy.md). Its
data folder is `deploy/data`; `chown` that one the same way before the first start:
`mkdir -p deploy/data && sudo chown 65532 deploy/data`.

## Adding your apps

Add each app from the Apps view (directly, or via Discover). You'll need:

- **Its API key.** In Sonarr, Radarr, Lidarr and Prowlarr: Settings → General → Security → API Key.
- **A URL Observarr can reach:**
  - If Observarr and the app share a Docker network, use the app's **container name** as the host, e.g.
    `http://sonarr:8989` — see the compose snippet below for joining that network.
  - Otherwise, use the app's **LAN IP address and published port**, e.g. `http://192.168.1.20:8989`.
  - `127.0.0.1` / `localhost` is Observarr's own container — never the app you're adding.

Compose's own `default` network only connects containers started from the *same* compose file, so it won't
reach apps started by a different stack. Join their existing network instead:

```yaml
services:
  observarr:
    # ...
    networks:
      - default
      - media   # the *arr apps' own external network

networks:
  media:
    external: true
```

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `OBSERVARR_LISTEN` | `:9898` | listen address |
| `OBSERVARR_DATA` | `/data` | data directory — see "Data folder" below for what lives in it |
| `OBSERVARR_SECRET_KEY_FILE` | `<data>/secret.key` | path of the credential key that seals every saved API key, if you want it stored somewhere other than the data directory — can point at a Docker or Compose secret (mounted read-only, e.g. mode 0444; only a file others can *write* is refused) |
| `OBSERVARR_INSECURE_COOKIES` | unset | `1` allows login over plain HTTP (LAN without TLS, no reverse proxy); never set it behind a proxy |
| `OBSERVARR_RESET_DB` | unset | `1` moves a corrupt database aside and starts empty — use once to recover, then unset it again; setup then needs a fresh setup token |
| `OBSERVARR_RESET_KEYS` | unset | `1` drops every saved service API key when `secret.key` is missing, so they can be re-entered — use once, then unset it again |
| `OBSERVARR_TRUSTED_PROXIES` | empty | comma-separated IPs/CIDRs of a reverse proxy to trust `X-Forwarded-For` / `X-Forwarded-Proto` from — nothing else is read from a proxy unless it's listed here; use the proxy's exact address, not a whole Docker network |
| `OBSERVARR_PROXY_AUTH` | unset | `1` additionally trusts the proxy's user header for login (requires `OBSERVARR_TRUSTED_PROXIES`); **the proxy must strip or overwrite that header on every request it forwards**, and every user name it sends becomes a full Observarr admin — only enable this behind a proxy you trust completely. Login, setup and signed-in requests that do not come from a trusted proxy are then refused; still publish no port for Observarr (or only `127.0.0.1:9898:9898`), so that only the proxy can reach it |
| `OBSERVARR_PROXY_USER_HEADER` | `Remote-User` | header in which that proxy passes the user name (read only when `OBSERVARR_PROXY_AUTH=1`) |
| `OBSERVARR_APP_CONFIGS` | unset | folder holding one subfolder per app's config, mounted read-only, for the "config folders" discovery source — unset disables that source; if set, must be an existing directory at startup |
| `OBSERVARR_HOST_PROC` | unset | the host's `/proc` mounted read-only (e.g. `/host/proc`) for host CPU, memory, load and network figures; if set, must be an existing directory at startup |
| `OBSERVARR_HOST_CGROUP` | unset | the host's `/sys/fs/cgroup` mounted read-only (e.g. `/host/sys/fs/cgroup`) for container CPU, memory, I/O and network (needs cgroup v2, `OBSERVARR_HOST_PROC` and `cgroup: host` on the Observarr container); if set, must be an existing directory at startup |
| `OBSERVARR_HOST_DISKS` | empty | comma-separated paths inside the container whose disks are measured (e.g. `/host/root,/host/media`); each must exist at startup |
| `OBSERVARR_DOCKER_HOST` | unset | Docker socket proxy, `tcp://host:port` or `unix:///path`, for container state, health and events (read-only: Observarr only issues GET requests) |

The container runs as uid 65532; a bind-mounted data directory must be writable by it (e.g. `chown 65532 ./data`).
The data directory must support hard links (any local filesystem does, and so do Docker volumes).

`OBSERVARR_INSECURE_COOKIES`, `OBSERVARR_PROXY_AUTH`, `OBSERVARR_RESET_DB` and `OBSERVARR_RESET_KEYS` each
accept only `0` or `1` (unset is the same as `0`); anything else is a startup error.

### Data folder

Everything Observarr keeps lives under `OBSERVARR_DATA` (`/data` by default):

- `observarr.db`, `observarr.db-wal`, `observarr.db-shm` — the SQLite database (WAL mode keeps the latter two
  alongside it while running); the audit log lives inside it too, pruned to the newest 90 days or 100,000
  entries, whichever is hit first.
- `secret.key` — the credential key that seals every saved API key (`OBSERVARR_SECRET_KEY_FILE` above).
- `setup-token` — the one-time setup token; present only until setup completes.
- `observarr.lock` — held for as long as Observarr runs, so a second Observarr pointed at the same data
  folder exits immediately instead of sharing (and corrupting) the database.
- `backups/` — the newest 5 pre-upgrade database copies (see "Recovery" below).

### Discovering apps from their config folders

Discovery's "config folders" source reads an app's own config file to suggest it — including its API key, which
never leaves the server unless you click "Use the API key found in …" when adding it. Mount each app's config
directory read-only under one parent folder, one subfolder per app, and point `OBSERVARR_APP_CONFIGS` at that
parent:

```yaml
services:
  observarr:
    image: observarr   # docker build -t observarr . — see Quick start above
    environment:
      - OBSERVARR_APP_CONFIGS=/appconfig
    volumes:
      - ./data:/data
      - /srv/sonarr/config:/appconfig/sonarr:ro
      - /srv/radarr/config:/appconfig/radarr:ro
    ports:
      - "9898:9898"
```

The subfolder name (`sonarr`, `radarr`, …) only matters as a hint for apps whose own config file doesn't already
say which app it belongs to; either way, nothing is added until you review the suggestion and click Add.

### Host and container figures

The System view (CPU, memory, disks, network, and every container's state, health and usage) needs the host's
`/proc` and `/sys/fs/cgroup` mounted read-only and a Docker socket proxy — all optional, and all `OBSERVARR_HOST_*`
and `OBSERVARR_DOCKER_HOST` above. See [`docs/deploy.md`](docs/deploy.md) for a full compose example
(`deploy/docker-compose.yml`), what each source enables, and the security notes around the socket proxy and
host mounts.

## Reverse proxy

Any reverse proxy works; a minimal [Caddy](https://caddyserver.com/) example:

```
observarr.example.com {
    reverse_proxy observarr:9898
}
```

Caddy gets you HTTPS (it obtains and renews the certificate itself) and sends HSTS and other security headers —
that's the proxy's job, not Observarr's.

Behind a proxy, always set `OBSERVARR_TRUSTED_PROXIES` to the proxy's address and leave
`OBSERVARR_INSECURE_COOKIES` unset. Without the first, every request seems to come from the proxy: the login
lockout then counts everyone's failed logins together, so a few wrong passwords from anywhere lock everyone out,
and the audit log shows only the proxy's address. The second would let the login cookie travel over plain HTTP.

If the proxy also forwards a trusted login header (Authelia, Authentik, …), set `OBSERVARR_PROXY_AUTH=1` as well
(see "Configuration" above). Observarr then refuses login, setup and signed-in requests that do not come from the
proxy, but bind it only to the proxy's network anyway: in Compose, publish no port for Observarr (the proxy
reaches it over their shared network), or at most `127.0.0.1:9898:9898` when the proxy runs on the host.

The System view's live updates use Server-Sent Events, which need the response to reach the browser
unbuffered. Caddy's `reverse_proxy` already disables buffering by default; with nginx, add `proxy_buffering
off;` to the matching `location` block.

## Recovery

- **Corrupt database.** Observarr never resets it automatically. Startup exits with an error naming the file;
  restore it from a backup (below), or start once with `OBSERVARR_RESET_DB=1` to move the corrupt file aside and
  start empty — setup then needs a fresh setup token.
- **Missing `secret.key`.** If the key that seals every saved API key goes missing while services with saved
  keys still exist, Observarr refuses to start rather than silently losing track of them. Restore the key from
  backup, or start once with `OBSERVARR_RESET_KEYS=1` to drop the saved keys so you can re-enter them — the
  services themselves aren't lost, only their keys.
- **Backups** live under `<data>/backups`, written automatically before each database migration (the newest 5
  are kept). Back up that whole folder together with `secret.key` — a database backup without the matching key
  can't decrypt the services' saved API keys.
- **Monitoring.** `GET /api/health` returns 503 `{"status":"degraded","reason":"…"}` when the database can't be
  written (a read-only or full disk) — the container's own `HEALTHCHECK` already calls this (`observarr
  -health`); point any external monitoring at the same endpoint to catch the same problem.

## Development

`make ui` builds the web app, `make check` runs gofmt, vet and tests, `make build` builds the binary.
